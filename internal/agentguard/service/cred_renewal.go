package service

import (
	"context"
	"sync/atomic"
)

// renewCount lets a push event tell whether a renewal that started after
// the event arrived has got its answer. started counts the renewals a
// session began, and covered is the count of the last one the server
// answered.
type renewCount struct {
	started, covered atomic.Uint64
}

// renewNow renews hs once, by a refresh or a re-acquire as
// refreshOrReacquire decides, and counts it for the push events of hs. The
// caller holds hs.lock.
func (s *credStore) renewNow(ctx context.Context, a *account, hs *heldSession) (bool, error) {
	n := hs.renewals.started.Add(1)
	reacquired, err := s.refreshOrReacquire(ctx, a, hs)
	if err == nil {
		hs.renewals.covered.Store(n)
	}
	return reacquired, err
}

// renewAfter renews hs for a push event that arrived when hs had started
// seen renewals. It does nothing when a renewal that started later has got
// its answer, because that renewal asked the server after the event and
// adopted what the event announced. So the events that arrive while one
// renewal runs cost that renewal and one more, and a burst that lasts
// longer than a renewal costs one more for each renewal an event arrives
// during.
func (s *credStore) renewAfter(ctx context.Context, a *account, hs *heldSession, seen uint64) {
	if err := hs.lock.lock(ctx); err != nil {
		return
	}
	defer hs.lock.unlock()
	if hs.renewals.covered.Load() > seen {
		return
	}
	class, detail := renewalClass(s.renewNow(ctx, a, hs))
	s.recordRenewal(a, hs, class, detail)
}
