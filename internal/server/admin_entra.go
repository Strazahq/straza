package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/entrasync"
	"github.com/strazahq/straza/internal/membership"
	"github.com/strazahq/straza/internal/store"
)

// roleEntraPayload is a role's security group in Entra on the roles list:
// its object id, empty until the create answered, the state of the link,
// the last complete read of its members, the sentence of what is wrong and
// what to do, and the members that were not given the role.
type roleEntraPayload struct {
	GroupID    string                 `json:"group_id"`
	State      string                 `json:"state"`
	ReadAt     *time.Time             `json:"read_at,omitempty"`
	Error      string                 `json:"error,omitempty"`
	NotApplied []entrasync.NotApplied `json:"not_applied,omitempty"`
}

// entraGroupsByRole reads every group link, keyed by role id. A read that
// fails answers none, and the roles list then carries no entra block,
// which a reader takes as not known.
func (a *App) entraGroupsByRole(ctx context.Context) map[string]*roleEntraPayload {
	rows, err := a.store.EntraGroups().List(ctx)
	if err != nil {
		a.reqlog(ctx).Warn("the roles list carries no group in Entra, because the links could not be read", "err", err)
		return nil
	}
	out := make(map[string]*roleEntraPayload, len(rows))
	for _, g := range rows {
		p := &roleEntraPayload{GroupID: g.GroupID, State: g.State, ReadAt: g.ReadAt, Error: g.Error}
		_ = json.Unmarshal([]byte(g.NotApplied), &p.NotApplied)
		out[g.RoleID] = p
	}
	return out
}

// entraStatusPayload is the answer of GET /v1/admin/entra: whether the
// Entra sync is on and for which tenant, the end of the last pass whose
// every request succeeded, since when the read has been failing with the
// sentence of the last failure, and how many roles have a pass on hold.
type entraStatusPayload struct {
	Enabled    bool       `json:"enabled"`
	Tenant     string     `json:"tenant,omitempty"`
	LastGood   *time.Time `json:"last_good,omitempty"`
	StaleSince *time.Time `json:"stale_since,omitempty"`
	Error      string     `json:"error,omitempty"`
	Held       int        `json:"held"`
}

func (a *App) handleEntraStatus(w http.ResponseWriter, r *http.Request) {
	st, err := entrasync.ReadStatus(r.Context(), a.store.Settings())
	var rows []store.EntraRoleGroup
	if err == nil {
		rows, err = a.store.EntraGroups().List(r.Context())
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the state of the read of Entra could not be loaded", err)
		return
	}
	cfg := a.cfg.Identity.Entra
	out := entraStatusPayload{Enabled: cfg.Enabled(), Tenant: cfg.Tenant, LastGood: st.LastGood, StaleSince: st.StaleSince, Error: st.Error}
	for _, g := range rows {
		if g.State == store.EntraGroupHeld {
			out.Held++
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// entraGroupOf loads the role of the request and its group link, and
// answers 404 for a role or a link that does not exist.
func (a *App) entraGroupOf(w http.ResponseWriter, r *http.Request) (store.Role, store.EntraRoleGroup, bool) {
	role, err := a.store.Roles().GetByID(r.Context(), r.PathValue("id"))
	var g store.EntraRoleGroup
	if err == nil {
		g, err = a.store.EntraGroups().Get(r.Context(), role.ID)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such role, or the role has no group in Entra")
		return store.Role{}, store.EntraRoleGroup{}, false
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError, "the role's group in Entra could not be loaded", err)
		return store.Role{}, store.EntraRoleGroup{}, false
	}
	return role, g, true
}

// handleEntraRelease is POST /v1/admin/roles/{id}/entra-group/release: an
// admin lets the removals through that the removal guard holds, and the
// sync's next pass applies them.
func (a *App) handleEntraRelease(w http.ResponseWriter, r *http.Request) {
	role, g, ok := a.entraGroupOf(w, r)
	if !ok {
		return
	}
	moved, err := a.store.EntraGroups().SetState(r.Context(), role.ID, store.EntraGroupHeld, store.EntraGroupReleased)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the removals could not be released", err)
		return
	}
	if !moved {
		apiError(w, http.StatusConflict, "nothing changed, because no removals of role "+role.Name+" are on hold. Read the role again to see its group's state")
		return
	}
	a.emitEventCtx(r.Context(), "straza.audit.admin", map[string]any{
		"action": "entra.pass.released", "role": role.Name, "roleId": role.ID, "group": g.GroupID,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

// reasonEntraRecreated is the reason on the record of each assignment that
// ends when an admin has a deleted group created again.
const reasonEntraRecreated = "the role's group in Entra was created again"

// handleEntraRecreate is POST /v1/admin/roles/{id}/entra-group/recreate:
// an admin has the group of a role created again after it was deleted in
// Entra. The assignments Entra gave the role end, because the new group
// starts with no members, and the sync creates the group at its next pass
// under a new name key, so the deleted group can still be restored in
// Entra without meeting it.
func (a *App) handleEntraRecreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role, g, ok := a.entraGroupOf(w, r)
	if !ok {
		return
	}
	if g.State != store.EntraGroupMissing {
		apiError(w, http.StatusConflict, "nothing changed, because the group of role "+role.Name+" is not marked as deleted in Entra. Read the role again to see its group's state")
		return
	}
	next, ok := nextUniqueName(g.UniqueName)
	if !ok {
		apiError(w, http.StatusConflict, "nothing changed, because the group of role "+role.Name+" was created again eight times already and Straza has no further name key for it. Delete the role and create it anew")
		return
	}
	asg, err := a.store.Roles().AssignmentsByRole(ctx, role.ID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the role's holders could not be read, so nothing changed", err)
		return
	}
	var steps []membership.Step
	for _, as := range asg {
		if as.Origin == store.OriginEntra {
			steps = append(steps, membership.Step{UserID: as.SubjectID, Action: membership.Unassign, RowID: as.ID})
		}
	}
	if _, err := membership.Apply(ctx, a.entraMembership(reasonEntraRecreated, reasonEntraRecreated, false), role.ID, store.OriginEntra, steps); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the role's holders from Entra could not be ended, so nothing changed", err)
		return
	}
	old := g.GroupID
	g.UniqueName, g.GroupID, g.State, g.ReadAt, g.Error, g.NotApplied = next, "", store.EntraGroupCreating, nil, "", "[]"
	if err := a.store.EntraGroups().Put(ctx, g); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the role's holders from Entra ended, and the new group could not be scheduled. Send the request again", err)
		return
	}
	a.emitEventCtx(ctx, "straza.audit.admin", map[string]any{
		"action": "entra.group.recreate", "role": role.Name, "roleId": role.ID, "group": old, "ended": len(steps),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "creating"})
}

// nextUniqueName answers the name key of a group that is created again:
// the role's first key with a counter from 2 to 9 behind it. A group's
// mailNickname carries the same value. The first key takes 61 of the 64
// characters a mailNickname holds, so a counter of one digit always fits.
func nextUniqueName(name string) (string, bool) {
	base, n := name, 1
	if i := strings.LastIndexByte(name, '-'); i == len(name)-2 {
		if d, err := strconv.Atoi(name[i+1:]); err == nil {
			base, n = name[:i], d
		}
	}
	if n >= 9 {
		return "", false
	}
	return base + "-" + strconv.Itoa(n+1), true
}

// entraMembership is how an assignment of the origin entra is written and
// reported: the identity events, the audit record with the reason for its
// direction, and the cached subjects of a user who lost a role dropped.
// With auto the records name no actor, because the sync wrote the change
// on what it read in Entra.
func (a *App) entraMembership(assigned, unassigned string, auto bool) membership.Deps {
	who := func(ctx context.Context) context.Context {
		if auto {
			return noActor(ctx)
		}
		return ctx
	}
	return membership.Deps{
		Roles: a.store.Roles(),
		IdentityChanged: func(ctx context.Context, id string) {
			a.resolver.Bump()
			a.emitEventCtx(who(ctx), "straza.identity.updated", map[string]any{"id": id, "origin": store.OriginEntra})
		},
		Changed: func(ctx context.Context, ch membership.Change) {
			action, reason := actionRolesAssign, assigned
			if ch.Action == membership.Unassign {
				action, reason = actionRolesUnassign, unassigned
				a.holdingChanged(ctx, ch.UserID)
			}
			a.pushRolesNudge(ch.UserID)
			a.auditAssignment(who(ctx), action, store.RoleAssignment{
				ID: ch.AssignmentID, SubjectKind: store.SubjectUser, SubjectID: ch.UserID,
				RoleID: ch.RoleID, Origin: ch.Origin,
			}, reason)
		},
	}
}
