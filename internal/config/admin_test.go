package config

import (
	"strings"
	"testing"
)

// TestAdminSecondPerson pins admin.secondPerson: the file sets it, it
// is false in both profiles when the file says nothing, and no environment
// variable sets it, because a hidden variable must not change who may
// publish.
func TestAdminSecondPerson(t *testing.T) {
	cases := []struct {
		name string
		file string
		env  map[string]string
		want bool
	}{
		{"standalone, the key left out", "profile: standalone\n", nil, false},
		{"enterprise, the key left out", "profile: enterprise\nstore:\n  dsn: postgres://x\n", nil, false},
		{"the key set in the file", "admin:\n  secondPerson: true\n", nil, true},
		{"an environment variable of its name", "profile: standalone\n", map[string]string{"STRAZA_ADMIN_SECOND_PERSON": "true", "STRAZA_ADMIN_SECONDPERSON": "true"}, false},
	}
	for _, tc := range cases {
		cfg, err := Loader{FilePath: writeFile(t, tc.file), ExplicitFile: true, Getenv: envMap(tc.env)}.Load()
		if err != nil {
			t.Fatalf("%s: Load: %v", tc.name, err)
		}
		if cfg.Admin.SecondPerson != tc.want {
			t.Errorf("%s: admin.secondPerson = %v, want %v", tc.name, cfg.Admin.SecondPerson, tc.want)
		}
	}
}

// TestAdminSecondPersonNote pins what the configuration page says of
// admin.secondPerson: which routes enforce it, and the two paths it does
// not cover, named here and explained on the drafts guide it links, so
// turning it on for segregation of duties reads as exactly the control it
// is.
func TestAdminSecondPersonNote(t *testing.T) {
	const want = "The drafts publish route and the direct admin routes enforce it. " +
		"False, the default in both profiles, lets the author publish. It does not cover assignments, which stay immediate, " +
		"or a credential of the person that an agent can read on that machine, and [Drafts and publishing]" +
		`({{< relref "guides/changes/who-may-draft.md" >}}) explains both.`
	for _, k := range Knobs() {
		if k.Path == "admin.secondPerson" {
			if !strings.Contains(k.Note, want) {
				t.Errorf("the note %q does not say %q", k.Note, want)
			}
			return
		}
	}
	t.Fatal("the knob table has no row for admin.secondPerson")
}
