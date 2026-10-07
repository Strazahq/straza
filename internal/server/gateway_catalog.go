package server

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/strazahq/straza/internal/server/catalogcache"
	"github.com/strazahq/straza/internal/server/ratelimit"
)

// gwBinding is one tool_bindings row resolved to names (role/app ids are
// storage details; the catalog works in policy identities).
type gwBinding struct {
	ID       string
	App      string
	Role     string
	Matchers []string
}

// listChangedNotification is the tools/list_changed JSON-RPC notification the
// gateway pushes so a live client re-lists. Shared by the broadcast in
// Invalidate() and the per-session nudge (notifySession) the checkin path fires
// when a session's roles or groups change under it.
const listChangedNotification = `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`

// catalogCacheCap bounds each of the tier-1 and tier-2 caches. One entry per
// live role set / per live session, LRU-evicted past the cap: the fleet size a
// single gateway keeps warm without unbounded growth.
const catalogCacheCap = 1024

// defaultNotifyDebounce is the window over which Invalidate/SetBindings
// broadcasts coalesce, so a burst of control-plane edits becomes one fleet-wide
// re-list instead of a herd. Cache flushing is always immediate; only the SSE
// notification is debounced. Tests set the coalescer delay to 0 for the old
// synchronous behavior.
const defaultNotifyDebounce = time.Second

// gateway holds the request-path state.
type gateway struct {
	bindings atomic.Value // []gwBinding
	epoch    atomic.Int64 // full-flush generation, folded into every cache key
	bindGen  atomic.Int64 // binding-table generation, guards stale cache inserts

	mu       sync.Mutex
	catalogs *catalogcache.LRU // roleKey|epoch|snapshot → *sessionCatalog (tier 1)
	overlays *catalogcache.LRU // sessionID → *catalogOverlay (tier 2)

	flight  *catalogcache.FlightGroup // singleflights tier-1 builds
	streams *sseHub
	limiter *ratelimit.Limiter
	notify  *catalogcache.Coalescer // debounces list_changed broadcasts
}

func newGateway() *gateway {
	g := &gateway{
		catalogs: catalogcache.NewLRU(catalogCacheCap),
		overlays: catalogcache.NewLRU(catalogCacheCap),
		flight:   catalogcache.NewFlightGroup(),
		streams:  newSSEHub(),
		limiter:  ratelimit.New(),
	}
	g.notify = catalogcache.NewCoalescer(defaultNotifyDebounce, func() {
		g.streams.broadcast(listChangedNotification)
	})
	g.bindings.Store([]gwBinding{})
	return g
}

// SetBindings swaps the in-memory binding table (control plane: boot and
// binding CRUD) and invalidates the catalogs the change actually touched. A
// role-level diff between the old and new tables drives targeted eviction so a
// single role's binding edit never flushes the whole fleet's caches.
func (g *gateway) SetBindings(bs []gwBinding) {
	old, _ := g.bindings.Load().([]gwBinding)
	changed := changedRoles(old, bs)
	g.bindings.Store(bs)

	// An empty diff still means "something happened". refreshBindings is also the
	// refresh hammer after an app create/update and the converge OnApps lane,
	// where the binding rows can be byte-identical yet the exposed inventory
	// (which tools an app publishes) has changed. We cannot tell those apart from
	// the binding table alone, so we fall back to a full flush (correctness over
	// surgery).
	if len(changed) == 0 {
		g.Invalidate()
		return
	}

	// Targeted path: a real binding change. Bump the binding generation (guards
	// stale cache inserts in catalogFor), drop only the tier-1 and tier-2 entries
	// whose role set intersects the changed roles, and coalesce the broadcast.
	// The epoch is deliberately NOT bumped; it stays the full-flush signal.
	g.bindGen.Add(1)
	g.mu.Lock()
	g.catalogs.DeleteFunc(func(_ string, v any) bool {
		return rolesIntersect(v.(*sessionCatalog).roles, changed)
	})
	g.overlays.DeleteFunc(func(_ string, v any) bool {
		return rolesIntersect(v.(*catalogOverlay).roles, changed)
	})
	g.mu.Unlock()
	g.notify.Trigger()
}

// Narrow drops the rows of the binding table that keep refuses and
// invalidates the catalogs of their roles as SetBindings does. It never adds
// a row, and a call that drops nothing changes nothing.
func (g *gateway) Narrow(keep func(gwBinding) bool) {
	old, _ := g.bindings.Load().([]gwBinding)
	kept := make([]gwBinding, 0, len(old))
	for _, b := range old {
		if keep(b) {
			kept = append(kept, b)
		}
	}
	if len(kept) < len(old) {
		g.SetBindings(kept)
	}
}

// Invalidate drops all tier-1 catalogs AND tier-2 overlays and notifies live
// client streams (notifications/tools/list_changed) so they re-list. The epoch
// bump alone would stale every cache key; flushing the maps too keeps memory
// bounded and the intent obvious. The cache flush is immediate; only the
// broadcast is debounced, so an invalidation herd collapses to one re-list.
func (g *gateway) Invalidate() {
	g.epoch.Add(1)
	g.mu.Lock()
	g.catalogs.Flush()
	g.overlays.Flush()
	g.mu.Unlock()
	g.notify.Trigger()
}

// dropOverlay evicts one session's tier-2 overlay (its next tools/list rebuilds
// it). Used by the checkin role-change nudge so a re-listing client gets a
// freshly probed catalog under its new roles/groups.
func (g *gateway) dropOverlay(sessionID string) {
	g.mu.Lock()
	g.overlays.Delete(sessionID)
	g.mu.Unlock()
}

// refreshBindings resolves tool_bindings rows to (role name, app name)
// pairs and swaps the gateway's in-memory table. Control plane only (boot +
// binding CRUD).
func (a *App) refreshBindings(ctx context.Context) error {
	rows, err := a.store.ToolBindings().List(ctx)
	if err != nil {
		return err
	}
	roles, err := a.store.Roles().List(ctx)
	if err != nil {
		return err
	}
	roleName := map[string]string{}
	for _, ro := range roles {
		roleName[ro.ID] = ro.Name
	}
	apps, err := a.store.Apps().List(ctx)
	if err != nil {
		return err
	}
	appName := map[string]string{}
	for _, ap := range apps {
		appName[ap.ID] = ap.Name
	}

	out := make([]gwBinding, 0, len(rows))
	for _, b := range rows {
		var matchers []string
		if err := json.Unmarshal([]byte(b.ToolMatcher), &matchers); err != nil || len(matchers) == 0 {
			continue
		}
		role, app := roleName[b.RoleID], appName[b.AppID]
		if role == "" || app == "" {
			continue
		}
		out = append(out, gwBinding{ID: b.ID, App: app, Role: role, Matchers: matchers})
	}
	a.gateway.SetBindings(out)
	return nil
}
