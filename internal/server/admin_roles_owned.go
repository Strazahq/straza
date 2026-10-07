package server

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// roleCreateRequest is the POST /v1/admin/roles body: the name, and for a
// server-owned role the owning server's name and its explicit tool list.
type roleCreateRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Kind        string   `json:"kind"`
	Server      string   `json:"server"`
	Tools       []string `json:"tools"`
}

// The refusals of the server-owned role rules, worded once in drafts.
const (
	ownedRoleKindErr    = drafts.OwnedRoleKindErr
	ownedRoleToolsErr   = drafts.OwnedRoleToolsErr
	ownedRoleServerErr  = drafts.OwnedRoleServerErr
	ownedRoleImpliesErr = drafts.OwnedRoleImpliesErr
)

// ownedRoleFields answers the owning server's name and the explicit tool
// list of a server-owned role's binding, both empty for a global role.
func (a *App) ownedRoleFields(ctx context.Context, role store.Role) (string, []string) {
	if role.OwnerAppID == "" {
		return "", nil
	}
	server := ""
	if row, err := a.store.Apps().GetByID(ctx, role.OwnerAppID); err == nil {
		server = row.Name
	}
	var tools []string
	if bindings, err := a.store.ToolBindings().ListByRole(ctx, role.ID); err == nil {
		for _, b := range bindings {
			if b.AppID == role.OwnerAppID {
				_ = json.Unmarshal([]byte(b.ToolMatcher), &tools)
			}
		}
	}
	return server, tools
}

// ownedToolsByRole reads each server-owned role's explicit tool list off
// its binding to the owning server, keyed by role id.
func ownedToolsByRole(roles []store.Role, bindings []store.ToolBinding) map[string][]string {
	owner := make(map[string]string, len(roles))
	for _, role := range roles {
		if role.OwnerAppID != "" {
			owner[role.ID] = role.OwnerAppID
		}
	}
	out := map[string][]string{}
	for _, b := range bindings {
		if owner[b.RoleID] != b.AppID {
			continue
		}
		var tools []string
		_ = json.Unmarshal([]byte(b.ToolMatcher), &tools)
		out[b.RoleID] = tools
	}
	return out
}

// rolesForStanding lists every role under a full standing and only the
// roles owned by the caller's servers under a server admin's, never a
// global role.
func (a *App) rolesForStanding(ctx context.Context, st adminStanding) ([]store.Role, error) {
	if st.Full {
		return a.store.Roles().List(ctx)
	}
	ids := make([]string, 0, len(st.Apps))
	for id := range st.Apps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []store.Role
	for _, id := range ids {
		owned, err := a.store.Roles().ListByOwner(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, owned...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
