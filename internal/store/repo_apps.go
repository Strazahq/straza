package store

import (
	"context"
	"database/sql"
)

type appRepo struct{ s *sqlStore }

const appCols = "id, name, version, manifest, runtime_kind, status, source, admin_role_id, created_at, updated_at, deleted_at"

func scanApp(row scanner) (App, error) {
	var a App
	var ar sql.NullString
	var ca, ua scanTime
	var da scanTimePtr
	if err := row.Scan(&a.ID, &a.Name, &a.Version, &a.Manifest, &a.RuntimeKind,
		&a.Status, &a.Source, &ar, &ca, &ua, &da); err != nil {
		return App{}, scanErr(err)
	}
	a.AdminRoleID = ar.String
	a.CreatedAt, a.UpdatedAt, a.DeletedAt = ca.t, ua.t, da.t
	return a, nil
}

// Create inserts the row and, when the caller names no admin role, mints
// one in the same transaction, so a refused insert leaves no stray role
// and a committed row never reads empty. A named role must exist.
func (r appRepo) Create(ctx context.Context, a App) (App, error) {
	a.ID = newID()
	a.CreatedAt = now()
	a.UpdatedAt = a.CreatedAt
	if a.Manifest == "" {
		a.Manifest = "{}"
	}
	if a.Status == "" {
		a.Status = "pending"
	}
	if a.Source == "" {
		a.Source = "api"
	}
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return App{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if a.AdminRoleID == "" {
		role, err := r.mintAdminRole(ctx, tx, a.Name)
		if err != nil {
			return App{}, err
		}
		a.AdminRoleID = role.ID
	} else if err := r.roleExists(ctx, tx, a.AdminRoleID); err != nil {
		return App{}, err
	}
	_, err = tx.ExecContext(ctx, r.s.q(`INSERT INTO apps (id, name, version, manifest, runtime_kind, status, source, admin_role_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`),
		a.ID, a.Name, a.Version, a.Manifest, a.RuntimeKind, a.Status, a.Source, a.AdminRoleID,
		r.s.tArg(a.CreatedAt), r.s.tArg(a.UpdatedAt))
	if err != nil {
		return App{}, r.s.mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return App{}, r.s.mapErr(err)
	}
	return a, nil
}

func (r appRepo) GetByID(ctx context.Context, id string) (App, error) {
	return scanApp(r.s.queryRow(ctx,
		`SELECT `+appCols+` FROM apps WHERE id = $1 AND deleted_at IS NULL`, id))
}

func (r appRepo) GetByName(ctx context.Context, name string) (App, error) {
	return scanApp(r.s.queryRow(ctx,
		`SELECT `+appCols+` FROM apps WHERE name = $1 AND deleted_at IS NULL`, name))
}

func (r appRepo) List(ctx context.Context) ([]App, error) {
	rows, err := r.s.query(ctx,
		`SELECT `+appCols+` FROM apps WHERE deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r appRepo) Update(ctx context.Context, a App) (App, error) {
	a.UpdatedAt = now()
	err := mustAffect(r.s.exec(ctx, `UPDATE apps SET name = $1, version = $2, manifest = $3, runtime_kind = $4,
		status = $5, source = $6, updated_at = $7 WHERE id = $8 AND deleted_at IS NULL`,
		a.Name, a.Version, a.Manifest, a.RuntimeKind, a.Status, a.Source, r.s.tArg(a.UpdatedAt), a.ID))
	if err != nil {
		return App{}, err
	}
	return r.GetByID(ctx, a.ID)
}

func (r appRepo) SoftDelete(ctx context.Context, id string) error {
	t := r.s.tArg(now())
	return mustAffect(r.s.exec(ctx, `UPDATE apps SET deleted_at = $1, status = 'stopped', updated_at = $2
		WHERE id = $3 AND deleted_at IS NULL`, t, t, id))
}

// Revive brings the row back and re-mints its admin role when the old one
// was deleted while the row lay removed, so a revived server never reads
// empty either. An empty manifest is stored as {}, as Create stores it.
func (r appRepo) Revive(ctx context.Context, a App) (App, error) {
	a.UpdatedAt = now()
	if a.Manifest == "" {
		a.Manifest = "{}"
	}
	err := mustAffect(r.s.exec(ctx, `UPDATE apps SET version = $1, manifest = $2, runtime_kind = $3,
		status = $4, source = $5, updated_at = $6, deleted_at = NULL WHERE name = $7 AND deleted_at IS NOT NULL`,
		a.Version, a.Manifest, a.RuntimeKind, a.Status, a.Source, r.s.tArg(a.UpdatedAt), a.Name))
	if err != nil {
		return App{}, err
	}
	row, err := r.GetByName(ctx, a.Name)
	if err != nil {
		return App{}, err
	}
	if _, err := r.ensureAdminRole(ctx, row); err != nil {
		return App{}, err
	}
	return r.GetByName(ctx, a.Name)
}

type toolBindingRepo struct{ s *sqlStore }

const toolBindingCols = "id, role_id, app_id, tool_matcher, effect, created_at"

func scanToolBinding(row scanner) (ToolBinding, error) {
	var b ToolBinding
	var ca scanTime
	if err := row.Scan(&b.ID, &b.RoleID, &b.AppID, &b.ToolMatcher, &b.Effect, &ca); err != nil {
		return ToolBinding{}, scanErr(err)
	}
	b.CreatedAt = ca.t
	return b, nil
}

func (r toolBindingRepo) Create(ctx context.Context, b ToolBinding) (ToolBinding, error) {
	b.ID = newID()
	b.CreatedAt = now()
	if b.ToolMatcher == "" {
		b.ToolMatcher = "[]"
	}
	if b.Effect == "" {
		b.Effect = "allow"
	}
	_, err := r.s.exec(ctx, `INSERT INTO tool_bindings (id, role_id, app_id, tool_matcher, effect, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		b.ID, b.RoleID, b.AppID, b.ToolMatcher, b.Effect, r.s.tArg(b.CreatedAt))
	if err != nil {
		return ToolBinding{}, err
	}
	return b, nil
}

func (r toolBindingRepo) List(ctx context.Context) ([]ToolBinding, error) {
	return r.bindings(ctx, `SELECT `+toolBindingCols+` FROM tool_bindings ORDER BY created_at`)
}

func (r toolBindingRepo) ListByRole(ctx context.Context, roleID string) ([]ToolBinding, error) {
	return r.bindings(ctx,
		`SELECT `+toolBindingCols+` FROM tool_bindings WHERE role_id = $1 ORDER BY created_at`, roleID)
}

func (r toolBindingRepo) Delete(ctx context.Context, id string) error {
	return mustAffect(r.s.exec(ctx, `DELETE FROM tool_bindings WHERE id = $1`, id))
}

func (r toolBindingRepo) bindings(ctx context.Context, query string, args ...any) ([]ToolBinding, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ToolBinding
	for rows.Next() {
		b, err := scanToolBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

type credentialRepo struct{ s *sqlStore }

const credentialCols = "id, app_id, scope, owner_id, kind, enc_payload, oauth_meta, rotated_at, created_at" // #nosec G101 -- column list, not a credential

func scanCredential(row scanner) (Credential, error) {
	var c Credential
	var ra scanTimePtr
	var ca scanTime
	if err := row.Scan(&c.ID, &c.AppID, &c.Scope, &c.OwnerID, &c.Kind,
		&c.EncPayload, &c.OAuthMeta, &ra, &ca); err != nil {
		return Credential{}, scanErr(err)
	}
	c.RotatedAt, c.CreatedAt = ra.t, ca.t
	return c, nil
}

func (r credentialRepo) Create(ctx context.Context, c Credential) (Credential, error) {
	c.ID = newID()
	c.CreatedAt = now()
	if c.OAuthMeta == "" {
		c.OAuthMeta = "{}"
	}
	_, err := r.s.exec(ctx, `INSERT INTO credentials (id, app_id, scope, owner_id, kind, enc_payload, oauth_meta, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		c.ID, c.AppID, c.Scope, c.OwnerID, c.Kind, c.EncPayload, c.OAuthMeta, r.s.tArg(c.CreatedAt))
	if err != nil {
		return Credential{}, err
	}
	return c, nil
}

func (r credentialRepo) GetByID(ctx context.Context, id string) (Credential, error) {
	return scanCredential(r.s.queryRow(ctx,
		`SELECT `+credentialCols+` FROM credentials WHERE id = $1`, id))
}

func (r credentialRepo) ListByApp(ctx context.Context, appID string) ([]Credential, error) {
	rows, err := r.s.query(ctx,
		`SELECT `+credentialCols+` FROM credentials WHERE app_id = $1 ORDER BY created_at`, appID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r credentialRepo) ListByOwner(ctx context.Context, scope, ownerID string) ([]Credential, error) {
	rows, err := r.s.query(ctx,
		`SELECT `+credentialCols+` FROM credentials WHERE scope = $1 AND owner_id = $2 ORDER BY created_at`,
		scope, ownerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r credentialRepo) Update(ctx context.Context, c Credential) (Credential, error) {
	err := mustAffect(r.s.exec(ctx, `UPDATE credentials SET enc_payload = $1, oauth_meta = $2, rotated_at = $3
		WHERE id = $4`, c.EncPayload, c.OAuthMeta, r.s.tArg(now()), c.ID))
	if err != nil {
		return Credential{}, err
	}
	return r.GetByID(ctx, c.ID)
}

func (r credentialRepo) Delete(ctx context.Context, id string) error {
	return mustAffect(r.s.exec(ctx, `DELETE FROM credentials WHERE id = $1`, id))
}
