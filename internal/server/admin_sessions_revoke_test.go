package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestDeviceRevokeRecord pins the straza.audit.admin record of an admin
// device revoke: one device.revoke with the actor, the device as target,
// its user and the reason when the revoke lands, and none for a 404, for a
// write the database refused, for a second revoke of the same device and
// for the key reuse refusal, which revokes a device on its own lane.
func TestDeviceRevokeRecord(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	admin, _ := checkinToken(t, app, base)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	other, err := app.store.Users().Create(context.Background(), store.User{Username: "somebody-else"})
	if err != nil {
		t.Fatal(err)
	}
	enrolled := 0
	enroll := func(t *testing.T) *keyHolder {
		t.Helper()
		enrolled++
		h := newKeyHolder(t, base)
		h.enroll(idToken, fmt.Sprintf("sha256:b352-%d", enrolled))
		return h
	}
	records := func(device string) []map[string]any {
		var out []map[string]any
		for _, ev := range adminAuditEvents(t, app) {
			if ev["action"] == actionDeviceRevoke && ev["target"] == device {
				out = append(out, ev)
			}
		}
		return out
	}
	revoke := func(user, device string) int {
		code, _, _ := callJSON(t, http.MethodDelete, base+"/v1/admin/users/"+user+"/devices/"+device, admin, nil)
		return code
	}

	cases := []struct {
		name   string
		fault  string
		user   string
		device func(t *testing.T) string
		status int
		want   int
	}{
		{"an unknown device", "", kim.ID, func(*testing.T) string { return "no-such-device" }, http.StatusNotFound, 0},
		{"a device of another user", "", other.ID, func(t *testing.T) string { return enroll(t).device }, http.StatusNotFound, 0},
		{"a write the database refused", "devices.revoke", kim.ID, func(t *testing.T) string { return enroll(t).device }, http.StatusInternalServerError, 0},
		{"a revoke that lands", "", kim.ID, func(t *testing.T) string { return enroll(t).device }, http.StatusOK, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			device := tc.device(t)
			if tc.fault != "" {
				fs.arm(tc.fault, pgDown)
			}
			code := revoke(tc.user, device)
			fs.disarm()
			if code != tc.status {
				t.Fatalf("the revoke = %d, want %d", code, tc.status)
			}
			got := records(device)
			if len(got) != tc.want {
				t.Fatalf("%d device.revoke records, want %d: %v", len(got), tc.want, got)
			}
			if tc.want == 0 {
				return
			}
			for k, v := range map[string]any{"actor": "kim", "actorId": kim.ID, "actorVia": "session",
				"target": device, "device": device, "user": kim.ID, "reason": "device revoked by admin"} {
				if got[0][k] != v {
					t.Errorf("the record's %s = %v, want %v", k, got[0][k], v)
				}
			}
			if code := revoke(tc.user, device); code != http.StatusNotFound {
				t.Errorf("a second revoke = %d, want 404", code)
			}
			if n := len(records(device)); n != 1 {
				t.Errorf("%d device.revoke records after a second revoke, want 1", n)
			}
		})
	}

	t.Run("the key reuse refusal", func(t *testing.T) {
		h := enroll(t)
		h.counter = 2
		h.mustCheckin()
		if code, out := h.call("/v1/checkin", "", h.checkinBody(1)); code != http.StatusForbidden || out["error"] != keyReusedMsg {
			t.Fatalf("a reused counter = %d %v, want 403 %q", code, out, keyReusedMsg)
		}
		if _, err := app.store.Devices().GetByID(context.Background(), h.device); err == nil {
			t.Fatal("the key reuse refusal left the device enrolled")
		}
		if n := len(records(h.device)); n != 0 {
			t.Errorf("%d device.revoke records for the key reuse refusal, want 0", n)
		}
	})
}
