package ctl

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Roles lists all roles.
func (c *Client) Roles(ctx context.Context) ([]Role, error) {
	var out []Role
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/roles", nil, &out)
}

// RolesJSON returns the roles list exactly as the server answered it.
func (c *Client) RolesJSON(ctx context.Context) ([]byte, error) {
	return c.DoBytes(ctx, http.MethodGet, "/v1/admin/roles")
}

// CreateRole creates a role. kind is the roles.kind enum on the wire
// (business, application, approver, straza); an empty kind stays off the body so the
// server's default applies. The enum is NOT validated here on purpose: the
// server owns it and answers a bad value with the reason (400), so a client
// copy would only drift.
//
// server names the MCP server that owns the role and tools names the tools
// the role reaches on that server. Both stay off the body when server is
// empty, so an older strazad reads the request it reads today.
func (c *Client) CreateRole(ctx context.Context, name, description, kind, server string, tools []string) (Role, error) {
	var out Role
	body := map[string]any{"name": name, "description": description}
	if kind != "" {
		body["kind"] = kind
	}
	if server != "" {
		body["server"] = server
		body["tools"] = tools
	}
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/roles", body, &out)
}

// RoleExport fetches the canonical VCS document for a role (spec/objects
// kind Role, 0.59.0): raw YAML bytes, server-serialized.
func (c *Client) RoleExport(ctx context.Context, name string) ([]byte, error) {
	role, err := c.RoleByName(ctx, name)
	if err != nil {
		return nil, err
	}
	return c.DoBytes(ctx, http.MethodGet, "/v1/admin/roles/"+url.PathEscape(role.ID)+"/export")
}

// RoleByName resolves a role name to a role.
func (c *Client) RoleByName(ctx context.Context, name string) (Role, error) {
	roles, err := c.Roles(ctx)
	if err != nil {
		return Role{}, err
	}
	for _, r := range roles {
		if r.Name == name {
			return r, nil
		}
	}
	return Role{}, fmt.Errorf("no role named %q", name)
}

// RoleDeleted is the answer of a role's deletion: the status, and the
// names of the live policy sets the server turned off with the role
// because each matched only that role. The list is absent from an older
// server.
type RoleDeleted struct {
	Status  string   `json:"status"`
	SetsOff []string `json:"sets_off"`
}

// DeleteRole deletes a role by name and answers what the server did. The
// server cascades the role's assignments and access rows, turns off every
// live policy set that matched only this role, and refuses (409) a role an
// active policy names as a decider pool, with the reason surfacing
// verbatim.
func (c *Client) DeleteRole(ctx context.Context, name string) (RoleDeleted, error) {
	role, err := c.RoleByName(ctx, name)
	if err != nil {
		return RoleDeleted{}, err
	}
	var out RoleDeleted
	return out, c.Do(ctx, http.MethodDelete, "/v1/admin/roles/"+url.PathEscape(role.ID), nil, &out)
}

// Assign grants a role to a user (both by name).
func (c *Client) Assign(ctx context.Context, username, roleName string) (Assignment, error) {
	u, err := c.UserByUsername(ctx, username)
	if err != nil {
		return Assignment{}, err
	}
	role, err := c.RoleByName(ctx, roleName)
	if err != nil {
		return Assignment{}, err
	}
	var out Assignment
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/assignments", map[string]string{
		"subject_kind": "user", "subject_id": u.ID, "role_id": role.ID,
	}, &out)
}

// Unassign takes a role away from a user (both by name). It reads the
// user's own assignments, sending the subject kind and the subject id
// together because the server filters only when it has both, deletes the
// row of that role, and says so when the user holds the role only through
// another role or not at all.
func (c *Client) Unassign(ctx context.Context, username, roleName string) error {
	u, err := c.UserByUsername(ctx, username)
	if err != nil {
		return err
	}
	role, err := c.RoleByName(ctx, roleName)
	if err != nil {
		return err
	}
	var rows []Assignment
	if err := c.Do(ctx, http.MethodGet, "/v1/admin/assignments?subject_kind=user&subject_id="+url.QueryEscape(u.ID), nil, &rows); err != nil {
		return err
	}
	for _, as := range rows {
		if as.SubjectKind == "user" && as.SubjectID == u.ID && as.RoleID == role.ID {
			return c.Do(ctx, http.MethodDelete, "/v1/admin/assignments/"+url.PathEscape(as.ID), nil, nil)
		}
	}
	return fmt.Errorf("%s does not hold %s directly, so there is no assignment to remove. A role reached through another role goes with that role, so take that role away instead", username, roleName)
}

// Assignments lists role assignments.
func (c *Client) Assignments(ctx context.Context) ([]Assignment, error) {
	var out []Assignment
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/assignments", nil, &out)
}

// Packs lists knowledge packs.
func (c *Client) Packs(ctx context.Context) ([]Pack, error) {
	var out []Pack
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/packs", nil, &out)
}

// CreatePack creates a knowledge pack.
func (c *Client) CreatePack(ctx context.Context, name, version, content string) (Pack, error) {
	var out Pack
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/packs", map[string]string{
		"name": name, "version": version, "content": content,
	}, &out)
}

// BindPack binds a pack to a role (both by name).
func (c *Client) BindPack(ctx context.Context, packName, roleName string) error {
	packs, err := c.Packs(ctx)
	if err != nil {
		return err
	}
	var pack Pack
	for _, p := range packs {
		if p.Name == packName {
			pack = p
		}
	}
	if pack.ID == "" {
		return fmt.Errorf("no pack named %q", packName)
	}
	role, err := c.RoleByName(ctx, roleName)
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodPost, "/v1/admin/packs/"+url.PathEscape(pack.ID)+"/bindings",
		map[string]string{"role_id": role.ID}, nil)
}

// UnbindPack removes a pack-role binding (both by name). The wire's binding
// id is the role id (openapi 0.58.0: the edge has no row id of its own).
func (c *Client) UnbindPack(ctx context.Context, packName, roleName string) error {
	packs, err := c.Packs(ctx)
	if err != nil {
		return err
	}
	var pack Pack
	for _, p := range packs {
		if p.Name == packName {
			pack = p
		}
	}
	if pack.ID == "" {
		return fmt.Errorf("no pack named %q", packName)
	}
	role, err := c.RoleByName(ctx, roleName)
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodDelete,
		"/v1/admin/packs/"+url.PathEscape(pack.ID)+"/bindings/"+url.PathEscape(role.ID), nil, nil)
}

// UpdateRole sets a role's description by name; the name and the kind are
// fixed at create, so the description is the one field an update carries.
func (c *Client) UpdateRole(ctx context.Context, name, description string) (Role, error) {
	role, err := c.RoleByName(ctx, name)
	if err != nil {
		return Role{}, err
	}
	var out Role
	return out, c.Do(ctx, http.MethodPatch, "/v1/admin/roles/"+url.PathEscape(role.ID), map[string]string{"description": description}, &out)
}

// RoleImplication is one outgoing implication edge of a role. ID is the
// implied role's id (the edge's natural key, what the DELETE consumes).
type RoleImplication struct {
	ID          string `json:"id"`
	ImpliesID   string `json:"implies_id"`
	ImpliesName string `json:"implies_name"`
}

// RoleImplications lists a role's outgoing implication edges by role name.
func (c *Client) RoleImplications(ctx context.Context, name string) ([]RoleImplication, error) {
	role, err := c.RoleByName(ctx, name)
	if err != nil {
		return nil, err
	}
	var out []RoleImplication
	return out, c.Do(ctx, http.MethodGet,
		"/v1/admin/roles/"+url.PathEscape(role.ID)+"/implications", nil, &out)
}

// AddRoleImplication adds one outgoing implication edge (both roles by name):
// holding name then also grants implied. The server refuses an edge that
// would close a cycle (409) and returns the reason verbatim.
func (c *Client) AddRoleImplication(ctx context.Context, name, implied string) error {
	role, err := c.RoleByName(ctx, name)
	if err != nil {
		return err
	}
	target, err := c.RoleByName(ctx, implied)
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodPost,
		"/v1/admin/roles/"+url.PathEscape(role.ID)+"/implications",
		map[string]string{"implies_role_id": target.ID}, nil)
}

// RemoveRoleImplication removes one outgoing implication edge (both roles by
// name). The edge has no row id of its own (composite key role+implies), so
// the DELETE addresses it by the IMPLIED role's id inside the holder's scope,
// the same id RoleImplications reports as ID.
func (c *Client) RemoveRoleImplication(ctx context.Context, name, implied string) error {
	role, err := c.RoleByName(ctx, name)
	if err != nil {
		return err
	}
	target, err := c.RoleByName(ctx, implied)
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodDelete,
		"/v1/admin/roles/"+url.PathEscape(role.ID)+"/implications/"+url.PathEscape(target.ID),
		nil, nil)
}
