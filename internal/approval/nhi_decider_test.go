package approval

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// TestIsNHIFailsClosed pins that isNHI treats a user it cannot read as no
// person and says why: a missing row and a store outage both read as
// non-human with ErrStoreUnavailable, so Decide refuses the decider with
// the outage and records nothing, never with the sentence for a non-human
// identity, and an empty id is no user at all.
func TestIsNHIFailsClosed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	person := h.seedUser(t, "ana", "sec-approvers")
	agent := h.seedNHI(t, "robot", "sec-approvers")
	cases := []struct {
		name        string
		userID      string
		userErr     error
		want, fault bool
	}{
		{"a readable person", person.ID, nil, false, false},
		{"an agent", agent.ID, nil, true, false},
		{"a person whose row cannot be read", person.ID, pgDown, true, true},
		{"a missing user", "no-such-user", nil, true, true},
		{"an empty id", "", nil, false, false},
	}
	for _, tc := range cases {
		h.svc.st = &flakyStore{Store: h.st, userErr: tc.userErr}
		got, err := h.svc.isNHI(ctx, tc.userID)
		if got != tc.want || errors.Is(err, ErrStoreUnavailable) != tc.fault {
			t.Errorf("%s: isNHI = %v, %v, want %v and an outage %v", tc.name, got, err, tc.want, tc.fault)
		}
	}
	h.svc.st = h.st

	requester := h.seedUser(t, "nova")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatal(err)
	}
	h.svc.st = &flakyStore{Store: h.st, userErr: pgDown}
	_, err = h.svc.Decide(ctx, rec.ID, "approved", person.ID, "console", "", "")
	h.svc.st = h.st
	if !errors.Is(err, ErrStoreUnavailable) || errors.Is(err, ErrNHIDecider) {
		t.Errorf("Decide with the users read down = %v, want ErrStoreUnavailable", err)
	}
	if got, err := h.st.Approvals().GetByID(ctx, rec.ID); err != nil || got.State != string(StatePending) {
		t.Errorf("the record reads %q (%v), want it still pending", got.State, err)
	}
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", person.ID, "console", "", ""); err != nil {
		t.Errorf("Decide once the store answers = %v, want the person's approval", err)
	}
}

// TestSlackRefusesANonPersonInWords pins that a Slack tap whose email matches
// only an AI agent maps to no Straza user and records nothing, that the words
// for a decider who is no longer a person stand, and that a users read
// outage during a Slack decision reads as the outage.
func TestSlackRefusesANonPersonInWords(t *testing.T) {
	h := newHarness(t)
	requester := h.seedUser(t, "nova")
	h.seedNHI(t, "robot", "sec-approvers")
	fake := newSlackFake(t, "robot@x.io", true)
	attachSlack(t, h, fake.srv.URL, "robot@x.io", true)
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, err := h.svc.Request(context.Background(), req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatal(err)
	}
	resp := slackCallback(t, h, rec.ID, "approve", "approved", rec.ExpiresAt, "U-ROBOT")
	const want = "Your Slack identity is not linked to a Straza user, so this decision was not recorded."
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), want) {
		t.Errorf("the agent's tap = %d %s, want the sentence %q", resp.Code, resp.Body.String(), want)
	}
	if got, _ := h.svc.Get(context.Background(), rec.ID); got.State != StatePending {
		t.Errorf("the agent's tap left the record %s, want it pending", got.State)
	}
	if got := decideErrorText(ErrNHIDecider); got != "your Straza user is an AI agent or a service account, and only a person can decide a request. Ask a person who may approve it to decide it" {
		t.Errorf("decideErrorText of a non-person decider = %q", got)
	}
	if got := decideErrorText(ErrStoreUnavailable); got != "Straza is temporarily unavailable. Try again in a moment" {
		t.Errorf("decideErrorText of an outage = %q", got)
	}
}
