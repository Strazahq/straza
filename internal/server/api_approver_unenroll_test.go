package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// identityUpdatedEvents returns the data payloads of every
// straza.identity.updated event the outbox has seen, oldest first. Reads
// through ListRecent for the same reason adminAuditEvents does: observing
// emits via ListUnpublished races the relay.
func identityUpdatedEvents(t *testing.T, app *App) []map[string]any {
	t.Helper()
	rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	var out []map[string]any
	for i := len(rows) - 1; i >= 0; i-- { // newest-first -> oldest-first
		row := rows[i]
		if row.Subject != "straza.identity.updated" {
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

// lastIdentityAction returns the newest straza.identity.updated payload with
// the given action, failing the test when none exists.
func lastIdentityAction(t *testing.T, app *App, action string) map[string]any {
	t.Helper()
	events := identityUpdatedEvents(t, app)
	for i := len(events) - 1; i >= 0; i-- {
		if events[i]["action"] == action {
			return events[i]
		}
	}
	t.Fatalf("no straza.identity.updated event with action %q in outbox", action)
	return nil
}

// TestApproverSelfUnenroll pins the self-unenroll endpoint
// (DELETE /v1/approver/enrollment): the
// device's own bearer retires the calling enrollment with the admin revoke's
// exact effect (row-backed: the row dies, every future bearer use 401s), the
// audit event attributes the initiator (via=self vs the admin lane's
// via=admin), and the lane is deliberately bearer-only (the accepted visible
// fail-closed self-DoS; no body, no side effects beyond the calling device).
func TestApproverSelfUnenroll(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	devA, tokA, _ := enrollApproverDevice(t, base, adminTok)

	// 1. Bearer gate: no bearer and a non-approver token are both refused;
	// nothing is deleted by an unauthenticated call.
	if code := adminReq(t, "DELETE", base+"/v1/approver/enrollment", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("self-unenroll with no bearer = %d, want 401", code)
	}
	if code := adminReq(t, "DELETE", base+"/v1/approver/enrollment", adminTok, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("self-unenroll with a session token = %d, want 401", code)
	}
	if _, err := app.store.Approvers().GetDevice(context.Background(), devA); err != nil {
		t.Fatalf("device row must survive refused calls: %v", err)
	}

	// 2. Happy path: the device's own bearer retires the enrollment. 204, no body.
	if code := adminReq(t, "DELETE", base+"/v1/approver/enrollment", tokA, nil, nil); code != http.StatusNoContent {
		t.Fatalf("self-unenroll = %d, want 204", code)
	}
	if _, err := app.store.Approvers().GetDevice(context.Background(), devA); err == nil {
		t.Fatal("device row must be gone after self-unenroll")
	}
	ev := lastIdentityAction(t, app, "approver-device-revoked")
	if ev["device"] != devA || ev["user"] != kim.ID {
		t.Errorf("revoke event names {device:%v user:%v}, want {%s %s}", ev["device"], ev["user"], devA, kim.ID)
	}
	if ev["via"] != "self" {
		t.Errorf("self-unenroll event via = %v, want self", ev["via"])
	}

	// 3. The bearer is dead everywhere after (row-backed revocation), and the
	// second unenroll call is one of those 401s: idempotence lives at the
	// credential layer, exactly like the admin revoke.
	var errResp struct {
		Code string `json:"code"`
	}
	if code := adminReq(t, "GET", base+"/v1/approver/pending", tokA, nil, &errResp); code != http.StatusUnauthorized {
		t.Errorf("pending after self-unenroll = %d, want 401", code)
	}
	if errResp.Code != codeDeviceRevoked {
		t.Errorf("pending 401 code = %q, want %q", errResp.Code, codeDeviceRevoked)
	}
	errResp.Code = ""
	if code := adminReq(t, "DELETE", base+"/v1/approver/enrollment", tokA, nil, &errResp); code != http.StatusUnauthorized {
		t.Errorf("second self-unenroll = %d, want 401", code)
	}
	if errResp.Code != codeDeviceRevoked {
		t.Errorf("second self-unenroll 401 code = %q, want %q", errResp.Code, codeDeviceRevoked)
	}
	// A device-key refresh can no longer even fetch a challenge.
	if code := adminReq(t, "POST", base+"/v1/approver/refresh/challenge", "",
		map[string]any{"approver_device_id": devA}, nil); code != http.StatusNotFound {
		t.Errorf("refresh challenge after self-unenroll = %d, want 404", code)
	}

	// 4. The admin lane emits the same action attributed via=admin, so the two
	// initiators stay distinguishable on the audit record.
	devB, tokB, _ := enrollApproverDevice(t, base, adminTok)
	if code := adminReq(t, "DELETE", base+"/v1/admin/approvers/"+devB, adminTok, nil, nil); code != http.StatusOK {
		t.Fatalf("admin revoke = %d", code)
	}
	ev = lastIdentityAction(t, app, "approver-device-revoked")
	if ev["device"] != devB || ev["via"] != "admin" {
		t.Errorf("admin revoke event = {device:%v via:%v}, want {%s admin}", ev["device"], ev["via"], devB)
	}
	if code := adminReq(t, "GET", base+"/v1/approver/pending", tokB, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("pending after admin revoke = %d, want 401", code)
	}
}
