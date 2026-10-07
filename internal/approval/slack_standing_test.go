package approval

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// slackLoginFailures answers the data of every straza.audit.authn login
// failure in the outbox.
func slackLoginFailures(t *testing.T, st store.Store) []map[string]any {
	t.Helper()
	evs, err := st.Outbox().ListUnpublished(context.Background(), 1000)
	if err != nil {
		t.Fatalf("Outbox: %v", err)
	}
	var out []map[string]any
	for _, e := range evs {
		if e.Subject != "straza.audit.authn" {
			continue
		}
		var ce struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(e.CE), &ce); err != nil {
			t.Fatalf("authn CE does not parse: %v", err)
		}
		if ce.Type == "straza.audit.authn" && ce.Data["action"] == "login" && ce.Data["outcome"] == "failure" {
			out = append(out, ce.Data)
		}
	}
	return out
}

// TestSlackRefusesADisabledOrLockedPerson pins that a Slack tap by a person
// whose Straza user is disabled or locked is refused before any decision, in
// words that name the Straza user the Slack email matched, leaves the record
// pending, and writes exactly one
// straza.audit.authn login failure that names the person, the request and
// the reason. An active person's tap is the positive control: it is
// recorded and writes no login failure.
func TestSlackRefusesADisabledOrLockedPerson(t *testing.T) {
	const (
		disabledWords = "Could not record decision: your Slack email matches the Straza user ana, which is disabled, so it cannot decide a request. " +
			"Ask another person who may approve the request to decide it. If you should still decide requests, tell your Straza administrator that your Slack email matches the disabled user ana"
		lockedWords = "Could not record decision: your Slack email matches the Straza user ana, which is locked, so it cannot decide a request. " +
			"Ask another person who may approve the request to decide it. If you should still decide requests, tell your Straza administrator that your Slack email matches the locked user ana"
	)
	disable := func(t *testing.T, h *harness, u store.User) {
		u.Status = store.UserDisabled
		if _, err := h.st.Users().Update(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	lock := func(_ *testing.T, h *harness, u store.User) {
		h.svc.UserBlocked = func(id string) bool { return id == u.ID }
	}
	cases := []struct {
		name         string
		arm          []func(*testing.T, *harness, store.User)
		says, reason string
	}{
		{"an active person", nil, "Recorded: approved. Your Slack email matched the Straza user ana.", ""},
		{"a person disabled in the store", []func(*testing.T, *harness, store.User){disable}, disabledWords, "user is disabled"},
		{"a person disabled and on the denylist, as a disable leaves them", []func(*testing.T, *harness, store.User){disable, lock}, disabledWords, "user is disabled"},
		{"a person locked on the denylist with the status active", []func(*testing.T, *harness, store.User){lock}, lockedWords, "user is locked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			requester := h.seedUser(t, "nova")
			ana := h.seedUser(t, "ana", "sec-approvers")
			fake := newSlackFake(t, "ana@x.io", true)
			attachSlack(t, h, fake.srv.URL, "ana@x.io", true)
			spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
			rec, err := h.svc.Request(context.Background(), req("s-1", requester.ID, "nova", spec))
			if err != nil {
				t.Fatal(err)
			}
			for _, arm := range tc.arm {
				arm(t, h, ana)
			}

			resp := slackCallback(t, h, rec.ID, "approve", "approved", rec.ExpiresAt, "U-ANA")
			var body struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(resp.Body.Bytes(), &body)
			if resp.Code != http.StatusOK || body.Text != tc.says {
				t.Errorf("the tap = %d %q, want 200 %q", resp.Code, body.Text, tc.says)
			}
			got, err := h.svc.Get(context.Background(), rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			fails := slackLoginFailures(t, h.st)
			if tc.reason == "" {
				if got.State != StateApproved || got.DecidedBy != ana.ID {
					t.Errorf("the active person's tap left the record %s by %q, want approved by ana", got.State, got.DecidedBy)
				}
				if len(fails) != 0 {
					t.Errorf("the active person's tap wrote login failures: %v", fails)
				}
				return
			}
			if got.State != StatePending {
				t.Errorf("the refused tap left the record %s, want it pending", got.State)
			}
			if len(fails) != 1 {
				t.Fatalf("the refused tap wrote %d login failures, want exactly 1: %v", len(fails), fails)
			}
			want := map[string]any{
				"action": "login", "outcome": "failure", "via": "slack",
				"user": "ana", "userId": ana.ID, "approvalId": rec.ID, "reason": tc.reason,
			}
			for k, v := range want {
				if fails[0][k] != v {
					t.Errorf("record %s = %v, want %v (record %v)", k, fails[0][k], v, fails[0])
				}
			}
			for _, k := range []string{"sourceIp", "userAgent", "session", "harness"} {
				if _, ok := fails[0][k]; ok {
					t.Errorf("record carries %s, which a Slack tap does not know: %v", k, fails[0])
				}
			}
			evs, _ := h.st.Outbox().ListUnpublished(context.Background(), 1000)
			for _, e := range evs {
				if e.Subject == auditSubject && strings.Contains(e.CE, `"phase":"resolution"`) {
					t.Errorf("the refused tap wrote a resolution record: %s", e.CE)
				}
			}
		})
	}
}
