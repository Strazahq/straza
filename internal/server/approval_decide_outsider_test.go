package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestDecideRouteHidesResolvedRecordsFromOutsiders pins the decide routes on
// a resolved record. A signed-in person who could not decide the record while
// it was pending reads the refusal a pending record gives them, with no
// field of the record, for either verdict. The person who decided it, another
// member of its approver pool and the requester of a confirm record keep the
// idempotent answer: the same verdict again answers 200 with the record, the
// other verdict 409, an expired record 410. A user who is not a person is
// refused before the route reads anything. No answer writes an approval
// record.
func TestDecideRouteHidesResolvedRecordsFromOutsiders(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app) // kim holds role dev, the record's approver role
	kimTok := loginDeviceFlow(t, base, "kim", "hunter2!")
	pat, patTok := outsiderTestPerson(t, app, base, "pat", "dev")
	ola, olaTok := outsiderTestPerson(t, app, base, "ola")
	nova, novaTok := outsiderTestPerson(t, app, base, "nova")
	svc, err := app.store.Users().Create(ctx, store.User{Username: "svc", UserType: store.UserTypeService})
	if err != nil {
		t.Fatal(err)
	}
	grantRole(t, app, svc.ID, "dev")
	svcTok, err := app.tokens.MintIDToken(svc.ID, "straza", time.Minute, "svc", "")
	if err != nil {
		t.Fatal(err)
	}

	approved := outsiderTestRecord(t, app, nova, "approved", false)
	if code, _, out := callJSON(t, http.MethodPost, base+"/v1/admin/approvals/"+approved.ID+"/approve", kimTok, map[string]string{"reason": "the change window is open"}); code != http.StatusOK {
		t.Fatalf("kim's approval = %d %v", code, out)
	}
	denied := outsiderTestRecord(t, app, nova, "denied", false)
	if code, _, out := callJSON(t, http.MethodPost, base+"/v1/admin/approvals/"+denied.ID+"/deny", kimTok, map[string]string{"reason": "not during the freeze"}); code != http.StatusOK {
		t.Fatalf("kim's denial = %d %v", code, out)
	}
	expired := outsiderTestRecord(t, app, nova, "expired", false)
	if won, err := app.store.Approvals().ClaimExpired(ctx, expired.ID, time.Now().Add(time.Hour)); err != nil || !won {
		t.Fatalf("expire = %v, %v", won, err)
	}
	confirmed := outsiderTestRecord(t, app, nova, "confirmed", true)
	if _, err := app.approval.Decide(ctx, confirmed.ID, "approved", nova.ID, "phone", "", "dev-1"); err != nil {
		t.Fatalf("nova's signed confirmation: %v", err)
	}

	const (
		notApprover  = "approval: decider is not an authorized approver"
		notRequester = "approval: only the requester may confirm this request"
		selfApproval = "approval: requester may not approve their own request"
		conflict     = "approval: already resolved with a different verdict"
		gone         = "approval: request has expired"
	)
	nonPerson := "the user svc is a service account, and only a person can use the admin API or decide a request. " +
		"For automation, a person mints an admin API token with strazactl api-token create, " +
		"and an AI agent or a service account proposes config changes through the built-in straza MCP server's drafting tools, " +
		"which the role straza-draft-config lists."
	cases := []struct {
		name   string
		caller string
		bearer string
		rec    approval.Record
		verb   string
		want   int
		state  string // the record's state in a 200 answer
		says   string // the whole error body otherwise
	}{
		{"an outsider repeats an approval", ola.Username, olaTok, approved, "approve", http.StatusForbidden, "", notApprover},
		{"an outsider posts the other verdict on an approval", ola.Username, olaTok, approved, "deny", http.StatusForbidden, "", notApprover},
		{"an outsider repeats a denial", ola.Username, olaTok, denied, "deny", http.StatusForbidden, "", notApprover},
		{"an outsider posts the other verdict on a denial", ola.Username, olaTok, denied, "approve", http.StatusForbidden, "", notApprover},
		{"an outsider on an expired record", ola.Username, olaTok, expired, "approve", http.StatusForbidden, "", notApprover},
		{"an outsider on a confirmation", ola.Username, olaTok, confirmed, "approve", http.StatusForbidden, "", notRequester},
		{"a pool member on someone else's confirmation", pat.Username, patTok, confirmed, "approve", http.StatusForbidden, "", notRequester},
		{"the requester of an approve record", nova.Username, novaTok, approved, "approve", http.StatusForbidden, "", selfApproval},
		{"the decider taps the same verdict again", kim.Username, kimTok, approved, "approve", http.StatusOK, "approved", ""},
		{"the decider taps the other verdict", kim.Username, kimTok, approved, "deny", http.StatusConflict, "", conflict},
		{"the decider repeats a denial", kim.Username, kimTok, denied, "deny", http.StatusOK, "denied", ""},
		{"the decider on an expired record", kim.Username, kimTok, expired, "deny", http.StatusGone, "", gone},
		{"a pool member who did not decide repeats the verdict", pat.Username, patTok, approved, "approve", http.StatusOK, "approved", ""},
		{"a pool member who did not decide posts the other verdict", pat.Username, patTok, denied, "approve", http.StatusConflict, "", conflict},
		{"a pool member on an expired record", pat.Username, patTok, expired, "approve", http.StatusGone, "", gone},
		{"the requester repeats a confirmation", nova.Username, novaTok, confirmed, "approve", http.StatusOK, "approved", ""},
		{"the requester posts the other verdict on a confirmation", nova.Username, novaTok, confirmed, "deny", http.StatusConflict, "", conflict},
		{"a service account in the pool", svc.Username, svcTok, approved, "approve", http.StatusForbidden, "", nonPerson},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := outsiderTestRecords(t, app, tc.rec.ID)
			code, _, out := callJSON(t, http.MethodPost, base+"/v1/admin/approvals/"+tc.rec.ID+"/"+tc.verb, tc.bearer, nil)
			if tc.state != "" {
				got, _ := out["approval"].(map[string]any)
				if code != tc.want || got["state"] != tc.state {
					t.Errorf("%s posts %s = %d %v, want %d with the record %s", tc.caller, tc.verb, code, out, tc.want, tc.state)
				}
			} else if msg, _ := out["error"].(string); code != tc.want || msg != tc.says || len(out) != 1 {
				t.Errorf("%s posts %s = %d %v, want %d with %q alone", tc.caller, tc.verb, code, out, tc.want, tc.says)
			}
			if after := outsiderTestRecords(t, app, tc.rec.ID); after != before {
				t.Errorf("the answer wrote %d approval records, want none", after-before)
			}
			if rec, err := app.approval.Get(ctx, tc.rec.ID); err != nil || rec.DecidedAt == nil {
				t.Errorf("the record reads %+v, %v, want it resolved as the setup left it", rec, err)
			}
		})
	}
}

// outsiderTestPerson creates a person with the password hunter2! and the
// given roles, and answers the user and a login token.
func outsiderTestPerson(t *testing.T, app *App, base, name string, roles ...string) (store.User, string) {
	t.Helper()
	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	u, err := app.store.Users().Create(context.Background(), store.User{Username: name, PasswordHash: hash, UserType: store.UserTypeHuman})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		grantRole(t, app, u.ID, r)
	}
	return u, loginDeviceFlow(t, base, name, "hunter2!")
}

// outsiderTestRecord raises a pending record of requester whose approver
// pool is the role dev, a confirm record when confirm is set.
func outsiderTestRecord(t *testing.T, app *App, requester store.User, argv string, confirm bool) approval.Record {
	t.Helper()
	rec, err := app.approval.Request(context.Background(), approval.RequestInput{
		SessionID: "s-" + argv, UserID: requester.ID, Username: requester.Username, RuleID: "r-1", SetName: "g",
		ArgvHash: "sha256:" + argv, Lane: "hook", Summary: "shell.exec: deploy-prod",
		Justification: "ship the hotfix", ArgsPreview: `{"cmd":"deploy-prod --force"}`, ArgsBytes: 29,
		Spec:    policy.ApproveSpec{Roles: []string{"dev"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
		Confirm: confirm,
	})
	if err != nil {
		t.Fatalf("raise %s: %v", argv, err)
	}
	return rec
}

// outsiderTestRecords counts the approval audit records of one approval.
func outsiderTestRecords(t *testing.T, app *App, id string) int {
	t.Helper()
	n := 0
	for _, d := range outboxDataFor(t, app, "straza.audit.approval") {
		if d["approvalId"] == id {
			n++
		}
	}
	return n
}
