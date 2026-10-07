package approval

import "github.com/strazahq/straza/internal/policy"

// notifier is one registered third-party approval notification channel (Slack,
// mobile push, Teams later). The console is NOT a notifier: it is a pull
// surface with no delivery step, and so are the CLI and admin API.
//
// Delivery is best effort with logging, and a channel failure never affects
// the approval outcome (fail-open for the notification, never for the
// decision). Every method runs on its own dispatch goroutine off the request
// path and must bound its own I/O with timeouts. The CALLER guarantees the
// lane: created fires only on the pod that INSERTED a genuinely new pending
// record, never on a dedupe or conflict-adopt hit; resolved fires exactly once
// cluster-wide, on the pod that won the atomic terminal flip, so it carries
// anything that must not duplicate across pods; reconcile fires on EVERY pod
// receiving the broadcast with whatever state that pod could read, so it must
// be idempotent and self-guard on missing state (surface edits like card
// updates, never fresh notifications).
type notifier interface {
	name() string
	created(rec Record)
	resolved(rec Record)
	reconcile(rec Record)
}

// reminderNotifier is the optional near-expiry ticket reminder capability
// (reminder.go). The sweep claims the single per-ticket reminder ONLY when at
// least one registered notifier implements this: claiming with no deliverer
// would burn the one reminder silently, so a deployment without a
// reminder-capable channel leaves rows unclaimed for a channel enabled later.
type reminderNotifier interface {
	reminder(rec Record)
}

// register appends a notifier to the dispatch registry (construction time
// only; the slice is never mutated after New returns, so dispatch reads it
// without locking).
func (s *Service) register(n notifier) {
	s.notifiers = append(s.notifiers, n)
}

// channelAllowed reports whether rec's notification routing admits the named
// channel (spec/policyset revision 8 `approve.notify`, persisted on the record
// at request time). Empty routing = every configured channel (the
// pre-revision-8 default). Routing filters the ANNOUNCEMENT lanes only
// (created/resolved/reminder); reconcile reacts to what actually happened
// (a card this channel itself posted), not to what routing says should.
func channelAllowed(rec Record, name string) bool {
	if len(rec.Notify) == 0 {
		return true
	}
	for _, ch := range rec.Notify {
		if ch == name {
			return true
		}
	}
	return false
}

// unrouted reports whether rec carries no decider signal at all: no roles, no
// user-scoped deciders, no requester lane (confirm and self-approval records
// target the requester by construction). Such a record is announced NOWHERE
// (revision 13, the quiet default): straza-admin keeps decide rights over it
// on the pull surfaces (console/CLI) and it expires to deny as ever, but an
// unconfigured pool no longer pages every admin's phone at fleet scale.
func unrouted(rec Record) bool {
	return len(rec.ApproverRoles) == 0 && len(rec.ApproverUsers) == 0 &&
		rec.Mode != policy.ModeConfirm && !rec.SelfApproval
}

// notifyCreated fans the new-record announce to every registered notifier the
// record's routing admits. Call sites guarantee the inserting-pod-only
// contract.
func (s *Service) notifyCreated(rec Record) {
	if unrouted(rec) {
		return
	}
	for _, n := range s.notifiers {
		if channelAllowed(rec, n.name()) {
			go n.created(rec) // #nosec G118 -- best-effort; failure logs, record stands
		}
	}
}

// notifyResolved fans a terminal transition to every registered notifier the
// record's routing admits. Call sites guarantee the exactly-once (state-flip
// win) contract.
func (s *Service) notifyResolved(rec Record) {
	for _, n := range s.notifiers {
		if channelAllowed(rec, n.name()) {
			go n.resolved(rec) // #nosec G118 -- best-effort; failure logs, record stands
		}
	}
}

// notifyReconcile fans the every-pod resolution broadcast to every registered
// notifier; implementations are idempotent by contract and self-guard on the
// refs they themselves recorded, so routing is deliberately NOT consulted (a
// channel that never announced has nothing to reconcile).
func (s *Service) notifyReconcile(rec Record) {
	for _, n := range s.notifiers {
		go n.reconcile(rec) // #nosec G118 -- best-effort surface update
	}
}

// notifyReminder fans the near-expiry ticket reminder to every
// reminder-capable notifier the record's routing admits. Call sites guarantee
// the exactly-once (ClaimTicketReminder win) contract. A record whose routing
// admits NO reminder-capable channel still burns its claim at the call site
// (deliberate silence, not a lost reminder): the record's routing is immutable,
// so no later delivery could ever become legal, and leaving the row unclaimed
// would relist it every sweep until expiry (a starvation risk under the
// bounded list).
func (s *Service) notifyReminder(rec Record) {
	if unrouted(rec) {
		return
	}
	for _, n := range s.notifiers {
		if r, ok := n.(reminderNotifier); ok && channelAllowed(rec, n.name()) {
			go r.reminder(rec) // #nosec G118 -- best-effort; failure logs, record stands
		}
	}
}

// remindersWanted reports whether any registered notifier can deliver a
// ticket reminder, the reminder sweep's claim guard.
func (s *Service) remindersWanted() bool {
	for _, n := range s.notifiers {
		if _, ok := n.(reminderNotifier); ok {
			return true
		}
	}
	return false
}
