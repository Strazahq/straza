package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// GetByUsername fails while "users-by-name" is armed, apart from the reads
// by id that "users" fails.
func (u faultUsers) GetByUsername(ctx context.Context, username string) (store.User, error) {
	if err := u.f.fault("users-by-name"); err != nil {
		return store.User{}, err
	}
	return u.UserRepo.GetByUsername(ctx, username)
}

// TestDeciderReadFaultFailsClosed pins the hook lane's answer when the
// approval service reads who decides a bare approve. A store fault on the
// read of the requester or the sponsor fails closed with the sentence that
// says the record could not be read, one fail-closed Error record and one
// count in straza_failclosed_total, and moves no unroutable count. A
// requester or sponsor that is not in the store stays unroutable: the
// routing cause, one unroutable count and no Error record.
func TestDeciderReadFaultFailsClosed(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, _, fs := testAppFaultLog(t, log)
	ctx := context.Background()
	if _, err := app.store.Users().Create(ctx, store.User{Username: "grace", Email: "grace@x.io"}); err != nil {
		t.Fatal(err)
	}
	agent := func(t *testing.T, name, sponsor string) store.User {
		t.Helper()
		u, err := app.store.Users().Create(ctx, store.User{Username: name, Email: name + "@x.io",
			UserType: store.UserTypeAgent, Sponsor: sponsor})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	cases := []struct {
		name       string
		requester  func(t *testing.T) store.User
		arm        string // the faultStore key armed around the call, empty for none
		wantReason []string
		failClosed float64 // the count straza_failclosed_total{lane=hook} gains
		unroutable float64 // the count straza_approvals_unroutable_total gains
		wantCause  string  // the cause of the one fail-closed Error record
	}{
		{
			name:       "a store fault reading the requester",
			requester:  func(t *testing.T) store.User { return agent(t, "nova-1", "grace") },
			arm:        "users",
			wantReason: []string{approvalDeciderUnreadReason},
			failClosed: 1,
			wantCause:  `approval deciders: a user record that says who decides could not be read: requester \"nova-1\": FATAL`,
		},
		{
			name:       "a store fault reading the sponsor",
			requester:  func(t *testing.T) store.User { return agent(t, "nova-2", "grace") },
			arm:        "users-by-name",
			wantReason: []string{approvalDeciderUnreadReason},
			failClosed: 1,
			wantCause:  `approval deciders: a user record that says who decides could not be read: sponsor \"grace\": FATAL`,
		},
		{
			name:       "a requester that is not in the store",
			requester:  func(*testing.T) store.User { return store.User{ID: "u-gone", Username: "gone"} },
			wantReason: []string{"Straza: approval cannot be routed: ", `requester "gone" could not be read`, "Denied (fail-closed)"},
			unroutable: 1,
		},
		{
			name:       "a sponsor that is not in the store",
			requester:  func(t *testing.T) store.User { return agent(t, "nova-4", "ghost") },
			wantReason: []string{"Straza: approval cannot be routed: ", `sponsor "ghost" is not known to Straza`, "Denied (fail-closed)"},
			unroutable: 1,
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := tc.requester(t)
			rule := fmt.Sprintf("r-read-%d", i)
			hookBefore := failClosedCounter(t, app, "hook")
			unBefore := counterValue(t, app, "straza_approvals_unroutable_total")
			buf.Reset()
			reqCtx := ctxWithCapture(ctx, log, "read-corr-"+rule)
			claims := authn.Claims{Session: "sess-" + rule, Subject: u.ID}
			ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod " + rule}
			if tc.arm != "" {
				fs.arm(tc.arm, pgDown)
			}
			dec, id := app.resolveApproveHook(reqCtx, claims, policy.Subject{User: u.Username}, ev, bareApproveDecision(rule))
			fs.disarm()
			if dec.Effect != policy.EffectDeny || id != "" {
				t.Fatalf("resolve = %+v id=%q, want a deny with no record", dec, id)
			}
			for _, want := range tc.wantReason {
				if !strings.Contains(dec.Reason, want) {
					t.Errorf("reason %q lacks %q", dec.Reason, want)
				}
			}
			if got := failClosedCounter(t, app, "hook") - hookBefore; got != tc.failClosed {
				t.Errorf("straza_failclosed_total{lane=hook} rose by %v, want %v", got, tc.failClosed)
			}
			if got := counterValue(t, app, "straza_approvals_unroutable_total") - unBefore; got != tc.unroutable {
				t.Errorf("straza_approvals_unroutable_total rose by %v, want %v", got, tc.unroutable)
			}
			// Records of other users' background reads are left out: only
			// the records of this call's rule count.
			var recs []string
			for _, rec := range errorRecords(buf) {
				if strings.Contains(rec, "rule="+rule) {
					recs = append(recs, rec)
				}
			}
			switch {
			case tc.wantCause == "" && len(recs) != 0:
				t.Errorf("Error records = %q, want none for a routing deny", recs)
			case tc.wantCause != "" && (len(recs) != 1 || !strings.Contains(recs[0], tc.wantCause) ||
				!strings.Contains(recs[0], "correlation_id=read-corr-"+rule) || !strings.Contains(recs[0], "lane=hook")):
				t.Errorf("Error records = %q, want one fail-closed record with %s", recs, tc.wantCause)
			}
		})
	}
}
