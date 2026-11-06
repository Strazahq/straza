package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// edgeClock is a settable clock for the subject cache.
type edgeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *edgeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *edgeClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// TestSubjectCacheEdge pins the cache rule on its own: a subject reads as
// missing from its edge on, a zero edge never ends it, and a put hands back
// the subject it replaces even when that one was past its edge, which the
// check-in needs to notice a role change.
func TestSubjectCacheEdge(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	clk := &edgeClock{t: t0}
	c := newSubjectCache()
	c.now = clk.now
	dev := policy.Subject{User: "kim", Roles: []string{"dev"}}
	c.put("windowed", dev, t0.Add(time.Hour))
	c.put("open", dev, time.Time{})

	for _, tc := range []struct {
		name    string
		at      time.Time
		session string
		want    bool
	}{
		{"windowed before the edge", t0.Add(time.Hour - time.Second), "windowed", true},
		{"windowed at the edge", t0.Add(time.Hour), "windowed", false},
		{"windowed after the edge", t0.Add(2 * time.Hour), "windowed", false},
		{"no window ten years on", t0.AddDate(10, 0, 0), "open", true},
		{"unknown session", t0, "nobody", false},
	} {
		clk.set(tc.at)
		if _, ok := c.get(tc.session); ok != tc.want {
			t.Errorf("%s: cached = %v, want %v", tc.name, ok, tc.want)
		}
	}

	clk.set(t0.Add(2 * time.Hour))
	prev, ok := c.put("windowed", policy.Subject{User: "kim"}, time.Time{})
	if !ok || !slices.Equal(prev.Roles, []string{"dev"}) {
		t.Errorf("put over an ended subject returned %v, %v, want the dev subject it replaced", prev, ok)
	}
}

// TestRoleWindowEdgeEndsCachedSubject pins that a role whose window opens or
// closes mid-session changes decisions at that time, not at the next
// check-in. At the edge the cached subject reads as missing, so decide and
// the gateway answer the check-in-again 401, and the check-in that follows
// builds the subject with the roles of the new time. A subject whose roles
// carry no window never ends.
func TestRoleWindowEdgeEndsCachedSubject(t *testing.T) {
	t.Parallel()
	clk := &edgeClock{t: time.Now()}
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.subjects.now = clk.now }})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", adminTok, "application/yaml", []byte(rmPolicy)); code != http.StatusCreated {
		t.Fatalf("apply = %d %s", code, b)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/block-rm/activate", adminTok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	devRole, err := app.store.Roles().GetByName(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	rm := map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x"}
	harness := map[string]string{"name": "claude-code", "version": "2.1.0"}
	attestation := map[string]any{"managed": false, "hashes": map[string]string{}}

	// devRule reports whether a decision came from the dev-matched rule. The
	// standalone starter denies rm -rf for everyone, so the rule id, not the
	// effect, shows whether the dev role applied.
	devRule := func(dec map[string]any) bool { return dec["ruleId"] == "no-rm-rf" }

	// Each row gives a fresh user one dev assignment whose window edge is an
	// hour after the row starts.
	for _, tc := range []struct {
		name, user          string
		ends                bool // true: the window ends at the edge; false: it opens there
		devBefore, devAfter bool
	}{
		{"a role that ends mid-session", "edge-ends", true, true, false},
		{"a role that starts mid-session", "edge-starts", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			edge := clk.now().Add(time.Hour)
			u, err := app.store.Users().Create(ctx, store.User{Username: tc.user})
			if err != nil {
				t.Fatal(err)
			}
			a := store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: devRole.ID}
			if tc.ends {
				a.ValidTo = &edge
			} else {
				a.ValidFrom = &edge
			}
			if _, err := app.store.Roles().Assign(ctx, a); err != nil {
				t.Fatal(err)
			}
			app.resolver.Bump()
			idt, err := app.tokens.MintIDToken(u.ID, "straza", time.Minute, u.Username, "")
			if err != nil {
				t.Fatal(err)
			}
			code, body := postJSON(t, base+"/v1/checkin", map[string]any{"id_token": idt, "harness": harness, "attestation": attestation})
			if code != http.StatusOK {
				t.Fatalf("checkin = %d %v", code, body)
			}
			token, sessionID := body["session_token"].(string), body["session_id"].(string)

			clk.set(edge.Add(-time.Second))
			if code, dec := decide(t, base, token, rm); code != http.StatusOK || devRule(dec) != tc.devBefore {
				t.Fatalf("decide a second before the edge = %d %v, want the dev rule applied %v", code, dec, tc.devBefore)
			}

			clk.set(edge)
			code, dec := decide(t, base, token, rm)
			if code != http.StatusUnauthorized || dec["error"] != sessionStateExpiredMsg {
				t.Fatalf("decide at the edge = %d %v, want 401 %q", code, dec, sessionStateExpiredMsg)
			}
			if code, _, raw := mcpCall(t, base, token, "ping", nil); code != http.StatusUnauthorized {
				t.Errorf("gateway at the edge = %d %s, want 401", code, raw)
			}

			code, body = postJSON(t, base+"/v1/checkin", map[string]any{
				"session_token": token, "harness": harness, "attestation": attestation,
			})
			if code != http.StatusOK || body["session_id"] != sessionID {
				t.Fatalf("check-in after the edge = %d %v, want 200 for session %s", code, body, sessionID)
			}
			roles, _ := body["roles"].([]any)
			if hasDev := slices.Contains(roles, any("dev")); hasDev != tc.devAfter {
				t.Errorf("roles after the edge = %v, want dev present %v", roles, tc.devAfter)
			}
			if code, dec := decide(t, base, body["session_token"].(string), rm); code != http.StatusOK || devRule(dec) != tc.devAfter {
				t.Errorf("decide after the check-in = %d %v, want the dev rule applied %v", code, dec, tc.devAfter)
			}
		})
	}

	// kim's dev assignment has no window, so her subject never ends.
	token, _ := checkinTokenAs(t, base, "claude-code")
	clk.set(clk.now().AddDate(10, 0, 0))
	if code, dec := decide(t, base, token, rm); code != http.StatusOK || !devRule(dec) {
		t.Errorf("decide ten years on with no window = %d %v, want the role-matched deny", code, dec)
	}
}

// lostRoleWorld is one deployment of replicas a and b in which kim holds
// dev, dev reaches every tool of echoapp, a policy allows echoapp for dev,
// and boss is an administrator.
type lostRoleWorld struct {
	a, b         *App
	baseA, baseB string
	kim          store.User
}

// newLostRoleWorld boots the deployment with both replicas converged.
func newLostRoleWorld(t *testing.T) lostRoleWorld {
	t.Helper()
	ctx := context.Background()
	var w lostRoleWorld
	w.a, w.b, w.baseA, w.baseB = replicaPair(t)
	up := startGatewayUpstream(t)
	dev, err := w.a.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	w.kim = seedGatewayUser(t, w.a, "kim", "dev")
	grantAdmin(t, w.a, seedGatewayUser(t, w.a, "boss", "boss-desk").ID)
	setAccess(t, w.a, dev.ID, putServer(t, w.a, echoManifest(up.URL)), true, `["*"]`)
	activateSets(t, w.a, map[string]string{"base": policyText("base", 100, allowEchoapp)})
	for _, app := range []*App{w.a, w.b} {
		if err := app.converge(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// echoParams are the params of a tools/call of echoapp's echo.
var echoParams = map[string]any{"name": "echoapp__echo", "arguments": map[string]any{"text": "hi"}}

// kimDenials asserts that exactly want deny records of kim's calls of
// echoapp's echo exist in session, and that each names kim and the reason.
func kimDenials(t *testing.T, app *App, kim store.User, session string, want int) {
	t.Helper()
	recs := awaitMCPRecords(t, app, want, func(d map[string]any) bool {
		return d["session"] == session && d["app"] == "echoapp" && d["toolName"] == "echo" && d["effect"] == "deny"
	})
	for _, d := range recs {
		if d["user"] != kim.ID || !strings.Contains(fmt.Sprint(d["reason"]), "no access row for this session's roles admits it") {
			t.Errorf("the deny record = %v, want kim's id and the reason", d)
		}
	}
}

// TestReplicaServesOnlyTheTokensRoles pins the cache rule across replicas:
// a replica serves a cached subject only to a token whose rol claim hashes
// that subject's roles. kim's session is cached on a and on b, an
// administrator removes dev on b, and kim's refresh lands on b, which mints
// a token without dev. a still holds the subject from before, so each
// reader of the cache on a answers that token with the check-in-again 401
// and writes no record, a refresh that lands on b again changes nothing on
// a, and once a refresh lands on a the call meets the unknown tool with one
// deny record. Before the removal a token b minted for the same roles is
// served on a with no check-in.
func TestReplicaServesOnlyTheTokensRoles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newLostRoleWorld(t)
	tok := sessionToken(t, w.baseA, "kim")
	tok = checkInAgain(t, w.baseB, tok)
	session := sessionOf(t, w.a, tok)
	if code, _, raw := mcpCall(t, w.baseA, tok, "tools/call", echoParams); code != http.StatusOK || !strings.Contains(string(raw), "echo: hi") {
		t.Fatalf("before the removal a answered b's token with %d %s, want the call to run", code, raw)
	}

	asg, err := w.a.store.Roles().ListAssignments(ctx, store.SubjectUser, w.kim.ID)
	if err != nil || len(asg) != 1 {
		t.Fatalf("kim's assignments = %v %v, want one", asg, err)
	}
	if code := adminReq(t, http.MethodDelete, w.baseB+"/v1/admin/assignments/"+asg[0].ID, loginDeviceFlow(t, w.baseB, "boss", "hunter2!"), nil, nil); code != http.StatusOK {
		t.Fatalf("unassign on b = %d", code)
	}
	w.a.convCons.Handle(ctx, "straza.identity.updated",
		replicaEvent{"straza.identity.updated", map[string]any{"id": w.kim.ID}}.payload(t, w.b.instance))
	tok = checkInAgain(t, w.baseB, tok)

	errorOf := func(body map[string]any) string { s, _ := body["error"].(string); return s }
	for _, tc := range []struct {
		name string
		send func() (int, map[string]any)
	}{
		{"a tools/call on /mcp", func() (int, map[string]any) {
			code, body, _ := mcpCall(t, w.baseA, tok, "tools/call", echoParams)
			return code, body
		}},
		{"a tools/list on /mcp", func() (int, map[string]any) {
			code, body, _ := mcpCall(t, w.baseA, tok, "tools/list", nil)
			return code, body
		}},
		{"a tools/call on the server's own endpoint", func() (int, map[string]any) {
			code, body, _ := mcpPost(t, w.baseA+"/mcp/echoapp", tok, "tools/call", map[string]any{"name": "echo"})
			return code, body
		}},
		{"a hook decision", func() (int, map[string]any) {
			return decide(t, w.baseA, tok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "ls"})
		}},
		{"a git grant", func() (int, map[string]any) {
			return postJSONAuth(t, w.baseA+"/v1/grants", tok, map[string]any{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := tc.send(); code != http.StatusUnauthorized || errorOf(body) != sessionStateExpiredMsg {
				t.Errorf("a answered the token b minted without dev with %d %v, want 401 %q", code, body, sessionStateExpiredMsg)
			}
		})
	}

	tok = checkInAgain(t, w.baseB, tok)
	if code, body, _ := mcpCall(t, w.baseA, tok, "tools/call", echoParams); code != http.StatusUnauthorized || errorOf(body) != sessionStateExpiredMsg {
		t.Errorf("after a second refresh on b, a answered %d %v, want 401 %q", code, body, sessionStateExpiredMsg)
	}
	tok = checkInAgain(t, w.baseA, tok)
	_, body, raw := mcpCall(t, w.baseA, tok, "tools/call", echoParams)
	if rpcErr, _ := body["error"].(map[string]any); rpcErr["message"] != `unknown tool "echoapp__echo"` {
		t.Errorf("after a refresh on a, a answered %s, want the unknown tool", raw)
	}
	kimDenials(t, w.a, w.kim, session, 1)
}

// TestRefreshReadsRolesAgainAfterOneTokenLifetime pins the bound on a
// replica that missed the identity event of a role removal: its resolver
// keeps the roles it read for at most one session token lifetime, so a
// refresh within it keeps the removed role and the first refresh after it
// drops the role, and the call it then refuses writes one deny record.
func TestRefreshReadsRolesAgainAfterOneTokenLifetime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := &edgeClock{t: time.Now()}
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.subjects.now = clk.now }})
	up := startGatewayUpstream(t)
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	kim := seedGatewayUser(t, app, "kim", "dev")
	setAccess(t, app, dev.ID, putServer(t, app, echoManifest(up.URL)), true, `["*"]`)
	activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	t0 := clk.now()
	tok := sessionToken(t, base, "kim")
	session := sessionOf(t, app, tok)

	asg, err := app.store.Roles().ListAssignments(ctx, store.SubjectUser, kim.ID)
	if err != nil || len(asg) != 1 {
		t.Fatalf("kim's assignments = %v %v, want one", asg, err)
	}
	// Another replica's removal: the row goes, and this replica hears no event.
	if err := app.store.Roles().Unassign(ctx, asg[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		at   time.Duration
		runs bool
	}{
		{"a refresh a second before one token lifetime", authn.DefaultTokenTTL - time.Second, true},
		{"a refresh at one token lifetime", authn.DefaultTokenTTL, false},
	} {
		clk.set(t0.Add(tc.at))
		tok = checkInAgain(t, base, tok)
		_, _, raw := mcpCall(t, base, tok, "tools/call", echoParams)
		if runs := strings.Contains(string(raw), "echo: hi"); runs != tc.runs {
			t.Errorf("%s: the call answered %s, want it to run %v", tc.name, raw, tc.runs)
		}
	}
	kimDenials(t, app, kim, session, 1)
}
