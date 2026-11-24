package server

import (
	"context"
	"testing"
	"time"
)

// TestLeasedPass pins that of two replicas only the one that holds a job's
// lease runs its pass, that the other takes over once the holder stops
// renewing, and that a pass gets a deadline before the lease ends.
func TestLeasedPass(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	t0 := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	const interval = time.Minute
	steps := []struct {
		name   string
		holder string
		after  time.Duration
		want   bool
	}{
		{"the first replica runs", "strazad/a", 0, true},
		{"the second replica does not", "strazad/b", time.Second, false},
		{"the first replica runs again", "strazad/a", interval, true},
		{"the second still does not, a tick later", "strazad/b", 2 * interval, false},
		{"the second takes over after the holder's lease ended", "strazad/b", 2*interval + leaseSlack + time.Second, true},
		{"the first is refused now", "strazad/a", 2*interval + leaseSlack + 2*time.Second, false},
	}
	for _, st := range steps {
		now := t0.Add(st.after)
		ran := false
		got := leasedPass(ctx, app.store.Leases(), "test-job", st.holder, now, interval, func(pctx context.Context) {
			ran = true
			deadline, ok := pctx.Deadline()
			if !ok {
				t.Errorf("%s: the pass has no deadline", st.name)
			}
			if lease := time.Now().Add(interval + leaseSlack); !deadline.Before(lease) {
				t.Errorf("%s: the pass may run until %v, at or past its lease of about %v", st.name, deadline, lease)
			}
		})
		if got != st.want || ran != st.want {
			t.Errorf("%s: leasedPass = %v, pass ran = %v, want %v", st.name, got, ran, st.want)
		}
	}
}
