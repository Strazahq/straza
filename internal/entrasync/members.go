package entrasync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/membership"
	"github.com/strazahq/straza/internal/store"
)

// Reasons a member of a role's group is not given the role.
const (
	ReasonNotRead   = "not-read"   // identity.entra.read does not list this kind
	ReasonNameTaken = "name-taken" // another Straza user has the account's sign-in name
	ReasonNotAgent  = "not-agent"  // an application's service principal, which is no agent identity
	ReasonUnread    = "unread"     // Entra returned a type and an id only
)

// NotApplied is one member of a role's group that was not given the role.
type NotApplied struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// notAppliedCap bounds the members kept in a group's row.
const notAppliedCap = 200

// heldSentence is the row's sentence while the removal guard holds a pass.
const heldSentence = "Entra's group lists %d of this role's %d holders from Entra no longer, which is more than the removal guard lets through, so nobody was removed. Check the group in Entra. If the removals are right, apply them."

// reconcile reads every member of the role's group, nested groups
// included, and makes the role's Entra assignments match: a member that is
// a Straza user gets the role, and a holder the read no longer lists loses
// it. A failed read changes nothing. A read that would remove more holders
// than the removal guard allows adds members and holds the removals for an
// admin. note is kept as the row's sentence when nothing else needs saying.
func (s *Syncer) reconcile(ctx context.Context, role store.Role, g store.EntraRoleGroup, note string) error {
	members, err := s.d.Graph.TransitiveMembers(ctx, g.GroupID)
	if errors.Is(err, entragraph.ErrNotFound) {
		if g.ReadAt == nil {
			return nil // not settled in Entra yet
		}
		g.State, g.Error = store.EntraGroupMissing, groupGone
		if err := s.d.Groups.Put(ctx, g); err != nil {
			return err
		}
		s.d.Record(ctx, map[string]any{"action": "entra.group.missing", "role": role.Name, "roleId": role.ID, "group": g.GroupID, "reason": g.Error})
		return nil
	}
	if err != nil {
		return err
	}
	want, skipped, nested, err := s.sort(ctx, g.GroupID, members)
	if err != nil {
		return err
	}
	s.setNested(role.ID, nested)
	asg, err := s.d.Roles.AssignmentsByRole(ctx, role.ID)
	if err != nil {
		return err
	}
	var steps, removes []membership.Step
	holders := 0
	for _, a := range asg {
		if a.Origin != store.OriginEntra {
			continue
		}
		holders++
		if !want[a.SubjectID] {
			removes = append(removes, membership.Step{UserID: a.SubjectID, Action: membership.Unassign, RowID: a.ID})
		}
		delete(want, a.SubjectID)
	}
	for id := range want {
		steps = append(steps, membership.Step{UserID: id, Action: membership.Assign})
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].UserID < steps[j].UserID })

	// The admin's release may have landed since the pass listed the rows.
	if cur, err := s.d.Groups.Get(ctx, role.ID); err == nil {
		g.State = cur.State
	}
	guard := s.d.Config.RemovalGuard
	held := g.State != store.EntraGroupReleased && len(removes) >= guard.MinMembers && len(removes)*100 > guard.Percent*holders
	wasHeld := g.State == store.EntraGroupHeld
	g.State, g.Error = store.EntraGroupLinked, note
	if held {
		g.State, g.Error = store.EntraGroupHeld, fmt.Sprintf(heldSentence, len(removes), holders)
	} else {
		steps = append(removes, steps...)
	}
	if len(steps) > 0 {
		if _, err := membership.Apply(ctx, s.d.Membership, role.ID, store.OriginEntra, steps); err != nil {
			return err
		}
	}
	now := s.d.Now()
	g.ReadAt, g.NotApplied = &now, encodeNotApplied(skipped)
	if err := s.d.Groups.Put(ctx, g); err != nil {
		return err
	}
	if held && !wasHeld {
		s.d.Record(ctx, map[string]any{"action": "entra.pass.held", "role": role.Name, "roleId": role.ID, "group": g.GroupID,
			"removals": len(removes), "holders": holders, "reason": g.Error})
	}
	return nil
}

// sort splits the members of a complete read of the group groupID into the
// Straza users that get the role, the members that do not with the reason
// for each, and the nested groups. A person is found, created or taken
// over as member does, an agent as agentMembers does, and both keep the
// role while disabled. The break-glass account is never given a role here.
func (s *Syncer) sort(ctx context.Context, groupID string, members []entragraph.Member) (map[string]bool, []NotApplied, []string, error) {
	want := map[string]bool{}
	var skipped []NotApplied
	var nested []string
	protected := ""
	if s.d.Protected != nil {
		protected = s.d.Protected(ctx)
	}
	people := s.readsPeople()
	var agentUsers []entragraph.Member
	for _, m := range members {
		reason := ""
		switch m.Kind {
		case entragraph.KindGroup:
			nested = append(nested, m.ID)
			continue
		case entragraph.KindAgentUser:
			agentUsers = append(agentUsers, m)
			continue
		case entragraph.KindUser:
			switch {
			case !people:
				reason = ReasonNotRead
			default:
				u, why, err := s.member(ctx, groupID, m)
				switch {
				case err != nil:
					return nil, nil, nil, err
				case why != "":
					reason = why
				case u.ID != protected:
					want[u.ID] = true
				}
			}
		default:
			reason = ReasonUnread
		}
		if reason != "" {
			skipped = append(skipped, NotApplied{ID: m.ID, Kind: string(m.Kind), Reason: reason})
		}
	}
	agents, err := s.agentMembers(ctx, groupID, agentUsers, nested, want)
	if err != nil {
		return nil, nil, nil, err
	}
	skipped = append(skipped, agents...)
	return want, skipped, nested, nil
}

// setNested records which nested groups reach the role, so a change in one
// of them makes the delta read the role's group again.
func (s *Syncer) setNested(roleID string, nested []string) {
	for id, roles := range s.nested {
		roles = slices.DeleteFunc(roles, func(r string) bool { return r == roleID })
		if len(roles) == 0 {
			delete(s.nested, id)
		} else {
			s.nested[id] = roles
		}
	}
	for _, id := range nested {
		s.nested[id] = append(s.nested[id], roleID)
	}
}

// encodeNotApplied renders the members that were not applied as the JSON
// list the group's row keeps, sorted by id and cut at notAppliedCap.
func encodeNotApplied(skipped []NotApplied) string {
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].ID < skipped[j].ID })
	if len(skipped) > notAppliedCap {
		skipped = skipped[:notAppliedCap]
	}
	if len(skipped) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(skipped)
	if err != nil {
		return "[]"
	}
	return string(raw)
}
