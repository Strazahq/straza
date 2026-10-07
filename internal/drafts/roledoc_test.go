package drafts

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixture reads a spec/objects fixture.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "spec", "objects", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRoleDocMarshalWritesTheExportForm pins the one serializer against the
// export fixtures: lists given in any order come out sorted, in the bytes
// the export route serves.
func TestRoleDocMarshalWritesTheExportForm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		doc     RoleDoc
		fixture string
	}{
		{"a global role with an implication, a binding and a pack", RoleDoc{
			APIVersion: RoleAPIVersion, Kind: "Role", Metadata: RoleDocMeta{Name: "dev"},
			Spec: RoleDocSpec{Kind: RoleKindApplication, Description: "Developer seat: governed demo-tools access",
				Implies: []string{"reader"}, Bindings: []RoleBinding{{App: "demo-tools", Tools: []string{"echo", "add"}}},
				Packs: []string{"golang-style"}},
		}, "role-dev.yaml"},
		{"a role its server owns", RoleDoc{
			APIVersion: RoleAPIVersion, Kind: "Role", Metadata: RoleDocMeta{Name: "demo-tools-readers"},
			Spec: RoleDocSpec{Kind: RoleKindApplication, Description: "Read-only tools of demo-tools for analysts",
				Server: "demo-tools", Bindings: []RoleBinding{{App: "demo-tools", Tools: []string{"echo", "add"}}}},
		}, "role-owned.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.doc.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if want := fixture(t, tc.fixture); string(got) != want {
				t.Errorf("Marshal drifted from %s:\n--- got ---\n%s\n--- want ---\n%s", tc.fixture, got, want)
			}
			if tc.doc.Spec.Bindings[0].Tools[0] != "echo" {
				t.Errorf("Marshal sorted the caller's list in place: %v", tc.doc.Spec.Bindings[0].Tools)
			}
		})
	}
}

// TestParseRoleReadsEveryExportBack pins that each export fixture reads back
// strictly and writes out the same bytes.
func TestParseRoleReadsEveryExportBack(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"role-dev.yaml", "role-owned.yaml"} {
		t.Run(name, func(t *testing.T) {
			doc, err := ParseRole(fixture(t, name))
			if err != nil {
				t.Fatalf("ParseRole: %v", err)
			}
			if got := roleText(doc); got != fixture(t, name) {
				t.Errorf("round trip drifted:\n--- got ---\n%s\n--- want ---\n%s", got, fixture(t, name))
			}
		})
	}
}

func TestParseRoleRefuses(t *testing.T) {
	t.Parallel()
	const head = "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: dev\n"
	cases := []struct {
		name, doc, want string
	}{
		{"an empty text", "", "the text holds no document"},
		{"two documents", head + "spec:\n    kind: business\n---\n" + head, "the text holds more than one document"},
		{"a field no Role document has", head + "spec:\n    kind: business\n    color: blue\n", "line 7: field color not found in type spec"},
		{"another apiVersion", strings.Replace(head, "v1beta1", "v1", 1) + "spec:\n    kind: business\n", `apiVersion must be straza.dev/v1beta1, and it is "straza.dev/v1"`},
		{"another kind", strings.Replace(head, "kind: Role", "kind: Group", 1), `kind must be Role, and it is "Group"`},
		{"no name", "apiVersion: straza.dev/v1beta1\nkind: Role\nspec:\n    kind: business\n", "metadata.name is empty"},
		{"a kind the admin API does not know", head + "spec:\n    kind: console\n", `spec.kind must be business, application, approver or straza, and it is "console"`},
		{"a binding with no server", head + "spec:\n    kind: application\n    bindings:\n        - tools: [echo]\n", "spec.bindings[0].app is empty"},
		{"an implication listed twice", head + "spec:\n    kind: business\n    implies: [dev, github-readers, ops, ops]\n", "spec.implies lists ops twice"},
		{"a tool listed twice", head + "spec:\n    kind: application\n    bindings:\n        - app: github\n          tools: [a, b, a]\n", "spec.bindings[0].tools lists a twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRole(tc.doc)
			if err == nil || err.Error() != tc.want {
				t.Errorf("ParseRole error = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestParseRoleReadsATrailingSeparatorAsOneDocument pins that a text ending
// in a --- line holds one document, as the bundle reader counts it.
func TestParseRoleReadsATrailingSeparatorAsOneDocument(t *testing.T) {
	t.Parallel()
	if _, err := ParseRole("apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n  name: dev\nspec:\n  kind: application\n---\n"); err != nil {
		t.Errorf("ParseRole = %v, want the one document read", err)
	}
}

// TestParseRoleLeavesAMissingKindToItsCaller pins that a document with no
// spec.kind reads, so intake can refuse it with its own code.
func TestParseRoleLeavesAMissingKindToItsCaller(t *testing.T) {
	t.Parallel()
	doc, err := ParseRole("apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: dev\nspec:\n    description: x\n")
	if err != nil || doc.Spec.Kind != "" || doc.Metadata.Name != "dev" {
		t.Errorf("ParseRole = %+v, %v, want a document named dev with no kind", doc, err)
	}
}

func TestRoleDocOf(t *testing.T) {
	t.Parallel()
	w := World{
		Roles: map[string]Role{
			"echoapp-readers": {Name: "echoapp-readers", Kind: RoleKindApplication, Plane: PlaneAccess, Owner: "echoapp", Owned: true, Description: "Readers"},
			"engineering":     {Name: "engineering", Kind: RoleKindBusiness, Plane: PlaneAccess, Packs: []string{"z-pack", "a-pack"}},
			"auditors":        {Name: "auditors", Kind: RoleKindBusiness, Plane: PlaneControl},
			"orphan":          {Name: "orphan", Kind: RoleKindApplication, Plane: PlaneAccess},
		},
		Implies: map[string][]string{"engineering": {"echoapp-readers", "auditors"}},
		Access: map[string]Access{
			"echoapp-readers": {ID: "b1", Server: "echoapp", Tools: []string{"search", "echo"}},
			"orphan":          {ID: "b2", Server: ""},
		},
	}
	cases := []struct {
		role string
		ok   bool
		want RoleDocSpec
	}{
		{"echoapp-readers", true, RoleDocSpec{Kind: RoleKindApplication, Description: "Readers", Server: "echoapp",
			Bindings: []RoleBinding{{App: "echoapp", Tools: []string{"echo", "search"}}}}},
		{"engineering", true, RoleDocSpec{Kind: RoleKindBusiness, Implies: []string{"auditors", "echoapp-readers"}, Packs: []string{"a-pack", "z-pack"}}},
		{"auditors", true, RoleDocSpec{Kind: RoleKindStraza}},
		{"orphan", true, RoleDocSpec{Kind: RoleKindApplication}},
		{"nosuch", false, RoleDocSpec{}},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			doc, ok := RoleDocOf(w, tc.role)
			if ok != tc.ok || !reflect.DeepEqual(doc.Spec, tc.want) {
				t.Errorf("RoleDocOf(%s) = %+v, %v, want %+v, %v", tc.role, doc.Spec, ok, tc.want, tc.ok)
			}
			if ok && (doc.APIVersion != RoleAPIVersion || doc.Kind != "Role" || doc.Metadata.Name != tc.role) {
				t.Errorf("RoleDocOf(%s) head = %+v", tc.role, doc)
			}
		})
	}
	if got := w.Implies["engineering"]; got[0] != "echoapp-readers" {
		t.Errorf("RoleDocOf sorted the World's own list: %v", got)
	}
}

// TestRoleDocOfReadsARepeatedTool pins that the document of a live role
// whose stored row lists a tool twice, which the access row route stored as
// sent before drafts, lists each tool once, sorted, so the document of every
// live role parses and a direct route can change that role.
func TestRoleDocOfReadsARepeatedTool(t *testing.T) {
	t.Parallel()
	w := World{Roles: map[string]Role{"legacy": {Name: "legacy", Kind: RoleKindApplication, Plane: PlaneAccess}},
		Access: map[string]Access{"legacy": {Server: "github", Tools: []string{"search", "b", "search"}}}}
	doc, ok := RoleDocOf(w, "legacy")
	if !ok || len(doc.Spec.Bindings) != 1 || !reflect.DeepEqual(doc.Spec.Bindings[0].Tools, []string{"b", "search"}) {
		t.Fatalf("RoleDocOf = %+v, want one binding with the tools b and search", doc.Spec.Bindings)
	}
	text, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRole(string(text)); err != nil {
		t.Errorf("the live role's document does not parse: %v\n%s", err, text)
	}
}
