package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// TestObjectsFor pins the declaration accessor: the pinned reach of a named
// tool, all for an unnamed one, drift where the server's own mark is
// stricter than the pin, does not parse or names another type, and the
// block the instance serves now rather than the one its manifest started
// with. A tool marked ai.straza/lists-objects reaches all of the mark's
// type, so a looser pin of it drifts.
func TestObjectsFor(t *testing.T) {
	mark := func(m map[string]any) mcp.Meta { return mcp.Meta{markObject: m} }
	lists := func(typ string) mcp.Meta { return mcp.Meta{markListsObjects: map[string]any{"type": typ}} }
	pinned := &ObjectsSpec{Type: "repo", Plural: "repos", List: ObjectListSpec{Tool: "repo_list"}, Tools: map[string]ToolReach{
		"pr_create":     {Reach: ReachArgument, Argument: "repo"},
		"pr_merge":      {Reach: ReachArgument, Argument: "repo"},
		"whoami":        {Reach: ReachNone},
		"repo_new":      {Reach: ReachAll},
		"pr_broken":     {Reach: ReachArgument, Argument: "repo"},
		"pr_typed":      {Reach: ReachArgument, Argument: "repo"},
		"all_typed":     {Reach: ReachAll},
		"lister_none":   {Reach: ReachNone},
		"lister_cred":   {Reach: ReachCredential},
		"lister_arg":    {Reach: ReachArgument, Argument: "repo"},
		"lister_all":    {Reach: ReachAll},
		"lister_typed":  {Reach: ReachAll},
		"lister_broken": {Reach: ReachAll},
		"plain_none":    {Reach: ReachNone},
	}}
	repo := repoSchema("repo", true)
	inv := []*mcp.Tool{
		{Name: "pr_create", Meta: mark(map[string]any{"type": "repo", "argument": "repo"}), InputSchema: repo},
		{Name: "pr_merge", Meta: mark(map[string]any{"type": "repo", "argument": "repository"}), InputSchema: repo},
		{Name: "whoami", Meta: mark(map[string]any{"type": "repo", "argument": "repo"})},
		{Name: "repo_new", Meta: mark(map[string]any{"type": "repo", "argument": "repo"})},
		{Name: "repo_list"},
		{Name: "pr_broken", Meta: mcp.Meta{markObject: "argument repo"}, InputSchema: repo},
		{Name: "pr_typed", Meta: mark(map[string]any{"type": "project", "argument": "repo"}), InputSchema: repo},
		{Name: "all_typed", Meta: mark(map[string]any{"type": "project", "reach": "none"})},
		{Name: "lister_none", Meta: lists("repo")},
		{Name: "lister_cred", Meta: lists("repo")},
		{Name: "lister_arg", Meta: lists("repo"), InputSchema: repo},
		{Name: "lister_all", Meta: lists("repo")},
		{Name: "lister_free", Meta: lists("repo")},
		{Name: "lister_typed", Meta: lists("project")},
		{Name: "lister_broken", Meta: mcp.Meta{markListsObjects: "repo"}},
		{Name: "plain_none"},
		nil,
	}
	m := New(Options{})
	m.byName["ghapp"] = &instance{app: store.App{ID: "app-1", Name: "ghapp"}, objects: pinned, inventory: inv,
		manifest: Manifest{Straza: Extensions{Objects: &ObjectsSpec{Type: "project"}}}}
	m.byName["plain"] = &instance{app: store.App{ID: "app-2", Name: "plain"}}
	cases := []struct {
		server, tool string
		want         ObjectsCall
		ok           bool
	}{
		{"ghapp", "pr_create", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachArgument, Argument: "repo"}}, true},
		{"ghapp", "pr_merge", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachArgument, Argument: "repo"}, Drifted: true}, true},
		{"ghapp", "whoami", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachNone}, Drifted: true}, true},
		{"ghapp", "repo_new", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachAll}}, true},
		{"ghapp", "repo_list", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachAll}}, true},
		{"ghapp", "not_listed", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachAll}}, true},
		{"ghapp", "pr_broken", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachArgument, Argument: "repo"}, Drifted: true}, true},
		{"ghapp", "pr_typed", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachArgument, Argument: "repo"}, Drifted: true}, true},
		{"ghapp", "all_typed", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachAll}, Drifted: true}, true},
		{"ghapp", "lister_none", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachNone}, Drifted: true}, true},
		{"ghapp", "lister_cred", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachCredential}, Drifted: true}, true},
		{"ghapp", "lister_arg", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachArgument, Argument: "repo"}, Drifted: true}, true},
		{"ghapp", "lister_all", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachAll}}, true},
		{"ghapp", "lister_free", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachAll}}, true},
		{"ghapp", "lister_typed", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachAll}, Drifted: true}, true},
		{"ghapp", "lister_broken", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachAll}, Drifted: true}, true},
		{"ghapp", "plain_none", ObjectsCall{AppID: "app-1", Server: "ghapp", Type: "repo", Plural: "repos", Reach: ToolReach{Reach: ReachNone}}, true},
		{"plain", "pr_create", ObjectsCall{}, false},
		{"ghost", "pr_create", ObjectsCall{}, false},
	}
	for _, tc := range cases {
		got, ok := m.ObjectsFor(tc.server, tc.tool)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ObjectsFor(%s, %s) = %+v, %v; want %+v, %v", tc.server, tc.tool, got, ok, tc.want, tc.ok)
		}
	}
}

// repoSchema is an input schema with the string property arg, listed in
// required when required is set.
func repoSchema(arg string, required bool) map[string]any {
	s := map[string]any{"type": "object", "properties": map[string]any{arg: map[string]any{"type": "string"}}}
	if required {
		s["required"] = []any{arg}
	}
	return s
}

// TestObjectsForSchema pins drift from the live input schema: a tool whose
// pinned argument is no longer a required top-level string of its schema
// has drifted, whatever its mark says, as the publish review refuses it. A
// tool with no schema drifts too, because the review refuses it as well.
func TestObjectsForSchema(t *testing.T) {
	mark := mcp.Meta{markObject: map[string]any{"type": "repo", "argument": "repo"}}
	cases := []struct {
		name    string
		tool    *mcp.Tool
		drifted bool
	}{
		{"an unchanged schema", &mcp.Tool{Name: "pr_create", Meta: mark, InputSchema: repoSchema("repo", true)}, false},
		{"an unchanged schema with no mark", &mcp.Tool{Name: "pr_create", InputSchema: repoSchema("repo", true)}, false},
		{"a renamed argument under the old mark", &mcp.Tool{Name: "pr_create", Meta: mark, InputSchema: repoSchema("repository", true)}, true},
		{"a renamed argument with no mark", &mcp.Tool{Name: "pr_create", InputSchema: repoSchema("repository", true)}, true},
		{"an argument made optional", &mcp.Tool{Name: "pr_create", Meta: mark, InputSchema: repoSchema("repo", false)}, true},
		{"an argument that is no longer a string", &mcp.Tool{Name: "pr_create", Meta: mark, InputSchema: map[string]any{"type": "object",
			"properties": map[string]any{"repo": map[string]any{"type": "integer"}}, "required": []any{"repo"}}}, true},
		{"no schema", &mcp.Tool{Name: "pr_create", Meta: mark}, true},
	}
	pinned := &ObjectsSpec{Type: "repo", List: ObjectListSpec{Tool: "repo_list"}, Tools: map[string]ToolReach{
		"pr_create": {Reach: ReachArgument, Argument: "repo"},
	}}
	lister := &mcp.Tool{Name: "repo_list", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv := []*mcp.Tool{lister, tc.tool}
			m := New(Options{})
			m.byName["ghapp"] = &instance{app: store.App{ID: "app-1", Name: "ghapp"}, objects: pinned, inventory: inv}
			got, ok := m.ObjectsFor("ghapp", "pr_create")
			if !ok || got.Drifted != tc.drifted {
				t.Errorf("ObjectsFor = %+v, %v; want drifted %v", got, ok, tc.drifted)
			}
			refusals, _, _ := ReviewObjects("ghapp", *pinned, inv)
			if review := len(refusals) > 0; review != tc.drifted {
				t.Errorf("the publish review refuses %v (%q), and the call-time check drifts %v: the two must agree", review, refusals, tc.drifted)
			}
		})
	}
}

// TestCallMeta pins that both runtimes send CallInput.Meta as the call's
// _meta, and send none when it is nil.
func TestCallMeta(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "meta", Version: "1.0.0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "meta", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			meta, _ := json.Marshal(req.Params.Meta)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(meta)}}}, nil
		})
	up := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(up.Close)
	remote := besideRuntime("meta", RemoteSpec{URL: up.URL}, nil)
	t.Cleanup(remote.Stop)
	command := startCommandRuntime(t, helperSpec(t, EnvVar{Name: "STRAZA_HELPER_META", Value: "1"}))
	for name, rt := range map[string]Runtime{"remote": remote, "command": command} {
		for _, tc := range []struct {
			meta map[string]any
			want string
		}{
			{map[string]any{"ai.straza/object-id": "101"}, `{"ai.straza/object-id":"101"}`},
			{nil, `null`},
		} {
			res, err := rt.Call(context.Background(), CallInput{Tool: "meta", Meta: tc.meta})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := textOf(res); got != tc.want {
				t.Errorf("%s runtime sent _meta %s, want %s", name, got, tc.want)
			}
		}
	}
}
