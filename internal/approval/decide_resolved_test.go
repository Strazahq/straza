package approval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestDecideOnAResolvedRecord pins who reads a resolved record's outcome
// through Decide, the call of the console and the signed lane. The person
// who decided it keeps the idempotent answer even after their role is gone,
// and so do a member of its pool, read fresh, and the requester of a
// confirm record: the same verdict answers the record, the other verdict
// ErrConflict, an expired record ErrExpired. Every other caller gets the
// refusal a pending record gives them and no field of the record, and no
// answer changes the record.
func TestDecideOnAResolvedRecord(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	nova := h.seedUser(t, "nova")
	kim := h.seedUser(t, "kim", "sec-approvers")
	pat := h.seedUser(t, "pat", "sec-approvers")
	sam := h.seedUser(t, "sam", "sec-approvers")
	rex := h.seedUser(t, "rex", "sec-approvers")
	ola := h.seedUser(t, "ola")
	lee := h.seedUser(t, "lee")
	max := h.seedUser(t, "max")
	bot := h.seedNHI(t, "bot", "sec-approvers")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	raise := func(argv string, confirm bool) Record {
		t.Helper()
		in := req("s-"+argv, nova.ID, "nova", spec)
		in.ArgvHash, in.Confirm = "sha256:"+argv, confirm
		rec, err := h.svc.Request(ctx, in)
		if err != nil {
			t.Fatalf("raise %s: %v", argv, err)
		}
		return rec
	}
	decided := func(rec Record, verdict string, by store.User, channel, deviceID string) Record {
		t.Helper()
		if _, err := h.svc.Decide(ctx, rec.ID, verdict, by.ID, channel, "in the change window", deviceID); err != nil {
			t.Fatalf("%s decides %s: %v", by.Username, rec.ID, err)
		}
		return rec
	}
	expired := func(rec Record) Record {
		t.Helper()
		if won, err := h.st.Approvals().ClaimExpired(ctx, rec.ID, time.Now().Add(time.Hour)); err != nil || !won {
			t.Fatalf("expire %s = %v, %v", rec.ID, won, err)
		}
		return rec
	}
	approved := decided(raise("approved", false), "approved", kim, "console", "")
	denied := decided(raise("denied", false), "denied", kim, "console", "")
	gone := expired(raise("expired", false))
	confirmed := decided(raise("confirmed", true), "approved", nova, "phone", "dev-1")
	confirmGone := expired(raise("confirm-expired", true))
	bySam := decided(raise("sam", false), "approved", sam, "console", "")
	h.resolver.roles[sam.ID], h.resolver.roles[rex.ID] = nil, nil // both lose the role after the decision
	sponsored, err := h.st.Approvals().Insert(ctx, store.Approval{
		SessionID: "s-sponsored", UserID: nova.ID, Username: "nova", RuleID: "r-1", ArgvHash: "sha256:sponsored",
		ApproverUsers: []string{"lee", "max"}, State: "pending", RetryTTLSeconds: 60,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	byMax := decided(recordFromStore(sponsored), "approved", max, "console", "")

	cases := []struct {
		name    string
		rec     Record
		caller  store.User
		verdict string
		want    error // nil answers the record in the verdict's state
	}{
		{"an outsider repeats an approval", approved, ola, "approved", ErrNotApprover},
		{"an outsider posts the other verdict on an approval", approved, ola, "denied", ErrNotApprover},
		{"an outsider repeats a denial", denied, ola, "denied", ErrNotApprover},
		{"an outsider posts the other verdict on a denial", denied, ola, "approved", ErrNotApprover},
		{"an outsider on an expired record", gone, ola, "approved", ErrNotApprover},
		{"an outsider on a confirmation", confirmed, ola, "approved", ErrNotRequester},
		{"an outsider on an expired confirmation", confirmGone, ola, "approved", ErrNotRequester},
		{"a pool member on someone else's confirmation", confirmed, pat, "approved", ErrNotRequester},
		{"the requester of an approve record", approved, nova, "approved", ErrSelfApproval},
		{"an AI agent that holds the pool's role", approved, bot, "approved", ErrNHIDecider},
		{"a pool member who lost the role and did not decide", bySam, rex, "approved", ErrNotApprover},
		{"an outsider on a sponsor-routed record", byMax, ola, "approved", ErrNotApprover},
		{"the decider taps the same verdict again", approved, kim, "approved", nil},
		{"the decider taps the other verdict", approved, kim, "denied", ErrConflict},
		{"the decider repeats a denial", denied, kim, "denied", nil},
		{"the decider whose role was removed taps again", bySam, sam, "approved", nil},
		{"the decider whose role was removed taps the other verdict", bySam, sam, "denied", ErrConflict},
		{"a pool member who did not decide repeats the verdict", approved, pat, "approved", nil},
		{"a pool member who did not decide posts the other verdict", denied, pat, "approved", ErrConflict},
		{"a pool member on an expired record", gone, pat, "denied", ErrExpired},
		{"a named decider who did not decide repeats the verdict", byMax, lee, "approved", nil},
		{"the requester repeats a confirmation", confirmed, nova, "approved", nil},
		{"the requester posts the other verdict on a confirmation", confirmed, nova, "denied", ErrConflict},
		{"the requester on an expired confirmation", confirmGone, nova, "approved", ErrExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := h.svc.Get(ctx, tc.rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := h.svc.Decide(ctx, tc.rec.ID, tc.verdict, tc.caller.ID, "console", "", "")
			switch {
			case tc.want != nil && (!errors.Is(err, tc.want) || got.ID != ""):
				t.Errorf("%s posts %s = %+v, %v, want %v and no record", tc.caller.Username, tc.verdict, got, err, tc.want)
			case tc.want == nil && (err != nil || got.ID != tc.rec.ID || string(got.State) != tc.verdict):
				t.Errorf("%s posts %s = %+v, %v, want the record %s", tc.caller.Username, tc.verdict, got, err, tc.verdict)
			}
			after, err := h.svc.Get(ctx, tc.rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.State != before.State || after.DecidedBy != before.DecidedBy || !after.DecidedAt.Equal(*before.DecidedAt) {
				t.Errorf("the answer changed the record from %s by %q to %s by %q", before.State, before.DecidedBy, after.State, after.DecidedBy)
			}
		})
	}
}

// TestSlackTapOnAResolvedCard pins the Slack lane on a record decided in the
// console: a person outside the pool reads the refusal a pending card gives
// them, never who decided or when, and a pool member reads what was decided.
func TestSlackTapOnAResolvedCard(t *testing.T) {
	cases := []struct {
		name, tapper, action, verdict string
		says                          func(decidedAt string) string
	}{
		{"an outsider repeats the verdict", "ola", "approve", "approved", func(string) string {
			return "Could not record decision: you are not an authorized approver. Your Slack email matched the Straza user ola."
		}},
		{"an outsider posts the other verdict", "ola", "deny", "denied", func(string) string {
			return "Could not record decision: you are not an authorized approver. Your Slack email matched the Straza user ola."
		}},
		{"a pool member repeats the verdict", "pat", "approve", "approved", func(at string) string {
			return "This request was already approved by kim in the console at " + at +
				" UTC, so this tap recorded nothing new. Your Slack email matched the Straza user pat."
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			requester := h.seedUser(t, "nova")
			kim := h.seedUser(t, "kim", "sec-approvers")
			h.seedUser(t, "pat", "sec-approvers")
			h.seedUser(t, "ola")
			fake := newSlackFake(t, tc.tapper+"@x.io", true)
			attachSlack(t, h, fake.srv.URL, tc.tapper+"@x.io", true)
			spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
			rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
			if err != nil {
				t.Fatal(err)
			}
			done, err := h.svc.Decide(ctx, rec.ID, "approved", kim.ID, "console", "", "")
			if err != nil || done.DecidedAt == nil {
				t.Fatalf("kim decides = %+v, %v", done, err)
			}

			resp := slackCallback(t, h, rec.ID, tc.action, tc.verdict, rec.ExpiresAt, "U-TAP")
			want := tc.says(done.DecidedAt.UTC().Format("2006-01-02 15:04:05"))
			if got := slackTapText(t, resp.Code, resp.Body.Bytes()); got != want {
				t.Errorf("the tap reads %q, want %q", got, want)
			}
		})
	}
}
