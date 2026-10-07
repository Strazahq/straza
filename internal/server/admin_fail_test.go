package server

import (
	"errors"
	"net/http"
	"testing"
)

// TestAdminPlane5xxCarryCorrelation pins two admin-plane 5xx answers that
// go through a.fail: the status and body message are unchanged, the body
// carries correlation_id equal to the X-Request-Id header, and exactly one
// Error record carries that id. Faults ride the existing faultStore
// (Users.GetByID / Devices.GetByID).
func TestAdminPlane5xxCarryCorrelation(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	// An admin API token bearer: the ID-token and session lanes both read
	// the caller through Users.GetByID, which the users fault would trip
	// before the handler, and the token lane reads no user, so the fault
	// reaches handleUsersGet.
	var minted struct {
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", idToken, map[string]any{"name": "fail", "scope": "full"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint = %d", code)
	}
	bearer := minted.Token

	lanes := []struct {
		name    string
		arm     func()
		method  string
		url     string
		wantMsg string
	}{
		{"users get (admin_users.go handleUsersGet)", func() { fs.arm("users", errors.New("boom")) },
			"GET", base + "/v1/admin/users/" + user.ID, "get failed"},
		{"device revoke lookup (admin_sessions.go handleDeviceRevoke)", func() { fs.arm("devices", errors.New("boom")) },
			"DELETE", base + "/v1/admin/users/" + user.ID + "/devices/no-such-device", "device lookup failed"},
	}
	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			buf.Reset()
			lane.arm()
			code, hdr, out := callJSON(t, lane.method, lane.url, bearer, nil)
			fs.disarm()
			if code != http.StatusInternalServerError {
				t.Fatalf("status = %d %v, want 500", code, out)
			}
			if out["error"] != lane.wantMsg {
				t.Fatalf("body error = %v, want %q", out["error"], lane.wantMsg)
			}
			id := hdr.Get("X-Request-Id")
			if id == "" || out["correlation_id"] != id {
				t.Fatalf("correlation_id = %v, X-Request-Id = %q", out["correlation_id"], id)
			}
			assertOneErrorWithCorrelation(t, buf, http.StatusInternalServerError, id)
		})
	}
}
