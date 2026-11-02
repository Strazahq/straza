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

// TestBareApproveUnroutableDenies pins that Routable answers what Request
// answers for the same call and writes nothing: the same cause for each
// sponsor that cannot decide, nil when a pending ticket already covers the
// call, and the same error when the ticket lookup meets a store fault.
func TestBareApproveUnroutableDenies(t *testing.T) {
	ctx := context.Background()
	// agentOf creates the agent nova sponsored by sponsor.
	agentOf := func(t *testing.T, h *harness, sponsor string) store.User {
		u, err := h.st.Users().Create(ctx, store.User{Username: "nova", Email: "n@x.io",
			UserType: store.UserTypeAgent, Sponsor: sponsor})
		if err != nil {
			t.Fatalf("Create nova: %v", err)
		}
		return u
	}
	cases := []struct {
		name      string
		requester func(t *testing.T, h *harness) store.User
		ticket    bool     // the rule is class ticket
		wantCause []string // the UnroutableError cause both answer
		wantFault error    // the store error both answer instead
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
			name: "disabled sponsor",
			requester: func(t *testing.T, h *harness) store.User {
				if _, err := h.st.Users().Create(ctx, store.User{Username: "gone", Email: "g@x.io", Status: store.UserDisabled}); err != nil {
					t.Fatalf("Create gone: %v", err)
				}
				return agentOf(t, h, "gone")
			},
			ticket:    true,
			wantCause: []string{`sponsor "gone" is not an active user`, "Fix:"},
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
		{
			// The lookup runs first, as in Request, so the ticket that
			// covers the call still answers after its sponsor is disabled.
			name: "a pending ticket for the same call routes after its sponsor is disabled",
			requester: func(t *testing.T, h *harness) store.User {
				grace := h.seedUser(t, "grace")
				nova := agentOf(t, h, "grace")
				spec := bareSpec()
				spec.Class = policy.ClassTicket
				if _, err := h.svc.Request(ctx, req("s-0", nova.ID, "nova", spec)); err != nil {
					t.Fatalf("open the ticket: %v", err)
				}
				grace.Status = store.UserDisabled
				if _, err := h.st.Users().Update(ctx, grace); err != nil {
					t.Fatalf("disable grace: %v", err)
				}
				return nova
			},
			ticket: true,
		},
		{
			name: "a store fault on the ticket lookup",
			requester: func(t *testing.T, h *harness) store.User {
				h.seedUser(t, "grace")
				nova := agentOf(t, h, "grace")
				h.svc.st = &flakyStore{Store: h.st, ticketErr: pgDown}
				return nova
			},
			ticket:    true,
			wantFault: pgDown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			u := tc.requester(t, h)
			spec := bareSpec()
			if tc.ticket {
				spec.Class = policy.ClassTicket
			}
			in := req("s-1", u.ID, u.Username, spec)
			// rows counts the approval rows and the outbox rows in the real
			// store, which a faulted service still writes to.
			rows := func() (int, int) {
				t.Helper()
				recs, err := h.st.Approvals().List(ctx, "")
				if err != nil {
					t.Fatal(err)
				}
				evs, err := h.st.Outbox().ListUnpublished(ctx, 1000)
				if err != nil {
					t.Fatal(err)
				}
				return len(recs), len(evs)
			}
			approvals, outbox := rows()
			checked := h.svc.Routable(ctx, in)
			if a, o := rows(); a != approvals || o != outbox {
				t.Errorf("Routable wrote %d approval rows and %d outbox rows, want none", a-approvals, o-outbox)
			}
			_, err := h.svc.Request(ctx, in)
			// Denied at request means denied WITHOUT a doomed record, and a
			// joined ticket is the one already there.
			if a, o := rows(); a != approvals || o != outbox {
				t.Errorf("Request wrote %d approval rows and %d outbox rows, want none", a-approvals, o-outbox)
			}
			var un, checkedUn *UnroutableError
			switch {
			case tc.wantCause != nil:
				if !errors.As(err, &un) || !errors.As(checked, &checkedUn) {
					t.Fatalf("Request err = %v and Routable err = %v, want *UnroutableError from both", err, checked)
				}
				if checkedUn.Cause != un.Cause {
					t.Errorf("Routable cause %q, want Request's %q", checkedUn.Cause, un.Cause)
				}
				for _, want := range tc.wantCause {
					if !strings.Contains(un.Cause, want) {
						t.Errorf("cause %q missing %q", un.Cause, want)
					}
				}
			case tc.wantFault != nil:
				if !errors.Is(err, tc.wantFault) || errors.As(err, &un) || checked == nil || checked.Error() != err.Error() {
					t.Errorf("Routable err = %v, Request err = %v, want the same store fault from both", checked, err)
				}
			case checked != nil || err != nil:
				t.Errorf("Routable err = %v, Request err = %v, want both to join the pending ticket", checked, err)
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
