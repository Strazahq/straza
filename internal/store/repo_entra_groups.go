package store

import (
	"context"
	"time"
)

// States of an EntraRoleGroup.
const (
	// EntraGroupCreating: the create in Entra has not been confirmed yet.
	EntraGroupCreating = "creating"
	// EntraGroupLinked: the group exists and its members are applied.
	EntraGroupLinked = "linked"
	// EntraGroupMissing: a group that was read before is gone from Entra,
	// and an admin decides whether to create it again.
	EntraGroupMissing = "missing"
	// EntraGroupDeleting: the role is gone and its group is being deleted.
	EntraGroupDeleting = "deleting"
	// EntraGroupHeld: a pass would remove a large share of the role's
	// holders and waits for an admin.
	EntraGroupHeld = "held"
	// EntraGroupReleased: an admin let the held removals through, and the
	// next pass applies them.
	EntraGroupReleased = "released"
)

// EntraRoleGroup links a role to its security group in Microsoft Entra ID.
// The row outlives a deleted role until its group is deleted too. GroupID
// is empty until the create answered. ReadAt is the last complete read of
// the members, nil before the first. Error is the sentence of the last
// failure, empty after a good pass. NotApplied is a JSON list of the
// members the last read found and did not apply.
type EntraRoleGroup struct {
	RoleID     string
	UniqueName string
	GroupID    string
	State      string
	ReadAt     *time.Time
	Error      string
	NotApplied string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// EntraGroupRepo holds the links between roles and their groups in Entra.
// Only the Entra sync and the two admin actions on a group write it.
type EntraGroupRepo interface {
	List(ctx context.Context) ([]EntraRoleGroup, error)
	// Get answers ErrNotFound for a role that has no row.
	Get(ctx context.Context, roleID string) (EntraRoleGroup, error)
	// Put stores g whole under its RoleID, creating the row or replacing
	// it. An empty NotApplied is stored as an empty list.
	Put(ctx context.Context, g EntraRoleGroup) error
	// SetState moves the row of roleID from the state from to the state
	// to and answers whether it did, so an admin action and a pass that
	// race leave one winner.
	SetState(ctx context.Context, roleID, from, to string) (bool, error)
	Delete(ctx context.Context, roleID string) error
}

type entraGroupRepo struct{ s *sqlStore }

// EntraGroups answers the Entra group repo over the same sqlStore.
func (s *sqlStore) EntraGroups() EntraGroupRepo { return entraGroupRepo{s} }

const entraGroupCols = "role_id, unique_name, group_id, state, read_at, error, not_applied, created_at, updated_at"

func scanEntraGroup(row scanner) (EntraRoleGroup, error) {
	var g EntraRoleGroup
	var read scanTimePtr
	var created, updated scanTime
	if err := row.Scan(&g.RoleID, &g.UniqueName, &g.GroupID, &g.State, &read, &g.Error, &g.NotApplied, &created, &updated); err != nil {
		return EntraRoleGroup{}, scanErr(err)
	}
	g.ReadAt, g.CreatedAt, g.UpdatedAt = read.t, created.t, updated.t
	return g, nil
}

func (r entraGroupRepo) List(ctx context.Context) ([]EntraRoleGroup, error) {
	rows, err := r.s.query(ctx, `SELECT `+entraGroupCols+` FROM entra_role_groups ORDER BY unique_name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []EntraRoleGroup
	for rows.Next() {
		g, err := scanEntraGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r entraGroupRepo) Get(ctx context.Context, roleID string) (EntraRoleGroup, error) {
	return scanEntraGroup(r.s.queryRow(ctx, `SELECT `+entraGroupCols+` FROM entra_role_groups WHERE role_id = $1`, roleID))
}

func (r entraGroupRepo) Put(ctx context.Context, g EntraRoleGroup) error {
	if g.NotApplied == "" {
		g.NotApplied = "[]"
	}
	t := now()
	_, err := r.s.exec(ctx, `INSERT INTO entra_role_groups (`+entraGroupCols+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (role_id) DO UPDATE SET unique_name = excluded.unique_name, group_id = excluded.group_id,
			state = excluded.state, read_at = excluded.read_at, error = excluded.error,
			not_applied = excluded.not_applied, updated_at = excluded.updated_at`,
		g.RoleID, g.UniqueName, g.GroupID, g.State, r.s.tArgPtr(g.ReadAt), g.Error, g.NotApplied, r.s.tArg(t), r.s.tArg(t))
	return err
}

func (r entraGroupRepo) SetState(ctx context.Context, roleID, from, to string) (bool, error) {
	res, err := r.s.exec(ctx, `UPDATE entra_role_groups SET state = $1, updated_at = $2 WHERE role_id = $3 AND state = $4`,
		to, r.s.tArg(now()), roleID, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r entraGroupRepo) Delete(ctx context.Context, roleID string) error {
	_, err := r.s.exec(ctx, `DELETE FROM entra_role_groups WHERE role_id = $1`, roleID)
	return err
}
