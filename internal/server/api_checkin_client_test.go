package server

import (
	"context"
	"net/http"
	"testing"
)

// TestCheckinRecordsClientVersion pins the additive client stamp: a check-in
// that names its build lands it on the session and the sessions list shows
// it, and one that sends nothing (an older client) leaves it blank.
func TestCheckinRecordsClientVersion(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	cases := []struct {
		name   string
		client map[string]string
		want   string
	}{
		{"stamped client", map[string]string{"version": "v1.2.3-4-gabcdef0", "commit": "abcdef0"}, "v1.2.3-4-gabcdef0"},
		{"older client sends nothing", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"id_token":    idToken,
				"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
				"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
			}
			if tc.client != nil {
				body["client"] = tc.client
			}
			code, resp := postJSON(t, base+"/v1/checkin", body)
			if code != http.StatusOK {
				t.Fatalf("checkin = %d: %v", code, resp)
			}
			ses, err := app.store.Sessions().GetByID(context.Background(), resp["session_id"].(string))
			if err != nil || ses.ClientVersion != tc.want {
				t.Fatalf("stored client version = %q, %v; want %q", ses.ClientVersion, err, tc.want)
			}
			var rows []sessionPayload
			if code := adminReq(t, http.MethodGet, base+"/v1/admin/sessions", adminTok, nil, &rows); code != http.StatusOK {
				t.Fatalf("sessions list = %d", code)
			}
			for _, r := range rows {
				if r.ID == ses.ID && r.ClientVersion != tc.want {
					t.Fatalf("listed client version = %q, want %q", r.ClientVersion, tc.want)
				}
			}
		})
	}
}
