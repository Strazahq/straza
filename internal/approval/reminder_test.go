package approval

import (
	"context"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// seedTicket inserts a pending ticket-class approval directly (bypassing Request
// so no create-push fires), returning the stored row. argv keeps the pending
// partial-unique key distinct across rows; requesterID owns it and holds the
// approver role via the record's ApproverRoles.
func (h *harness) seedTicket(t *testing.T, requesterID, argv string, created, expires time.Time, refs map[string]string) store.Approval {
	t.Helper()
	a, err := h.st.Approvals().Insert(context.Background(), store.Approval{
		SessionID: "s-" + argv, UserID: requesterID, RuleID: "r-1", ArgvHash: argv,
		SetName: "guardrails", Summary: "mcp.call midpoint:disable_user",
		ApproverRoles: []string{"sec-approvers"}, Class: "ticket", State: "pending",
		CreatedAt: created, ExpiresAt: expires, ChannelRefs: refs,
	})
	if err != nil {
		t.Fatalf("seedTicket %s: %v", argv, err)
	}
	return a
}

// stillListable reports whether the reminder sweep would still pick up id (i.e.
// no reminder_pushed_at marker was written), using a wide window.
func (h *harness) reminderListed(t *testing.T, id string, now time.Time) bool {
	t.Helper()
	rows, err := h.st.Approvals().ListTicketsForReminder(context.Background(), now, now.Add(72*time.Hour), 256)
	if err != nil {
		t.Fatalf("ListTicketsForReminder: %v", err)
	}
	for _, a := range rows {
		if a.ID == id {
			return true
		}
	}
	return false
}

// TestSweepTicketReminderFiresOnce: a still-pending ticket entering the reminder
// window produces exactly one decide-kind push to the eligible approver, and a
// second sweep tick fires nothing (the atomic claim stamped the marker).
func TestSweepTicketReminderFiresOnce(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	h.svc.now = func() time.Time { return t0 }

	nova := h.seedUser(t, "nova")                // requester
	kim := h.seedUser(t, "kim", "sec-approvers") // eligible approver
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.sh/kim")

	// Pending ticket due in 1h (inside the default 2h window); TTL 24h > offset.
	tk := h.seedTicket(t, nova.ID, "tk-1", t0.Add(-23*time.Hour), t0.Add(time.Hour), nil)

	h.svc.sweepTicketReminders(context.Background())
	got := waitPush(t, rec, 1, 2*time.Second)
	if got[0].user != kim.ID || got[0].kind != pushKindDecide || got[0].ref != tk.ID {
		t.Fatalf("reminder push = %+v, want decide to kim for %s", got[0], tk.ID)
	}

	// Second tick: marker present ⇒ zero new pushes.
	h.svc.sweepTicketReminders(context.Background())
	assertNoPush(t, rec, 300*time.Millisecond)
}

// TestSweepTicketReminderResolvedBefore: a ticket resolved before the sweep is
// never reminded (the list's pending filter + the claim's pending guard).
func TestSweepTicketReminderResolvedBefore(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	h.svc.now = func() time.Time { return t0 }

	nova := h.seedUser(t, "nova")
	kim := h.seedUser(t, "kim", "sec-approvers")
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.sh/kim")

	tk := h.seedTicket(t, nova.ID, "tk-r", t0.Add(-23*time.Hour), t0.Add(time.Hour), nil)
	// Resolve it (approved) before the sweep runs.
	if _, err := h.st.Approvals().MarkDecided(context.Background(), tk.ID, "approved", kim.ID, "Kim", "console", t0, nil, "", ""); err != nil {
		t.Fatalf("MarkDecided: %v", err)
	}

	h.svc.sweepTicketReminders(context.Background())
	assertNoPush(t, rec, 300*time.Millisecond)
}

// TestSweepTicketReminderShortWindow: a ticket whose whole TTL is <= the reminder
// offset is never reminded (the create push already covered it) and the claim is
// never burned, so the row stays untouched.
func TestSweepTicketReminderShortWindow(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	h.svc.now = func() time.Time { return t0 }

	nova := h.seedUser(t, "nova")
	kim := h.seedUser(t, "kim", "sec-approvers")
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.sh/kim")

	// TTL 1h <= default offset 2h: created 30m ago, due in 30m.
	tk := h.seedTicket(t, nova.ID, "tk-short", t0.Add(-30*time.Minute), t0.Add(30*time.Minute), nil)

	h.svc.sweepTicketReminders(context.Background())
	assertNoPush(t, rec, 300*time.Millisecond)
	// The claim was not burned: no marker written.
	if !h.reminderListed(t, tk.ID, t0) {
		t.Error("short-window ticket must not be marked (claim must not be burned)")
	}
}

// TestSweepTicketReminderHoldUntouched: a hold-class pending row entering the
// same window is never reminded (class filter).
func TestSweepTicketReminderHoldUntouched(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	h.svc.now = func() time.Time { return t0 }

	nova := h.seedUser(t, "nova")
	kim := h.seedUser(t, "kim", "sec-approvers")
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.sh/kim")

	// A hold row (class defaults 'hold') with an expiry inside the window.
	if _, err := h.st.Approvals().Insert(context.Background(), store.Approval{
		SessionID: "s-hold", UserID: nova.ID, RuleID: "r-1", ArgvHash: "hold-1",
		ApproverRoles: []string{"sec-approvers"}, State: "pending",
		CreatedAt: t0.Add(-time.Hour), ExpiresAt: t0.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Insert hold: %v", err)
	}

	h.svc.sweepTicketReminders(context.Background())
	assertNoPush(t, rec, 300*time.Millisecond)
}

// TestSweepTicketReminderNoPushBackend: with no push backend the sweep is a
// no-op that burns NO claim, so a later-enabled push can still remind the row.
func TestSweepTicketReminderNoPushBackend(t *testing.T) {
	h := newHarness(t) // no attachPush ⇒ s.push == nil
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	h.svc.now = func() time.Time { return t0 }

	nova := h.seedUser(t, "nova")
	tk := h.seedTicket(t, nova.ID, "tk-nopush", t0.Add(-23*time.Hour), t0.Add(time.Hour), nil)

	h.svc.sweepTicketReminders(context.Background())
	if !h.reminderListed(t, tk.ID, t0) {
		t.Error("push==nil sweep must not burn the claim (row must stay listable)")
	}
}

// TestSweepTicketReminderKnobHonored: a custom offset shifts the fire moment: a
// ticket outside the default window but inside the configured one is reminded;
// and the default applies when the knob is zero.
func TestSweepTicketReminderKnobHonored(t *testing.T) {
	h := newHarness(t)
	rec := attachPush(t, h)
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	h.svc.now = func() time.Time { return t0 }

	nova := h.seedUser(t, "nova")
	kim := h.seedUser(t, "kim", "sec-approvers")
	h.seedPushDevice(t, kim.ID, "unifiedpush", "https://ntfy.sh/kim")

	// Due in 5h: outside the default 2h window, so a zero-knob sweep skips it.
	tk := h.seedTicket(t, nova.ID, "tk-knob", t0.Add(-19*time.Hour), t0.Add(5*time.Hour), nil)
	h.svc.sweepTicketReminders(context.Background())
	assertNoPush(t, rec, 300*time.Millisecond)

	// Widen the knob to 6h: now the same ticket is inside the window and fires.
	h.svc.cfg.Push.TicketReminderBefore = 6 * time.Hour
	h.svc.sweepTicketReminders(context.Background())
	got := waitPush(t, rec, 1, 2*time.Second)
	if got[0].ref != tk.ID || got[0].kind != pushKindDecide {
		t.Fatalf("widened-knob push = %+v, want decide for %s", got[0], tk.ID)
	}
}
