package drafts

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestBundleFixtures pins the fixtures of spec/objects through the bundle
// reader. A valid bundle reads into the items its documents name, passes
// intake, and writes every Role in the export's form. Each invalid bundle is
// refused for the reason its file name gives. Intake runs only over a bundle
// the reader accepted, because the secret scan runs there and a refusal of
// the reader already answers the draft. The Role exports of revision 4 are
// rows too, because every export is a valid bundle document.
func TestBundleFixtures(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		items   []string // the object and op of each item, in document order
		refused []string // the code and object of each refusal
	}{
		"role-dev.yaml":   {items: []string{"Role/dev put"}},
		"role-owned.yaml": {items: []string{"Role/demo-tools-readers put"}},
		"bundle-onboard.yaml": {items: []string{"App/github put", "Role/github-readers put",
			"Role/github-writers put", "PolicySet/github-writers-access put"}},
		"bundle-removal.yaml":              {items: []string{"App/old-tools remove"}},
		"invalid-bundle-two-bindings.yaml": {refused: []string{"role.bindings Role/dev-tools"}},
		"invalid-bundle-missing-kind.yaml": {refused: []string{"role.kind-missing Role/dev"}},
		"invalid-bundle-secret-env.yaml": {items: []string{"App/ticket-tools put"},
			refused: []string{"secret.value App/ticket-tools"}},
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "spec", "objects", "fixtures", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, ok := cases[filepath.Base(f)]; !ok {
			t.Errorf("spec/objects/fixtures/%s has no row in this test, so nothing pins it", filepath.Base(f))
		}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			text := fixture(t, name)
			items, fs := ParseBundle([]string{text})
			if len(fs) == 0 {
				fs = Intake(Draft{Door: DoorStrazactl, Items: items}, Principal{Username: "alice"})
			}
			var gotItems, gotRefused []string
			for _, it := range items {
				gotItems = append(gotItems, it.Object()+" "+string(it.Op))
				if it.Kind == KindRole && (it.Doc == "" || !strings.Contains(text, it.Doc)) {
					t.Errorf("%s is not written in the export's form. The reader keeps it as:\n%s", it.Object(), it.Doc)
				}
			}
			for _, f := range fs {
				if f.Class != ClassRefused {
					t.Errorf("finding %s is %s, want only refusals", f.Code, f.Class)
				}
				gotRefused = append(gotRefused, f.Code+" "+f.Object)
			}
			if !reflect.DeepEqual(gotItems, tc.items) {
				t.Errorf("items = %q, want %q", gotItems, tc.items)
			}
			if !reflect.DeepEqual(gotRefused, tc.refused) {
				t.Errorf("refusals = %q, want %q", gotRefused, tc.refused)
			}
		})
	}
}
