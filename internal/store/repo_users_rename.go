package store

import (
	"context"
	"database/sql"
	"errors"
)

// UpdateRenaming writes every column of the live user u as Update does
// and, when the write changes the username, points every live user whose
// sponsor is the old username at the new one, all in one transaction. A
// write that fails stores nothing and moves no sponsor. It answers the row
// read back and the ids of the users whose sponsor it changed.
func (r userRepo) UpdateRenaming(ctx context.Context, u User) (User, []string, error) {
	query, args := r.updateStmt(u)
	var repointed []string
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		// The row is locked on Postgres, so two renames of one user run one
		// after the other and the second moves the sponsors from the name
		// the first one wrote. SQLite has one writer at a time.
		read := `SELECT username FROM users WHERE id = $1 AND deleted_at IS NULL`
		if r.s.d == dialectPostgres {
			read += ` FOR UPDATE`
		}
		var old string
		if err := tx.QueryRowContext(ctx, r.s.q(read), u.ID).Scan(&old); err != nil {
			return scanErr(err)
		}
		res, err := tx.ExecContext(ctx, r.s.q(query), args...)
		if err := mustAffect(res, r.s.mapErr(err)); err != nil {
			return err
		}
		if old == u.Username {
			return nil
		}
		repointed, err = r.repoint(ctx, tx, old, u.Username)
		return err
	})
	if err != nil {
		return User{}, nil, err
	}
	updated, err := r.GetByID(ctx, u.ID)
	return updated, repointed, err
}

// repoint points every live user whose sponsor is the username old at the
// username to, inside tx, and answers their ids.
func (r userRepo) repoint(ctx context.Context, tx *sql.Tx, old, to string) ([]string, error) {
	var repointed []string
	rows, err := tx.QueryContext(ctx, r.s.q(`SELECT id FROM users WHERE sponsor = $1 AND deleted_at IS NULL ORDER BY id`), old)
	if err != nil {
		return nil, r.s.mapErr(err)
	}
	for rows.Next() {
		var agent string
		if err := rows.Scan(&agent); err != nil {
			_ = rows.Close()
			return nil, err
		}
		repointed = append(repointed, agent)
	}
	// The rows are closed before the next statement, because a Postgres
	// connection runs one statement at a time.
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, r.s.q(`UPDATE users SET sponsor = $1, updated_at = $2 WHERE sponsor = $3 AND deleted_at IS NULL`), to, r.s.tArg(now()), old)
	return repointed, r.s.mapErr(err)
}

// Rename gives the live user id the username username and points every
// live user whose sponsor is the old username at the new one, in one
// transaction. It answers the row read back and the ids of those users.
func (r userRepo) Rename(ctx context.Context, id, username string) (User, []string, error) {
	t := r.s.tArg(now())
	var repointed []string
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, r.s.q(`SELECT username FROM users WHERE id = $1 AND deleted_at IS NULL`), id).Scan(&old); err != nil {
			return scanErr(err)
		}
		if _, err := tx.ExecContext(ctx, r.s.q(`UPDATE users SET username = $1, updated_at = $2 WHERE id = $3`), username, t, id); err != nil {
			return r.s.mapErr(err)
		}
		var err error
		repointed, err = r.repoint(ctx, tx, old, username)
		return err
	})
	if err != nil {
		return User{}, nil, err
	}
	u, err := r.GetByID(ctx, id)
	return u, repointed, err
}
