package manager

import (
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// liveRepoTools is a running repo server's tools/list answer: a read-only
// listing tool, a tool whose required string argument repo names a repo, a
// tool with an optional, a numeric and a nested argument, and a search the
// server marks as reaching every repo.
func liveRepoTools() []*mcp.Tool {
	schema := func(props map[string]any, required ...string) map[string]any {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	str := map[string]any{"type": "string"}
	return []*mcp.Tool{
		{Name: "repo_list", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, InputSchema: schema(map[string]any{"cursor": str}),
			Meta: mcp.Meta{markListsObjects: map[string]any{"type": "repo"}}},
		{Name: "repo_read", InputSchema: schema(map[string]any{"repo": str}, "repo"),
			Meta: mcp.Meta{markObject: map[string]any{"type": "repo", "argument": "repo"}}},
		{Name: "pr_list", InputSchema: schema(map[string]any{"repo": str, "count": map[string]any{"type": "integer"},
			"target": map[string]any{"type": "object", "properties": map[string]any{"repo": str}}}, "count", "target")},
		{Name: "search_code", InputSchema: schema(map[string]any{"query": str}, "query"),
			Meta: mcp.Meta{markObject: map[string]any{"type": "repo", "reach": "all"}}},
		{Name: "get_me", InputSchema: schema(nil)},
	}
}

func TestReviewObjects(t *testing.T) {
	arg := func(n string) ToolReach { return ToolReach{Reach: ReachArgument, Argument: n} }
	all, none := ToolReach{Reach: ReachAll}, ToolReach{Reach: ReachNone}
	pinned := func(list string, tools map[string]ToolReach) ObjectsSpec {
		return ObjectsSpec{Type: "repo", List: ObjectListSpec{Tool: list}, Tools: tools}
	}
	notReadOnly := liveRepoTools()
	notReadOnly[0].Annotations = nil
	decoyLister := append(liveRepoTools(), &mcp.Tool{Name: "whoami", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}})
	cases := []struct {
		name      string
		spec      ObjectsSpec
		tools     []*mcp.Tool
		refusals  []string // each refusal must contain the matching text
		undecided []string
		unknown   []string
	}{
		{
			name:      "the server's own marks",
			spec:      pinned("repo_list", map[string]ToolReach{"repo_read": arg("repo"), "search_code": all}),
			undecided: []string{"get_me", "pr_list"},
		},
		{
			name: "every tool decided, one stricter than its mark",
			spec: pinned("repo_list", map[string]ToolReach{"repo_read": all, "search_code": all, "pr_list": all, "get_me": none}),
		},
		{
			name:      "a listing tool the server lacks",
			spec:      pinned("repos", map[string]ToolReach{"repo_read": arg("repo"), "search_code": all}),
			refusals:  []string{"ghapp lists no tool named repos, so Straza cannot list its repos with it"},
			undecided: []string{"get_me", "pr_list", "repo_list"},
		},
		{
			name:      "a listing tool not marked read-only",
			spec:      pinned("repo_list", map[string]ToolReach{"repo_read": arg("repo"), "search_code": all}),
			tools:     notReadOnly,
			refusals:  []string{"repo_list is not marked read-only, so Straza will not call it to list repos"},
			undecided: []string{"get_me", "pr_list"},
		},
		{
			name: "arguments that are optional, numeric, nested or missing",
			spec: pinned("repo_list", map[string]ToolReach{"repo_read": arg("repo"), "search_code": all,
				"pr_list": arg("repo"), "get_me": arg("repo")}),
			refusals: []string{
				"get_me has no argument repo that is a required top-level string",
				"pr_list has no argument repo that is a required top-level string",
			},
		},
		{
			name: "a pinned mark looser than the server's",
			spec: pinned("repo_list", map[string]ToolReach{"repo_read": none, "search_code": arg("query"), "pr_list": all, "get_me": none}),
			refusals: []string{
				"ghapp marks repo_read with the reach argument repo, and the pinned reach none is looser",
				"ghapp marks search_code with the reach all, and the pinned reach argument query is looser",
			},
		},
		{
			name:      "another read-only tool named the listing tool, with the server's lister pinned none",
			spec:      pinned("whoami", map[string]ToolReach{"repo_list": none, "repo_read": arg("repo"), "search_code": all}),
			tools:     decoyLister,
			refusals:  []string{"ghapp marks repo_list as its listing tool, so its reach is all, and the pinned reach none is looser. Pin all, or leave repo_list out of objects.tools."},
			undecided: []string{"get_me", "pr_list"},
		},
		{
			name:      "tools the server does not list",
			spec:      pinned("repo_list", map[string]ToolReach{"repo_read": arg("repo"), "search_code": all, "pr_merge": arg("repo"), "gone": none}),
			undecided: []string{"get_me", "pr_list"},
			unknown:   []string{"gone", "pr_merge"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := tc.tools
			if tools == nil {
				tools = liveRepoTools()
			}
			refusals, undecided, unknown := ReviewObjects("ghapp", tc.spec, tools)
			if len(refusals) != len(tc.refusals) {
				t.Fatalf("refusals = %q, want %d containing %q", refusals, len(tc.refusals), tc.refusals)
			}
			for i, want := range tc.refusals {
				if !strings.Contains(refusals[i], want) || !strings.HasSuffix(refusals[i], ".") {
					t.Errorf("refusal %d = %q, want a sentence containing %q", i, refusals[i], want)
				}
			}
			if !slices.Equal(undecided, tc.undecided) || !slices.Equal(unknown, tc.unknown) {
				t.Errorf("undecided %q unknown %q, want %q and %q", undecided, unknown, tc.undecided, tc.unknown)
			}
		})
	}
}
