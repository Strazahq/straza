package agentguard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManagedSettingsPath(t *testing.T) {
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", "")
	for _, harness := range []string{"claude-code", "codex", "gemini"} {
		p, err := ManagedSettingsPath(harness)
		if err != nil || p == "" {
			t.Errorf("ManagedSettingsPath(%s) = %q, %v", harness, p, err)
		}
	}
	if _, err := ManagedSettingsPath("cursor"); err == nil {
		t.Error("unknown harness must error (Tier-1 only)")
	}

	dir := t.TempDir()
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", dir)
	p, err := ManagedSettingsPath("gemini")
	if err != nil || p != filepath.Join(dir, "gemini", "settings.json") {
		t.Errorf("override path = %q, %v", p, err)
	}
}

func TestLegacyManagedSettingsPath(t *testing.T) {
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", "")
	if runtime.GOOS == "windows" {
		if p, err := ManagedSettingsPath("claude-code"); err != nil || p != `C:\Program Files\ClaudeCode\managed-settings.json` {
			t.Errorf("windows pin = %q, %v (vendor moved off ProgramData at claude-code v2.1.75)", p, err)
		}
		if p := LegacyManagedSettingsPath("claude-code"); p != `C:\ProgramData\ClaudeCode\managed-settings.json` {
			t.Errorf("legacy windows path = %q", p)
		}
	} else if p := LegacyManagedSettingsPath("claude-code"); p != "" {
		t.Errorf("no retired path exists off-windows, got %q", p)
	}
	// claude-code MOVED its managed file, and codex never read a
	// managed-settings.json on any platform (its managed layer is
	// requirements.toml), though older straza versions wrote one. Both are
	// retired locations to clean up; gemini and an unknown harness have none.
	wantCodex := "/etc/codex/managed-settings.json"
	switch runtime.GOOS {
	case "windows":
		wantCodex = `C:\ProgramData\Codex\managed-settings.json`
	case "darwin":
		wantCodex = "/Library/Application Support/Codex/managed-settings.json"
	}
	if p := LegacyManagedSettingsPath("codex"); p != wantCodex {
		t.Errorf("LegacyManagedSettingsPath(codex) = %q, want %q", p, wantCodex)
	}
	if p, err := ManagedSettingsPath("codex"); err != nil || p == wantCodex {
		t.Errorf("ManagedSettingsPath(codex) = %q, %v; the current path must not be the retired one", p, err)
	}
	for _, h := range []string{"gemini", "cursor"} {
		if p := LegacyManagedSettingsPath(h); p != "" {
			t.Errorf("LegacyManagedSettingsPath(%s) = %q, want empty", h, p)
		}
	}
	dir := t.TempDir()
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", dir)
	if p := LegacyManagedSettingsPath("claude-code"); p != filepath.Join(dir, "claude-code", "legacy-managed-settings.json") {
		t.Errorf("seamed legacy path = %q", p)
	}
}

func TestMeasureAttestationUserMode(t *testing.T) {
	t.Setenv("STRAZA_SYSTEM", filepath.Join(t.TempDir(), "absent"))
	att := measureAttestation("claude-code")
	if att.Managed {
		t.Error("no managed layout must measure Managed=false")
	}
	if !strings.HasPrefix(att.Hashes["self"], "sha256:") {
		t.Errorf("self hash = %q, want sha256:*", att.Hashes["self"])
	}
	if att.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("platform = %q", att.Platform)
	}
	if _, ok := att.Hashes["config"]; ok {
		t.Error("user mode must not report a managed config measurement")
	}
}

// TestInstallManagedAndMeasure exercises the managed install end to end on a relocated
// system layout: managed config + binary + wiring written, measurements
// returned, and measureAttestation subsequently claiming a managed install
// whose hashes match the registered ones.
func TestInstallManagedAndMeasure(t *testing.T) {
	sysDir := t.TempDir()
	t.Setenv("STRAZA_SYSTEM", sysDir)
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sysDir, "harness"))
	t.Setenv("STRAZA_MANAGED_BIN_DIR", filepath.Join(sysDir, "bin"))
	t.Setenv("STRAZA_HOME", t.TempDir())

	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	server := snapshotKeyServer(t, map[string]string{"k1": "AAAA"})

	// Pre-place straza-authored wiring at the RETIRED location (the
	// pre-2.1.75 Windows path, seamed): vendor-side it is silently dead, so
	// install must migrate it away.
	legacy := LegacyManagedSettingsPath("claude-code")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"hooks":{"PreToolUse":[{"command":"straza hook"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := InstallManaged(context.Background(), ManagedInstallOptions{
		Harnesses: []string{"claude-code", "gemini"}, ServerURL: server,
	}, io.Discard); err != nil {
		t.Fatalf("InstallManaged: %v", err)
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("retired wiring at %s must be removed by install (stat err=%v)", legacy, err)
	}
	// A FOREIGN file at the retired path is never touched (re-run also pins
	// install idempotency).
	if err := os.WriteFile(legacy, []byte(`{"other":"config"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallManaged(context.Background(), ManagedInstallOptions{
		Harnesses: []string{"claude-code", "gemini"}, ServerURL: server,
	}, io.Discard); err != nil {
		t.Fatalf("InstallManaged re-run: %v", err)
	}
	if raw, err := os.ReadFile(legacy); err != nil || string(raw) != `{"other":"config"}` {
		t.Errorf("foreign file at retired path must survive install: %s, %v", raw, err)
	}
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}

	// The whole managed layout must be readable by every user on the box, not
	// just root: an unreadable file means other users' harnesses silently run
	// ungoverned AND measureAttestation cannot hash what it cannot open, so the
	// fleet reports att=none. Asserted end-to-end (not just at the writer)
	// because the call site matters too: managed install must never reach for
	// the user-scope writer.
	if runtime.GOOS != "windows" {
		for _, harness := range []string{"claude-code", "gemini"} {
			path, err := ManagedSettingsPath(harness)
			if err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode().Perm(); got != 0o644 {
				t.Errorf("managed wiring %s mode = %04o, want 0644 (world-readable)", path, got)
			}
		}
		if fi, err := os.Stat(ManagedConfigPath()); err != nil {
			t.Fatal(err)
		} else if got := fi.Mode().Perm(); got != 0o644 {
			t.Errorf("managed config mode = %04o, want 0644 (world-readable)", got)
		}
	}

	// The wiring invokes the managed binary copy, not the user one.
	wiring, err := ManagedSettingsPath("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(wiring)
	if err != nil {
		t.Fatalf("managed wiring not written: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	binPath := ManagedBinaryPath("")
	hooks, _ := settings["hooks"].(map[string]any)
	found := false
	if arr, ok := hooks["PreToolUse"].([]any); ok {
		for _, item := range arr {
			if hookHasCommand(item, hookCommand(binPath, "claude-code")) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("wiring %s does not invoke the managed binary %s", raw, binPath)
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Errorf("managed binary missing: %v", err)
	}

	// Managed MCP registration: claude-code's managed-mcp.json carries the
	// straza entry pointing at the MANAGED binary (not the running user-scope
	// one); gemini has no managed MCP layer, so nothing else appears.
	mcpPath, err := ManagedMCPConfigPath("claude-code")
	if err != nil || mcpPath == "" {
		t.Fatalf("ManagedMCPConfigPath = (%q, %v)", mcpPath, err)
	}
	mcpCfg, err := readJSONMap(mcpPath)
	if err != nil {
		t.Fatalf("managed MCP registration not written: %v", err)
	}
	straza, _ := mcpCfg["mcpServers"].(map[string]any)["straza"].(map[string]any)
	if straza == nil {
		t.Fatal("straza entry missing from managed-mcp.json")
	}
	if straza["command"] != binPath {
		t.Errorf("managed MCP entry command = %v, want the managed binary %s", straza["command"], binPath)
	}
	if p, err := ManagedMCPConfigPath("gemini"); err != nil || p != "" {
		t.Errorf("gemini managed MCP = (%q, %v), want none", p, err)
	}

	// The managed config shadows the user config (root-owned wins).
	cfg, err := store.LoadConfig()
	if err != nil || cfg.ServerURL != server {
		t.Fatalf("LoadConfig after managed install = %+v, %v", cfg, err)
	}

	// Measurement now claims managed and hashes the files install wrote.
	att := measureAttestation("claude-code")
	if !att.Managed {
		t.Fatal("managed layout must measure Managed=true")
	}
	for artifact, path := range map[string]string{"config": ManagedConfigPath(), "hooks.claude-code": wiring} {
		want, err := fileSHA256(path)
		if err != nil || att.Hashes[artifact] != want {
			t.Errorf("measured %s = %q, on disk %q (%v)", artifact, att.Hashes[artifact], want, err)
		}
	}

	// Tampering the wiring changes its measurement (the server then computes
	// att=none; asserted end-to-end in the demo test). Whitespace keeps the
	// JSON loadable so the uninstall below still parses it.
	if err := os.WriteFile(wiring, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := measureAttestation("claude-code").Hashes["hooks.claude-code"]; got == att.Hashes["hooks.claude-code"] {
		t.Error("tampered wiring must change the measured hash")
	}

	// UninstallManaged removes the Straza entries again: hooks, the managed
	// MCP registration, and any straza-authored retired-path leftover.
	if err := os.WriteFile(legacy, []byte(`{"hooks":{"note":"straza leftover"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UninstallManaged([]string{"claude-code", "gemini"}, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("retired wiring must be cleaned by uninstall (stat err=%v)", err)
	}
	raw, _ = os.ReadFile(wiring)
	if strings.Contains(string(raw), "straza") {
		t.Errorf("wiring still references straza after uninstall: %s", raw)
	}
	mcpCfg, err = readJSONMap(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mcpCfg["mcpServers"].(map[string]any)["straza"]; ok {
		t.Error("straza entry still in managed-mcp.json after uninstall")
	}
}

// TestManagedHookPathSharedResolver pins the one-resolver contract: install,
// uninstall, attestation, and doctor all resolve a harness's managed hook
// file through managedHookPath, and for codex that resolver follows
// CodexManagedRequirementsPath, including a relocated %ProgramData%, so no
// consumer can read a file install never wrote.
// This pin keeps attestation out of the same hole: a consumer
// quietly reverting to ManagedSettingsPath for codex fails here.
// snapshotKeyServer stands in for the strazad a managed install names: it
// serves keys at the snapshot-keys path and 404 everywhere else, so the
// install takes the local render lane.
func snapshotKeyServer(t *testing.T, keys map[string]string) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/straza/snapshot-keys.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var doc struct {
			Keys []map[string]string `json:"keys"`
		}
		for kid, key := range keys {
			doc.Keys = append(doc.Keys, map[string]string{"kid": kid, "key": key})
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// TestInstallManagedPinsOnlyTheNamedServer pins that the managed layout takes
// its server and keys from --server alone. A user's own config is writable by
// that user and any agent running as them, so it never decides what root
// pins for every user of the machine, and without --server nothing is written.
func TestInstallManagedPinsOnlyTheNamedServer(t *testing.T) {
	named := map[string]string{"k1": "TkFNRUQ="}
	tests := []struct {
		name       string
		server     bool
		wantErr    []string
		wantPinned bool
	}{
		{name: "no server is refused before anything is written", server: false,
			wantErr: []string{"`--server <server-url>`", "never from a user's own settings",
				"straza install --managed --server https://straza.example.com claude-code gemini"}},
		{name: "the named server wins over the user's config", server: true, wantPinned: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sysDir := t.TempDir()
			t.Setenv("STRAZA_SYSTEM", sysDir)
			t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sysDir, "harness"))
			t.Setenv("STRAZA_MANAGED_BIN_DIR", filepath.Join(sysDir, "bin"))
			t.Setenv("STRAZA_HOME", t.TempDir())
			store, err := OpenStore()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveConfig(Config{ServerURL: "http://rogue.example:8420",
				SnapshotKeys: map[string]string{"k1": "Uk9HVUU="}}); err != nil {
				t.Fatal(err)
			}
			opts := ManagedInstallOptions{Harnesses: []string{"claude-code", "gemini"}}
			if tt.server {
				opts.ServerURL = snapshotKeyServer(t, named)
			}

			err = InstallManaged(context.Background(), opts, io.Discard)
			for _, want := range tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to contain %q", err, want)
				}
			}
			if len(tt.wantErr) == 0 && err != nil {
				t.Fatalf("InstallManaged: %v", err)
			}
			if _, statErr := os.Stat(ManagedConfigPath()); tt.wantPinned != (statErr == nil) {
				t.Fatalf("managed config present = %v, want %v", statErr == nil, tt.wantPinned)
			}
			if !tt.wantPinned {
				return
			}
			cfg, err := store.LoadConfig()
			if err != nil || cfg.ServerURL != opts.ServerURL || cfg.SnapshotKeys["k1"] != named["k1"] {
				t.Fatalf("managed config = %+v, %v, want the named server %s and its key", cfg, err, opts.ServerURL)
			}
		})
	}
}

func TestManagedHookPathSharedResolver(t *testing.T) {
	t.Run("codex follows a relocated ProgramData like install does", func(t *testing.T) {
		setOSName(t, "windows")
		t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", "") // the test seam would mask the vendor-path branch
		relocated := t.TempDir()
		t.Setenv("ProgramData", relocated)
		got, err := managedHookPath("codex")
		if err != nil {
			t.Fatalf("managedHookPath(codex): %v", err)
		}
		want, err := CodexManagedRequirementsPath()
		if err != nil {
			t.Fatalf("CodexManagedRequirementsPath: %v", err)
		}
		if got != want {
			t.Errorf("managedHookPath(codex) = %q, want install's resolver %q", got, want)
		}
		if !strings.HasPrefix(got, relocated) {
			t.Errorf("managedHookPath(codex) = %q ignores the relocated ProgramData %q", got, relocated)
		}
		if vendor, _ := ManagedSettingsPath("codex"); got == vendor {
			t.Errorf("relocated box still resolved the vendor default %q: the exact divergence this pin exists to catch", vendor)
		}
	})
	t.Run("settings-dir seam keeps the resolvers coincident", func(t *testing.T) {
		t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
		got, err := managedHookPath("codex")
		if err != nil {
			t.Fatalf("managedHookPath(codex): %v", err)
		}
		want, _ := ManagedSettingsPath("codex")
		if got != want {
			t.Errorf("under the seam managedHookPath(codex) = %q, want %q", got, want)
		}
	})
	t.Run("non-codex resolves via ManagedSettingsPath", func(t *testing.T) {
		for _, harness := range []string{"claude-code", "gemini"} {
			got, err := managedHookPath(harness)
			want, werr := ManagedSettingsPath(harness)
			if err != nil || werr != nil || got != want {
				t.Errorf("managedHookPath(%s) = (%q, %v), want (%q, %v)", harness, got, err, want, werr)
			}
		}
	})
}

// TestAttestationMeasuresWhatInstallWrote mirrors doctor's
// TestWiredLayerReadsWhatInstallWrites for the attestation surface: the
// hooks.codex measurement must hash the exact file InstallCodexManagedHooks
// wrote at the shared resolver's path, never a path of attestation's own
// choosing. (The relocated-%ProgramData% branch itself is Windows-only and
// covered by construction plus the resolver pin above; under the seam the
// resolvers coincide, which is exactly what makes this assertable in CI.)
func TestAttestationMeasuresWhatInstallWrote(t *testing.T) {
	setOSName(t, "linux")
	sysDir := t.TempDir()
	t.Setenv("STRAZA_SYSTEM", sysDir)
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sysDir, "harness"))
	if err := os.MkdirAll(ManagedRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ManagedConfigPath(), []byte("serverUrl: https://managed.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := managedHookPath("codex")
	if err != nil {
		t.Fatalf("managedHookPath(codex): %v", err)
	}
	if _, err := InstallCodexManagedHooks(path, "/usr/local/bin/straza"); err != nil {
		t.Fatalf("InstallCodexManagedHooks: %v", err)
	}
	att := measureAttestation("codex")
	if !att.Managed {
		t.Fatal("managed layout present but Managed=false")
	}
	want, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := att.Hashes["hooks.codex"]; got != want {
		t.Errorf("hooks.codex = %q, want the hash of the file install wrote (%q at %s)", got, want, path)
	}
}

// TestInstallManagedEndsAtTheWiring pins the install's closing contract:
// in the default layout the person at the workstation gets the
// files it wrote and nothing about the registry, which the server fills by
// itself; only a local render, which the server never published, ends with
// the one line an administrator registers.
func TestInstallManagedEndsAtTheWiring(t *testing.T) {
	t.Run("served wiring prints no registry step", func(t *testing.T) {
		f := newFetchFixture(t, "linux")
		f.serve(t, "claude-code", "linux", map[string][]byte{"hooks.claude-code": []byte(`{"hooks":{"served":true}}`)})
		out, err := f.install(t, "claude-code")
		if err != nil {
			t.Fatalf("install: %v", err)
		}
		for _, banned := range []string{"strazactl attestation", "admin token", "--artifact self", "--artifact config"} {
			if strings.Contains(out, banned) {
				t.Errorf("install output still carries %q:\n%s", banned, out)
			}
		}
		if !strings.Contains(out, "server-published") {
			t.Errorf("served lane must say so:\n%s", out)
		}
	})
	t.Run("local render prints the one wiring line for an administrator", func(t *testing.T) {
		f := newFetchFixture(t, "linux")
		f.serve(t, "claude-code", "linux", map[string][]byte{"hooks.claude-code": []byte(`{"hooks":{"served":true}}`)})
		custom := filepath.Join(t.TempDir(), "custom-bin")
		var out strings.Builder
		if err := InstallManaged(context.Background(), ManagedInstallOptions{
			Harnesses: []string{"claude-code"}, ServerURL: f.url, BinDir: custom,
		}, &out); err != nil {
			t.Fatalf("install: %v", err)
		}
		text := out.String()
		if n := strings.Count(text, "strazactl attestation add --artifact hooks.claude-code --harness claude-code"); n != 1 {
			t.Errorf("want exactly one hooks registration line, got %d:\n%s", n, text)
		}
		if strings.Contains(text, "--artifact self") || strings.Contains(text, "--artifact config") {
			t.Errorf("binary and config must not be offered for registration:\n%s", text)
		}
	})
}
