package server

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/store"
)

// ---------------------------------------------------------------------------
// The proof harness for the invariant "no database reads on any request path:
// decisions use in-memory snapshots, auth uses locally-verified tokens".
//
// countingStore is the instrument: it wraps a real store.Store and counts every
// repo getter, so a path that reaches for a repo is visible even when cheap.
//
// It deliberately does NOT embed store.Store: embedding would let a newly added
// repo getter fall through to the wrapped store silently and never be counted.
// With the methods written out one per repo, adding a repo to store.Store
// breaks this package's build (the `var _ store.Store` assertion below) until
// it is instrumented here and classified in storeRepoLanes, and the reflection
// test then fails any getter that bumps nothing, bumps the wrong counter, or
// is never classified.
// ---------------------------------------------------------------------------

type countingStore struct {
	inner  store.Store
	counts sync.Map // repo name → *atomic.Int64
}

// Compile gate: countingStore must implement the WHOLE store.Store surface by
// hand. A new repo getter on the interface fails to build here first.
var _ store.Store = (*countingStore)(nil)

func (c *countingStore) bump(name string) {
	v, _ := c.counts.LoadOrStore(name, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
}

func (c *countingStore) count(name string) int64 {
	if v, ok := c.counts.Load(name); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

// snapshot reads every classified repo counter at once, the "before" half of a
// request-path window.
func (c *countingStore) snapshot() map[string]int64 {
	out := make(map[string]int64, len(storeRepoLanes))
	for name := range storeRepoLanes {
		out[name] = c.count(name)
	}
	return out
}

// Lifecycle methods: not repo access, never counted.
func (c *countingStore) Ping(ctx context.Context) error    { return c.inner.Ping(ctx) }
func (c *countingStore) Migrate(ctx context.Context) error { return c.inner.Migrate(ctx) }
func (c *countingStore) Close() error                      { return c.inner.Close() }

// Repo getters: one per store.Store getter, all 20 instrumented.
func (c *countingStore) Users() store.UserRepo       { c.bump("users"); return c.inner.Users() }
func (c *countingStore) Roles() store.RoleRepo       { c.bump("roles"); return c.inner.Roles() }
func (c *countingStore) Devices() store.DeviceRepo   { c.bump("devices"); return c.inner.Devices() }
func (c *countingStore) Sessions() store.SessionRepo { c.bump("sessions"); return c.inner.Sessions() }
func (c *countingStore) Packs() store.PackRepo       { c.bump("packs"); return c.inner.Packs() }
func (c *countingStore) Apps() store.AppRepo         { c.bump("apps"); return c.inner.Apps() }
func (c *countingStore) ToolBindings() store.ToolBindingRepo {
	c.bump("tool_bindings")
	return c.inner.ToolBindings()
}
func (c *countingStore) Credentials() store.CredentialRepo {
	c.bump("credentials")
	return c.inner.Credentials()
}
func (c *countingStore) Policies() store.PolicyRepo { c.bump("policies"); return c.inner.Policies() }
func (c *countingStore) Snapshots() store.SnapshotRepo {
	c.bump("snapshots")
	return c.inner.Snapshots()
}
func (c *countingStore) Outbox() store.OutboxRepo { c.bump("outbox"); return c.inner.Outbox() }
func (c *countingStore) Audit() store.AuditRepo   { c.bump("audit"); return c.inner.Audit() }
func (c *countingStore) Revocations() store.RevocationRepo {
	c.bump("revocations")
	return c.inner.Revocations()
}
func (c *countingStore) SigningKeys() store.SigningKeyRepo {
	c.bump("signing_keys")
	return c.inner.SigningKeys()
}
func (c *countingStore) Settings() store.SettingsRepo {
	c.bump("settings")
	return c.inner.Settings()
}
func (c *countingStore) AttestationHashes() store.AttestationRepo {
	c.bump("attestation_hashes")
	return c.inner.AttestationHashes()
}
func (c *countingStore) Conversations() store.ConversationRepo {
	c.bump("conversations")
	return c.inner.Conversations()
}
func (c *countingStore) Approvals() store.ApprovalRepo {
	c.bump("approvals")
	return c.inner.Approvals()
}
func (c *countingStore) Approvers() store.ApproverRepo {
	c.bump("approvers")
	return c.inner.Approvers()
}
func (c *countingStore) Drafts() store.DraftRepo { c.bump("drafts"); return c.inner.Drafts() }

// repoLane says where a repo is allowed to be touched.
type repoLane int

const (
	// laneForbidden: reaching for this repo while a gateway/decide request is
	// in flight violates the no-DB-reads-on-request-paths invariant. The
	// counters are process-wide,
	// so "in flight" means the whole assertion window; the background owners
	// listed per repo are all boot-time or slow tickers whose next tick lands
	// well outside a window measured in milliseconds.
	laneForbidden repoLane = iota
	// laneAsync: the async audit spine owns this repo (audit is async) and drains it
	// from its own goroutine while requests are in flight, so the global
	// counter moves for reasons that are not request-path reads. Exempt by
	// design, not by omission.
	laneAsync
)

type repoRule struct {
	lane repoLane
	why  string
}

// storeRepoLanes classifies EVERY repo getter on store.Store. The map is the
// explicit allowlist behind the reflection walk in
// TestCountingStoreInstrumentsEveryRepo: a repo that exists but is not
// classified here fails that test, so a new repo cannot join the tree without
// someone deciding whether the request path may touch it.
var storeRepoLanes = map[string]repoRule{
	"users":              {laneForbidden, "subject identity is served from the in-memory subject cache filled at check-in"},
	"roles":              {laneForbidden, "the role set rides the cached subject and keys the precomputed catalog"},
	"devices":            {laneForbidden, "device posture is settled at check-in, not per request"},
	"sessions":           {laneForbidden, "session validity is the locally-verified token plus the in-memory denylist (background owner: the 1-minute idle-session janitor, closeIdleSessions)"},
	"packs":              {laneForbidden, "knowledge packs are compiled into the snapshot"},
	"apps":               {laneForbidden, "app rows are compiled into the manager's in-memory view"},
	"tool_bindings":      {laneForbidden, "bindings are refreshed into []gwBinding on control-plane events (refreshBindings)"},
	"credentials":        {laneForbidden, "credentials resolve from the in-memory broker cache and never reach the agent"},
	"policies":           {laneForbidden, "policy documents are compiled off-path; the request path sees only the snapshot"},
	"snapshots":          {laneForbidden, "the active snapshot is loaded once and swapped in memory"},
	"revocations":        {laneForbidden, "the denylist is in memory, fed by the kill-switch consumer; the store is the boot-time rebuild source only"},
	"signing_keys":       {laneForbidden, "session tokens verify against the locally cached JWKS (auth uses locally verified tokens)"},
	"attestation_hashes": {laneForbidden, "attestation is checked at check-in against the cached allow-set, never per request"},
	"settings":           {laneForbidden, "settings are read at boot (approval token key, pod identity)"},
	"conversations":      {laneForbidden, "capture is a read model written by the spine consumer and read by admin surfaces only"},
	"approvers":          {laneForbidden, "approver devices and push targets belong to the enrol/notify lanes"},
	"drafts":             {laneForbidden, "a decision never reads a draft, and the drafting tools read and write their own rows by id on their own calls, named at the call site like the approve lane"},
	// approvals is forbidden on every plain lane. The approve escalation lane
	// is the one documented exception and names itself at the call site; see
	// the approve phase of TestGatewayNoDBOnRequestPath.
	// (background owner: the approval service's 15s expiry/reminder sweep,
	// approval.Service.Run; that test's phases all run within ~2s of boot, so
	// the sweep never ticks inside an assertion window.)
	"approvals": {laneForbidden, "a decision that carries no approve marker must never reach the approvals table (zero-cost, TestDecideApproveZeroCost)"},
	"outbox":    {laneAsync, "the audit spool's drain goroutine and the relay own it (auditSpool.run, runRelay)"},
	"audit":     {laneAsync, "the chain consumer appends off the request path"},
}

// controlPlaneRepos are the repos that MUST stay untouched during gateway
// traffic: every laneForbidden entry above, sorted for a stable failure order.
var controlPlaneRepos = forbiddenRepos()

func forbiddenRepos() []string {
	var out []string
	for name, rule := range storeRepoLanes {
		if rule.lane == laneForbidden {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// assertNoRepoAccess is the "after" half of a request-path window: every
// forbidden repo must sit exactly where the snapshot left it. allowed names
// repos this particular lane is documented to touch (the approve escalation
// lane); each one still has to MOVE, so a stale allowance is loud too.
func assertNoRepoAccess(t *testing.T, cs *countingStore, phase string, before map[string]int64, allowed ...string) {
	t.Helper()
	exempt := map[string]bool{}
	for _, name := range allowed {
		exempt[name] = true
	}
	for _, repo := range controlPlaneRepos {
		got := cs.count(repo)
		if exempt[repo] {
			if got == before[repo] {
				t.Errorf("%s: repo %s is listed as an escalation-lane exception but was never touched; drop the exception", phase, repo)
			}
			continue
		}
		if got != before[repo] {
			t.Errorf("%s: DB touched on a request path; repo %s went %d → %d (invariant violated: %s)",
				phase, repo, before[repo], got, storeRepoLanes[repo].why)
		}
	}
}

// testAppCounting boots a full strazad over an instrumented store. Mutators
// may adjust the config before boot.
func testAppCounting(t *testing.T, mutators ...func(*config.Config)) (*App, string, *countingStore) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: "127.0.0.1:0"},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
		// PolicyFilter defaults OFF here so the existing pins that list
		// policy-denied tools (TestGatewayPEP) keep their meaning; the two-tier
		// tests flip it on per case. WarnSize 100 keeps small test catalogs quiet.
		Apps: config.Apps{
			PollInterval:           time.Second,
			HealthInterval:         time.Hour,
			Catalog:                config.Catalog{PolicyFilter: false, WarnSize: 100},
			AllowLoopbackUpstreams: true,
		},
	}
	for _, mutate := range mutators {
		mutate(&cfg)
	}
	seedStoreTemplate(t, cfg)
	raw, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cs := &countingStore{inner: raw}

	ctx, cancel := context.WithCancel(context.Background())
	app, err := build(ctx, cfg, logging.New(cfg.Log, io.Discard), cs)
	if err != nil {
		cancel()
		t.Fatalf("build: %v", err)
	}
	// Notification broadcasts are debounced in production (defaultNotifyDebounce);
	// make them synchronous for tests so a boot/setup invalidation cannot straggle
	// onto a stream a test opens moments later. Tests that specifically exercise
	// the debounce (TestGatewayNotifyCoalesces) re-arm a window explicitly.
	app.gateway.notify.SetDelay(0)
	base := "http://" + app.Addr()
	app.cfg.Server.PublicURL = base
	tokens, err := authn.NewTokenService(ctx, app.store.SigningKeys(), base, 0)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	app.tokens = tokens
	app.http.Handler = app.routes()

	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Log("app did not stop in time")
		}
	})
	return app, base, cs
}

// TestCountingStoreInstrumentsEveryRepo is the anti-drift half of the
// no-database-read proof: it walks store.Store by reflection, calls every
// repo getter on a live countingStore, and requires that each one bumps its
// OWN counter and that the counter is classified in storeRepoLanes. A repo
// added to store.Store fails the build first (countingStore implements the
// interface by hand); a repo added to countingStore without a bump, with a
// copy-pasted bump, or without a lane decision fails here.
func TestCountingStoreInstrumentsEveryRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw, err := store.Open(config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	cs := &countingStore{inner: raw}

	iface := reflect.TypeOf((*store.Store)(nil)).Elem()
	v := reflect.ValueOf(cs)
	lifecycle := map[string]bool{"Ping": true, "Migrate": true, "Close": true}
	seen := map[string]bool{}
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		if lifecycle[name] {
			continue
		}
		want := repoCounterName(name)
		if _, ok := storeRepoLanes[want]; !ok {
			t.Errorf("store.Store.%s() is not classified in storeRepoLanes (want key %q); decide whether the request path may touch it", name, want)
			continue
		}
		seen[want] = true
		before := cs.snapshot()
		v.MethodByName(name).Call(nil)
		var moved []string
		for repo, n := range cs.snapshot() {
			if n != before[repo] {
				moved = append(moved, repo)
			}
		}
		sort.Strings(moved)
		if len(moved) != 1 || moved[0] != want {
			t.Errorf("countingStore.%s() bumped %v, want exactly [%s]; the getter is uninstrumented or bumps the wrong counter", name, moved, want)
		}
	}
	for repo := range storeRepoLanes {
		if !seen[repo] {
			t.Errorf("storeRepoLanes classifies %q but store.Store has no such repo getter (stale entry)", repo)
		}
	}
	if len(seen) != 20 {
		t.Errorf("instrumented %d repos, store.Store carried 20 at the last re-baseline; update this pin deliberately", len(seen))
	}
}

// repoCounterName maps a store.Store getter name to its counter name
// (ToolBindings → tool_bindings). Mechanical on purpose: the reflection walk
// derives the expected counter instead of trusting a second hand-written list.
func repoCounterName(method string) string {
	var b strings.Builder
	for i, r := range method {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r | 0x20)
	}
	return b.String()
}
