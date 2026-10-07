package server

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/snapshot"
	"github.com/strazahq/straza/internal/store"
)

// The delays of the retry of a failed apply: the first retry runs after
// firstApplyRetry, and each later one waits twice as long, up to
// lastApplyRetry.
const (
	firstApplyRetry = 5 * time.Second
	lastApplyRetry  = time.Minute
)

// checkInTries bounds how often a check-in resolves roles again because an
// apply moved them while they were read.
const checkInTries = 5

// The check-in's refusals when it cannot cache roles whose config this
// replica applied, each answered with 503.
const (
	checkInApplyFailed = "Straza could not apply the latest config on this server before it checked your roles, " +
		"so you are not checked in. Check in again in a moment, and tell your administrator if it keeps failing"
	checkInRolesMoving = "Straza could not check your roles, because the config on this server changed each time it read them, " +
		"so you are not checked in. Check in again in a moment"
)

// appliedConfig is what this replica applied last: the id of each role by
// its name and the implication edges, zero before the boot's apply. The
// config generation it applied is a.appliedGen, which check-ins read
// without a.configMu.
type appliedConfig struct {
	roles map[string]string
	edges map[string][]string
}

// publishHead is what a publish changed, from its plan and outcome: the
// servers it changed or removed, the roles whose access row changed or
// went, the roles it removed, the snapshot it built, and the implication
// edges before (the World's) and after (the overlay's).
type publishHead struct {
	Servers       []string
	AccessRoles   []string
	Removed       []string
	Snapshot      *snapshot.Built
	Before, After map[string][]string
}

// converge moves this replica to live state for a change it did not make:
// steps 1 to 6 under a.configMu, then the starts without it. The boot, the
// converge handlers, every subscription of the consumer and the retry run
// it. It answers the error of a failed apply, which applyLocked has logged
// and retries.
func (a *App) converge(ctx context.Context) error {
	a.configMu.Lock()
	st, err := a.applyLocked(ctx, nil)
	a.configMu.Unlock()
	if err != nil {
		return err
	}
	a.startLive(ctx, st, nil)
	return nil
}

// applyLocked runs steps 1 to 6 of an apply, the caller holding a.configMu:
//
//  1. Refresh the credential broker cache.
//  2. Stop the servers whose row changed or went.
//  3. Narrow the access table to the rows the state holds.
//  4. Swap to the state's policy snapshot.
//  5. Bump role resolution and drop the cached subjects of moved roles.
//  6. Load the state's access rows into the gateway.
//  7. Start what the state needs, which startLive does without a.configMu.
//
// Steps 3 and 5 also run ahead of the stop. A non-nil head first runs steps
// 2 to 5 from the publish's plan in memory, so they cannot fail on a read.
// A failed read or step ends the apply at the narrower state it reached,
// and the retry on a.lifetime outlives the caller's ctx. It answers the
// state it read for step 7.
func (a *App) applyLocked(ctx context.Context, head *publishHead) (store.LiveState, error) {
	if head != nil {
		// The publish committed before this call, so an instance registered
		// before now may run a row it replaced, whoever started it, and
		// StopNamed reads each named row again before it stops one.
		if err := a.applyHead(ctx, head, time.Now()); err != nil {
			return store.LiveState{}, a.applyFailed("the stop of the servers the publish changed", err)
		}
	}
	live := ""
	if cur := a.snapshots.Current(); cur != nil {
		live = cur.ID
	}
	st, err := a.store.Drafts().LiveState(ctx, live)
	if err != nil {
		return store.LiveState{}, a.applyFailed("the read of live state", err)
	}
	settled := true
	if err := a.broker.Refresh(ctx); err != nil && a.lifetime.Err() == nil {
		// The old cache can only keep a server parked for its credential
		// parked, so the apply goes on and the retry refreshes again.
		settled = false
		a.log.Error(fmt.Sprintf("The config apply could not refresh the credential cache on this replica: %v. "+
			"It goes on with the credentials it holds, which can only keep a server that waits for its credential waiting, "+
			"and tries again in %s.", err, a.retryApply()), "component", "apply")
	}
	// The rows the state lacks and the sessions of the roles it moved go
	// before the stop, whose read can fail, so a removed server or role
	// holds no row and no session while the apply retries. A check-in in
	// that window applies first and is refused while the apply fails, so
	// no session is cached again under the old snapshot.
	a.gateway.Narrow(keepsAccess(st.Access))
	a.subjects.dropHolding(a.movedRoles(st))
	// Stale waits for no lock, and StopNamed, which waits for the manager's
	// lock behind a start's probe, runs only when a server must stop.
	stale, err := a.manager.Stale(ctx, st.Apps, st.ReadAt)
	if err == nil && len(stale) > 0 {
		err = a.manager.StopNamed(ctx, stale, st.ReadAt)
	}
	if err != nil {
		return store.LiveState{}, a.applyFailed("the stop of the servers whose row changed", err)
	}
	gone := setOf(stale)
	a.gateway.Narrow(func(b gwBinding) bool { return !gone[b.App] })
	if err := a.swapTo(ctx, st.Snapshot, live); err != nil {
		return store.LiveState{}, a.applyFailed("the policy snapshot swap", err)
	}
	a.applyRoles(st)
	a.gateway.SetBindings(bindingsOf(st.Access))
	a.appliedGen.Store(st.Generation)
	if settled {
		a.applySettled()
	}
	return st, nil
}

// startLive runs step 7 without a.configMu: every start the state needs,
// each reading its row again first. announce names the servers whose start
// emits straza.apps.deployed and whose error the caller answers with, and
// the manager logs every other failure with the recovery it names.
func (a *App) startLive(ctx context.Context, st store.LiveState, announce map[string]bool) map[string]error {
	return a.manager.StartMissing(ctx, st.Apps, announce)
}

// applyHead runs steps 2 to 5 from what a publish changed, before any read
// of live state: it stops the servers the publish changed or removed as of
// since, drops their access rows and every row of a role whose access
// changed or went, adopts the snapshot the publish built with the nudge and
// the invalidation, and drops the sessions of the roles whose closure
// changed. A publish that names no server does not wait for the manager's
// lock.
func (a *App) applyHead(ctx context.Context, h *publishHead, since time.Time) error {
	if len(h.Servers) > 0 {
		if err := a.manager.StopNamed(ctx, h.Servers, since); err != nil {
			return err
		}
	}
	servers, roles := setOf(h.Servers), setOf(slices.Concat(h.AccessRoles, h.Removed))
	a.gateway.Narrow(func(b gwBinding) bool { return !servers[b.App] && !roles[b.Role] })
	if h.Snapshot != nil {
		a.snapshots.Adopt(*h.Snapshot)
		a.pushPolicyNudge(map[string]any{"snapshot": h.Snapshot.ID})
		a.gateway.Invalidate()
	}
	a.resolver.Bump()
	moved := changedReach(h.Before, h.After)
	for _, r := range h.Removed {
		moved[r] = true
	}
	a.subjects.dropHolding(moved)
	return nil
}

// swapTo is step 4 from state: when the state's active snapshot is not
// live, the snapshot this apply read against, it opens the blob LiveState
// read as Load does and adopts it while live is still the live one. No
// active snapshot swaps nothing.
func (a *App) swapTo(ctx context.Context, snap store.Snapshot, live string) error {
	if snap.ID == "" || snap.ID == live {
		return nil
	}
	eng, opened, err := policy.OpenSnapshot(snap.Blob, snap.ID, func(kid string) (ed25519.PublicKey, bool) {
		return a.snapKeys.Public(ctx, kid)
	})
	if err != nil {
		return fmt.Errorf("open the policy snapshot %s with the snapshot signing keys: %w", snap.ID, err)
	}
	a.adoptWhileLive(snapshot.Built{ID: snap.ID, SignerKeyID: snap.SignerKeyID, Blob: snap.Blob,
		Engine: eng, MaxAge: opened.MaxAgeSecs, Sets: len(opened.Documents)}, live)
	return nil
}

// adoptWhileLive adopts b, the store's active snapshot, as loaded and
// invalidates the gateway catalogs while live, the snapshot the apply read
// against, is still the live one. The activate route swaps outside
// a.configMu until it publishes through a draft, and a snapshot it swapped
// in since is newer than b, so b waits for the next apply. It checks after
// b was opened, which takes milliseconds.
func (a *App) adoptWhileLive(b snapshot.Built, live string) {
	if cur := a.snapshots.Current(); cur != nil && cur.ID != live {
		return
	}
	a.snapshots.AdoptLoaded(b)
	a.gateway.Invalidate()
}

// applyRoles is step 5 from state: it bumps role resolution and drops the
// cached subjects of the roles the state moved, then records the state as
// applied. The same drop ran ahead of the stop, so here it catches a
// subject cached since. With nothing applied yet it cannot tell what
// changed, so it drops every cached subject.
func (a *App) applyRoles(st store.LiveState) {
	a.resolver.Bump()
	if a.applied.roles == nil {
		a.subjects.dropAll()
	} else {
		a.subjects.dropHolding(a.movedRoles(st))
	}
	a.applied = appliedConfig{roles: st.RoleIDs, edges: st.Implies}
}

// movedRoles answers the roles applied last whose holders st drops: the
// roles st lacks or holds under another id, and the roles whose closure
// changed since. It answers nil with nothing applied yet.
func (a *App) movedRoles(st store.LiveState) map[string]bool {
	if a.applied.roles == nil {
		return nil
	}
	moved := changedReach(a.applied.edges, st.Implies)
	for name, id := range a.applied.roles {
		if st.RoleIDs[name] != id {
			moved[name] = true
		}
	}
	return moved
}

// settledRoles is what a check-in cached: the roles it resolved, the
// subject built from them, and the subject that one replaced, if any.
type settledRoles struct {
	roles   []store.Role
	subject policy.Subject
	prev    policy.Subject
	hadPrev bool
}

// settleRoles resolves the roles of userID and caches the subject build
// makes from them for sessionID, with the session facts check-in read, only
// once this replica applied the config they come from. It reads the config
// generation after the roles, and when the store holds one this replica has
// not applied it applies first and resolves again. It caches only while the
// resolution epoch it resolved in holds, because an apply bumps the epoch
// before it drops the sessions of the roles it moved, and resolves again
// otherwise.
// After checkInTries tries it answers checkInRolesMoving, and a failed apply
// answers checkInApplyFailed, each with its cause. A failed resolution
// answers its error and no sentence.
func (a *App) settleRoles(ctx context.Context, sessionID, userID string, facts sessionFacts, build func([]store.Role) policy.Subject) (settledRoles, string, error) {
	for try := 1; ; try++ {
		epoch := a.resolver.Epoch()
		roles, until, err := a.resolver.ResolveRolesUntil(ctx, userID, a.subjects.now())
		if err != nil {
			return settledRoles{}, "", err
		}
		applied, err := a.catchUp(ctx)
		if err != nil {
			return settledRoles{}, checkInApplyFailed, err
		}
		if !applied {
			sub := build(roles)
			held := func() bool { return a.resolver.Epoch() == epoch }
			if prev, had, ok := a.subjects.putIf(sessionID, sub, facts, until, held); ok {
				return settledRoles{roles: roles, subject: sub, prev: prev, hadPrev: had}, "", nil
			}
		}
		if try == checkInTries {
			return settledRoles{}, checkInRolesMoving, fmt.Errorf("the config moved under each of %d reads of the roles", checkInTries)
		}
	}
}

// catchUp applies live state for a check-in when the store holds a config
// generation this replica has not applied, as a publish whose event has not
// arrived leaves, and answers whether it applied. A check-in on an
// up-to-date replica takes no lock, and one that waited for the lock looks
// again, since the apply that held it may have caught up. The servers that
// apply would start come with the publish's own event.
func (a *App) catchUp(ctx context.Context) (bool, error) {
	gen, err := a.store.Drafts().Generation(ctx)
	if err != nil {
		return false, fmt.Errorf("read the config generation: %w", err)
	}
	if gen <= a.appliedGen.Load() {
		return false, nil
	}
	a.configMu.Lock()
	defer a.configMu.Unlock()
	if gen <= a.appliedGen.Load() {
		return false, nil
	}
	if _, err := a.applyLocked(ctx, nil); err != nil {
		return false, err
	}
	return true, nil
}

// applyFailed logs a failed step once, arms the retry and answers the
// error. When a.lifetime ended, the process is stopping, so it logs and
// arms nothing.
func (a *App) applyFailed(step string, err error) error {
	if a.lifetime.Err() == nil {
		a.log.Error(fmt.Sprintf("The config apply stopped at %s on this replica: %v. "+
			"It serves the narrower state it reached and tries again in %s.", step, err, a.retryApply()), "component", "apply")
	}
	return fmt.Errorf("config apply: %s: %w", step, err)
}

// retryApply arms the retry of a failed apply on a.lifetime and answers its
// delay: firstApplyRetry after an apply that settled, doubling to
// lastApplyRetry. An armed retry is replaced, since the failure that calls
// this was an attempt too. The caller holds a.configMu.
func (a *App) retryApply() time.Duration {
	wait := max(a.applyWait, firstApplyRetry)
	a.applyWait = min(2*wait, lastApplyRetry)
	if a.applyRetry != nil {
		a.applyRetry.Stop()
	}
	life := a.lifetime
	a.applyRetry = time.AfterFunc(wait, func() {
		if life.Err() == nil {
			_ = a.converge(life)
		}
	})
	return wait
}

// applySettled stops a pending retry and resets its delay after an apply
// that reached every step. The caller holds a.configMu.
func (a *App) applySettled() {
	if a.applyRetry != nil {
		a.applyRetry.Stop()
		a.applyRetry = nil
	}
	a.applyWait = 0
}

// keepsAccess is step 3 from state, ahead of the stop: a row of the table
// stays only when the state holds a row identical in role, server and
// matchers. The rows of the servers the stop names go after it.
func keepsAccess(access []store.LiveAccess) func(gwBinding) bool {
	rows := make(map[string]bool, len(access))
	for _, r := range access {
		rows[accessKey(r.Role, r.App, r.Matchers)] = true
	}
	return func(b gwBinding) bool { return rows[accessKey(b.Role, b.App, b.Matchers)] }
}

// accessKey is an access row's identity for step 3. The separators are
// control characters, so no role, server or matcher name can forge a join.
func accessKey(role, app string, matchers []string) string {
	return role + "\x00" + app + "\x00" + strings.Join(matchers, "\x01")
}

// bindingsOf is step 6: the state's access rows as the gateway holds them.
func bindingsOf(access []store.LiveAccess) []gwBinding {
	out := make([]gwBinding, 0, len(access))
	for _, r := range access {
		out = append(out, gwBinding{ID: r.ID, App: r.App, Role: r.Role, Matchers: r.Matchers})
	}
	return out
}

// changedReach answers the roles whose closure over after differs from
// their closure over before, whether it lost a role or gained one. A role
// with no edge reaches only itself, so only a role with an edge before or
// after can differ.
func changedReach(before, after map[string][]string) map[string]bool {
	changed := map[string]bool{}
	for _, edges := range []map[string][]string{before, after} {
		for role := range edges {
			// A gained role drops the holders' sessions as a lost one does,
			// because it can carry a deny that a session's compiled closure
			// does not know until it checks in again.
			if !changed[role] && !maps.Equal(closure(before, role), closure(after, role)) {
				changed[role] = true
			}
		}
	}
	return changed
}

// closure answers role and every role it implies through edges. The seen
// set makes a cycle harmless.
func closure(edges map[string][]string, role string) map[string]bool {
	seen := map[string]bool{role: true}
	queue := []string{role}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		for _, next := range edges[r] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return seen
}

// setOf answers names as a set, never nil.
func setOf(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}
