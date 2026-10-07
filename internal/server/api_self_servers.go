package server

import (
	"context"
	"net/http"
	"time"
)

// serverRow is one entry of GET /v1/self/servers: a managed app the acting
// user reaches through a role or holds a caller-kind row on, with the row
// fields the connect list answers and never a value. agents and
// allow_agents are present on caller kinds only.
type serverRow struct {
	App      string `json:"app"`
	Runtime  string `json:"runtime"`
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Agents   string `json:"agents,omitempty"`
	Reached  bool   `json:"reached"`
	connectRow
}

// handleSelfServers answers one row per managed app the acting user reaches
// or is connected to, in app name order, for the credentials page. An app
// of credential kind none lists when reached; an app that is neither
// reached nor connected is left out. GET /v1/self/servers, with user= for
// a sponsor or an administrator under the connect routes' rule.
func (a *App) handleSelfServers(w http.ResponseWriter, r *http.Request, userID string) {
	target, refusal, err := a.actFor(r.Context(), userID, r.URL.Query().Get("user"))
	if err != nil {
		apiError(w, http.StatusNotFound, err.Error())
		return
	}
	if refusal != "" {
		apiError(w, http.StatusForbidden, refusal)
		return
	}
	reached, err := a.reachedApps(r.Context(), target.ID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the servers could not be listed", err)
		return
	}
	// Views lists apps by name, which is the answer's order.
	out := []serverRow{}
	for _, view := range a.manager.Views() {
		e := serverRow{App: view.Name, Runtime: view.Manifest.Straza.Runtime.Kind, Kind: view.Manifest.CredentialKind(), Reached: reached[view.ID]}
		if view.Manifest.CallerKind() {
			e.Provider = oauthProviderOf(view)
			e.Agents = view.Manifest.AgentsSource()
			if err := a.fillConnectRow(r.Context(), view, target.ID, &e.connectRow); err != nil {
				a.fail(w, r, http.StatusInternalServerError, "the servers could not be listed", err)
				return
			}
		}
		if e.Reached || e.Connected {
			out = append(out, e)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// reachedApps names the ids of the apps that a tool binding on one of the
// user's resolved roles reaches.
func (a *App) reachedApps(ctx context.Context, userID string) (map[string]bool, error) {
	roles, err := a.resolver.ResolveRoles(ctx, userID, time.Now())
	if err != nil {
		return nil, err
	}
	held := make(map[string]bool, len(roles))
	for _, role := range roles {
		held[role.ID] = true
	}
	bindings, err := a.store.ToolBindings().List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, b := range bindings {
		if held[b.RoleID] {
			out[b.AppID] = true
		}
	}
	return out, nil
}
