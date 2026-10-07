package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestAdminRoutesRefuseCodingHarnessSessions pins that the admin plane is
// closed to a session that a coding harness checked in, whatever roles the
// user holds: the agent in that harness runs as the user and holds the
// session token. Login tokens and the sessions of the human clients pass on
// both admin guards.
func TestAdminRoutesRefuseCodingHarnessSessions(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	login := loginDeviceFlow(t, base, "kim", "hunter2!")
	session := func(harness string) string {
		t.Helper()
		tok, _ := checkinTokenAs(t, base, harness)
		return tok
	}

	tests := []struct {
		name   string
		bearer string
		want   int
		says   string
	}{
		{"login token", login, http.StatusOK, ""},
		{"strazactl session", session("strazactl"), http.StatusOK, ""},
		{"console session", session("console"), http.StatusOK, ""},
		{"self-service session", session("self-service"), http.StatusOK, ""},
		{"claude-code session", session("claude-code"), http.StatusForbidden, `coding harness "claude-code"`},
		{"codex session", session("codex"), http.StatusForbidden, "Run strazactl from your own terminal"},
	}
	// One route per guard: the users list is behind requireAdmin, the apps
	// list behind requireServerAdmin.
	for _, route := range []string{"/v1/admin/users", "/v1/admin/apps"} {
		for _, tc := range tests {
			t.Run(route+" "+tc.name, func(t *testing.T) {
				var body json.RawMessage
				code := adminReq(t, http.MethodGet, base+route, tc.bearer, nil, &body)
				if code != tc.want {
					t.Fatalf("%s = %d %s, want %d", route, code, body, tc.want)
				}
				if tc.says == "" {
					return
				}
				var refusal struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(body, &refusal); err != nil || !strings.Contains(refusal.Error, tc.says) {
					t.Errorf("refusal %s must say %q", body, tc.says)
				}
			})
		}
	}
}
