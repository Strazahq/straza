package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// Each caller's own connection. A person connects their own upstream
// account to a caller-kind
// app: on an oauth-kind app strazad runs the auth-code flow server-side
// (the provider client secret and the resulting tokens never reach any
// client), on a token-kind app the person pastes a token the server tests
// once and seals. Either way the gateway thereafter calls upstream as that
// person. The OAuth `state` parameter is a signed purpose-scoped token, so
// the signed-in finish in api_connect_callback.go verifies it on any pod.

// buildOAuthProviders resolves config provider registrations (reading
// clientSecretFile when set) into the broker's client configs.
func buildOAuthProviders(cfg config.Config) (map[string]secrets.ProviderConfig, error) {
	out := make(map[string]secrets.ProviderConfig, len(cfg.OAuth.Providers))
	for name, p := range cfg.OAuth.Providers {
		secret := p.ClientSecret
		if p.ClientSecretFile != "" {
			raw, err := os.ReadFile(p.ClientSecretFile) // #nosec G304 -- operator-configured secret path
			if err != nil {
				return nil, fmt.Errorf("oauth.providers.%s: read clientSecretFile: %w", name, err)
			}
			secret = string(bytes.TrimSpace(raw))
		}
		out[name] = secrets.ProviderConfig{
			Name: name, ClientID: p.ClientID, ClientSecret: secret,
			AuthURL: p.AuthURL, TokenURL: p.TokenURL, Scopes: p.Scopes,
		}
	}
	return out, nil
}

// requireUser authenticates a session token with no role requirement and
// hands the handler the calling user's id, with the device the session
// started on in the request context for refuseMachineWrite. The session is
// judged by judgeSession as the admin plane judges it, on every request. It
// admits an AI agent, because straza connect calls these routes on the
// agent's own session.
func (a *App) requireUser(next func(w http.ResponseWriter, r *http.Request, userID string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, scheme := sessionCredential(r)
		if raw == "" {
			apiError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := a.verifySessionToken(w, r, raw, scheme)
		if errors.Is(err, errAnswered) {
			return
		}
		if err != nil || claims.Session == "" {
			apiError(w, http.StatusUnauthorized, "session token rejected")
			return
		}
		u, _, ok := a.judgeSession(w, r, claims, "connect bearer", claims.Harness)
		if !ok {
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionDeviceKey{}, claims.Device)), u.ID)
	}
}

// sessionDeviceKey carries the device a connect request's session started
// on, empty for a session that began with the person's own sign-in.
type sessionDeviceKey struct{}

// refuseMachineWrite answers 403, records one connection.refused event and
// reports true when a write that lets agents in, or that changes another
// user's connection, arrives on a session a machine enrollment started. The
// background service holds such sessions, and any
// program running as the person can use them, an agent included, so only a
// session that began with the person's own sign-in in a browser makes those
// writes. grant is a write that turns the agents opt-in on. An admin API
// token puts no device in the context, so it never meets this rule.
func (a *App) refuseMachineWrite(w http.ResponseWriter, r *http.Request, callerID string, target store.User, app string, grant bool) bool {
	device, _ := r.Context().Value(sessionDeviceKey{}).(string)
	if device == "" || (target.ID == callerID && !grant) {
		return false
	}
	page := strings.TrimRight(a.cfg.Server.PublicURL, "/") + connectPagePath
	reason := "a session a machine started cannot change another user's connection"
	msg := fmt.Sprintf("changing %s's %s connection needs your own sign-in in a browser, because this session was started on a machine, "+
		"where an agent running as you can use it too. Make the change in the Credentials tab of the self-service page, "+
		"which lists the agents you sponsor: %s", target.Username, app, page)
	if target.ID == callerID {
		reason = "a session a machine started cannot allow agents"
		msg = fmt.Sprintf("letting the agents you sponsor use your %s connection needs your own sign-in in a browser, because this session "+
			"was started on a machine, where an agent running as you can use it too. Turn it on in the Credentials tab of the "+
			"self-service page: %s", app, page)
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "connection.refused", "user": target.ID, "setBy": callerID, "app": app, "reason": reason,
	})
	apiError(w, http.StatusForbidden, msg)
	return true
}

// callerApp resolves a managed caller-kind app (oauth or token) by name or
// id: the one kind of app a person or agent has a connection on.
func (a *App) callerApp(r *http.Request) (store.App, manager.AppView, error) {
	row, err := a.appByRefValue(r, "app")
	if err != nil {
		return store.App{}, manager.AppView{}, fmt.Errorf("unknown server")
	}
	view, ok := a.manager.View(row.Name)
	if !ok {
		return store.App{}, manager.AppView{}, fmt.Errorf("the MCP server %s is not managed (install it first)", row.Name)
	}
	if !view.Manifest.CallerKind() {
		return store.App{}, manager.AppView{}, fmt.Errorf("the MCP server %s does not give each caller their own credential (credential kind %s), so there is nothing to connect. An administrator sets its shared secret with strazactl apps secret set %s",
			row.Name, view.Manifest.CredentialKind(), row.Name)
	}
	return row, view, nil
}

// oauthProviderOf names the provider of an oauth-kind view, empty on a
// token-kind one.
func oauthProviderOf(view manager.AppView) string {
	if view.Manifest.CredentialKind() == manager.CredentialOAuth {
		return view.Manifest.Straza.Credential.OAuth.Provider
	}
	return ""
}

func (a *App) appByRefValue(r *http.Request, key string) (store.App, error) {
	ref := r.PathValue(key)
	row, err := a.store.Apps().GetByID(r.Context(), ref)
	if err == nil {
		return row, nil
	}
	return a.store.Apps().GetByName(r.Context(), ref)
}

func (a *App) connectRedirectURI() string {
	return strings.TrimRight(a.cfg.Server.PublicURL, "/") + "/v1/connect/callback"
}

// actFor resolves the user a connect request acts for: the caller when ref
// is empty, else the named user (username or id) when the caller is that
// user's sponsor or holds admin standing (the straza-admin role or a
// delegated apps:write grant). refusal is the sentence for a 403; a user
// that does not exist is an error for a 404.
func (a *App) actFor(ctx context.Context, callerID, ref string) (target store.User, refusal string, err error) {
	caller, err := a.store.Users().GetByID(ctx, callerID)
	if err != nil {
		return store.User{}, "", fmt.Errorf("unknown user")
	}
	if ref == "" || ref == caller.ID || ref == caller.Username {
		return caller, "", nil
	}
	if target, err = a.userByRef(ctx, ref); err != nil {
		return store.User{}, "", err
	}
	if target.Sponsor == caller.Username {
		return target, "", nil
	}
	roles, err := a.resolver.ResolveRoles(ctx, caller.ID, time.Now())
	if err != nil {
		return store.User{}, "", err
	}
	for _, role := range roles {
		if role.Name == AdminRole {
			return target, "", nil
		}
	}
	if a.scopeForRoles(roles).Grants["apps:write"] {
		return target, "", nil
	}
	return store.User{}, fmt.Sprintf("only %s's sponsor or an administrator can manage %s's connections", target.Username, target.Username), nil
}

// connectRequest is the body of POST and PATCH /v1/connect/{app}. Every
// field is optional: a bare POST on an oauth-kind app starts the sign-in.
type connectRequest struct {
	Token       string `json:"token,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	User        string `json:"user,omitempty"`
	AllowAgents *bool  `json:"allow_agents,omitempty"`
}

// tokenMaxBytes caps a pasted token: no known server issues a longer one,
// and a larger paste is a mistake worth refusing in words.
const tokenMaxBytes = 8192

// handleConnectStart connects the acting user to one app. On a token-kind
// app the body carries the pasted token, tested once against the server
// and sealed under the user's id; on an oauth-kind app it answers a person
// with the provider authorization URL for the Credentials tab and that
// tab's address for a terminal, and refuses an agent and an admin API token.
// POST /v1/connect/{app}.
func (a *App) handleConnectStart(w http.ResponseWriter, r *http.Request, c connectCaller) {
	row, view, err := a.callerApp(r)
	if err != nil {
		apiError(w, http.StatusNotFound, err.Error())
		return
	}
	var req connectRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			apiError(w, http.StatusBadRequest, "the body must be a JSON object with token, expires_at and user")
			return
		}
	}
	if view.Manifest.CredentialKind() == manager.CredentialToken {
		a.connectToken(w, r, row, c, req)
		return
	}
	providerName := oauthProviderOf(view)
	if c.token != nil {
		if target, ok := a.connectTarget(w, r, c, req.User); ok {
			apiError(w, http.StatusForbidden, a.tokenSignInRefusal(target, row.Name, providerName))
		}
		return
	}
	userID := c.userID
	if req.User != "" {
		apiError(w, http.StatusBadRequest, fmt.Sprintf(
			"the MCP server %s uses each caller's own sign-in through %s, which needs that person's browser, so nobody can connect on behalf of %s. Set credential.kind token on the server for pasted tokens, or credential.agents sponsor or shared for agents",
			row.Name, providerName, req.User))
		return
	}
	if req.Token != "" {
		apiError(w, http.StatusBadRequest, fmt.Sprintf(
			"the MCP server %s uses sign-in through %s, not a pasted token. Start the sign-in instead", row.Name, providerName))
		return
	}
	p, ok := a.oauthProviders[providerName]
	if !ok {
		apiError(w, http.StatusConflict, fmt.Sprintf(
			"provider %q is not configured. Add oauth.providers.%s to the strazad config", providerName, providerName))
		return
	}
	// A sign-in is stored only for the user of the session that finishes it
	// in a browser, and no browser is signed in to Straza as an agent.
	caller, err := a.store.Users().GetByID(r.Context(), userID)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown user")
		return
	}
	if !personUser(caller) {
		apiError(w, http.StatusForbidden, agentSignInRefusal(caller, row.Name, providerName))
		return
	}
	state, err := a.tokens.MintConnectState(userID, row.ID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not mint connect state", err)
		return
	}
	scopes := view.Manifest.Straza.Credential.OAuth.Scopes
	if len(scopes) == 0 {
		scopes = p.Scopes
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"app":           row.Name,
		"provider":      providerName,
		"authorize_url": secrets.AuthorizeURL(p, a.connectRedirectURI(), state, scopes),
		"page_url":      strings.TrimRight(a.cfg.Server.PublicURL, "/") + connectPagePath,
		"expires_in":    int(authn.ConnectStateTTL.Seconds()),
	})
}

// agentSignInRefusal is the sentence for an agent or a service that starts a
// provider sign-in: why it cannot, and the credential.agents values that do
// give it a credential on the server.
func agentSignInRefusal(caller store.User, server, provider string) string {
	ways := "shared or client_credentials"
	if caller.Sponsor != "" {
		ways = fmt.Sprintf("sponsor so %s runs on %s's sign-in once %s allows it, or to shared or client_credentials",
			caller.Username, caller.Sponsor, caller.Sponsor)
	}
	return fmt.Sprintf("agent %s cannot sign in at %s, because a sign-in is finished in a browser that is signed in to Straza as the same user, and an agent has no such browser. An administrator sets credential.agents on %s to %s",
		caller.Username, provider, server, ways)
}

// connectToken stores a pasted token for the acting user after one probe
// against the server. The value is sealed at once and never echoed; the
// answer carries its fingerprint. One identity record per set names the
// actor, the subject, the app, the fingerprint and the probe outcome.
func (a *App) connectToken(w http.ResponseWriter, r *http.Request, row store.App, c connectCaller, req connectRequest) {
	token := strings.TrimSpace(req.Token)
	switch {
	case token == "":
		apiError(w, http.StatusBadRequest, fmt.Sprintf("a token is required. Paste the one %s gave you", row.Name))
		return
	case len(token) > tokenMaxBytes:
		apiError(w, http.StatusBadRequest, fmt.Sprintf("the token is longer than %d bytes, which no known server issues. Check the paste", tokenMaxBytes))
		return
	}
	var expires *time.Time
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			apiError(w, http.StatusBadRequest, "expires_at must be an RFC 3339 time, for example 2026-12-31T00:00:00Z")
			return
		}
		if !t.After(time.Now()) {
			apiError(w, http.StatusBadRequest, fmt.Sprintf("expires_at %s is already past. Paste a token that is still valid", t.UTC().Format("2006-01-02")))
			return
		}
		t = t.UTC()
		expires = &t
	}
	target, ok := a.connectTarget(w, r, c, req.User)
	if !ok {
		return
	}
	if a.refuseMachineWrite(w, r, c.userID, target, row.Name, false) {
		return
	}
	probeCtx, cancel := context.WithTimeout(r.Context(), a.upstreamTimeout(row.Name))
	defer cancel()
	if err := a.manager.ProbeWith(probeCtx, row.Name, &manager.Secret{ID: "probe", Value: token}); err != nil {
		// A refused address is not the token's fault, so its sentence, which
		// names the administrator's next step, answers alone.
		msg := fmt.Sprintf("the MCP server %s refused the token or did not answer (%v). Check the token and try again", row.Name, err)
		if errors.Is(err, manager.ErrDialRefused) {
			msg = err.Error()
		}
		a.fail(w, r, http.StatusBadGateway, msg, err)
		return
	}
	meta := secrets.GrantMeta{ExpiresAt: expires}
	if target.ID != c.id() {
		meta.SetBy = c.id()
	}
	// A re-paste of a rotated token keeps the owner's agents opt-in.
	if prev, ok, err := a.broker.UserRowFor(r.Context(), row.ID, target.ID); err == nil && ok {
		meta.AllowAgents = prev.Meta.AllowAgents
	}
	cred, err := a.broker.SetToken(r.Context(), row.ID, target.ID, token, meta)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the token could not be stored", err)
		return
	}
	fp := secrets.Fingerprint(token)
	data := c.stamp(map[string]any{
		"action": "token.connect", "user": target.ID, "app": row.Name,
		"credentialId": cred.ID, "fingerprint": fp, "probe": "ok",
	})
	if expires != nil {
		data["expiresAt"] = expires.Format(time.RFC3339)
	}
	a.emitEvent(r, "straza.identity.updated", data)
	a.emitEvent(r, "straza.apps.updated", map[string]any{"change": "grant", "app": row.Name})
	out := map[string]any{
		"app": row.Name, "user": target.Username, "kind": store.CredToken,
		"fingerprint": fp, "allow_agents": meta.AllowAgents,
	}
	if expires != nil {
		out["expires_at"] = expires.Format(time.RFC3339)
	}
	if meta.SetBy != "" {
		out["set_by"] = a.setterName(r.Context(), c.id())
	}
	writeJSON(w, http.StatusOK, out)
}

// handleConnectAgents records the owner's opt-in for their sponsored agents
// on one connection, on either caller kind. PATCH /v1/connect/{app}.
func (a *App) handleConnectAgents(w http.ResponseWriter, r *http.Request, c connectCaller) {
	row, _, err := a.callerApp(r)
	if err != nil {
		apiError(w, http.StatusNotFound, err.Error())
		return
	}
	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AllowAgents == nil {
		apiError(w, http.StatusBadRequest, "the body must be a JSON object with allow_agents true or false")
		return
	}
	target, ok := a.connectTarget(w, r, c, req.User)
	if !ok {
		return
	}
	if a.refuseMachineWrite(w, r, c.userID, target, row.Name, *req.AllowAgents) {
		return
	}
	cred, err := a.broker.SetAllowAgents(r.Context(), row.ID, target.ID, *req.AllowAgents)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, fmt.Sprintf("%s has no %s connection to allow agents on. Connect first", target.Username, row.Name))
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the connection could not be updated", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", c.stamp(map[string]any{
		"action": "connection.agents", "user": target.ID, "app": row.Name,
		"credentialId": cred.ID, "allow": *req.AllowAgents,
	}))
	a.emitEvent(r, "straza.apps.updated", map[string]any{"change": "grant", "app": row.Name})
	writeJSON(w, http.StatusOK, map[string]any{"app": row.Name, "user": target.Username, "allow_agents": *req.AllowAgents})
}

// handleConnectList reports the acting user's connection on every managed
// caller-kind app, never a value: the kind, the app's agents setting, the
// fingerprint of a pasted token, the expiry, who set it and the agents
// opt-in. GET /v1/connect, with user= for a sponsor, an administrator or an
// admin API token.
func (a *App) handleConnectList(w http.ResponseWriter, r *http.Request, c connectCaller) {
	target, ok := a.connectTarget(w, r, c, r.URL.Query().Get("user"))
	if !ok {
		return
	}
	type entry struct {
		App      string `json:"app"`
		Kind     string `json:"kind"`
		Provider string `json:"provider,omitempty"`
		Agents   string `json:"agents"`
		connectRow
	}
	out := []entry{}
	for _, view := range a.manager.Views() {
		if !view.Manifest.CallerKind() {
			continue
		}
		e := entry{App: view.Name, Kind: view.Manifest.CredentialKind(), Provider: oauthProviderOf(view), Agents: view.Manifest.AgentsSource()}
		if err := a.fillConnectRow(r.Context(), view, target.ID, &e.connectRow); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "list credentials failed", err)
			return
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}

// connectRow is the wire shape of one user's row on one caller-kind app,
// shared by the connect list and the servers read, never a value.
// allow_agents is present on every caller-kind entry, row or no row.
type connectRow struct {
	Connected   bool     `json:"connected"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	SetBy       string   `json:"set_by,omitempty"`
	AllowAgents *bool    `json:"allow_agents,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
}

// fillConnectRow copies the user's row on one caller-kind app into e from
// the broker read, naming whoever set it by username. Without a row e
// keeps connected false and allow_agents false.
func (a *App) fillConnectRow(ctx context.Context, view manager.AppView, userID string, e *connectRow) error {
	e.AllowAgents = new(bool)
	ur, ok, err := a.broker.UserRowFor(ctx, view.ID, userID)
	if err != nil || !ok {
		return err
	}
	e.Connected = true
	e.Fingerprint = ur.Fingerprint
	e.UpdatedAt = ur.SetAt.UTC().Format(time.RFC3339)
	e.Scopes = ur.Meta.Scopes
	*e.AllowAgents = ur.Meta.AllowAgents
	if ur.Meta.ExpiresAt != nil {
		e.ExpiresAt = ur.Meta.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if ur.Meta.SetBy != "" {
		e.SetBy = a.setterName(ctx, ur.Meta.SetBy)
	}
	return nil
}

// setterName names whoever set a row for a person: the username while the
// id resolves to a user, the token's name marked as an admin API token while
// it resolves to one, else the id itself.
func (a *App) setterName(ctx context.Context, id string) string {
	if setter, err := a.store.Users().GetByID(ctx, id); err == nil {
		return setter.Username
	}
	if tokens, err := a.apiTokens(ctx); err == nil {
		for _, m := range tokens {
			if m.ID == id {
				return m.Name + " (admin API token)"
			}
		}
	}
	return id
}

// handleConnectDelete removes the acting user's connection on one app,
// grant or token. DELETE /v1/connect/{app}, with user= for a sponsor, an
// administrator or an admin API token.
func (a *App) handleConnectDelete(w http.ResponseWriter, r *http.Request, c connectCaller) {
	row, view, err := a.callerApp(r)
	if err != nil {
		apiError(w, http.StatusNotFound, err.Error())
		return
	}
	target, ok := a.connectTarget(w, r, c, r.URL.Query().Get("user"))
	if !ok {
		return
	}
	if a.refuseMachineWrite(w, r, c.userID, target, row.Name, false) {
		return
	}
	if err := a.broker.DeleteGrant(r.Context(), row.ID, target.ID); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "disconnect failed", err)
		return
	}
	action := "token.disconnect"
	if view.Manifest.CredentialKind() == manager.CredentialOAuth {
		action = "oauth.disconnect"
	}
	a.emitEvent(r, "straza.identity.updated", c.stamp(map[string]any{
		"action": action, "user": target.ID, "app": row.Name, "provider": oauthProviderOf(view),
	}))
	a.emitEvent(r, "straza.apps.updated", map[string]any{"change": "grant", "app": row.Name})
	writeJSON(w, http.StatusOK, map[string]string{"app": row.Name, "user": target.Username, "status": "disconnected"})
}
