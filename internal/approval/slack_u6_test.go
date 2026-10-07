package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// lockedLog is a log destination the tap and the notifier goroutines share.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// slackTapText answers the ephemeral text of a Slack callback answer.
func slackTapText(t *testing.T, code int, raw []byte) string {
	t.Helper()
	var body struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || code != http.StatusOK {
		t.Fatalf("the tap = %d %s, want 200 with an ephemeral text", code, raw)
	}
	return body.Text
}

// TestSlackMapsATapToTheActivePerson pins the precedence mapUser applies
// among the undeleted users that share the tapping profile's email, created
// in the order the rows list: the active person first, then a disabled or
// locked person, whom the standing refusal names, and never an AI agent. A
// match on AI agents only is unmappable and logs a Warn line naming them.
// Every reply after a match names the matched Straza user.
func TestSlackMapsATapToTheActivePerson(t *testing.T) {
	const (
		matched   = " Your Slack email matched the Straza user "
		notLinked = "Your Slack identity is not linked to a Straza user, so this decision was not recorded."
		oldWords  = "Could not record decision: your Slack email matches the Straza user ana-old, which is disabled, so it cannot decide a request. " +
			"Ask another person who may approve the request to decide it. If you should still decide requests, tell your Straza administrator that your Slack email matches the disabled user ana-old"
		lockedWords = "Could not record decision: your Slack email matches the Straza user ana, which is locked, so it cannot decide a request. " +
			"Ask another person who may approve the request to decide it. If you should still decide requests, tell your Straza administrator that your Slack email matches the locked user ana"
	)
	disabled := store.User{Username: "ana-old", Status: store.UserDisabled}
	agent := store.User{Username: "ana-agent", UserType: store.UserTypeAgent, Sponsor: "ana"}
	ana := store.User{Username: "ana"}
	cases := []struct {
		name    string
		rows    []store.User
		locked  string // the username on the lock denylist, "" for none
		plain   string // the username that holds no approver role, "" for none
		says    string
		decider string // the username that decided, "" when the record stays pending
		warn    string // a Warn line's words, "" for none
	}{
		{"an older disabled row and the active person", []store.User{disabled, ana}, "", "",
			"Recorded: approved." + matched + "ana.", "ana", ""},
		{"an older AI agent row with its sponsor's email and the active person", []store.User{agent, ana}, "", "",
			"Recorded: approved." + matched + "ana.", "ana", ""},
		{"a locked person and a newer active person", []store.User{ana, {Username: "ana-new"}}, "ana", "",
			"Recorded: approved." + matched + "ana-new.", "ana-new", ""},
		{"two active people, the older one no approver", []store.User{{Username: "ana-1"}, ana}, "", "ana-1",
			"Could not record decision: you are not an authorized approver." + matched + "ana-1.", "", ""},
		{"an AI agent and a disabled person with no active person", []store.User{agent, disabled}, "", "", oldWords, "", ""},
		{"an AI agent and a locked person with no active person", []store.User{agent, ana}, "ana", "", lockedWords, "", ""},
		{"an AI agent only", []store.User{agent}, "", "", notLinked, "", "ana-agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			requester := h.seedUser(t, "nova")
			ids := map[string]string{}
			for _, u := range tc.rows {
				u.Email = "ana@x.io"
				got, err := h.st.Users().Create(context.Background(), u)
				if err != nil {
					t.Fatal(err)
				}
				ids[got.ID] = got.Username
				if got.Username != tc.plain {
					h.resolver.roles[got.ID] = []store.Role{{ID: "role-sec-approvers", Name: "sec-approvers"}}
				}
				if got.Username == tc.locked {
					h.svc.UserBlocked = func(id string) bool { return id == got.ID }
				}
			}
			fake := newSlackFake(t, "ana@x.io", true)
			sc := attachSlack(t, h, fake.srv.URL, "ana@x.io", true)
			var logs lockedLog
			sc.log = slog.New(slog.NewTextHandler(&logs, nil))
			spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
			rec, err := h.svc.Request(context.Background(), req("s-1", requester.ID, "nova", spec))
			if err != nil {
				t.Fatal(err)
			}

			resp := slackCallback(t, h, rec.ID, "approve", "approved", rec.ExpiresAt, "U-ANA")
			if got := slackTapText(t, resp.Code, resp.Body.Bytes()); got != tc.says {
				t.Errorf("the tap reads %q, want %q", got, tc.says)
			}
			got, err := h.svc.Get(context.Background(), rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.decider == "" && got.State != StatePending:
				t.Errorf("the record is %s by %s, want it pending", got.State, ids[got.DecidedBy])
			case tc.decider != "" && (got.State != StateApproved || ids[got.DecidedBy] != tc.decider):
				t.Errorf("the record is %s by %q, want approved by %s", got.State, ids[got.DecidedBy], tc.decider)
			}
			if tc.warn != "" && !strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("no Warn line, want one naming %s:\n%s", tc.warn, logs.String())
			}
			if tc.warn != "" && !strings.Contains(logs.String(), tc.warn) {
				t.Errorf("the log does not name %s:\n%s", tc.warn, logs.String())
			}
		})
	}
}

// TestSlackTellsARepeatedTapItDecidedNothing pins that the tap that decides
// a request reads Recorded, that a second tap with the same verdict reads
// that the request was already decided, with the verdict, the decider and
// the time, and that a tap with the other verdict keeps its refusal. Each
// reply names the Straza user the Slack email matched.
func TestSlackTellsARepeatedTapItDecidedNothing(t *testing.T) {
	h := newHarness(t)
	requester := h.seedUser(t, "nova")
	h.seedUser(t, "ana", "sec-approvers")
	fake := newSlackFake(t, "ana@x.io", true)
	attachSlack(t, h, fake.srv.URL, "ana@x.io", true)
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, err := h.svc.Request(context.Background(), req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatal(err)
	}

	first := slackCallback(t, h, rec.ID, "approve", "approved", rec.ExpiresAt, "U-ANA")
	const matched = " Your Slack email matched the Straza user ana."
	if got := slackTapText(t, first.Code, first.Body.Bytes()); got != "Recorded: approved."+matched {
		t.Fatalf("the first tap reads %q, want Recorded", got)
	}
	decided, err := h.svc.Get(context.Background(), rec.ID)
	if err != nil || decided.DecidedAt == nil {
		t.Fatalf("the record after the first tap = %+v, %v", decided, err)
	}
	when := decided.DecidedAt.UTC().Format("2006-01-02 15:04:05")
	cases := []struct {
		name, action, verdict, says string
	}{
		{"the same verdict again", "approve", "approved",
			"This request was already approved by ana in Slack at " + when + " UTC, so this tap recorded nothing new." + matched},
		{"the other verdict", "deny", "denied", "Could not record decision: already decided the other way." + matched},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := slackCallback(t, h, rec.ID, tc.action, tc.verdict, rec.ExpiresAt, "U-ANA")
			if got := slackTapText(t, resp.Code, resp.Body.Bytes()); got != tc.says {
				t.Errorf("the tap reads %q, want %q", got, tc.says)
			}
			after, err := h.svc.Get(context.Background(), rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.State != StateApproved || !after.DecidedAt.Equal(*decided.DecidedAt) {
				t.Errorf("the tap changed the record to %s at %v", after.State, after.DecidedAt)
			}
		})
	}
}
