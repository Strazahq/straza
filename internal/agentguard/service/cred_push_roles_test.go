package service

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestCredPushRenewsOnNudge pins what the service does with the events of
// its push stream that are not a revocation: the role nudge and the policy
// nudge each renew the session once, the renewal adopts the roles the
// server resolves now, and an event this service does not know does
// nothing. None of them revokes the window.
func TestCredPushRenewsOnNudge(t *testing.T) {
	for _, tc := range []struct {
		name, events  string
		wantRefreshes int
	}{
		{"the role nudge", "roles", 1},
		{"the policy nudge", "policy", 1},
		{"an event of a later server, then the role nudge", "future,roles", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			first, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			r.waitStream(t, alice, "w1")
			want := []string{"auditor", "dev"}
			r.srv.with(func() { r.srv.roles, r.srv.refreshes = want, 0 })

			r.srv.pushEvents <- tc.events
			waitFor(t, "the renewal the nudge asks for", func() bool {
				ses, err := r.s.Session(context.Background(), alice, first.Harness, false)
				return err == nil && slices.Equal(ses.Roles, want)
			})
			var refreshes int
			r.srv.with(func() { refreshes = r.srv.refreshes })
			if refreshes != tc.wantRefreshes {
				t.Errorf("%d refreshes, want %d", refreshes, tc.wantRefreshes)
			}
			for _, ss := range r.s.Status(alice).Sessions {
				if ss.Revoked != "" {
					t.Errorf("the nudge revoked the window: %q", ss.Revoked)
				}
			}
		})
	}
}

// TestCredPushBurstRenewsAtMostTwice pins the skip of nudges a renewal
// already covers: five nudges in one burst cost the renewal the first one
// starts and at most one more, because a renewal that started after a
// nudge arrived and got its answer already carries what that nudge
// announced. The server holds that first renewal until all five arrived. A
// burst that lasts longer than a renewal costs one more for each renewal a
// nudge arrives during, which this test does not reach.
func TestCredPushBurstRenewsAtMostTwice(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	first, err := r.start(alice, "w1")
	if err != nil {
		t.Fatal(err)
	}
	r.waitStream(t, alice, "w1")
	hold := make(chan struct{})
	want := []string{"auditor", "dev"}
	r.srv.with(func() { r.srv.roles, r.srv.refreshes, r.srv.refreshHold = want, 0, hold })

	r.srv.pushEvents <- "roles,roles,roles,roles,roles"
	count := func() (n int) {
		r.srv.with(func() { n = r.srv.refreshes })
		return n
	}
	waitFor(t, "the first renewal to reach the server", func() bool { return count() >= 1 })
	// The five events left the server in one write, so the service has read
	// them all well before this wait ends, with the first renewal still held.
	time.Sleep(100 * time.Millisecond)
	close(hold)
	waitFor(t, "the renewed roles", func() bool {
		ses, err := r.s.Session(context.Background(), alice, first.Harness, false)
		return err == nil && slices.Equal(ses.Roles, want)
	})
	// A renewal that is still owed starts within this wait, so a count
	// read after it is final.
	time.Sleep(300 * time.Millisecond)
	if n := count(); n < 1 || n > 2 {
		t.Fatalf("five nudges in one burst cost %d renewals, want one or two", n)
	}
}
