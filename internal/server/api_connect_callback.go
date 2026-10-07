package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// The two halves of finishing a caller's own sign-in. The provider sends the
// browser to GET /v1/connect/callback, which no session can reach, so that
// half redeems and stores nothing: a signed state proves who started a
// sign-in, never who is finishing it. The Credentials tab then posts the code
// and the state with the person's session, and the sign-in is stored only
// when the state names the user of that session.

// connectPagePath is the Credentials tab of the self-service page, where a
// person starts a sign-in and where the provider's answer is finished.
const connectPagePath = "/self-service/credentials"

// handleConnectCallback is the provider's return address. It redeems
// nothing and stores nothing: it moves the provider's answer behind the # of
// the Credentials tab's address, a fixed same-origin path built from no part
// of the request. GET /v1/connect/callback.
func (a *App) handleConnectCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hand := url.Values{}
	switch {
	case q.Get("error") != "":
		hand.Set("error", q.Get("error"))
	case q.Get("code") != "" && q.Get("state") != "":
		hand.Set("code", q.Get("code"))
		hand.Set("state", q.Get("state"))
	default:
		connectPage(w, http.StatusBadRequest, "Nothing to finish",
			"This address is where a provider sends your browser after a sign-in, and it was opened without one. Start the sign-in again from your credentials page.")
		return
	}
	w.Header().Set("Location", connectPagePath+"#connect&"+hand.Encode())
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusSeeOther)
}

// connectFinishRequest is the body of POST /v1/connect/callback: the code
// and the state the provider handed the person's browser.
type connectFinishRequest struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

// handleConnectFinish redeems a provider code for the signed-in user and
// seals the grant. The state must name that user: a state another user
// started is refused before the provider is asked, with one identity record
// naming both. POST /v1/connect/callback.
func (a *App) handleConnectFinish(w http.ResponseWriter, r *http.Request, userID string) {
	var req connectFinishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" || req.State == "" {
		apiError(w, http.StatusBadRequest, "the body must be a JSON object with code and state")
		return
	}
	claims, err := a.tokens.VerifyConnectState(req.State)
	if err != nil {
		apiError(w, http.StatusBadRequest, fmt.Sprintf(
			"This sign-in link is invalid or older than %d minutes, so nothing was stored. Press the Sign in button on the server's row again",
			int(authn.ConnectStateTTL.Minutes())))
		return
	}
	row, rowErr := a.store.Apps().GetByID(r.Context(), claims.App)
	var view manager.AppView
	managed := false
	if rowErr == nil {
		view, managed = a.manager.View(row.Name)
	}
	providerName := ""
	if managed {
		providerName = oauthProviderOf(view)
	}
	if claims.Subject != userID {
		server := "an MCP server that no longer exists"
		data := map[string]any{
			"action": "oauth.connect.refused", "user": claims.Subject, "setBy": userID,
			"reason": "the state names another user",
		}
		if rowErr == nil {
			server = row.Name
			data["app"] = row.Name
		}
		if providerName != "" {
			data["provider"] = providerName
		}
		a.emitEvent(r, "straza.identity.updated", data)
		apiError(w, http.StatusForbidden, fmt.Sprintf(
			"%s started this sign-in to %s, and you are signed in as %s, so nothing was stored. If someone sent you the link, tell an administrator. To connect your own account, start the sign-in yourself",
			a.setterName(r.Context(), claims.Subject), server, a.setterName(r.Context(), userID)))
		return
	}
	if rowErr != nil {
		apiError(w, http.StatusBadRequest, "The MCP server this sign-in was started for was removed after the sign-in started, so nothing was stored. If it was added again, press the Sign in button on its row again")
		return
	}
	if providerName == "" {
		apiError(w, http.StatusBadRequest, fmt.Sprintf(
			"The MCP server %s no longer gives each caller their own sign-in, so nothing was stored", row.Name))
		return
	}
	p, ok := a.oauthProviders[providerName]
	if !ok {
		apiError(w, http.StatusConflict, fmt.Sprintf(
			"The provider %s is no longer configured on this Straza server, so nothing was stored. Ask an administrator to add oauth.providers.%s to the strazad config",
			providerName, providerName))
		return
	}

	grant, expiry, err := secrets.ExchangeCode(r.Context(), a.oauthHTTP, p, req.Code, a.connectRedirectURI())
	if err != nil {
		a.fail(w, r, http.StatusBadGateway, fmt.Sprintf(
			"%s rejected the sign-in code, so nothing was stored. A code works once and for a short time. Press Sign in with %s again",
			providerName, providerName), err)
		return
	}
	meta := secrets.GrantMeta{
		Provider:  providerName,
		Scopes:    view.Manifest.Straza.Credential.OAuth.Scopes,
		ExpiresAt: expiry,
	}
	// A reconnect keeps the owner's agents opt-in.
	if prev, ok, err := a.broker.UserRowFor(r.Context(), row.ID, userID); err == nil && ok {
		meta.AllowAgents = prev.Meta.AllowAgents
	}
	if _, err := a.broker.SetGrant(r.Context(), row.ID, userID, grant, meta); err != nil {
		// A server added again under a removed name is a new row with a new
		// id, so a grant that names the id read before the removal meets no
		// row and the store refuses it, which is the one way the row read
		// above is gone by now.
		if _, gone := a.store.Apps().GetByID(r.Context(), row.ID); errors.Is(gone, store.ErrNotFound) {
			apiError(w, http.StatusConflict, fmt.Sprintf(
				"The MCP server %s was removed and added again while this sign-in was finishing, so nothing was stored. Press the Sign in button on its row again", row.Name))
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "The sign-in could not be stored, so nothing changed. Press the Sign in button again", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "oauth.connect", "user": userID, "app": row.Name, "provider": providerName,
	})
	// Convergence hint: other pods reload the broker cache so the
	// fresh grant serves calls on any pod, not just the one that connected.
	a.emitEvent(r, "straza.apps.updated", map[string]any{"change": "grant", "app": row.Name})
	writeJSON(w, http.StatusOK, map[string]any{
		"app": row.Name, "provider": providerName, "kind": manager.CredentialOAuth, "allow_agents": meta.AllowAgents,
	})
}

// connectPage renders the tiny browser-facing answer for a callback address
// opened with nothing to finish. No external assets (works air-gapped), no
// dynamic values beyond the escaped message.
func connectPage(w http.ResponseWriter, code int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Straza: %s</title>
<body style="font-family:system-ui,sans-serif;max-width:34rem;margin:15vh auto;padding:0 1rem">
<h1 style="font-size:1.3rem">%s</h1><p>%s</p></body>`,
		html.EscapeString(title), html.EscapeString(title), msg)
}
