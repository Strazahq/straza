package entrasync

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/store"
)

// Sentences a group's row carries while Straza cannot act on the group.
const (
	notOwnerOnCreate = "A group with this role's name key exists in Entra and Straza is not its owner, so Straza reads no members from it. Delete that group in Entra, or add the Straza service app as its owner, and Straza links it at its next pass."
	notOwner         = "Straza is no longer an owner of this group in Entra, so it cannot rename or delete it. Members are still read. Add the Straza service app as an owner of the group in Entra."
	groupGone        = "The group of this role was deleted in Entra, so Straza reads no members and the role keeps its last holders. Create the group again, which ends the holders Entra gave the role, or delete the role."
	groupReplaced    = "Entra holds another group under this role's name key than the one Straza created, so Straza reads no members and the role keeps its last holders. Create the group again, which ends the holders Entra gave the role, or delete the role."
)

// descriptionCap is the longest description Graph accepts for a group.
const descriptionCap = 1024

// UniqueName is the key under which a role's group is created in Entra. It
// holds ids only, because it can never change.
func UniqueName(deployment, roleID string) string {
	return "straza-" + deployment + "-role-" + roleID
}

func (s *Syncer) displayName(r store.Role) string { return s.d.Config.GroupNamePrefix + r.Name }

// description is the group's description, written for an Entra admin who
// does not know Straza: the role its members hold, the role's own
// description, and that Straza manages the group. The role's description
// is cut first, ending in "...", when the whole would pass Graph's cap. A
// sentence on what the role grants is left out, because the sync reads no
// tool bindings and no server's tools.
func description(r store.Role) string {
	head := "Members of this group hold the Straza role " + r.Name + "."
	tail := "Straza created this group and manages it. Do not rename or delete it."
	d := strings.TrimSpace(r.Description)
	if d == "" {
		return head + " " + tail
	}
	if !strings.ContainsAny(d[len(d)-1:], ".!?") {
		d += "." // the parts are joined by a space, so the next sentence would run on
	}
	if room := descriptionCap - len(head) - len(tail) - 2; len(d) > room {
		n := max(room-len("..."), 0)
		for n > 0 && !utf8.RuneStart(d[n]) {
			n--
		}
		d = strings.TrimSpace(d[:n]) + "..."
	}
	return head + " " + d + " " + tail
}

// oneGroup reports whether err concerns one group alone: Graph refused the
// request or did not find the group. Any other failure may hit every group
// and ends the pass.
func oneGroup(err error) bool {
	var ge *entragraph.Error
	return errors.As(err, &ge) && (ge.Status == 400 || ge.Status == 403 || ge.Status == 404)
}

// lifecycle makes the group rows match the roles: a role with no row gets
// one, a row whose create is not confirmed is tried again, and a row whose
// role is gone has its group deleted and is then removed.
func (s *Syncer) lifecycle(ctx context.Context, roles []store.Role, byID map[string]store.Role) error {
	rows, err := s.d.Groups.List(ctx)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(rows))
	for _, g := range rows {
		have[g.RoleID] = true
	}
	for _, r := range roles {
		if !have[r.ID] {
			rows = append(rows, store.EntraRoleGroup{RoleID: r.ID, UniqueName: UniqueName(s.d.Deployment, r.ID), State: store.EntraGroupCreating})
		}
	}
	for _, g := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		role, alive := byID[g.RoleID]
		switch {
		case !alive:
			err = s.deleteGroup(ctx, g)
		case g.State == store.EntraGroupCreating:
			err = s.createGroup(ctx, role, g)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// createGroup creates the role's group, or finds the one an earlier create
// made, and links it once a write of Straza's own to it succeeded, which
// Entra allows only to an owner. A group Graph does not show yet stays in
// the creating state and is tried at the next pass.
func (s *Syncer) createGroup(ctx context.Context, role store.Role, g store.EntraRoleGroup) error {
	name, desc := s.displayName(role), description(role)
	made, err := s.d.Graph.EnsureGroup(ctx, entragraph.GroupSpec{UniqueName: g.UniqueName, DisplayName: name, Description: desc})
	if err == nil {
		err = s.d.Graph.UpdateGroup(ctx, made.ID, name, desc)
	}
	switch {
	case errors.Is(err, entragraph.ErrNotFound):
		g.Error = ""
	case err != nil && oneGroup(err):
		g.Error = err.Error()
		var ge *entragraph.Error
		if errors.As(err, &ge) && ge.Status == 403 {
			g.Error = notOwnerOnCreate
		}
	case err != nil:
		return err
	default:
		g.GroupID, g.State, g.Error = made.ID, store.EntraGroupLinked, ""
	}
	if err := s.d.Groups.Put(ctx, g); err != nil {
		return err
	}
	if g.State == store.EntraGroupLinked {
		s.d.Record(ctx, map[string]any{"action": "entra.group.create", "role": role.Name, "roleId": role.ID, "group": g.GroupID})
	}
	return nil
}

// deleteGroup deletes the group of a role that is gone and removes its row.
// A group Graph no longer knows counts as deleted. A row whose create never
// answered is looked up by its uniqueName first, because the create may
// have landed.
func (s *Syncer) deleteGroup(ctx context.Context, g store.EntraRoleGroup) error {
	id := g.GroupID
	if id == "" {
		found, err := s.d.Graph.GroupByUniqueName(ctx, g.UniqueName)
		if errors.Is(err, entragraph.ErrNotFound) {
			return s.d.Groups.Delete(ctx, g.RoleID)
		}
		if err != nil {
			return err
		}
		id = found.ID
	}
	err := s.d.Graph.DeleteGroup(ctx, id)
	if err != nil && !errors.Is(err, entragraph.ErrNotFound) {
		if !oneGroup(err) {
			return err
		}
		g.State, g.Error = store.EntraGroupDeleting, "The role is deleted and Straza could not delete its group in Entra, because it is no longer an owner of it. Delete the group in Entra."
		return s.d.Groups.Put(ctx, g)
	}
	if err := s.d.Groups.Delete(ctx, g.RoleID); err != nil {
		return err
	}
	s.d.Record(ctx, map[string]any{"action": "entra.group.delete", "roleId": g.RoleID, "group": id})
	return nil
}

// checkGroup reads a linked group and writes Straza's name and description
// back when either differs, so a rename in Entra or a new description in
// Straza is repaired. It marks a group that is gone, or replaced under the
// same uniqueName, as missing in g, with one audit record. The note it
// answers is the sentence for a group Straza can read and no longer write.
func (s *Syncer) checkGroup(ctx context.Context, role store.Role, g *store.EntraRoleGroup) (string, error) {
	found, err := s.d.Graph.GroupByUniqueName(ctx, g.UniqueName)
	gone := errors.Is(err, entragraph.ErrNotFound) && g.ReadAt != nil
	switch {
	case errors.Is(err, entragraph.ErrNotFound) && !gone:
		return "", nil // not settled in Entra yet
	case err != nil && !gone:
		return "", err
	case gone || found.ID != g.GroupID:
		g.State, g.Error = store.EntraGroupMissing, groupGone
		if !gone {
			g.Error = groupReplaced
		}
		if err := s.d.Groups.Put(ctx, *g); err != nil {
			return "", err
		}
		s.d.Record(ctx, map[string]any{"action": "entra.group.missing", "role": role.Name, "roleId": role.ID, "group": g.GroupID, "reason": g.Error})
		return "", nil
	}
	name, desc := s.displayName(role), description(role)
	if found.DisplayName == name && found.Description == desc {
		return "", nil
	}
	if err := s.d.Graph.UpdateGroup(ctx, g.GroupID, name, desc); err != nil {
		if oneGroup(err) {
			return notOwner, nil
		}
		return "", err
	}
	s.d.Record(ctx, map[string]any{"action": "entra.group.update", "role": role.Name, "roleId": role.ID, "group": g.GroupID, "name": name, "description": desc})
	return "", nil
}
