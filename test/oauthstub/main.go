// Command oauthstub is a local stand-in for the two external parties of a
// per-user OAuth connect flow, so the whole thing can be exercised
// interactively on one machine with zero external accounts:
//
//   - an OAuth provider: GET /authorize renders a "who are you" page in the
//     browser (the human-paced step a real login page plays) and redirects
//     back with a one-shot code; POST /token redeems codes and refresh
//     tokens. Access tokens are versioned per identity ("gho_<user>-v<N>")
//     so refresh-worker rotations are visible.
//   - a credentialed upstream MCP server at /mcp with one tool, `whoami`,
//     answering with the exact Authorization header it received, the
//     observable proof of which upstream identity a gateway call used.
//
// Run it, point a strazad `oauth.providers.local` entry and an oauth-kind app
// manifest at it, then `straza connect <server>`.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stub struct {
	expires int64 // seconds; 0 = non-expiring tokens without refresh handles

	mu       sync.Mutex
	codes    map[string]string // one-shot code → identity
	versions map[string]int    // identity → issued token version
	sessions map[string]string // MCP session id → last Authorization header
	nextCode int
}

var authorizePage = template.Must(template.New("authorize").Parse(`<!doctype html>
<meta charset="utf-8"><title>oauthstub: authorize</title>
<body style="font-family:system-ui,sans-serif;max-width:34rem;margin:15vh auto;padding:0 1rem">
<h1 style="font-size:1.3rem">Local OAuth provider</h1>
<p>Straza is asking to connect. Whose upstream account is this?</p>
<form method="GET" action="/authorize">
  <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
  <input type="hidden" name="state" value="{{.State}}">
  <input type="hidden" name="client_id" value="{{.ClientID}}">
  <input name="user" placeholder="alice" autofocus required>
  <button type="submit">Authorize</button>
</form></body>`))

func (s *stub) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, state := q.Get("redirect_uri"), q.Get("state")
	if redirect == "" || state == "" {
		http.Error(w, "missing redirect_uri or state", http.StatusBadRequest)
		return
	}
	// A real provider only redirects to registered URIs; the stub pins
	// loopback (also mutes gosec's open-redirect finding for good reason).
	cb, err := url.Parse(redirect)
	if err != nil || (cb.Hostname() != "127.0.0.1" && cb.Hostname() != "localhost") {
		http.Error(w, "redirect_uri must be loopback", http.StatusBadRequest)
		return
	}
	user := q.Get("user")
	if user == "" {
		// The provider's "login page": the human picks an identity.
		_ = authorizePage.Execute(w, map[string]string{
			"RedirectURI": redirect, "State": state, "ClientID": q.Get("client_id"),
		})
		return
	}
	if !identRe.MatchString(user) {
		http.Error(w, "user must match [a-z0-9-]{1,32}", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.nextCode++
	code := fmt.Sprintf("code-%d", s.nextCode)
	s.codes[code] = user
	s.mu.Unlock()
	cq := cb.Query()
	cq.Set("code", code)
	cq.Set("state", state)
	cb.RawQuery = cq.Encode()
	log.Printf("authorize: %s → redirecting back to strazad", user) // #nosec G706 -- identRe-validated
	http.Redirect(w, r, cb.String(), http.StatusFound)              // #nosec G710 -- loopback-pinned above
}

// identRe keeps chosen identities token- and log-safe.
var identRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func (s *stub) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	var user, kind string
	s.mu.Lock()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		kind = "exchange"
		code := r.PostForm.Get("code")
		user = s.codes[code]  // codes only ever hold identRe-validated names
		delete(s.codes, code) // one-shot
	case "refresh_token":
		kind = "refresh"
		u := r.PostForm.Get("refresh_token")
		if len(u) > 4 && u[:4] == "ghr_" {
			if _, known := s.versions[u[4:]]; known {
				user = u[4:]
			}
		}
	}
	if user != "" {
		s.versions[user]++
	}
	v := s.versions[user]
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if user == "" {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		return
	}
	resp := map[string]any{"access_token": fmt.Sprintf("gho_%s-v%d", user, v), "token_type": "bearer"}
	if s.expires > 0 {
		resp["expires_in"] = s.expires
		resp["refresh_token"] = "ghr_" + user
	}
	log.Printf("token %s: %s now holds gho_%s-v%d", kind, user, user, v) // #nosec G706 -- identRe-validated
	_ = json.NewEncoder(w).Encode(resp)
}

// mcpHandler serves the upstream MCP server. Middleware records each MCP
// session's Authorization header (keyed by Mcp-Session-Id) so the whoami
// tool can answer with the credential strazad injected for that session.
func (s *stub) mcpHandler() http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "oauthstub", Version: "0.1.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "whoami", Description: "Answers with the Authorization header the upstream received"},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			s.mu.Lock()
			auth := s.sessions[req.Session.ID()]
			s.mu.Unlock()
			if auth == "" {
				auth = "anonymous (no credential injected)"
			}
			log.Printf("mcp whoami → %s", auth)
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "you are calling upstream as: " + auth},
			}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			s.mu.Lock()
			s.sessions[sid] = r.Header.Get("Authorization")
			s.mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	})
}

func main() {
	listen := flag.String("listen", "127.0.0.1:9096", "address to serve on")
	expires := flag.Int64("expires", 0, "access-token lifetime in seconds (0 = non-expiring, no refresh token)")
	flag.Parse()

	s := &stub{expires: *expires,
		codes: map[string]string{}, versions: map[string]int{}, sessions: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", s.handleAuthorize)
	mux.HandleFunc("POST /token", s.handleToken)
	mux.Handle("/mcp", s.mcpHandler())

	log.Printf("oauthstub on http://%s: /authorize /token /mcp (token expiry: %ds, 0=never)", *listen, *expires)
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}
