package server

import (
	"net/http"
	"sort"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
)

// grantOnlyRow is one access row whose listed tools run on the grant alone:
// no policy rule names them for the role.
type grantOnlyRow struct {
	Role      string   `json:"role"`
	App       string   `json:"app"`
	BindingID string   `json:"binding_id"`
	Tools     []string `json:"tools"`
}

// grantOnlyRows finds, for every binding, the tools its matchers admit on
// the app's known inventory for which no rule fires when the engine
// evaluates the role alone with the granted fact off: the decision is then
// the default. Rows come sorted by role, then app.
func grantOnlyRows(views []manager.AppView, bindings []gwBinding, eng *policy.Engine) []grantOnlyRow {
	byApp := make(map[string]manager.AppView, len(views))
	for _, v := range views {
		byApp[v.Name] = v
	}
	rows := []grantOnlyRow{}
	for _, b := range bindings {
		view, ok := byApp[b.App]
		if !ok {
			continue
		}
		var tools []string
		for _, t := range view.Tools {
			if !manager.MatchAnyGlob(b.Matchers, t.Name) {
				continue
			}
			d := eng.Evaluate(policy.Event{
				Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: b.App, ToolName: t.Name,
			}, policy.Subject{Roles: []string{b.Role}})
			if d.Default {
				tools = append(tools, t.Name)
			}
		}
		if len(tools) > 0 {
			rows = append(rows, grantOnlyRow{Role: b.Role, App: b.App, BindingID: b.ID, Tools: tools})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Role != rows[j].Role {
			return rows[i].Role < rows[j].Role
		}
		return rows[i].App < rows[j].App
	})
	return rows
}

// handleAccessGrantOnly answers GET /v1/admin/access/grant-only: every
// access row with tools that run on the grant alone, the check straza
// doctor prints after the upgrade that made grants run without a rule.
func (a *App) handleAccessGrantOnly(w http.ResponseWriter, _ *http.Request) {
	bindings, _ := a.gateway.bindings.Load().([]gwBinding)
	writeJSON(w, http.StatusOK, map[string]any{
		"rows": grantOnlyRows(a.manager.Views(), bindings, a.snapshots.Current().Engine),
	})
}
