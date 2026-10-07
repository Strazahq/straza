package agentguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHomeResolution pins the state-root resolution: $STRAZA_HOME overrides,
// otherwise ~/.straza. Exactly one name, no legacy fallback: a pre-rename
// ~/.straz root is deliberately orphaned and re-enrolling is the upgrade path.
func TestHomeResolution(t *testing.T) {
	cases := []struct {
		name       string
		makeDirs   []string // relative to the fake $HOME
		envHome    string   // STRAZA_HOME override
		wantSuffix string
	}{
		{name: "override wins", envHome: "explicit", wantSuffix: "explicit"},
		{name: "default", wantSuffix: ".straza"},
		{name: "pre-rename dir is ignored", makeDirs: []string{".straz"}, wantSuffix: ".straza"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home) // windows UserHomeDir
			t.Setenv("STRAZA_HOME", tc.envHome)
			for _, d := range tc.makeDirs {
				if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Home()
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(got) != tc.wantSuffix {
				t.Errorf("Home() = %q, want basename %q", got, tc.wantSuffix)
			}
		})
	}
}

// TestRevocationMarkerRoundTrip: the marker left when the server kills a
// session is what lets hooks tell the agent the truth ("revoked. An admin
// must re-enable you") instead of a generic re-enroll hint. Pin the
// write→read→clear cycle and that absence is not an error.
func TestRevocationMarkerRoundTrip(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.LoadRevocation(); err == nil {
		t.Fatal("no marker written yet, so LoadRevocation should error")
	}

	before := time.Now().Add(-time.Second)
	if err := store.MarkRevoked("user disabled via SCIM"); err != nil {
		t.Fatal(err)
	}
	rev, err := store.LoadRevocation()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rev.Reason, "disabled") || rev.At.Before(before) {
		t.Errorf("marker = %+v", rev)
	}

	// A fresh session clears the marker; clearing twice must stay silent.
	if err := store.ClearRevocation(); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearRevocation(); err != nil {
		t.Errorf("second clear errored: %v", err)
	}
	if _, err := store.LoadRevocation(); err == nil {
		t.Error("marker survived ClearRevocation")
	}
}

// TestAtomicStateWrites pins the atomic persistence of every writer that a torn
// file would fail-close on: SaveSession, SaveSnapshot, and SaveConfig must
// overwrite an existing file, round-trip the content exactly, and leave no
// temp residue behind in the directories they touch.
func TestAtomicStateWrites(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}

	// Seed an initial version of each atomically-written file.
	if err := store.SaveConfig(Config{ServerURL: "https://old"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot([]byte("blob-one")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{SessionID: "s-old", SessionToken: "tok-old"}); err != nil {
		t.Fatal(err)
	}

	// Overwrite each existing file with new content.
	if err := store.SaveConfig(Config{ServerURL: "https://new", SnapshotLagSeconds: 7}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot([]byte("blob-two-is-longer")); err != nil {
		t.Fatal(err)
	}
	want := Session{SessionID: "s-new", SessionToken: "tok-new", User: "u1", Roles: []string{"dev"}, SnapshotID: "snap-9"}
	if err := store.SaveSession(want); err != nil {
		t.Fatal(err)
	}

	// Round-trip: every read reflects exactly the last write.
	if cfg, err := store.LoadConfig(); err != nil || cfg.ServerURL != "https://new" || cfg.SnapshotLagSeconds != 7 {
		t.Fatalf("config round-trip = %+v (%v)", cfg, err)
	}
	if blob, err := store.LoadSnapshot(); err != nil || string(blob) != "blob-two-is-longer" {
		t.Fatalf("snapshot round-trip = %q (%v)", blob, err)
	}
	if ses, err := store.LoadSession(); err != nil || ses.SessionID != "s-new" ||
		ses.SessionToken != "tok-new" || ses.User != "u1" || ses.SnapshotID != "snap-9" {
		t.Fatalf("session round-trip = %+v (%v)", ses, err)
	}

	// No temp residue in either directory the writes touch (root for config,
	// state/ for session + snapshot).
	home, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{home, filepath.Join(home, "state")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp") {
				t.Errorf("temp residue left in %s: %s", dir, e.Name())
			}
		}
	}
}
