package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
)

// TestBulkSessionRevoke pins the bulk revoke: one admin call stands down a
// user's sessions with ONE straza.revocation.sessions control event carrying
// the set (zero per-session control events), per-target push still firing,
// and enforcement identical to N single revokes.
func TestBulkSessionRevoke(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)

	// Two live sessions for kim.
	tok1, ses1 := checkinToken(t, app, base)
	_, ses2 := checkinToken(t, app, base)
	_ = tok1

	// Count control events by kind: the bulk path must emit exactly one
	// plural event and zero singular ones.
	var mu sync.Mutex
	var plural []string // payloads
	singular := 0
	unsub1, err := app.bus.SubscribeCore("straza.revocation.sessions", func(_ string, data []byte) {
		mu.Lock()
		plural = append(plural, string(data))
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsub1()
	unsub2, err := app.bus.SubscribeCore("straza.revocation.session", func(string, []byte) {
		mu.Lock()
		singular++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsub2()

	// An edge push subscriber on session 1: the bulk revoke must reach it
	// through the same per-target push lane as a single revoke.
	lines, cancelSSE, resp := sseStream(t, base, tok1)
	defer cancelSSE()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse = %d", resp.StatusCode)
	}
	waitForLine(t, lines, "event: ready")

	grantAdmin(t, app, user.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	var out struct {
		Revoked int `json:"revoked"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/sessions/revoke", idToken,
		map[string]string{"user": "kim"}, &out); code != http.StatusOK {
		t.Fatalf("bulk revoke = %d", code)
	}
	if out.Revoked != 2 {
		t.Fatalf("revoked = %d, want 2", out.Revoked)
	}

	// Enforcement: both sessions blocked in-memory, per-target push arrived.
	for _, id := range []string{ses1, ses2} {
		if !app.denylist.blocked(authn.Claims{Session: id}) {
			t.Errorf("session %s not denylisted", id)
		}
	}
	waitForLine(t, lines, "event: revocation")

	// Control traffic: exactly one plural event carrying both ids, no
	// singular events from the bulk path.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(plural)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(plural) != 1 {
		t.Fatalf("plural control events = %d, want 1", len(plural))
	}
	if singular != 0 {
		t.Errorf("bulk revoke emitted %d per-session control events, want 0", singular)
	}
	if !strings.Contains(plural[0], ses1) || !strings.Contains(plural[0], ses2) {
		t.Errorf("plural event missing ids: %s", plural[0])
	}

	// Validation faces.
	for _, body := range []map[string]any{
		{}, // neither
		{"user": "kim", "sessions": []string{"x"}}, // both
	} {
		if code := adminReq(t, "POST", base+"/v1/admin/sessions/revoke", idToken, body, nil); code != http.StatusBadRequest {
			t.Errorf("body %v = %d, want 400", body, code)
		}
	}
	big := make([]string, bulkRevokeMaxExplicit+1)
	for i := range big {
		big[i] = "sess-x"
	}
	if code := adminReq(t, "POST", base+"/v1/admin/sessions/revoke", idToken,
		map[string]any{"sessions": big}, nil); code != http.StatusBadRequest {
		t.Errorf("over-cap explicit set accepted")
	}
}

// TestBulkRevocationEventConverges proves the rev-17 envelope on the wire:
// a pod that only ever SEES the plural event (never the admin call) blocks
// every listed session, the fresh-pod convergence path.
func TestBulkRevocationEventConverges(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()

	ids := []string{
		"aaaaaaaa-1111-2222-3333-444444444401",
		"aaaaaaaa-1111-2222-3333-444444444402",
	}
	for _, id := range ids {
		if app.denylist.blocked(authn.Claims{Session: id}) {
			t.Fatal("session pre-blocked")
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"specversion": "1.0", "id": "rev-bulk-1", "type": "straza.revocation.sessions",
		"source": "test", "time": "2026-07-29T00:00:00Z",
		"data": map[string]any{"sessions": ids, "reason": "test"},
	})
	if err := app.bus.Publish(ctx, "straza.revocation.sessions", payload); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		blocked := 0
		for _, id := range ids {
			if app.denylist.blocked(authn.Claims{Session: id}) {
				blocked++
			}
		}
		if blocked == len(ids) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("plural event applied to %d/%d sessions", blocked, len(ids))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
