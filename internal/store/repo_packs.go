package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type packRepo struct{ s *sqlStore }

const packCols = "id, name, version, content, checksum, created_at, updated_at"

func scanPack(row scanner) (KnowledgePack, error) {
	var p KnowledgePack
	var ca, ua scanTime
	if err := row.Scan(&p.ID, &p.Name, &p.Version, &p.Content, &p.Checksum, &ca, &ua); err != nil {
		return KnowledgePack{}, scanErr(err)
	}
	p.CreatedAt, p.UpdatedAt = ca.t, ua.t
	return p, nil
}

func (r packRepo) Create(ctx context.Context, p KnowledgePack) (KnowledgePack, error) {
	p.ID = newID()
	p.CreatedAt = now()
	p.UpdatedAt = p.CreatedAt
	_, err := r.s.exec(ctx, `INSERT INTO knowledge_packs (id, name, version, content, checksum, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		p.ID, p.Name, p.Version, p.Content, p.Checksum, r.s.tArg(p.CreatedAt), r.s.tArg(p.UpdatedAt))
	if err != nil {
		return KnowledgePack{}, err
	}
	return p, nil
}

func (r packRepo) GetByID(ctx context.Context, id string) (KnowledgePack, error) {
	return scanPack(r.s.queryRow(ctx, `SELECT `+packCols+` FROM knowledge_packs WHERE id = $1`, id))
}

func (r packRepo) GetByName(ctx context.Context, name string) (KnowledgePack, error) {
	return scanPack(r.s.queryRow(ctx, `SELECT `+packCols+` FROM knowledge_packs WHERE name = $1`, name))
}

func (r packRepo) List(ctx context.Context) ([]KnowledgePack, error) {
	return r.packs(ctx, `SELECT `+packCols+` FROM knowledge_packs ORDER BY name`)
}

func (r packRepo) Update(ctx context.Context, p KnowledgePack) (KnowledgePack, error) {
	p.UpdatedAt = now()
	err := mustAffect(r.s.exec(ctx, `UPDATE knowledge_packs SET name = $1, version = $2, content = $3, checksum = $4, updated_at = $5
		WHERE id = $6`, p.Name, p.Version, p.Content, p.Checksum, r.s.tArg(p.UpdatedAt), p.ID))
	if err != nil {
		return KnowledgePack{}, err
	}
	return r.GetByID(ctx, p.ID)
}

// Delete removes a pack that no role is bound to. A bound pack, like a
// missing id, affects no row and answers ErrNotFound, and the caller reads
// the bindings to tell the two apart.
func (r packRepo) Delete(ctx context.Context, id string) error {
	return r.s.tx(ctx, func(tx *sql.Tx) error {
		if r.s.d == dialectPostgres {
			// A bind in flight holds a key share on the pack row, and a
			// conditional delete that waits on that lock is not checked
			// again once the lock is granted, so it would cascade the
			// binding that just committed. Locking the row first makes
			// the delete read a fresh snapshot that carries the binding.
			// SQLite serializes writers, so the statement alone holds there.
			var locked string
			if err := tx.QueryRowContext(ctx, r.s.q(`SELECT id FROM knowledge_packs WHERE id = $1 FOR UPDATE`), id).Scan(&locked); err != nil {
				return scanErr(err)
			}
		}
		return mustAffect(tx.ExecContext(ctx, r.s.q(`DELETE FROM knowledge_packs WHERE id = $1
			AND NOT EXISTS (SELECT 1 FROM pack_bindings WHERE pack_id = $2)`), id, id))
	})
}

func (r packRepo) Bind(ctx context.Context, roleID, packID string) error {
	_, err := r.s.exec(ctx, `INSERT INTO pack_bindings (role_id, pack_id) VALUES ($1, $2)`, roleID, packID)
	return err
}

func (r packRepo) Unbind(ctx context.Context, roleID, packID string) error {
	return mustAffect(r.s.exec(ctx,
		`DELETE FROM pack_bindings WHERE role_id = $1 AND pack_id = $2`, roleID, packID))
}

// ListBindings joins role names in one query for the whole packs admin list;
// packs and roles scale with the org's catalog, not with agent count.
func (r packRepo) ListBindings(ctx context.Context) ([]PackBinding, error) {
	rows, err := r.s.query(ctx, `SELECT b.pack_id, b.role_id, r.name
		FROM pack_bindings b JOIN roles r ON r.id = b.role_id
		ORDER BY b.pack_id, r.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PackBinding
	for rows.Next() {
		var b PackBinding
		if err := rows.Scan(&b.PackID, &b.RoleID, &b.RoleName); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r packRepo) ForRoles(ctx context.Context, roleIDs []string) ([]KnowledgePack, error) {
	if len(roleIDs) == 0 {
		return nil, nil
	}
	ph := make([]string, len(roleIDs))
	args := make([]any, len(roleIDs))
	for i, id := range roleIDs {
		ph[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	query := `SELECT DISTINCT p.id, p.name, p.version, p.content, p.checksum, p.created_at, p.updated_at
		FROM knowledge_packs p JOIN pack_bindings b ON b.pack_id = p.id
		WHERE b.role_id IN (` + strings.Join(ph, ", ") + `) ORDER BY p.name`
	return r.packs(ctx, query, args...)
}

func (r packRepo) packs(ctx context.Context, query string, args ...any) ([]KnowledgePack, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []KnowledgePack
	for rows.Next() {
		p, err := scanPack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type signingKeyRepo struct{ s *sqlStore }

const signingKeyCols = "kid, purpose, status, private_key, public_key, created_at, rotated_at"

func scanSigningKey(row scanner) (SigningKey, error) {
	var k SigningKey
	var ca scanTime
	var ra scanTimePtr
	if err := row.Scan(&k.KID, &k.Purpose, &k.Status, &k.PrivateKey, &k.PublicKey, &ca, &ra); err != nil {
		return SigningKey{}, scanErr(err)
	}
	k.CreatedAt, k.RotatedAt = ca.t, ra.t
	return k, nil
}

func (r signingKeyRepo) Create(ctx context.Context, k SigningKey) (SigningKey, error) {
	if k.KID == "" {
		k.KID = newID()
	}
	k.CreatedAt = now()
	if k.Status == "" {
		k.Status = KeyActive
	}
	_, err := r.s.exec(ctx, `INSERT INTO signing_keys (kid, purpose, status, private_key, public_key, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		k.KID, k.Purpose, k.Status, k.PrivateKey, k.PublicKey, r.s.tArg(k.CreatedAt))
	if err != nil {
		return SigningKey{}, err
	}
	return k, nil
}

func (r signingKeyRepo) Get(ctx context.Context, kid string) (SigningKey, error) {
	return scanSigningKey(r.s.queryRow(ctx,
		`SELECT `+signingKeyCols+` FROM signing_keys WHERE kid = $1`, kid))
}

func (r signingKeyRepo) ListByPurpose(ctx context.Context, purpose string) ([]SigningKey, error) {
	rows, err := r.s.query(ctx, `SELECT `+signingKeyCols+` FROM signing_keys
		WHERE purpose = $1 ORDER BY created_at DESC`, purpose)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SigningKey
	for rows.Next() {
		k, err := scanSigningKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r signingKeyRepo) SetStatus(ctx context.Context, kid, status string) error {
	return mustAffect(r.s.exec(ctx, `UPDATE signing_keys SET status = $1, rotated_at = $2 WHERE kid = $3`,
		status, r.s.tArg(now()), kid))
}

// errNotStaged rolls Promote's transaction back when the key it was asked to
// promote is no longer staged, so the demotion it began is undone.
var errNotStaged = errors.New("store: signing key is not staged")

func (r signingKeyRepo) Promote(ctx context.Context, kid string) (bool, error) {
	at := r.s.tArg(now())
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		// The demotion names its purpose through the staged row, so a kid
		// that is not staged demotes nothing. It runs first because the
		// database holds at most one active client assertion key.
		if _, err := tx.ExecContext(ctx, r.s.q(`UPDATE signing_keys SET status = 'retiring', rotated_at = $1
			WHERE status = 'active' AND purpose = (SELECT purpose FROM signing_keys WHERE kid = $2 AND status = 'staged')`),
			at, kid); err != nil {
			return r.s.mapErr(err)
		}
		res, err := tx.ExecContext(ctx, r.s.q(`UPDATE signing_keys SET status = 'active', rotated_at = $1
			WHERE kid = $2 AND status = 'staged'`), at, kid)
		if err != nil {
			return r.s.mapErr(err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return errors.Join(errNotStaged, err)
		}
		return nil
	})
	if errors.Is(err, errNotStaged) {
		return false, nil
	}
	return err == nil, err
}

func (r signingKeyRepo) Retire(ctx context.Context, kid, from string) (bool, error) {
	res, err := r.s.exec(ctx, `UPDATE signing_keys SET status = 'retired', rotated_at = $1
		WHERE kid = $2 AND status = $3`, r.s.tArg(now()), kid, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

type settingsRepo struct{ s *sqlStore }

func (r settingsRepo) Set(ctx context.Context, key, value string) error {
	_, err := r.s.exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, key, value)
	return err
}

func (r settingsRepo) SetIfAbsent(ctx context.Context, key, value string) error {
	_, err := r.s.exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO NOTHING`, key, value)
	return err
}

func (r settingsRepo) Update(ctx context.Context, key, value string) error {
	return mustAffect(r.s.exec(ctx, `UPDATE settings SET value = $1, updated_at = CURRENT_TIMESTAMP WHERE key = $2`, value, key))
}

func (r settingsRepo) Get(ctx context.Context, key string) (string, error) {
	var v string
	err := r.s.queryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&v)
	if err != nil {
		return "", scanErr(err)
	}
	return v, nil
}

func (r settingsRepo) Delete(ctx context.Context, key string) error {
	return mustAffect(r.s.exec(ctx, `DELETE FROM settings WHERE key = $1`, key))
}

func (r settingsRepo) List(ctx context.Context) (map[string]string, error) {
	rows, err := r.s.query(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
