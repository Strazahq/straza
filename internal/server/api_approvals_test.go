package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/policy"
)

func seedApproval(t *testing.T, app *App, requesterID string, roles []string) approval.Record {
	t.Helper()
	rec, err := app.approval.Request(context.Background(), approval.RequestInput{
		SessionID: "s-x", UserID: requesterID, Username: "nova", RuleID: "r-1", SetName: "g",
		ArgvHash: "sha256:" + requesterID, Lane: "hook", Summary: "shell.exec: deploy-prod",
		Spec: policy.ApproveSpec{Roles: roles, TimeoutSeconds: 90, RetryTTLSeconds: 60},
	})
	if err != nil {
		t.Fatalf("seed approval: %v", err)
	}
	return rec
}

type approvalListResp struct {
	Approvals []struct {
		ID        string  `json:"id"`
		State     string  `json:"state"`
		DecidedAt *string `json:"decidedAt"`
	} `json:"approvals"`
}

type approvalDecideResp struct {
	Approval struct {
		State         string `json:"state"`
		DecidedByName string `json:"decidedByName"`
		Channel       string `json:"channel"`
	} `json:"approval"`
}

// TestApprovalsAdminList: GET /v1/admin/approvals is admin-gated and returns
// pending records with a null decidedAt.
func TestApprovalsAdminList(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	rec := seedApproval(t, app, "u-other", []string{"dev"})

	var out approvalListResp
	code := adminReq(t, "GET", base+"/v1/admin/approvals?state=pending", tok, nil, &out)
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	if len(out.Approvals) != 1 || out.Approvals[0].ID != rec.ID {
		t.Fatalf("list = %+v, want the pending record %s", out.Approvals, rec.ID)
	}
	if out.Approvals[0].State != "pending" || out.Approvals[0].DecidedAt != nil {
		t.Errorf("pending record must have null decidedAt: %+v", out.Approvals[0])
	}
}

// TestApprovalDecideEndpoints: approve/deny take a person's client
// (requirePersonClient), the service validates approver roles, and error
// statuses map to 404/403/409/410.
func TestApprovalDecideEndpoints(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app) // kim holds role dev
	grantAdmin(t, app, user.ID)
	tok, _ := checkinTokenAs(t, base, "console")

	// Happy approve: requester is someone else; kim holds an approver role.
	rec := seedApproval(t, app, "u-other", []string{"dev"})
	var out approvalDecideResp
	if code := adminReq(t, "POST", base+"/v1/admin/approvals/"+rec.ID+"/approve", tok, nil, &out); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	if out.Approval.State != "approved" || out.Approval.DecidedByName != "kim" || out.Approval.Channel != "console" {
		t.Errorf("approved record = %+v", out.Approval)
	}
	// Conflicting verdict on the resolved record → 409.
	if code := adminReq(t, "POST", base+"/v1/admin/approvals/"+rec.ID+"/deny", tok, nil, nil); code != http.StatusConflict {
		t.Errorf("deny after approve = %d, want 409", code)
	}

	// Unknown id → 404.
	if code := adminReq(t, "POST", base+"/v1/admin/approvals/nope/approve", tok, nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", code)
	}

	// Self-approval → 403 (kim is the requester here).
	self := seedApproval(t, app, user.ID, []string{"dev"})
	if code := adminReq(t, "POST", base+"/v1/admin/approvals/"+self.ID+"/approve", tok, nil, nil); code != http.StatusForbidden {
		t.Errorf("self-approval = %d, want 403", code)
	}

	// Not an approver → 403 (kim holds neither "ghost" nor straza-admin-as-role).
	notMine := seedApproval(t, app, "u-other2", []string{"ghost"})
	if code := adminReq(t, "POST", base+"/v1/admin/approvals/"+notMine.ID+"/deny", tok, nil, nil); code != http.StatusForbidden {
		t.Errorf("non-approver = %d, want 403", code)
	}
}

// TestApprovalDecideWithLoginToken pins that an approver deciding from the
// console/strazactl holds a LOGIN token,
// not an agent session token; requireIdentified must accept it (the service
// still validates approver roles), and a missing bearer still 401s.
func TestApprovalDecideWithLoginToken(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app) // kim, password hunter2!, holds role dev
	loginTok := loginDeviceFlow(t, base, "kim", "hunter2!")

	rec := seedApproval(t, app, "u-other", []string{"dev"})
	var out approvalDecideResp
	if code := adminReq(t, "POST", base+"/v1/admin/approvals/"+rec.ID+"/approve", loginTok, nil, &out); code != http.StatusOK {
		t.Fatalf("approve with a login token = %d, want 200", code)
	}
	if out.Approval.State != "approved" || out.Approval.DecidedByName != "kim" {
		t.Errorf("approved record = %+v", out.Approval)
	}

	rec2 := seedApproval(t, app, "u-other2", []string{"dev"})
	if code := adminReq(t, "POST", base+"/v1/admin/approvals/"+rec2.ID+"/deny", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("decide without a bearer = %d, want 401", code)
	}
}

// TestApprovalDecideTokenAndRepeat pins two answers the API document states:
// an admin API token carries no person and reads 401 with the sentence that
// says so, and repeating the verdict a record already carries answers 200
// with the record while the other verdict stays 409.
func TestApprovalDecideTokenAndRepeat(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app) // kim holds role dev
	grantAdmin(t, app, user.ID)
	tok, _ := checkinTokenAs(t, base, "console")

	var minted struct {
		Token string `json:"token"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/api-tokens", tok, map[string]any{"name": "ci", "scope": "approvals:write"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint = %d", code)
	}
	rec := seedApproval(t, app, "u-other", []string{"dev"})

	for _, tc := range []struct {
		name   string
		bearer string
		verb   string
		want   int
		state  string
		says   string
	}{
		{"an admin API token cannot decide", minted.Token, "approve", http.StatusUnauthorized, "", "an admin API token carries no user and cannot decide"},
		{"the first verdict resolves the record", tok, "approve", http.StatusOK, "approved", ""},
		{"the same verdict again answers the record", tok, "approve", http.StatusOK, "approved", ""},
		{"the other verdict is a conflict", tok, "deny", http.StatusConflict, "", "already resolved with a different verdict"},
	} {
		var out struct {
			Approval struct {
				State string `json:"state"`
			} `json:"approval"`
			Error string `json:"error"`
		}
		code := adminReq(t, "POST", base+"/v1/admin/approvals/"+rec.ID+"/"+tc.verb, tc.bearer, nil, &out)
		if code != tc.want || out.Approval.State != tc.state || !strings.Contains(out.Error, tc.says) {
			t.Errorf("%s: %s = %d state %q error %q, want %d state %q saying %q", tc.name, tc.verb, code, out.Approval.State, out.Error, tc.want, tc.state, tc.says)
		}
	}
}

// TestApprovalsStateFilterAndGet pins the ?state= contract:
// concrete states pass through to the store filter instead of silently
// coercing to pending, an unknown value answers 400 naming the valid set, and
// the new single-record read serves deep links without a re-list.
func TestApprovalsStateFilterAndGet(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app) // kim holds role dev
	grantAdmin(t, app, user.ID)
	tok, _ := checkinTokenAs(t, base, "console")

	pend := seedApproval(t, app, "u-other", []string{"dev"})
	den := seedApproval(t, app, "u-other2", []string{"dev"})
	if code := adminReq(t, "POST", base+"/v1/admin/approvals/"+den.ID+"/deny", tok, nil, nil); code != http.StatusOK {
		t.Fatalf("seed deny = %d", code)
	}

	for _, tc := range []struct {
		state string
		want  []string
	}{
		{"pending", []string{pend.ID}},
		{"", []string{pend.ID}}, // absent keeps the documented pending default
		{"denied", []string{den.ID}},
		{"approved", nil},
		{"expired", nil},
	} {
		var out approvalListResp
		url := base + "/v1/admin/approvals"
		if tc.state != "" {
			url += "?state=" + tc.state
		}
		if code := adminReq(t, "GET", url, tok, nil, &out); code != http.StatusOK {
			t.Fatalf("state=%q list = %d", tc.state, code)
		}
		if len(out.Approvals) != len(tc.want) {
			t.Fatalf("state=%q = %+v, want ids %v", tc.state, out.Approvals, tc.want)
		}
		for i, id := range tc.want {
			if out.Approvals[i].ID != id {
				t.Errorf("state=%q row %d = %s, want %s", tc.state, i, out.Approvals[i].ID, id)
			}
		}
	}
	if code := adminReq(t, "GET", base+"/v1/admin/approvals?state=bogus", tok, nil, nil); code != http.StatusBadRequest {
		t.Errorf("unknown state = %d, want 400", code)
	}

	var one approvalDecideResp
	if code := adminReq(t, "GET", base+"/v1/admin/approvals/"+den.ID, tok, nil, &one); code != http.StatusOK {
		t.Fatalf("single read = %d", code)
	}
	if one.Approval.State != "denied" {
		t.Errorf("single read state = %q, want denied", one.Approval.State)
	}
	if code := adminReq(t, "GET", base+"/v1/admin/approvals/nope", tok, nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown approval = %d, want 404", code)
	}
	// The literal channels route must keep winning over the {id} wildcard.
	if code := adminReq(t, "GET", base+"/v1/admin/approvals/channels", tok, nil, nil); code != http.StatusOK {
		t.Errorf("channels after {id} route = %d, want 200", code)
	}
}

// TestDecideRoutesRefuseCodingHarness pins which sessions may decide a request
// or mint an enroll token for a device that decides: a session checked in by
// a coding harness is refused with a sentence that names the harness and where
// to go, and a person's client passes.
func TestDecideRoutesRefuseCodingHarness(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app) // kim holds role dev
	grantAdmin(t, app, user.ID)

	tests := []struct {
		harness string
		want    int
	}{
		{"claude-code", http.StatusForbidden},
		{"codex", http.StatusForbidden},
		{"exec-wrapper", http.StatusForbidden},
		{"console", http.StatusOK},
		{"strazactl", http.StatusOK},
		{"self-service", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.harness, func(t *testing.T) {
			tok, _ := checkinTokenAs(t, base, tc.harness)
			rec := seedApproval(t, app, "u-other-"+tc.harness, []string{"dev"})

			var out struct {
				Error string `json:"error"`
			}
			if code := adminReq(t, "POST", base+"/v1/admin/approvals/"+rec.ID+"/approve", tok, nil, &out); code != tc.want {
				t.Fatalf("approve as %s = %d, want %d", tc.harness, code, tc.want)
			}
			cur, err := app.approval.Get(context.Background(), rec.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if tc.want == http.StatusOK {
				if cur.State != approval.StateApproved {
					t.Errorf("state = %q, want approved", cur.State)
				}
			} else {
				if cur.State != approval.StatePending {
					t.Errorf("a refused decide left state %q, want pending", cur.State)
				}
				for _, want := range []string{tc.harness, "/self-service/"} {
					if !strings.Contains(out.Error, want) {
						t.Errorf("refusal %q does not name %q", out.Error, want)
					}
				}
			}

			// The mint has a per-user cooldown, so a client that passes the gate
			// answers 200 or 429. Only the gate answers 403 here: kim is an admin.
			code := adminReq(t, "POST", base+"/v1/approvals/self/enroll-token", tok, map[string]string{"channel": "browser"}, nil)
			if refused := code == http.StatusForbidden; refused != (tc.want == http.StatusForbidden) {
				t.Errorf("enroll-token as %s = %d, gate refused = %v", tc.harness, code, refused)
			}
		})
	}
}

// TestOwnRequestRefusedOnTheConsoleRoute pins the sentence a person reads when
// they try to decide their own request from the console or strazactl, and
// that the config switch lifts the refusal.
func TestOwnRequestRefusedOnTheConsoleRoute(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app) // kim holds role dev
	tok, _ := checkinTokenAs(t, base, "console")

	own, err := app.approval.Request(context.Background(), approval.RequestInput{
		SessionID: "s-own", UserID: user.ID, Username: "kim", RuleID: "r-own", SetName: "g",
		ArgvHash: "sha256:own", Lane: "hook", Summary: "shell.exec: deploy-prod",
		Spec:    policy.ApproveSpec{TimeoutSeconds: 90, RetryTTLSeconds: 60},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("seed own request: %v", err)
	}
	var out struct {
		Error string `json:"error"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/approvals/"+own.ID+"/approve", tok, nil, &out); code != http.StatusForbidden {
		t.Fatalf("own request on the console route = %d, want 403", code)
	}
	for _, want := range []string{"your own request", "This browser", "/self-service/", "approval.unsignedOwnDecisions"} {
		if !strings.Contains(out.Error, want) {
			t.Errorf("refusal %q does not name %q", out.Error, want)
		}
	}
}
