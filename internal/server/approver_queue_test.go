package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestApproverRowCarriesTheLane pins that each row of the approver feed
// names the lane its record came in on, so the self-service page and the
// phone word a hook hold as running on the agent's retry. The ticket is
// one approval_request raised for a command, whose lane is the gateway's
// although the call is not an MCP call.
func TestApproverRowCarriesTheLane(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	_, deviceTok, _ := enrollApproverDevice(t, base, adminTok)

	now := time.Now().UTC().Truncate(time.Microsecond)
	cases := []struct {
		name    string
		lane    string
		class   string
		summary string
	}{
		{"a hold the hook raised", "hook", "hold", "shell.exec: make deploy"},
		{"a hold the gateway raised", "gateway", "hold", "mcp.call demo-tools:get-sum"},
		{"a ticket approval_request raised for a command", "gateway", "ticket", "shell.exec: make release"},
	}
	want := map[string]string{}
	for i, tc := range cases {
		a, err := app.store.Approvals().Insert(context.Background(), store.Approval{
			SessionID: "s-lane", UserID: kim.ID, Username: "kim", RuleID: "r-lane", SetName: "g",
			ArgvHash: "lane-" + tc.lane + "-" + tc.class, Lane: tc.lane, Class: tc.class, Summary: tc.summary,
			State: "pending", CreatedAt: now.Add(time.Duration(i) * time.Second), ExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("seed %s: %v", tc.name, err)
		}
		want[a.ID] = tc.name
	}

	var rows []map[string]any
	if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=mine", deviceTok, nil, &rows); code != http.StatusOK {
		t.Fatalf("GET /v1/approver/pending?scope=mine = %d, want 200", code)
	}
	got := map[string]any{}
	for _, r := range rows {
		if id, _ := r["id"].(string); want[id] != "" {
			got[want[id]] = r["lane"]
		}
	}
	for _, tc := range cases {
		if got[tc.name] != tc.lane {
			t.Errorf("%s: lane = %v, want %q", tc.name, got[tc.name], tc.lane)
		}
	}
}
