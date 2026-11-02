package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestObjectIDMeta pins the server's half of ai.straza/object-id: a call
// whose id matches the repo's runs, a call without one resolves the name
// alone, and a call whose id differs, as after a delete and a new repo of
// the same name, is refused with a sentence that says to refresh.
func TestObjectIDMeta(t *testing.T) {
	url, _ := startDemo(t)
	s := connect(t, url, testApp)
	cases := []struct {
		name string
		tool string
		meta mcp.Meta
		want string
		err  bool
	}{
		{"the repo's id", "repo_read", mcp.Meta{metaID: "9012331"}, "Strazahq/straza: id 9012331", false},
		{"no id", "repo_read", nil, "Strazahq/straza: id 9012331", false},
		{"another id", "repo_read", mcp.Meta{metaID: "1"},
			"repo_read refused the call: Straza approved the repo with the id 1, and Strazahq/straza has the id 9012331 now, so it was made again after Straza listed it. Refresh the repo list in Straza, then try again", true},
		{"a number for the id", "repo_read", mcp.Meta{metaID: 9012331}, "refused the call", true},
		{"another id on a write", "issue_create", mcp.Meta{metaID: "1"}, "issue_create refused the call", true},
		{"the id on a write", "issue_create", mcp.Meta{metaID: "9012331"}, "Opened issue #1 in Strazahq/straza", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"repo": "Strazahq/straza"}
			if tc.tool == "issue_create" {
				args["title"] = "a title"
			}
			res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Meta: tc.meta, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			for _, c := range res.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					text.WriteString(tc.Text)
				}
			}
			if res.IsError != tc.err || !strings.Contains(text.String(), tc.want) {
				t.Errorf("%s answered %q (error %v), want %q (error %v)", tc.tool, text.String(), res.IsError, tc.want, tc.err)
			}
		})
	}
}
