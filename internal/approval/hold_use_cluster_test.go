package approval

import (
	"context"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// cluster is two replicas over two store handles of one database, each
// subscribed to one bus the way Run subscribes it, with nova as the
// requester and kim as an approver.
type cluster struct {
	a, b      *Service
	st        store.Store
	nova, kim store.User
}

// newCluster builds a cluster on driver over bus. The Postgres database is
// the test's own, so the shared test database is never touched.
func newCluster(t *testing.T, driver string, bus coreBus) *cluster {
	t.Helper()
	stA, stB := openReplicaStores(t, driver)
	res := &fakeResolver{roles: map[string][]store.Role{}}
	c := &cluster{a: serviceOn(stA, bus, res), b: serviceOn(stB, bus, res), st: stA}
	for _, s := range []*Service{c.a, c.b} {
		if _, err := bus.SubscribeCore(resolvedSubjectPrefix+"*", s.handleResolved); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	var err error
	if c.nova, err = stA.Users().Create(ctx, store.User{Username: "nova", Email: "nova@x.io"}); err != nil {
		t.Fatal(err)
	}
	if c.kim, err = stA.Users().Create(ctx, store.User{Username: "kim", Email: "kim@x.io"}); err != nil {
		t.Fatal(err)
	}
	res.roles[c.kim.ID] = []store.Role{{ID: "role-sec", Name: "sec-approvers"}}
	return c
}

// dropBus loses every resolution broadcast, standing for core NATS while it
// reconnects, so a held call wakes only on its own timer.
type dropBus struct{}

func (dropBus) PublishCore(string, []byte) error { return nil }

func (dropBus) SubscribeCore(string, func(string, []byte)) (func(), error) {
	return func() {}, nil
}

// TestHeldCallRunsWhenItWakesLate pins that a held call runs on the approval
// it waited for however late it wakes, because the call was already waiting
// when the approval landed: when the resolution broadcast is lost and the
// call wakes on its own timer after the retry window, and when the holding
// replica's clock runs ahead of the deciding one by more than that window.
// The one use records the real time, and no retry uses the approval after it.
func TestHeldCallRunsWhenItWakesLate(t *testing.T) {
	cases := []struct {
		name              string
		bus               func() coreBus
		timeout, retryTTL int
		skew              time.Duration
	}{
		{"the broadcast is lost and the call wakes on its timer", func() coreBus { return dropBus{} }, 3, 1, 0},
		{"the holding replica's clock runs ahead", func() coreBus { return newFakeBus() }, 90, 5, 6 * time.Second},
	}
	for _, driver := range []string{config.DriverSQLite, config.DriverPostgres} {
		for _, tc := range cases {
			t.Run(driver+"/"+tc.name, func(t *testing.T) {
				c := newCluster(t, driver, tc.bus())
				holder, decider := c.a, c.b
				holder.now = func() time.Time { return time.Now().Add(tc.skew) }
				ctx := context.Background()
				spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: tc.timeout, RetryTTLSeconds: tc.retryTTL}
				in := req("s-1", c.nova.ID, "nova", spec)
				in.Lane = "gateway"
				rec, err := holder.Request(ctx, in)
				if err != nil {
					t.Fatalf("Request: %v", err)
				}
				woke := make(chan Record, 1)
				go func() {
					r, err := holder.Await(ctx, rec.ID)
					if err != nil {
						t.Errorf("Await: %v", err)
					}
					woke <- r
				}()
				waitForWaiters(t, holder, rec.ID, 1)
				if _, err := decider.Decide(ctx, rec.ID, "approved", c.kim.ID, "console", "the change window is open", ""); err != nil {
					t.Fatalf("Decide: %v", err)
				}
				r := <-woke
				if won, err := holder.ConsumeHeld(ctx, r); err != nil || !won {
					t.Fatalf("held call = %v, %v, want it to run on the approval it waited for", won, err)
				}
				for _, s := range []*Service{holder, decider} {
					if _, ok, err := s.ConsumeHold(ctx, c.nova.ID, "s-1", "r-1", "sha256:k1"); ok || err != nil {
						t.Fatalf("retry after the held call = %v, %v, want no second run", ok, err)
					}
				}
				assertOneUse(t, c.st, r, c.nova.ID, "s-1")
				row, err := c.st.Approvals().GetByID(ctx, rec.ID)
				if err != nil || row.ConsumedAt == nil || row.GrantExpiresAt == nil || !row.ConsumedAt.After(*row.GrantExpiresAt) {
					t.Errorf("consumed_at = %v with the retry window ending %v, %v, want the real time of the late use", row.ConsumedAt, row.GrantExpiresAt, err)
				}
			})
		}
	}
}

// TestConsumeHeldRefusesOtherRecords pins that a held call uses only an
// approved hold that carries a use deadline: a ticket its request attached
// to after the rule changed class keeps its grant for a later call, a hold
// an older build approved without a deadline is never used, and a pending
// hold is not either. None of them writes a consumed record.
func TestConsumeHeldRefusesOtherRecords(t *testing.T) {
	ctx := context.Background()
	ticket := policy.ApproveSpec{Roles: []string{"sec-approvers"}, Class: policy.ClassTicket}
	cases := []struct {
		name      string
		record    func(h *harness, nova, kim store.User) Record
		grantLeft bool
	}{
		{"a ticket the held call attached to after the rule changed class", func(h *harness, nova, kim store.User) Record {
			tk, err := h.svc.Request(ctx, req("s-1", nova.ID, "nova", ticket))
			if err != nil {
				t.Fatalf("Request ticket: %v", err)
			}
			if held, err := h.svc.Request(ctx, req("s-1", nova.ID, "nova", holdSpec())); err != nil || held.ID != tk.ID {
				t.Fatalf("held call attached to %s, %v, want the pending ticket %s", held.ID, err, tk.ID)
			}
			out, err := h.svc.Decide(ctx, tk.ID, "approved", kim.ID, "console", "", "")
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			return out
		}, true},
		{"a hold an older build approved with no use deadline", func(h *harness, nova, kim store.User) Record {
			rec, err := h.svc.Request(ctx, req("s-1", nova.ID, "nova", holdSpec()))
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			if won, err := h.st.Approvals().MarkDecided(ctx, rec.ID, "approved", kim.ID, "kim", "console", time.Now(), nil, "", ""); err != nil || !won {
				t.Fatalf("MarkDecided = %v, %v", won, err)
			}
			out, err := h.svc.Get(ctx, rec.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			return out
		}, false},
		{"a pending hold", func(h *harness, nova, _ store.User) Record {
			rec, err := h.svc.Request(ctx, req("s-1", nova.ID, "nova", holdSpec()))
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			return rec
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			nova, kim := h.seedUser(t, "nova"), h.seedUser(t, "kim", "sec-approvers")
			rec := tc.record(h, nova, kim)
			if won, err := h.svc.ConsumeHeld(ctx, rec); won || err != nil {
				t.Fatalf("ConsumeHeld = %v, %v, want the held call refused", won, err)
			}
			if n := len(consumedRecords(t, h.st)); n != 0 {
				t.Fatalf("consumed records = %d, want 0", n)
			}
			if !tc.grantLeft {
				return
			}
			if _, ok, err := h.svc.ConsumeGrant(ctx, nova.ID, "sha256:k1", "s-2"); !ok || err != nil {
				t.Fatalf("ticket grant after the refused held call = %v, %v, want it still usable", ok, err)
			}
		})
	}
}

// cancelingUse cancels the caller's context around the use write: as the
// write starts, standing for a client that disconnects while the write is in
// flight, or right after it returns, standing for one that disconnects as
// its approval is spent.
type cancelingUse struct {
	store.Store
	cancel func()
	during bool
}

func (f cancelingUse) Approvals() store.ApprovalRepo {
	return cancelingUseApprovals{f.Store.Approvals(), f.cancel, f.during}
}

type cancelingUseApprovals struct {
	store.ApprovalRepo
	cancel func()
	during bool
}

func (a cancelingUseApprovals) MarkConsumed(ctx context.Context, id, consumedBy string, now, asOf time.Time) (bool, error) {
	if a.during {
		a.cancel()
	}
	won, err := a.ApprovalRepo.MarkConsumed(ctx, id, consumedBy, now, asOf)
	a.cancel()
	return won, err
}

// TestUseOutlivesTheCaller pins that a use the server has begun ends the
// same way whatever the client does: when the caller's context is cancelled
// during the use write or right after it, the use still wins and writes its
// one consumed record, for a retry and for the held call, on both drivers.
// So an approval is never spent without its record because a client left.
func TestUseOutlivesTheCaller(t *testing.T) {
	uses := []struct {
		name string
		use  func(ctx context.Context, s *Service, rec Record, userID string) (bool, error)
	}{
		{"a retry", func(ctx context.Context, s *Service, _ Record, userID string) (bool, error) {
			_, ok, err := s.ConsumeHold(ctx, userID, "s-1", "r-1", "sha256:k1")
			return ok, err
		}},
		{"the held call", func(ctx context.Context, s *Service, rec Record, _ string) (bool, error) {
			return s.ConsumeHeld(ctx, rec)
		}},
	}
	for _, driver := range []string{config.DriverSQLite, config.DriverPostgres} {
		for _, u := range uses {
			for _, during := range []bool{true, false} {
				when := "cancelled right after the write"
				if during {
					when = "cancelled during the write"
				}
				t.Run(driver+"/"+u.name+"/"+when, func(t *testing.T) {
					c := newCluster(t, driver, newFakeBus())
					bg := context.Background()
					rec, err := c.a.Request(bg, req("s-1", c.nova.ID, "nova", holdSpec()))
					if err != nil {
						t.Fatalf("Request: %v", err)
					}
					approved, err := c.a.Decide(bg, rec.ID, "approved", c.kim.ID, "console", "the change window is open", "")
					if err != nil {
						t.Fatalf("Decide: %v", err)
					}
					ctx, cancel := context.WithCancel(bg)
					defer cancel()
					c.a.st = cancelingUse{Store: c.st, cancel: cancel, during: during}
					if won, err := u.use(ctx, c.a, approved, c.nova.ID); err != nil || !won {
						t.Fatalf("use = %v, %v, want it to win whatever the client does", won, err)
					}
					assertOneUse(t, c.st, approved, c.nova.ID, "s-1")
				})
			}
		}
	}
}
