package approval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// What a resolved approval entitles a later call to. A hold's approval runs
// one call of the session that raised it: the held call itself, or one
// identical retry inside retryTTLSeconds. A ticket's approval materializes a
// grant that one later call of the same person uses. Both uses are one
// atomic write in the store, so exactly one caller on any replica wins, and
// every win writes a consumed audit record. The deny-final window stops a
// refused ticket from being re-asked. All of these are PEP reads and all of
// them fail closed.

// ConsumeGrant is the ticket-class analog of ConsumeHold, but session-agnostic:
// it resolves the live approved grant bound to (userID,
// argvHash) and atomically consumes it once. A hit returns (grant, true, nil)
// with the "consumed" audit emitted (carrying the consuming session); no
// grant, an already-consumed grant, an expired consume window, or losing the
// single-use race all return (_, false, nil). A store error returns
// (_, false, err) so the PEP fails closed. Grants follow the human, so a fresh
// session consumes one an earlier session raised (plan-gate).
func (s *Service) ConsumeGrant(ctx context.Context, userID, argvHash, consumingSession string) (Record, bool, error) {
	now := s.now()
	g, err := s.st.Approvals().FindConsumableGrant(ctx, userID, argvHash, now)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Record{}, false, nil // no live grant → PEP denies (with/without a ticket)
		}
		return Record{}, false, fmt.Errorf("approval grant-find: %w", err) // fail closed; the PEP logs once with the correlation id
	}
	rec, won, err := s.use(ctx, recordFromStore(g), consumingSession, now, now)
	if err != nil {
		return Record{}, false, fmt.Errorf("approval grant-consume: %w", err) // fail closed; the PEP logs once
	}
	return rec, won, nil // !won: lost the single-use race, or window closed under us
}

// DeniedTicketWithinWindow reports whether the LATEST ticket for (userID,
// ruleID, argvHash) is a denial still inside its original decision window
// (ExpiresAt > now), a terminal deny-final. On a hit it returns (record,
// true, nil) and the PEP denies the call outright WITHOUT opening a fresh
// ticket: a human already said no, and re-asking within the same window would
// spam the approvers. It returns (_, false, nil) when there is no ticket, or
// the latest is not a within-window denial (an approved/pending/expired ticket,
// or a denial whose window has passed; those fall back to the normal
// open-a-fresh-ticket path). A store error returns (_, false, err) so the PEP
// fails closed. Checked AFTER ConsumeGrant, so a still-live grant from an
// earlier approval always wins over a later denial.
func (s *Service) DeniedTicketWithinWindow(ctx context.Context, userID, ruleID, argvHash string) (Record, bool, error) {
	latest, err := s.st.Approvals().FindLatestTicketByKey(ctx, userID, ruleID, argvHash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Record{}, false, nil // no prior ticket → open a fresh one
		}
		return Record{}, false, fmt.Errorf("approval denied-ticket-find: %w", err) // fail closed; the PEP logs once
	}
	if latest.State == string(StateDenied) && s.now().Before(latest.ExpiresAt) {
		return recordFromStore(latest), true, nil
	}
	return Record{}, false, nil
}

// ConsumeHold uses the approval of a hold for a retry of the same call: the
// approved, unused hold of (userID, sessionID, ruleID, argvHash) whose use
// deadline, decidedAt plus retryTTLSeconds, is still ahead. A win returns
// (record, true, nil) with the consumed audit record written. No such
// approval, or losing the one atomic use to a caller on any replica, returns
// (_, false, nil), and the PEP raises a new request. A store error returns
// (_, false, err) so the PEP denies fail-closed and logs it once.
func (s *Service) ConsumeHold(ctx context.Context, userID, sessionID, ruleID, argvHash string) (Record, bool, error) {
	now := s.now()
	a, err := s.st.Approvals().FindConsumableHold(ctx, userID, sessionID, ruleID, argvHash, now)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("approval hold-find: %w", err)
	}
	rec, won, err := s.use(ctx, recordFromStore(a), sessionID, now, now)
	if err != nil {
		return Record{}, false, fmt.Errorf("approval hold-use: %w", err)
	}
	return rec, won, nil
}

// ConsumeHeld uses the approval a held call waited for, by the record's id,
// so the consumed record names exactly that approval and rec.SessionID as the
// session that used it. The call was already waiting when the approval
// landed, so its use is judged at the decision time: a held call that wakes
// late, on its own timer after a lost broadcast or on a replica whose clock
// runs ahead, still runs. It reports false for a record that is not a
// decided hold, a ticket included, for an approval an older build decided
// without a use deadline, and when the approval was already used by another
// held call attached to the same request or by a retry. A store error
// returns (false, err) so the PEP denies fail-closed.
func (s *Service) ConsumeHeld(ctx context.Context, rec Record) (bool, error) {
	if rec.Class != policy.ClassHold || rec.DecidedAt == nil {
		return false, nil
	}
	_, won, err := s.use(ctx, rec, rec.SessionID, s.now(), *rec.DecidedAt)
	if err != nil {
		return false, fmt.Errorf("approval held-use: %w", err)
	}
	return won, nil
}

// use is the one gate every approval use passes: the atomic MarkConsumed,
// which at most one caller on any replica wins with the use window judged at
// asOf, and on the win the consumed audit record naming the approval, the
// session that used it and now, the real time of the use. The write and the
// record run on a context the caller cannot cancel: a client that leaves
// while the write is in flight could otherwise spend the approval and get
// back an error, and no record would name the use. The state stays
// approved, because a use is a new fact and not a new decision.
func (s *Service) use(ctx context.Context, rec Record, session string, now, asOf time.Time) (Record, bool, error) {
	ctx = context.WithoutCancel(ctx)
	won, err := s.st.Approvals().MarkConsumed(ctx, rec.ID, session, now, asOf)
	if err != nil || !won {
		return Record{}, false, err
	}
	rec.ConsumedBy = session
	rec.ConsumedAt = &now
	s.auditPhase(ctx, "consumed", rec)
	return rec, true, nil
}
