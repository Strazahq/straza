package server

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

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
			_, deviceToken := mintDevice(t, app, u.ID, store.DeviceClientKit, "sha256:"+u.Username)
			code, body := checkinDeviceAs(t, base, deviceToken, "claude-code", "2.1.0")
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
