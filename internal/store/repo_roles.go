package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type roleRepo struct{ s *sqlStore }

const roleCols = "id, name, description, kind, plane, COALESCE(owner_app_id, ''), created_at, updated_at"

// OwnedRolePrefix is what a server-owned role's name begins with: the
// server's name folded to a-z, 0-9 and single hyphens, then one hyphen.
func OwnedRolePrefix(appName string) string {
	return foldRoleName(appName) + "-"
}

func scanRole(row scanner) (Role, error) {
	var r Role
	var ca, ua scanTime
	if err := row.Scan(&r.ID, &r.Name, &r.Description, &r.Kind, &r.Plane, &r.OwnerAppID, &ca, &ua); err != nil {
		return Role{}, scanErr(err)
	}
	r.CreatedAt, r.UpdatedAt = ca.t, ua.t
	return r, nil
}

// insertRole writes the role row on db, the pool or an open transaction,
// with an empty owner stored as NULL.
func (r roleRepo) insertRole(ctx context.Context, db dbExec, role Role) error {
	var owner any
	if role.OwnerAppID != "" {
		owner = role.OwnerAppID
	}
	_, err := db.ExecContext(ctx, r.s.q(`INSERT INTO roles (id, name, description, kind, plane, owner_app_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`),
		role.ID, role.Name, role.Description, role.Kind, role.Plane, owner, r.s.tArg(role.CreatedAt), r.s.tArg(role.UpdatedAt))
	return r.s.mapErr(err)
}

func (r roleRepo) Create(ctx context.Context, role Role) (Role, error) {
	role.ID = newID()
	role.CreatedAt = now()
	role.UpdatedAt = role.CreatedAt
	if role.Kind == "" {
		role.Kind = RoleKindBusiness
	}
	if role.Plane == "" {
		role.Plane = RolePlaneAccess
	}
	if err := r.insertRole(ctx, r.s.db, role); err != nil {
		return Role{}, err
	}
	return role, nil
}

// CreateOwned creates a server-owned application role on the access plane
// and its one binding to the owning server, both inside one transaction.
func (r roleRepo) CreateOwned(ctx context.Context, role Role, toolMatcher string) (Role, ToolBinding, error) {
	if role.OwnerAppID == "" {
		return Role{}, ToolBinding{}, errors.New("store: a server-owned role needs an owner app id")
	}
	role.ID = newID()
	role.CreatedAt = now()
	role.UpdatedAt = role.CreatedAt
	role.Kind, role.Plane = RoleKindApplication, RolePlaneAccess
	b := ToolBinding{ID: newID(), RoleID: role.ID, AppID: role.OwnerAppID, ToolMatcher: toolMatcher, Effect: "allow", CreatedAt: role.CreatedAt}
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return Role{}, ToolBinding{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.insertRole(ctx, tx, role); err != nil {
		return Role{}, ToolBinding{}, err
	}
	_, err = tx.ExecContext(ctx, r.s.q(`INSERT INTO tool_bindings (id, role_id, app_id, tool_matcher, effect, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`),
		b.ID, b.RoleID, b.AppID, b.ToolMatcher, b.Effect, r.s.tArg(b.CreatedAt))
	if err != nil {
		return Role{}, ToolBinding{}, r.s.mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return Role{}, ToolBinding{}, r.s.mapErr(err)
	}
	return role, b, nil
}

func (r roleRepo) ListByOwner(ctx context.Context, appID string) ([]Role, error) {
	return r.roles(ctx, `SELECT `+roleCols+` FROM roles WHERE owner_app_id = $1 ORDER BY name`, appID)
}

// HolderCount walks the implication edges backwards from the role in one
// recursive query and counts each subject assigned anywhere on that set
// once. Windows are included: a holder assigned for later still holds the
// meaning the identity manager certified.
func (r roleRepo) HolderCount(ctx context.Context, roleID string) (int, error) {
	var n int
	err := r.s.queryRow(ctx, `WITH RECURSIVE reach(id) AS (
			SELECT CAST($1 AS TEXT)
			UNION
			SELECT ri.role_id FROM role_implications ri JOIN reach ON ri.implies_role_id = reach.id
		)
		SELECT COUNT(DISTINCT subject_id) FROM role_assignments WHERE role_id IN (SELECT id FROM reach)`, roleID).Scan(&n)
	return n, err
}

func (r roleRepo) GetByID(ctx context.Context, id string) (Role, error) {
	return scanRole(r.s.queryRow(ctx, `SELECT `+roleCols+` FROM roles WHERE id = $1`, id))
}

func (r roleRepo) GetByName(ctx context.Context, name string) (Role, error) {
	return scanRole(r.s.queryRow(ctx, `SELECT `+roleCols+` FROM roles WHERE name = $1`, name))
}

func (r roleRepo) List(ctx context.Context) ([]Role, error) {
	return r.roles(ctx, `SELECT `+roleCols+` FROM roles ORDER BY name`)
}

func (r roleRepo) roles(ctx context.Context, query string, args ...any) ([]Role, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Role
	for rows.Next() {
		role, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, rows.Err()
}

// Update writes the mutable fields. Plane is deliberately NOT in the SET
// list: flipping a role between planes would let a PATCH turn straza-admin
// into a bindable access role (or strand an access role beyond SCIM's
// reach), so the plane is fixed at create.
func (r roleRepo) Update(ctx context.Context, role Role) (Role, error) {
	role.UpdatedAt = now()
	err := mustAffect(r.s.exec(ctx, `UPDATE roles SET name = $1, description = $2, kind = $3, updated_at = $4
		WHERE id = $5`, role.Name, role.Description, role.Kind, r.s.tArg(role.UpdatedAt), role.ID))
	if err != nil {
		return Role{}, err
	}
	return r.GetByID(ctx, role.ID)
}

func (r roleRepo) Delete(ctx context.Context, id string) error {
	return mustAffect(r.s.exec(ctx, `DELETE FROM roles WHERE id = $1`, id))
}

func (r roleRepo) AddImplication(ctx context.Context, roleID, impliesRoleID string) error {
	_, err := r.s.exec(ctx,
		`INSERT INTO role_implications (role_id, implies_role_id) VALUES ($1, $2)`, roleID, impliesRoleID)
	return err
}

func (r roleRepo) RemoveImplication(ctx context.Context, roleID, impliesRoleID string) error {
	return mustAffect(r.s.exec(ctx,
		`DELETE FROM role_implications WHERE role_id = $1 AND implies_role_id = $2`, roleID, impliesRoleID))
}

func (r roleRepo) ListImplications(ctx context.Context) ([]RoleImplication, error) {
	rows, err := r.s.query(ctx,
		`SELECT role_id, implies_role_id FROM role_implications ORDER BY role_id, implies_role_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RoleImplication
	for rows.Next() {
		var imp RoleImplication
		if err := rows.Scan(&imp.RoleID, &imp.ImpliesRoleID); err != nil {
			return nil, err
		}
		out = append(out, imp)
	}
	return out, rows.Err()
}

const assignmentCols = "id, subject_kind, subject_id, role_id, valid_from, valid_to, origin, created_at"

func scanAssignment(row scanner) (RoleAssignment, error) {
	var a RoleAssignment
	var vf, vt scanTimePtr
	var ca scanTime
	if err := row.Scan(&a.ID, &a.SubjectKind, &a.SubjectID, &a.RoleID, &vf, &vt, &a.Origin, &ca); err != nil {
		return RoleAssignment{}, scanErr(err)
	}
	a.ValidFrom, a.ValidTo, a.CreatedAt = vf.t, vt.t, ca.t
	return a, nil
}

func (r roleRepo) Assign(ctx context.Context, a RoleAssignment) (RoleAssignment, error) {
	a.ID = newID()
	a.CreatedAt = now()
	if a.Origin == "" {
		a.Origin = OriginAdmin
	}
	_, err := r.s.exec(ctx, `INSERT INTO role_assignments (id, subject_kind, subject_id, role_id, valid_from, valid_to, origin, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		a.ID, a.SubjectKind, a.SubjectID, a.RoleID,
		r.s.tArgPtr(a.ValidFrom), r.s.tArgPtr(a.ValidTo), a.Origin, r.s.tArg(a.CreatedAt))
	if err != nil {
		return RoleAssignment{}, err
	}
	return a, nil
}

func (r roleRepo) Unassign(ctx context.Context, assignmentID string) error {
	return mustAffect(r.s.exec(ctx, `DELETE FROM role_assignments WHERE id = $1`, assignmentID))
}

func (r roleRepo) ApplyMembership(ctx context.Context, roleID string, add []RoleAssignment, removeIDs []string) ([]RoleAssignment, []RoleAssignment, error) {
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Each statement returns the row it changed, and none when there was
	// nothing to change: a row already deleted, or a holder a concurrent
	// write inserted first, which the unique key turns into a skip.
	var removed []RoleAssignment
	for _, id := range removeIDs {
		gone, err := r.txAssignments(ctx, tx, `DELETE FROM role_assignments WHERE id = $1 AND role_id = $2
			RETURNING `+assignmentCols, id, roleID)
		if err != nil {
			return nil, nil, err
		}
		removed = append(removed, gone...)
	}
	var added []RoleAssignment
	for _, a := range add {
		a.ID, a.RoleID, a.CreatedAt = newID(), roleID, now()
		if a.Origin == "" {
			a.Origin = OriginAdmin
		}
		made, err := r.txAssignments(ctx, tx, `INSERT INTO role_assignments (id, subject_kind, subject_id, role_id, valid_from, valid_to, origin, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (subject_kind, subject_id, role_id) DO NOTHING
			RETURNING `+assignmentCols,
			a.ID, a.SubjectKind, a.SubjectID, a.RoleID,
			r.s.tArgPtr(a.ValidFrom), r.s.tArgPtr(a.ValidTo), a.Origin, r.s.tArg(a.CreatedAt))
		if err != nil {
			return nil, nil, err
		}
		added = append(added, made...)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, r.s.mapErr(err)
	}
	return added, removed, nil
}

// txAssignments runs one statement inside tx and scans the assignment rows
// it returns, closing them before the transaction's next statement.
func (r roleRepo) txAssignments(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]RoleAssignment, error) {
	rows, err := tx.QueryContext(ctx, r.s.q(query), args...)
	if err != nil {
		return nil, r.s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []RoleAssignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, r.s.mapErr(err)
	}
	return out, nil
}

func (r roleRepo) Assignment(ctx context.Context, assignmentID string) (RoleAssignment, error) {
	out, err := r.assignments(ctx, `SELECT `+assignmentCols+` FROM role_assignments
		WHERE id = $1`, assignmentID)
	if err != nil {
		return RoleAssignment{}, err
	}
	if len(out) == 0 {
		return RoleAssignment{}, ErrNotFound
	}
	return out[0], nil
}

func (r roleRepo) ListAssignments(ctx context.Context, subjectKind, subjectID string) ([]RoleAssignment, error) {
	return r.assignments(ctx, `SELECT `+assignmentCols+` FROM role_assignments
		WHERE subject_kind = $1 AND subject_id = $2 ORDER BY created_at`, subjectKind, subjectID)
}

// AssignmentsByRole lists every assignment row carrying the role: the SCIM
// wire-group members render (spec/scim-profile rev 12) reads holders this
// way. Control plane only; decisions never touch it.
func (r roleRepo) AssignmentsByRole(ctx context.Context, roleID string) ([]RoleAssignment, error) {
	return r.assignments(ctx, `SELECT `+assignmentCols+` FROM role_assignments
		WHERE role_id = $1 ORDER BY created_at`, roleID)
}

func (r roleRepo) ListAllAssignments(ctx context.Context) ([]RoleAssignment, error) {
	return r.assignments(ctx, `SELECT `+assignmentCols+` FROM role_assignments ORDER BY created_at`)
}

func (r roleRepo) AssignmentCountsByRole(ctx context.Context) (map[string]int, error) {
	rows, err := r.s.query(ctx, `SELECT role_id, COUNT(*) FROM role_assignments GROUP BY role_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (r roleRepo) AssignmentPairsInForce(ctx context.Context, t time.Time) ([]AssignmentPair, error) {
	rows, err := r.s.query(ctx, `SELECT role_id, subject_id FROM role_assignments
		WHERE (valid_from IS NULL OR valid_from <= $1) AND (valid_to IS NULL OR valid_to > $2)`,
		r.s.tArg(t), r.s.tArg(t))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AssignmentPair
	for rows.Next() {
		var p AssignmentPair
		if err := rows.Scan(&p.RoleID, &p.SubjectID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r roleRepo) assignments(ctx context.Context, query string, args ...any) ([]RoleAssignment, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RoleAssignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
