package server

import (
	"context"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// Leases of the background jobs that run on one replica at a time.
const (
	leaseOAuthRefresh = "oauth-refresh"
	leaseEntraSync    = "entra-sync"
)

// leaseSlack is how long a lease outlasts the interval of its job, so the
// holder renews it at its next tick before another replica may take it.
// leaseMargin is how long before the lease's end a pass is cut off, so a
// slow pass never runs beside the next holder's.
const (
	leaseSlack  = 90 * time.Second
	leaseMargin = 30 * time.Second
)

// leasedPass runs pass when holder holds, or can take, the lease name at
// the time now, and answers whether it ran. The lease lasts one interval
// and leaseSlack, and pass gets a deadline leaseMargin before that. A
// replica that cannot read the lease runs nothing, because the job must not
// run twice.
func leasedPass(ctx context.Context, leases store.LeaseRepo, name, holder string, now time.Time, interval time.Duration, pass func(context.Context)) bool {
	hold := interval + leaseSlack
	held, err := leases.Acquire(ctx, name, holder, now, now.Add(hold))
	if err != nil || !held {
		return false
	}
	pctx, cancel := context.WithTimeout(ctx, hold-leaseMargin)
	defer cancel()
	pass(pctx)
	return true
}

// runLeased runs pass on this replica every interval while it holds the
// lease name, with a first pass at once, and gives the lease up when ctx
// ends so another replica takes over without waiting for it to lapse.
func (a *App) runLeased(ctx context.Context, name string, interval time.Duration, pass func(context.Context)) {
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = a.store.Leases().Release(rctx, name, a.instance)
	}()
	for {
		leasedPass(ctx, a.store.Leases(), name, a.instance, time.Now(), interval, pass)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
