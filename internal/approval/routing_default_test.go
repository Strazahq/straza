package approval

// Revision 14 approval routing of the policy set: a bare approve (no roles,
// no deciders, not confirm, no selfApproval) routes to the requester's
// sponsor by default, and a request whose pool resolves entirely empty is
// denied at request time with an actionable cause instead of holding a record
// nobody will ever see. The carve-outs (selfApproval, confirm, explicit
// roles) keep their semantics.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

func bareSpec() policy.ApproveSpec {
	return policy.ApproveSpec{TimeoutSeconds: 90, RetryTTLSeconds: 60}
}

func TestBareApproveRoutesToSponsor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.seedUser(t, "grace") // active human, no roles
	nova, err := h.st.Users().Create(ctx, store.User{Username: "nova", Email: "nova@x.io",
		UserType: store.UserTypeAgent, Sponsor: "grace"})
	if err != nil {
		t.Fatalf("Create nova: %v", err)
	}

	rec, err := h.svc.Request(ctx, req("s-1", nova.ID, "nova", bareSpec()))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(rec.ApproverUsers) != 1 || rec.ApproverUsers[0] != "grace" {
		t.Fatalf("bare approve ApproverUsers = %v, want [grace] (sponsor default)", rec.ApproverUsers)
	}
	if len(rec.ApproverRoles) != 0 {
		t.Fatalf("bare approve ApproverRoles = %v, want empty", rec.ApproverRoles)
	}
}

func TestBareApproveUnroutableDenies(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		requester func(t *testing.T, h *harness) store.User
		wantCause []string
	}{
		{
			name: "no sponsor",
			requester: func(t *testing.T, h *harness) store.User {
				u, err := h.st.Users().Create(ctx, store.User{Username: "bare", Email: "b@x.io", UserType: store.UserTypeAgent})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				return u
			},
			wantCause: []string{`agent "bare" has no sponsor`, `"r-1"`, "Fix:"},
		},
		{
			name: "unknown sponsor",
			requester: func(t *testing.T, h *harness) store.User {
				u, err := h.st.Users().Create(ctx, store.User{Username: "orphan", Email: "o@x.io",
					UserType: store.UserTypeAgent, Sponsor: "ghost"})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				return u
			},
			wantCause: []string{`sponsor "ghost" is not known to Straza`, "Fix:"},
		},
		{
			name: "NHI sponsor",
			requester: func(t *testing.T, h *harness) store.User {
				if _, err := h.st.Users().Create(ctx, store.User{Username: "botmgr", Email: "m@x.io", UserType: store.UserTypeService}); err != nil {
					t.Fatalf("Create botmgr: %v", err)
				}
				u, err := h.st.Users().Create(ctx, store.User{Username: "sage", Email: "s@x.io",
					UserType: store.UserTypeAgent, Sponsor: "botmgr"})
				if err != nil {
					t.Fatalf("Create sage: %v", err)
				}
				return u
			},
			wantCause: []string{`sponsor "botmgr" is an AI agent or a service account, not a person`},
		},
		{
			name: "sponsor is the requester",
			requester: func(t *testing.T, h *harness) store.User {
				u, err := h.st.Users().Create(ctx, store.User{Username: "carol", Email: "c@x.io", Sponsor: "carol"})
				if err != nil {
					t.Fatalf("Create carol: %v", err)
				}
				return u
			},
			wantCause: []string{`sponsor "carol" is the requester`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			u := tc.requester(t, h)
			_, err := h.svc.Request(ctx, req("s-1", u.ID, u.Username, bareSpec()))
			var un *UnroutableError
			if !errors.As(err, &un) {
				t.Fatalf("Request err = %v, want *UnroutableError", err)
			}
			for _, want := range tc.wantCause {
				if !strings.Contains(un.Cause, want) {
					t.Errorf("cause %q missing %q", un.Cause, want)
				}
			}
			// Denied at request means denied WITHOUT a doomed record.
			pending, err := h.st.Approvals().List(ctx, string(StatePending))
			if err != nil || len(pending) != 0 {
				t.Errorf("pending rows = %d (%v), want 0", len(pending), err)
			}
		})
	}
}

func TestBareApproveCarveOuts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// selfApproval is a decider signal: no sponsor default, record created
	// even for a requester the store cannot read (legacy lane preserved).
	self := bareSpec()
	self.SelfApproval = true
	rec, err := h.svc.Request(ctx, req("s-1", "u-ghost", "ghost", self))
	if err != nil {
		t.Fatalf("selfApproval Request: %v", err)
	}
	if len(rec.ApproverUsers) != 0 || len(rec.ApproverRoles) != 0 {
		t.Errorf("selfApproval pools = %v/%v, want empty/empty", rec.ApproverUsers, rec.ApproverRoles)
	}

	// confirm targets the requester by construction: untouched.
	in := req("s-2", "u-ghost", "ghost", bareSpec())
	in.ArgvHash = "sha256:k-confirm"
	in.Confirm = true
	rec2, err := h.svc.Request(ctx, in)
	if err != nil {
		t.Fatalf("confirm Request: %v", err)
	}
	if rec2.Mode != policy.ModeConfirm || len(rec2.ApproverUsers) != 0 {
		t.Errorf("confirm rec = mode %q pools %v, want confirm/empty", rec2.Mode, rec2.ApproverUsers)
	}

	// An explicit role pool never triggers sponsor resolution: no error even
	// though the requester is unreadable, pool = the roles verbatim.
	roles := bareSpec()
	roles.Roles = []string{"sec-approvers"}
	in3 := req("s-3", "u-ghost", "ghost", roles)
	in3.ArgvHash = "sha256:k-roles"
	rec3, err := h.svc.Request(ctx, in3)
	if err != nil {
		t.Fatalf("roles Request: %v", err)
	}
	if len(rec3.ApproverRoles) != 1 || len(rec3.ApproverUsers) != 0 {
		t.Errorf("roles rec pools = %v/%v, want [sec-approvers]/empty", rec3.ApproverRoles, rec3.ApproverUsers)
	}
}

func TestUnroutableTicketDeniesAtRequest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	bare, err := h.st.Users().Create(ctx, store.User{Username: "bare", Email: "b@x.io", UserType: store.UserTypeAgent})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	spec := bareSpec()
	spec.Class = policy.ClassTicket
	_, err = h.svc.Request(ctx, req("s-1", bare.ID, "bare", spec))
	var un *UnroutableError
	if !errors.As(err, &un) {
		t.Fatalf("ticket Request err = %v, want *UnroutableError (a doomed ticket must not sit a day-scale window)", err)
	}
}

func TestSelfSponsorAllowedUnderSelfApproval(t *testing.T) {
	// Explicit deciders: [sponsor] with selfApproval: the requester deciding
	// their own request is the DECLARED intent, so a self-pointing sponsor
	// edge is not a lockout and resolves normally.
	h := newHarness(t)
	ctx := context.Background()
	carol, err := h.st.Users().Create(ctx, store.User{Username: "carol", Email: "c@x.io", Sponsor: "carol"})
	if err != nil {
		t.Fatalf("Create carol: %v", err)
	}
	spec := sponsorSpec()
	spec.SelfApproval = true
	rec, err := h.svc.Request(ctx, req("s-1", carol.ID, "carol", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(rec.ApproverUsers) != 1 || rec.ApproverUsers[0] != "carol" {
		t.Errorf("ApproverUsers = %v, want [carol]", rec.ApproverUsers)
	}
}
