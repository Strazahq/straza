package store

import (
	"context"
	"database/sql"
	"errors"
)

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

func (r signingKeyRepo) ReplacePrivateKey(ctx context.Context, kid string, from, to []byte) (bool, error) {
	res, err := r.s.exec(ctx, `UPDATE signing_keys SET private_key = $1 WHERE kid = $2 AND private_key = $3`, to, kid, from)
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
