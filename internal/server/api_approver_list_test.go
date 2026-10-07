package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// approverListRow mirrors the GET /v1/admin/approvers payload.
type approverListRow struct {
	ID               string     `json:"id"`
	UserID           string     `json:"user_id"`
	Username         string     `json:"username"`
	Name             string     `json:"name"`
	Platform         string     `json:"platform"`
	KeySecurityLevel string     `json:"key_security_level"`
	Attestation      string     `json:"attestation"`
	EnrolledAt       time.Time  `json:"enrolled_at"`
	LastSeen         *time.Time `json:"last_seen"`
	PushRoutes       int        `json:"push_routes"`
}

// TestApproversListAdmin pins GET /v1/admin/approvers, the discoverability
// half of the approver-device kill switch: the enroll response goes to the
// phone, not the admin, so the list is where an admin finds the apd_ id
// that DELETE /v1/admin/approvers/{id} needs.
// The list names each enrolled phone with its owner, posture, and
// push-registration count (so "2 enrolled · 1 push route" attributes to a
// specific device), and feeds the id straight into the existing revoke.
func TestApproversListAdmin(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	var rows []approverListRow
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/approvers", adminTok, nil, &rows); code != http.StatusOK || len(rows) != 0 {
		t.Fatalf("fresh list = %d %+v, want 200 empty", code, rows)
	}

	deviceID, _, _ := enrollApproverDevice(t, base, adminTok)
	if err := app.store.Approvers().UpsertPush(ctx, store.ApproverPush{
		DeviceID: deviceID, Kind: "unifiedpush", TokenOrEndpoint: "https://ntfy.example/kim",
	}); err != nil {
		t.Fatal(err)
	}

	rows = nil
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/approvers", adminTok, nil, &rows); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	if len(rows) != 1 {
		t.Fatalf("list = %+v, want exactly the enrolled phone", rows)
	}
	row := rows[0]
	if !strings.HasPrefix(row.ID, "apd_") || row.ID != deviceID {
		t.Errorf("id = %q, want the apd_ id the revoke route takes (%s)", row.ID, deviceID)
	}
	if row.UserID != user.ID || row.Username != "kim" {
		t.Errorf("owner = %s/%s, want %s/kim (operators read usernames, not UUIDs)", row.UserID, row.Username, user.ID)
	}
	if row.Name != "kim-pixel" || row.Platform != "android" || row.KeySecurityLevel != "strongbox" || row.Attestation != "none" {
		t.Errorf("posture = %+v, want the enrolled device's", row)
	}
	if row.EnrolledAt.IsZero() {
		t.Error("enrolled_at missing")
	}
	if row.PushRoutes != 1 {
		t.Errorf("push_routes = %d, want 1 (the count is what attributes the channel-status gap)", row.PushRoutes)
	}

	// The user filter takes a username or an id; an unknown value is an empty
	// list (same shape as the bulk session revoke's user face).
	for _, filter := range []string{"kim", user.ID} {
		rows = nil
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/approvers?user="+filter, adminTok, nil, &rows); code != http.StatusOK || len(rows) != 1 {
			t.Errorf("list?user=%s = %d %+v, want the one phone", filter, code, rows)
		}
	}
	rows = nil
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/approvers?user=ghost", adminTok, nil, &rows); code != http.StatusOK || len(rows) != 0 {
		t.Errorf("list?user=ghost = %d %+v, want 200 empty", code, rows)
	}

	// Auth matrix: bearer required, straza-admin required.
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/approvers", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}
	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Users().Create(ctx, store.User{Username: "bob", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	bobToken := loginDeviceFlow(t, base, "bob", "hunter2!")
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/approvers", bobToken, nil, nil); code != http.StatusForbidden {
		t.Errorf("non-admin = %d, want 403", code)
	}

	// The loop closes: the listed id feeds the EXISTING revoke, and the phone
	// leaves the list (row-backed, the same absence that kills its token).
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/approvers/"+deviceID, adminTok, nil, nil); code != http.StatusOK {
		t.Fatalf("revoke listed device = %d", code)
	}
	rows = nil
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/approvers", adminTok, nil, &rows); code != http.StatusOK || len(rows) != 0 {
		t.Errorf("list after revoke = %d %+v, want empty", code, rows)
	}
}
