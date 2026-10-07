// Package connect is the terminal side of a caller's own credential on an
// MCP server: the /v1/connect calls, the wait on a sign-in and the lines that
// straza and strazactl both print, so the two tools cannot drift apart.
package connect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// API is the authenticated strazad client a tool brings: strazactl its
// logged-in session, straza the enrolled caller's session.
type API interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Status is one row of GET /v1/connect: the acting user's connection on one
// MCP server that gives each caller their own credential, never token
// material. Kind is oauth or token, Agents the server's credential.agents
// setting, Fingerprint the first hex characters of a pasted token's hash,
// SetBy the username of whoever set the row when it was not the owner.
type Status struct {
	App         string   `json:"app"`
	Kind        string   `json:"kind"`
	Provider    string   `json:"provider,omitempty"`
	Agents      string   `json:"agents"`
	Connected   bool     `json:"connected"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	SetBy       string   `json:"set_by,omitempty"`
	AllowAgents bool     `json:"allow_agents"`
	Scopes      []string `json:"scopes,omitempty"`
}

// List reports the acting user's connection on every such server. user names
// another user for a sponsor or an administrator; empty is the caller.
func List(ctx context.Context, api API, user string) ([]Status, error) {
	var out []Status
	return out, api.Do(ctx, http.MethodGet, "/v1/connect"+userQuery(user), nil, &out)
}

// TokenResult is the answer of a pasted token: the fingerprint the server
// stored, the expiry as recorded and who set it.
type TokenResult struct {
	App         string `json:"app"`
	User        string `json:"user"`
	Fingerprint string `json:"fingerprint"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	SetBy       string `json:"set_by,omitempty"`
	AllowAgents bool   `json:"allow_agents"`
}

// PasteToken stores a token for one token server: strazad tests it once
// against the upstream, seals it and answers with its fingerprint. expiresAt
// is an RFC 3339 time or empty; user is the agent an administrator or a
// sponsor acts for, or empty for the caller.
func PasteToken(ctx context.Context, api API, server, token, expiresAt, user string) (TokenResult, error) {
	body := map[string]string{"token": token}
	if expiresAt != "" {
		body["expires_at"] = expiresAt
	}
	if user != "" {
		body["user"] = user
	}
	var out TokenResult
	return out, api.Do(ctx, http.MethodPost, "/v1/connect/"+url.PathEscape(server), body, &out)
}

// SetAllowAgents records whether the owner's sponsored agents may use the
// connection on one server.
func SetAllowAgents(ctx context.Context, api API, server, user string, allow bool) error {
	body := map[string]any{"allow_agents": allow}
	if user != "" {
		body["user"] = user
	}
	return api.Do(ctx, http.MethodPatch, "/v1/connect/"+url.PathEscape(server), body, nil)
}

// SignIn runs the caller's own OAuth sign-in for one server from a terminal:
// it asks strazad to start, prints the address of the Credentials tab, where
// the person signs in to Straza and presses the server's Sign in button, then
// polls until the connection lands or the wait runs out. Progress goes to w,
// because a silent wait of minutes reads as a hang. tool is the command name
// the hints tell the caller to run again. Against an older strazad, which
// names no page, it prints the provider's link the way that server expects.
func SignIn(ctx context.Context, api API, server, tool string, w io.Writer) error {
	before, err := List(ctx, api, "")
	if err != nil {
		return err
	}
	var start struct {
		Provider     string `json:"provider"`
		AuthorizeURL string `json:"authorize_url"`
		PageURL      string `json:"page_url"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := api.Do(ctx, http.MethodPost, "/v1/connect/"+url.PathEscape(server), nil, &start); err != nil {
		return err
	}
	wait := time.Duration(start.ExpiresIn) * time.Second
	nudge := fmt.Sprintf("still waiting: press Sign in with %s in the browser. This command stops waiting in", start.Provider)
	gaveUp := fmt.Errorf("connect stopped waiting after %s and nothing changed. The sign-in still works on the page any time. Run `%s connect %s` again to wait for it", waitWords(wait), tool, server)
	if start.PageURL == "" {
		fmt.Fprintf(w, "Open %s\nand authorize Straza with %s.\n", start.AuthorizeURL, start.Provider)
		fmt.Fprintln(w, "waiting for authorization in the browser…")
		nudge = "still waiting: finish authorizing in the browser. Link expires in"
		gaveUp = fmt.Errorf("connect timed out: the authorization link expired. Run `%s connect %s` again", tool, server)
	} else {
		fmt.Fprintf(w, "%s uses your own sign-in at %s, which you finish in a browser.\nOpen %s\nsign in to Straza there, and press Sign in with %s on the %s row.\n",
			server, start.Provider, start.PageURL, start.Provider, server)
		fmt.Fprintln(w, "waiting for the sign-in…")
	}

	deadline := time.Now().Add(wait)
	prior := statusOf(before, server)
	for polls := 0; time.Now().Before(deadline); polls++ {
		// Poll first: the sign-in has usually already landed by the time the
		// user tabs back, so a wait before the first poll is pure latency. The
		// wait belongs between polls (loop bottom). A cancelled context must
		// fail closed here without an HTTP call: a clean ctx.Err().
		if err := ctx.Err(); err != nil {
			return err
		}
		cur, err := List(ctx, api, "")
		if err != nil {
			return err
		}
		now := statusOf(cur, server)
		// Done when a connection appears or an existing one rotates
		// (updated_at bumps on every reconnect, even for non-expiring grants).
		if now != nil && now.Connected && (prior == nil || !prior.Connected || now.UpdatedAt != prior.UpdatedAt) {
			fmt.Fprintf(w, "Connected: %s → %s\n", server, now.Provider)
			return nil
		}
		if polls%8 == 7 {
			fmt.Fprintf(w, "%s %s.\n", nudge, time.Until(deadline).Round(time.Second))
		}
		// Wait between polls (never before the first); cancellation during the
		// wait still returns ctx.Err().
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return gaveUp
}

// Remove deletes the acting user's connection on one server; user names
// another user for a sponsor or an administrator.
func Remove(ctx context.Context, api API, server, user string) error {
	return api.Do(ctx, http.MethodDelete, "/v1/connect/"+url.PathEscape(server)+userQuery(user), nil, nil)
}

// waitWords says a wait the way a person reads it: whole minutes where the
// wait is whole minutes, else Go's own short form.
func waitWords(d time.Duration) string {
	if d < time.Minute || d%time.Minute != 0 {
		return d.String()
	}
	if d == time.Minute {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}

func userQuery(user string) string {
	if user == "" {
		return ""
	}
	return "?user=" + url.QueryEscape(user)
}

func statusOf(list []Status, server string) *Status {
	for i := range list {
		if list[i].App == server {
			return &list[i]
		}
	}
	return nil
}
