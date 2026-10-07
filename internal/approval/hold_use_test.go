package approval

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// holdSpec is a normalized hold with a 60 second use window.
func holdSpec() policy.ApproveSpec {
	return policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
}

// serviceOn builds a Service over st and bus the way New does, standing for
// one replica; a second call on the same store is a second replica.
func serviceOn(st store.Store, bus coreBus, res RoleResolver) *Service {
	return &Service{
		st: st, bus: bus, resolver: res, cfg: config.Approval{}, log: slog.Default(),
		tokenKey: make([]byte, 32), now: time.Now,
		waiters: map[string][]chan struct{}{},
	}
}

// approveHold raises the hold of (s-1, r-1, sha256:k1) for nova and has kim
// approve it with a reason. It returns the approved record and nova's id.
func (h *harness) approveHold(t *testing.T) (Record, string) {
	t.Helper()
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", holdSpec()))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	out, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "the change window is open", "")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return out, requester.ID
}

// consumedRecords returns the data of every consumed approval record in the
// outbox, oldest first.
func consumedRecords(t *testing.T, st store.Store) []map[string]any {
	t.Helper()
	evs, err := st.Outbox().ListUnpublished(context.Background(), 1000)
	if err != nil {
		t.Fatalf("Outbox: %v", err)
	}
	var out []map[string]any
	for _, e := range evs {
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if e.Subject != auditSubject || json.Unmarshal([]byte(e.CE), &ce) != nil {
			continue
		}
		if ce.Data["phase"] == "consumed" {
			out = append(out, ce.Data)
		}
	}
	return out
}

// assertOneUse fails unless exactly one consumed record exists and it names
// the person, the session that used the approval, the approval and its
// call, the approved state, the decider and the decider's reason.
func assertOneUse(t *testing.T, st store.Store, rec Record, userID, session string) {
	t.Helper()
	recs := consumedRecords(t, st)
	if len(recs) != 1 {
		t.Fatalf("consumed records = %d, want exactly 1: %v", len(recs), recs)
	}
	got := recs[0]
	want := map[string]any{
		"approvalId": rec.ID, "user": userID, "session": rec.SessionID, "consumedBy": session,
		"rule": "r-1", "summary": "mcp.call midpoint:disable_user", "state": "approved",
		"decidedBy": rec.DecidedBy, "decidedReason": "the change window is open",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("consumed record %s = %v, want %v", k, got[k], v)
		}
	}
	at, err := time.Parse(time.RFC3339, fmt.Sprint(got["consumedAt"]))
	if err != nil || at.Before(rec.DecidedAt.Add(-time.Second)) {
		t.Errorf("consumedAt = %v, want a time at or after the decision %v", got["consumedAt"], rec.DecidedAt)
	}
}

// TestHoldApprovalRunsOnce pins that one approval of a hold runs one call:
// the held call whenever it wakes, or one identical retry of the same session
// inside the use window, whichever uses it first, on this replica or on a
// restarted one.
// Every other use misses, so that call raises a new request. The first case
// pins that the identical held call never runs a second time.
func TestHoldApprovalRunsOnce(t *testing.T) {
	type use func(h *harness, rec Record, userID string) (bool, error)
	ctx := context.Background()
	held := func(h *harness, rec Record, _ string) (bool, error) { return h.svc.ConsumeHeld(ctx, rec) }
	retry := func(session string) use {
		return func(h *harness, _ Record, userID string) (bool, error) {
			_, ok, err := h.svc.ConsumeHold(ctx, userID, session, "r-1", "sha256:k1")
			return ok, err
		}
	}
	restarted := func(h *harness, _ Record, userID string) (bool, error) {
		_, ok, err := serviceOn(h.st, newFakeBus(), h.resolver).ConsumeHold(ctx, userID, "s-1", "r-1", "sha256:k1")
		return ok, err
	}
	cases := []struct {
		name    string
		uses    []use
		want    []bool
		usedBy  string
		expired bool
	}{
		{"the held call runs and the identical call after it asks again", []use{held, retry("s-1")}, []bool{true, false}, "s-1", false},
		{"a retry after the pending answer runs once and the next one asks again", []use{retry("s-1"), retry("s-1")}, []bool{true, false}, "s-1", false},
		{"a retry that uses it first leaves the held call nothing", []use{retry("s-1"), held}, []bool{true, false}, "s-1", false},
		{"another session of the same person cannot use it", []use{retry("s-2"), retry("s-1")}, []bool{false, true}, "s-1", false},
		{"a restarted replica uses it once from the database", []use{restarted, retry("s-1")}, []bool{true, false}, "s-1", false},
		{"past its window a retry cannot use it", []use{retry("s-1")}, []bool{false}, "", true},
		{"past the retry window the held call still runs, once", []use{held, retry("s-1")}, []bool{true, false}, "s-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec, userID := h.approveHold(t)
			if tc.expired {
				h.svc.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
			}
			for i, u := range tc.uses {
				got, err := u(h, rec, userID)
				if err != nil || got != tc.want[i] {
					t.Fatalf("use %d = %v, %v, want %v", i, got, err, tc.want[i])
				}
			}
			if tc.usedBy == "" {
				if n := len(consumedRecords(t, h.st)); n != 0 {
					t.Fatalf("consumed records = %d, want 0 for an approval nobody used", n)
				}
				return
			}
			assertOneUse(t, h.st, rec, userID, tc.usedBy)
		})
	}
}

// TestDecideStampsHoldUseDeadline pins the use window a hold approval gets
// in the same write that approves it: decidedAt plus the record's
// retryTTLSeconds, 60 seconds for a row that carries none, and no window at
// all for a denial.
func TestDecideStampsHoldUseDeadline(t *testing.T) {
	cases := []struct {
		name     string
		retryTTL int
		verdict  string
		want     time.Duration
	}{
		{"an approval gets the rule's retry window", 60, "approved", 60 * time.Second},
		{"a short retry window is kept", 5, "approved", 5 * time.Second},
		{"a row with no retry window gets the default", 0, "approved", 60 * time.Second},
		{"a denial gets no window", 60, "denied", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			requester := h.seedUser(t, "nova")
			approver := h.seedUser(t, "kim", "sec-approvers")
			now := time.Now().UTC()
			row, err := h.st.Approvals().Insert(ctx, store.Approval{
				SessionID: "s-1", UserID: requester.ID, Username: "nova", RuleID: "r-1", ArgvHash: "sha256:k1",
				Lane: "hook", State: "pending", ApproverRoles: []string{"sec-approvers"},
				TimeoutSeconds: 90, RetryTTLSeconds: tc.retryTTL, CreatedAt: now, ExpiresAt: now.Add(90 * time.Second),
			})
			if err != nil {
				t.Fatalf("Insert: %v", err)
			}
			out, err := h.svc.Decide(ctx, row.ID, tc.verdict, approver.ID, "console", "", "")
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if tc.want == 0 {
				if out.GrantExpiresAt != nil {
					t.Fatalf("use deadline = %v, want none", out.GrantExpiresAt)
				}
				return
			}
			if out.GrantExpiresAt == nil || out.DecidedAt == nil || out.GrantExpiresAt.Sub(*out.DecidedAt) != tc.want {
				t.Fatalf("use deadline = %v after the decision %v, want %s after it", out.GrantExpiresAt, out.DecidedAt, tc.want)
			}
		})
	}
}

// TestTwoHeldCallsOneRun pins that two identical calls held at the same
// moment, which attach to one request, get one run from one approval: both
// wake approved, exactly one uses the approval, and the other is told no.
func TestTwoHeldCallsOneRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	in := req("s-1", requester.ID, "nova", holdSpec())
	in.Lane = "gateway"
	first, err := h.svc.Request(ctx, in)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	second, err := h.svc.Request(ctx, in)
	if err != nil || second.ID != first.ID {
		t.Fatalf("second identical call attached to %s, %v, want %s", second.ID, err, first.ID)
	}

	wins := make(chan bool, 2)
	for _, id := range []string{first.ID, second.ID} {
		go func() {
			rec, err := h.svc.Await(ctx, id)
			if err != nil || rec.State != StateApproved {
				t.Errorf("Await = %s, %v, want approved", rec.State, err)
				wins <- false
				return
			}
			won, err := h.svc.ConsumeHeld(ctx, rec)
			if err != nil {
				t.Errorf("ConsumeHeld: %v", err)
			}
			wins <- won
		}()
	}
	waitForWaiters(t, h.svc, first.ID, 2)
	if _, err := h.svc.Decide(ctx, first.ID, "approved", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	n := 0
	for range 2 {
		if <-wins {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("held calls that ran = %d, want exactly 1", n)
	}
	if got := len(consumedRecords(t, h.st)); got != 1 {
		t.Errorf("consumed records = %d, want 1", got)
	}
}

// waitForWaiters blocks until id has n registered Await waiters.
func waitForWaiters(t *testing.T, s *Service, id string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		got := len(s.waiters[id])
		s.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiters on %s = %d, want %d", id, got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestHoldApprovalOneWinnerAcrossReplicas pins the cluster-wide use in a
// two-replica topology: two replicas on one database and one bus, both
// subscribed to the resolution broadcast, a call held on one replica, the
// decision taken on the other, and retries sent to both at once. Exactly one
// of the held call and the retries runs, on SQLite and on Postgres when
// STRAZA_TEST_POSTGRES_DSN names a server.
func TestHoldApprovalOneWinnerAcrossReplicas(t *testing.T) {
	for _, driver := range []string{config.DriverSQLite, config.DriverPostgres} {
		t.Run(driver, func(t *testing.T) {
			c := newCluster(t, driver, newFakeBus())
			ctx := context.Background()
			in := req("s-1", c.nova.ID, "nova", holdSpec())
			in.Lane = "gateway"
			rec, err := c.a.Request(ctx, in)
			if err != nil {
				t.Fatalf("Request: %v", err)
			}

			const racers = 8
			wins := make(chan bool, racers+1)
			go func() {
				r, err := c.a.Await(ctx, rec.ID)
				if err != nil {
					t.Errorf("Await: %v", err)
				}
				won, err := c.a.ConsumeHeld(ctx, r)
				if err != nil {
					t.Errorf("ConsumeHeld: %v", err)
				}
				wins <- won
			}()
			waitForWaiters(t, c.a, rec.ID, 1)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range racers {
				replica := []*Service{c.a, c.b}[i%2]
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, ok, err := replica.ConsumeHold(ctx, c.nova.ID, "s-1", "r-1", "sha256:k1")
					if err != nil {
						t.Errorf("ConsumeHold: %v", err)
					}
					wins <- ok
				}()
			}
			if _, err := c.b.Decide(ctx, rec.ID, "approved", c.kim.ID, "console", "", ""); err != nil {
				t.Fatalf("Decide: %v", err)
			}
			close(start)
			wg.Wait()
			n := 0
			for range racers + 1 {
				if <-wins {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("calls that ran across two replicas = %d, want exactly 1", n)
			}
			if got := len(consumedRecords(t, c.st)); got != 1 {
				t.Errorf("consumed records = %d, want 1", got)
			}
		})
	}
}

// openReplicaStores opens two store handles on one migrated database. The
// Postgres database is created for the test and dropped after it, so the
// shared test database is never touched; without the DSN it skips.
func openReplicaStores(t *testing.T, driver string) (store.Store, store.Store) {
	t.Helper()
	var cfg config.Store
	switch driver {
	case config.DriverSQLite:
		path := filepath.Join(t.TempDir(), "replicas.db")
		storetest.SeedSQLite(t, path)
		cfg = config.Store{Driver: config.DriverSQLite, DSN: path}
	default:
		base := os.Getenv("STRAZA_TEST_POSTGRES_DSN")
		if base == "" {
			t.Skip("STRAZA_TEST_POSTGRES_DSN not set")
		}
		admin, err := sql.Open("pgx", base)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = admin.Close() })
		name := fmt.Sprintf("straza_approval_%d", time.Now().UnixNano())
		if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
			t.Fatalf("create database: %v", err)
		}
		t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE " + name + " WITH (FORCE)") })
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/" + name
		cfg = config.Store{Driver: config.DriverPostgres, DSN: u.String()}
	}
	open := func() store.Store {
		st, err := store.Open(config.Config{Store: cfg})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		return st
	}
	first := open()
	return first, open()
}

// holdFaultStore fails the hold lookup or the use write while armed, standing
// for a database that stopped answering at the use step.
type holdFaultStore struct {
	store.Store
	mu              sync.Mutex
	findErr, useErr error
}

func (f *holdFaultStore) arm(find, use error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findErr, f.useErr = find, use
}

func (f *holdFaultStore) Approvals() store.ApprovalRepo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return holdFaultApprovals{f.Store.Approvals(), f.findErr, f.useErr}
}

type holdFaultApprovals struct {
	store.ApprovalRepo
	findErr, useErr error
}

func (a holdFaultApprovals) FindConsumableHold(ctx context.Context, userID, sessionID, ruleID, argvHash string, now time.Time) (store.Approval, error) {
	if a.findErr != nil {
		return store.Approval{}, a.findErr
	}
	return a.ApprovalRepo.FindConsumableHold(ctx, userID, sessionID, ruleID, argvHash, now)
}

func (a holdFaultApprovals) MarkConsumed(ctx context.Context, id, consumedBy string, now, asOf time.Time) (bool, error) {
	if a.useErr != nil {
		return false, a.useErr
	}
	return a.ApprovalRepo.MarkConsumed(ctx, id, consumedBy, now, asOf)
}

// TestHoldUseStoreFault pins fail-closed at the use step: a store that does
// not answer the lookup or the use write returns an error and writes no
// consumed record, and the approval stays unused, so the same call can use
// it once the database answers again inside the window.
func TestHoldUseStoreFault(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		find, use error
		held      bool
	}{
		{"the lookup of a retry fails", pgDown, nil, false},
		{"the use write of a retry fails", nil, pgDown, false},
		{"the use write of the held call fails", nil, pgDown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec, userID := h.approveHold(t)
			fs := &holdFaultStore{Store: h.st}
			h.svc.st = fs
			fs.arm(tc.find, tc.use)
			var err error
			if tc.held {
				_, err = h.svc.ConsumeHeld(ctx, rec)
			} else {
				_, _, err = h.svc.ConsumeHold(ctx, userID, "s-1", "r-1", "sha256:k1")
			}
			if err == nil {
				t.Fatal("a store fault at the use step returned no error")
			}
			if n := len(consumedRecords(t, h.st)); n != 0 {
				t.Fatalf("consumed records = %d, want 0 after a failed use", n)
			}
			fs.arm(nil, nil)
			if _, ok, err := h.svc.ConsumeHold(ctx, userID, "s-1", "r-1", "sha256:k1"); err != nil || !ok {
				t.Fatalf("use after the database answered = %v, %v, want the unused approval", ok, err)
			}
			assertOneUse(t, h.st, rec, userID, "s-1")
		})
	}
}
