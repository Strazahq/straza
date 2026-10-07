package approval

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// ticketSpec is a fully-normalized ticket-class ApproveSpec (the shape a
// compiled Decision.Approve carries for a `class: ticket` rule).
func ticketSpec() policy.ApproveSpec {
	return policy.ApproveSpec{
		Class: policy.ClassTicket, Roles: []string{"change-approvers"},
		TicketTTLSeconds: 86400, GrantTTLSeconds: 3600, Bind: policy.BindFingerprint,
	}
}

// seedGrant inserts a live, approved, unconsumed ticket grant bound to
// (userID, argvHash): the state a human's approval leaves behind for a later
// (possibly fresh) session to consume.
func (h *harness) seedGrant(t *testing.T, userID, argvHash string, grantExp time.Time) store.Approval {
	t.Helper()
	now := time.Now().UTC()
	a, err := h.st.Approvals().Insert(context.Background(), store.Approval{
		SessionID: "planning-sess", UserID: userID, Username: "nova", RuleID: "r-1",
		SetName: "guardrails", ArgvHash: argvHash, Lane: "hook",
		Summary: "mcp.call midpoint:disable_user", Class: "ticket", State: "approved",
		DecidedBy: "u-appr", DecidedByName: "Ada Approver",
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(24 * time.Hour),
		GrantExpiresAt: &grantExp,
	})
	if err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	return a
}

// TestRequestCreatesTicket pins the ticket branch in Request: a ticket-class
// rule creates a pending row with Class="ticket" and ExpiresAt = createdAt +
// ticketTTL (the day-scale decision window, NOT the 90s hold window), no
// blocking-wait knobs, and dedupes repeated denied calls onto the one open
// ticket (via the user-scoped FindOpenTicketByUser), never re-notifying.
func TestRequestCreatesTicket(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	spec := ticketSpec()

	rec, err := h.svc.Request(ctx, req("s-1", "u-req", "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if rec.Class != "ticket" {
		t.Errorf("class = %q, want ticket", rec.Class)
	}
	if rec.State != StatePending {
		t.Errorf("state = %q, want pending", rec.State)
	}
	if got := rec.ExpiresAt.Sub(rec.CreatedAt); got != 86400*time.Second {
		t.Errorf("ticket decision window = %s, want 24h", got)
	}
	if rec.TimeoutSeconds != 0 || rec.RetryTTLSeconds != 0 {
		t.Errorf("ticket must not carry hold blocking knobs: timeout=%d retry=%d",
			rec.TimeoutSeconds, rec.RetryTTLSeconds)
	}

	// Dedupe: a repeated denied call attaches to the one open ticket.
	dup, err := h.svc.Request(ctx, req("s-1", "u-req", "nova", spec))
	if err != nil || dup.ID != rec.ID {
		t.Fatalf("dedupe = %+v, %v (want same id %s)", dup, err, rec.ID)
	}
	all, _ := h.st.Approvals().List(ctx, "")
	if len(all) != 1 {
		t.Errorf("expected 1 ticket row after dedupe, got %d", len(all))
	}
	assertAudit(t, h.st, "request", "pending")
}

// TestTicketRequestApproveConsumeEndToEnd is the real-flow proof:
// Request(ticket) → Decide(approved) → ConsumeGrant, with NO seeded grant
// rows. Unless Decide materializes approvals.grant_expires_at at decision
// time, FindConsumableGrant never matches (grant_expires_at NULL, and
// NULL > now is false) and the approved ticket is permanently un-consumable.
func TestTicketRequestApproveConsumeEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "change-approvers")
	spec := ticketSpec()

	rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	// A pending ticket is never consumable: the grant only materializes on
	// approval (the "no grant yet" invariant).
	if _, ok, err := h.svc.ConsumeGrant(ctx, requester.ID, "sha256:k1", "s-1"); ok || err != nil {
		t.Fatalf("pending ticket consume = ok:%v err:%v, want (false,nil)", ok, err)
	}

	out, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "", "")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.State != StateApproved {
		t.Fatalf("decide state = %q, want approved", out.State)
	}
	// The grant deadline was materialized at DECISION time (decidedAt +
	// grantTTL), NOT at request/createdAt time.
	if out.DecidedAt == nil {
		t.Fatal("approved ticket must carry a decidedAt")
	}
	if out.GrantExpiresAt == nil {
		t.Fatal("approved ticket must carry a materialized grant deadline (Phase 2b seam)")
	}
	if got := out.GrantExpiresAt.Sub(*out.DecidedAt); got != time.Duration(spec.GrantTTLSeconds)*time.Second {
		t.Errorf("grant window measured from decidedAt = %s, want %ds", got, spec.GrantTTLSeconds)
	}

	// With no seeded rows, the just-approved ticket is consumable.
	got, ok, err := h.svc.ConsumeGrant(ctx, requester.ID, "sha256:k1", "exec-sess")
	if err != nil || !ok {
		t.Fatalf("approved-ticket consume = ok:%v err:%v, want ok (Phase 2b fix)", ok, err)
	}
	if got.ID != rec.ID {
		t.Errorf("consumed %s, want the approved ticket %s", got.ID, rec.ID)
	}
	// Single-use: the second retry finds nothing.
	if _, ok, _ := h.svc.ConsumeGrant(ctx, requester.ID, "sha256:k1", "exec-sess-2"); ok {
		t.Error("grant must be single-use")
	}
}

// TestConsumeGrantSingleUse pins the DB-durable, single-use consume: the first
// ConsumeGrant wins (allow-once), a second finds nothing, and a "consumed"
// audit CE lands carrying the consuming session (state stays approved).
func TestConsumeGrantSingleUse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	grantExp := time.Now().UTC().Add(time.Hour)
	g := h.seedGrant(t, "u-req", "sha256:deploy", grantExp)

	rec, ok, err := h.svc.ConsumeGrant(ctx, "u-req", "sha256:deploy", "sess-A")
	if err != nil || !ok {
		t.Fatalf("first consume = ok:%v err:%v (want ok)", ok, err)
	}
	if rec.ID != g.ID {
		t.Errorf("consumed %s, want %s", rec.ID, g.ID)
	}
	if _, ok, _ := h.svc.ConsumeGrant(ctx, "u-req", "sha256:deploy", "sess-B"); ok {
		t.Error("grant must be single-use")
	}
	assertAudit(t, h.st, "consumed", "approved")
	assertAuditField(t, h.st, "consumed", "consumedBy", "sess-A")
}

// TestConsumeGrantCrossSession is the plan-gate case: a grant a planning
// session raised is consumed by a DIFFERENT session of the SAME user. Grants
// follow the human + fingerprint, never the session id.
func TestConsumeGrantCrossSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	grantExp := time.Now().UTC().Add(time.Hour)
	h.seedGrant(t, "u-req", "sha256:deploy", grantExp) // seeded under "planning-sess"

	_, ok, err := h.svc.ConsumeGrant(ctx, "u-req", "sha256:deploy", "fresh-execution-session")
	if err != nil || !ok {
		t.Fatalf("cross-session consume = ok:%v err:%v (want ok)", ok, err)
	}
}

// countAuditPhase counts outbox approval-audit CEs carrying the given phase:
// the "was a second request audited?" question assertAudit (presence-only)
// cannot answer.
func countAuditPhase(t *testing.T, st store.Store, phase string) int {
	t.Helper()
	evs, err := st.Outbox().ListUnpublished(context.Background(), 100)
	if err != nil {
		t.Fatalf("Outbox: %v", err)
	}
	n := 0
	for _, e := range evs {
		if e.Subject == auditSubject && strings.Contains(e.CE, `"phase":"`+phase+`"`) {
			n++
		}
	}
	return n
}

// TestRequestTicketDedupesAcrossSessions pins the user-scoped ticket dedupe:
// a FRESH session re-raising the same user + rule + call
// must attach to the ticket the earlier session already put in front of a
// human, never mint a SECOND pending ticket and push the approver again. The
// assertions are the full no-duplicate contract: same id, one row, ONE
// notify, ONE request audit.
func TestRequestTicketDedupesAcrossSessions(t *testing.T) {
	h := newHarness(t)
	ch := attachFakes(h, 1)
	ctx := context.Background()
	spec := ticketSpec()

	rec, err := h.svc.Request(ctx, req("planning-sess", "u-req", "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if got := countBy(waitEvents(t, ch, 1, 2*time.Second)); got["a/created"] != 1 {
		t.Fatalf("first request notify = %v, want one created", got)
	}

	// A different session of the SAME user, same rule, same call.
	dup, err := h.svc.Request(ctx, req("fresh-sess", "u-req", "nova", spec))
	if err != nil {
		t.Fatalf("fresh-session Request: %v", err)
	}
	if dup.ID != rec.ID {
		t.Errorf("fresh session got ticket %s, want the open ticket %s", dup.ID, rec.ID)
	}
	if dup.SessionID != rec.SessionID {
		t.Errorf("dedupe must return the EXISTING record verbatim: session %q, want %q",
			dup.SessionID, rec.SessionID)
	}
	// No second push, no second row, no second request audit.
	assertNoEvent(t, ch, 300*time.Millisecond)
	all, _ := h.st.Approvals().List(ctx, "")
	if len(all) != 1 {
		t.Errorf("cross-session dedupe must not open a second ticket, got %d rows", len(all))
	}
	if n := countAuditPhase(t, h.st, "request"); n != 1 {
		t.Errorf("request audit phases = %d, want 1 (a dedupe hit audits nothing)", n)
	}

	// Another user is NOT deduped: the scope is the requester, not the call.
	other, err := h.svc.Request(ctx, req("other-sess", "u-other", "sam", spec))
	if err != nil {
		t.Fatalf("other-user Request: %v", err)
	}
	if other.ID == rec.ID {
		t.Error("a different user must get their own ticket")
	}
}

// TestRequestTicketConcurrentSessionsOneTicket is the race the store index
// exists for: two fresh sessions of one user raise the same ticket at the same
// instant, both miss the dedupe lookup, and both insert. Exactly one row may
// survive; the loser must adopt the winner: same id back, NO error surfaced,
// and only ONE approver push for the pair.
func TestRequestTicketConcurrentSessionsOneTicket(t *testing.T) {
	h := newHarness(t)
	ch := attachFakes(h, 1)
	ctx := context.Background()
	spec := ticketSpec()

	const racers = 8
	var wg sync.WaitGroup
	ids := make(chan string, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			rec, err := h.svc.Request(ctx, req(fmt.Sprintf("fresh-sess-%d", n), "u-req", "nova", spec))
			if err != nil {
				t.Errorf("racing Request: %v", err) // the loser must NOT surface an error
				return
			}
			ids <- rec.ID
		}(i)
	}
	close(start)
	wg.Wait()
	close(ids)

	seen := map[string]int{}
	total := 0
	for id := range ids {
		seen[id]++
		total++
	}
	if total != racers {
		t.Fatalf("only %d/%d racers returned a ticket", total, racers)
	}
	if len(seen) != 1 {
		t.Errorf("racers got %d distinct ticket ids, want 1: %v", len(seen), seen)
	}
	all, _ := h.st.Approvals().List(ctx, "")
	if len(all) != 1 {
		t.Errorf("concurrent requests left %d rows, want exactly 1", len(all))
	}
	if got := countBy(waitEvents(t, ch, 1, 2*time.Second)); got["a/created"] != 1 {
		t.Errorf("notify fan-out = %v, want exactly one created for the pair", got)
	}
	assertNoEvent(t, ch, 300*time.Millisecond)
	if n := countAuditPhase(t, h.st, "request"); n != 1 {
		t.Errorf("request audit phases = %d, want 1", n)
	}
}

// TestRequestHoldStaysSessionScoped pins that holds stay
// session-scoped: a hold is a bounded blocking wait belonging to ONE session,
// so a second session requesting the same call gets its OWN pending hold and
// its own approver push, unaffected by the user-scoped ticket dedupe.
func TestRequestHoldStaysSessionScoped(t *testing.T) {
	h := newHarness(t)
	ch := attachFakes(h, 1)
	ctx := context.Background()
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	first, err := h.svc.Request(ctx, req("s-1", "u-req", "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	second, err := h.svc.Request(ctx, req("s-2", "u-req", "nova", spec))
	if err != nil {
		t.Fatalf("second-session Request: %v", err)
	}
	if second.ID == first.ID {
		t.Error("a hold must NOT dedupe across sessions (session-bound by design)")
	}
	if second.Class == policy.ClassTicket || first.Class == policy.ClassTicket {
		t.Errorf("hold requests must stay class hold: %q / %q", first.Class, second.Class)
	}
	all, _ := h.st.Approvals().List(ctx, "")
	if len(all) != 2 {
		t.Errorf("two sessions must hold two rows, got %d", len(all))
	}
	// Each session's hold announces on its own: one push per record.
	if got := countBy(waitEvents(t, ch, 2, 2*time.Second)); got["a/created"] != 2 {
		t.Errorf("hold notify fan-out = %v, want one created per session", got)
	}
	// Same session repeating still dedupes onto its own hold, no new push.
	again, err := h.svc.Request(ctx, req("s-1", "u-req", "nova", spec))
	if err != nil || again.ID != first.ID {
		t.Fatalf("same-session hold dedupe = %+v, %v (want %s)", again, err, first.ID)
	}
	assertNoEvent(t, ch, 300*time.Millisecond)
}

// TestConsumeGrantExpiredDeniesFinal: an approved grant past its consume window
// is dead: ConsumeGrant reports no grant (the PEP then denies final).
func TestConsumeGrantExpiredDeniesFinal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	past := time.Now().UTC().Add(-time.Minute)
	h.seedGrant(t, "u-req", "sha256:deploy", past)

	if _, ok, err := h.svc.ConsumeGrant(ctx, "u-req", "sha256:deploy", "sess"); ok || err != nil {
		t.Errorf("expired grant consume = ok:%v err:%v, want (false,nil)", ok, err)
	}
}

// TestConsumeGrantRace is the two-parallel-retries case: N goroutines race to
// cash the one live grant; exactly one wins (single-use holds under -race).
func TestConsumeGrantRace(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	grantExp := time.Now().UTC().Add(time.Hour)
	h.seedGrant(t, "u-req", "sha256:deploy", grantExp)

	const racers = 8
	var wg sync.WaitGroup
	wins := make(chan bool, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok, err := h.svc.ConsumeGrant(ctx, "u-req", "sha256:deploy", "sess")
			if err != nil {
				t.Errorf("ConsumeGrant err: %v", err)
			}
			wins <- ok
		}()
	}
	close(start)
	wg.Wait()
	close(wins)

	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("parallel consume: %d winners, want exactly 1", n)
	}
}

// TestDeniedTicketWithinWindowDeniesFinal: after a human denies a ticket, the
// deny-final lookup reports the still-in-window denial so the PEP blocks the
// call without opening a fresh ticket.
func TestDeniedTicketWithinWindowDeniesFinal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "change-approvers")

	rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", ticketSpec()))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := h.svc.Decide(ctx, rec.ID, "denied", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	got, ok, err := h.svc.DeniedTicketWithinWindow(ctx, requester.ID, "r-1", "sha256:k1")
	if err != nil || !ok {
		t.Fatalf("deny-final = ok:%v err:%v, want hit", ok, err)
	}
	if got.ID != rec.ID || got.State != StateDenied || got.DecidedByName != "kim" {
		t.Errorf("deny-final record = %+v, want denied %s by kim", got, rec.ID)
	}
}

// TestDeniedTicketPastWindowReopens: a denial whose decision window has already
// closed is NOT deny-final (the call re-opens a fresh ticket), and a key with no
// prior ticket is likewise not deny-final.
func TestDeniedTicketPastWindowReopens(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// A denied ticket whose decision window closed an hour ago.
	if _, err := h.st.Approvals().Insert(ctx, store.Approval{
		SessionID: "s-old", UserID: "u-req", RuleID: "r-1", ArgvHash: "sha256:k1",
		Lane: "hook", Class: "ticket", State: "denied",
		DecidedBy: "u-appr", DecidedByName: "Ada",
		CreatedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed past-window denial: %v", err)
	}
	if _, ok, err := h.svc.DeniedTicketWithinWindow(ctx, "u-req", "r-1", "sha256:k1"); ok || err != nil {
		t.Errorf("past-window denial = ok:%v err:%v, want (false,nil)", ok, err)
	}
	// No prior ticket for a different key → not deny-final.
	if _, ok, err := h.svc.DeniedTicketWithinWindow(ctx, "u-req", "r-1", "sha256:none"); ok || err != nil {
		t.Errorf("no prior ticket = ok:%v err:%v, want (false,nil)", ok, err)
	}
}

// TestTicketAuditSequence pins the full audit story for a ticket:
// request → resolution → consumed, each phase present with its state.
func TestTicketAuditSequence(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "change-approvers")
	spec := ticketSpec()

	rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	assertAudit(t, h.st, "request", "pending")

	if _, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	assertAudit(t, h.st, "resolution", "approved")

	// A live grant for the same user+fingerprint is cashed on a later retry.
	grantExp := time.Now().UTC().Add(time.Hour)
	h.seedGrant(t, requester.ID, "sha256:k1", grantExp)
	if _, ok, err := h.svc.ConsumeGrant(ctx, requester.ID, "sha256:k1", "s-1"); err != nil || !ok {
		t.Fatalf("consume = ok:%v err:%v", ok, err)
	}
	assertAudit(t, h.st, "consumed", "approved")
}

// assertAuditField fails unless an outbox straza.audit.approval CE for `phase`
// carries `field`:`val`.
func assertAuditField(t *testing.T, st store.Store, phase, field, val string) {
	t.Helper()
	evs, err := st.Outbox().ListUnpublished(context.Background(), 100)
	if err != nil {
		t.Fatalf("Outbox: %v", err)
	}
	for _, e := range evs {
		if e.Subject == auditSubject &&
			strings.Contains(e.CE, `"phase":"`+phase+`"`) &&
			strings.Contains(e.CE, `"`+field+`":"`+val+`"`) {
			return
		}
	}
	t.Errorf("no %s audit CE with %s=%s", phase, field, val)
}
