package agentguard

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/harnesscfg"
)

// doctorContentFixture: a seamed managed layout plus a strazad stub, the
// managed-content check's test bed (mirrors newFetchFixture; the check
// takes a Config directly).
type doctorContentFixture struct {
	cfg     Config
	binPath string
	docs    map[string]harnesscfg.Document
}

func newDoctorContentFixture(t *testing.T) *doctorContentFixture {
	t.Helper()
	setOSName(t, "linux")
	sys := t.TempDir()
	t.Setenv("STRAZA_SYSTEM", sys)
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sys, "harness"))
	t.Setenv("STRAZA_MANAGED_BIN_DIR", filepath.Join(sys, "bin"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	f := &doctorContentFixture{binPath: filepath.Join(sys, "bin", "straza"), docs: map[string]harnesscfg.Document{}}
	old := vendorDefaultBinPath
	vendorDefaultBinPath = func(string) string { return f.binPath }
	t.Cleanup(func() { vendorDefaultBinPath = old })

	// ManagedInstalled: with the $STRAZA_SYSTEM seam a present config.yaml
	// counts as the managed layout (managed.go).
	if err := os.WriteFile(ManagedConfigPath(), []byte("serverUrl: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, ok := f.docs[r.URL.Query().Get("harness")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(ts.Close)

	pub := fetchTestKey().Public().(ed25519.PublicKey)
	f.cfg = Config{ServerURL: ts.URL, SnapshotKeys: map[string]string{
		fetchTestKID: base64.StdEncoding.EncodeToString(pub),
	}}
	return f
}

func (f *doctorContentFixture) serve(t *testing.T, harness string, contents map[string][]byte) {
	t.Helper()
	doc := harnesscfg.Document{Format: harnesscfg.Format, Kind: harnesscfg.Kind, Harness: harness, Platform: "linux"}
	for name, content := range contents {
		art, err := harnesscfg.SignArtifact(fetchTestKID, fetchTestKey(), harness, "linux", name, content)
		if err != nil {
			t.Fatal(err)
		}
		doc.Artifacts = append(doc.Artifacts, art)
	}
	f.docs[harness] = doc
}

func (f *doctorContentFixture) writeManaged(t *testing.T, harness string, content []byte) {
	t.Helper()
	path, err := managedHookPath(harness)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestManagedContentCheckVerdicts is the three-way verdict table plus the
// deleted-file case, which doctor must tell apart from never-installed.
func TestManagedContentCheckVerdicts(t *testing.T) {
	ctx := context.Background()

	t.Run("matches served artifact", func(t *testing.T) {
		f := newDoctorContentFixture(t)
		served := []byte(`{"hooks":{"newer":"server form"}}`)
		f.serve(t, "gemini", map[string][]byte{"hooks.gemini": served})
		f.writeManaged(t, "gemini", served)
		c := managedContentCheck(ctx, f.cfg)
		if c == nil || c.Status != checkOK || !strings.Contains(c.Detail, "server-published") {
			t.Fatalf("want ok server-published, got %+v", c)
		}
	})

	t.Run("matches local render only while server publishes newer", func(t *testing.T) {
		f := newDoctorContentFixture(t)
		f.serve(t, "gemini", map[string][]byte{"hooks.gemini": []byte(`{"newer":true}`)})
		local, err := RenderManagedArtifacts("gemini", "linux", f.binPath)
		if err != nil {
			t.Fatal(err)
		}
		f.writeManaged(t, "gemini", local["hooks.gemini"])
		c := managedContentCheck(ctx, f.cfg)
		if c == nil || c.Status != checkWarn || !strings.Contains(c.Detail, "stale or custom") {
			t.Fatalf("want warn stale, got %+v", c)
		}
	})

	t.Run("hand-edited fails online", func(t *testing.T) {
		f := newDoctorContentFixture(t)
		f.serve(t, "gemini", map[string][]byte{"hooks.gemini": []byte(`{"published":1}`)})
		f.writeManaged(t, "gemini", []byte(`{"hooks":{},"hand":"edited"}`))
		c := managedContentCheck(ctx, f.cfg)
		if c == nil || c.Status != checkFail || !strings.Contains(c.Detail, "NO legitimate render") {
			t.Fatalf("want fail no-render, got %+v", c)
		}
		if !strings.Contains(c.Hint, "install --managed") {
			t.Fatalf("hint must name the remedy, got %q", c.Hint)
		}
	})

	t.Run("custom binPath render still counts via wiring extraction", func(t *testing.T) {
		f := newDoctorContentFixture(t)
		f.serve(t, "gemini", map[string][]byte{"hooks.gemini": []byte(`{"published":1}`)})
		custom := filepath.Join(t.TempDir(), "elsewhere", "straza")
		arts, err := RenderManagedArtifacts("gemini", "linux", custom)
		if err != nil {
			t.Fatal(err)
		}
		f.writeManaged(t, "gemini", arts["hooks.gemini"])
		c := managedContentCheck(ctx, f.cfg)
		if c == nil || c.Status != checkWarn || !strings.Contains(c.Detail, "stale or custom") {
			t.Fatalf("want warn (custom layout render recognized), got %+v", c)
		}
	})

	t.Run("offline degrades honestly", func(t *testing.T) {
		f := newDoctorContentFixture(t)
		f.cfg.ServerURL = "http://127.0.0.1:1" // nothing listens
		local, err := RenderManagedArtifacts("gemini", "linux", f.binPath)
		if err != nil {
			t.Fatal(err)
		}
		f.writeManaged(t, "gemini", local["hooks.gemini"])
		c := managedContentCheck(ctx, f.cfg)
		if c == nil || c.Status != checkOK || !strings.Contains(c.Detail, "skipped") {
			t.Fatalf("want ok with skipped note, got %+v", c)
		}

		f.writeManaged(t, "gemini", []byte(`{"hand":"edited"}`))
		c = managedContentCheck(ctx, f.cfg)
		if c == nil || c.Status != checkWarn || !strings.Contains(c.Detail, "cannot verify offline") {
			t.Fatalf("offline unverifiable must WARN, not fail or pass: %+v", c)
		}
	})

	t.Run("deleted managed wiring under a governance claim fails", func(t *testing.T) {
		f := newDoctorContentFixture(t)
		_ = f
		user, err := SettingsPath("claude-code")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(user, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"command":"straza hook --harness claude-code"}]}]}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		c := managedContentCheck(ctx, f.cfg)
		if c == nil || c.Status != checkFail || !strings.Contains(c.Detail, "MISSING") {
			t.Fatalf("want fail missing-under-claim, got %+v", c)
		}
	})

	t.Run("nothing managed for a harness stays silent", func(t *testing.T) {
		f := newDoctorContentFixture(t)
		c := managedContentCheck(ctx, f.cfg)
		if c != nil {
			t.Fatalf("no managed wiring and no claims: want nil, got %+v", c)
		}
	})
}
