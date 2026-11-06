package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// machineSession stores a live session for the user on a machine, the way
// the Straza background service's check-in starts one, so the session
// carries the device that every machine session carries.
func machineSession(t *testing.T, app *App, user store.User) string {
	t.Helper()
	_, tok := storedSession(t, app, user.ID, user.Username+"-laptop", "claude-code")
	return tok
}

// TestConnectWritesFromAMachineSession drives the connect writes that need
// the person: from a session a machine enrollment started, turning the
// agents opt-in on and any write on another user's connection are refused
// with the self-service page and one connection.refused record naming the
// subject, the actor, the server and the reason, and nothing is stored. The
// same session still pastes its own token, turns the opt-in off and
// disconnects itself, and a session that began with the person's own
// sign-in makes every write.
func TestConnectWritesFromAMachineSession(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	alice := seedGatewayUser(t, app, "alice", "dev")
	joe := seedAgent(t, app, "joe", "alice", "dev")
	kim := seedGatewayUser(t, app, "kim", AdminRole)
	installTokenApp(t, app, up, "tokapp", "sponsor")
	catRecompile(t, app)

	aliceSignIn := sessionToken(t, base, "alice")
	aliceMachine := machineSession(t, app, alice)
	kimSignIn := sessionToken(t, base, "kim")
	kimMachine := machineSession(t, app, kim)
	if code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", aliceSignIn, map[string]any{"token": "tok-alice"}, nil); code != http.StatusOK {
		t.Fatalf("alice's own paste = %d", code)
	}

	const page = "/self-service/credentials"
	tests := []struct {
		name   string
		tok    string
		method string
		path   string
		body   map[string]any
		want   int
		// refusedFor is the subject of the one connection.refused record a
		// refusal leaves, with its reason.
		refusedFor, reason string
	}{
		{"a machine cannot turn the agents opt-in on", aliceMachine, http.MethodPatch, "/v1/connect/tokapp",
			map[string]any{"allow_agents": true}, http.StatusForbidden, alice.ID, "a session a machine started cannot allow agents"},
		{"a machine cannot paste a token for the agent it sponsors", aliceMachine, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-joe", "user": "joe"}, http.StatusForbidden, joe.ID, "a session a machine started cannot change another user's connection"},
		{"an admin's machine cannot remove another user's connection", kimMachine, http.MethodDelete, "/v1/connect/tokapp?user=alice",
			nil, http.StatusForbidden, alice.ID, "a session a machine started cannot change another user's connection"},
		{"a machine turns its own opt-in off", aliceMachine, http.MethodPatch, "/v1/connect/tokapp",
			map[string]any{"allow_agents": false}, http.StatusOK, "", ""},
		{"a machine pastes its own token", aliceMachine, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-alice-2"}, http.StatusOK, "", ""},
		{"the person's sign-in turns the opt-in on", aliceSignIn, http.MethodPatch, "/v1/connect/tokapp",
			map[string]any{"allow_agents": true}, http.StatusOK, "", ""},
		{"the person's sign-in pastes for the agent they sponsor", aliceSignIn, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-joe", "user": "joe"}, http.StatusOK, "", ""},
		{"an admin's sign-in removes another user's connection", kimSignIn, http.MethodDelete, "/v1/connect/tokapp?user=joe",
			nil, http.StatusOK, "", ""},
		{"a machine disconnects itself", aliceMachine, http.MethodDelete, "/v1/connect/tokapp",
			nil, http.StatusOK, "", ""},
	}
	for _, tc := range tests {
		before := len(identityActions(t, app, "connection.refused"))
		var out map[string]any
		code := adminReq(t, tc.method, base+tc.path, tc.tok, tc.body, &out)
		if code != tc.want {
			t.Fatalf("%s: %s %s = %d %v, want %d", tc.name, tc.method, tc.path, code, out, tc.want)
		}
		refused := identityActions(t, app, "connection.refused")[before:]
		if tc.refusedFor == "" {
			if len(refused) != 0 {
				t.Errorf("%s: refusal records %v, want none", tc.name, refused)
			}
			continue
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, "your own sign-in in a browser") || !strings.Contains(msg, page) {
			t.Errorf("%s: refusal = %q, want the reason and the self-service page", tc.name, msg)
		}
		if len(refused) != 1 {
			t.Fatalf("%s: refusal records %v, want exactly one", tc.name, refused)
		}
		rec := refused[0]
		setBy := alice.ID
		if tc.tok == kimMachine {
			setBy = kim.ID
		}
		if rec["user"] != tc.refusedFor || rec["setBy"] != setBy || rec["app"] != "tokapp" || rec["reason"] != tc.reason {
			t.Errorf("%s: record = %v, want user %s, setBy %s, app tokapp, reason %q", tc.name, rec, tc.refusedFor, setBy, tc.reason)
		}
	}
	// The refused writes stored nothing: joe's only paste is the one his
	// sponsor's own sign-in made, which kim's sign-in then removed.
	if pastes := identityActions(t, app, "token.connect"); len(pastes) != 3 {
		t.Errorf("token.connect records = %d, want alice's two and the one for joe", len(pastes))
	}
}

// agentSession stores a live session for an agent the way its own key
// check-in starts one: a headless agent enrolls no device, so the session
// carries none, as a person's sign-in in a browser carries none.
func agentSession(t *testing.T, app *App, agent store.User) string {
	t.Helper()
	_, tok := storedSession(t, app, agent.ID, "", "claude-code")
	return tok
}

// TestConnectAgentActsOnlyForItself drives the connect routes with the
// device-less sessions of agents that hold every standing a person uses to
// act for another user: straza-admin, straza-global-mcp-admin, a role that
// admin.roleAreas maps to apps:write, and a sponsor link. Each write on
// another user's connection answers 403 with the sentence that only a person
// acts for another user, leaves exactly one connection.refused record naming
// the subject, the actor, the server and the reason, and stores nothing. A
// read of another user's connections answers the same 403 and leaves no
// record. A person without that standing meets the same record on a write.
// An agent still pastes its own token, turns its opt-in off and disconnects
// itself. Turning its own opt-in on, on a token server or a sign-in server,
// is refused with one record, because only a person lets agents use a
// connection.
func TestConnectAgentActsOnlyForItself(t *testing.T) {
	t.Parallel()
	provider := startOAuthProviderMock(t, 3600)
	app, base, _ := testAppCounting(t, withOAuthGithub(provider), func(cfg *config.Config) {
		cfg.Admin.RoleAreas = map[string][]string{"apps-operator": {"apps:read", "apps:write"}}
	})
	up := startGatewayUpstream(t)
	alice := seedGatewayUser(t, app, "alice", "dev")
	mallory := seedGatewayUser(t, app, "mallory", "dev")
	if _, err := app.store.Roles().Create(context.Background(), store.Role{Name: "apps-operator"}); err != nil {
		t.Fatal(err)
	}
	root := seedAgent(t, app, "root-bot", "", AdminRole)
	mcpAdmin := seedAgent(t, app, "mcp-bot", "", MCPAdminRole)
	operator := seedAgent(t, app, "ops-bot", "", "apps-operator")
	lead := seedAgent(t, app, "lead-bot", "", "dev")
	sub := seedAgent(t, app, "sub-bot", "lead-bot", "dev")
	joe := seedAgent(t, app, "joe", "alice", "dev")
	tokapp := installTokenApp(t, app, up, "tokapp", "sponsor")
	installOAuthGithubApp(t, app, up.URL)
	catRecompile(t, app)

	rootTok := agentSession(t, app, root)
	subTok := agentSession(t, app, sub)
	for _, paste := range []struct{ tok, token string }{{sessionToken(t, base, "alice"), "tok-alice"}, {subTok, "tok-sub"}} {
		if code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", paste.tok, map[string]any{"token": paste.token}, nil); code != http.StatusOK {
			t.Fatalf("own paste of %s = %d", paste.token, code)
		}
	}

	// ask is who a refusal sends the caller to: a person target itself, an
	// agent target's sponsor when that is an active person other than the
	// caller, and an administrator.
	agentSays := func(actor, target store.User, ask string) string {
		return fmt.Sprintf("%[1]s cannot read or change %[2]s's connections, because %[1]s is an agent and only a person acts for another user, "+
			"whatever roles %[1]s holds. Send the request without a user to work on your own connections, "+
			"or ask %[3]s to do this.", actor.Username, target.Username, ask)
	}
	const standingReason = "only a person who is the user's sponsor or an administrator can change another user's connection"
	actors := []struct {
		name          string
		tok           string
		actor, target store.User
		says          string
	}{
		{"an agent that holds straza-admin", rootTok, root, alice, agentSays(root, alice, "alice or an administrator")},
		{"an agent that holds straza-admin, for another agent", rootTok, root, joe, agentSays(root, joe, "joe's sponsor alice or an administrator")},
		{"an agent that holds straza-global-mcp-admin", agentSession(t, app, mcpAdmin), mcpAdmin, alice, agentSays(mcpAdmin, alice, "alice or an administrator")},
		{"an agent whose role maps to apps:write", agentSession(t, app, operator), operator, alice, agentSays(operator, alice, "alice or an administrator")},
		{"an agent that holds straza-admin, for an agent an agent sponsors", rootTok, root, sub, agentSays(root, sub, "an administrator")},
		{"an agent named as another agent's sponsor", agentSession(t, app, lead), lead, sub, agentSays(lead, sub, "an administrator")},
		{"a person without standing", sessionToken(t, base, "mallory"), mallory, alice,
			"mallory cannot read or change alice's connections, because mallory neither sponsors alice nor is an administrator. " +
				"Ask alice or an administrator to do this."},
	}
	for _, ac := range actors {
		user := ac.target.Username
		calls := []struct {
			method, path string
			body         map[string]any
			write        bool
		}{
			{http.MethodPatch, "/v1/connect/tokapp", map[string]any{"user": user, "allow_agents": true}, true},
			{http.MethodPost, "/v1/connect/tokapp", map[string]any{"user": user, "token": "tok-attacker"}, true},
			{http.MethodDelete, "/v1/connect/tokapp?user=" + user, nil, true},
			{http.MethodGet, "/v1/connect?user=" + user, nil, false},
			{http.MethodGet, "/v1/self/servers?user=" + user, nil, false},
		}
		for _, call := range calls {
			t.Run(ac.name+" "+call.method+" "+call.path, func(t *testing.T) {
				before := len(identityActions(t, app, "connection.refused"))
				var out any
				code := adminReq(t, call.method, base+call.path, ac.tok, call.body, &out)
				body, _ := out.(map[string]any)
				if code != http.StatusForbidden || body["error"] != ac.says {
					t.Fatalf("= %d %v, want 403 %q", code, out, ac.says)
				}
				refused := identityActions(t, app, "connection.refused")[before:]
				if !call.write {
					if len(refused) != 0 {
						t.Errorf("a refused read left records %v, want none", refused)
					}
					return
				}
				if len(refused) != 1 {
					t.Fatalf("refusal records %v, want exactly one", refused)
				}
				if rec := refused[0]; rec["user"] != ac.target.ID || rec["setBy"] != ac.actor.ID || rec["app"] != "tokapp" || rec["reason"] != standingReason {
					t.Errorf("record = %v, want user %s, setBy %s, app tokapp, reason %q", rec, ac.target.ID, ac.actor.ID, standingReason)
				}
			})
		}
	}

	// The agent still works on its own connection, except to let agents in.
	page := strings.TrimRight(app.cfg.Server.PublicURL, "/") + testConnectPage
	const sponsorWay = "An agent whose sponsor is a person can instead run on that person's connection, " +
		"which the person allows in the Credentials tab of the self-service page: "
	own := []struct {
		name, method, server string
		body                 map[string]any
		want                 int
		says                 string
	}{
		{"pastes its own token", http.MethodPost, "tokapp", map[string]any{"token": "tok-root"}, http.StatusOK, ""},
		{"turns its own opt-in off", http.MethodPatch, "tokapp", map[string]any{"allow_agents": false}, http.StatusOK, ""},
		{"cannot turn its own opt-in on", http.MethodPatch, "tokapp", map[string]any{"allow_agents": true}, http.StatusForbidden,
			"root-bot cannot let agents use its tokapp connection, because only a person turns that on. " +
				"Ask an administrator to turn it on for root-bot with strazactl connect tokapp --user root-bot --allow-agents, " +
				"or to set each agent's own token with strazactl connect tokapp --user <agent>. " + sponsorWay + page},
		{"cannot turn its own opt-in on, on a sign-in server", http.MethodPatch, "github", map[string]any{"allow_agents": true}, http.StatusForbidden,
			"root-bot cannot let agents use its github connection, because only a person turns that on and only a person holds a sign-in connection. " +
				sponsorWay + page},
		{"disconnects itself", http.MethodDelete, "tokapp", nil, http.StatusOK, ""},
	}
	for _, tc := range own {
		t.Run("an agent "+tc.name, func(t *testing.T) {
			before := len(identityActions(t, app, "connection.refused"))
			var out map[string]any
			code := adminReq(t, tc.method, base+"/v1/connect/"+tc.server, rootTok, tc.body, &out)
			if code != tc.want || (tc.says != "" && out["error"] != tc.says) {
				t.Fatalf("= %d %v, want %d %q", code, out, tc.want, tc.says)
			}
			refused := identityActions(t, app, "connection.refused")[before:]
			if tc.says == "" {
				if len(refused) != 0 {
					t.Errorf("refusal records %v, want none", refused)
				}
				return
			}
			if len(refused) != 1 {
				t.Fatalf("refusal records %v, want exactly one", refused)
			}
			const reason = "an agent or a service account cannot allow agents"
			if rec := refused[0]; rec["user"] != root.ID || rec["setBy"] != root.ID || rec["app"] != tc.server || rec["reason"] != reason {
				t.Errorf("record = %v, want user and setBy %s, app %s, reason %q", rec, root.ID, tc.server, reason)
			}
		})
	}

	// Nothing the refusals asked for was stored: alice and sub-bot keep their
	// own tokens with the opt-in off, and the only writes are the agents' own.
	for _, kept := range []struct {
		user  store.User
		token string
	}{{alice, "tok-alice"}, {sub, "tok-sub"}} {
		ur, ok, err := app.broker.UserRowFor(context.Background(), tokapp.ID, kept.user.ID)
		if err != nil || !ok || ur.Fingerprint != secrets.Fingerprint(kept.token) || ur.Meta.AllowAgents || ur.Meta.SetBy != "" {
			t.Errorf("%s's row = %+v %v %v, want their own %s with the opt-in off", kept.user.Username, ur, ok, err, kept.token)
		}
	}
	if pastes := identityActions(t, app, "token.connect"); len(pastes) != 3 {
		t.Errorf("token.connect records = %v, want the own pastes of alice, sub-bot and root-bot", pastes)
	}
	if agents := identityActions(t, app, "connection.agents"); len(agents) != 1 || agents[0]["user"] != root.ID {
		t.Errorf("connection.agents records = %v, want root-bot's own opt-in off", agents)
	}
	if gone := identityActions(t, app, "token.disconnect"); len(gone) != 1 || gone[0]["user"] != root.ID {
		t.Errorf("token.disconnect records = %v, want root-bot's own disconnect", gone)
	}
}

// TestConnectSignInRefusalsLeaveARecord drives the refusals of a provider
// sign-in on POST /v1/connect/{app}. An agent that starts its own sign-in and
// an admin API token that starts one for a person or an agent each answer
// 403 and leave exactly one connection.refused record naming the subject,
// the actor, the server and the reason, and a token's record carries
// actorVia api-token. A session that names a user answers 400 and leaves no
// record, and a person who starts her own sign-in leaves none.
func TestConnectSignInRefusalsLeaveARecord(t *testing.T) {
	t.Parallel()
	provider := startOAuthProviderMock(t, 3600)
	app, base := testApp(t, withOAuthGithub(provider))
	up := startGatewayUpstream(t)
	alice := seedGatewayUser(t, app, "alice", "dev")
	joe := seedAgent(t, app, "joe", "alice", "dev")
	seedGatewayUser(t, app, "kim", AdminRole)
	installOAuthGithubApp(t, app, up.URL)
	catRecompile(t, app)

	kimSignIn, _ := sessionAs(t, base, "kim", "console")
	writeID, writeTok := mintConnectToken(t, base, kimSignIn, "ci-write", "apps:write")
	aliceSignIn := sessionToken(t, base, "alice")
	page := strings.TrimRight(app.cfg.Server.PublicURL, "/") + testConnectPage
	const (
		agentReason = "an agent or a service account cannot start a sign-in"
		tokenReason = "an admin API token cannot start a sign-in"
	)
	tests := []struct {
		name string
		tok  string
		body map[string]any
		code int
		says string
		// user, setBy and reason are the one connection.refused record the
		// call leaves, user empty for none.
		user, setBy, reason string
		viaToken            bool
	}{
		{"an agent starts its own sign-in", agentSession(t, app, joe), nil, http.StatusForbidden,
			agentSignInRefusal(joe, "github", "github"), joe.ID, joe.ID, agentReason, false},
		{"a token starts a sign-in for a person", writeTok, map[string]any{"user": "alice"}, http.StatusForbidden,
			wantTokenSignInAlice + page, alice.ID, writeID, tokenReason, true},
		{"a token starts a sign-in for an agent", writeTok, map[string]any{"user": "joe"}, http.StatusForbidden,
			agentSignInRefusal(joe, "github", "github"), joe.ID, writeID, tokenReason, true},
		{"a session names a user", aliceSignIn, map[string]any{"user": "joe"}, http.StatusBadRequest, "", "", "", "", false},
		{"a person starts her own sign-in", aliceSignIn, nil, http.StatusOK, "", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(identityActions(t, app, "connection.refused"))
			var out map[string]any
			code := adminReq(t, http.MethodPost, base+"/v1/connect/github", tc.tok, tc.body, &out)
			if code != tc.code || (tc.says != "" && out["error"] != tc.says) {
				t.Fatalf("= %d %v, want %d %q", code, out, tc.code, tc.says)
			}
			refused := identityActions(t, app, "connection.refused")[before:]
			if tc.user == "" {
				if len(refused) != 0 {
					t.Errorf("refusal records %v, want none", refused)
				}
				return
			}
			if len(refused) != 1 {
				t.Fatalf("refusal records %v, want exactly one", refused)
			}
			rec := refused[0]
			if rec["user"] != tc.user || rec["setBy"] != tc.setBy || rec["app"] != "github" || rec["reason"] != tc.reason {
				t.Errorf("record = %v, want user %s, setBy %s, app github, reason %q", rec, tc.user, tc.setBy, tc.reason)
			}
			if via, has := rec["actorVia"]; tc.viaToken != has || (has && via != "api-token") {
				t.Errorf("record actorVia = %v (present %v), want api-token only for a token", via, has)
			}
		})
	}
}
