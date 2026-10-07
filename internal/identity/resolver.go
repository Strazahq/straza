// Package identity implements identity-plane logic above the store: role
// resolution.
package identity

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// Resolver computes a user's effective roles:
//
//	direct assignments (valid at t)
//	→ transitive closure over role implications (cycle-safe BFS)
//
// Results are cached per epoch. The admin plane bumps the epoch on any
// write that can change resolution: assignments, implications, memberships,
// role deletion. A cached entry also ends at the next window edge of the
// user's assignments, so a time-limited assignment starts and ends on time
// with no write.
type Resolver struct {
	store store.Store

	mu    sync.Mutex
	epoch uint64
	cache map[string]resolution // userID → resolved roles (sorted by name)
}

// resolution is one cached answer. until is the earliest valid_from or
// valid_to of the user's direct assignments after the time the answer was
// resolved for, zero when there is none: a lookup at or past it resolves
// again.
type resolution struct {
	roles []store.Role
	until time.Time
}

// NewResolver returns a Resolver over s.
func NewResolver(s store.Store) *Resolver {
	return &Resolver{store: s, cache: map[string]resolution{}}
}

// Bump invalidates all cached resolutions. Call after any identity write
// that affects role resolution.
func (r *Resolver) Bump() {
	r.mu.Lock()
	r.epoch++
	r.cache = map[string]resolution{}
	r.mu.Unlock()
}

// Epoch returns the current cache epoch (diagnostics / snapshot binding).
func (r *Resolver) Epoch() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.epoch
}

// ResolveRoles returns the user's effective roles at time t, sorted by name.
// A cached answer serves until the epoch moves or t reaches the next window
// edge of the user's assignments.
func (r *Resolver) ResolveRoles(ctx context.Context, userID string, t time.Time) ([]store.Role, error) {
	roles, _, err := r.ResolveRolesUntil(ctx, userID, t)
	return roles, err
}

// ResolveRolesUntil is ResolveRoles that also returns the time the answer
// stops being true: the next valid_from or valid_to of the user's
// assignments after t, zero when there is none. A caller that keeps the
// roles past t must resolve again at that time.
func (r *Resolver) ResolveRolesUntil(ctx context.Context, userID string, t time.Time) ([]store.Role, time.Time, error) {
	r.mu.Lock()
	epoch := r.epoch
	if c, ok := r.cache[userID]; ok && (c.until.IsZero() || t.Before(c.until)) {
		r.mu.Unlock()
		return c.roles, c.until, nil
	}
	r.mu.Unlock()

	roles, until, err := r.resolve(ctx, userID, t)
	if err != nil {
		return nil, time.Time{}, err
	}

	r.mu.Lock()
	// A write that landed while the store was read may have revoked what
	// was just resolved, so the answer is cached only for the epoch it
	// started in.
	if r.epoch == epoch {
		r.cache[userID] = resolution{roles: roles, until: until}
	}
	r.mu.Unlock()
	return roles, until, nil
}

// resolve computes the roles at t and the next window edge after t.
func (r *Resolver) resolve(ctx context.Context, userID string, t time.Time) ([]store.Role, time.Time, error) {
	// Seed: direct assignments.
	seed := map[string]bool{}
	direct, err := r.store.Roles().ListAssignments(ctx, store.SubjectUser, userID)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("identity: list user assignments: %w", err)
	}
	var until time.Time
	for _, a := range direct {
		if assignmentValidAt(a, t) {
			seed[a.RoleID] = true
		}
		until = nextEdge(until, t, a.ValidFrom)
		until = nextEdge(until, t, a.ValidTo)
	}
	// Closure over implications. Visited set makes cycles harmless.
	imps, err := r.store.Roles().ListImplications(ctx)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("identity: list implications: %w", err)
	}
	adj := map[string][]string{}
	for _, imp := range imps {
		adj[imp.RoleID] = append(adj[imp.RoleID], imp.ImpliesRoleID)
	}
	effective := Closure(seed, adj)

	// Materialize role records; roles deleted since assignment are dropped.
	all, err := r.store.Roles().List(ctx)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("identity: list roles: %w", err)
	}
	var out []store.Role
	for _, role := range all {
		if effective[role.ID] {
			out = append(out, role)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, until, nil
}

// nextEdge returns the earlier of until and edge when edge is after t,
// else until; a nil edge and a zero until are open.
func nextEdge(until, t time.Time, edge *time.Time) time.Time {
	if edge == nil || !edge.After(t) {
		return until
	}
	if until.IsZero() || edge.Before(until) {
		return *edge
	}
	return until
}

// assignmentValidAt reports whether a is in force at t: valid_from inclusive,
// valid_to exclusive; nil bounds are open.
func assignmentValidAt(a store.RoleAssignment, t time.Time) bool {
	if a.ValidFrom != nil && t.Before(*a.ValidFrom) {
		return false
	}
	if a.ValidTo != nil && !t.Before(*a.ValidTo) {
		return false
	}
	return true
}

// Closure returns all role ids reachable from seed over adj (BFS,
// cycle-safe).
func Closure(seed map[string]bool, adj map[string][]string) map[string]bool {
	out := map[string]bool{}
	queue := make([]string, 0, len(seed))
	for id := range seed {
		out[id] = true
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if !out[next] {
				out[next] = true
				queue = append(queue, next)
			}
		}
	}
	return out
}
