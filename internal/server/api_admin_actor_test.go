package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// adminAuditEvents returns the data payloads of every straza.audit.admin
// event the outbox has seen, oldest first. It reads through ListRecent
// (published rows included): the relay marks rows published on its own
// schedule, so observing emits via ListUnpublished races the relay on a
// slow runner.
func adminAuditEvents(t *testing.T, app *App) []map[string]any {
	t.Helper()
	rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	var out []map[string]any
	for i := len(rows) - 1; i >= 0; i-- { // newest-first → oldest-first
		row := rows[i]
		if row.Subject != "straza.audit.admin" {
			continue
		}
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(row.CE), &ce); err != nil {
			t.Fatalf("bad CE in outbox: %v", err)
		}
		out = append(out, ce.Data)
	}
	return out
}

// lastAdminAction returns the newest straza.audit.admin payload with the
// given action, failing the test when none exists.
func lastAdminAction(t *testing.T, app *App, action string) map[string]any {
	t.Helper()
	events := adminAuditEvents(t, app)
	for i := len(events) - 1; i >= 0; i-- {
		if events[i]["action"] == action {
			return events[i]
		}
	}
	t.Fatalf("no straza.audit.admin event with action %q in outbox", action)
	return nil
}

// TestAdminAuditEventsCarryActor pins admin action attribution: every
// straza.audit.admin CloudEvent names WHO performed the mutation: the
// spec/events §2 minimum payload has promised `actor` since v1alpha1, and a
// governance product whose own admin actions are anonymous fails the
// change-management evidence question ("who locked this account? who minted
// that token?"). All three authentication lanes requireAdmin accepts must
// attribute: ID-token login (the console), session token, and the wat_
// admin API token. A missing actor must stay absent, never guessed, so
// each lane asserts the exact triple, not just presence.
func TestAdminAuditEventsCarryActor(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	grantAdmin(t, app, user.ID)
	// The target is a second person: a locked kim could not sign in to unlock.
	lee := mkHuman(t, app, "lee")

	assertActor := func(t *testing.T, data map[string]any, name, id, via string) {
		t.Helper()
		if got := data["actor"]; got != name {
			t.Errorf("actor = %v, want %q", got, name)
		}
		if got := data["actorId"]; got != id {
			t.Errorf("actorId = %v, want %q", got, id)
		}
		if got := data["actorVia"]; got != via {
			t.Errorf("actorVia = %v, want %q", got, via)
		}
	}

	// Lane 1: ID-token login (the console's lane).
	if code := adminReq(t, "POST", base+"/v1/admin/users/"+lee.ID+"/lock", idToken,
		map[string]string{"reason": "attribution test"}, nil); code != http.StatusOK {
		t.Fatalf("lock via login token = %d", code)
	}
	assertActor(t, lastAdminAction(t, app, "user.lock"), "kim", user.ID, "login")

	// Lane 2: session token (an enrolled session doing admin work).
	_, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token": idToken,
		"harness":  map[string]string{"name": "strazactl", "version": "dev"},
		"attestation": map[string]any{
			"managed": false, "hashes": map[string]string{},
		},
	})
	sessionToken, _ := checkin["session_token"].(string)
	if sessionToken == "" {
		t.Fatalf("checkin minted no session token: %v", checkin)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/users/"+lee.ID+"/unlock", sessionToken,
		nil, nil); code != http.StatusOK {
		t.Fatalf("unlock via session token = %d", code)
	}
	assertActor(t, lastAdminAction(t, app, "user.unlock"), "kim", user.ID, "session")

	// Lane 3: admin API token. The actor is the token, name + id, so a
	// leaked-token incident reads which credential acted, not which human.
	var minted map[string]string
	if code := adminReq(t, "POST", base+"/v1/admin/api-tokens", idToken,
		map[string]string{"name": "iga-pull", "scope": "full"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint api token = %d", code)
	}
	// The mint itself was performed by kim and must say so.
	assertActor(t, lastAdminAction(t, app, "api-token.create"), "kim", user.ID, "login")
	if code := adminReq(t, "POST", base+"/v1/admin/users/"+lee.ID+"/lock", minted["token"],
		map[string]string{"reason": "token lane"}, nil); code != http.StatusOK {
		t.Fatalf("lock via api token = %d", code)
	}
	assertActor(t, lastAdminAction(t, app, "user.lock"), "iga-pull", minted["id"], "api-token")
}
