package approval

import (
	"context"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// notifierEvent is one observed lifecycle dispatch: which fake, which method,
// which record.
type notifierEvent struct {
	notifier string
	method   string
	id       string
}

// fakeNotifier records every lifecycle dispatch onto a shared channel (the
// dispatch helpers fire goroutines, so observation is channel-based like
// pushRecorder). It deliberately does NOT implement reminderNotifier.
type fakeNotifier struct {
	id string
	ch chan notifierEvent
}

func (f *fakeNotifier) name() string        { return f.id }
func (f *fakeNotifier) created(rec Record)  { f.ch <- notifierEvent{f.id, "created", rec.ID} }
func (f *fakeNotifier) resolved(rec Record) { f.ch <- notifierEvent{f.id, "resolved", rec.ID} }
func (f *fakeNotifier) reconcile(rec Record) {
	f.ch <- notifierEvent{f.id, "reconcile", rec.ID}
}

// fakeReminderNotifier additionally implements the reminder capability.
type fakeReminderNotifier struct{ fakeNotifier }

func (f *fakeReminderNotifier) reminder(rec Record) {
	f.ch <- notifierEvent{f.id, "reminder", rec.ID}
}

// attachFakes registers n plain fake notifiers sharing one event channel.
func attachFakes(h *harness, n int) chan notifierEvent {
	ch := make(chan notifierEvent, 64)
	for i := 0; i < n; i++ {
		h.svc.register(&fakeNotifier{id: string(rune('a' + i)), ch: ch})
	}
	return ch
}

// waitEvents gathers exactly n events or fails; order across goroutines is not
// deterministic, so callers assert on multisets.
func waitEvents(t *testing.T, ch chan notifierEvent, n int, timeout time.Duration) []notifierEvent {
	t.Helper()
	var out []notifierEvent
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case e := <-ch:
			out = append(out, e)
		case <-deadline:
			t.Fatalf("waited for %d notifier event(s), got %d: %+v", n, len(out), out)
		}
	}
	return out
}

func assertNoEvent(t *testing.T, ch chan notifierEvent, grace time.Duration) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("unexpected notifier event: %+v", e)
	case <-time.After(grace):
	}
}

// countBy tallies events by (notifier, method) for multiset assertions.
func countBy(events []notifierEvent) map[string]int {
	out := map[string]int{}
	for _, e := range events {
		out[e.notifier+"/"+e.method]++
	}
	return out
}

// TestNotifierCreatedFanout: a genuinely new record announces `created` on
// every registered notifier exactly once; a dedupe hit announces nothing (the
// no-spam-on-retry contract, now channel-agnostic).
func TestNotifierCreatedFanout(t *testing.T) {
	h := newHarness(t)
	ch := attachFakes(h, 2)
	ctx := context.Background()
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	rec, err := h.svc.Request(ctx, req("s-1", "u-req", "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	got := countBy(waitEvents(t, ch, 2, 2*time.Second))
	if got["a/created"] != 1 || got["b/created"] != 1 {
		t.Fatalf("created fan-out = %v, want one per notifier", got)
	}

	// Dedupe hit: same key returns the existing record and fires NOTHING.
	if dup, err := h.svc.Request(ctx, req("s-1", "u-req", "nova", spec)); err != nil || dup.ID != rec.ID {
		t.Fatalf("dedupe = %+v, %v", dup, err)
	}
	assertNoEvent(t, ch, 300*time.Millisecond)
}

// TestNotifierResolvedAndReconcileOnDecide: a decision fires `resolved` (the
// win-guarded exactly-once lane) AND `reconcile` (the every-pod broadcast
// lane) on every registered notifier.
func TestNotifierResolvedAndReconcileOnDecide(t *testing.T) {
	h := newHarness(t)
	ch := attachFakes(h, 2)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	waitEvents(t, ch, 2, 2*time.Second) // drain the two created events

	if _, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	got := countBy(waitEvents(t, ch, 4, 2*time.Second))
	for _, want := range []string{"a/resolved", "b/resolved", "a/reconcile", "b/reconcile"} {
		if got[want] != 1 {
			t.Fatalf("decide fan-out = %v, want exactly one %s", got, want)
		}
	}
	assertNoEvent(t, ch, 300*time.Millisecond)
}

// TestNotifierResolvedOnExpiry: the expiry sweep's claim win fires `resolved`
// on every notifier, and nothing else (expiry has no broadcast today, so no
// reconcile; this pins the current shape).
func TestNotifierResolvedOnExpiry(t *testing.T) {
	h := newHarness(t)
	ch := attachFakes(h, 2)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	h.svc.now = func() time.Time { return t0 }
	rec, err := h.svc.Request(ctx, req("s-1", "u-req", "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	waitEvents(t, ch, 2, 2*time.Second) // drain created

	h.svc.now = func() time.Time { return t0.Add(2 * time.Minute) } // past the 90s window
	h.svc.sweepExpired(ctx)
	got := countBy(waitEvents(t, ch, 2, 2*time.Second))
	if got["a/resolved"] != 1 || got["b/resolved"] != 1 {
		t.Fatalf("expiry fan-out = %v, want one resolved per notifier", got)
	}
	_ = rec
	assertNoEvent(t, ch, 300*time.Millisecond)
}

// TestNotifierReminderCapability: the reminder sweep claims the single
// per-ticket reminder ONLY when a reminder-capable notifier is registered.
// A registry of plain notifiers must leave the claim unburned exactly like an
// empty registry (a channel enabled later can still remind the row).
func TestNotifierReminderCapability(t *testing.T) {
	cases := []struct {
		name         string
		attach       func(h *harness) chan notifierEvent
		wantReminder bool
	}{
		{"no notifiers", func(h *harness) chan notifierEvent {
			return make(chan notifierEvent, 8)
		}, false},
		{"plain notifier only", func(h *harness) chan notifierEvent {
			return attachFakes(h, 1)
		}, false},
		{"reminder-capable notifier", func(h *harness) chan notifierEvent {
			ch := make(chan notifierEvent, 8)
			h.svc.register(&fakeReminderNotifier{fakeNotifier{id: "r", ch: ch}})
			return ch
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ch := tc.attach(h)
			t0 := time.Now().UTC().Truncate(time.Microsecond)
			h.svc.now = func() time.Time { return t0 }
			nova := h.seedUser(t, "nova")
			tk := h.seedTicket(t, nova.ID, "tk-cap", t0.Add(-23*time.Hour), t0.Add(time.Hour), nil)

			h.svc.sweepTicketReminders(context.Background())
			if tc.wantReminder {
				got := waitEvents(t, ch, 1, 2*time.Second)
				if got[0].method != "reminder" || got[0].id != tk.ID {
					t.Fatalf("event = %+v, want reminder for %s", got[0], tk.ID)
				}
				if h.reminderListed(t, tk.ID, t0) {
					t.Error("claim must be burned after a delivered reminder")
				}
				return
			}
			assertNoEvent(t, ch, 300*time.Millisecond)
			if !h.reminderListed(t, tk.ID, t0) {
				t.Error("claim must NOT be burned without a reminder-capable notifier")
			}
		})
	}
}

// TestNotifierRoutingNarrows: a record carrying `approve.notify` routing
// (revision 8) announces only on admitted channels: created and resolved are
// filtered, reconcile deliberately is NOT (a channel self-guards on the refs
// it recorded; one that never announced has nothing to reconcile).
func TestNotifierRoutingNarrows(t *testing.T) {
	h := newHarness(t)
	ch := attachFakes(h, 2) // notifiers named "a" and "b"
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60,
		Notify: []string{"a"}}

	rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(rec.Notify) != 1 || rec.Notify[0] != "a" {
		t.Fatalf("record notify = %v, want [a] persisted", rec.Notify)
	}
	got := countBy(waitEvents(t, ch, 1, 2*time.Second))
	if got["a/created"] != 1 {
		t.Fatalf("created = %v, want a only", got)
	}
	assertNoEvent(t, ch, 300*time.Millisecond) // b stays silent

	if _, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	// resolved honors routing (a only); reconcile fans to both.
	got = countBy(waitEvents(t, ch, 3, 2*time.Second))
	if got["a/resolved"] != 1 || got["a/reconcile"] != 1 || got["b/reconcile"] != 1 || got["b/resolved"] != 0 {
		t.Fatalf("decide fan-out = %v, want a/resolved + reconcile on both, no b/resolved", got)
	}
	assertNoEvent(t, ch, 300*time.Millisecond)
}

// TestNotifierRoutingConsoleOnly: `notify: [console]` names no push notifier,
// so every third-party channel stays silent. The console pull surface is the
// whole announcement.
func TestNotifierRoutingConsoleOnly(t *testing.T) {
	h := newHarness(t)
	ch := attachFakes(h, 2)
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60,
		Notify: []string{policy.NotifyConsole}}

	if _, err := h.svc.Request(context.Background(), req("s-1", "u-req", "nova", spec)); err != nil {
		t.Fatalf("Request: %v", err)
	}
	assertNoEvent(t, ch, 300*time.Millisecond)
}

// TestNotifierReminderRouting: the reminder honors the record's routing: a
// routed-in channel fires; a routed-away record burns its claim in deliberate
// silence (immutable routing means no later delivery could become legal, and
// an unclaimed row would relist every sweep until expiry).
func TestNotifierReminderRouting(t *testing.T) {
	cases := []struct {
		name     string
		notify   []string
		wantFire bool
	}{
		{"routed to the capable channel", []string{"r"}, true},
		{"routed away", []string{"elsewhere"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ch := make(chan notifierEvent, 8)
			h.svc.register(&fakeReminderNotifier{fakeNotifier{id: "r", ch: ch}})
			t0 := time.Now().UTC().Truncate(time.Microsecond)
			h.svc.now = func() time.Time { return t0 }
			nova := h.seedUser(t, "nova")

			a, err := h.st.Approvals().Insert(context.Background(), store.Approval{
				SessionID: "s-rt", UserID: nova.ID, RuleID: "r-1", ArgvHash: "rt-1",
				ApproverRoles: []string{"sec-approvers"}, Class: "ticket", State: "pending",
				CreatedAt: t0.Add(-23 * time.Hour), ExpiresAt: t0.Add(time.Hour),
				Notify: tc.notify,
			})
			if err != nil {
				t.Fatalf("Insert: %v", err)
			}

			h.svc.sweepTicketReminders(context.Background())
			if tc.wantFire {
				got := waitEvents(t, ch, 1, 2*time.Second)
				if got[0].method != "reminder" || got[0].id != a.ID {
					t.Fatalf("event = %+v, want reminder for %s", got[0], a.ID)
				}
			} else {
				assertNoEvent(t, ch, 300*time.Millisecond)
			}
			// Both ways the claim is burned exactly once.
			if h.reminderListed(t, a.ID, t0) {
				t.Error("claim must be burned (delivered or deliberately silenced)")
			}
		})
	}
}
