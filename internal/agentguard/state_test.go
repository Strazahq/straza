package agentguard

import (
	"os"
	"path/filepath"
	"testing"
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
