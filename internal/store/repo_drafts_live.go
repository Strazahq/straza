package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// LiveState reads the generation first, because a repeatable read on
// Postgres takes its snapshot at the first statement, and every later part
// then belongs to the generation it answers.
func (r draftRepo) LiveState(ctx context.Context, liveSnapshot string) (LiveState, error) {
	st := LiveState{ReadAt: time.Now(), RoleIDs: map[string]string{}, Implies: map[string][]string{}}
	err := r.read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, r.s.q(`SELECT generation FROM config_generation WHERE id = 1`)).Scan(&st.Generation); err != nil {
			return scanErr(err)
		}
		snap, err := scanSnapshot(tx.QueryRowContext(ctx, r.s.q(`SELECT id, signer_key_id, size,
			CASE WHEN id = $1 THEN NULL ELSE blob END, active, created_at FROM snapshots WHERE active = TRUE`), liveSnapshot))
		switch {
		case err == nil:
			st.Snapshot = snap
		case !errors.Is(err, ErrNotFound):
			return err
		}
		if st.Apps, err = r.liveApps(ctx, tx); err != nil {
			return err
		}
		if st.Access, err = r.liveAccess(ctx, tx); err != nil {
			return err
		}
		if st.Roles, err = r.liveRoles(ctx, tx, st.RoleIDs); err != nil {
			return err
		}
		return r.liveImplies(ctx, tx, st.Implies)
	})
	if err != nil {
		return LiveState{}, err
	}
	return st, nil
}

func (r draftRepo) liveApps(ctx context.Context, tx *sql.Tx) ([]App, error) {
	rows, err := tx.QueryContext(ctx, r.s.q(`SELECT `+appCols+` FROM apps WHERE deleted_at IS NULL ORDER BY name`))
	if err != nil {
		return nil, r.s.mapErr(err)
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

// liveAccess reads the access rows of live servers with their names. A row
// whose tool matchers are empty or do not decode is left out, as the
// gateway's own table leaves it out, so it grants nothing.
func (r draftRepo) liveAccess(ctx context.Context, tx *sql.Tx) ([]LiveAccess, error) {
	rows, err := tx.QueryContext(ctx, r.s.q(`SELECT b.id, ro.name, a.name, b.tool_matcher FROM tool_bindings b
		JOIN roles ro ON ro.id = b.role_id
		JOIN apps a ON a.id = b.app_id AND a.deleted_at IS NULL
		ORDER BY b.created_at, b.id`))
	if err != nil {
		return nil, r.s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []LiveAccess
	for rows.Next() {
		var la LiveAccess
		var matcher string
		if err := rows.Scan(&la.ID, &la.Role, &la.App, &matcher); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(matcher), &la.Matchers) != nil || len(la.Matchers) == 0 {
			continue
		}
		out = append(out, la)
	}
	return out, rows.Err()
}

// liveRoles answers every role name, sorted, and fills ids with each
// role's id by its name.
func (r draftRepo) liveRoles(ctx context.Context, tx *sql.Tx, ids map[string]string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, r.s.q(`SELECT id, name FROM roles ORDER BY name`))
	if err != nil {
		return nil, r.s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out = append(out, name)
		ids[name] = id
	}
	return out, rows.Err()
}

// liveImplies fills edges with the names of the roles each role implies
// directly, both sorted by name.
func (r draftRepo) liveImplies(ctx context.Context, tx *sql.Tx, edges map[string][]string) error {
	rows, err := tx.QueryContext(ctx, r.s.q(`SELECT ro.name, im.name FROM role_implications ri
		JOIN roles ro ON ro.id = ri.role_id
		JOIN roles im ON im.id = ri.implies_role_id
		ORDER BY ro.name, im.name`))
	if err != nil {
		return r.s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var role, implied string
		if err := rows.Scan(&role, &implied); err != nil {
			return err
		}
		edges[role] = append(edges[role], implied)
	}
	return rows.Err()
}
