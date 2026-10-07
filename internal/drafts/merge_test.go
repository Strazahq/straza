package drafts

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// manifestJSON is a remote server's manifest as JSON, with limits.rps and
// a description given, and extra spliced into its straza block.
func manifestJSON(rps, description, extra string) string {
	return `{"apiVersion":"straza.dev/v1beta1","kind":"App","metadata":{"name":"demo","description":"` + description + `"},` +
		`"server":{"name":"example.com/demo","remotes":[{"type":"streamable-http","url":"https://a.example/mcp"}]},` +
		`"straza":{"limits":{"rps":` + rps + `},"runtime":{"kind":"remote","remote":{"url":"https://a.example/mcp"}}` + extra + `}}`
}

// decoded reads a JSON document for a comparison that ignores spelling.
func decoded(t *testing.T, doc string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("%v in %s", err, doc)
	}
	return v
}

func put(doc string) Version { return Version{Op: OpPut, Doc: doc} }

// TestMergeFields pins the rule of every field of Check again: a field the
// draft alone changed keeps the draft's value, a field live alone changed
// takes live's, both changed to one value takes it, and both changed to
// different values is a conflict until a pick settles it. A pick that names
// a field in no conflict decides nothing.
func TestMergeFields(t *testing.T) {
	t.Parallel()
	base := manifestJSON("5", "old", "")
	cases := []struct {
		name         string
		draft, live  string
		picks        map[string]Pick
		want         string
		conflicts    []Conflict
		picked       int
		draftChanged bool
	}{
		{name: "the draft alone changed a field", draft: manifestJSON("20", "old", ""), live: manifestJSON("5", "new", ""),
			want: manifestJSON("20", "new", ""), draftChanged: true},
		{name: "live alone changed a field", draft: manifestJSON("5", "draft words", ""), live: manifestJSON("10", "old", ""),
			want: manifestJSON("10", "draft words", ""), draftChanged: true},
		{name: "both changed a field to one value", draft: manifestJSON("20", "old", ""), live: manifestJSON("20", "new", ""),
			want: manifestJSON("20", "new", ""), draftChanged: true},
		{name: "both changed a field to different values", draft: manifestJSON("20", "old", ""), live: manifestJSON("10", "old", ""),
			conflicts: []Conflict{{Object: "App/demo", Field: "straza.limits.rps", Base: "5", Draft: "20", Live: "10"}}},
		{name: "a pick of the draft's value", draft: manifestJSON("20", "old", ""), live: manifestJSON("10", "new", ""),
			picks: map[string]Pick{"App/demo straza.limits.rps": PickDraft}, want: manifestJSON("20", "new", ""), picked: 1, draftChanged: true},
		{name: "a pick of live's value", draft: manifestJSON("20", "old", ""), live: manifestJSON("10", "new", ""),
			picks: map[string]Pick{"App/demo straza.limits.rps": PickLive}, want: manifestJSON("10", "new", ""), picked: 1, draftChanged: true},
		{name: "a pick of a field in no conflict decides nothing", draft: manifestJSON("20", "old", ""), live: manifestJSON("5", "new", ""),
			picks: map[string]Pick{"App/demo metadata.description": PickDraft}, want: manifestJSON("20", "new", ""), draftChanged: true},
		{name: "a block the draft adds", draft: manifestJSON("5", "old", `,"exposure":{"tools":["a","b"]}`), live: manifestJSON("10", "old", ""),
			want: manifestJSON("10", "old", `,"exposure":{"tools":["a","b"]}`), draftChanged: true},
		{name: "a leaf the draft removed and live changed", draft: `{"apiVersion":"straza.dev/v1beta1","kind":"App","metadata":{"name":"demo","description":"old"},` +
			`"server":{"name":"example.com/demo","remotes":[{"type":"streamable-http","url":"https://a.example/mcp"}]},` +
			`"straza":{"runtime":{"kind":"remote","remote":{"url":"https://a.example/mcp"}}}}`,
			live:      manifestJSON("10", "old", ""),
			conflicts: []Conflict{{Object: "App/demo", Field: "straza.limits.rps", Base: "5", Draft: "", Live: "10"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Merge(KindApp, "App/demo", put(base), put(tc.draft), put(tc.live), tc.picks)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(m.Conflicts, tc.conflicts) {
				t.Fatalf("conflicts\n got %+v\nwant %+v", m.Conflicts, tc.conflicts)
			}
			if m.Picked != tc.picked {
				t.Errorf("picked = %d, want %d", m.Picked, tc.picked)
			}
			if tc.conflicts != nil {
				return
			}
			if m.Op != OpPut || m.Drop {
				t.Errorf("op %s drop %v, want a put", m.Op, m.Drop)
			}
			if !reflect.DeepEqual(decoded(t, m.Doc), decoded(t, tc.want)) {
				t.Errorf("merged\n got %s\nwant %s", m.Doc, tc.want)
			}
			if m.Changed != !reflect.DeepEqual(decoded(t, tc.want), decoded(t, tc.draft)) {
				t.Errorf("changed = %v for a merge whose document is %s and the draft's %s", m.Changed, m.Doc, tc.draft)
			}
		})
	}
}

// TestAppFieldsWalkLeaves pins the fields of a server: the manifest's
// leaves as the audit record's changed paths name them, where an object is
// walked key by key, an array or a scalar is one leaf, the verbatim server
// block is one leaf, and an empty block keeps its own path.
func TestAppFieldsWalkLeaves(t *testing.T) {
	t.Parallel()
	doc := `{"metadata":{"name":"demo"},"server":{"name":"x","remotes":[{"url":"https://a"}]},` +
		`"straza":{"limits":{},"runtime":{"kind":"command","command":{"exec":"npx","args":["-y","pkg"],"env":[{"name":"A","value":"1"}]}}}}`
	fs, err := appFields(doc)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range fs {
		names = append(names, name)
	}
	slices.Sort(names)
	want := []string{"metadata.name", "server", "straza.limits", "straza.runtime.command.args", "straza.runtime.command.env",
		"straza.runtime.command.exec", "straza.runtime.kind"}
	if !slices.Equal(names, want) {
		t.Errorf("fields\n got %v\nwant %v", names, want)
	}
	back, err := buildApp(fs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded(t, back), decoded(t, doc)) {
		t.Errorf("rebuilt\n got %s\nwant %s", back, doc)
	}
}

// TestMergeNumbersCompareByValue pins that a number reads as its value, so
// the spelling Postgres gives a stored manifest never reads as a change.
func TestMergeNumbersCompareByValue(t *testing.T) {
	t.Parallel()
	m, err := Merge(KindApp, "App/demo", put(manifestJSON("1.50", "old", "")), put(manifestJSON("15e-1", "draft words", "")),
		put(manifestJSON("1.5", "old", `,"exposure":{"tools":["a"]}`)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Conflicts) != 0 {
		t.Fatalf("conflicts %+v, want none", m.Conflicts)
	}
	want := manifestJSON("15e-1", "draft words", `,"exposure":{"tools":["a"]}`)
	if !reflect.DeepEqual(decoded(t, m.Doc), decoded(t, want)) {
		t.Errorf("merged\n got %s\nwant %s", m.Doc, want)
	}
}

// roleDoc is the canonical Role document of dev with description, one
// binding on github with tools, and the roles it implies.
func roleDoc(t *testing.T, description string, tools, implies []string) string {
	t.Helper()
	d := RoleDoc{APIVersion: RoleAPIVersion, Kind: string(KindRole), Metadata: RoleDocMeta{Name: "dev"},
		Spec: RoleDocSpec{Kind: RoleKindApplication, Description: description, Server: "github", Implies: implies}}
	if tools != nil {
		d.Spec.Bindings = []RoleBinding{{App: "github", Tools: tools}}
	}
	raw, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestMergeRoleFields pins a role's three fields, spec.description,
// spec.bindings and spec.implies, and that every other field of the merged
// document is the draft's.
func TestMergeRoleFields(t *testing.T) {
	t.Parallel()
	base := roleDoc(t, "old", []string{"a"}, []string{"x"})
	cases := []struct {
		name        string
		draft, live string
		picks       map[string]Pick
		want        string
		conflicts   []Conflict
	}{
		{name: "the draft's description and live's implications", draft: roleDoc(t, "new", []string{"a"}, []string{"x"}),
			live: roleDoc(t, "old", []string{"a"}, []string{"x", "y"}), want: roleDoc(t, "new", []string{"a"}, []string{"x", "y"})},
		{name: "live's binding and the draft's implications", draft: roleDoc(t, "old", []string{"a"}, nil),
			live: roleDoc(t, "old", []string{"a", "b"}, []string{"x"}), want: roleDoc(t, "old", []string{"a", "b"}, nil)},
		{name: "both changed the binding", draft: roleDoc(t, "old", []string{"c"}, []string{"x"}),
			live: roleDoc(t, "old", nil, []string{"x"}),
			conflicts: []Conflict{{Object: "Role/dev", Field: "spec.bindings", Base: `[{"app":"github","tools":["a"]}]`,
				Draft: `[{"app":"github","tools":["c"]}]`, Live: ""}}},
		{name: "a pick settles the binding", draft: roleDoc(t, "old", []string{"c"}, []string{"x"}),
			live: roleDoc(t, "new", nil, []string{"x"}), picks: map[string]Pick{"Role/dev spec.bindings": PickDraft},
			want: roleDoc(t, "new", []string{"c"}, []string{"x"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Merge(KindRole, "Role/dev", put(base), put(tc.draft), put(tc.live), tc.picks)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(m.Conflicts, tc.conflicts) {
				t.Fatalf("conflicts\n got %+v\nwant %+v", m.Conflicts, tc.conflicts)
			}
			if tc.conflicts == nil && m.Doc != tc.want {
				t.Errorf("merged\n got %s\nwant %s", m.Doc, tc.want)
			}
		})
	}
}

// TestMergePolicySetFields pins a set's two fields, its text and its state,
// on or off.
func TestMergePolicySetFields(t *testing.T) {
	t.Parallel()
	on := func(text string) Version { return Version{Op: OpPut, Doc: text} }
	off := func(text string) Version { return Version{Op: OpOff, Doc: text} }
	cases := []struct {
		name              string
		base, draft, live Version
		picks             map[string]Pick
		want              Version
		conflicts         []Conflict
	}{
		{name: "the draft's text on a set live turned off", base: on("v1"), draft: on("v2"), live: off("v1"), want: off("v2")},
		{name: "live's text under the draft's turning off", base: on("v1"), draft: off("v1"), live: on("v3"), want: off("v3")},
		{name: "both changed the text", base: on("v1"), draft: on("v2"), live: on("v3"),
			conflicts: []Conflict{{Object: "PolicySet/guard", Field: "text", Base: "v1", Draft: "v2", Live: "v3"}}},
		{name: "both changed the state", base: off("v1"), draft: on("v1"), live: Version{Op: OpPut, Doc: "v1"}, want: on("v1")},
		{name: "a pick of live's text", base: on("v1"), draft: on("v2"), live: on("v3"),
			picks: map[string]Pick{"PolicySet/guard text": PickLive}, want: on("v3")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Merge(KindPolicySet, "PolicySet/guard", tc.base, tc.draft, tc.live, tc.picks)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(m.Conflicts, tc.conflicts) {
				t.Fatalf("conflicts\n got %+v\nwant %+v", m.Conflicts, tc.conflicts)
			}
			if tc.conflicts == nil && (m.Op != tc.want.Op || m.Doc != tc.want.Doc) {
				t.Errorf("merged %s %q, want %s %q", m.Op, m.Doc, tc.want.Op, tc.want.Doc)
			}
		})
	}
}

// TestMergeExistence pins the state field: a removal stands over a changed
// object and leaves the draft when live removed the object too, a change
// of an object live removed is a conflict, and an item that changed
// nothing leaves the draft with the object.
func TestMergeExistence(t *testing.T) {
	t.Parallel()
	base, changed := put(manifestJSON("5", "old", "")), put(manifestJSON("20", "old", ""))
	gone, removal := Version{Op: OpRemove}, Version{Op: OpRemove}
	stateConflict := []Conflict{{Object: "App/demo", Field: FieldState, Base: "present", Draft: "present", Live: "absent"}}
	cases := []struct {
		name              string
		base, draft, live Version
		picks             map[string]Pick
		wantOp            Op
		wantDrop          bool
		conflicts         []Conflict
	}{
		{name: "a removal over a changed object", base: base, draft: removal, live: changed, wantOp: OpRemove},
		{name: "a removal of an object live removed too", base: base, draft: removal, live: gone, wantDrop: true},
		{name: "a change of an object live removed", base: base, draft: changed, live: gone, conflicts: stateConflict},
		{name: "the draft's change kept over live's removal", base: base, draft: changed, live: gone,
			picks: map[string]Pick{"App/demo state": PickDraft}, wantOp: OpPut},
		{name: "live's removal taken over the draft's change", base: base, draft: changed, live: gone,
			picks: map[string]Pick{"App/demo state": PickLive}, wantDrop: true},
		{name: "an unchanged item of an object live removed", base: base, draft: base, live: gone, wantDrop: true},
		{name: "an object both sides created alike", base: gone, draft: changed, live: changed, wantOp: OpPut},
		{name: "an object both sides created apart", base: gone, draft: changed, live: put(manifestJSON("7", "old", "")),
			conflicts: []Conflict{{Object: "App/demo", Field: "straza.limits.rps", Base: "", Draft: "20", Live: "7"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Merge(KindApp, "App/demo", tc.base, tc.draft, tc.live, tc.picks)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(m.Conflicts, tc.conflicts) {
				t.Fatalf("conflicts\n got %+v\nwant %+v", m.Conflicts, tc.conflicts)
			}
			if tc.conflicts != nil {
				return
			}
			if m.Drop != tc.wantDrop || (!tc.wantDrop && m.Op != tc.wantOp) {
				t.Errorf("op %s drop %v, want op %s drop %v", m.Op, m.Drop, tc.wantOp, tc.wantDrop)
			}
			if tc.wantOp == OpPut && !tc.wantDrop && !reflect.DeepEqual(decoded(t, m.Doc), decoded(t, tc.draft.Doc)) {
				t.Errorf("merged %s, want the draft's %s", m.Doc, tc.draft.Doc)
			}
		})
	}
}

// commandJSON is a command server's manifest as JSON with env as its
// environment and rps as its rate.
func commandJSON(rps, env string) string {
	return `{"apiVersion":"straza.dev/v1beta1","kind":"App","metadata":{"name":"demo"},"server":{"name":"example.com/demo"},` +
		`"straza":{"limits":{"rps":` + rps + `},"runtime":{"kind":"command","command":{"exec":"npx","env":[` + env + `]}}}}`
}

// TestMergeMaskedBase pins the base of a server stored with a secret: the
// base masks every place the secret scan flags, live is compared masked the
// same way, so a place that still holds the secret reads unchanged and the
// draft's value stands, while taking live's value where live holds a
// secret, left alone or picked, refuses, and a base that does not read as
// a manifest refuses too.
func TestMergeMaskedBase(t *testing.T) {
	t.Parallel()
	// The token is built at run time, so the source holds no
	// credential-shaped literal.
	token := `{"name":"GITHUB_TOKEN","value":"` + "gh" + "p_" + strings.Repeat("A1b2C3d4", 5) + `"}`
	const plain = `{"name":"LOG_LEVEL","value":"debug"}`
	secretLive := commandJSON("5", token)
	maskedBase, masked := WithoutSecrets(secretLive)
	if !masked || strings.Contains(maskedBase, "A1b2C3d4") {
		t.Fatalf("the stamp's mask kept the token: %s", maskedBase)
	}
	cases := []struct {
		name        string
		base        string
		draft, live string
		picks       map[string]Pick
		want        string
		secret      string
		unread      bool
	}{
		{name: "a place that still holds the secret keeps the draft's value", base: maskedBase,
			draft: commandJSON("5", plain), live: commandJSON("10", token), want: commandJSON("10", plain)},
		{name: "live gained a secret where the draft left the field alone", base: commandJSON("5", plain),
			draft: commandJSON("20", plain), live: commandJSON("5", token), secret: "straza.runtime.command.env"},
		{name: "a pick of live where live holds a secret", base: commandJSON("5", plain),
			draft: commandJSON("5", `{"name":"LOG_LEVEL","value":"info"}`), live: commandJSON("5", token),
			picks: map[string]Pick{"App/demo straza.runtime.command.env": PickLive}, secret: "straza.runtime.command.env"},
		{name: "a base masked whole", base: "[REDACTED]", draft: commandJSON("5", plain), live: commandJSON("10", plain), unread: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Merge(KindApp, "App/demo", put(tc.base), put(tc.draft), put(tc.live), tc.picks)
			var secret *LiveSecretError
			var unread *UnreadError
			switch {
			case tc.secret != "":
				if !errors.As(err, &secret) || secret.Field != tc.secret || secret.Object != "App/demo" {
					t.Fatalf("err = %v, want a live secret at %s", err, tc.secret)
				}
				return
			case tc.unread:
				if !errors.As(err, &unread) || unread.Side != "base" {
					t.Fatalf("err = %v, want the base unread", err)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			if len(m.Conflicts) != 0 || !reflect.DeepEqual(decoded(t, m.Doc), decoded(t, tc.want)) {
				t.Errorf("merged %s with conflicts %+v, want %s", m.Doc, m.Conflicts, tc.want)
			}
			if strings.Contains(m.Doc, "A1b2C3d4") {
				t.Errorf("the merged document holds the secret: %s", m.Doc)
			}
		})
	}
}

// TestAppFieldText pins the words a conflict shows for a server's field: a
// string as itself, any other value as compact JSON.
func TestAppFieldText(t *testing.T) {
	t.Parallel()
	got, err := AppFieldText(manifestJSON("5", "old", `,"exposure":{"tools":["a", "b"]}`))
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{"straza.limits.rps": "5", "metadata.description": "old", "straza.exposure.tools": `["a","b"]`} {
		if got[field] != want {
			t.Errorf("%s = %q, want %q", field, got[field], want)
		}
	}
}
