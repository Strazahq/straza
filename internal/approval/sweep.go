package approval

import (
	"context"
	"time"
)

// The periodic sweeps Run drives: the expiry flip (fail-closed: an undecided
// record becomes a denial by timing out) and the retention janitor. Both are
// safe to run on every pod; each row is claimed atomically so exactly one pod
// audits and notifies it.

// sweepExpired flips pending-past-expiry records to expired (per-row atomic
// claim), audits each, and wakes any local waiter.
func (s *Service) sweepExpired(ctx context.Context) {
	rows, err := s.st.Approvals().ListExpirable(ctx, s.now(), 256)
	if err != nil {
		s.log.Warn("approval expiry sweep: list failed", "err", err)
		return
	}
	for _, a := range rows {
		at := s.now()
		won, err := s.st.Approvals().ClaimExpired(ctx, a.ID, at)
		if err != nil || !won {
			continue
		}
		// rec mirrors the row the claim wrote (state and decided time) so the
		// terminal notification below carries what a re-read would show.
		rec := recordFromStore(a)
		rec.State = StateExpired
		rec.DecidedAt = &at
		s.auditPhase(ctx, "resolution", rec)
		s.signalWaiters(a.ID)
		// Terminal notification on the sweep's ClaimExpired win (the pod that
		// claimed the row owns the single notification, the same exactly-once rule
		// as Decide).
		s.notifyResolved(rec)
	}
}

// prune deletes terminal records older than the retention horizon.
func (s *Service) prune(ctx context.Context) {
	retention := s.cfg.Retention
	if retention <= 0 {
		retention = 720 * time.Hour
	}
	if n, err := s.st.Approvals().PurgeBefore(ctx, s.now().Add(-retention)); err == nil && n > 0 {
		s.log.Info("approval retention purge", "records", n)
	}
	// Housekeeping for the mobile approver surface: expired challenges and
	// enroll tokens are already refused at use (the SQL guards check expiry), so
	// this is cleanup, not correctness. Prune anything already past its short TTL.
	cutoff := s.now()
	if n, err := s.st.Approvers().PurgeChallengesBefore(ctx, cutoff); err == nil && n > 0 {
		s.log.Info("approver challenge purge", "challenges", n)
	}
	if n, err := s.st.Approvers().PurgeEnrollTokensBefore(ctx, cutoff); err == nil && n > 0 {
		s.log.Info("approver enroll-token purge", "tokens", n)
	}
}
