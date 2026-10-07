package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// seedAgent creates a local agent user sponsored by a human, holding one role.
func seedAgent(t *testing.T, app *App, username, sponsor, roleName string) store.User {
	t.Helper()
	ctx := context.Background()
	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	u, err := app.store.Users().Create(ctx, store.User{
		Username: username, PasswordHash: hash, UserType: store.UserTypeAgent, Sponsor: sponsor,
	})
	if err != nil {
		t.Fatal(err)
	}
	role, err := app.store.Roles().GetByName(ctx, roleName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID,
	}); err != nil {
		t.Fatal(err)
	}
	app.resolver.Bump()
	return u
}

// installTokenApp installs a token-kind remote app over the test upstream
// with the given agents setting, binds every tool to dev and waits for it
// to run.
func installTokenApp(t *testing.T, app *App, up *gatewayUpstream, name, agents string) store.App {
	t.Helper()
	ctx := context.Background()
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: %s}
server: {name: straza.test/%s, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  credential:
    kind: token
    agents: %s
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
`, name, name, up.URL, agents))
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	catReach(t, app, "dev", row, `["*"]`)
	catWaitRunning(t, app, name)
	return row
}

// echoCall drives one echo call through the gateway and returns the raw
// answer.
func echoCall(t *testing.T, base, tok, app string) string {
	t.Helper()
	_, _, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
		"name": app + "__echo", "arguments": map[string]string{"text": "hi"},
	})
	return string(raw)
}

func identityActions(t *testing.T, app *App, action string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range outboxDataFor(t, app, "straza.identity.updated") {
		if ev["action"] == action {
			out = append(out, ev)
		}
	}
	return out
}

// TestConnectTokenLane is the caller-credentials story end to end: alice
// pastes her token and the server tests it once, her calls run on it and
// the record says own; her agent joe is refused until she allows her
// agents, then runs on her row with the record naming her; a stranger
// cannot set joe's token, alice as sponsor can, an admin can remove it;
// the checkin subject carries joe's sponsor; the identity records fire
// once per act; and deprovisioning takes the token row with its kind.
func TestConnectTokenLane(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()
	alice := seedGatewayUser(t, app, "alice", "dev")
	joe := seedAgent(t, app, "joe", "alice", "dev")
	seedGatewayUser(t, app, "mallory", "dev")
	kim := seedGatewayUser(t, app, "kim", AdminRole)
	row := installTokenApp(t, app, up, "tokapp", "sponsor")
	catRecompile(t, app)

	aliceTok := sessionToken(t, base, "alice")
	joeTok, joeSID := gatewaySession(t, base, "joe")
	malloryTok := sessionToken(t, base, "mallory")
	kimTok := sessionToken(t, base, "kim")

	sub, ok := app.subjects.get(joeSID)
	if !ok || sub.Sponsor != "alice" || sub.SponsorID != alice.ID || sub.UserType != store.UserTypeAgent {
		t.Fatalf("joe's subject = %+v, want sponsor alice by name and id", sub)
	}

	// Alice pastes; the server probes the upstream with it before storing.
	const aliceToken = "tok-alice-NEVER-LEAK"
	future := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	var pasted map[string]any
	if code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", aliceTok,
		map[string]any{"token": aliceToken, "expires_at": future.Format(time.RFC3339)}, &pasted); code != http.StatusOK {
		t.Fatalf("paste = %d %v", code, pasted)
	}
	if pasted["fingerprint"] != secrets.Fingerprint(aliceToken) || pasted["kind"] != "token" || pasted["user"] != "alice" {
		t.Errorf("paste answer = %v", pasted)
	}
	if !up.sawAuth("Bearer " + aliceToken) {
		t.Error("the paste did not probe the upstream with the token")
	}
	var list []map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/connect", aliceTok, nil, &list); code != http.StatusOK || len(list) != 1 {
		t.Fatalf("list = %d %v", code, list)
	}
	if e := list[0]; e["app"] != "tokapp" || e["kind"] != "token" || e["agents"] != "sponsor" || e["connected"] != true ||
		e["fingerprint"] != secrets.Fingerprint(aliceToken) || e["allow_agents"] != false || e["expires_at"] != future.Format(time.RFC3339) {
		t.Errorf("list entry = %v", e)
	}

	// Alice runs on her own row.
	if raw := echoCall(t, base, aliceTok, "tokapp"); !strings.Contains(raw, "echo: hi") || strings.Contains(raw, aliceToken) {
		t.Fatalf("alice call = %s", raw)
	}
	own := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
		return d["app"] == "tokapp" && d["user"] == alice.ID && d["credentialSource"] == "own"
	})
	if own[0]["credentialOwner"] != nil || own[0]["credentialId"] == "" {
		t.Errorf("alice's record = %v, want source own with the row id and no owner", own[0])
	}

	// Joe has no row and alice has not allowed her agents: refused in words.
	if raw := echoCall(t, base, joeTok, "tokapp"); !strings.Contains(raw, "its sponsor alice has not allowed agents on their tokapp connection") {
		t.Fatalf("joe before opt-in = %s", raw)
	}
	if code := adminReq(t, http.MethodPatch, base+"/v1/connect/tokapp", aliceTok, map[string]any{"allow_agents": true}, nil); code != http.StatusOK {
		t.Fatalf("opt-in = %d", code)
	}
	if raw := echoCall(t, base, joeTok, "tokapp"); !strings.Contains(raw, "echo: hi") || strings.Contains(raw, aliceToken) {
		t.Fatalf("joe on the sponsor's row = %s", raw)
	}
	sp := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
		return d["app"] == "tokapp" && d["user"] == joe.ID && d["credentialSource"] == "sponsor"
	})
	if sp[0]["credentialOwner"] != alice.ID {
		t.Errorf("joe's record = %v, want credentialOwner alice", sp[0])
	}

	// A stranger cannot set joe's token; his sponsor can; the record names her.
	const joeToken = "tok-joe-NEVER-LEAK"
	var refusal map[string]any
	if code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", malloryTok, map[string]any{"token": joeToken, "user": "joe"}, &refusal); code != http.StatusForbidden ||
		refusal["error"] != "only joe's sponsor or an administrator can manage joe's connections" {
		t.Fatalf("stranger paste for joe = %d %v", code, refusal)
	}
	var forJoe map[string]any
	if code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", aliceTok, map[string]any{"token": joeToken, "user": "joe"}, &forJoe); code != http.StatusOK || forJoe["set_by"] != "alice" {
		t.Fatalf("sponsor paste for joe = %d %v", code, forJoe)
	}
	if raw := echoCall(t, base, joeTok, "tokapp"); !strings.Contains(raw, "echo: hi") {
		t.Fatalf("joe on his own row = %s", raw)
	}
	awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
		return d["app"] == "tokapp" && d["user"] == joe.ID && d["credentialSource"] == "own"
	})
	if !up.sawAuth("Bearer " + joeToken) {
		t.Error("joe's own token never reached the upstream")
	}
	var joeList []map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/connect?user=joe", aliceTok, nil, &joeList); code != http.StatusOK || len(joeList) != 1 || joeList[0]["set_by"] != "alice" {
		t.Errorf("joe's list as alice = %d %v", code, joeList)
	}

	// An admin removes joe's row; joe rides the sponsor's again.
	if code := adminReq(t, http.MethodDelete, base+"/v1/connect/tokapp?user=joe", kimTok, nil, nil); code != http.StatusOK {
		t.Fatalf("admin delete for joe = %d", code)
	}
	if raw := echoCall(t, base, joeTok, "tokapp"); !strings.Contains(raw, "echo: hi") {
		t.Fatalf("joe after delete = %s", raw)
	}
	awaitMCPRecords(t, app, 2, func(d map[string]any) bool {
		return d["app"] == "tokapp" && d["user"] == joe.ID && d["credentialSource"] == "sponsor"
	})

	// Refusals at paste time, each in words.
	bad := []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"token": ""}, "a token is required. Paste the one tokapp gave you"},
		{map[string]any{"token": "x", "expires_at": "2020-01-01T00:00:00Z"}, "expires_at 2020-01-01 is already past"},
		{map[string]any{"token": "x", "expires_at": "tomorrow"}, "expires_at must be an RFC 3339 time"},
	}
	for _, tc := range bad {
		var out map[string]any
		if code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", malloryTok, tc.body, &out); code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out["error"]), tc.want) {
			t.Errorf("paste %v = %d %v, want 400 %q", tc.body, code, out, tc.want)
		}
	}

	// An expired own row denies and never falls through to the sponsor.
	mallory, err := app.store.Users().GetByUsername(ctx, "mallory")
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-24 * time.Hour)
	if _, err := app.broker.SetToken(ctx, row.ID, mallory.ID, "tok-old", secrets.GrantMeta{ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if raw := echoCall(t, base, malloryTok, "tokapp"); !strings.Contains(raw, "your token for app tokapp expired on "+past.UTC().Format("2006-01-02")) {
		t.Fatalf("expired own row = %s", raw)
	}

	// One identity record per act, with the actor and the subject.
	connects := identityActions(t, app, "token.connect")
	if len(connects) != 2 || connects[0]["user"] != alice.ID || connects[0]["setBy"] != alice.ID ||
		connects[0]["fingerprint"] != secrets.Fingerprint(aliceToken) || connects[0]["probe"] != "ok" ||
		connects[1]["user"] != joe.ID || connects[1]["setBy"] != alice.ID {
		t.Errorf("token.connect records = %v", connects)
	}
	if agents := identityActions(t, app, "connection.agents"); len(agents) != 1 || agents[0]["allow"] != true || agents[0]["user"] != alice.ID {
		t.Errorf("connection.agents records = %v", agents)
	}
	if gone := identityActions(t, app, "token.disconnect"); len(gone) != 1 || gone[0]["user"] != joe.ID || gone[0]["setBy"] != kim.ID {
		t.Errorf("token.disconnect records = %v", gone)
	}

	// Deprovisioning takes the token row and names its kind.
	app.deprovisionGrants(ctx, alice.ID, "deactivated")
	if rows := userGrants(t, app, alice.ID); len(rows) != 0 {
		t.Errorf("alice's rows after deprovision = %d, want none", len(rows))
	}
	if removes := grantRemoveEvents(t, app, alice.ID); len(removes) != 1 || removes[0]["kind"] != "token" {
		t.Errorf("apps.grant.remove for alice = %v, want one token row", removes)
	}
}

// TestConnectTokenSharedAndRefusals: with credential.agents shared an agent
// without a row runs on the shared secret and the record says so, a human
// without a row is still refused, a shared secret on an agents-own app is
// refused at set time, and a token kind on a command runtime is refused at
// install with the one-identity sentence.
func TestConnectTokenSharedAndRefusals(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	alice := seedGatewayUser(t, app, "alice", "dev")
	joe := seedAgent(t, app, "joe", "alice", "dev")
	seedGatewayUser(t, app, "kim", AdminRole)
	shared := installTokenApp(t, app, up, "sharedapp", "shared")
	ownOnly := installTokenApp(t, app, up, "ownapp", "own")
	catRecompile(t, app)
	kimTok := loginDeviceFlow(t, base, "kim", "hunter2!")

	// The server's own secret is the shared row here (dev is a business role
	// in this fixture, so a per-role row is refused on its own grounds).
	const sharedValue = "sk-shared-NEVER-LEAK"
	var out map[string]any
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/apps/"+shared.ID+"/secrets", kimTok,
		map[string]any{"value": sharedValue}, &out); code != http.StatusCreated {
		t.Fatalf("shared secret on sharedapp = %d %v", code, out)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/apps/"+ownOnly.ID+"/secrets", kimTok,
		map[string]any{"value": sharedValue}, &out); code != http.StatusBadRequest ||
		out["error"] != "the MCP server ownapp uses each caller's own token (credential.kind token) and lets no agent use a shared account (credential.agents own), so a static secret would never be used. Set credential.agents: shared in the manifest first." {
		t.Fatalf("shared secret on ownapp = %d %v", code, out)
	}

	joeTok := sessionToken(t, base, "joe")
	aliceTok := sessionToken(t, base, "alice")
	if raw := echoCall(t, base, joeTok, "sharedapp"); !strings.Contains(raw, "echo: hi") || strings.Contains(raw, sharedValue) {
		t.Fatalf("joe on the shared row = %s", raw)
	}
	recs := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
		return d["app"] == "sharedapp" && d["user"] == joe.ID && d["credentialSource"] == "shared"
	})
	if recs[0]["credentialOwner"] != nil {
		t.Errorf("shared record carries an owner: %v", recs[0])
	}
	if !up.sawAuth("Bearer " + sharedValue) {
		t.Error("the shared secret never reached the upstream")
	}
	if raw := echoCall(t, base, aliceTok, "sharedapp"); !strings.Contains(raw, "MCP server sharedapp needs your own token and none is stored for you. Run straza connect sharedapp, or paste one on your credentials page at "+testConnectPage) {
		t.Fatalf("human on agents shared = %s", raw)
	}
	if raw := echoCall(t, base, joeTok, "ownapp"); !strings.Contains(raw, "agent joe has no token for app ownapp. Its sponsor alice sets one on their credentials page at "+testConnectPage+", or an administrator sets it with strazactl connect ownapp --user joe") {
		t.Fatalf("agent on agents own = %s", raw)
	}
	// A call refused for want of a credential never ran, so its record is a
	// deny with the refusal as its reason and no credential fields.
	for _, row := range []struct{ app, user, reason string }{
		{"sharedapp", alice.ID, "MCP server sharedapp needs your own token and none is stored for you. Run straza connect sharedapp, or paste one on your credentials page at " + testConnectPage},
		{"ownapp", joe.ID, "agent joe has no token for app ownapp. Its sponsor alice sets one on their credentials page at " + testConnectPage + ", or an administrator sets it with strazactl connect ownapp --user joe"},
	} {
		recs := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
			return d["app"] == row.app && d["user"] == row.user && d["effect"] == "deny"
		})
		if recs[0]["reason"] != row.reason || recs[0]["credentialSource"] != nil || recs[0]["credentialId"] != nil {
			t.Errorf("refused %s record = %v, want reason %q and no credential fields", row.app, recs[0], row.reason)
		}
	}

	// A caller kind on a process runtime never installs.
	var refusal map[string]any
	code := rawReq(t, http.MethodPost, base+"/v1/admin/apps", kimTok, "application/yaml", []byte(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: procapp}
server: {name: straza.test/procapp, version: "1.0.0"}
straza:
  runtime:
    kind: command
    command: {exec: npx, args: ["-y", "example-mcp"]}
  credential:
    kind: token
    agents: sponsor
    inject: {as: env, name: TOKEN}
`), &refusal)
	if code != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(refusal["error"]), "credential.kind token gives each caller their own credential, which a command runtime cannot take: it is one process and one identity") {
		t.Fatalf("token on command install = %d %v", code, refusal)
	}
}
