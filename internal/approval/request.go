package approval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/store"
)

// Request intake: mint a pending record, or attach to the live one that already
// covers this call. The class picks both the row shape (hold = bounded blocking
// wait, ticket = day-scale decision window + post-approval grant) and the dedupe
// scope (session vs user).

// UnroutableError reports a request whose decider pool resolved entirely
// empty (revision 14): no roles, no usable sponsor, no requester lane. Such
// an approval would hold a record nobody is notified of and nobody but a
// console-watching admin could ever find, so the PEP denies it at request
// time instead, and Cause carries the words the calling agent sees: what
// failed to resolve, then the concrete fix.
type UnroutableError struct{ Cause string }

func (e *UnroutableError) Error() string { return "approval unroutable: " + e.Cause }

// modeFor maps the PEP's decision marker onto the record's mode column. A
// sponsor-routed record that fell on its requester is a confirm record too:
// the same person decides alone, so every lane treats the two alike.
func modeFor(in RequestInput, requesterDecides bool) string {
	if in.Confirm || requesterDecides {
		return policy.ModeConfirm
	}
	return policy.ModeApprove
}

// bareApprove reports whether the rule carries NO decider signal at all: no
// roles, no decider kinds, not confirm, no selfApproval. Revision 14 gives
// such a rule the sponsor default: approval falls on the requester's
// accountable human unless the author said otherwise.
func bareApprove(in RequestInput) bool {
	return len(in.Spec.Roles) == 0 && len(in.Spec.Deciders) == 0 &&
		!in.Confirm && !in.Spec.SelfApproval
}

// resolveDeciders materializes the record's user-scoped decide pool at
// request time: the explicit decider KINDS (revision 13 approve.deciders,
// today only sponsor) plus the revision 14 sponsor DEFAULT for a bare
// approve. The sponsor decider means the person behind the agent. For a
// requester who has a sponsor that is the sponsor, who must exist, be active,
// not be an NHI (an agent sponsoring an agent is not accountability), and not
// be the requester (unless selfApproval declared exactly that intent). For a
// person with no sponsor it is the person: requesterDecides reports it, and
// the caller turns the record into a confirm record unless roles carry it.
// An agent with no sponsor has no person behind it. A resolution failure
// returns the empty pool plus a cause string; the caller decides whether
// roles still carry the record (warn-only) or nothing does (UnroutableError,
// revision 14: deny now, never hold a doomed record).
func (s *Service) resolveDeciders(ctx context.Context, in RequestInput) (users []string, requesterDecides bool, cause string) {
	wantSponsor := bareApprove(in)
	for _, dk := range in.Spec.Deciders {
		if dk == policy.DeciderSponsor {
			wantSponsor = true
		}
	}
	if !wantSponsor {
		return nil, false, ""
	}
	requester, err := s.st.Users().GetByID(ctx, in.UserID)
	if err != nil {
		return nil, false, fmt.Sprintf("requester %q could not be read (%v)", in.Username, err)
	}
	if requester.Sponsor == "" {
		if userIsNHI(requester) {
			return nil, false, fmt.Sprintf("agent %q has no sponsor", in.Username)
		}
		return nil, true, ""
	}
	su, err := s.st.Users().GetByUsername(ctx, requester.Sponsor)
	if err != nil {
		return nil, false, fmt.Sprintf("sponsor %q is not known to Straza (identity manager sync missing or stale)", requester.Sponsor)
	}
	if su.Status != store.UserActive {
		return nil, false, fmt.Sprintf("sponsor %q is not an active user", requester.Sponsor)
	}
	if userIsNHI(su) {
		return nil, false, fmt.Sprintf("sponsor %q is an AI agent or a service account, not a person", requester.Sponsor)
	}
	if su.ID == requester.ID && !in.Spec.SelfApproval {
		return nil, false, fmt.Sprintf("sponsor %q is the requester", su.Username)
	}
	return []string{su.Username}, false, ""
}

// Request creates (or dedupes onto) a pending record, then notifies + audits.
// The dedupe key is (rule, argvHash) among pending rows, scoped per class by
// findPendingDedupe: session for a hold, USER for a ticket. A hit returns the
// existing record WITHOUT re-notifying (no channel spam on retry).
func (s *Service) Request(ctx context.Context, in RequestInput) (Record, error) {
	// Intake sanitation for the client/model-typed display strings. Postgres
	// rejects a raw NUL in any text value (SQLSTATE 22021: a justification
	// carrying one would turn this whole call into an unexplained fail-closed
	// deny, while sqlite stores the same bytes silently), and
	// these strings render on approver surfaces, where C0/bidi tricks can
	// make the displayed request differ from the real one. Neutralize escapes
	// all of that to inert visible ASCII and is a no-op on clean text; the
	// preview lane already arrives escaped, this pins the invariant at the
	// one place every caller shares. Username is deliberately untouched: it
	// is a server-verified identity field joined against users, not display
	// text, and escaping it would break those joins.
	in.Summary = redact.Neutralize(in.Summary)
	in.Justification = redact.Neutralize(in.Justification)
	in.ArgsPreview = redact.Neutralize(in.ArgsPreview)
	if existing, err := s.findPendingDedupe(ctx, in, in.ArgvHash); err == nil {
		s.log.Debug("approval request attached to pending", "component", "approval", "id", existing.ID, "rule", in.RuleID)
		return recordFromStore(existing), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return Record{}, fmt.Errorf("approval request-dedupe: %w", err) // the PEP logs once with the correlation id
	}

	users, requesterDecides, cause := s.resolveDeciders(ctx, in)
	// A rule that also names approver roles asked for a second pair of eyes,
	// so the roles carry the record and it never becomes a self-confirmation.
	requesterDecides = requesterDecides && len(in.Spec.Roles) == 0
	if len(users) == 0 && len(in.Spec.Roles) == 0 && !in.Confirm && !in.Spec.SelfApproval && !requesterDecides {
		// Revision 14: an entirely empty pool means nobody would ever be
		// notified and nobody but a console-watching admin could ever decide,
		// so the call is denied NOW with the cause instead of minting a record
		// that can only expire (a fleet must never wait on an approval nobody
		// will see). The PEP's decision audit carries the deny + reason; this
		// warn line and the server-side unroutable counter are the ops trail.
		if cause == "" {
			cause = "the rule names no approver"
		}
		s.log.Warn("approval unroutable; denied at request",
			"lane", in.Lane, "rule", in.RuleID, "user", in.Username, "cause", cause)
		return Record{}, &UnroutableError{Cause: fmt.Sprintf(
			"%s, and rule %q names no approver roles. Fix: set the requester's sponsor to a person in your identity manager, or add approve.roles / approve.deciders to the rule",
			cause, in.RuleID)}
	}
	if cause != "" {
		// Roles still carry the record; the failed sponsor half is warn-only.
		s.log.Warn("approval deciders: sponsor unresolved, roles carry the record",
			"rule", in.RuleID, "user", in.Username, "cause", cause)
	}

	now := s.now()
	row := store.Approval{
		SessionID: in.SessionID, UserID: in.UserID, Username: in.Username,
		RuleID: in.RuleID, SetName: in.SetName, ArgvHash: in.ArgvHash, Lane: in.Lane,
		Summary: in.Summary, Justification: in.Justification,
		ApproverRoles: in.Spec.Roles, ApproverUsers: users,
		SelfApproval: in.Spec.SelfApproval,
		Mode:         modeFor(in, requesterDecides),
		State:        string(StatePending), CreatedAt: now,
		ArgsPreview: in.ArgsPreview, ArgsTruncated: in.ArgsTruncated, ArgsBytes: in.ArgsBytes,
		Notify: in.Spec.Notify,
	}
	if in.Spec.Class == policy.ClassTicket {
		// Ticket: ExpiresAt is the day-scale DECISION window (createdAt +
		// ticketTTL) that sweepExpired/ClaimExpired flip past. The blocking-wait
		// knobs are inert, and there is NO grant window yet: a pending ticket is
		// never consumable; the grant materializes only on approval.
		ticketTTL := in.Spec.TicketTTLSeconds
		if ticketTTL <= 0 {
			ticketTTL = defaultTicketTTLSeconds
		}
		// Persist the configured post-approval consume window so Decide can
		// materialize grant_expires_at = decidedAt + grantTTL at approval time.
		// Without it, an approved ticket has no grant deadline
		// and is never consumable.
		grantTTL := in.Spec.GrantTTLSeconds
		if grantTTL <= 0 {
			grantTTL = defaultGrantTTLSeconds
		}
		row.Class = policy.ClassTicket
		row.ExpiresAt = now.Add(time.Duration(ticketTTL) * time.Second)
		row.GrantTTLSeconds = grantTTL
	} else {
		// Hold (default): unchanged (bounded blocking wait + retry-exemption TTL).
		timeout := in.Spec.TimeoutSeconds
		if timeout <= 0 {
			timeout = 90
		}
		ttl := in.Spec.RetryTTLSeconds
		if ttl <= 0 {
			ttl = 60
		}
		row.TimeoutSeconds = timeout
		row.RetryTTLSeconds = ttl
		row.ExpiresAt = now.Add(time.Duration(timeout) * time.Second)
	}
	created, err := s.st.Approvals().Insert(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Lost a race on a partial unique index: idx_approvals_pending_key
			// between two pods of one session, or idx_approvals_pending_ticket_user
			// between two fresh sessions of one user. Either way the winner's
			// row is the one decision that covers this call: adopt it and return it
			// like any other dedupe hit. The loser surfaces no error and, because it
			// returns from here, never pushes or audits a second request.
			if existing, e2 := s.findPendingDedupe(ctx, in, in.ArgvHash); e2 == nil {
				s.log.Debug("approval request attached to pending", "component", "approval", "id", existing.ID, "rule", in.RuleID)
				return recordFromStore(existing), nil
			}
		}
		// The PEP maps any error here to a generic client-facing deny and
		// writes the ONE server-side record for it (a.failClosed: correlation
		// id, lane, rule, this op-wrapped cause). Logging here too would record
		// every fail-closed deny twice.
		return Record{}, fmt.Errorf("approval request-insert: %w", err)
	}
	rec := recordFromStore(created)
	// Debug, not Info, like every state-transition line: per-request volume, ids
	// only, never the justification or summary text.
	s.log.Debug("approval requested", "component", "approval", "id", rec.ID, "lane", in.Lane,
		"rule", in.RuleID, "user", in.Username, "mode", row.Mode, "class", row.Class)
	s.auditPhase(ctx, "request", rec)
	// Announce on every registered channel. Only on a genuinely NEW record;
	// the dedupe and conflict-adopt returns above never reach here, so a retry
	// never re-notifies (the no-spam-on-retry contract, channel-agnostic).
	s.notifyCreated(rec)
	return rec, nil
}

// findPendingDedupe resolves the live pending record a repeat request must
// attach to, scoped the way that class is scoped:
//
//   - hold: session, rule, key (FindPendingByKey). A hold is a bounded
//     blocking wait belonging to one session; two sessions each get their own.
//   - ticket: user, rule, key (FindOpenTicketByUser). A ticket outlives its
//     session and the grant it produces is user-scoped, so a
//     fresh session re-raising the same call attaches to the ticket already in
//     front of a human instead of minting a duplicate and pushing again.
//
// ErrNotFound (nothing to attach to) is the caller's signal to mint.
func (s *Service) findPendingDedupe(ctx context.Context, in RequestInput, key string) (store.Approval, error) {
	if in.Spec.Class == policy.ClassTicket {
		return s.st.Approvals().FindOpenTicketByUser(ctx, in.UserID, in.RuleID, key)
	}
	return s.st.Approvals().FindPendingByKey(ctx, in.SessionID, in.RuleID, key)
}
