// Package entrasync keeps one security group in Microsoft Entra ID for each
// Straza role and applies the group's members to the role. Where Entra owns
// people or agents, it also creates a member as a Straza person or agent,
// disables and enables it as Entra says, and takes an agent's sponsor from
// Entra. A complete, successful read of a group is evidence and may end
// assignments. A failed read is not: the last good membership keeps
// granting, and the failure is shown as stale.
package entrasync

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/membership"
	"github.com/strazahq/straza/internal/store"
)

// Graph is the part of the Microsoft Graph client the sync calls.
type Graph interface {
	EnsureGroup(ctx context.Context, spec entragraph.GroupSpec) (entragraph.Group, error)
	GroupByUniqueName(ctx context.Context, name string) (entragraph.Group, error)
	UpdateGroup(ctx context.Context, id, displayName, description string) error
	DeleteGroup(ctx context.Context, id string) error
	TransitiveMembers(ctx context.Context, id string) ([]entragraph.Member, error)
	Delta(ctx context.Context, link string, groupIDs []string) (entragraph.DeltaResult, error)
	UsersDelta(ctx context.Context, link string) (entragraph.UsersResult, error)
	AgentIdentities(ctx context.Context, groupID string) ([]entragraph.Member, error)
	AgentUserParents(ctx context.Context, groupID string) (map[string]string, error)
	Agents(ctx context.Context, ids []string) (map[string]entragraph.Agent, error)
}

// Deps are what a Syncer reads, writes and reports through.
type Deps struct {
	Graph    Graph
	Roles    store.RoleRepo
	Users    store.UserRepo
	Groups   store.EntraGroupRepo
	Agents   store.EntraAgentRepo
	Settings store.SettingsRepo
	// Membership writes and reports the assignments, as for every identity
	// source.
	Membership membership.Deps
	// People reports and runs what the sync does to a person or an agent.
	People People
	// Record writes one audit record of the sync's own acts. data carries
	// the action and names no actor.
	Record func(ctx context.Context, data map[string]any)
	Config config.Entra
	// Deployment is the part of every group's uniqueName that keeps two
	// Straza deployments in one tenant apart. It never changes.
	Deployment string
	// Protected answers the id of the break-glass account, which is never
	// given a role from a group, or "" when there is none.
	Protected func(ctx context.Context) string
	Now       func() time.Time
	Log       *slog.Logger
}

// Syncer runs the passes. One goroutine calls Pass, on the replica that
// holds the sync's lease. What it keeps in memory is lost with the lease,
// and the next holder starts with a full pass, which reads every group.
type Syncer struct {
	d         Deps
	links     map[string]string           // delta batch key to its delta link
	tracked   string                      // the group ids the links cover
	nested    map[string][]string         // nested group id to the roles it reaches
	seen      map[string]entragraph.Agent // the agent identities this pass read, by id
	lastFull  time.Time
	notBefore time.Time // no pass before this time, which Graph named
}

// New returns a Syncer over d.
func New(d Deps) *Syncer {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Syncer{d: d, links: map[string]string{}, nested: map[string][]string{}}
}

// StatusKey is the settings key of the Status the sync keeps.
const StatusKey = "entra.sync"

// Status says whether Straza can read Entra. LastGood is the end of the
// last pass in which every request succeeded. StaleSince is the first
// failed pass after it, nil while reads succeed, and Error is the sentence
// of the last failure.
type Status struct {
	LastGood   *time.Time `json:"lastGood,omitempty"`
	StaleSince *time.Time `json:"staleSince,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// ReadStatus reads the Status from settings. A deployment whose sync never
// ran reads as the zero Status.
func ReadStatus(ctx context.Context, settings store.SettingsRepo) (Status, error) {
	raw, err := settings.Get(ctx, StatusKey)
	if errors.Is(err, store.ErrNotFound) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	var st Status
	return st, json.Unmarshal([]byte(raw), &st)
}

// Pass runs one pass: it creates and deletes groups so that every role has
// one, then applies members. A full pass reads every group and runs on the
// first call, once every reconcile interval and whenever the set of groups
// changed. Any other pass asks the delta which groups changed and reads
// only those. A request that fails for a reason beyond one group ends the
// pass and marks the read stale, and nothing it did not read is changed.
func (s *Syncer) Pass(ctx context.Context) {
	now := s.d.Now()
	if now.Before(s.notBefore) {
		return
	}
	err := s.pass(ctx, now)
	if err != nil && ctx.Err() != nil {
		return // the replica is stopping or lost its lease, which says nothing about Entra
	}
	if err != nil {
		var ge *entragraph.Error
		if errors.As(err, &ge) && ge.RetryAfter > 0 {
			s.notBefore = now.Add(ge.RetryAfter)
		}
		s.lastFull, s.links = time.Time{}, map[string]string{}
	}
	s.setStatus(ctx, s.d.Now(), err)
}

func (s *Syncer) pass(ctx context.Context, now time.Time) error {
	roles, err := s.d.Roles.List(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]store.Role, len(roles))
	for _, r := range roles {
		byID[r.ID] = r
	}
	if err := s.lifecycle(ctx, roles, byID); err != nil {
		return err
	}
	s.seen = map[string]entragraph.Agent{}
	if err := s.feed(ctx); err != nil {
		return err
	}
	if err := s.refreshAgents(ctx); err != nil {
		return err
	}
	rows, err := s.d.Groups.List(ctx)
	if err != nil {
		return err
	}
	var live []store.EntraRoleGroup
	for _, g := range rows {
		if _, ok := byID[g.RoleID]; ok && g.GroupID != "" && reads(g.State) {
			live = append(live, g)
		}
	}
	full := s.lastFull.IsZero() || !now.Before(s.lastFull.Add(s.d.Config.ReconcileInterval)) || s.trackedKey(live) != s.tracked
	due := live
	if !full {
		changed, err := s.changed(ctx, live)
		if err != nil {
			return err
		}
		if changed == nil {
			full = true // a delta link ended, so every group is read
		} else {
			due = withReleased(changed, live)
		}
	}
	if full {
		if err := s.freshLinks(ctx, live); err != nil {
			return err
		}
	}
	for _, g := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		note := ""
		if full {
			if note, err = s.checkGroup(ctx, byID[g.RoleID], &g); err != nil {
				return err
			}
		}
		if g.State == store.EntraGroupMissing {
			continue
		}
		if err := s.reconcile(ctx, byID[g.RoleID], g, note); err != nil {
			return err
		}
	}
	if full {
		s.lastFull = now
	}
	return nil
}

// withReleased adds to due every group whose held removals an admin let
// through, so the release takes effect at the next pass and does not wait
// for a change in Entra or for the next full pass.
func withReleased(due, live []store.EntraRoleGroup) []store.EntraRoleGroup {
	have := make(map[string]bool, len(due))
	for _, g := range due {
		have[g.RoleID] = true
	}
	for _, g := range live {
		if g.State == store.EntraGroupReleased && !have[g.RoleID] {
			due = append(due, g)
		}
	}
	return due
}

// reads reports whether the members of a group in this state are read.
func reads(state string) bool {
	return state == store.EntraGroupLinked || state == store.EntraGroupHeld || state == store.EntraGroupReleased
}

// trackedIDs lists, sorted, the ids of the groups the delta watches: every
// read role group and every nested group the last reads found in one.
func (s *Syncer) trackedIDs(live []store.EntraRoleGroup) []string {
	seen := map[string]bool{}
	for _, g := range live {
		seen[g.GroupID] = true
	}
	for id := range s.nested {
		seen[id] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *Syncer) trackedKey(live []store.EntraRoleGroup) string {
	return strings.Join(s.trackedIDs(live), ",")
}

// deltaBatch is how many group ids one delta request filters on.
const deltaBatch = 50

func batches(ids []string) [][]string {
	var out [][]string
	for len(ids) > deltaBatch {
		out, ids = append(out, ids[:deltaBatch]), ids[deltaBatch:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

// freshLinks starts the delta anew for the watched groups and keeps only
// the links. It runs before the full reads, so a change that lands during
// them is in the next delta.
func (s *Syncer) freshLinks(ctx context.Context, live []store.EntraRoleGroup) error {
	links := map[string]string{}
	for _, b := range batches(s.trackedIDs(live)) {
		res, err := s.d.Graph.Delta(ctx, "", b)
		if err != nil {
			return err
		}
		links[strings.Join(b, ",")] = res.Link
	}
	s.links, s.tracked = links, s.trackedKey(live)
	return nil
}

// changed asks the delta which watched groups changed since the last pass
// and answers the role groups to read again: a changed role group, and
// every role group that a changed nested group reaches. It answers nil,
// not an empty list, when Graph no longer knows a delta link, and the
// caller then reads every group.
func (s *Syncer) changed(ctx context.Context, live []store.EntraRoleGroup) ([]store.EntraRoleGroup, error) {
	byGroup := make(map[string]store.EntraRoleGroup, len(live))
	byRole := make(map[string]store.EntraRoleGroup, len(live))
	for _, g := range live {
		byGroup[g.GroupID], byRole[g.RoleID] = g, g
	}
	hit := map[string]store.EntraRoleGroup{}
	for key, link := range s.links {
		res, err := s.d.Graph.Delta(ctx, link, nil)
		var ge *entragraph.Error
		if errors.As(err, &ge) && (ge.Status == 400 || ge.Status == 410) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		s.links[key] = res.Link
		for _, c := range res.Groups {
			if g, ok := byGroup[c.ID]; ok {
				hit[g.RoleID] = g
			}
			for _, roleID := range s.nested[c.ID] {
				if g, ok := byRole[roleID]; ok {
					hit[roleID] = g
				}
			}
		}
	}
	out := make([]store.EntraRoleGroup, 0, len(hit))
	for _, g := range hit {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UniqueName < out[j].UniqueName })
	return out, nil
}

// setStatus stores the outcome of a pass and writes one audit record when
// the read turns stale and one when it recovers, not one for each pass.
func (s *Syncer) setStatus(ctx context.Context, now time.Time, passErr error) {
	st, err := ReadStatus(ctx, s.d.Settings)
	if err != nil {
		s.d.Log.Warn("entra sync: the state of the read could not be loaded, so this pass's outcome is not stored", "err", err)
		return
	}
	was := st.StaleSince != nil
	if passErr == nil {
		st = Status{LastGood: &now}
	} else {
		if !was {
			st.StaleSince = &now
		}
		st.Error = passErr.Error()
		s.d.Log.Warn("entra sync: a pass failed, so the roles keep their last good holders and the next pass tries again", "err", passErr)
	}
	raw, err := json.Marshal(st)
	if err == nil {
		err = s.d.Settings.Set(ctx, StatusKey, string(raw))
	}
	if err != nil {
		s.d.Log.Warn("entra sync: the state of the read could not be stored", "err", err)
		return
	}
	switch {
	case passErr != nil && !was:
		s.d.Record(ctx, map[string]any{"action": "entra.read.stale", "reason": passErr.Error()})
	case passErr == nil && was:
		s.d.Record(ctx, map[string]any{"action": "entra.read.recovered"})
	}
}
