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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
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
	path, err := ManagedHookPath(harness)
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
		c := managedContentCheck(ctx, f.cfg, nil)
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
		c := managedContentCheck(ctx, f.cfg, nil)
		if c == nil || c.Status != checkWarn || !strings.Contains(c.Detail, "stale or custom") {
			t.Fatalf("want warn stale, got %+v", c)
		}
	})

	// An older Straza's render, which strazad keeps registered after an
	// upgrade, and a hand edit look the same from this machine: neither
	// matches the served wiring or this build's render. Doctor says both
	// and claims no attestation outcome it cannot know.
	t.Run("matches neither render online", func(t *testing.T) {
		f := newDoctorContentFixture(t)
		f.serve(t, "gemini", map[string][]byte{"hooks.gemini": []byte(`{"published":1}`)})
		f.writeManaged(t, "gemini", []byte(`{"hooks":{},"older":"render"}`))
		c := managedContentCheck(ctx, f.cfg, nil)
		want := "gemini: managed wiring matches neither the wiring the server serves now nor this build's render, " +
			"so an older Straza install wrote it or it was edited"
		if c == nil || c.Status != checkWarn || c.Detail != want {
			t.Fatalf("got %+v, want warn %q", c, want)
		}
		wantHint := "an administrator runs elevated `straza install --managed --server <server-url> <harness>` again to take the wiring " +
			"the server serves now (custom layouts: pass your --bin-dir). Older wiring keeps attesting managed while strazad keeps its hash " +
			"registered. Edited wiring attests none from the next session start"
		if c.Hint != wantHint {
			t.Fatalf("hint = %q, want %q", c.Hint, wantHint)
		}
	})

	t.Run("served artifact that fails verification fails", func(t *testing.T) {
		f := newDoctorContentFixture(t)
		f.serve(t, "gemini", map[string][]byte{"hooks.gemini": []byte(`{"published":1}`)})
		f.docs["gemini"].Artifacts[0].Content = []byte(`{"published":2}`) // signed bytes changed in transit
		f.writeManaged(t, "gemini", []byte(`{"hooks":{},"older":"render"}`))
		c := managedContentCheck(ctx, f.cfg, nil)
		if c == nil || c.Status != checkFail || !strings.Contains(c.Detail, "gemini: matches no local render, and the served artifact could not be verified (") {
			t.Fatalf("want fail naming the unverified served artifact, got %+v", c)
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
		c := managedContentCheck(ctx, f.cfg, nil)
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
		c := managedContentCheck(ctx, f.cfg, nil)
		if c == nil || c.Status != checkOK || !strings.Contains(c.Detail, "skipped") {
			t.Fatalf("want ok with skipped note, got %+v", c)
		}

		f.writeManaged(t, "gemini", []byte(`{"hand":"edited"}`))
		c = managedContentCheck(ctx, f.cfg, nil)
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
		c := managedContentCheck(ctx, f.cfg, nil)
		if c == nil || c.Status != checkFail || !strings.Contains(c.Detail, "MISSING") {
			t.Fatalf("want fail missing-under-claim, got %+v", c)
		}
	})

	t.Run("nothing managed for a harness stays silent", func(t *testing.T) {
		f := newDoctorContentFixture(t)
		c := managedContentCheck(ctx, f.cfg, nil)
		if c != nil {
			t.Fatalf("no managed wiring and no claims: want nil, got %+v", c)
		}
	})
}

// TestManagedContentCheckBySessionAttestation pins how doctor judges managed
// wiring that matches neither the served wiring nor this build's render: by
// the attestation strazad gave the newest session of that harness at the
// check-in that started it. managed means an older render strazad still
// accepts. none fails without naming one cause, because the wiring, a
// retired hash, the binary or the managed configuration can each make it.
// Wiring that changed after that check-in warns that no session has been
// judged on it since, and on Linux and macOS a change counts by the later of
// the modification and change times, which a copy that keeps an old
// modification time cannot set back. Any other level, a session of another
// harness and a service that reports no level leave the verdict open.
func TestManagedContentCheckBySessionAttestation(t *testing.T) {
	ctx := context.Background()
	// The file is written just before each case runs, so its change time is
	// now. A check-in a minute from now was judged on it as it is, and one
	// an hour ago was judged before it last changed.
	after, before := time.Now().Add(time.Minute), time.Now().Add(-time.Hour)
	ses := func(harness, id, level string, at time.Time) localapi.SessionStatus {
		return localapi.SessionStatus{Harness: localapi.Harness{Name: harness}, SessionID: id, Attestation: level, AttestedAt: at}
	}
	const (
		open = "gemini: managed wiring matches neither the wiring the server serves now nor this build's render, " +
			"so an older Straza install wrote it or it was edited"
		older       = "gemini: managed wiring is older than the wiring the server serves now, and strazad accepted it at the check-in of session s1 (attestation managed)"
		notAccepted = "gemini: managed wiring matches neither the wiring the server serves now nor this build's render, " +
			"and strazad did not accept this machine's measurements at the check-in of session s2 (attestation none). " +
			"The wiring was edited, strazad no longer registers its hash, or the straza binary or managed configuration does not match the registry"
		changed = "gemini: managed wiring matches neither the wiring the server serves now nor this build's render, " +
			"and it changed after the check-in of session s1, so strazad has not judged it since. The next session start judges it"
	)
	cases := []struct {
		name       string
		sessions   []localapi.SessionStatus
		mtimeBack  bool // the modification time is set back an hour after the write, as cp -p does
		wantStatus string
		wantDetail string
	}{
		{name: "a service that reports no level", sessions: []localapi.SessionStatus{ses("gemini", "s1", "", time.Time{})}, wantStatus: checkWarn, wantDetail: open},
		{name: "no session", wantStatus: checkWarn, wantDetail: open},
		{name: "strazad accepted it", sessions: []localapi.SessionStatus{ses("gemini", "s1", "managed", after)}, wantStatus: checkWarn, wantDetail: older},
		{name: "strazad did not accept it", sessions: []localapi.SessionStatus{ses("gemini", "s2", "none", after)}, wantStatus: checkFail, wantDetail: notAccepted},
		{name: "the file changed after the check-in", sessions: []localapi.SessionStatus{ses("gemini", "s1", "managed", before)}, wantStatus: checkWarn, wantDetail: changed},
		{name: "the file changed after an advisory check-in", sessions: []localapi.SessionStatus{ses("gemini", "s1", "advisory", before)}, wantStatus: checkWarn, wantDetail: changed},
		{name: "an advisory level", sessions: []localapi.SessionStatus{ses("gemini", "s1", "advisory", after)}, wantStatus: checkWarn, wantDetail: open},
		{name: "another harness's session", sessions: []localapi.SessionStatus{ses("claude-code", "s1", "managed", after)}, wantStatus: checkWarn, wantDetail: open},
		{name: "the newest check-in decides", sessions: []localapi.SessionStatus{
			ses("gemini", "s1", "managed", after), ses("gemini", "s2", "none", after.Add(time.Minute))}, wantStatus: checkFail, wantDetail: notAccepted},
		{name: "a modification time set back after the check-in", mtimeBack: true,
			sessions: []localapi.SessionStatus{ses("gemini", "s1", "managed", time.Now().Add(-time.Minute))}, wantStatus: checkWarn, wantDetail: changed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mtimeBack && runtime.GOOS == "windows" {
				t.Skip("Windows keeps no change time that a copy cannot set")
			}
			f := newDoctorContentFixture(t)
			f.serve(t, "gemini", map[string][]byte{"hooks.gemini": []byte(`{"published":1}`)})
			f.writeManaged(t, "gemini", []byte(`{"hooks":{},"older":"render"}`))
			if tc.mtimeBack {
				path, err := ManagedHookPath("gemini")
				if err != nil {
					t.Fatal(err)
				}
				old := time.Now().Add(-time.Hour)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			c := managedContentCheck(ctx, f.cfg, tc.sessions)
			if c == nil || c.Status != tc.wantStatus || c.Detail != tc.wantDetail {
				t.Fatalf("got %+v, want %s %q", c, tc.wantStatus, tc.wantDetail)
			}
		})
	}
}

// TestManagedContentCheckMetadataChange pins that a change to the managed
// wiring's metadata alone never fails doctor. The wiring is an older render
// strazad accepted at a check-in after it was written. A chmod to the same
// mode, as a hardening script, an SELinux relabel or an installer that
// writes the same bytes again would do, moves the change time, so doctor
// no longer says strazad accepted it, and it warns until the next session
// start judges the file.
func TestManagedContentCheckMetadataChange(t *testing.T) {
	f := newDoctorContentFixture(t)
	f.serve(t, "gemini", map[string][]byte{"hooks.gemini": []byte(`{"published":1}`)})
	f.writeManaged(t, "gemini", []byte(`{"hooks":{},"older":"render"}`))
	path, err := ManagedHookPath("gemini")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	sessions := []localapi.SessionStatus{{Harness: localapi.Harness{Name: "gemini"}, SessionID: "s1", Attestation: "managed", AttestedAt: time.Now()}}
	time.Sleep(20 * time.Millisecond)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fi.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	c := managedContentCheck(context.Background(), f.cfg, sessions)
	if c == nil || c.Status != checkWarn {
		t.Fatalf("got %+v, want warn", c)
	}
	if runtime.GOOS != "windows" && !strings.Contains(c.Detail, "changed after the check-in of session s1") {
		t.Errorf("detail = %q, want the change after the check-in, not an accepted verdict", c.Detail)
	}
}
