package store

import (
	"context"
	"time"
)

// LeaseRepo hands out named leases, so a background job that must run on
// one replica at a time runs on the replica that holds its lease.
type LeaseRepo interface {
	// Acquire gives the lease name to holder until the time until, and
	// answers whether holder holds it now. It succeeds when nobody holds
	// the lease, when holder already holds it, which renews it, or when the
	// last holder's time is before now. A lease is no fence: a holder that
	// runs past its time may overlap the next one, so the job's deadline
	// must be shorter than the lease.
	Acquire(ctx context.Context, name, holder string, now, until time.Time) (bool, error)
	// Release gives the lease name up when holder holds it, and does
	// nothing otherwise.
	Release(ctx context.Context, name, holder string) error
}

type leaseRepo struct{ s *sqlStore }

// Leases answers the lease repo over the same sqlStore.
func (s *sqlStore) Leases() LeaseRepo { return leaseRepo{s} }

func (r leaseRepo) Acquire(ctx context.Context, name, holder string, now, until time.Time) (bool, error) {
	res, err := r.s.exec(ctx, `INSERT INTO leases (name, holder, until) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET holder = excluded.holder, until = excluded.until
		WHERE leases.holder = excluded.holder OR leases.holder = '' OR leases.until IS NULL
			OR `+r.s.timeAfter("$4", "leases.until"),
		name, holder, r.s.tArg(until), r.s.tArg(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r leaseRepo) Release(ctx context.Context, name, holder string) error {
	_, err := r.s.exec(ctx, `UPDATE leases SET holder = '', until = NULL WHERE name = $1 AND holder = $2`, name, holder)
	return err
}
