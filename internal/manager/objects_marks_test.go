package manager

import (
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// marked is a tool whose _meta carries the given marks.
func marked(name string, meta map[string]any) *mcp.Tool {
	return &mcp.Tool{Name: name, Meta: mcp.Meta(meta)}
}

func objMark(fields map[string]any) map[string]any { return map[string]any{markObject: fields} }

func listsMark(typ string, attrs ...map[string]any) map[string]any {
	a := make([]any, len(attrs))
	for i := range attrs {
		a[i] = attrs[i]
	}
	return map[string]any{markListsObjects: map[string]any{"type": typ, "attributes": a}}
}

// demoTools is the objects-demo server's tools/list answer.
func demoTools() []*mcp.Tool {
	return []*mcp.Tool{
		marked("whoami", objMark(map[string]any{"type": "repo", "reach": "none"})),
		marked("repo_list", listsMark("repo", map[string]any{"name": "area", "setBy": "org"})),
		marked("repo_read", objMark(map[string]any{"type": "repo", "argument": "repo"})),
		marked("issue_create", objMark(map[string]any{"type": "repo", "argument": "repo"})),
		marked("search_code", objMark(map[string]any{"type": "repo", "reach": "all"})),
		{Name: "unmarked"},
	}
}

func TestDeclaredObjects(t *testing.T) {
	demo := ObjectsSpec{
		Type: "repo",
		List: ObjectListSpec{Tool: "repo_list", Attributes: []ObjectAttribute{{Name: "area", SetBy: "org"}}},
		Tools: map[string]ToolReach{
			"repo_read":    {Reach: ReachArgument, Argument: "repo"},
			"issue_create": {Reach: ReachArgument, Argument: "repo"},
			"search_code":  {Reach: ReachAll},
			"whoami":       {Reach: ReachNone},
		},
	}
	cases := []struct {
		name       string
		tools      []*mcp.Tool
		wantOK     bool
		wantSpec   *ObjectsSpec
		wantFaults []string // each fault must contain the matching text
	}{
		{name: "no marks", tools: []*mcp.Tool{{Name: "a"}, nil, marked("b", map[string]any{"other/key": 1})}},
		{name: "the demo server", tools: demoTools(), wantOK: true, wantSpec: &demo},
		{
			name: "credential reach and a listing tool without attributes",
			tools: []*mcp.Tool{
				marked("tables", listsMark("table")),
				marked("query", objMark(map[string]any{"type": "table", "reach": "credential"})),
			},
			wantOK: true,
			wantSpec: &ObjectsSpec{Type: "table", List: ObjectListSpec{Tool: "tables", Attributes: []ObjectAttribute{}},
				Tools: map[string]ToolReach{"query": {Reach: ReachCredential}}},
		},
		{
			name: "two types",
			tools: append(demoTools(),
				marked("project_read", objMark(map[string]any{"type": "project", "argument": "project"}))),
			wantOK:     true,
			wantFaults: []string{"2 object types: project, repo"},
		},
		{
			name:       "no listing tool",
			tools:      []*mcp.Tool{marked("repo_read", objMark(map[string]any{"type": "repo", "argument": "repo"}))},
			wantOK:     true,
			wantFaults: []string{"ghapp marks objects, but no tool carries an ai.straza/lists-objects mark"},
		},
		{
			name:       "two listing tools",
			tools:      append(demoTools(), marked("repo_list2", listsMark("repo"))),
			wantOK:     true,
			wantFaults: []string{"2 listing tools: repo_list, repo_list2"},
		},
		{
			name:       "object mark not a mapping",
			tools:      append(demoTools(), marked("x", map[string]any{markObject: "repo"})),
			wantOK:     true,
			wantFaults: []string{"The ai.straza/object mark on x does not parse"},
		},
		{
			name:       "object mark without a type",
			tools:      append(demoTools(), marked("x", objMark(map[string]any{"reach": "all"}))),
			wantOK:     true,
			wantFaults: []string{"The ai.straza/object mark on x does not parse"},
		},
		{
			name:       "object mark with both forms",
			tools:      append(demoTools(), marked("x", objMark(map[string]any{"type": "repo", "reach": "all", "argument": "repo"}))),
			wantOK:     true,
			wantFaults: []string{"The ai.straza/object mark on x carries both an argument and a reach"},
		},
		{
			name:       "object mark with neither form",
			tools:      append(demoTools(), marked("x", objMark(map[string]any{"type": "repo"}))),
			wantOK:     true,
			wantFaults: []string{"The ai.straza/object mark on x does not parse"},
		},
		{
			name:       "object mark with an unknown reach",
			tools:      append(demoTools(), marked("x", objMark(map[string]any{"type": "repo", "reach": "some"}))),
			wantOK:     true,
			wantFaults: []string{`The ai.straza/object mark on x has the reach "some"`},
		},
		{
			name:       "lists mark that does not parse",
			tools:      append(demoTools()[2:], marked("repo_list", map[string]any{markListsObjects: map[string]any{"type": "repo", "attributes": "area"}})),
			wantOK:     true,
			wantFaults: []string{"The ai.straza/lists-objects mark on repo_list does not parse"},
		},
		{
			name: "a type that breaks the manifest rule",
			tools: []*mcp.Tool{
				marked("repo_list", listsMark("Repo")),
				marked("repo_read", objMark(map[string]any{"type": "Repo", "argument": "repo"})),
			},
			wantOK:     true,
			wantFaults: []string{`ghapp's marks break a manifest rule: objects.type "Repo"`},
		},
		{
			name:       "an attribute that breaks the manifest rule",
			tools:      append(demoTools()[2:], marked("repo_list", listsMark("repo", map[string]any{"name": "area", "setBy": "owner"}))),
			wantOK:     true,
			wantFaults: []string{`ghapp's marks break a manifest rule: objects.list.attributes "area" has setBy "owner"`},
		},
		{
			name: "a listing tool that also marks the reach none",
			tools: []*mcp.Tool{
				marked("repo_list", map[string]any{
					markListsObjects: map[string]any{"type": "repo"},
					markObject:       map[string]any{"type": "repo", "reach": "none"},
				}),
				marked("repo_read", objMark(map[string]any{"type": "repo", "argument": "repo"})),
			},
			wantOK:     true,
			wantFaults: []string{"ghapp's marks break a manifest rule: objects.tools.repo_list gives the listing tool the reach none. The listing tool returns every repo, so its reach is all. Write all, or leave repo_list out of objects.tools."},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, ok, faults := DeclaredObjects("ghapp", tc.tools)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if len(faults) != len(tc.wantFaults) {
				t.Fatalf("faults = %q, want %d containing %q", faults, len(tc.wantFaults), tc.wantFaults)
			}
			for i, want := range tc.wantFaults {
				if !strings.Contains(faults[i], want) || !strings.HasSuffix(faults[i], ".") {
					t.Errorf("fault %d = %q, want a sentence containing %q", i, faults[i], want)
				}
			}
			if tc.wantSpec != nil && !reflect.DeepEqual(spec, *tc.wantSpec) {
				t.Errorf("spec = %+v, want %+v", spec, *tc.wantSpec)
			}
		})
	}
}

// TestDeclaredObjectsParsesAsAManifest pins that a fault-free declared
// block passes the manifest's own rules, so a pre-filled draft parses.
func TestDeclaredObjectsParsesAsAManifest(t *testing.T) {
	spec, ok, faults := DeclaredObjects("ghapp", demoTools())
	if !ok || len(faults) > 0 {
		t.Fatalf("ok %v faults %q", ok, faults)
	}
	m, err := Parse(objectsManifest(goodObjects))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	m.Straza.Objects = &spec
	raw, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if _, err := Parse([]byte(raw)); err != nil {
		t.Fatalf("declared block does not parse: %v", err)
	}
}

func TestToolReachAtLeastAsStrict(t *testing.T) {
	arg := func(n string) ToolReach { return ToolReach{Reach: ReachArgument, Argument: n} }
	all, none, cred := ToolReach{Reach: ReachAll}, ToolReach{Reach: ReachNone}, ToolReach{Reach: ReachCredential}
	cases := []struct {
		name         string
		pinned, live ToolReach
		want         bool
	}{
		{"equal argument", arg("repo"), arg("repo"), true},
		{"other argument", arg("name"), arg("repo"), false},
		{"all over argument", all, arg("repo"), true},
		{"all over credential", all, cred, true},
		{"all over none", all, none, true},
		{"argument over none", arg("repo"), none, true},
		{"credential over none", cred, none, true},
		{"none over argument", none, arg("repo"), false},
		{"none over all", none, all, false},
		{"argument over all", arg("repo"), all, false},
		{"credential over argument", cred, arg("repo"), false},
		{"argument over credential", arg("repo"), cred, false},
		{"equal none", none, none, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pinned.AtLeastAsStrict(tc.live); got != tc.want {
				t.Fatalf("%+v.AtLeastAsStrict(%+v) = %v, want %v", tc.pinned, tc.live, got, tc.want)
			}
		})
	}
}

// TestInventoryHashCoversMarks pins that the drift hash covers the
// ai.straza/* marks with the views switch off and on, in any key order,
// and that a server with no marks keeps the hash it had before marks
// existed, byte for byte.
func TestInventoryHashCoversMarks(t *testing.T) {
	argMark := mcp.Meta{"other": "x", markObject: map[string]any{"type": "repo", "argument": "repo"}}
	allMark := mcp.Meta{"other": "x", markObject: map[string]any{"type": "repo", "reach": "all"}}
	two := mcp.Meta{markObject: map[string]any{"type": "repo", "reach": "all"}, "ai.straza/future": 1}
	twoAgain := mcp.Meta{"ai.straza/future": 1, markObject: map[string]any{"reach": "all", "type": "repo"}}
	for _, on := range []bool{false, true} {
		if got := inventoryHash(goldenTools(mcp.Meta{"other": "x"}), nil, on); got != goldenToolsHash {
			t.Errorf("views %v: a server with no marks hashes %s, want the golden %s", on, got, goldenToolsHash)
		}
		hashes := map[string]string{}
		for name, meta := range map[string]mcp.Meta{"argument": argMark, "all": allMark, "two keys": two} {
			h := inventoryHash(goldenTools(meta), nil, on)
			if h == goldenToolsHash {
				t.Errorf("views %v: the %s mark left the hash unchanged", on, name)
			}
			if prev, dup := hashes[h]; dup {
				t.Errorf("views %v: the %s and %s marks hash alike", on, name, prev)
			}
			hashes[h] = name
		}
		if inventoryHash(goldenTools(two), nil, on) != inventoryHash(goldenTools(twoAgain), nil, on) {
			t.Errorf("views %v: the same marks in another key order hash differently", on)
		}
	}
}
