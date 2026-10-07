package approval

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// fakeBus is a synchronous in-process core-NATS stand-in: PublishCore fans out
// to every handler whose "<prefix>.*" subscription matches, inline. Determinism
// beats fidelity for the resolution-path tests.
type fakeBus struct {
	mu   sync.Mutex
	subs map[string][]func(string, []byte)
}

func newFakeBus() *fakeBus { return &fakeBus{subs: map[string][]func(string, []byte){}} }

func (f *fakeBus) SubscribeCore(subject string, h func(string, []byte)) (func(), error) {
	f.mu.Lock()
	f.subs[subject] = append(f.subs[subject], h)
	f.mu.Unlock()
	return func() {}, nil
}

func (f *fakeBus) PublishCore(subject string, data []byte) error {
	f.mu.Lock()
	var handlers []func(string, []byte)
	for pat, hs := range f.subs {
		if pat == subject || (strings.HasSuffix(pat, ".*") && strings.HasPrefix(subject, strings.TrimSuffix(pat, "*"))) {
			handlers = append(handlers, hs...)
		}
	}
	f.mu.Unlock()
	for _, h := range handlers {
		h(subject, data)
	}
	return nil
}

type fakeResolver struct{ roles map[string][]store.Role }

func (f *fakeResolver) ResolveRoles(_ context.Context, userID string, _ time.Time) ([]store.Role, error) {
	return f.roles[userID], nil
}

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	storetest.SeedSQLite(t, path)
	st, err := store.Open(config.Config{Store: config.Store{
		Driver: config.DriverSQLite, DSN: path,
	}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return st
}

type harness struct {
	svc      *Service
	st       store.Store
	resolver *fakeResolver
	bus      *fakeBus
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := newTestStore(t)
	bus := newFakeBus()
	res := &fakeResolver{roles: map[string][]store.Role{}}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	svc := &Service{
		st: st, bus: bus, resolver: res, cfg: config.Approval{}, log: slog.Default(),
		tokenKey: key, now: time.Now,
		waiters: map[string][]chan struct{}{},
	}
	// Wire the resolution subscription exactly as Run would, deterministically.
	unsub, err := bus.SubscribeCore(resolvedSubjectPrefix+"*", svc.handleResolved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub)
	return &harness{svc: svc, st: st, resolver: res, bus: bus}
}

// seedUser creates a user and grants them the given role names in the resolver.
func (h *harness) seedUser(t *testing.T, username string, roles ...string) store.User {
	t.Helper()
	u, err := h.st.Users().Create(context.Background(), store.User{Username: username, Email: username + "@x.io"})
	if err != nil {
		t.Fatalf("Create user: %v", err)
	}
	rs := make([]store.Role, len(roles))
	for i, r := range roles {
		rs[i] = store.Role{ID: "role-" + r, Name: r}
	}
	h.resolver.roles[u.ID] = rs
	return u
}

func req(session, userID, username string, spec policy.ApproveSpec) RequestInput {
	return RequestInput{
		SessionID: session, UserID: userID, Username: username, RuleID: "r-1",
		SetName: "guardrails", ArgvHash: "sha256:k1", Lane: "hook",
		Summary: "mcp.call midpoint:disable_user", Spec: spec,
	}
}

func TestRequestCreatesAndDedupes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	rec, err := h.svc.Request(ctx, req("s-1", "u-req", "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if rec.State != StatePending || rec.TimeoutSeconds != 90 || rec.RetryTTLSeconds != 60 {
		t.Fatalf("record = %+v", rec)
	}
	if rec.ExpiresAt.Sub(rec.CreatedAt) != 90*time.Second {
		t.Errorf("expiry window = %s, want 90s", rec.ExpiresAt.Sub(rec.CreatedAt))
	}

	// Dedupe: same (session, rule, argv) returns the SAME record, no new row.
	dup, err := h.svc.Request(ctx, req("s-1", "u-req", "nova", spec))
	if err != nil || dup.ID != rec.ID {
		t.Fatalf("dedupe = %+v, %v (want same id %s)", dup, err, rec.ID)
	}
	all, _ := h.st.Approvals().List(ctx, "")
	if len(all) != 1 {
		t.Errorf("expected 1 row after dedupe, got %d", len(all))
	}

	// A request audit CE landed in the outbox.
	assertAudit(t, h.st, "request", "pending")
}

// TestRequestDedupesByMintKeyOnly pins the closed fingerprint drain in Request:
// a pending row under the v1 key no longer attaches a request that arrives
// with the v2 mint key, even when the v1 candidate still rides the input, and
// a repeat of the same v2 key keeps attaching.
func TestRequestDedupesByMintKeyOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	pre := req("s-1", "u-req", "nova", spec) // ArgvHash "sha256:k1" = a v1-era row
	rec, err := h.svc.Request(ctx, pre)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	post := req("s-1", "u-req", "nova", spec)
	post.ArgvHash = "v2:call:sha256:deadbeef" // the v2 mint key for the same call
	fresh, err := h.svc.Request(ctx, post)
	if err != nil || fresh.ID == rec.ID {
		t.Fatalf("v2 request = %+v, %v (want its own row, not the v1 row %s)", fresh, err, rec.ID)
	}
	again, err := h.svc.Request(ctx, post)
	if err != nil || again.ID != fresh.ID {
		t.Fatalf("repeat of the v2 key = %+v, %v (want the same row %s)", again, err, fresh.ID)
	}
	all, _ := h.st.Approvals().List(ctx, "")
	if len(all) != 2 {
		t.Errorf("expected the v1 row and one v2 row, got %d rows", len(all))
	}
}

// TestDecideApprovedHoldIsUsableOnce pins what an approval of a hold leaves:
// a use of the identical call from the same session, read from the store,
// which works once and names the decider.
func TestDecideApprovedHoldIsUsableOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	out, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "", "")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.State != StateApproved || out.DecidedByName != "kim" || out.DecidedAt == nil {
		t.Fatalf("decided = %+v", out)
	}
	assertAudit(t, h.st, "resolution", "approved")

	// The approval is usable once by the identical call of the same session.
	ex, ok, err := h.svc.ConsumeHold(ctx, requester.ID, "s-1", "r-1", "sha256:k1")
	if err != nil || !ok || ex.DecidedByName != "kim" {
		t.Fatalf("ConsumeHold = %+v, %v, %v", ex, ok, err)
	}
	if _, ok, err := h.svc.ConsumeHold(ctx, requester.ID, "s-1", "r-1", "sha256:k1"); ok || err != nil {
		t.Errorf("second use = %v, %v, want false: an approval runs one call", ok, err)
	}
}

func TestDecideValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	h.seedUser(t, "mallory") // no approver role
	approver := h.seedUser(t, "kim", "sec-approvers")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	mkReq := func(argv string) Record {
		in := req("s-1", requester.ID, "nova", spec)
		in.ArgvHash = argv
		r, err := h.svc.Request(ctx, in)
		if err != nil {
			t.Fatalf("Request: %v", err)
		}
		return r
	}

	// Unknown id.
	if _, err := h.svc.Decide(ctx, "nope", "approved", approver.ID, "console", "", ""); err != ErrNotFound {
		t.Errorf("unknown id = %v, want ErrNotFound", err)
	}
	// Bad verdict.
	r0 := mkReq("k0")
	if _, err := h.svc.Decide(ctx, r0.ID, "maybe", approver.ID, "console", "", ""); err != ErrBadVerdict {
		t.Errorf("bad verdict = %v, want ErrBadVerdict", err)
	}
	// Self-approval (requester decides own request, selfApproval false).
	if _, err := h.svc.Decide(ctx, r0.ID, "approved", requester.ID, "console", "", ""); err != ErrSelfApproval {
		t.Errorf("self approval = %v, want ErrSelfApproval", err)
	}
	// Not an approver (mallory holds no qualifying role).
	if _, err := h.svc.Decide(ctx, r0.ID, "approved", h.userID(t, "mallory"), "console", "", ""); err != ErrNotApprover {
		t.Errorf("non-approver = %v, want ErrNotApprover", err)
	}
	// Happy deny, then idempotent same-verdict, then conflicting verdict.
	if _, err := h.svc.Decide(ctx, r0.ID, "denied", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if _, err := h.svc.Decide(ctx, r0.ID, "denied", approver.ID, "console", "", ""); err != nil {
		t.Errorf("idempotent same verdict = %v, want nil", err)
	}
	if _, err := h.svc.Decide(ctx, r0.ID, "approved", approver.ID, "console", "", ""); err != ErrConflict {
		t.Errorf("conflicting verdict = %v, want ErrConflict", err)
	}

	// Expired record: Decide refuses with ErrExpired (clock advanced past the
	// record's window, before the sweep marked it).
	r1 := mkReq("k1")
	h.svc.now = func() time.Time { return r1.ExpiresAt.Add(time.Second) }
	if _, err := h.svc.Decide(ctx, r1.ID, "approved", approver.ID, "console", "", ""); err != ErrExpired {
		t.Errorf("expired decide = %v, want ErrExpired", err)
	}
}

// TestConfirmModeRequesterOnly pins the revision-11 decide semantics: a
// mode confirm record is decidable by its requester ALONE. Root and any
// role-holder are refused with ErrNotRequester (confirmation proves the
// requester's presence, which nobody else can supply); the requester needs
// no approver role at all; the snapshot rides the record's mode column.
func TestConfirmModeRequesterOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova") // deliberately NO roles
	admin := h.seedUser(t, "root", AdminFallbackRole)
	approver := h.seedUser(t, "kim", "sec-approvers")
	spec := policy.ApproveSpec{TimeoutSeconds: 90, RetryTTLSeconds: 60}

	in := req("s-1", requester.ID, "nova", spec)
	in.Confirm = true
	rec, err := h.svc.Request(ctx, in)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if rec.Mode != policy.ModeConfirm {
		t.Fatalf("record mode = %q, want confirm (snapshot must ride the record)", rec.Mode)
	}

	// Root may NOT confirm someone else's request.
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", admin.ID, "console", "", ""); err != ErrNotRequester {
		t.Errorf("root decide on confirm = %v, want ErrNotRequester", err)
	}
	// Neither may a role-holding approver.
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "", ""); err != ErrNotRequester {
		t.Errorf("approver decide on confirm = %v, want ErrNotRequester", err)
	}
	// The requester confirms without holding any approver role, on a device
	// that signs.
	out, err := h.svc.Decide(ctx, rec.ID, "approved", requester.ID, "phone", "", "dev-1")
	if err != nil {
		t.Fatalf("requester confirm: %v", err)
	}
	if out.State != StateApproved || out.DecidedByName != "nova" {
		t.Fatalf("confirmed = %+v", out)
	}
	// The used approval carries the mode (the "confirmed by" wording seam).
	if ex, ok, err := h.svc.ConsumeHold(ctx, requester.ID, "s-1", "r-1", "sha256:k1"); err != nil || !ok || ex.Mode != policy.ModeConfirm {
		t.Errorf("used approval = %+v, %v, %v (want mode confirm)", ex, ok, err)
	}
}

// TestConfirmModeNHIRequesterCannotConfirm: a confirm record whose requester
// is an NHI is undecidable (the NHI hard-block outranks the identity match).
// The engine's autonomous clamp should prevent such records from existing;
// this pins the decide-side backstop.
func TestConfirmModeNHIRequesterCannotConfirm(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	nhi := h.seedNHI(t, "robot")
	spec := policy.ApproveSpec{TimeoutSeconds: 90, RetryTTLSeconds: 60}
	in := req("s-2", nhi.ID, "robot", spec)
	in.Confirm = true
	rec, err := h.svc.Request(ctx, in)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", nhi.ID, "console", "", ""); err != ErrNHIDecider {
		t.Errorf("NHI self-confirm = %v, want ErrNHIDecider", err)
	}
}

// TestConfirmModeDefaultApprove: a plain request (Confirm unset) snapshots
// mode approve, and pre-migration empty-mode rows read as approve semantics
// (the decide path treats only the literal confirm mode specially).
func TestConfirmModeDefaultApprove(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, err := h.svc.Request(ctx, req("s-3", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if rec.Mode != policy.ModeApprove {
		t.Errorf("default record mode = %q, want approve", rec.Mode)
	}
}

// TestConfirmEligibleToDecide pins the queue-filter twin of the decide gate:
// a confirm record is decidable-listed for its requester alone, regardless
// of roles, and never for anyone else.
func TestConfirmEligibleToDecide(t *testing.T) {
	rec := Record{UserID: "u-req", Mode: policy.ModeConfirm}
	if !eligibleToDecide(rec, "u-req", "", map[string]bool{}) {
		t.Error("requester must be eligible on a confirm record (no roles needed)")
	}
	if eligibleToDecide(rec, "u-root", "", map[string]bool{AdminFallbackRole: true}) {
		t.Error("root must NOT be eligible on someone else's confirm record")
	}
	// Approve semantics untouched: four-eyes still hides the requester.
	plain := Record{UserID: "u-req", ApproverRoles: []string{"sec-approvers"}}
	if eligibleToDecide(plain, "u-req", "", map[string]bool{"sec-approvers": true}) {
		t.Error("four-eyes must keep hiding the requester on approve records")
	}
}

func TestStrazaAdminFallback(t *testing.T) {
	// Since revision 14 an entirely-empty pool can no longer be MINTED via
	// Request (unroutable denies at request time); the fallback survives as
	// the backstop for legacy pending rows created before the upgrade, so
	// this seeds one directly.
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	admin := h.seedUser(t, "root", AdminFallbackRole)
	h.seedUser(t, "kim", "sec-approvers") // a non-admin role that must NOT qualify

	// Empty ApproverRoles → only straza-admin may approve.
	legacy, err := h.st.Approvals().Insert(ctx, store.Approval{
		SessionID: "s-1", UserID: requester.ID, Username: "nova",
		RuleID: "r-1", SetName: "guardrails", ArgvHash: "sha256:legacy", Lane: "hook",
		Mode: policy.ModeApprove, State: string(StatePending),
		TimeoutSeconds: 90, RetryTTLSeconds: 60,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(90 * time.Second),
	})
	if err != nil {
		t.Fatalf("Insert legacy row: %v", err)
	}
	rec := recordFromStore(legacy)

	if _, err := h.svc.Decide(ctx, rec.ID, "approved", h.userID(t, "kim"), "console", "", ""); err != ErrNotApprover {
		t.Errorf("non-admin on empty-roles rule = %v, want ErrNotApprover", err)
	}
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", admin.ID, "console", "", ""); err != nil {
		t.Errorf("straza-admin fallback approve = %v, want nil", err)
	}
}

func TestAwaitResolves(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	in := req("s-1", requester.ID, "nova", spec)
	in.Lane = "gateway"
	rec, _ := h.svc.Request(ctx, in)

	done := make(chan Record, 1)
	go func() {
		r, err := h.svc.Await(context.Background(), rec.ID)
		if err != nil {
			t.Errorf("Await: %v", err)
		}
		done <- r
	}()
	// Let the waiter register, then decide.
	time.Sleep(20 * time.Millisecond)
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	select {
	case r := <-done:
		if r.State != StateApproved {
			t.Errorf("Await returned %s, want approved", r.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Await did not wake on resolution")
	}
}

func TestAwaitContextCancel(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))

	cctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := h.svc.Await(cctx, rec.ID); err == nil {
		t.Error("Await must return ctx error when the deadline passes with no decision")
	}
}

func TestSweepExpired(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := h.st.Approvals().Insert(ctx, store.Approval{
		SessionID: "s-1", RuleID: "r-1", ArgvHash: "k", State: "pending",
		CreatedAt: now.Add(-10 * time.Minute), ExpiresAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	h.svc.sweepExpired(ctx)

	rows, _ := h.st.Approvals().List(ctx, "expired")
	if len(rows) != 1 {
		t.Fatalf("expected 1 expired record, got %d", len(rows))
	}
	assertAudit(t, h.st, "resolution", "expired")
}

// TestHoldUseWindow pins the retry window of a hold's approval: past
// decidedAt plus retryTTLSeconds the approval is refused and no consumed
// record is written.
func TestHoldUseWindow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	// Advance past the retry window: the approval is spent and refused.
	h.svc.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, ok, err := h.svc.ConsumeHold(ctx, requester.ID, "s-1", "r-1", "sha256:k1"); ok || err != nil {
		t.Errorf("use after the window = %v, %v, want false", ok, err)
	}
	if n := countAuditPhase(t, h.st, "consumed"); n != 0 {
		t.Errorf("consumed records = %d, want 0", n)
	}
}

func (h *harness) userID(t *testing.T, username string) string {
	t.Helper()
	u, err := h.st.Users().GetByUsername(context.Background(), username)
	if err != nil {
		t.Fatalf("GetByUsername %s: %v", username, err)
	}
	return u.ID
}

// assertAudit fails unless an outbox straza.audit.approval CE exists with the
// given phase and state.
func assertAudit(t *testing.T, st store.Store, phase, state string) {
	t.Helper()
	evs, err := st.Outbox().ListUnpublished(context.Background(), 100)
	if err != nil {
		t.Fatalf("Outbox: %v", err)
	}
	for _, e := range evs {
		if e.Subject != auditSubject {
			continue
		}
		if strings.Contains(e.CE, `"phase":"`+phase+`"`) && strings.Contains(e.CE, `"state":"`+state+`"`) {
			return
		}
	}
	t.Errorf("no %s/%s approval audit CE found in outbox", phase, state)
}
