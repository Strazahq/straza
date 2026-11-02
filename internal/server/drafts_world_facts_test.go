package server

import (
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestManifestFactsScopes pins the OAuth scopes manifestFacts reads, which
// server.scopes-wider compares: the scopes of an oauth credential as the
// manifest lists them, and none for a credential of another kind.
func TestManifestFactsScopes(t *testing.T) {
	t.Parallel()
	head := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: mf}\nserver: {name: example.com/mf, version: \"1.0.0\"}\n" +
		"straza:\n  runtime: {kind: remote, remote: {url: \"https://api.example.com/mcp\"}}\n"
	cases := []struct {
		name, credential string
		want             []string
	}{
		{"an oauth credential with scopes", "  credential:\n    kind: oauth\n    oauth: {provider: entra, scopes: [repo, read:user]}\n" +
			"    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n", []string{"repo", "read:user"}},
		{"an oauth credential without scopes", "  credential:\n    kind: oauth\n    oauth: {provider: entra}\n" +
			"    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n", nil},
		{"a static credential", "  credential:\n    kind: static\n    inject: {as: header, name: X-Api-Key}\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := appFactsOf(t, head+tc.credential).Scopes; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("scopes %q, want %q", got, tc.want)
			}
		})
	}
}

// TestManifestFactsPinned pins the pinned facts manifestFacts reads, which
// who gains and agent.own-reach compare: the reach of each tool the block
// names, and the argument each argument tool reads its object from.
func TestManifestFactsPinned(t *testing.T) {
	t.Parallel()
	p := appFactsOf(t, objectsManifest("https://repos.example.com/mcp", pinnedRepos)).Pinned
	if p == nil {
		t.Fatal("no pinned facts")
	}
	if want := map[string]string{"repo_read": "argument", "whoami": "none"}; !reflect.DeepEqual(p.Reach, want) {
		t.Errorf("reach %q, want %q", p.Reach, want)
	}
	if want := map[string]string{"repo_read": "repo"}; !reflect.DeepEqual(p.Argument, want) {
		t.Errorf("argument %q, want %q", p.Argument, want)
	}
}

// TestDestructiveTools pins the destructive facts of a running server's
// tools: each tool whose annotations set destructiveHint keeps the value
// they set, true or false, and a tool with no annotations, or annotations
// that leave the hint unset, is left out, so the check tells an explicit
// false from no hint.
func TestDestructiveTools(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	cases := []struct {
		name  string
		tools []*mcp.Tool
		want  map[string]bool
	}{
		{"no tools", nil, nil},
		{"no hint anywhere", []*mcp.Tool{{Name: "ticket_read"}, {Name: "ticket_list", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}}, nil},
		{"an explicit true and an explicit false", []*mcp.Tool{
			{Name: "ticket_close", Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes}},
			{Name: "ticket_comment", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no}},
			{Name: "ticket_read", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			nil,
		}, map[string]bool{"ticket_close": true, "ticket_comment": false}},
	}
	for _, tc := range cases {
		if got := destructiveTools(tc.tools); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: destructive %v, want %v", tc.name, got, tc.want)
		}
	}
}
