package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The refusals of an admin API token on the connect routes, pinned verbatim.
const (
	wantTokenNoUserBody = "an admin API token is not a user, so a request on it must name the user whose connection it reads or changes. " +
		"Name that user, by username or id, in the field user of the body."
	wantTokenNoUserQuery = "an admin API token is not a user, so a request on it must name the user whose connection it reads or changes. " +
		"Name that user, by username or id, in the query parameter user."
	wantTokenNoAppsWrite = "this admin API token lacks the scope apps:write, which a change to a user's connection needs. " +
		"A person mints a token that holds it in the console under Settings, API tokens."
	wantTokenNoAppsRead = "this admin API token lacks the scope apps:read, which reading a user's connections needs. " +
		"A person mints a token that holds it in the console under Settings, API tokens."
	wantTokenSignInAlice = "a sign-in at github is stored only for the person who finishes it in a browser, so an admin API token " +
		"cannot start one for alice. alice signs in on the Credentials tab of the self-service page: "
)

// mintConnectToken mints an admin API token with scope through the admin
// API as the session admin and answers its id and plaintext.
func mintConnectToken(t *testing.T, base, admin, name, scope string) (string, string) {
	t.Helper()
	var out struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", admin,
		map[string]any{"name": name, "scope": scope}, &out); code != http.StatusCreated {
		t.Fatalf("mint %s = %d", name, code)
	}
	return out.ID, out.Token
}

// connectRecords returns the straza.identity.updated records of the
// connection writes and refusals, oldest first.
func connectRecords(t *testing.T, app *App) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range outboxDataFor(t, app, "straza.identity.updated") {
		switch ev["action"] {
		case "token.connect", "token.disconnect", "oauth.disconnect", "connection.agents", "connection.refused":
			out = append(out, ev)
		}
	}
	return out
}

// TestConnectTakesAnAdminAPIToken drives the connect routes with every kind
// of caller: an administrator's machine session, the same administrator's
// own sign-in, and admin API tokens with apps:write, with apps:read only and
// with full. A token passes on its scope as on the admin plane, must name
// the user it acts for, meets no machine-session rule, starts no provider
// sign-in, and each write it lands leaves exactly one record with setBy the
// token's id and actorVia api-token, while a person's record carries no
// actorVia. The stored row names the token as its setter, and the list reads
// that setter as the token's name.
func TestConnectTakesAnAdminAPIToken(t *testing.T) {
	t.Parallel()
	provider := startOAuthProviderMock(t, 3600)
	app, base := testApp(t, withOAuthGithub(provider))
	up := startGatewayUpstream(t)
	alice := seedGatewayUser(t, app, "alice", "dev")
	joe := seedAgent(t, app, "joe", "alice", "dev")
	kim := seedGatewayUser(t, app, "kim", AdminRole)
	installTokenApp(t, app, up, "tokapp", "sponsor")
	installOAuthGithubApp(t, app, up.URL)
	catRecompile(t, app)

	kimSignIn, _ := sessionAs(t, base, "kim", "console")
	_, kimMachine := storedSession(t, app, kim.ID, "kim-laptop", "claude-code")
	writeID, writeTok := mintConnectToken(t, base, kimSignIn, "ci-write", "apps:write")
	_, readTok := mintConnectToken(t, base, kimSignIn, "ci-read", "apps:read")
	fullID, fullTok := mintConnectToken(t, base, kimSignIn, "ci-full", "full")
	if code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", sessionToken(t, base, "alice"), map[string]any{"token": "tok-alice"}, nil); code != http.StatusOK {
		t.Fatalf("alice's own paste = %d", code)
	}

	tests := []struct {
		name   string
		tok    string
		method string
		path   string
		body   map[string]any
		code   int
		says   string
		// action is the one record the call leaves, empty for none, with its
		// subject, its setter and whether it carries actorVia api-token.
		action, user, setBy string
		viaToken            bool
		setByName           string
	}{
		{"an admin's machine session cannot paste for an agent", kimMachine, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-joe-1", "user": "joe"}, http.StatusForbidden, "",
			"connection.refused", joe.ID, kim.ID, false, ""},
		{"an admin's sign-in pastes for an agent", kimSignIn, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-joe-2", "user": "joe"}, http.StatusOK, "",
			"token.connect", joe.ID, kim.ID, false, "kim"},
		{"a read token cannot paste", readTok, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-joe-3", "user": "joe"}, http.StatusForbidden, wantTokenNoAppsWrite, "", "", "", false, ""},
		{"a write token pastes for an agent", writeTok, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-joe-4", "user": "joe"}, http.StatusOK, "",
			"token.connect", joe.ID, writeID, true, "ci-write (admin API token)"},
		{"a write token that names no user", writeTok, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-x"}, http.StatusBadRequest, wantTokenNoUserBody, "", "", "", false, ""},
		{"a write token that names an unknown user", writeTok, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-x", "user": "nobody"}, http.StatusNotFound, `unknown user "nobody"`, "", "", "", false, ""},
		{"a write token cannot start a sign-in for a person", writeTok, http.MethodPost, "/v1/connect/github",
			map[string]any{"user": "alice"}, http.StatusForbidden, wantTokenSignInAlice + strings.TrimRight(app.cfg.Server.PublicURL, "/") + testConnectPage,
			"", "", "", false, ""},
		{"a write token cannot start a sign-in for an agent", writeTok, http.MethodPost, "/v1/connect/github",
			map[string]any{"user": "joe"}, http.StatusForbidden, agentSignInRefusal(joe, "github", "github"), "", "", "", false, ""},
		{"a write token cannot list", writeTok, http.MethodGet, "/v1/connect?user=joe",
			nil, http.StatusForbidden, wantTokenNoAppsRead, "", "", "", false, ""},
		{"a read token that names no user", readTok, http.MethodGet, "/v1/connect",
			nil, http.StatusBadRequest, wantTokenNoUserQuery, "", "", "", false, ""},
		{"a full token turns a person's agents opt-in on", fullTok, http.MethodPatch, "/v1/connect/tokapp",
			map[string]any{"allow_agents": true, "user": "alice"}, http.StatusOK, "",
			"connection.agents", alice.ID, fullID, true, ""},
		{"a write token that names no user on the opt-in", writeTok, http.MethodPatch, "/v1/connect/tokapp",
			map[string]any{"allow_agents": false}, http.StatusBadRequest, wantTokenNoUserBody, "", "", "", false, ""},
		{"a write token that names no user on a disconnect", writeTok, http.MethodDelete, "/v1/connect/tokapp",
			nil, http.StatusBadRequest, wantTokenNoUserQuery, "", "", "", false, ""},
		{"a full token disconnects an agent", fullTok, http.MethodDelete, "/v1/connect/tokapp?user=joe",
			nil, http.StatusOK, "", "token.disconnect", joe.ID, fullID, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(connectRecords(t, app))
			var out map[string]any
			code := adminReq(t, tc.method, base+tc.path, tc.tok, tc.body, &out)
			if code != tc.code {
				t.Fatalf("%s %s = %d %v, want %d", tc.method, tc.path, code, out, tc.code)
			}
			if tc.says != "" && out["error"] != tc.says {
				t.Errorf("error = %q, want %q", out["error"], tc.says)
			}
			recs := connectRecords(t, app)[before:]
			if tc.action == "" {
				if len(recs) != 0 {
					t.Errorf("records = %v, want none", recs)
				}
				return
			}
			if len(recs) != 1 {
				t.Fatalf("records = %v, want exactly one %s", recs, tc.action)
			}
			rec := recs[0]
			if rec["action"] != tc.action || rec["user"] != tc.user || rec["setBy"] != tc.setBy {
				t.Errorf("record = %v, want %s for %s set by %s", rec, tc.action, tc.user, tc.setBy)
			}
			if via, has := rec["actorVia"]; tc.viaToken != has || (has && via != "api-token") {
				t.Errorf("record actorVia = %v (present %v), want api-token only for a token", via, has)
			}
			if tc.setByName != "" && out["set_by"] != tc.setByName {
				t.Errorf("set_by = %v, want %q", out["set_by"], tc.setByName)
			}
		})
	}
}

// TestConnectTokenRowNamesTheToken pins the stored row of a paste an admin
// API token makes: its setter is the token's id, and a read lists it under
// the token's name.
func TestConnectTokenRowNamesTheToken(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "alice", "dev")
	joe := seedAgent(t, app, "joe", "alice", "dev")
	seedGatewayUser(t, app, "kim", AdminRole)
	row := installTokenApp(t, app, up, "tokapp", "sponsor")
	kimSignIn, _ := sessionAs(t, base, "kim", "console")
	tokID, tok := mintConnectToken(t, base, kimSignIn, "ci-connect", "apps:read,apps:write")

	if code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", tok, map[string]any{"token": "tok-joe", "user": "joe"}, nil); code != http.StatusOK {
		t.Fatalf("paste = %d", code)
	}
	ur, ok, err := app.broker.UserRowFor(context.Background(), row.ID, joe.ID)
	if err != nil || !ok || ur.Meta.SetBy != tokID {
		t.Fatalf("joe's row = %+v %v %v, want one set by the token %s", ur.Meta, ok, err, tokID)
	}
	var list []map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/connect?user=joe", tok, nil, &list); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	for _, e := range list {
		if e["app"] == "tokapp" && e["set_by"] != "ci-connect (admin API token)" {
			t.Errorf("tokapp entry = %v, want set_by ci-connect (admin API token)", e)
		}
	}
}
