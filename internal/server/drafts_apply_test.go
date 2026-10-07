package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/identity"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/snapshot"
	"github.com/strazahq/straza/internal/store"
)

// outcome is what a replica's gateway decides for one call: the call runs,
// a person must approve it first, or it is refused, as a denial, as a tool
// the session cannot see, or as a session that must check in again.
type outcome string

const (
	callRuns    outcome = "runs"
	callHeld    outcome = "held"
	callDenied  outcome = "denied"
	callUnknown outcome = "unknown"
	callCheckIn outcome = "check in"
)

// width orders outcomes from the narrowest, every refusal, to the widest.
func (o outcome) width() int {
	switch o {
	case callRuns:
		return 2
	case callHeld:
		return 1
	}
	return 0
}

// within reports whether got is what a call decided before or after a
// publish, or no wider than the narrower of the two.
func within(got, before, after outcome) bool {
	return got == before || got == after || got.width() <= min(before.width(), after.width())
}

// gatewayDecides answers what app's gateway decides for a call of tool by
// session, from the state handleToolCall reads: the cached subject, the
// session's catalog and hidden set, and the live snapshot. It runs nothing.
func gatewayDecides(app *App, session, tool string) outcome {
	sub, ok := app.subjects.get(session)
	if !ok {
		return callCheckIn
	}
	tier1 := app.catalogFor(sub.Roles)
	target, visible := tier1.targets[tool]
	if !visible || app.overlayFor(session, sub, tier1).hidden[tool] {
		return callUnknown
	}
	d := app.snapshots.Current().Engine.Evaluate(policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall,
		App: target.app, ToolName: target.tool, Granted: true}, sub)
	switch {
	case d.Effect != policy.EffectAllow:
		return callDenied
	case d.Approve != nil:
		return callHeld
	}
	return callRuns
}

// echoTools are the two tools of startGatewayUpstream as the gateway names
// them on the server echoapp.
var echoTools = []string{"echoapp__echo", "echoapp__env"}

// decidesAll answers gatewayDecides for every tool of echoTools.
func decidesAll(app *App, session string) map[string]outcome {
	out := map[string]outcome{}
	for _, tool := range echoTools {
		out[tool] = gatewayDecides(app, session, tool)
	}
	return out
}

// sessionOf answers the session a session token names.
func sessionOf(t *testing.T, app *App, tok string) string {
	t.Helper()
	claims, err := app.tokens.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	return claims.Session
}

// checkInAgain checks a session in again with its token, as a client does
// on the gateway's check-in-again answer, and answers the new token.
func checkInAgain(t *testing.T, base, tok string) string {
	t.Helper()
	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"session_token": tok,
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{"self": "x"}},
	})
	if code != http.StatusOK {
		t.Fatalf("check-in again = %d %v", code, body)
	}
	return body["session_token"].(string)
}

// putServer stores a server's row at the manifest doc, as a publish's write
// of an App item leaves it, and answers the row. It starts nothing.
func putServer(t *testing.T, app *App, doc string) store.App {
	t.Helper()
	ctx := context.Background()
	mf, err := manager.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := mf.JSON()
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.store.Apps().GetByName(ctx, mf.Metadata.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		row, err = app.store.Apps().Create(ctx, store.App{Name: mf.Metadata.Name, Version: "1.0.0", Manifest: raw,
			RuntimeKind: mf.Straza.Runtime.Kind, Status: manager.StatusRunning, Source: store.AppSourceAPI})
	case err == nil:
		row.Manifest = raw
		row, err = app.store.Apps().Update(ctx, row)
	}
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// setAccess leaves the role roleID with one access row on server, with
// the matchers given, when want holds, and with none otherwise.
func setAccess(t *testing.T, app *App, roleID string, server store.App, want bool, matchers string) {
	t.Helper()
	ctx := context.Background()
	rows, err := app.store.ToolBindings().ListByRole(ctx, roleID)
	if err != nil {
		t.Fatal(err)
	}
	have := false
	for _, r := range rows {
		if r.AppID != server.ID {
			continue
		}
		if want && r.ToolMatcher == matchers {
			have = true
			continue
		}
		if err := app.store.ToolBindings().Delete(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if want && !have {
		if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: roleID, AppID: server.ID, ToolMatcher: matchers}); err != nil {
			t.Fatal(err)
		}
	}
}

// policyText is the set name at priority for the holders of dev, with the
// rules given.
func policyText(name string, priority int, rules string) string {
	return fmt.Sprintf(`apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: %s}
spec:
  priority: %d
  match: {roles: [dev]}
  rules:
%s`, name, priority, rules)
}

// The rules the scenarios compose: every tool of echoapp runs, env is
// denied, and env waits for a person's approval.
const (
	allowEchoapp = `    - id: allow-echoapp
      tools: [mcp.call]
      apps: [echoapp]
      effect: allow
`
	denyEnv = `    - id: deny-env
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {deny: ["env"]}
      effect: deny
      reason: "Straza: env is off"
`
	holdEnv = `    - id: hold-env
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["env"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
`
)

// activateSets builds the successor of the active snapshot in which each
// named set holds its text, or leaves when its text is empty, stores and
// activates it as a publish's transaction does, emitting nothing, and
// answers it for the publish's head.
func activateSets(t *testing.T, app *App, sets map[string]string) *snapshot.Built {
	t.Helper()
	ctx := context.Background()
	act, err := app.store.Snapshots().GetActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string][]byte{}
	for name, text := range sets {
		changes[name] = nil
		if text != "" {
			changes[name] = []byte(text)
		}
	}
	b, err := app.snapshots.Build(ctx, act.ID, changes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Snapshots().Create(ctx, store.Snapshot{ID: b.ID, SignerKeyID: b.SignerKeyID, Blob: b.Blob}); err != nil && !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	if err := app.store.Snapshots().SetActive(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	return &b
}

// publishOn runs a publish's apply on app as the publish route does once
// its transaction committed: the head and the shared steps under
// a.configMu, then the starts.
func publishOn(ctx context.Context, app *App, head *publishHead) error {
	app.configMu.Lock()
	st, err := app.applyLocked(ctx, head)
	app.configMu.Unlock()
	if err != nil {
		return err
	}
	app.startLive(ctx, st, nil)
	return nil
}

// liveFails wraps a store so a test can fail LiveState, the one read of an
// apply, hand the state back without its read time, which the stop that
// follows refuses, and run a step once right after a read that succeeded.
type liveFails struct {
	store.Store
	fail     atomic.Bool
	noReadAt atomic.Bool
	after    atomic.Pointer[func()]
	reads    atomic.Int64
}

func (s *liveFails) Drafts() store.DraftRepo { return liveFailsRepo{s.Store.Drafts(), s} }

type liveFailsRepo struct {
	store.DraftRepo
	s *liveFails
}

func (r liveFailsRepo) LiveState(ctx context.Context, live string) (store.LiveState, error) {
	r.s.reads.Add(1)
	if r.s.fail.Load() {
		return store.LiveState{}, errors.New("database unavailable")
	}
	st, err := r.DraftRepo.LiveState(ctx, live)
	if r.s.noReadAt.Load() {
		st.ReadAt = time.Time{}
	}
	if fn := r.s.after.Swap(nil); fn != nil && err == nil {
		(*fn)()
	}
	return st, err
}

// wrapLive is the pre-run hook that puts a liveFails around app's store
// and, when logs is set, a capturing logger in place of app's.
func wrapLive(fs **liveFails, logs **syncBuffer) func(*App) {
	return func(app *App) {
		*fs = &liveFails{Store: app.store}
		app.store = *fs
		if logs != nil {
			app.log, *logs = captureLogger()
		}
	}
}

// waitFor polls cond until it holds, failing the test after ten seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// whoamiOn answers what the tagged server that app runs says it is.
func whoamiOn(ctx context.Context, app *App) (string, error) {
	res, err := app.manager.Call(ctx, "tagged", "whoami", nil, nil)
	if err != nil {
		return "", err
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text, nil
		}
	}
	return "", nil
}

// replicaPair boots two replicas of one deployment on one store and one key
// file, each with its own event bus. b shares a's store object, so sqlite
// keeps its one connection, where two connections to one file turn a write
// away at once. Both relays drain the one outbox, so a test feeds b a
// publish's events through b's own converge consumer, in the order it
// chooses.
func replicaPair(t *testing.T) (a, b *App, baseA, baseB string) {
	t.Helper()
	a, baseA = testApp(t, func(cfg *config.Config) { cfg.Apps.HealthInterval = time.Hour })
	cfg := a.cfg
	cfg.DataDir, cfg.Server.Listen = t.TempDir(), "127.0.0.1:0"
	cfg.Secrets.KEKFile = a.cfg.KEKFile()
	ctx, cancel := context.WithCancel(context.Background())
	b, err := build(ctx, cfg, logging.New(cfg.Log, io.Discard), keptStore{a.store})
	if err != nil {
		cancel()
		t.Fatalf("build replica b: %v", err)
	}
	baseB = "http://" + b.Addr()
	b.cfg.Server.PublicURL = baseB
	tokens, err := authn.NewTokenService(ctx, b.store.SigningKeys(), baseB, 0)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	b.tokens = tokens
	b.http.Handler = b.routes()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("replica b did not stop in time")
		}
	})
	return a, b, baseA, baseB
}

// keptStore is a store whose Close leaves it open, for a replica that
// shares the store another replica closes.
type keptStore struct{ store.Store }

func (keptStore) Close() error { return nil }

// replicaEvent is one event a publish writes, as a converge consumer reads
// it.
type replicaEvent struct {
	subject string
	data    map[string]any
}

// payload is the event's CloudEvents envelope from source, as emitEventCtx
// writes it.
func (e replicaEvent) payload(t *testing.T, source string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"specversion": "1.0", "id": uuid.NewString(), "type": e.subject,
		"source": source, "time": time.Now().UTC().Format(time.RFC3339Nano), "data": e.data})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// orders answers every order of the indices 0 to n-1.
func orders(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, rest := range orders(n - 1) {
		for i := 0; i <= len(rest); i++ {
			out = append(out, slices.Concat(rest[:i], []int{n - 1}, rest[i:]))
		}
	}
	return out
}

// TestDropHoldingDropsEveryHolderOfTheRoles pins dropHolding: it drops a
// subject that holds one of the roles directly or through another role,
// keeps every other subject, and answers how many it dropped.
func TestDropHoldingDropsEveryHolderOfTheRoles(t *testing.T) {
	cases := []struct {
		name  string
		roles map[string]bool
		left  []string
	}{
		{name: "a role held directly", roles: map[string]bool{"dev": true}, left: []string{"lee", "max"}},
		{name: "a role held through another role", roles: map[string]bool{"reader": true}, left: []string{"lee", "max"}},
		{name: "two roles", roles: map[string]bool{"reader": true, "ops": true}, left: []string{"max"}},
		{name: "a role nobody holds", roles: map[string]bool{"gone": true}, left: []string{"kim", "lee", "max"}},
		{name: "no role", roles: map[string]bool{}, left: []string{"kim", "lee", "max"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newSubjectCache()
			c.put("kim", policy.Subject{User: "kim", Roles: []string{"dev", "reader"}}, time.Time{})
			c.put("lee", policy.Subject{User: "lee", Roles: []string{"ops"}}, time.Time{})
			c.put("max", policy.Subject{User: "max"}, time.Time{})
			if n := c.dropHolding(tc.roles); n != 3-len(tc.left) {
				t.Errorf("dropHolding answered %d, want %d", n, 3-len(tc.left))
			}
			for _, s := range []string{"kim", "lee", "max"} {
				if _, ok := c.get(s); ok != slices.Contains(tc.left, s) {
					t.Errorf("subject %s cached = %v, want %v", s, ok, !ok)
				}
			}
		})
	}
}

// TestDropAllDropsEveryCachedSubject pins dropAll, the role step of a
// replica that has applied nothing yet.
func TestDropAllDropsEveryCachedSubject(t *testing.T) {
	c := newSubjectCache()
	c.put("kim", policy.Subject{User: "kim", Roles: []string{"dev"}}, time.Time{})
	c.put("max", policy.Subject{User: "max"}, time.Time{})
	if n := c.dropAll(); n != 2 {
		t.Errorf("dropAll answered %d, want 2", n)
	}
	for _, s := range []string{"kim", "max"} {
		if _, ok := c.get(s); ok {
			t.Errorf("subject %s is still cached", s)
		}
	}
}

// TestChangedReachNamesEveryRoleWhoseClosureChanged pins changedReach: a
// role whose implication closure lost or gained a role, directly or through
// another role, is named, and a role whose closure holds the same roles is
// not, whatever its edges did.
func TestChangedReachNamesEveryRoleWhoseClosureChanged(t *testing.T) {
	cases := []struct {
		name          string
		before, after map[string][]string
		want          []string
	}{
		{name: "nothing changed", before: map[string][]string{"dev": {"reader"}}, after: map[string][]string{"dev": {"reader"}}},
		{name: "an edge removed", before: map[string][]string{"dev": {"reader"}}, after: map[string][]string{},
			want: []string{"dev"}},
		{name: "an edge removed further down", before: map[string][]string{"dev": {"reader"}, "reader": {"viewer"}},
			after: map[string][]string{"dev": {"reader"}}, want: []string{"dev", "reader"}},
		{name: "a lost role still reached another way", before: map[string][]string{"dev": {"reader"}, "reader": {"viewer"}},
			after: map[string][]string{"dev": {"reader", "viewer"}}, want: []string{"reader"}},
		{name: "an edge added", before: map[string][]string{}, after: map[string][]string{"dev": {"reader"}},
			want: []string{"dev"}},
		{name: "an edge added further down", before: map[string][]string{"dev": {"reader"}},
			after: map[string][]string{"dev": {"reader"}, "reader": {"viewer"}}, want: []string{"dev", "reader"}},
		{name: "an edge added that reaches nothing new", before: map[string][]string{"dev": {"reader"}, "reader": {"viewer"}},
			after: map[string][]string{"dev": {"reader", "viewer"}, "reader": {"viewer"}}},
		{name: "a cycle kept", before: map[string][]string{"a": {"b"}, "b": {"a"}}, after: map[string][]string{"a": {"b"}, "b": {"a"}}},
		{name: "a cycle broken", before: map[string][]string{"a": {"b"}, "b": {"a"}}, after: map[string][]string{"a": {"b"}},
			want: []string{"b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for r := range changedReach(tc.before, tc.after) {
				got = append(got, r)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("changedReach = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestKeepsAccessKeepsIdenticalRows pins step 3 from state: a row stays
// only when the state holds a row identical in role, server and matchers.
func TestKeepsAccessKeepsIdenticalRows(t *testing.T) {
	live := []store.LiveAccess{
		{ID: "n1", Role: "dev", App: "x", Matchers: []string{"*"}},
		{ID: "n2", Role: "dev", App: "y", Matchers: []string{"echo", "env"}},
	}
	cases := []struct {
		name string
		row  gwBinding
		want bool
	}{
		{name: "an identical row with another id", row: gwBinding{ID: "o1", Role: "dev", App: "x", Matchers: []string{"*"}}, want: true},
		{name: "a row whose matchers changed", row: gwBinding{ID: "n2", Role: "dev", App: "y", Matchers: []string{"echo"}}},
		{name: "a row of another role", row: gwBinding{ID: "n1", Role: "ops", App: "x", Matchers: []string{"*"}}},
		{name: "a row the state lacks", row: gwBinding{ID: "o3", Role: "dev", App: "z", Matchers: []string{"*"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := keepsAccess(live)(tc.row); got != tc.want {
				t.Errorf("keep = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestApplyRolesDropsTheSessionsWhoseRolesChangedReach pins step 5 from
// state: with nothing applied it drops every cached subject, and after an
// apply it drops the holders of a role that went, that holds another id
// under its name, or whose closure lost or gained a role, and no one else.
// It bumps role resolution once and records the state as applied.
func TestApplyRolesDropsTheSessionsWhoseRolesChangedReach(t *testing.T) {
	ids := map[string]string{"dev": "r1", "ops": "r2", "reader": "r3"}
	edges := map[string][]string{"dev": {"reader"}}
	applied := appliedConfig{roles: ids, edges: edges}
	cases := []struct {
		name    string
		applied appliedConfig
		state   store.LiveState
		left    []string
	}{
		{name: "nothing applied yet", state: store.LiveState{Generation: 5, RoleIDs: ids, Implies: edges}},
		{name: "an implication removed", applied: applied, state: store.LiveState{Generation: 5, RoleIDs: ids, Implies: map[string][]string{}},
			left: []string{"lee", "max"}},
		{name: "a role removed", applied: applied, state: store.LiveState{Generation: 5,
			RoleIDs: map[string]string{"dev": "r1", "reader": "r3"}, Implies: edges}, left: []string{"kim", "max"}},
		{name: "a role created again under its name", applied: applied, state: store.LiveState{Generation: 5,
			RoleIDs: map[string]string{"dev": "r1", "ops": "r9", "reader": "r3"}, Implies: edges}, left: []string{"kim", "max"}},
		{name: "an implication added", applied: appliedConfig{roles: ids, edges: map[string][]string{}},
			state: store.LiveState{Generation: 5, RoleIDs: ids, Implies: edges}, left: []string{"lee", "max"}},
		{name: "an implication added that reaches nothing new",
			applied: appliedConfig{roles: ids, edges: map[string][]string{"dev": {"reader"}, "reader": {"viewer"}}},
			state:   store.LiveState{Generation: 5, RoleIDs: ids, Implies: map[string][]string{"dev": {"reader", "viewer"}, "reader": {"viewer"}}},
			left:    []string{"kim", "lee", "max"}},
		{name: "nothing changed", applied: applied, state: store.LiveState{Generation: 4, RoleIDs: ids, Implies: edges},
			left: []string{"kim", "lee", "max"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{resolver: identity.NewResolver(nil), subjects: newSubjectCache(), applied: tc.applied}
			a.subjects.put("kim", policy.Subject{User: "kim", Roles: []string{"dev", "reader"}}, time.Time{})
			a.subjects.put("lee", policy.Subject{User: "lee", Roles: []string{"ops"}}, time.Time{})
			a.subjects.put("max", policy.Subject{User: "max"}, time.Time{})
			epoch := a.resolver.Epoch()

			a.applyRoles(tc.state)
			for _, s := range []string{"kim", "lee", "max"} {
				if _, ok := a.subjects.get(s); ok != slices.Contains(tc.left, s) {
					t.Errorf("subject %s cached = %v, want %v", s, ok, !ok)
				}
			}
			if got := a.resolver.Epoch(); got != epoch+1 {
				t.Errorf("resolution epoch moved from %d to %d, want one bump", epoch, got)
			}
			if fmt.Sprint(a.applied.roles) != fmt.Sprint(tc.state.RoleIDs) ||
				fmt.Sprint(a.applied.edges) != fmt.Sprint(tc.state.Implies) {
				t.Errorf("applied = %+v, want the state's generation, roles and edges", a.applied)
			}
		})
	}
}

// TestRetryApplyDoublesToAMinute pins the retry's delays: 5 seconds after
// an apply that settled, doubling to one minute, and 5 seconds again once
// an apply settles.
func TestRetryApplyDoublesToAMinute(t *testing.T) {
	// A lifetime that ended arms timers that do nothing when they fire.
	life, cancel := context.WithCancel(context.Background())
	cancel()
	a := &App{lifetime: life}
	a.configMu.Lock()
	defer a.configMu.Unlock()
	var got []time.Duration
	for range 6 {
		got = append(got, a.retryApply())
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute}
	if !slices.Equal(got, want) {
		t.Errorf("delays = %v, want %v", got, want)
	}
	a.applySettled()
	if a.applyRetry != nil {
		t.Error("a settled apply left a retry armed")
	}
	if wait := a.retryApply(); wait != 5*time.Second {
		t.Errorf("the first delay after a settled apply = %s, want 5s", wait)
	}
	a.applySettled()
}

// TestSwapToKeepsASnapshotSwappedInAfterTheRead pins step 4's guard: when
// the live snapshot is no longer the one the apply read against, because
// the activate route swapped a newer one in outside a.configMu, the apply
// leaves it. While it is, the apply adopts the state's snapshot, and a
// state with no active snapshot swaps nothing.
func TestSwapToKeepsASnapshotSwappedInAfterTheRead(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	read := app.snapshots.Current().ID
	build := func(name string) snapshot.Built {
		b, err := app.snapshots.Build(ctx, read, map[string][]byte{name: []byte(policyText(name, 100, allowEchoapp))})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	state, newer := build("from-state"), build("from-activate")
	snap := store.Snapshot{ID: state.ID, SignerKeyID: state.SignerKeyID, Blob: state.Blob}
	app.snapshots.Adopt(newer)

	if err := app.swapTo(ctx, snap, read); err != nil || app.snapshots.Current().ID != newer.ID {
		t.Errorf("swapTo over a newer live snapshot = %v, live %s; want %s kept", err, app.snapshots.Current().ID, newer.ID)
	}
	if err := app.swapTo(ctx, store.Snapshot{}, newer.ID); err != nil || app.snapshots.Current().ID != newer.ID {
		t.Errorf("swapTo with no active snapshot = %v, live %s; want nothing swapped", err, app.snapshots.Current().ID)
	}
	if err := app.swapTo(ctx, snap, newer.ID); err != nil || app.snapshots.Current().ID != state.ID {
		t.Errorf("swapTo while the read one is live = %v, live %s; want %s", err, app.snapshots.Current().ID, state.ID)
	}
}

// TestAdoptWhileLiveChecksAtTheAdopt pins where step 4's guard sits: in the
// adopt, after the state's snapshot was opened, which takes milliseconds. A
// snapshot the activate route swapped in while it was opened stays live,
// and while the read one is live the state's snapshot is adopted.
func TestAdoptWhileLiveChecksAtTheAdopt(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	read := app.snapshots.Current().ID
	build := func(name string) snapshot.Built {
		b, err := app.snapshots.Build(ctx, read, map[string][]byte{name: []byte(policyText(name, 100, allowEchoapp))})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	state, newer := build("from-state"), build("from-activate")
	app.snapshots.Adopt(newer)

	app.adoptWhileLive(state, read)
	if got := app.snapshots.Current().ID; got != newer.ID {
		t.Errorf("the adopt over a snapshot swapped in meanwhile left %s live, want %s", got, newer.ID)
	}
	app.adoptWhileLive(state, newer.ID)
	if got := app.snapshots.Current().ID; got != state.ID {
		t.Errorf("the adopt while the read snapshot is live left %s live, want %s", got, state.ID)
	}
}

// TestBootAppliesLiveStateBeforeNewReturns pins the boot's apply: by the
// time New returns, before any session exists, the replica has applied
// live state and recorded every role of the store by its id, with the
// generation it read, so no later apply drops a session for want of it.
func TestBootAppliesLiveStateBeforeNewReturns(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	roles, err := app.store.Roles().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := app.store.Drafts().Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app.configMu.Lock()
	applied := app.applied
	app.configMu.Unlock()
	if len(roles) == 0 || len(applied.roles) != len(roles) || app.appliedGen.Load() != gen {
		t.Fatalf("applied %d roles at generation %d at boot, want the store's %d at %d", len(applied.roles), app.appliedGen.Load(), len(roles), gen)
	}
	for _, r := range roles {
		if applied.roles[r.Name] != r.ID {
			t.Errorf("role %s is recorded as %q, want its id %q", r.Name, applied.roles[r.Name], r.ID)
		}
	}
}

// TestPublishMakesTheSessionCheckInAgainWithoutTheImpliedRole is the
// single-replica case: a publish that removes an implication drops the
// session of a holder, whose next call answers check in again, and the
// check-in resolves the roles without the implied role and its tools.
func TestPublishMakesTheSessionCheckInAgainWithoutTheImpliedRole(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	up := startGatewayUpstream(t)
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := app.store.Roles().Create(ctx, store.Role{Name: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Roles().AddImplication(ctx, dev.ID, reader.ID); err != nil {
		t.Fatal(err)
	}
	seedGatewayUser(t, app, "kim", "dev")
	echo := putServer(t, app, echoManifest(up.URL))
	setAccess(t, app, reader.ID, echo, true, `["*"]`)
	activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	tok := sessionToken(t, base, "kim")
	if _, list, _ := mcpCall(t, base, tok, "tools/list", nil); !slices.Contains(toolNamesOf(t, list), "echoapp__echo") {
		t.Fatalf("before the publish kim lists %v, want echoapp__echo through reader", toolNamesOf(t, list))
	}

	if err := app.store.Roles().RemoveImplication(ctx, dev.ID, reader.ID); err != nil {
		t.Fatal(err)
	}
	if err := publishOn(ctx, app, &publishHead{Before: map[string][]string{"dev": {"reader"}}, After: map[string][]string{}}); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := mcpCall(t, base, tok, "tools/list", nil); code != http.StatusUnauthorized {
		t.Fatalf("the call after the publish = %d, want 401 to check in again", code)
	}
	tok = checkInAgain(t, base, tok)
	if _, list, _ := mcpCall(t, base, tok, "tools/list", nil); len(toolNamesOf(t, list)) != 0 {
		t.Errorf("after checking in again kim lists %v, want nothing", toolNamesOf(t, list))
	}
	if sub, _ := app.subjects.get(sessionOf(t, app, tok)); slices.Contains(sub.Roles, "reader") {
		t.Errorf("kim's roles after checking in again = %v, want no reader", sub.Roles)
	}
}

// TestPublishDropsTheSessionOfARoleThatGainedAnImplication: a publish that
// makes dev imply reader, whose policy denies env, drops the session of kim,
// who holds dev and could call env before it, so the next call answers
// check in again and the call after the check-in is denied. The read of
// live state fails, so the head alone drops the session. A session kept on
// its old closure would keep calling env until it checked in on its own.
func TestPublishDropsTheSessionOfARoleThatGainedAnImplication(t *testing.T) {
	t.Parallel()
	var fs *liveFails
	app, base := testAppPreRun(t, []func(*App){wrapLive(&fs, nil)})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	up := startGatewayUpstream(t)
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := app.store.Roles().Create(ctx, store.Role{Name: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	seedGatewayUser(t, app, "kim", "dev")
	setAccess(t, app, dev.ID, putServer(t, app, echoManifest(up.URL)), true, `["*"]`)
	activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp),
		"gate": strings.Replace(policyText("gate", 900, denyEnv), "[dev]", "[reader]", 1)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	tok := sessionToken(t, base, "kim")
	kim := sessionOf(t, app, tok)
	if got := gatewayDecides(app, kim, "echoapp__env"); got != callRuns {
		t.Fatalf("before the publish kim's env call decides %s, want runs", got)
	}

	if err := app.store.Roles().AddImplication(ctx, dev.ID, reader.ID); err != nil {
		t.Fatal(err)
	}
	fs.fail.Store(true)
	if err := publishOn(ctx, app, &publishHead{Before: map[string][]string{}, After: map[string][]string{"dev": {"reader"}}}); err == nil {
		t.Fatal("the publish's read did not fail")
	}
	if got := gatewayDecides(app, kim, "echoapp__env"); got != callCheckIn {
		t.Errorf("kim's env call after the head decides %s, want check in", got)
	}
	fs.fail.Store(false)
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	if got := gatewayDecides(app, sessionOf(t, app, checkInAgain(t, base, tok)), "echoapp__env"); got != callDenied {
		t.Errorf("kim's env call after checking in again decides %s, want denied through reader", got)
	}
}

// TestApplyStopsAtAFailedReadAndRetries: a publish whose read of live state
// fails keeps the head's narrower state, the row that went dropped and the
// row that came not yet added, logs the step with its retry, and the retry
// that reads live state adds the row.
func TestApplyStopsAtAFailedReadAndRetries(t *testing.T) {
	t.Parallel()
	var fs *liveFails
	var logs *syncBuffer
	app, base := testAppPreRun(t, []func(*App){wrapLive(&fs, &logs)})
	// The retry an apply arms runs on its context, which ends with the test.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	up := startGatewayUpstream(t)
	for _, name := range []string{"dev", "ops"} {
		if _, err := app.store.Roles().Create(ctx, store.Role{Name: name, Kind: store.RoleKindApplication}); err != nil {
			t.Fatal(err)
		}
	}
	seedGatewayUser(t, app, "kim", "dev")
	seedGatewayUser(t, app, "lee", "ops")
	dev, _ := app.store.Roles().GetByName(ctx, "dev")
	ops, _ := app.store.Roles().GetByName(ctx, "ops")
	echo := putServer(t, app, echoManifest(up.URL))
	setAccess(t, app, dev.ID, echo, true, `["*"]`)
	activateSets(t, app, map[string]string{"base": strings.Replace(policyText("base", 100, allowEchoapp), "[dev]", "[dev, ops]", 1)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	kim, lee := sessionOf(t, app, sessionToken(t, base, "kim")), sessionOf(t, app, sessionToken(t, base, "lee"))
	if k, l := gatewayDecides(app, kim, "echoapp__echo"), gatewayDecides(app, lee, "echoapp__echo"); k != callRuns || l != callUnknown {
		t.Fatalf("before the publish kim %s and lee %s, want runs and unknown", k, l)
	}

	setAccess(t, app, dev.ID, echo, false, "")
	setAccess(t, app, ops.ID, echo, true, `["*"]`)
	fs.fail.Store(true)
	err := publishOn(ctx, app, &publishHead{AccessRoles: []string{"dev", "ops"}})
	if err == nil || !strings.Contains(err.Error(), "the read of live state") {
		t.Fatalf("publish with a failing read = %v, want the read's failure", err)
	}
	if k, l := gatewayDecides(app, kim, "echoapp__echo"), gatewayDecides(app, lee, "echoapp__echo"); k != callUnknown || l != callUnknown {
		t.Errorf("after the head kim %s and lee %s, want unknown for both", k, l)
	}
	const logged = "The config apply stopped at the read of live state on this replica: database unavailable. " +
		"It serves the narrower state it reached and tries again in 5s."
	if !strings.Contains(logs.String(), logged) {
		t.Errorf("log lacks %q:\n%s", logged, logs.String())
	}

	fs.fail.Store(false)
	app.configMu.Lock()
	armed := app.applyRetry != nil
	if armed {
		app.applyRetry.Reset(time.Millisecond)
	}
	app.configMu.Unlock()
	if !armed {
		t.Fatal("the failed apply armed no retry")
	}
	waitFor(t, "the retry settles", func() bool {
		app.configMu.Lock()
		defer app.configMu.Unlock()
		return app.applyRetry == nil
	})
	if k, l := gatewayDecides(app, kim, "echoapp__echo"), gatewayDecides(app, lee, "echoapp__echo"); k != callUnknown || l != callRuns {
		t.Errorf("after the retry kim %s and lee %s, want unknown and runs", k, l)
	}
}

// TestApplyNarrowsBeforeAStopThatFails pins what runs ahead of the stop:
// with the stop refusing the state's read time, the apply ends at the stop,
// and the rows of a server or a role removed since are already gone, the
// removed role's sessions with them, so a call through them is unknown or
// must check in while the apply retries. The retry, once the stop can run,
// stops the removed server and leaves the other one running.
func TestApplyNarrowsBeforeAStopThatFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		remove func(t *testing.T, app *App, dev store.Role, echo store.App, kim store.User)
		want   outcome
		runs   bool
	}{
		{name: "the rows of a removed server", want: callUnknown, remove: func(t *testing.T, app *App, _ store.Role, echo store.App, _ store.User) {
			if err := app.store.Apps().SoftDelete(context.Background(), echo.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "the rows and sessions of a removed role", want: callCheckIn, runs: true, remove: func(t *testing.T, app *App, dev store.Role, _ store.App, kim store.User) {
			ctx := context.Background()
			held, err := app.store.Roles().ListAssignments(ctx, store.SubjectUser, kim.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, as := range held {
				if err := app.store.Roles().Unassign(ctx, as.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := app.store.Roles().Delete(ctx, dev.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var fs *liveFails
			app, base := testAppPreRun(t, []func(*App){wrapLive(&fs, nil)})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			up := startGatewayUpstream(t)
			dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
			if err != nil {
				t.Fatal(err)
			}
			kim := seedGatewayUser(t, app, "kim", "dev")
			echo := putServer(t, app, echoManifest(up.URL))
			setAccess(t, app, dev.ID, echo, true, `["*"]`)
			activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp)})
			if err := app.converge(ctx); err != nil {
				t.Fatal(err)
			}
			session := sessionOf(t, app, sessionToken(t, base, "kim"))
			if got := gatewayDecides(app, session, "echoapp__echo"); got != callRuns {
				t.Fatalf("before the removal kim's call decides %s, want runs", got)
			}

			tc.remove(t, app, dev, echo, kim)
			fs.noReadAt.Store(true)
			err = app.converge(ctx)
			if err == nil || !strings.Contains(err.Error(), "the stop of the servers whose row changed") {
				t.Fatalf("apply with a failing stop = %v, want the stop's failure", err)
			}
			if got := gatewayDecides(app, session, "echoapp__echo"); got != tc.want {
				t.Errorf("kim's call after the failed stop decides %s, want %s", got, tc.want)
			}
			if _, live := app.manager.View("echoapp"); !live {
				t.Fatal("the failed stop stopped the server")
			}

			fs.noReadAt.Store(false)
			app.configMu.Lock()
			armed := app.applyRetry != nil
			if armed {
				app.applyRetry.Reset(time.Millisecond)
			}
			app.configMu.Unlock()
			if !armed {
				t.Fatal("the failed apply armed no retry")
			}
			waitFor(t, "the retry settles", func() bool {
				app.configMu.Lock()
				defer app.configMu.Unlock()
				return app.applyRetry == nil
			})
			if _, live := app.manager.View("echoapp"); live != tc.runs {
				t.Errorf("after the retry echoapp runs = %v, want %v", live, tc.runs)
			}
			if got := gatewayDecides(app, session, "echoapp__echo"); got != tc.want {
				t.Errorf("kim's call after the retry decides %s, want %s", got, tc.want)
			}
		})
	}
}

// TestApplyStopsAtAFailedSwapWithAccessNarrowed: an apply whose snapshot
// swap fails has already narrowed access to the rows live state holds, so
// an access row that went stays gone while the old policy decides, and the
// step is logged with its retry.
func TestApplyStopsAtAFailedSwapWithAccessNarrowed(t *testing.T) {
	t.Parallel()
	var fs *liveFails
	var logs *syncBuffer
	app, base := testAppPreRun(t, []func(*App){wrapLive(&fs, &logs)})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	up := startGatewayUpstream(t)
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	seedGatewayUser(t, app, "kim", "dev")
	echo := putServer(t, app, echoManifest(up.URL))
	setAccess(t, app, dev.ID, echo, true, `["*"]`)
	activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	kim := sessionOf(t, app, sessionToken(t, base, "kim"))

	setAccess(t, app, dev.ID, echo, false, "")
	if _, err := app.store.Snapshots().Create(ctx, store.Snapshot{ID: "unopenable", SignerKeyID: "nobody", Blob: []byte("not a snapshot")}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.Snapshots().SetActive(ctx, "unopenable"); err != nil {
		t.Fatal(err)
	}
	err = app.converge(ctx)
	if err == nil || !strings.Contains(err.Error(), "the policy snapshot swap") {
		t.Fatalf("apply over an unopenable snapshot = %v, want the swap's failure", err)
	}
	if got := gatewayDecides(app, kim, "echoapp__echo"); got != callUnknown {
		t.Errorf("kim's call after the failed swap decides %s, want unknown: the row went", got)
	}
	const logged = "The config apply stopped at the policy snapshot swap on this replica: open the policy snapshot unopenable with the snapshot signing keys: "
	if !strings.Contains(logs.String(), logged) || !strings.Contains(logs.String(), "It serves the narrower state it reached and tries again in 5s.") {
		t.Errorf("log lacks the swap's failure and its retry:\n%s", logs.String())
	}
}

// TestPublishStopsAnInstanceOfTheRowItReplaced: an instance started from a
// server's row after the publish read its base, here by an enable, runs the
// row the publish replaced, so the head stops it even when the read of live
// state that follows fails.
func TestPublishStopsAnInstanceOfTheRowItReplaced(t *testing.T) {
	t.Parallel()
	var fs *liveFails
	app, _ := testAppPreRun(t, []func(*App){wrapLive(&fs, nil)})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	putServer(t, app, string(taggedManifest(startTaggedUpstream(t, "one").URL)))
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	// The publish reads its base here. The enable that follows starts an
	// instance of the row as that read saw it.
	if _, err := app.manager.Disable(ctx, "tagged"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.manager.Enable(ctx, "tagged"); err != nil {
		t.Fatal(err)
	}
	if got, err := whoamiOn(ctx, app); err != nil || got != "one" {
		t.Fatalf("the enabled server answers %q, %v; want one", got, err)
	}

	putServer(t, app, string(taggedManifest(startTaggedUpstream(t, "two").URL)))
	fs.fail.Store(true)
	_ = publishOn(ctx, app, &publishHead{Servers: []string{"tagged"}})
	if got, err := whoamiOn(ctx, app); err == nil {
		t.Errorf("the replaced row still serves after the head, answering %q", got)
	}
}

// TestPublishHeadHoldsWhenTheReadFails pins the head's policy and role
// steps on their own: with the read of live state failing, the snapshot the
// publish built is live, and a session whose role lost an implication must
// check in again.
func TestPublishHeadHoldsWhenTheReadFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		check func(t *testing.T, app *App, head *publishHead, kim string)
	}{
		{name: "the snapshot the publish built", check: func(t *testing.T, app *App, head *publishHead, _ string) {
			if got := app.snapshots.Current().ID; got != head.Snapshot.ID {
				t.Errorf("live snapshot after the head = %s, want %s", got, head.Snapshot.ID)
			}
		}},
		{name: "the session of a role that lost an implication", check: func(t *testing.T, app *App, _ *publishHead, kim string) {
			if got := gatewayDecides(app, kim, "echoapp__echo"); got != callCheckIn {
				t.Errorf("kim's call after the head decides %s, want check in", got)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var fs *liveFails
			app, base := testAppPreRun(t, []func(*App){wrapLive(&fs, nil)})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			up := startGatewayUpstream(t)
			dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
			if err != nil {
				t.Fatal(err)
			}
			reader, err := app.store.Roles().Create(ctx, store.Role{Name: "reader"})
			if err != nil {
				t.Fatal(err)
			}
			if err := app.store.Roles().AddImplication(ctx, dev.ID, reader.ID); err != nil {
				t.Fatal(err)
			}
			seedGatewayUser(t, app, "kim", "dev")
			setAccess(t, app, reader.ID, putServer(t, app, echoManifest(up.URL)), true, `["*"]`)
			activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp)})
			if err := app.converge(ctx); err != nil {
				t.Fatal(err)
			}
			kim := sessionOf(t, app, sessionToken(t, base, "kim"))
			if got := gatewayDecides(app, kim, "echoapp__echo"); got != callRuns {
				t.Fatalf("before the publish kim's call decides %s, want runs", got)
			}

			if err := app.store.Roles().RemoveImplication(ctx, dev.ID, reader.ID); err != nil {
				t.Fatal(err)
			}
			head := &publishHead{Before: map[string][]string{"dev": {"reader"}}, After: map[string][]string{},
				Snapshot: activateSets(t, app, map[string]string{"gate": policyText("gate", 900, denyEnv)})}
			fs.fail.Store(true)
			if err := publishOn(ctx, app, head); err == nil {
				t.Fatal("the publish's read did not fail")
			}
			tc.check(t, app, head, kim)
		})
	}
}

// TestApplyStartsAServerWhoseSecretArrived is the server-level twin of the
// manager's test: a command server that replica b parked for want of its
// secret starts on b's next apply once replica a set the secret, because
// the apply refreshes b's credential cache before it judges the server.
func TestApplyStartsAServerWhoseSecretArrived(t *testing.T) {
	t.Parallel()
	a, b, _, _ := replicaPair(t)
	ctx := context.Background()
	row := putServer(t, a, `
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: parked}
server: {name: straza.test/parked, version: "1.0.0"}
straza:
  runtime:
    kind: command
    command: {exec: /bin/sh, args: ["-c", "exec sleep 30"]}
  credential: {kind: static, inject: {as: env, name: TOKEN}}
`)
	status := func() string {
		v, ok := b.manager.View("parked")
		if !ok {
			return "no instance"
		}
		return v.Status
	}
	for range 2 {
		if err := b.converge(ctx); err != nil {
			t.Fatal(err)
		}
		if got := status(); got != manager.StatusPending {
			t.Fatalf("b's server with no secret is %s, want pending", got)
		}
	}

	if _, err := a.broker.Set(ctx, row.ID, "", "tok-arrived"); err != nil {
		t.Fatal(err)
	}
	a.manager.SecretUpdated(ctx, row.ID)
	if err := b.converge(ctx); err != nil {
		t.Fatal(err)
	}
	if got := status(); got == manager.StatusPending || got == "no instance" {
		t.Errorf("b's server after a set the secret is %s, want started", got)
	}
}

// TestStartLiveSkipsARemovalPublishedWhileItWaits: a start that waits for
// the manager's lock, held here by an install whose server does not answer
// yet, reads the row again once it holds the lock, so a server removed in
// the meantime does not come back.
func TestStartLiveSkipsARemovalPublishedWhileItWaits(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	row := putServer(t, app, string(taggedManifest(startTaggedUpstream(t, "one").URL)))
	st, err := app.store.Drafts().LiveState(ctx, app.snapshots.Current().ID)
	if err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	var reach, free sync.Once
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reach.Do(func() { close(reached) })
		<-release
		http.Error(w, "not yet", http.StatusServiceUnavailable)
	}))
	t.Cleanup(hang.Close)
	unblock := func() { free.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	slow, err := manager.Parse([]byte(strings.ReplaceAll(echoManifest(hang.URL), "echoapp", "slow")))
	if err != nil {
		t.Fatal(err)
	}
	installed := make(chan error, 1)
	go func() {
		_, err := app.manager.Install(ctx, slow, store.AppSourceAPI)
		installed <- err
	}()
	<-reached
	started := make(chan map[string]error, 1)
	go func() { started <- app.startLive(ctx, st, nil) }()
	// Long enough for startLive to reach the lock on any box; a slower box
	// can only make the test pass without testing, never fail.
	time.Sleep(50 * time.Millisecond)
	if err := app.store.Apps().SoftDelete(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-installed; err != nil {
		t.Fatal(err)
	}
	<-started
	if _, live := app.manager.View("tagged"); live {
		t.Error("startLive started a server removed while it waited for the lock")
	}
}

// TestApplyKeepsAServerInstalledAfterItsRead: an install through the
// manager, as the apps directory watcher makes one, that lands after an
// apply read live state keeps running, because its instance is younger than
// the read.
func TestApplyKeepsAServerInstalledAfterItsRead(t *testing.T) {
	t.Parallel()
	var fs *liveFails
	app, _ := testAppPreRun(t, []func(*App){wrapLive(&fs, nil)})
	ctx := context.Background()
	putServer(t, app, string(taggedManifest(startTaggedUpstream(t, "one").URL)))
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	two, err := manager.Parse(taggedManifest(startTaggedUpstream(t, "two").URL))
	if err != nil {
		t.Fatal(err)
	}
	install := func() {
		if _, err := app.manager.Install(ctx, two, store.AppSourceGitops); err != nil {
			t.Error(err)
		}
	}
	fs.after.Store(&install)

	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := whoamiOn(ctx, app); err != nil || got != "two" {
		t.Errorf("after the apply the server answers %q, %v; want two, the install after the read", got, err)
	}
}

// replicaWorld is the deployment the event-order test publishes into: two
// replicas, the upstream of echoapp, the roles dev and reader, kim, who
// holds dev, and the row of echoapp.
type replicaWorld struct {
	a, b        *App
	baseB       string
	up          *gatewayUpstream
	dev, reader store.Role
	echo        store.App
}

// TestReplicaFedAPublishInEveryOrderIsNeverWider feeds replica b the events
// of a publish made on replica a in every order, through b's own converge
// consumer. After each event every call kim makes through b's gateway
// decides as it did before the publish, as it does after it, or narrower
// than both, never wider than either. Once kim checks in again, b decides
// as after the publish.
func TestReplicaFedAPublishInEveryOrderIsNeverWider(t *testing.T) {
	t.Parallel()
	const draft = "7"
	base := policyText("base", 100, allowEchoapp)
	gate := func(on bool, text string) string {
		if on {
			return text
		}
		return ""
	}
	cases := []struct {
		name string
		// write leaves the store as the publish leaves it when after holds,
		// and as it was before otherwise, and answers the snapshot.
		write         func(t *testing.T, w *replicaWorld, after bool) *snapshot.Built
		head          func(w *replicaWorld, snap *snapshot.Built) *publishHead
		events        func(w *replicaWorld, snap *snapshot.Built) []replicaEvent
		before, after map[string]outcome
	}{
		{
			name: "an access row and its gate added together",
			write: func(t *testing.T, w *replicaWorld, after bool) *snapshot.Built {
				setAccess(t, w.a, w.dev.ID, w.echo, after, `["echo","env"]`)
				return activateSets(t, w.a, map[string]string{"base": base, "gate": gate(after, policyText("gate", 900, denyEnv))})
			},
			head: func(_ *replicaWorld, snap *snapshot.Built) *publishHead {
				return &publishHead{AccessRoles: []string{"dev"}, Snapshot: snap}
			},
			events: func(w *replicaWorld, snap *snapshot.Built) []replicaEvent {
				return []replicaEvent{
					{"straza.identity.updated", map[string]any{"id": w.dev.ID, "draft": draft}},
					{"straza.apps.updated", map[string]any{"change": "binding", "app": w.echo.ID, "draft": draft}},
					{"straza.policy.updated", map[string]any{"snapshot": snap.ID, "sets": snap.Sets, "draft": draft}},
					{"straza.apps.updated", map[string]any{"change": "publish", "snapshot": snap.ID, "apps": []string{}, "draft": draft}},
				}
			},
			before: map[string]outcome{"echoapp__echo": callUnknown, "echoapp__env": callUnknown},
			after:  map[string]outcome{"echoapp__echo": callRuns, "echoapp__env": callDenied},
		},
		{
			name: "an implication and its gate removed together",
			write: func(t *testing.T, w *replicaWorld, after bool) *snapshot.Built {
				setAccess(t, w.a, w.reader.ID, w.echo, true, `["*"]`)
				ctx := context.Background()
				if after {
					if err := w.a.store.Roles().RemoveImplication(ctx, w.dev.ID, w.reader.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
						t.Fatal(err)
					}
				} else if err := w.a.store.Roles().AddImplication(ctx, w.dev.ID, w.reader.ID); err != nil && !errors.Is(err, store.ErrConflict) {
					t.Fatal(err)
				}
				return activateSets(t, w.a, map[string]string{"base": base, "gate": gate(!after, policyText("gate", 900, denyEnv))})
			},
			head: func(_ *replicaWorld, snap *snapshot.Built) *publishHead {
				return &publishHead{Before: map[string][]string{"dev": {"reader"}}, After: map[string][]string{}, Snapshot: snap}
			},
			events: func(w *replicaWorld, snap *snapshot.Built) []replicaEvent {
				return []replicaEvent{
					{"straza.identity.updated", map[string]any{"id": w.dev.ID, "draft": draft}},
					{"straza.policy.updated", map[string]any{"snapshot": snap.ID, "sets": snap.Sets, "draft": draft}},
					{"straza.apps.updated", map[string]any{"change": "publish", "snapshot": snap.ID, "apps": []string{}, "draft": draft}},
				}
			},
			before: map[string]outcome{"echoapp__echo": callRuns, "echoapp__env": callDenied},
			after:  map[string]outcome{"echoapp__echo": callUnknown, "echoapp__env": callUnknown},
		},
		{
			name: "a narrower exposure and the hold it made moot dropped together",
			write: func(t *testing.T, w *replicaWorld, after bool) *snapshot.Built {
				setAccess(t, w.a, w.dev.ID, w.echo, true, `["*"]`)
				doc := echoManifest(w.up.URL)
				if after {
					doc += "  exposure: {tools: [echo]}\n"
				}
				putServer(t, w.a, doc)
				return activateSets(t, w.a, map[string]string{"base": base, "hold": gate(!after, policyText("hold", 900, holdEnv))})
			},
			head: func(_ *replicaWorld, snap *snapshot.Built) *publishHead {
				return &publishHead{Servers: []string{"echoapp"}, Snapshot: snap}
			},
			events: func(_ *replicaWorld, snap *snapshot.Built) []replicaEvent {
				return []replicaEvent{
					{"straza.policy.updated", map[string]any{"snapshot": snap.ID, "sets": snap.Sets, "draft": draft}},
					{"straza.apps.updated", map[string]any{"change": "publish", "snapshot": snap.ID, "apps": []string{"echoapp"}, "draft": draft}},
				}
			},
			before: map[string]outcome{"echoapp__echo": callRuns, "echoapp__env": callHeld},
			after:  map[string]outcome{"echoapp__echo": callRuns, "echoapp__env": callUnknown},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			w := &replicaWorld{up: startGatewayUpstream(t)}
			w.a, w.b, _, w.baseB = replicaPair(t)
			var err error
			if w.dev, err = w.a.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication}); err != nil {
				t.Fatal(err)
			}
			if w.reader, err = w.a.store.Roles().Create(ctx, store.Role{Name: "reader"}); err != nil {
				t.Fatal(err)
			}
			seedGatewayUser(t, w.a, "kim", "dev")
			w.echo = putServer(t, w.a, echoManifest(w.up.URL))
			tc.write(t, w, false)
			if err := w.b.converge(ctx); err != nil {
				t.Fatal(err)
			}
			tok := sessionToken(t, w.baseB, "kim")

			for _, order := range orders(len(tc.events(w, &snapshot.Built{}))) {
				tc.write(t, w, false)
				for _, app := range []*App{w.a, w.b} {
					if err := app.converge(ctx); err != nil {
						t.Fatal(err)
					}
				}
				tok = checkInAgain(t, w.baseB, tok)
				session := sessionOf(t, w.b, tok)
				if got := decidesAll(w.b, session); fmt.Sprint(got) != fmt.Sprint(tc.before) {
					t.Fatalf("before the publish b decides %v, want %v", got, tc.before)
				}

				snap := tc.write(t, w, true)
				if err := publishOn(ctx, w.a, tc.head(w, snap)); err != nil {
					t.Fatal(err)
				}
				events := tc.events(w, snap)
				for i, idx := range order {
					w.b.convCons.Handle(ctx, events[idx].subject, events[idx].payload(t, w.a.instance))
					for tool, got := range decidesAll(w.b, session) {
						if !within(got, tc.before[tool], tc.after[tool]) {
							t.Errorf("order %v, after event %d (%s): %s decides %s, wider than before (%s) and after (%s)",
								order, i, events[idx].subject, tool, got, tc.before[tool], tc.after[tool])
						}
					}
				}
				tok = checkInAgain(t, w.baseB, tok)
				if got := decidesAll(w.b, sessionOf(t, w.b, tok)); fmt.Sprint(got) != fmt.Sprint(tc.after) {
					t.Errorf("order %v: once kim checks in again b decides %v, want %v", order, got, tc.after)
				}
			}
		})
	}
}

// holdImpl is a store for a resolver whose ListImplications, once armed,
// answers only after release closes, so a check-in's role resolution
// straddles whatever the test runs in between.
type holdImpl struct {
	store.Store
	armed   atomic.Bool
	reached chan struct{}
	release chan struct{}
}

func (s *holdImpl) Roles() store.RoleRepo { return holdRoles{s.Store.Roles(), s} }

type holdRoles struct {
	store.RoleRepo
	s *holdImpl
}

func (r holdRoles) ListImplications(ctx context.Context) ([]store.RoleImplication, error) {
	out, err := r.RoleRepo.ListImplications(ctx)
	if r.s.armed.CompareAndSwap(true, false) {
		close(r.s.reached)
		<-r.s.release
	}
	return out, err
}

// checkInReply is a check-in's answer as a test reads it.
type checkInReply struct {
	code                 int
	sentence, retryAfter string
}

// checkInAnswer checks a session in again with its token from any goroutine
// and answers the status, the error sentence and the Retry-After header.
func checkInAnswer(base, tok string) (checkInReply, error) {
	raw, err := json.Marshal(map[string]any{
		"session_token": tok,
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{"self": "x"}},
	})
	if err != nil {
		return checkInReply{}, err
	}
	resp, err := http.Post(base+"/v1/checkin", "application/json", bytes.NewReader(raw))
	if err != nil {
		return checkInReply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	sentence, _ := out["error"].(string)
	return checkInReply{code: resp.StatusCode, sentence: sentence, retryAfter: resp.Header.Get("Retry-After")}, nil
}

// TestCheckInStraddlingAnApplyKeepsNoLostRole: kim's check-in reads the
// implications before a publish that removes dev->reader and the env gate
// together, and caches its subject after this replica's apply dropped kim's
// session, through the publishing replica's head and through the converge
// of another replica. kim's calls stay no wider than before or after.
func TestCheckInStraddlingAnApplyKeepsNoLostRole(t *testing.T) {
	t.Parallel()
	for _, via := range []string{"head", "converge"} {
		t.Run(via, func(t *testing.T) {
			t.Parallel()
			hold := &holdImpl{reached: make(chan struct{}), release: make(chan struct{})}
			app, base := testAppPreRun(t, []func(*App){func(app *App) {
				hold.Store = app.store
				app.resolver = identity.NewResolver(hold)
			}})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			up := startGatewayUpstream(t)
			dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
			if err != nil {
				t.Fatal(err)
			}
			reader, err := app.store.Roles().Create(ctx, store.Role{Name: "reader"})
			if err != nil {
				t.Fatal(err)
			}
			if err := app.store.Roles().AddImplication(ctx, dev.ID, reader.ID); err != nil {
				t.Fatal(err)
			}
			seedGatewayUser(t, app, "kim", "dev")
			setAccess(t, app, reader.ID, putServer(t, app, echoManifest(up.URL)), true, `["*"]`)
			activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp), "gate": policyText("gate", 900, denyEnv)})
			if err := app.converge(ctx); err != nil {
				t.Fatal(err)
			}
			tok := sessionToken(t, base, "kim")
			session := sessionOf(t, app, tok)
			before := decidesAll(app, session)
			after := map[string]outcome{"echoapp__echo": callUnknown, "echoapp__env": callUnknown}

			// An unrelated identity event empties the resolution cache, and
			// kim's client checks in again while the publish lands.
			app.resolver.Bump()
			hold.armed.Store(true)
			done := make(chan error, 1)
			go func() {
				reply, err := checkInAnswer(base, tok)
				if err == nil && reply.code != http.StatusOK {
					err = fmt.Errorf("check-in = %d %q", reply.code, reply.sentence)
				}
				done <- err
			}()
			select {
			case <-hold.reached:
			case <-time.After(10 * time.Second):
				t.Fatal("the check-in never read the implications")
			}
			if err := app.store.Roles().RemoveImplication(ctx, dev.ID, reader.ID); err != nil {
				t.Fatal(err)
			}
			snap := activateSets(t, app, map[string]string{"gate": ""})
			if via == "head" {
				err = publishOn(ctx, app, &publishHead{Before: map[string][]string{"dev": {"reader"}}, After: map[string][]string{}, Snapshot: snap})
			} else {
				err = app.converge(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			close(hold.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			for tool, got := range decidesAll(app, session) {
				if !within(got, before[tool], after[tool]) {
					t.Errorf("after the straddling check-in %s decides %s, wider than before (%s) and after (%s)", tool, got, before[tool], after[tool])
				}
			}
		})
	}
}

// TestCheckInAppliesAPublishNotAppliedYet: a publish on replica a adds
// dev->reader and the env gate together. Replica b has not applied it when
// an unrelated identity event empties b's resolution cache and kim checks
// in on b, so the check-in applies live state before it caches the roles it
// read, and kim's env call is denied as after the publish, never run.
func TestCheckInAppliesAPublishNotAppliedYet(t *testing.T) {
	t.Parallel()
	a, b, _, baseB := replicaPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	up := startGatewayUpstream(t)
	dev, err := a.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := a.store.Roles().Create(ctx, store.Role{Name: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	seedGatewayUser(t, a, "kim", "dev")
	setAccess(t, a, reader.ID, putServer(t, a, echoManifest(up.URL)), true, `["*"]`)
	activateSets(t, a, map[string]string{"base": policyText("base", 100, allowEchoapp)})
	for _, app := range []*App{a, b} {
		if err := app.converge(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tok := sessionToken(t, baseB, "kim")
	if got := decidesAll(b, sessionOf(t, b, tok)); got["echoapp__env"] != callUnknown {
		t.Fatalf("before the publish kim on b decides %v, want env unknown", got)
	}

	if err := a.store.Roles().AddImplication(ctx, dev.ID, reader.ID); err != nil {
		t.Fatal(err)
	}
	if err := publishOn(ctx, a, &publishHead{Snapshot: activateSets(t, a, map[string]string{"gate": policyText("gate", 900, denyEnv)})}); err != nil {
		t.Fatal(err)
	}
	// b has not applied the publish, whose generation the store holds.
	b.configMu.Lock()
	behind(b)
	b.configMu.Unlock()
	b.convCons.Handle(ctx, "straza.identity.updated", replicaEvent{"straza.identity.updated", map[string]any{"action": "session.start"}}.payload(t, a.instance))
	tok = checkInAgain(t, baseB, tok)
	got := decidesAll(b, sessionOf(t, b, tok))
	if want := (map[string]outcome{"echoapp__echo": callRuns, "echoapp__env": callDenied}); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("kim checked in on b before b applied the publish and decides %v, want %v", got, want)
	}
}

// holdAssignments is a store for a resolver whose ListAssignments, once
// armed, waits for release before it reads, so whatever the test runs in
// between lands before the check-in's roles are read.
type holdAssignments struct {
	store.Store
	armed   atomic.Bool
	reached chan struct{}
	release chan struct{}
}

func (s *holdAssignments) Roles() store.RoleRepo { return holdAssignmentRoles{s.Store.Roles(), s} }

type holdAssignmentRoles struct {
	store.RoleRepo
	s *holdAssignments
}

func (r holdAssignmentRoles) ListAssignments(ctx context.Context, kind, id string) ([]store.RoleAssignment, error) {
	if r.s.armed.CompareAndSwap(true, false) {
		close(r.s.reached)
		<-r.s.release
	}
	return r.RoleRepo.ListAssignments(ctx, kind, id)
}

// TestCheckInReadsTheGenerationAfterTheRoles: a publish that adds
// dev->reader and the env gate together commits after kim's check-in began
// and before it reads the roles, and this replica has not applied it. The
// check-in reads the config generation after the roles, sees the store
// ahead, applies, and so never caches the new roles under the old policy.
func TestCheckInReadsTheGenerationAfterTheRoles(t *testing.T) {
	t.Parallel()
	hold := &holdAssignments{reached: make(chan struct{}), release: make(chan struct{})}
	app, base := testAppPreRun(t, []func(*App){func(app *App) {
		hold.Store = app.store
		app.resolver = identity.NewResolver(hold)
	}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	up := startGatewayUpstream(t)
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := app.store.Roles().Create(ctx, store.Role{Name: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	seedGatewayUser(t, app, "kim", "dev")
	setAccess(t, app, reader.ID, putServer(t, app, echoManifest(up.URL)), true, `["*"]`)
	activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	tok := sessionToken(t, base, "kim")

	app.resolver.Bump()
	hold.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		reply, err := checkInAnswer(base, tok)
		if err == nil && reply.code != http.StatusOK {
			err = fmt.Errorf("check-in = %d %q", reply.code, reply.sentence)
		}
		done <- err
	}()
	select {
	case <-hold.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the check-in never read the assignments")
	}
	// The publish commits, and this replica has not applied its generation.
	if err := app.store.Roles().AddImplication(ctx, dev.ID, reader.ID); err != nil {
		t.Fatal(err)
	}
	activateSets(t, app, map[string]string{"gate": policyText("gate", 900, denyEnv)})
	app.configMu.Lock()
	behind(app)
	app.configMu.Unlock()
	close(hold.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := decidesAll(app, sessionOf(t, app, tok))
	if want := (map[string]outcome{"echoapp__echo": callRuns, "echoapp__env": callDenied}); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("after the check-in kim decides %v, want %v", got, want)
	}
}

// TestCheckInRefusesWhenItCannotSettleTheRoles pins the check-in's two
// refusals: an apply it needs first fails, or the roles move under every
// try. Each answers 503 with the sentence that says what to do and a
// Retry-After, as the check-in's other retryable refusals do.
func TestCheckInRefusesWhenItCannotSettleTheRoles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// arm makes the next check-in on app meet the failure.
		arm  func(app *App, fs *liveFails, moving *atomic.Bool)
		want string
	}{
		{name: "the apply a check-in needs first fails", want: checkInApplyFailed, arm: func(app *App, fs *liveFails, _ *atomic.Bool) {
			app.configMu.Lock()
			behind(app)
			app.configMu.Unlock()
			fs.fail.Store(true)
		}},
		{name: "the roles move under every try", want: checkInRolesMoving, arm: func(app *App, _ *liveFails, moving *atomic.Bool) {
			moving.Store(true)
			app.resolver.Bump() // so no cached resolution answers without a read
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var fs *liveFails
			var moving atomic.Bool
			var resolver *identity.Resolver
			app, base := testAppPreRun(t, []func(*App){wrapLive(&fs, nil), func(app *App) {
				resolver = identity.NewResolver(&movingImpl{Store: app.store, moving: &moving, resolver: &resolver})
				app.resolver = resolver
			}})
			seedGatewayUser(t, app, "kim", "dev")
			tok := sessionToken(t, base, "kim")

			tc.arm(app, fs, &moving)
			reply, err := checkInAnswer(base, tok)
			if err != nil {
				t.Fatal(err)
			}
			if reply.code != http.StatusServiceUnavailable || reply.sentence != tc.want || reply.retryAfter != loginOutageRetryAfter {
				t.Errorf("check-in = %d %q Retry-After %q, want 503 %q Retry-After %q",
					reply.code, reply.sentence, reply.retryAfter, tc.want, loginOutageRetryAfter)
			}
		})
	}
}

// movingImpl is a store for a resolver whose ListImplications, while moving
// holds, bumps that resolver as an apply's role step does, so every
// resolution straddles one.
type movingImpl struct {
	store.Store
	moving   *atomic.Bool
	resolver **identity.Resolver
}

func (s *movingImpl) Roles() store.RoleRepo { return movingRoles{s.Store.Roles(), s} }

type movingRoles struct {
	store.RoleRepo
	s *movingImpl
}

func (r movingRoles) ListImplications(ctx context.Context) ([]store.RoleImplication, error) {
	out, err := r.RoleRepo.ListImplications(ctx)
	if r.s.moving.Load() {
		(*r.s.resolver).Bump()
	}
	return out, err
}

// bootHook is a store that runs fn once, at the first ToolBindings().List
// after the first Snapshots().GetActive: a publish that commits between a
// booting replica's snapshot load and its access table load.
type bootHook struct {
	store.Store
	armed, fired atomic.Bool
	fn           func()
}

func (s *bootHook) Snapshots() store.SnapshotRepo { return bootSnaps{s.Store.Snapshots(), s} }

func (s *bootHook) ToolBindings() store.ToolBindingRepo {
	return bootBindings{s.Store.ToolBindings(), s}
}

func (*bootHook) Close() error { return nil }

type bootSnaps struct {
	store.SnapshotRepo
	s *bootHook
}

func (r bootSnaps) GetActive(ctx context.Context) (store.Snapshot, error) {
	snap, err := r.SnapshotRepo.GetActive(ctx)
	r.s.armed.Store(true)
	return snap, err
}

type bootBindings struct {
	store.ToolBindingRepo
	s *bootHook
}

func (r bootBindings) List(ctx context.Context) ([]store.ToolBinding, error) {
	if r.s.armed.Load() && r.s.fired.CompareAndSwap(false, true) {
		r.s.fn()
	}
	return r.ToolBindingRepo.List(ctx)
}

// TestBootStraddlingAPublishServesItConsistently: replica b boots while a
// publish that adds kim's access row and the env gate together commits
// between b's snapshot load and its access table load. b serves one
// consistent state from its first request, the store's, never the new row
// under the old snapshot, and records the generation it applied.
func TestBootStraddlingAPublishServesItConsistently(t *testing.T) {
	t.Parallel()
	a, _ := testApp(t)
	ctx := context.Background()
	up := startGatewayUpstream(t)
	dev, err := a.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	seedGatewayUser(t, a, "kim", "dev")
	echo := putServer(t, a, echoManifest(up.URL))
	activateSets(t, a, map[string]string{"base": policyText("base", 100, allowEchoapp)})

	hook := &bootHook{Store: a.store}
	hook.fn = func() {
		setAccess(t, a, dev.ID, echo, true, `["echo","env"]`)
		activateSets(t, a, map[string]string{"gate": policyText("gate", 900, denyEnv)})
	}
	cfg := a.cfg
	cfg.DataDir, cfg.Server.Listen = t.TempDir(), "127.0.0.1:0"
	cfg.Secrets.KEKFile = a.cfg.KEKFile()
	bctx, cancel := context.WithCancel(context.Background())
	b, err := build(bctx, cfg, logging.New(cfg.Log, io.Discard), hook)
	if err != nil {
		cancel()
		t.Fatalf("build replica b: %v", err)
	}
	if !hook.fired.Load() {
		cancel()
		t.Fatal("the publish did not land inside b's boot")
	}
	// Before Run serves anything, b holds the store's snapshot and the
	// generation it applied.
	st, err := b.store.Drafts().LiveState(ctx, "")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	gen := b.appliedGen.Load()
	if b.snapshots.Current().ID != st.Snapshot.ID || gen != st.Generation {
		t.Errorf("b holds snapshot %s at generation %d when New returns, want the store's %s at %d", b.snapshots.Current().ID, gen, st.Snapshot.ID, st.Generation)
	}
	baseB := "http://" + b.Addr()
	b.cfg.Server.PublicURL = baseB
	tokens, err := authn.NewTokenService(bctx, b.store.SigningKeys(), baseB, 0)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	b.tokens = tokens
	b.http.Handler = b.routes()
	done := make(chan error, 1)
	go func() { done <- b.Run(bctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("replica b did not stop in time")
		}
	})

	got := decidesAll(b, sessionOf(t, b, sessionToken(t, baseB, "kim")))
	if want := (map[string]outcome{"echoapp__echo": callRuns, "echoapp__env": callDenied}); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("kim on the booted replica decides %v, want %v", got, want)
	}
}

// TestApplyDropsTheHoldersOfARoleCreatedAgainUnderItsName: two publishes
// land before this replica applies. The first removes the role dev with its
// one holder kim, the second creates a new role named dev with an access
// row. The apply drops kim's session, whose cached roles name the old dev.
func TestApplyDropsTheHoldersOfARoleCreatedAgainUnderItsName(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	up := startGatewayUpstream(t)
	old, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	kim := seedGatewayUser(t, app, "kim", "dev")
	echo := putServer(t, app, echoManifest(up.URL))
	activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	session := sessionOf(t, app, sessionToken(t, base, "kim"))

	held, err := app.store.Roles().ListAssignments(ctx, store.SubjectUser, kim.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, as := range held {
		if err := app.store.Roles().Unassign(ctx, as.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.store.Roles().Delete(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	created, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	setAccess(t, app, created.ID, echo, true, `["*"]`)
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	if got := decidesAll(app, session); got["echoapp__echo"] != callCheckIn || got["echoapp__env"] != callCheckIn {
		t.Errorf("kim, who held the old dev, decides %v after the apply, want check in", got)
	}
}

// TestFailedSwapKeepsAnAddedRowOut pins step 6 after step 4: the store gains
// kim's access row and a policy snapshot this replica cannot open, so the
// apply stops at the swap, and the row, whose gate the unopened snapshot
// holds, is not live under the old policy.
func TestFailedSwapKeepsAnAddedRowOut(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	up := startGatewayUpstream(t)
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	seedGatewayUser(t, app, "kim", "dev")
	echo := putServer(t, app, echoManifest(up.URL))
	activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	kim := sessionOf(t, app, sessionToken(t, base, "kim"))

	setAccess(t, app, dev.ID, echo, true, `["*"]`)
	if _, err := app.store.Snapshots().Create(ctx, store.Snapshot{ID: "unopenable", SignerKeyID: "nobody", Blob: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.Snapshots().SetActive(ctx, "unopenable"); err != nil {
		t.Fatal(err)
	}
	if err := app.converge(ctx); err == nil {
		t.Fatal("the swap did not fail")
	}
	if got := gatewayDecides(app, kim, "echoapp__env"); got != callUnknown {
		t.Errorf("kim's env call after the failed swap decides %s, want unknown: the row came before its policy", got)
	}
}

// TestFailedSwapAfterAStopKeepsTheStoppedServersRowsOut pins the rows of
// the servers the stop named: echoapp's manifest changes, so the apply
// stops it, and the swap then fails. The rows on the stopped server stay
// out of the table until an apply that reaches step 6 adds them back.
func TestFailedSwapAfterAStopKeepsTheStoppedServersRowsOut(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	echo := putServer(t, app, echoManifest(startGatewayUpstream(t).URL))
	setAccess(t, app, dev.ID, echo, true, `["*"]`)
	activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}

	putServer(t, app, echoManifest(startGatewayUpstream(t).URL))
	if _, err := app.store.Snapshots().Create(ctx, store.Snapshot{ID: "unopenable", SignerKeyID: "nobody", Blob: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.Snapshots().SetActive(ctx, "unopenable"); err != nil {
		t.Fatal(err)
	}
	err = app.converge(ctx)
	if err == nil || !strings.Contains(err.Error(), "the policy snapshot swap") {
		t.Fatalf("apply over an unopenable snapshot = %v, want the swap's failure", err)
	}
	if _, live := app.manager.View("echoapp"); live {
		t.Fatal("the apply did not stop the server whose manifest changed")
	}
	if slices.ContainsFunc(app.gateway.bindings.Load().([]gwBinding), func(b gwBinding) bool { return b.App == "echoapp" }) {
		t.Error("the rows on the server the apply stopped are still in the table")
	}
}

// TestApplyDropsSessionsBeforeItAddsAccess pins steps 4 to 6 in order: when
// the role step drops sessions, the snapshot is already swapped and the
// access rows the state adds are not live yet. The test holds the subject
// cache, so the apply waits inside the role step while the test looks.
func TestApplyDropsSessionsBeforeItAddsAccess(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	up := startGatewayUpstream(t)
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	echo := putServer(t, app, echoManifest(up.URL))
	activateSets(t, app, map[string]string{"base": policyText("base", 100, allowEchoapp)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	setAccess(t, app, dev.ID, echo, true, `["*"]`)
	snap := activateSets(t, app, map[string]string{"gate": policyText("gate", 900, denyEnv)})
	app.configMu.Lock()
	app.applied = appliedConfig{} // so the role step drops every subject under the cache's lock
	app.configMu.Unlock()

	app.subjects.mu.Lock()
	epoch := app.resolver.Epoch()
	done := make(chan error, 1)
	go func() { done <- app.converge(ctx) }()
	waitFor(t, "the apply reaches the role step", func() bool { return app.resolver.Epoch() != epoch })
	swapped := app.snapshots.Current().ID == snap.ID
	added := slices.ContainsFunc(app.gateway.bindings.Load().([]gwBinding), func(b gwBinding) bool { return b.Role == "dev" })
	app.subjects.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !swapped || added {
		t.Errorf("at the role step the snapshot is swapped = %v and dev's row is live = %v, want swapped and not live", swapped, added)
	}
}

// TestPolicyOnlyPublishDoesNotWaitForAServerStart: an install on this
// replica probes a server that does not answer yet and holds the manager's
// lock. The apply of a publish that changes only policy, and the apply of
// another replica's policy change, adopt their snapshot and release
// a.configMu while it waits, because neither has a server to stop. Their
// starts wait for the manager's lock after that, outside a.configMu.
func TestPolicyOnlyPublishDoesNotWaitForAServerStart(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reached, release := make(chan struct{}), make(chan struct{})
	var reach, free sync.Once
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reach.Do(func() { close(reached) })
		<-release
		http.Error(w, "not yet", http.StatusServiceUnavailable)
	}))
	t.Cleanup(hang.Close)
	unblock := func() { free.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	slow, err := manager.Parse([]byte(strings.ReplaceAll(echoManifest(hang.URL), "echoapp", "slow")))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = app.manager.Install(ctx, slow, store.AppSourceAPI) }()
	<-reached

	applies := []struct {
		name string
		run  func(snap *snapshot.Built) error
	}{
		{name: "the publishing replica's apply", run: func(snap *snapshot.Built) error {
			return publishOn(ctx, app, &publishHead{Snapshot: snap})
		}},
		{name: "another replica's apply", run: func(*snapshot.Built) error { return app.converge(ctx) }},
	}
	done := make(chan error, len(applies))
	for i, ap := range applies {
		snap := activateSets(t, app, map[string]string{fmt.Sprintf("gate-%d", i): policyText(fmt.Sprintf("gate-%d", i), 900, denyEnv)})
		go func() { done <- ap.run(snap) }()
		waitFor(t, ap.name+" adopts its snapshot and releases the config lock while the start probes", func() bool {
			if app.snapshots.Current().ID != snap.ID || !app.configMu.TryLock() {
				return false
			}
			app.configMu.Unlock()
			return true
		})
	}
	unblock()
	for range applies {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// TestSwapLogNamesWhereTheSnapshotCameFrom pins the swap line's word: a
// replica that applies a snapshot another replica published logs it with
// source load, as it did before the apply, and the publishing replica's
// head logs its own snapshot with source publish.
func TestSwapLogNamesWhereTheSnapshotCameFrom(t *testing.T) {
	t.Parallel()
	log, logs := captureLogger()
	app, _, _ := testAppFaultLog(t, log)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	swapLine := func(id string) string {
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, `msg="policy snapshot swapped"`) && strings.Contains(line, "id="+id) {
				return line
			}
		}
		return ""
	}

	loaded := activateSets(t, app, map[string]string{"loaded": policyText("loaded", 100, allowEchoapp)})
	if err := app.converge(ctx); err != nil {
		t.Fatal(err)
	}
	published := activateSets(t, app, map[string]string{"published": policyText("published", 100, allowEchoapp)})
	if err := publishOn(ctx, app, &publishHead{Snapshot: published}); err != nil {
		t.Fatal(err)
	}
	if line := swapLine(loaded.ID); !strings.Contains(line, "source=load") {
		t.Errorf("the swap to another replica's snapshot logged %q, want source=load", line)
	}
	if line := swapLine(published.ID); !strings.Contains(line, "source=publish") {
		t.Errorf("the swap to this replica's publish logged %q, want source=publish", line)
	}
}

// behind makes app a replica that has not applied the store's config
// generation, as a publish whose event has not arrived leaves it. The caller
// holds app.configMu.
func behind(app *App) {
	app.appliedGen.Add(-1)
}

// TestCheckInOnAnUpToDateReplicaTakesNoLock: a check-in on a replica that
// applied the store's config generation learns so without a.configMu, so
// it finishes while an apply holds the lock.
func TestCheckInOnAnUpToDateReplicaTakesNoLock(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	tok := sessionToken(t, base, "kim")

	app.configMu.Lock()
	done := make(chan checkInReply, 1)
	go func() {
		reply, _ := checkInAnswer(base, tok)
		done <- reply
	}()
	select {
	case reply := <-done:
		app.configMu.Unlock()
		if reply.code != http.StatusOK {
			t.Errorf("check-in = %d %q, want 200", reply.code, reply.sentence)
		}
	case <-time.After(5 * time.Second):
		app.configMu.Unlock()
		<-done
		t.Fatal("the check-in on an up-to-date replica waited for the config lock")
	}
}

// TestCheckInsBehindOnePublishApplyOnce: two check-ins on a replica behind
// the store's config generation wait for a.configMu together, and the one
// that takes it second looks again under the lock, finds the generation
// the first applied, and reads live state no second time.
func TestCheckInsBehindOnePublishApplyOnce(t *testing.T) {
	t.Parallel()
	var fs *liveFails
	app, base := testAppPreRun(t, []func(*App){wrapLive(&fs, nil)})
	seedGatewayUser(t, app, "kim", "dev")
	seedGatewayUser(t, app, "lee", "ops")
	toks := []string{sessionToken(t, base, "kim"), sessionToken(t, base, "lee")}

	app.configMu.Lock()
	behind(app)
	reads := fs.reads.Load()
	done := make(chan checkInReply, len(toks))
	for _, tok := range toks {
		go func() {
			reply, _ := checkInAnswer(base, tok)
			done <- reply
		}()
	}
	// Long enough for both check-ins to wait for the lock on any box; a
	// slower box can only make the test pass without testing, never fail.
	time.Sleep(100 * time.Millisecond)
	app.configMu.Unlock()
	for range toks {
		if reply := <-done; reply.code != http.StatusOK {
			t.Errorf("check-in = %d %q, want 200", reply.code, reply.sentence)
		}
	}
	if got := fs.reads.Load() - reads; got != 1 {
		t.Errorf("two check-ins behind one publish read live state %d times, want once", got)
	}
}
