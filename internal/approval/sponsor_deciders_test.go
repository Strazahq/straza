package approval

// Sponsor deciders + the quiet default (spec/policyset revision 13,
// approve.deciders): request-time resolution of the requester's sponsor into
// the record's user-scoped pool, decide rights honoring that pool (with the
// admin fallback narrowed to ENTIRELY empty pools), and the unrouted-record
// announcement silence, so an unconfigured pool can never page every admin.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

func sponsorSpec() policy.ApproveSpec {
	return policy.ApproveSpec{Deciders: []string{policy.DeciderSponsor}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
}

func TestSponsorDeciderResolution(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.seedUser(t, "grace") // active human, holds no roles at all
	nova, err := h.st.Users().Create(ctx, store.User{Username: "nova", Email: "nova@x.io",
		UserType: store.UserTypeAgent, Sponsor: "grace"})
	if err != nil {
		t.Fatalf("Create nova: %v", err)
	}

	rec, err := h.svc.Request(ctx, req("s-1", nova.ID, "nova", sponsorSpec()))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(rec.ApproverUsers) != 1 || rec.ApproverUsers[0] != "grace" {
		t.Fatalf("ApproverUsers = %v, want [grace]", rec.ApproverUsers)
	}
	// Persistence: the pool survives the store round-trip.
	stored, err := h.st.Approvals().GetByID(ctx, rec.ID)
	if err != nil || len(stored.ApproverUsers) != 1 || stored.ApproverUsers[0] != "grace" {
		t.Fatalf("stored ApproverUsers = %v (%v), want [grace]", stored.ApproverUsers, err)
	}
}

func TestSponsorDeciderUnresolvable(t *testing.T) {
	// Revision 14: an explicit [sponsor] pool that fails to resolve, with no
	// roles beside it, is UNROUTABLE and denies at request time.
	// routing_default_test.go pins the causes.
	h := newHarness(t)
	ctx := context.Background()
	bare, _ := h.st.Users().Create(ctx, store.User{Username: "bare", Email: "b@x.io", UserType: store.UserTypeAgent})
	_, err := h.svc.Request(ctx, req("s-1", bare.ID, "bare", sponsorSpec()))
	var un *UnroutableError
	if !errors.As(err, &un) {
		t.Fatalf("Request err = %v, want *UnroutableError", err)
	}

	// A role pool beside the failed sponsor carries the record: warn-only.
	h2 := newHarness(t)
	agent, _ := h2.st.Users().Create(ctx, store.User{Username: "sage", Email: "s@x.io", UserType: store.UserTypeAgent})
	spec := sponsorSpec()
	spec.Roles = []string{"sec-approvers"}
	rec, err := h2.svc.Request(ctx, req("s-2", agent.ID, "sage", spec))
	if err != nil {
		t.Fatalf("Request with roles beside failed sponsor: %v", err)
	}
	if len(rec.ApproverUsers) != 0 || len(rec.ApproverRoles) != 1 {
		t.Errorf("pools = %v/%v, want empty users, [sec-approvers]", rec.ApproverUsers, rec.ApproverRoles)
	}
}

func TestSponsorDecideRights(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	grace := h.seedUser(t, "grace") // no roles: the user pool alone must admit her
	root := h.seedUser(t, "root", AdminFallbackRole)
	nova, _ := h.st.Users().Create(ctx, store.User{Username: "nova", Email: "nova@x.io",
		UserType: store.UserTypeAgent, Sponsor: "grace"})
	rec, err := h.svc.Request(ctx, req("s-1", nova.ID, "nova", sponsorSpec()))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	if _, ok, err := h.svc.approver(ctx, grace.ID, rec.ApproverRoles, rec.ApproverUsers); err != nil || !ok {
		t.Errorf("sponsor may not decide (ok=%v err=%v), want allowed", ok, err)
	}
	// A users-only pool does NOT fall back to straza-admin: the sponsor
	// decides, not every admin (add roles: [straza-admin] to opt in).
	if _, ok, _ := h.svc.approver(ctx, root.ID, rec.ApproverRoles, rec.ApproverUsers); ok {
		t.Error("admin decided a sponsor-routed record via the fallback")
	}
	// An ENTIRELY empty pool keeps the fallback: never unapprovable-by-anyone.
	if _, ok, err := h.svc.approver(ctx, root.ID, nil, nil); err != nil || !ok {
		t.Errorf("empty-pool admin fallback broken (ok=%v err=%v)", ok, err)
	}

	// The queue filter twin agrees: grace sees the row, root does not.
	if !eligibleToDecide(rec, grace.ID, "grace", map[string]bool{}) {
		t.Error("queue filter hides the record from its sponsor")
	}
	if eligibleToDecide(rec, root.ID, "root", map[string]bool{AdminFallbackRole: true}) {
		t.Error("queue filter shows a sponsor-routed record to the admin fallback")
	}
}

// captureNotifier records created-lane announcements synchronously enough to
// assert on (notifyCreated dispatches on goroutines; the channel serializes).
type captureNotifier struct {
	mu      sync.Mutex
	got     chan string
	nameStr string
}

func (c *captureNotifier) name() string { return c.nameStr }
func (c *captureNotifier) created(rec Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got <- rec.ID
}
func (c *captureNotifier) resolved(Record)  {}
func (c *captureNotifier) reconcile(Record) {}

func TestUnroutedRecordsAnnounceNowhere(t *testing.T) {
	h := newHarness(t)
	cap := &captureNotifier{got: make(chan string, 8), nameStr: "cap"}
	h.svc.notifiers = []notifier{cap}

	waitAnnounce := func() string {
		select {
		case id := <-cap.got:
			return id
		case <-time.After(2 * time.Second):
			return ""
		}
	}
	quiet := func() bool {
		select {
		case id := <-cap.got:
			t.Fatalf("unrouted record announced: %s", id)
			return false
		case <-time.After(300 * time.Millisecond):
			return true
		}
	}

	// Unrouted (no roles, no users, plain approve): silence.
	h.svc.notifyCreated(Record{ID: "a-unrouted"})
	quiet()
	// Role-routed: announces.
	h.svc.notifyCreated(Record{ID: "a-roles", ApproverRoles: []string{"sec-approvers"}})
	if waitAnnounce() != "a-roles" {
		t.Error("role-routed record did not announce")
	}
	// Sponsor-routed: announces.
	h.svc.notifyCreated(Record{ID: "a-sponsor", ApproverUsers: []string{"grace"}})
	if waitAnnounce() != "a-sponsor" {
		t.Error("sponsor-routed record did not announce")
	}
	// Confirm targets the requester by construction: announces despite the
	// empty pool. Same for self-approval.
	h.svc.notifyCreated(Record{ID: "a-confirm", Mode: policy.ModeConfirm})
	if waitAnnounce() != "a-confirm" {
		t.Error("confirm record did not announce")
	}
	h.svc.notifyCreated(Record{ID: "a-self", SelfApproval: true})
	if waitAnnounce() != "a-self" {
		t.Error("self-approval record did not announce")
	}
}
