package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestDeviceRevokeAdmin pins DELETE /v1/admin/users/{id}/devices/{deviceId}:
// the enrollment kill switch. Revocation is row-backed like the
// approver-device sibling (the device row is deleted, so the machine's key
// fails closed at every later check-in), plus the kill-switch cascade
// (revocation row, in-memory denylist, target-scoped push) for the in-flight
// window and peer pods.
func TestDeviceRevokeAdmin(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	// Enroll a machine key and prove its session refreshes BEFORE the revoke:
	// the positive control that makes the later 403s meaningful.
	key := newKeyHolder(t, base)
	key.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:revoke-me")
	deviceID := key.device
	deviceSessionToken, _ := key.mustCheckin()
	refresh := func() (int, map[string]any) {
		return key.call("/v1/checkin", deviceSessionToken, map[string]any{"session_token": deviceSessionToken,
			"harness": map[string]string{"name": "claude-code", "version": "2.1.0"}})
	}
	if code, body := refresh(); code != http.StatusOK {
		t.Fatalf("pre-revoke refresh = %d %v, want 200", code, body)
	}

	// Refusals first: the sibling admin routes' auth matrix and 404 shape.
	bare, err := app.store.Users().Create(ctx, store.User{Username: "somebody-else"})
	if err != nil {
		t.Fatal(err)
	}
	refusals := []struct {
		name   string
		url    string
		bearer string
		want   int
	}{
		{"no token", base + "/v1/admin/users/" + user.ID + "/devices/" + deviceID, "", http.StatusUnauthorized},
		{"unknown device", base + "/v1/admin/users/" + user.ID + "/devices/no-such-device", adminTok, http.StatusNotFound},
		// A real device under the WRONG user 404s identically to an unknown id:
		// the nesting is a belongs-to check, not a hint oracle.
		{"wrong owner", base + "/v1/admin/users/" + bare.ID + "/devices/" + deviceID, adminTok, http.StatusNotFound},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			if code := adminReq(t, http.MethodDelete, tc.url, tc.bearer, nil, nil); code != tc.want {
				t.Errorf("delete = %d, want %d", code, tc.want)
			}
		})
	}

	var out map[string]string
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/users/"+user.ID+"/devices/"+deviceID, adminTok, nil, &out); code != http.StatusOK || out["status"] != "revoked" {
		t.Fatalf("device revoke = %d %v", code, out)
	}

	// Row-backed: the enrollment is gone from the store and the admin list.
	if _, err := app.store.Devices().GetByID(ctx, deviceID); err == nil {
		t.Error("device row survived the revoke")
	}
	var devs []store.Device
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+user.ID+"/devices", adminTok, nil, &devs); code != http.StatusOK || len(devs) != 0 {
		t.Errorf("device list after revoke = %d %+v, want 200 empty", code, devs)
	}
	// Kill-switch cascade: persisted revocation row (boot-time denylist rebuild) and
	// the in-memory entry (this pod, immediately).
	revs, err := app.store.Revocations().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rv := range revs {
		if rv.Kind == store.RevokeDevice && rv.TargetID == deviceID {
			found = true
		}
	}
	if !found {
		t.Errorf("no device revocation row for %s: %+v", deviceID, revs)
	}
	if !app.denylist.deviceBlocked(deviceID) {
		t.Error("revoked device not on the in-memory denylist")
	}

	// Fail closed at checkin, both lanes. The key check-in finds no row, and
	// the refresh dies on the in-memory denylist before any store read (it
	// reads the device binding from the token's claims). A live session on a
	// revoked device cannot outlast its 300 s token.
	if code, body := key.checkin(); code != http.StatusForbidden || body["error"] != keyNotEnrolledMsg {
		t.Errorf("key checkin after revoke = %d %v, want 403 with %q", code, body, keyNotEnrolledMsg)
	}
	if code, body := refresh(); code != http.StatusForbidden || !strings.Contains(body["error"].(string), "revoked") {
		t.Errorf("session refresh after device revoke = %d %v, want 403 naming the revocation", code, body)
	}

	// A re-delete is a clean 404, exactly like the sibling delete routes.
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/users/"+user.ID+"/devices/"+deviceID, adminTok, nil, nil); code != http.StatusNotFound {
		t.Errorf("re-delete = %d, want 404", code)
	}
}

// TestDeviceRevokeRequiresAdmin pins the role gate: a valid non-admin session
// cannot revoke devices, its own included (self-service device retirement is
// deliberately not a surface; logout ends sessions, admins end enrollments).
func TestDeviceRevokeRequiresAdmin(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app) // kim, role dev, NOT straza-admin
	token, _ := checkinToken(t, app, base)

	d, _ := mintDevice(t, app, user.ID, store.DeviceClientKit, "sha256:mine")
	deviceID := d.ID

	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/users/"+user.ID+"/devices/"+deviceID, token, nil, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin device revoke = %d, want 403", code)
	}
	if _, err := app.store.Devices().GetByID(context.Background(), deviceID); err != nil {
		t.Errorf("refused revoke must leave the row: %v", err)
	}
}
