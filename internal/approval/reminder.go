package approval

import (
	"context"
	"time"
)

// defaultTicketReminderBefore is the fallback near-expiry reminder offset used
// when approval.push.ticketReminderBefore is zero (2h): the single reminder push
// for a still-pending ticket fires once its expiry falls within this window.
// Follows the Retention zero-means-default idiom (config validate() rejects a
// negative value, so only zero or a positive offset reaches here).
const defaultTicketReminderBefore = 2 * time.Hour

// reminderMarkerKey is the channel_refs notifier-state key that records the one
// near-expiry reminder push already fired for a ticket (value = the RFC3339 claim
// time). It lives in the existing channel_refs map, so the reminder needs NO
// migration: ListTicketsForReminder / ClaimTicketReminder gate on its presence
// with a SQL substring guard (channel_refs NOT LIKE '%reminder_pushed_at%'). The
// key is chosen to be unambiguous (no other ref key contains this substring); the
// SQL '_' wildcard matches the literal underscores harmlessly, since the whole
// token still has to appear.
const reminderMarkerKey = "reminder_pushed_at"

// sweepTicketReminders fires the single near-expiry reminder for every
// still-pending ticket entering its reminder window, fanned to every
// reminder-capable notifier. It runs on Run's 15s sweep tick, AFTER
// sweepExpired, so a ticket that lapsed on the same tick is already
// non-pending and correctly skipped. This is a background goroutine, not a
// request path: its DB reads follow the same discipline as sweepExpired
// (DB reads stay off request paths).
//
// Exactly-once: ClaimTicketReminder atomically stamps reminder_pushed_at under
// the still-pending + not-yet-reminded guard, so the pod that wins the claim owns
// the one reminder (mirrors ClaimExpired's per-row atomic claim). How a channel
// renders the reminder is its own contract (pushDelivery.reminder re-sends the
// create envelope, which the mobile app REPLACES rather than stacks). Holds are
// untouched (class filter). No separate audit event: this is the notification
// layer, like the create notification, which is also not separately audited.
func (s *Service) sweepTicketReminders(ctx context.Context) {
	// No reminder-capable notifier ⇒ nothing to emit. Deliberately do NOT
	// claim (stamp the marker) here, so a channel enabled later can still
	// remind a row that is in window now; burning the claim with no delivery
	// would silently drop it.
	if !s.remindersWanted() {
		return
	}
	offset := s.cfg.Push.TicketReminderBefore
	if offset <= 0 {
		offset = defaultTicketReminderBefore
	}
	now := s.now()
	rows, err := s.st.Approvals().ListTicketsForReminder(ctx, now, now.Add(offset), 256)
	if err != nil {
		s.log.Warn("approval ticket reminder sweep: list failed", "err", err)
		return
	}
	for _, a := range rows {
		// Short-window ticket: the whole decision window is <= the reminder offset,
		// so the create push already sits inside the reminder window: a reminder
		// here would fire (near-)immediately at creation, noise not signal. Skip it
		// AND leave the claim unburned (nothing changes on the row).
		if a.ExpiresAt.Sub(a.CreatedAt) <= offset {
			continue
		}
		// Clone the notifier-state map before extending it; never mutate a map
		// shared with the scanned row / other readers.
		refs := make(map[string]string, len(a.ChannelRefs)+1)
		for k, v := range a.ChannelRefs {
			refs[k] = v
		}
		refs[reminderMarkerKey] = now.UTC().Format(time.RFC3339)
		won, err := s.st.Approvals().ClaimTicketReminder(ctx, a.ID, refs)
		if err != nil {
			s.log.Warn("approval ticket reminder: claim failed", "id", a.ID, "err", err)
			continue
		}
		if !won {
			continue // another pod claimed it, or a resolution raced in; benign
		}
		s.log.Info("approval ticket reminder", "id", a.ID, "expiresAt", a.ExpiresAt)
		s.notifyReminder(recordFromStore(a))
	}
}
