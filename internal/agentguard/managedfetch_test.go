package agentguard

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/harnesscfg"
)

// fetchFixture is one seamed managed layout + a strazad stub serving signed
// harness-config documents, the whole fetch-lane test bed.
type fetchFixture struct {
	url     string // the stub strazad the install names with --server
	binPath string
	docs    map[string]harnesscfg.Document // keyed harness; missing = 404
	status  int                            // non-zero forces this HTTP status
}

const fetchTestKID = "fetch-test-key"

func fetchTestKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
}

// newFetchFixture seams the managed layout into temp dirs, points the vendor
// default at the fixture's bin path (so the default-layout precondition
// holds), and serves the snapshot key of the fixture signer.
func newFetchFixture(t *testing.T, goos string) *fetchFixture {
	t.Helper()
	setOSName(t, goos)
	sys := t.TempDir()
	t.Setenv("STRAZA_SYSTEM", sys)
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sys, "harness"))
	t.Setenv("STRAZA_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir()) // step 3c writes the invoker's config.toml
	binDir := filepath.Join(sys, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fetchFixture{binPath: filepath.Join(binDir, "straza"), docs: map[string]harnesscfg.Document{}}
	// The straza package's binary, which install --managed on Linux requires.
	if err := os.WriteFile(f.binPath, []byte("packaged straza"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := vendorDefaultBinPath
	vendorDefaultBinPath = func(string) string { return f.binPath }
	t.Cleanup(func() { vendorDefaultBinPath = old })

	pub := fetchTestKey().Public().(ed25519.PublicKey)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/straza/snapshot-keys.json" {
			_, _ = fmt.Fprintf(w, `{"keys":[{"kid":%q,"key":%q}]}`, fetchTestKID, base64.StdEncoding.EncodeToString(pub))
			return
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		doc, ok := f.docs[r.URL.Query().Get("harness")]
		if r.URL.Path != "/v1/harness-config" || !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(ts.Close)
	f.url = ts.URL
	return f
}

// serve signs arbitrary artifact contents for a harness and publishes them
// on the stub. Contents deliberately need not match any local render: the
// fetch lane's whole point is writing what the SERVER says (version skew).
func (f *fetchFixture) serve(t *testing.T, harness, goos string, contents map[string][]byte) {
	t.Helper()
	doc := harnesscfg.Document{Format: harnesscfg.Format, Kind: harnesscfg.Kind, Harness: harness, Platform: goos}
	for name, content := range contents {
		art, err := harnesscfg.SignArtifact(fetchTestKID, fetchTestKey(), harness, goos, name, content)
		if err != nil {
			t.Fatal(err)
		}
		doc.Artifacts = append(doc.Artifacts, art)
	}
	f.docs[harness] = doc
}

func (f *fetchFixture) install(t *testing.T, harness string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := InstallManaged(context.Background(), ManagedInstallOptions{
		Harnesses: []string{harness}, ServerURL: f.url, BinDir: filepath.Dir(f.binPath),
	}, &out)
	return out.String(), err
}

func readManaged(t *testing.T, harness string) []byte {
	t.Helper()
	path, err := ManagedHookPath(harness)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestInstallManagedFetchLane: a default-layout box writes the served bytes
// VERBATIM (both artifacts) and measures exactly them, even when they differ
// from what this binary would render (version skew is the point).
func TestInstallManagedFetchLane(t *testing.T) {
	f := newFetchFixture(t, "linux")
	hooks := []byte(`{"hooks":{"PreToolUse":"SERVER RENDERED NEWER FORM"}}`)
	mcp := []byte(`{"mcpServers":{"straza":{"command":"` + f.binPath + `","args":["newer"]}}}`)
	f.serve(t, "claude-code", "linux", map[string][]byte{
		"hooks.claude-code": hooks, "mcp.claude-code": mcp,
	})
	out, err := f.install(t, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readManaged(t, "claude-code"), hooks) {
		t.Fatal("managed settings are not the served bytes")
	}
	mcpPath, _ := ManagedMCPConfigPath("claude-code")
	got, err := os.ReadFile(mcpPath)
	if err != nil || !bytes.Equal(got, mcp) {
		t.Fatalf("managed mcp is not the served bytes: %v", err)
	}
	if !strings.Contains(out, "server-published") {
		t.Fatalf("output does not name the lane:\n%s", out)
	}
}

// TestInstallManagedRefusesBadCrypto: a served document that fails
// verification ABORTS the install with nothing written. Never a silent
// fallback: bad crypto is evidence, not inconvenience.
func TestInstallManagedRefusesBadCrypto(t *testing.T) {
	f := newFetchFixture(t, "linux")
	f.serve(t, "claude-code", "linux", map[string][]byte{"hooks.claude-code": []byte("{}")})
	doc := f.docs["claude-code"]
	doc.Artifacts[0].Sig[3] ^= 0xff
	f.docs["claude-code"] = doc

	_, err := f.install(t, "claude-code")
	if err == nil || !strings.Contains(err.Error(), "verif") {
		t.Fatalf("want verification refusal, got %v", err)
	}
	path, _ := ManagedHookPath("claude-code")
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("refusal must leave no wiring behind")
	}
}

// TestInstallManagedRefusesUnknownArtifact: the server never names paths;
// an artifact name outside the client's fixed map is refused outright, so a
// future or compromised server cannot steer a write anywhere new.
func TestInstallManagedRefusesUnknownArtifact(t *testing.T) {
	f := newFetchFixture(t, "linux")
	f.serve(t, "claude-code", "linux", map[string][]byte{
		"hooks.claude-code": []byte("{}"), "boot.claude-code": []byte("evil"),
	})
	_, err := f.install(t, "claude-code")
	if err == nil || !strings.Contains(err.Error(), "boot.claude-code") {
		t.Fatalf("want unknown-artifact refusal naming it, got %v", err)
	}
	path, _ := ManagedHookPath("claude-code")
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("refusal must leave no wiring behind")
	}
}

// TestInstallManagedLocalLaneFallbacks: benign conditions take the LOCAL
// lane (today's render), each with a printed reason: no published artifact
// (pre-0.54.0 server / 404), and operator content in the managed file,
// which is preserved (the codex lockdown line is content we RECOMMEND).
func TestInstallManagedLocalLaneFallbacks(t *testing.T) {
	t.Run("no published artifact", func(t *testing.T) {
		f := newFetchFixture(t, "linux") // nothing served: 404
		out, err := f.install(t, "gemini")
		if err != nil {
			t.Fatal(err)
		}
		want, err := RenderManagedArtifacts("gemini", "linux", f.binPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readManaged(t, "gemini"), want["hooks.gemini"]) {
			t.Fatal("local lane did not write this build's render")
		}
		if !strings.Contains(out, "local render") {
			t.Fatalf("output does not explain the lane:\n%s", out)
		}
	})

	t.Run("operator content preserved", func(t *testing.T) {
		f := newFetchFixture(t, "linux")
		f.serve(t, "codex", "linux", map[string][]byte{"hooks.codex": []byte("# server block only\n")})
		path, err := ManagedHookPath("codex")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		lockdown := CodexManagedLockdownLine + "\n"
		if err := os.WriteFile(path, []byte(lockdown), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := f.install(t, "codex")
		if err != nil {
			t.Fatal(err)
		}
		got := string(readManaged(t, "codex"))
		if !strings.Contains(got, CodexManagedLockdownLine) {
			t.Fatal("operator lockdown line was dropped")
		}
		if !strings.Contains(got, codexReqBeginPrefix) {
			t.Fatal("straza block missing after merge")
		}
		if !strings.Contains(out, "operator content") {
			t.Fatalf("output does not explain the lane:\n%s", out)
		}
	})

	t.Run("custom bin dir", func(t *testing.T) {
		f := newFetchFixture(t, "linux")
		f.serve(t, "gemini", "linux", map[string][]byte{"hooks.gemini": []byte(`{"served":true}`)})
		custom := filepath.Join(t.TempDir(), "elsewhere")
		placeManagedBinary(t, custom)
		var out strings.Builder
		err := InstallManaged(context.Background(), ManagedInstallOptions{
			Harnesses: []string{"gemini"}, ServerURL: f.url, BinDir: custom,
		}, &out)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(readManaged(t, "gemini"), []byte("served")) {
			t.Fatal("custom layout must not take the server lane")
		}
		if !strings.Contains(out.String(), "custom binary path") {
			t.Fatalf("output does not explain the lane:\n%s", out.String())
		}
	})
}

// TestInstallManagedFetchLaneIdempotent: a second run over served bytes
// changes nothing and still succeeds.
func TestInstallManagedFetchLaneIdempotent(t *testing.T) {
	f := newFetchFixture(t, "linux")
	f.serve(t, "gemini", "linux", map[string][]byte{"hooks.gemini": []byte(`{"hooks":{"x":1}}`)})
	if _, err := f.install(t, "gemini"); err != nil {
		t.Fatal(err)
	}
	first := readManaged(t, "gemini")
	if _, err := f.install(t, "gemini"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, readManaged(t, "gemini")) {
		t.Fatal("second run changed the file")
	}
}

// TestServedRunsElsewhere pins the check that guards a server older than
// this client: served wiring whose hook commands, or whose straza MCP entry,
// run another path than this machine's managed binary would never run. The
// Windows codex shim is judged by its path alone, and no file it names is read.
func TestServedRunsElsewhere(t *testing.T) {
	const old, current = "/usr/local/bin/straza", "/usr/bin/straza"
	const winOld, winCurrent = `C:\Old\bin\straza.exe`, `C:\ProgramData\straza\bin\straza.exe`
	render := func(harness, goos, binPath string) map[string][]byte {
		t.Helper()
		arts, err := RenderManagedArtifacts(harness, goos, binPath)
		if err != nil {
			t.Fatal(err)
		}
		return arts
	}
	mcpOld := render("claude-code", "linux", current)
	mcpOld["mcp.claude-code"] = render("claude-code", "linux", old)["mcp.claude-code"]
	tests := []struct {
		name    string
		harness string
		arts    map[string][]byte
		binPath string
		want    string
	}{
		{"claude-code rendered for the old path refuses", "claude-code", render("claude-code", "linux", old), current, old},
		{"claude-code rendered for this binary passes", "claude-code", render("claude-code", "linux", current), current, ""},
		{"gemini rendered for the old path refuses", "gemini", render("gemini", "linux", old), current, old},
		{"gemini rendered for this binary passes", "gemini", render("gemini", "linux", current), current, ""},
		{"codex rendered for the old path refuses", "codex", render("codex", "linux", old), current, old},
		{"codex rendered for this binary passes", "codex", render("codex", "linux", current), current, ""},
		{"the claude-code mcp entry counts as a hook command", "claude-code", mcpOld, current, old},
		{"windows codex shim beside another binary refuses", "codex", render("codex", "windows", winOld), winCurrent,
			codexShimPathFor("windows", winOld)},
		{"windows codex shim beside this binary passes", "codex", render("codex", "windows", winCurrent), winCurrent, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := &harnesscfg.Document{Harness: tt.harness}
			for name, content := range tt.arts {
				doc.Artifacts = append(doc.Artifacts, harnesscfg.Artifact{Name: name, Content: content})
			}
			if got := servedRunsElsewhere(tt.harness, doc, tt.binPath); got != tt.want {
				t.Errorf("servedRunsElsewhere = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestInstallManagedRefusesWiringForAnotherBinary: a server that renders
// hooks for another path than this machine's binary is refused with the
// sentence that names both paths, and nothing is written at all, the
// managed config included.
func TestInstallManagedRefusesWiringForAnotherBinary(t *testing.T) {
	f := newFetchFixture(t, "linux")
	arts, err := RenderManagedArtifacts("claude-code", "linux", "/usr/local/bin/straza")
	if err != nil {
		t.Fatal(err)
	}
	f.serve(t, "claude-code", "linux", arts)
	_, err = f.install(t, "claude-code")
	want := "The Straza server at " + f.url + " renders claude-code hooks that run /usr/local/bin/straza, " +
		"but this machine's Straza is " + f.binPath + ", so those hooks would never run. " +
		"Upgrade the Straza server first, then run `straza install --managed` again."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	hooks, _ := ManagedHookPath("claude-code")
	mcp, _ := ManagedMCPConfigPath("claude-code")
	for _, p := range []string{ManagedConfigPath(), hooks, mcp} {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Errorf("refusal wrote %s (stat err=%v), want nothing written", p, statErr)
		}
	}
}
