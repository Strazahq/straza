package manager

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

func boolPtr(b bool) *bool { return &b }

// hinted is the golden tool list with ann on its second tool.
func hinted(ann *mcp.ToolAnnotations) []*mcp.Tool {
	tools := goldenTools(nil)
	tools[1].Annotations = ann
	return tools
}

// TestInventoryHashCoversHints pins that the drift hash covers the four
// annotation hints and no title, and that a server that sends no hint
// hashes as its tools alone.
func TestInventoryHashCoversHints(t *testing.T) {
	for _, tc := range []struct {
		name string
		ann  *mcp.ToolAnnotations
	}{
		{"no annotations", nil},
		{"empty annotations", &mcp.ToolAnnotations{}},
		{"a title alone", &mcp.ToolAnnotations{Title: "The time"}},
	} {
		if got := inventoryHash(hinted(tc.ann), nil, false); got != goldenToolsHash {
			t.Errorf("%s: hash %s, want the golden %s", tc.name, got, goldenToolsHash)
		}
	}
	seen := map[string]string{goldenToolsHash: "no hint"}
	for _, tc := range []struct {
		name string
		ann  *mcp.ToolAnnotations
	}{
		{"readOnlyHint", &mcp.ToolAnnotations{ReadOnlyHint: true}},
		{"idempotentHint", &mcp.ToolAnnotations{IdempotentHint: true}},
		{"destructiveHint true", &mcp.ToolAnnotations{DestructiveHint: boolPtr(true)}},
		{"destructiveHint false", &mcp.ToolAnnotations{DestructiveHint: boolPtr(false)}},
		{"openWorldHint true", &mcp.ToolAnnotations{OpenWorldHint: boolPtr(true)}},
		{"openWorldHint false", &mcp.ToolAnnotations{OpenWorldHint: boolPtr(false)}},
	} {
		h := inventoryHash(hinted(tc.ann), nil, false)
		if prev, dup := seen[h]; dup {
			t.Errorf("%s and %s hash alike", tc.name, prev)
		}
		seen[h] = tc.name
	}
	titled := inventoryHash(hinted(&mcp.ToolAnnotations{ReadOnlyHint: true, Title: "The time"}), nil, false)
	if plain := inventoryHash(hinted(&mcp.ToolAnnotations{ReadOnlyHint: true}), nil, false); titled != plain {
		t.Error("a title beside a hint changed the hash")
	}
}

// TestInventoryHashFramesFields pins that no text of a server's passes as
// another field or record of the drift hash: each pair of listings below
// writes the same bytes once the fields are only joined, and must hash
// apart.
func TestInventoryHashFramesFields(t *testing.T) {
	object := map[string]any{"type": "object"}
	tool := func(name, description string, edit func(*mcp.Tool)) *mcp.Tool {
		tl := &mcp.Tool{Name: name, Description: description, InputSchema: object}
		if edit != nil {
			edit(tl)
		}
		return tl
	}
	readOnly := func(tl *mcp.Tool) { tl.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true} }
	for _, tc := range []struct {
		name string
		a, b []*mcp.Tool
	}{
		{"a name that spells a hint record",
			[]*mcp.Tool{tool("a", "d", nil), tool("hints\x00true\x00unset\x00false\x00unset\x00z", "d", nil)},
			[]*mcp.Tool{tool("a", "d", readOnly), tool("z", "d", nil)}},
		{"a description that spells the next tool",
			[]*mcp.Tool{tool("a", "d\x00{\"type\":\"object\"}\x00z\x00d", nil)},
			[]*mcp.Tool{tool("a", "d", nil), tool("z", "d", nil)}},
		{"a mark key that spells a second mark",
			[]*mcp.Tool{tool("a", "d", func(tl *mcp.Tool) { tl.Meta = mcp.Meta{markObject + "\x00\"x\"\x00ai.straza/z": "y"} })},
			[]*mcp.Tool{tool("a", "d", func(tl *mcp.Tool) { tl.Meta = mcp.Meta{markObject: "x", "ai.straza/z": "y"} })}},
	} {
		if inventoryHash(tc.a, nil, false) == inventoryHash(tc.b, nil, false) {
			t.Errorf("%s: the two listings hash alike", tc.name)
		}
	}
}

// TestChangedTools pins which tools of two listings read as changed, and
// the words for each part.
func TestChangedTools(t *testing.T) {
	tool := func(name string, edit func(*mcp.Tool)) *mcp.Tool {
		tl := &mcp.Tool{Name: name, Description: "d", InputSchema: map[string]any{"type": "object"}}
		if edit != nil {
			edit(tl)
		}
		return tl
	}
	for _, tc := range []struct {
		name       string
		prev, next *mcp.Tool
		want       []string // nil when the tool does not read as changed
	}{
		{"the same tool", tool("a", nil), tool("a", nil), nil},
		{"a description", tool("a", nil), tool("a", func(tl *mcp.Tool) { tl.Description = "other" }), []string{"description"}},
		{"an input schema", tool("a", nil), tool("a", func(tl *mcp.Tool) {
			tl.InputSchema = map[string]any{"type": "object", "required": []any{"x"}}
		}), []string{"input schema"}},
		{"a mark", tool("a", nil), tool("a", func(tl *mcp.Tool) {
			tl.Meta = mcp.Meta{markObject: map[string]any{"type": "repo", "reach": "all"}}
		}), []string{"marks"}},
		{"a _meta key that is no mark", tool("a", nil), tool("a", func(tl *mcp.Tool) { tl.Meta = mcp.Meta{"other": 1} }), nil},
		{"a title and an output schema", tool("a", nil), tool("a", func(tl *mcp.Tool) {
			tl.Title, tl.OutputSchema = "A", map[string]any{"type": "object"}
			tl.Annotations = &mcp.ToolAnnotations{Title: "A"}
		}), nil},
		{"a hint set", tool("a", nil), tool("a", func(tl *mcp.Tool) {
			tl.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
		}), []string{"readOnlyHint is now true"}},
		{"a hint taken back", tool("a", func(tl *mcp.Tool) {
			tl.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolPtr(true)}
		}), tool("a", nil), []string{"readOnlyHint is now false", "destructiveHint is now unset"}},
		{"every hint and the description", tool("a", func(tl *mcp.Tool) {
			tl.Annotations = &mcp.ToolAnnotations{DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(true)}
		}), tool("a", func(tl *mcp.Tool) {
			tl.Description = "other"
			tl.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)}
		}), []string{"description", "readOnlyHint is now true", "destructiveHint is now false", "idempotentHint is now true", "openWorldHint is now false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := []*mcp.Tool{tool("gone", nil), tc.prev}
			next := []*mcp.Tool{tc.next, tool("new", nil)}
			got := changedTools(prev, next)
			var want []toolChange
			if tc.want != nil {
				want = []toolChange{{name: "a", parts: tc.want}}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("changedTools = %v, want %v", got, want)
			}
			same := inventoryHash([]*mcp.Tool{tc.prev}, nil, false) == inventoryHash([]*mcp.Tool{tc.next}, nil, false)
			if same != (tc.want == nil) {
				t.Errorf("the hashes are equal = %v while the tool reads changed = %v", same, tc.want != nil)
			}
		})
	}
}

// TestDriftDetail pins the health reason of a drift for each kind of
// change, and that it names at most maxChangedShown changed tools.
func TestDriftDetail(t *testing.T) {
	flip := toolChange{name: "delete_repo", parts: []string{"readOnlyHint is now true"}}
	text := toolChange{name: "list_repos", parts: []string{"description", "input schema"}}
	var many []toolChange
	for i := range maxChangedShown + 4 {
		many = append(many, toolChange{name: fmt.Sprintf("t%02d", i), parts: []string{"description"}})
	}
	for _, tc := range []struct {
		name           string
		added, removed []string
		changed        []toolChange
		views          []string
		links          bool
		want           string
	}{
		{"tools added and removed", []string{"create_release"}, []string{"delete_repo"}, nil, nil, false,
			"inventory drift: +[create_release] -[delete_repo]"},
		{"changed tools", []string{}, []string{}, []toolChange{flip, text}, nil, false,
			"inventory drift: the server changed delete_repo (readOnlyHint is now true), list_repos (description, input schema)"},
		{"an added tool beside a changed one", []string{"create_release"}, []string{}, []toolChange{flip}, nil, false,
			"inventory drift: +[create_release] -[], changed delete_repo (readOnlyHint is now true)"},
		{"changed tools before changed views", []string{}, []string{}, []toolChange{flip}, []string{"ui://a"}, true,
			"inventory drift: the server changed delete_repo (readOnlyHint is now true)"},
		{"changed views", []string{}, []string{}, nil, []string{"ui://a", "ui://b"}, true,
			"inventory drift: the server changed the views ui://a, ui://b"},
		{"changed links", []string{}, []string{}, nil, nil, true,
			"inventory drift: the server changed which views its tools open"},
		{"more changed tools than the reason names", []string{}, []string{}, many, nil, false,
			"inventory drift: the server changed t00 (description), t01 (description), t02 (description), t03 (description), t04 (description), " +
				"t05 (description), t06 (description), t07 (description), t08 (description), t09 (description) and 4 more"},
	} {
		if got := driftDetail(tc.added, tc.removed, tc.changed, tc.views, tc.links); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestDriftNamesAChangedTool pins the alarm end to end: a probe that finds a
// flipped hint and a new description degrades the server with a reason that
// names both tools, and raises one straza.apps.drift whose changed list
// holds them, while its added and removed lists stay empty.
func TestDriftNamesAChangedTool(t *testing.T) {
	done := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}
	set := func(srv *mcp.Server, name, description string, ann *mcp.ToolAnnotations) {
		srv.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: map[string]any{"type": "object"}, Annotations: ann}, done)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "hints", Version: "1.0.0"}, nil)
	set(srv, "delete_repo", "delete a repo", &mcp.ToolAnnotations{DestructiveHint: boolPtr(true)})
	set(srv, "list_repos", "list repos", &mcp.ToolAnnotations{ReadOnlyHint: true})
	set(srv, "same", "stays", nil)
	up := newViewsUpstream(t, srv)
	mgr, sink, _ := viewsManager(t)
	ctx := context.Background()
	if _, err := mgr.Install(ctx, viewsManifest(t, "hints", up.URL, `["*"]`, false), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "hints", StatusRunning)

	set(srv, "delete_repo", "delete a repo", &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolPtr(true)})
	set(srv, "list_repos", "list every repo", &mcp.ToolAnnotations{ReadOnlyHint: true})
	v, _ := mgr.HealthCheckOne(ctx, "hints")
	const want = "inventory drift: the server changed delete_repo (readOnlyHint is now true), list_repos (description)"
	if v.Status != StatusDegraded || v.Detail != want {
		t.Errorf("status = %s with the reason %q, want degraded with %q", v.Status, v.Detail, want)
	}
	if got := sink.count("straza.apps.drift"); got != 1 {
		t.Fatalf("%d drift events, want 1", got)
	}
	ev, _ := sink.find("straza.apps.drift")
	if got, _ := ev.data["changed"].([]string); strings.Join(got, ",") != "delete_repo,list_repos" {
		t.Errorf("the event's changed list is %v, want delete_repo and list_repos", ev.data["changed"])
	}
	for _, key := range []string{"added", "removed"} {
		if got, ok := ev.data[key].([]string); !ok || len(got) != 0 {
			t.Errorf("the event's %s list is %v, want an empty list", key, ev.data[key])
		}
	}
}
