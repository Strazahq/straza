package agentguard

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestManagedBinDir pins where the managed binary lives on each OS. On Linux
// the straza package installs it at /usr/bin, because distribution packages
// never write under /usr/local, and on macOS the Straza package installs it
// at /opt/straza/bin, which only root can change. The served default render
// must name the same path, or the default layout would never take the
// server lane.
func TestManagedBinDir(t *testing.T) {
	tests := []struct {
		goos, env, want string
	}{
		{goos: "linux", want: "/usr/bin"},
		{goos: "darwin", want: "/opt/straza/bin"},
		{goos: "linux", env: "/opt/straza/bin", want: "/opt/straza/bin"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+tt.env, func(t *testing.T) {
			setOSName(t, tt.goos)
			t.Setenv("STRAZA_MANAGED_BIN_DIR", tt.env)
			if got := ManagedBinDir(); got != tt.want {
				t.Fatalf("ManagedBinDir() = %q, want %q", got, tt.want)
			}
			if got, want := ManagedBinaryPath(""), filepath.Join(tt.want, "straza"); got != want {
				t.Errorf("ManagedBinaryPath(\"\") = %q, want %q", got, want)
			}
			if tt.env == "" && defaultManagedBinaryPath(tt.goos) != tt.want+"/straza" {
				t.Errorf("defaultManagedBinaryPath(%s) = %q, want %q", tt.goos, defaultManagedBinaryPath(tt.goos), tt.want+"/straza")
			}
		})
	}
}

// TestLockManagedRootExported pins the step the Straza MSI runs through `straza
// installer lock`: it creates a missing managed root, and it takes a root
// that already holds the binary as it is. With the STRAZA_SYSTEM seam set,
// locking it to administrators is left out, as in install.
func TestLockManagedRootExported(t *testing.T) {
	for _, row := range []struct {
		name   string
		placed bool
	}{
		{"a missing root is created", false},
		{"a root that holds the binary is kept", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "straza")
			t.Setenv("STRAZA_SYSTEM", root)
			var bin string
			if row.placed {
				bin = placeManagedBinary(t, filepath.Join(root, "bin"))
			}
			if err := LockManagedRoot(); err != nil {
				t.Fatalf("LockManagedRoot: %v", err)
			}
			if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
				t.Fatalf("managed root after the lock: %v", err)
			}
			if row.placed {
				if raw, err := os.ReadFile(bin); err != nil || string(raw) != "packaged straza" {
					t.Errorf("binary after the lock = %q, %v; want it untouched", raw, err)
				}
			}
		})
	}
}

// placeManagedBinary stands in for the straza package: it puts a binary at
// ManagedBinaryPath(dir), which install --managed on Linux requires and never
// writes. It returns the path.
func placeManagedBinary(t *testing.T, dir string) string {
	t.Helper()
	path := ManagedBinaryPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("packaged straza"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestInstallManagedBinaryByOS pins who puts the managed binary in place. On
// Linux the straza package installs it, on Windows the Straza MSI and on
// macOS the Straza package for macOS, so install never writes it and
// refuses before writing anything when the binary is missing, naming the
// package or the MSI only for the default path it installs. On macOS a copy
// written over the signed binary in place would crash it.
func TestInstallManagedBinaryByOS(t *testing.T) {
	const (
		packageMissing = "`straza install --managed` found no %s, which the Straza package installs. " +
			"Install the straza package for this distribution first, then run this command again."
		customMissing = "`straza install --managed` found no straza binary at %s. " +
			"Put the straza binary there, or leave out --bin-dir and STRAZA_MANAGED_BIN_DIR to use /usr/bin/straza from the straza package, then run this command again."
		msiMissing = "`straza install --managed` found no %s, which the Straza MSI installs. " +
			"Install the Straza MSI for Windows first, then run this command again."
		customMissingWindows = "`straza install --managed` found no straza.exe at %s. " +
			`Put straza.exe there, or leave out --bin-dir and STRAZA_MANAGED_BIN_DIR to use C:\ProgramData\straza\bin\straza.exe from the Straza MSI, then run this command again.`
		pkgMissing = "`straza install --managed` found no %s, which the Straza package for macOS installs. " +
			"Install the Straza package first, then run this command again."
		customMissingDarwin = "`straza install --managed` found no straza binary at %s. " +
			"Put the straza binary there, or leave out --bin-dir and STRAZA_MANAGED_BIN_DIR to use /opt/straza/bin/straza from the Straza package for macOS, then run this command again."
	)
	tests := []struct {
		name        string
		goos        string
		defaultPath bool // binPath is the vendor default the package installs
		placed      bool
		wantErr     string // a format for binPath; "" means install succeeds
	}{
		{name: "linux default path without the package refuses", goos: "linux", defaultPath: true, wantErr: packageMissing},
		{name: "linux custom path without a binary refuses", goos: "linux", wantErr: customMissing},
		{name: "linux keeps the packaged binary", goos: "linux", defaultPath: true, placed: true},
		{name: "linux keeps a binary at a custom path", goos: "linux", placed: true},
		{name: "windows default path without the MSI refuses", goos: "windows", defaultPath: true, wantErr: msiMissing},
		{name: "windows custom path without a binary refuses", goos: "windows", wantErr: customMissingWindows},
		{name: "windows keeps the binary the MSI installed", goos: "windows", defaultPath: true, placed: true},
		{name: "windows keeps a binary at a custom path", goos: "windows", placed: true},
		{name: "darwin default path without the macOS package refuses", goos: "darwin", defaultPath: true, wantErr: pkgMissing},
		{name: "darwin custom path without a binary refuses", goos: "darwin", wantErr: customMissingDarwin},
		{name: "darwin keeps the binary the macOS package installed", goos: "darwin", defaultPath: true, placed: true},
		{name: "darwin keeps a binary at a custom path", goos: "darwin", placed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setOSName(t, tt.goos)
			sysDir := t.TempDir()
			t.Setenv("STRAZA_SYSTEM", sysDir)
			t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sysDir, "harness"))
			t.Setenv("STRAZA_MANAGED_BIN_DIR", filepath.Join(sysDir, "bin"))
			t.Setenv("STRAZA_HOME", t.TempDir())
			binPath := ManagedBinaryPath("")
			if tt.defaultPath {
				old := vendorDefaultBinPath
				vendorDefaultBinPath = func(string) string { return binPath }
				t.Cleanup(func() { vendorDefaultBinPath = old })
			}
			if tt.placed {
				placeManagedBinary(t, "")
			}
			wiring, err := ManagedHookPath("gemini")
			if err != nil {
				t.Fatal(err)
			}

			err = InstallManaged(context.Background(), ManagedInstallOptions{
				Harnesses: []string{"gemini"}, ServerURL: snapshotKeyServer(t, map[string]string{"k1": "AAAA"}),
			}, io.Discard)
			if tt.wantErr != "" {
				want := fmt.Sprintf(tt.wantErr, binPath)
				if err == nil || err.Error() != want {
					t.Fatalf("err = %v, want %q", err, want)
				}
				for _, p := range []string{ManagedConfigPath(), wiring, binPath} {
					if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
						t.Errorf("refusal wrote %s (stat err=%v), want nothing written", p, statErr)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("InstallManaged: %v", err)
			}
			if raw, err := os.ReadFile(binPath); err != nil || string(raw) != "packaged straza" {
				t.Errorf("managed binary after install = %q, %v; want the one the package or the MSI installed, untouched", raw, err)
			}
		})
	}
}

// TestInstallManaged exercises the managed install end to end on a
// relocated system layout: managed config + wiring written beside the managed
// binary, and the managed config shadowing the user's. The Straza background service
// measures what install wrote; its own tests pin that measurement.
func TestInstallManaged(t *testing.T) {
	sysDir := t.TempDir()
	t.Setenv("STRAZA_SYSTEM", sysDir)
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sysDir, "harness"))
	t.Setenv("STRAZA_MANAGED_BIN_DIR", filepath.Join(sysDir, "bin"))
	t.Setenv("STRAZA_HOME", t.TempDir())
	placeManagedBinary(t, "")

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
	// ungoverned AND the service's attestation cannot hash what it cannot open, so the
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
// file through ManagedHookPath, and for codex that resolver follows
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
			placeManagedBinary(t, "")
			store, err := OpenStore()
			if err != nil {
				t.Fatal(err)
			}
			writeUserConfig(t, store, Config{ServerURL: "http://rogue.example:8420",
				SnapshotKeys: map[string]string{"k1": "Uk9HVUU="}})
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
		got, err := ManagedHookPath("codex")
		if err != nil {
			t.Fatalf("ManagedHookPath(codex): %v", err)
		}
		want, err := CodexManagedRequirementsPath()
		if err != nil {
			t.Fatalf("CodexManagedRequirementsPath: %v", err)
		}
		if got != want {
			t.Errorf("ManagedHookPath(codex) = %q, want install's resolver %q", got, want)
		}
		if !strings.HasPrefix(got, relocated) {
			t.Errorf("ManagedHookPath(codex) = %q ignores the relocated ProgramData %q", got, relocated)
		}
		if vendor, _ := ManagedSettingsPath("codex"); got == vendor {
			t.Errorf("relocated box still resolved the vendor default %q: the exact divergence this pin exists to catch", vendor)
		}
	})
	t.Run("settings-dir seam keeps the resolvers coincident", func(t *testing.T) {
		t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
		got, err := ManagedHookPath("codex")
		if err != nil {
			t.Fatalf("ManagedHookPath(codex): %v", err)
		}
		want, _ := ManagedSettingsPath("codex")
		if got != want {
			t.Errorf("under the seam ManagedHookPath(codex) = %q, want %q", got, want)
		}
	})
	t.Run("non-codex resolves via ManagedSettingsPath", func(t *testing.T) {
		for _, harness := range []string{"claude-code", "gemini"} {
			got, err := ManagedHookPath(harness)
			want, werr := ManagedSettingsPath(harness)
			if err != nil || werr != nil || got != want {
				t.Errorf("ManagedHookPath(%s) = (%q, %v), want (%q, %v)", harness, got, err, want, werr)
			}
		}
	})
}

// TestAttestationMeasuresWhatInstallWrote mirrors doctor's
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
		placeManagedBinary(t, custom)
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
