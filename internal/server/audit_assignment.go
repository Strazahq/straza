package server

import (
	"context"

	"github.com/strazahq/straza/internal/store"
)

// Role audit actions: one straza.audit.admin record per role grant that
// starts or ends, on every lane that writes one, and one per role row that
// starts or ends.
const (
	actionRolesAssign   = "roles.assign"
	actionRolesUnassign = "roles.unassign"
	actionRolesCreate   = "roles.create"
	actionRolesDelete   = "roles.delete"
)

// auditAssignment chains one straza.audit.admin record for a role grant that
// started (roles.assign) or ended (roles.unassign). target is the assignment
// id, user the subject, role the role's name (its id when the role is gone),
// roleId the role id, origin the lane that wrote the row (scim or admin) and
// reason the caller's sentence when it has one. The actor triple is stamped by
// emitEventCtx from the context, so a caller without an authenticated
// principal chains the record with the actor fields absent.
func (a *App) auditAssignment(ctx context.Context, action string, as store.RoleAssignment, reason string) {
	roleName := as.RoleID
	if role, err := a.store.Roles().GetByID(ctx, as.RoleID); err == nil {
		roleName = role.Name
	}
	data := map[string]any{
		"action": action, "target": as.ID, "user": as.SubjectID,
		"role": roleName, "roleId": as.RoleID, "origin": as.Origin,
	}
	if reason != "" {
		data["reason"] = reason
	}
	a.emitEventCtx(ctx, "straza.audit.admin", data)
}
