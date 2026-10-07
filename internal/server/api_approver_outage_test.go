package server

import (
	"net/http"
	"testing"
)

// TestApproverRefreshOutageIs503 pins the wire half of the approval-service
// split: with the device (or user) table down, the refresh challenge and the
// signed refresh answer 503 service_unavailable with the generic body and
// Retry-After, never 404 "no such approver device", device_revoked or
// user_inactive (the app destroys its key on the latter two). Healthy
// control after disarm: the challenge mints again.
func TestApproverRefreshOutageIs503(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	deviceID, _, priv := enrollApproverDevice(t, base, adminTok)

	// A real challenge minted while healthy, so the signed refresh reaches
	// the device read under fault.
	var ch struct {
		Challenge string `json:"challenge"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/refresh/challenge", "", map[string]any{"approver_device_id": deviceID}, &ch); code != http.StatusOK {
		t.Fatalf("healthy challenge = %d", code)
	}
	sig := signApproverRefresh(t, priv, deviceID, ch.Challenge)

	for _, repo := range []string{"approvers", "users"} {
		fs.arm(repo, pgDown)
		if repo == "approvers" {
			code, hdr, out := callJSON(t, "POST", base+"/v1/approver/refresh/challenge", "", map[string]any{"approver_device_id": deviceID})
			requireOutage(t, "refresh/challenge "+repo, code, hdr, out)
			if out["code"] != codeServiceUnavailable {
				t.Errorf("refresh/challenge %s: code = %v, want service_unavailable", repo, out["code"])
			}
		}
		code, hdr, out := callJSON(t, "POST", base+"/v1/approver/refresh", "", map[string]any{
			"approver_device_id": deviceID, "challenge": ch.Challenge, "signature": sig,
		})
		requireOutage(t, "refresh "+repo, code, hdr, out)
		if out["code"] != codeServiceUnavailable {
			t.Errorf("refresh %s: code = %v, want service_unavailable (not device_revoked/user_inactive)", repo, out["code"])
		}
		fs.disarm()
	}
	if code := adminReq(t, "POST", base+"/v1/approver/refresh/challenge", "", map[string]any{"approver_device_id": deviceID}, &ch); code != http.StatusOK {
		t.Fatalf("healthy challenge after disarm = %d", code)
	}
}

// TestBearerLanesAnswerOutage pins the two other bearer lanes: the admin
// plane (strazactl, console) and the end-user
// approvals plane both carry a session token whose row read can hit a store
// outage; that is a 503 with the generic body, not 401 "session is no longer
// active" / "token rejected" (which make a CLI re-enroll and a console
// re-login for nothing). Healthy controls after disarm.
func TestBearerLanesAnswerOutage(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	tok, _ := checkinToken(t, app, base)

	fs.arm("sessions", pgDown)
	code, hdr, out := callJSON(t, "GET", base+"/v1/admin/roles", tok, nil)
	requireOutage(t, "admin bearer sessions", code, hdr, out)
	code, hdr, out = callJSON(t, "POST", base+"/v1/approvals/self/enroll-token", tok, map[string]any{})
	requireOutage(t, "approvals bearer sessions", code, hdr, out)
	fs.disarm()

	if code := adminReq(t, "GET", base+"/v1/admin/roles", tok, nil, nil); code != http.StatusOK {
		t.Fatalf("healthy admin read after disarm = %d", code)
	}
}
