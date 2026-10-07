package approval

import (
	"context"
	"errors"
	"testing"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestSponsorDeciderFallsOnThePersonBehindTheAgent pins who a sponsor-routed
// rule lands on. An agent's call goes to its sponsor. A person who runs their
// own agent and has no sponsor gets a confirm record, which they alone decide.
// A person who has a sponsor keeps that sponsor as the decider. A rule that
// also names an approver role never collapses into self-confirmation.
func TestSponsorDeciderFallsOnThePersonBehindTheAgent(t *testing.T) {
	bare := policy.ApproveSpec{TimeoutSeconds: 90, RetryTTLSeconds: 60}
	explicit := policy.ApproveSpec{Deciders: []string{policy.DeciderSponsor}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	withRole := policy.ApproveSpec{Deciders: []string{policy.DeciderSponsor}, Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	ticket := policy.ApproveSpec{Class: policy.ClassTicket}

	tests := []struct {
		name       string
		requester  store.User
		spec       policy.ApproveSpec
		wantMode   string
		wantUsers  []string
		unroutable bool
	}{
		{name: "person, no sponsor, bare approve", requester: store.User{Username: "pat"}, spec: bare, wantMode: policy.ModeConfirm},
		{name: "person, no sponsor, sponsor decider", requester: store.User{Username: "pat"}, spec: explicit, wantMode: policy.ModeConfirm},
		{name: "person, no sponsor, bare ticket", requester: store.User{Username: "pat"}, spec: ticket, wantMode: policy.ModeConfirm},
		{name: "person, no sponsor, sponsor decider beside a role", requester: store.User{Username: "pat"}, spec: withRole, wantMode: policy.ModeApprove},
		{name: "person with a sponsor", requester: store.User{Username: "pat", Sponsor: "grace"}, spec: bare, wantMode: policy.ModeApprove, wantUsers: []string{"grace"}},
		{name: "person whose sponsor is unknown", requester: store.User{Username: "pat", Sponsor: "ghost"}, spec: bare, unroutable: true},
		{name: "agent, no sponsor", requester: store.User{Username: "bot", UserType: store.UserTypeAgent}, spec: bare, unroutable: true},
		{name: "agent with a sponsor", requester: store.User{Username: "bot", UserType: store.UserTypeAgent, Sponsor: "grace"}, spec: bare, wantMode: policy.ModeApprove, wantUsers: []string{"grace"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			h.seedUser(t, "grace")
			tc.requester.Email = tc.requester.Username + "@x.io"
			u, err := h.st.Users().Create(ctx, tc.requester)
			if err != nil {
				t.Fatalf("Create requester: %v", err)
			}

			rec, err := h.svc.Request(ctx, req("s-1", u.ID, u.Username, tc.spec))
			if tc.unroutable {
				var un *UnroutableError
				if !errors.As(err, &un) {
					t.Fatalf("Request err = %v, want *UnroutableError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			if rec.Mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", rec.Mode, tc.wantMode)
			}
			if len(rec.ApproverUsers) != len(tc.wantUsers) || (len(tc.wantUsers) == 1 && rec.ApproverUsers[0] != tc.wantUsers[0]) {
				t.Errorf("approver users = %v, want %v", rec.ApproverUsers, tc.wantUsers)
			}
		})
	}
}

// TestPersonBehindTheAgentDecidesAlone pins the decide side of a record that
// fell on its requester: the requester confirms on a device that signs, and
// nobody else can, an admin included.
func TestPersonBehindTheAgentDecidesAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pat := h.seedUser(t, "pat")
	root := h.seedUser(t, "root", AdminFallbackRole)

	rec, err := h.svc.Request(ctx, req("s-1", pat.ID, "pat", policy.ApproveSpec{TimeoutSeconds: 90, RetryTTLSeconds: 60}))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !eligibleToDecide(rec, pat.ID, "pat", nil) || eligibleToDecide(rec, root.ID, "root", map[string]bool{AdminFallbackRole: true}) {
		t.Errorf("decidable list: want the requester alone on %+v", rec)
	}
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", root.ID, "console", "", ""); !errors.Is(err, ErrNotRequester) {
		t.Errorf("admin decide = %v, want ErrNotRequester", err)
	}
	out, err := h.svc.Decide(ctx, rec.ID, "approved", pat.ID, "phone", "", "dev-1")
	if err != nil {
		t.Fatalf("requester confirm on a signed device: %v", err)
	}
	if out.State != StateApproved || out.DecidedByName != "pat" {
		t.Errorf("confirmed = %+v", out)
	}
}
