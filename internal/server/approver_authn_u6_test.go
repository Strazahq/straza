package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestApproverRefusalsWriteOneLoginFailure pins the straza.audit.authn login
// failures of the signed phone lane, with via approver-token, the user, the
// userId, the reason and the connection's sourceIp. A person retyped as an
// AI agent writes one per refused decision, naming the request. A disabled
// or locked person writes one per suspension however often the phone polls,
// and a new suspension after an accepted request writes again. The request
// the person made first while active writes none.
func TestApproverRefusalsWriteOneLoginFailure(t *testing.T) {
	t.Parallel()
	const nonPerson = "only a person can decide a request"
	setStatus := func(status string) func(*testing.T, *App, store.User) {
		return func(t *testing.T, app *App, u store.User) {
			u.Status = status
			if _, err := app.store.Users().Update(context.Background(), u); err != nil {
				t.Fatal(err)
			}
		}
	}
	lock := func(_ *testing.T, app *App, u store.User) { app.denylist.revokeUser(u.ID) }
	unlock := func(_ *testing.T, app *App, u store.User) { app.denylist.allowUser(u.ID) }
	retype := func(t *testing.T, app *App, u store.User) {
		u.UserType = store.UserTypeAgent
		if _, err := app.store.Users().Update(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name       string
		arm, unarm func(*testing.T, *App, store.User)
		reason     string
	}{
		{"a disabled person", setStatus(store.UserDisabled), setStatus(store.UserActive), "user is disabled"},
		{"a locked person", lock, unlock, "user is locked"},
		{"a person retyped as an AI agent", nil, nil, nonPerson},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, base := testApp(t)
			kim := seedIdentity(t, app)
			priv, _, tok := enrollApprover(t, app, kim.ID)
			rec := seedApproval(t, app, "u-other", []string{"dev"})
			poll := func(want int) {
				t.Helper()
				if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=decidable", tok, nil, nil); code != want {
					t.Fatalf("poll = %d, want %d", code, want)
				}
			}
			ch := decidableChallenge(t, base, tok, rec.ID)
			if tc.arm != nil {
				// Two polls in one suspension, an accepted poll, and one poll
				// in a second suspension.
				tc.arm(t, app, kim)
				poll(http.StatusUnauthorized)
				poll(http.StatusUnauthorized)
				tc.unarm(t, app, kim)
				poll(http.StatusOK)
				tc.arm(t, app, kim)
				poll(http.StatusUnauthorized)
				tc.unarm(t, app, kim)
				ch = decidableChallenge(t, base, tok, rec.ID)
			}
			// The retyped decision ends every row. The chain is written in
			// order, so once its record is there every earlier one is too.
			retype(t, app, kim)
			ts := time.Now().Unix()
			body := decideBodyFor(rec.ID, "approve", ch, signApprover(t, priv, rec.ID, "approve", ch, ts), ts)
			if code := adminReq(t, "POST", base+"/v1/approver/decide", tok, body, nil); code != http.StatusForbidden {
				t.Fatalf("the retyped decision = %d, want 403", code)
			}
			waitAuthn(t, app, nonPerson, func(d map[string]any) bool { return d["reason"] == nonPerson })

			var got []map[string]any
			for _, d := range authnData(t, app) {
				if d["via"] == approverTokenVia && d["reason"] == tc.reason {
					got = append(got, d)
				}
			}
			want := 1
			if tc.arm != nil {
				want = 2
			}
			if len(got) != want {
				t.Fatalf("approver-token records with reason %q = %d, want %d: %v", tc.reason, len(got), want, got)
			}
			fields := map[string]any{"action": "login", "outcome": "failure", "via": approverTokenVia,
				"user": "kim", "userId": kim.ID}
			if tc.arm == nil {
				fields["approvalId"] = rec.ID
			} else if _, ok := got[0]["approvalId"]; ok {
				t.Errorf("the user_inactive record carries approvalId, which requireApprover does not know: %v", got[0])
			}
			for k, v := range fields {
				if got[0][k] != v {
					t.Errorf("record %s = %v, want %v (record %v)", k, got[0][k], v, got[0])
				}
			}
			if ip, _ := got[0]["sourceIp"].(string); ip == "" {
				t.Errorf("record carries no sourceIp, though the phone's connection produced it: %v", got[0])
			}
			for _, k := range []string{"session", "harness"} {
				if _, ok := got[0][k]; ok {
					t.Errorf("record carries %s, which the phone lane does not know: %v", k, got[0])
				}
			}
		})
	}
}

// TestApproverInactiveRecordSurvivesAFailedWrite pins that a user_inactive
// refusal whose record could not be written leaves the approver token
// unmarked: the refused poll logs a Warn line, and the next refused poll of
// the same suspension writes the record, once.
func TestApproverInactiveRecordSurvivesAFailedWrite(t *testing.T) {
	t.Parallel()
	const warn = "approver auth: the user_inactive refusal record could not be written; the next refused request writes it"
	log, buf := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	kim := seedIdentity(t, app)
	priv, _, tok := enrollApprover(t, app, kim.ID)
	rec := seedApproval(t, app, "u-other", []string{"dev"})
	poll := func() {
		t.Helper()
		if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=decidable", tok, nil, nil); code != http.StatusUnauthorized {
			t.Fatalf("poll = %d, want 401", code)
		}
	}
	ch := decidableChallenge(t, base, tok, rec.ID)
	app.denylist.revokeUser(kim.ID)
	fs.arm("outbox:straza.audit.authn", errors.New("outbox insert failed"))
	poll()
	fs.arm("outbox:straza.audit.authn", nil)
	poll()
	poll()
	app.denylist.allowUser(kim.ID)
	if !strings.Contains(buf.String(), warn) {
		t.Errorf("the failed write logged no Warn line %q:\n%s", warn, buf.String())
	}

	// The retyped decision's record is the barrier: the chain is written in
	// order, so once it is there every earlier record is too.
	kim.UserType = store.UserTypeAgent
	if _, err := app.store.Users().Update(context.Background(), kim); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Unix()
	body := decideBodyFor(rec.ID, "approve", ch, signApprover(t, priv, rec.ID, "approve", ch, ts), ts)
	if code := adminReq(t, "POST", base+"/v1/approver/decide", tok, body, nil); code != http.StatusForbidden {
		t.Fatalf("the retyped decision = %d, want 403", code)
	}
	waitAuthn(t, app, "the retyped decision", func(d map[string]any) bool { return d["approvalId"] == rec.ID })
	if n := countAuthn(t, app, func(d map[string]any) bool {
		return d["via"] == approverTokenVia && d["reason"] == "user is locked"
	}); n != 1 {
		t.Errorf("user is locked records = %d, want exactly 1, written by the poll after the failed write", n)
	}
}
