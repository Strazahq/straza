package drafts

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// coversHead is a Role document up to its one binding's tools, so a case
// adds the covers line alone.
const coversHead = "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: ghapp-write\nspec:\n    kind: application\n    bindings:\n        - app: ghapp\n          tools: [pr_create]\n"

// TestParseRoleReadsCovers pins the two forms of the covers line, every and
// a list of values, kept as written with no id, and a binding without the
// line, which reads as no scope. An empty list and a repeated value read,
// because the check refuses each in the words of the server's type.
func TestParseRoleReadsCovers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, line string
		want       *Scope
	}{
		{"no covers line", "", nil},
		{"every", "          covers: every\n", &Scope{Every: true}},
		{"objects in flow form", "          covers: {objects: [Strazahq/pay-web, Strazahq/pay-api]}\n",
			&Scope{Objects: []ObjectRef{{Value: "Strazahq/pay-web"}, {Value: "Strazahq/pay-api"}}}},
		{"objects in block form", "          covers:\n            objects:\n                - Strazahq/pay-api\n",
			&Scope{Objects: []ObjectRef{{Value: "Strazahq/pay-api"}}}},
		{"a quoted value that reads as a number", "          covers: {objects: [\"123\"]}\n",
			&Scope{Objects: []ObjectRef{{Value: "123"}}}},
		{"an empty list", "          covers: {objects: []}\n", &Scope{Objects: []ObjectRef{}}},
		{"a repeated value", "          covers: {objects: [a, a]}\n", &Scope{Objects: []ObjectRef{{Value: "a"}, {Value: "a"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := ParseRole(coversHead + tc.line)
			if err != nil {
				t.Fatalf("ParseRole: %v", err)
			}
			if got := doc.Spec.Bindings[0].Covers; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Covers = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestParseRoleRefusesACoversLineOfAnotherShape pins the strict reading of
// covers: a scalar other than every, a key other than objects, a list
// that is not one, and a value that is not a string are refused, naming
// the line.
func TestParseRoleRefusesACoversLineOfAnotherShape(t *testing.T) {
	t.Parallel()
	const wrong = "line 10: covers must be every or a mapping with one key, objects, that lists names"
	otherKey := func(key string) string {
		return "line 10: a covers mapping takes only the key objects, and this one carries " + key +
			". Write covers: every for every object, or covers: {objects: [<name>, ...]} for some"
	}
	cases := []struct {
		name, line, want string
	}{
		{"another word", "          covers: all\n", wrong},
		{"null", "          covers: null\n", wrong},
		{"a tilde", "          covers: ~\n", wrong},
		{"no value", "          covers:\n", wrong},
		{"a boolean", "          covers: true\n", wrong},
		{"a list without the objects key", "          covers: [a, b]\n", wrong},
		{"an empty mapping", "          covers: {}\n", wrong},
		{"a where line", "          covers: {where: {team: [payments]}}\n", otherKey("where")},
		{"objects and another key", "          covers: {objects: [a], every: true}\n", otherKey("every")},
		{"every as a mapping", "          covers: {every: true}\n", otherKey("every")},
		{"objects twice", "          covers: {objects: [a], objects: [b]}\n", "line 10: covers carries objects twice"},
		{"objects that is not a list", "          covers: {objects: a}\n", "line 10: covers.objects must be a list of names"},
		{"a number", "          covers: {objects: [a, 123]}\n", "line 10: covers.objects lists 123, which is not a string. Quote it"},
		{"a nested list", "          covers: {objects: [[a]]}\n", "line 10: covers.objects lists an entry that is not a name"},
		{"an alias", "          covers: {objects: [&x a, *x]}\n", "line 10: covers.objects lists an entry that is not a name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRole(coversHead + tc.line)
			if err == nil || err.Error() != tc.want {
				t.Errorf("ParseRole error = %v, want %q", err, tc.want)
			}
		})
	}
	alias := strings.Replace(coversHead, "    kind: application\n", "    kind: application\n    description: &none\n", 1) + "          covers: *none\n"
	if _, err := ParseRole(alias); err == nil || err.Error() != strings.Replace(wrong, "line 10", "line 11", 1) {
		t.Errorf("ParseRole of an alias of a null: %v, want %q on line 11", err, wrong)
	}
}

// TestRoleDocMarshalWritesCovers pins the export form of the covers line
// against its fixtures: every as the word, and objects as the values sorted,
// never the ids. The caller's list stays in its order.
func TestRoleDocMarshalWritesCovers(t *testing.T) {
	t.Parallel()
	objects := &Scope{Objects: []ObjectRef{{Value: "Strazahq/pay-web", ID: "2"}, {Value: "Strazahq/pay-api", ID: "1"}}}
	cases := []struct {
		name, fixture string
		doc           RoleDoc
	}{
		{"every", "role-every.yaml", RoleDoc{APIVersion: RoleAPIVersion, Kind: "Role", Metadata: RoleDocMeta{Name: "ghapp-read"},
			Spec: RoleDocSpec{Kind: RoleKindApplication, Description: "Read tools of ghapp on every repo", Server: "ghapp",
				Bindings: []RoleBinding{{App: "ghapp", Tools: []string{"pr_list", "get_me"}, Covers: &Scope{Every: true}}}}}},
		{"objects", "role-objects.yaml", RoleDoc{APIVersion: RoleAPIVersion, Kind: "Role", Metadata: RoleDocMeta{Name: "ghapp-write-payments"},
			Spec: RoleDocSpec{Kind: RoleKindApplication, Description: "Write tools of ghapp on the payments repos", Server: "ghapp",
				Bindings: []RoleBinding{{App: "ghapp", Tools: []string{"pr_create", "get_me"}, Covers: objects}}}}},
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
			back, err := ParseRole(string(got))
			if err != nil {
				t.Fatalf("ParseRole of the export: %v", err)
			}
			if roleText(back) != string(got) {
				t.Errorf("round trip drifted:\n%s", roleText(back))
			}
		})
	}
	if objects.Objects[0].Value != "Strazahq/pay-web" {
		t.Errorf("Marshal sorted the caller's objects in place: %+v", objects.Objects)
	}
}

// TestScopeJSON pins the object form of the covers line on the admin API:
// every as {"every":true}, objects with value and id, and a strict reading
// that refuses an unknown key, both forms at once and a value that is not
// a string.
func TestScopeJSON(t *testing.T) {
	t.Parallel()
	out := []struct {
		scope *Scope
		want  string
	}{
		{&Scope{Every: true}, `{"every":true}`},
		{&Scope{Objects: []ObjectRef{{Value: "b", ID: "2"}, {Value: "a", ID: "1"}}}, `{"objects":[{"value":"b","id":"2"},{"value":"a","id":"1"}]}`},
		{&Scope{}, `{"objects":[]}`},
	}
	for _, tc := range out {
		got, err := json.Marshal(tc.scope)
		if err != nil || string(got) != tc.want {
			t.Errorf("json.Marshal(%+v) = %s, %v, want %s", tc.scope, got, err, tc.want)
		}
	}
	const forms = `Send {"every": true} or {"objects": [{"value": "<name>"}]}`
	in := []struct {
		name, body string
		want       *Scope
		err        string
	}{
		{"every", `{"every":true}`, &Scope{Every: true}, ""},
		{"objects with an id the server ignores", `{"objects":[{"value":"a","id":"9"}]}`, &Scope{Objects: []ObjectRef{{Value: "a", ID: "9"}}}, ""},
		{"objects by value", `{"objects":[{"value":"a"}]}`, &Scope{Objects: []ObjectRef{{Value: "a"}}}, ""},
		{"an empty list", `{"objects":[]}`, &Scope{Objects: []ObjectRef{}}, ""},
		{"every false and no list", `{"every":false}`, &Scope{}, ""},
		{"both forms", `{"every":true,"objects":[{"value":"a"}]}`, nil, "covers takes every or objects, not both. " + forms},
		{"an unknown key", `{"where":{"team":["payments"]}}`, nil, `covers carries "where", and it takes only every or objects. ` + forms},
		{"an unknown key on an object", `{"objects":[{"value":"a","title":"A"}]}`, nil, `covers carries "title", and it takes only every or objects. ` + forms},
		{"a value that is not a string", `{"objects":[{"value":7}]}`, nil, "covers must be {\"every\": true} or {\"objects\": [{\"value\": \"<name>\"}]}"},
		{"not an object", `"every"`, nil, "covers must be {\"every\": true} or {\"objects\": [{\"value\": \"<name>\"}]}"},
	}
	for _, tc := range in {
		t.Run(tc.name, func(t *testing.T) {
			var got *Scope
			err := json.Unmarshal([]byte(tc.body), &got)
			if tc.err != "" {
				var bad *CoversError
				if !errors.As(err, &bad) || err.Error() != tc.err {
					t.Errorf("json.Unmarshal error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("json.Unmarshal = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
}

// TestScopeKey pins the key every binding comparison reads: ids alone, in
// any order, so a renamed object compares equal and another object does
// not.
func TestScopeKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a, b *Scope
		same bool
	}{
		{"nil and nil", nil, nil, true},
		{"nil and every", nil, &Scope{Every: true}, false},
		{"every and every", &Scope{Every: true}, &Scope{Every: true}, true},
		{"every and objects", &Scope{Every: true}, &Scope{Objects: []ObjectRef{{Value: "a", ID: "1"}}}, false},
		{"nil and an empty list", nil, &Scope{}, false},
		{"a rename", &Scope{Objects: []ObjectRef{{Value: "old", ID: "1"}}}, &Scope{Objects: []ObjectRef{{Value: "new", ID: "1"}}}, true},
		{"another order", &Scope{Objects: []ObjectRef{{ID: "1"}, {ID: "2"}}}, &Scope{Objects: []ObjectRef{{ID: "2"}, {ID: "1"}}}, true},
		{"another object", &Scope{Objects: []ObjectRef{{Value: "a", ID: "1"}}}, &Scope{Objects: []ObjectRef{{Value: "a", ID: "2"}}}, false},
		{"one more object", &Scope{Objects: []ObjectRef{{ID: "1"}}}, &Scope{Objects: []ObjectRef{{ID: "1"}, {ID: "2"}}}, false},
	}
	for _, tc := range cases {
		if got := tc.a.Key() == tc.b.Key(); got != tc.same {
			t.Errorf("%s: %q == %q is %v, want %v", tc.name, tc.a.Key(), tc.b.Key(), got, tc.same)
		}
	}
	if got := (&Scope{Objects: []ObjectRef{{ID: "2"}, {ID: "1"}}}).Key(); got != "objects:1\x002" {
		t.Errorf("Key = %q, want the sorted ids", got)
	}
}
