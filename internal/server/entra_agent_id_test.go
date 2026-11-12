package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// TestAdminEntraAgentID walks entra_agent_id through the admin API: an
// agent takes an id in any case and holds it in lower case, a person, a
// value that is not a GUID, an id another agent holds and a type change of
// a holder are refused with a sentence that names the fix and change
// nothing, an empty value clears it, and each write that changes it names
// entra_agent_id in its user.update record.
func TestAdminEntraAgentID(t *testing.T) {
	t.Parallel()
	const (
		id    = "dc013442-2b7a-4c1e-9f1a-0123456789ab"
		other = "5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b"
	)
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	tok, _ := checkinToken(t, app, base)
	sam := seedAgent(t, app, "sam-sre-agent", "kim", "dev")
	joe := seedAgent(t, app, "joe-java-developer", "kim", "dev")
	ada := mkHuman(t, app, "ada")
	bea := mkHuman(t, app, "bea")
	ctx := context.Background()
	ada.UserType = store.UserTypeHuman
	if _, err := app.store.Users().Update(ctx, ada); err != nil {
		t.Fatal(err)
	}
	stored := func(u store.User) string {
		got, err := app.store.Users().GetByID(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.EntraAgentID
	}

	for _, tc := range []struct {
		name   string
		user   store.User
		body   map[string]any
		code   int
		answer []string
		want   func() bool
	}{
		{"sam takes an upper-case id", sam, map[string]any{"entra_agent_id": strings.ToUpper(id)}, http.StatusOK,
			[]string{`"entra_agent_id":"` + id + `"`}, func() bool { return stored(sam) == id }},
		{"joe takes sam's id", joe, map[string]any{"entra_agent_id": id}, http.StatusConflict,
			[]string{id + " is already the Entra agent identity of sam-sre-agent", "strazactl users entra-agent-id unset sam-sre-agent",
				"give joe-java-developer an agent identity of its own"}, func() bool { return stored(joe) == "" }},
		{"a person takes an id", ada, map[string]any{"entra_agent_id": other}, http.StatusBadRequest,
			[]string{"ada is a person, and only a user of type agent can hold an Entra agent identity"}, func() bool { return stored(ada) == "" }},
		{"an unclassified user takes an id", bea, map[string]any{"entra_agent_id": other}, http.StatusBadRequest,
			[]string{"bea is an unclassified identity with no user_type", "make bea an agent first if it is one"}, func() bool { return stored(bea) == "" }},
		{"a value that is not a GUID", joe, map[string]any{"entra_agent_id": "sam-sre-agent"}, http.StatusBadRequest,
			[]string{`\"sam-sre-agent\" is not an Entra agent identity id`, "Entra admin centre"}, func() bool { return stored(joe) == "" }},
		{"the holder turns person", sam, map[string]any{"user_type": "human"}, http.StatusBadRequest,
			[]string{"sam-sre-agent holds the Entra agent identity " + id, "strazactl users entra-agent-id unset sam-sre-agent, then change its type"},
			func() bool { return stored(sam) == id }},
		{"an empty value clears", sam, map[string]any{"entra_agent_id": ""}, http.StatusOK, nil, func() bool { return stored(sam) == "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]any
			code := adminReq(t, http.MethodPatch, base+"/v1/admin/users/"+tc.user.ID, tok, tc.body, &raw)
			answer := string(mustJSON(raw))
			if code != tc.code {
				t.Fatalf("PATCH = %d %s, want %d", code, answer, tc.code)
			}
			for _, want := range tc.answer {
				if !strings.Contains(answer, want) {
					t.Fatalf("answer %s does not say %q", answer, want)
				}
			}
			if !tc.want() {
				t.Fatal("the stored id is not what the write should leave")
			}
		})
	}
	changed := userUpdateRecords(t, app, sam.ID)
	if len(changed) != 2 || !slices.Contains(changed[0], "entra_agent_id") || !slices.Contains(changed[1], "entra_agent_id") {
		t.Fatalf("sam's user.update records = %v, want two that name entra_agent_id", changed)
	}
	if got := userUpdateRecords(t, app, joe.ID); len(got) != 0 {
		t.Fatalf("joe's refused writes left records %v", got)
	}
}

// TestEntraAgentIDReachesTheSession pins that check-in keeps the agent's
// Entra agent id in its session facts and in its token's eai claim and
// whoami shows it, and that a write that changes the id, over the admin API
// or over SCIM, drops the agent's cached subjects on this server, so the
// next check-in reads the new id. The write leaves the agent's client
// tokens alone, so it cuts no fetch short, and a provider outside Entra,
// whose token the id does not name, keeps serving the cached one. A write
// that leaves the id alone drops nothing.
func TestEntraAgentIDReachesTheSession(t *testing.T) {
	t.Parallel()
	const id = "dc013442-2b7a-4c1e-9f1a-0123456789ab"
	idp := startCCProvider(t)
	app, base := testApp(t, withCCProvider(idp))
	idp.keys = func() []byte {
		doc, err := app.assertionKeys.JWKS(time.Now())
		if err != nil {
			t.Errorf("JWKS: %v", err)
		}
		return doc
	}
	ctx := context.Background()
	key, err := app.assertionKeys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.assertionKeys.Promote(ctx, time.Now().Add(2*authn.KeyReloadInterval), key.KID); err != nil {
		t.Fatal(err)
	}
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	var minted struct {
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "idm", "scope": "scim:read,scim:write"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint the SCIM token = %d", code)
	}
	up := startGatewayUpstream(t)
	sam := seedAgent(t, app, "sam", "", "dev")
	installCCApp(t, app, up, "ccapp")
	catRecompile(t, app)

	facts := func(sid string) (sessionFacts, bool) {
		e, ok := app.subjects.lookup(sid)
		return e.facts, ok
	}
	whoamiID := func(tok string) any {
		text, isErr, sc := callTool(t, base, tok, "straza__whoami", map[string]any{})
		if isErr {
			t.Fatal("whoami answered an error")
		}
		line := "Entra agent identity: " + id + ".\n"
		if want := sc["entraAgentId"] == id; strings.Contains(text, line) != want {
			t.Fatalf("whoami text %q, want the line %q only when the structured answer holds %s", text, line, id)
		}
		return sc["entraAgentId"]
	}
	call := func(tok string) {
		t.Helper()
		if raw := echoCall(t, base, tok, "ccapp"); !strings.Contains(raw, "echo: hi") {
			t.Fatalf("sam's call = %s", raw)
		}
	}
	claimed := func(tok, want string) {
		t.Helper()
		c, err := app.tokens.Verify(tok)
		if err != nil || c.EntraAgentID != want {
			t.Fatalf("the session token carries eai %q (%v), want %q", c.EntraAgentID, err, want)
		}
		if code, dec := decide(t, base, tok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "ls"}); code != http.StatusOK {
			t.Fatalf("the hook lane's decide on this token = %d %v", code, dec)
		}
	}

	tok, sid := agentCheckin(t, app, base, "sam")
	if f, ok := facts(sid); !ok || f.entraAgentID != "" {
		t.Fatalf("facts before any id = %+v, %v", f, ok)
	}
	if got := whoamiID(tok); got != nil {
		t.Fatalf("whoami without an id shows %v", got)
	}
	claimed(tok, "")
	call(tok)

	if code := adminReq(t, http.MethodPatch, base+"/v1/admin/users/"+sam.ID, adminTok, map[string]any{"title": "SRE"}, nil); code != http.StatusOK {
		t.Fatalf("title PATCH = %d", code)
	}
	if _, ok := facts(sid); !ok {
		t.Fatal("a write that left the id alone dropped the cached subject")
	}

	if code := adminReq(t, http.MethodPatch, base+"/v1/admin/users/"+sam.ID, adminTok, map[string]any{"entra_agent_id": id}, nil); code != http.StatusOK {
		t.Fatalf("admin PATCH = %d", code)
	}
	if _, ok := facts(sid); ok {
		t.Fatal("the admin write of the id left the cached subject")
	}
	tok, sid = agentCheckin(t, app, base, "sam")
	if f, _ := facts(sid); f.entraAgentID != id {
		t.Fatalf("facts after the admin write = %+v, want %s", f, id)
	}
	if got := whoamiID(tok); got != id {
		t.Fatalf("whoami shows %v, want %s", got, id)
	}
	claimed(tok, id)
	call(tok)
	if got := idp.asked(); got != "sam" {
		t.Fatalf("the provider was asked for %q, want the token cached before the admin write served", got)
	}

	code, body, _ := scimSend(t, http.MethodPatch, base+"/scim/v2/Users/"+sam.ID, minted.Token,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"remove","path":"entraAgentId"}]}`)
	if code != http.StatusOK {
		t.Fatalf("SCIM remove = %d %s", code, body)
	}
	if _, ok := facts(sid); ok {
		t.Fatal("the SCIM write of the id left the cached subject")
	}
	tok, sid = agentCheckin(t, app, base, "sam")
	if f, _ := facts(sid); f.entraAgentID != "" {
		t.Fatalf("facts after the SCIM remove = %+v", f)
	}
	claimed(tok, "")
	call(tok)
	if got := idp.asked(); got != "sam" {
		t.Fatalf("the provider was asked for %q, want the cached token served after the SCIM write", got)
	}
}

// TestEntraAgentIDChangeReachesEveryReplica pins that a change of an
// agent's Entra agent id that one replica takes reaches another replica
// that still caches the old id. A token minted after the change carries the
// new id, so the other replica answers it with the check-in-again 401
// instead of serving the old id, and serves the new id after a check-in
// there. A token minted before the change keeps the old id on that replica
// until it expires, the one token lifetime the spec states.
func TestEntraAgentIDChangeReachesEveryReplica(t *testing.T) {
	const oldID, newID = "dc013442-2b7a-4c1e-9f1a-0123456789ab", "5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b"
	a, b, baseA, baseB := replicaPair(t)
	kim := seedIdentity(t, a)
	grantAdmin(t, a, kim.ID)
	adminTok, _ := checkinToken(t, a, baseA)
	sam := seedAgent(t, a, "sam", "", "dev")
	b.resolver.Bump()
	setID := func(id string) {
		t.Helper()
		if code := adminReq(t, http.MethodPatch, baseA+"/v1/admin/users/"+sam.ID, adminTok, map[string]any{"entra_agent_id": id}, nil); code != http.StatusOK {
			t.Fatalf("set the Entra agent id through a = %d", code)
		}
	}
	whoami := func(base, tok string) (int, any) {
		t.Helper()
		code, body, raw := mcpPost(t, base+"/mcp", tok, "tools/call", map[string]any{"name": "straza__whoami", "arguments": map[string]any{}})
		if code != http.StatusOK {
			return code, nil
		}
		res, _ := body["result"].(map[string]any)
		sc, ok := res["structuredContent"].(map[string]any)
		if !ok {
			t.Fatalf("whoami on %s answered %s", base, raw)
		}
		return code, sc["entraAgentId"]
	}

	setID(oldID)
	early, _ := agentCheckin(t, b, baseB, "sam")
	if code, got := whoami(baseB, early); code != http.StatusOK || got != oldID {
		t.Fatalf("whoami on b before the change = %d %v, want %s", code, got, oldID)
	}

	// The session refreshes on a, so b still caches it under the old id.
	setID(newID)
	refresh := func(base, tok string) string {
		t.Helper()
		code, body := catRefreshCheckin(t, base, tok)
		next, _ := body["session_token"].(string)
		if code != http.StatusOK || next == "" {
			t.Fatalf("refresh on %s = %d %v", base, code, body)
		}
		return next
	}
	late := refresh(baseA, early)
	if code, got := whoami(baseB, late); code != http.StatusUnauthorized {
		t.Fatalf("b answered a token minted after the change with %d %v, want 401 so the agent checks in again", code, got)
	}
	if code, got := whoami(baseB, early); code != http.StatusOK || got != oldID {
		t.Fatalf("b answered the token minted before the change with %d %v, want the old id until it expires", code, got)
	}
	fresh := refresh(baseB, late)
	if code, got := whoami(baseB, fresh); code != http.StatusOK || got != newID {
		t.Fatalf("whoami on b after its own check-in = %d %v, want %s", code, got, newID)
	}
}
