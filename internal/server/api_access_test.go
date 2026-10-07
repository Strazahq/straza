package server

import (
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
)

// TestGrantOnlyRows drives the doctor check over a fixture whose grants are
// wider than its rules: the tools no rule names come back per binding, a
// binding every rule covers yields no row, a stopped app yields no row, and
// the rows are sorted by role then app.
func TestGrantOnlyRows(t *testing.T) {
	t.Parallel()
	tools := func(names ...string) []*mcp.Tool {
		out := make([]*mcp.Tool, 0, len(names))
		for _, n := range names {
			out = append(out, &mcp.Tool{Name: n})
		}
		return out
	}
	views := []manager.AppView{
		{Name: "demo-tools", Status: manager.StatusRunning, Tools: tools("echo", "get-sum", "get-env", "get-tiny-image")},
		{Name: "midpoint", Status: manager.StatusRunning, Tools: tools("search", "delete_user")},
	}
	bindings := []gwBinding{
		{ID: "b-dev", App: "demo-tools", Role: "dev-tools", Matchers: []string{"*"}},
		{ID: "b-mid", App: "midpoint", Role: "dev-tools", Matchers: []string{"search"}},
		{ID: "b-test", App: "demo-tools", Role: "test-role", Matchers: []string{"get-*"}},
		{ID: "b-ghost", App: "ghostapp", Role: "test-role", Matchers: []string{"*"}},
	}
	doc, err := policy.Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: dev-guardrails }
spec:
  match: { roles: [dev-tools] }
  rules:
    - id: demo-echo
      tools: [mcp.call]
      apps: [demo-tools]
      toolNames: { allow: ["echo", "get-sum"] }
      effect: allow
    - id: demo-env
      tools: [mcp.call]
      apps: [demo-tools]
      toolNames: { deny: ["get-env"] }
      effect: deny
    - id: midpoint-all
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: { allow: ["*"] }
      effect: allow
`))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := policy.NewEngine([]policy.Document{doc}, policy.EffectAllow)
	if err != nil {
		t.Fatal(err)
	}
	got := grantOnlyRows(views, bindings, eng)
	want := []grantOnlyRow{
		{Role: "dev-tools", App: "demo-tools", BindingID: "b-dev", Tools: []string{"get-tiny-image"}},
		{Role: "test-role", App: "demo-tools", BindingID: "b-test", Tools: []string{"get-sum", "get-env", "get-tiny-image"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v\nwant %+v", got, want)
	}
	if rows := grantOnlyRows(views, nil, eng); len(rows) != 0 {
		t.Errorf("no bindings yielded %+v", rows)
	}
}
