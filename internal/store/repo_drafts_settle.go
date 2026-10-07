package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// errSettleMoved rolls a settle back when what the conversion read moved
// before the settle took the publish lock.
var errSettleMoved = errors.New("store: the policy set moved since the conversion read it")

func (r draftRepo) SettlePolicy(ctx context.Context, s PolicySettle) (bool, error) {
	if s.Was != nil && s.Was.Status == s.Status && s.Was.YAMLSource == s.Text && s.Was.Priority == s.Priority {
		return false, nil
	}
	err := r.inTx(ctx, func(tx *sql.Tx) error {
		if err := r.publishLock(ctx, tx); err != nil {
			return err
		}
		// A publish that landed after the conversion's read moved the
		// generation, and the text it published would read as a saved edit.
		var generation int64
		if err := tx.QueryRowContext(ctx, r.s.q(`SELECT generation FROM config_generation WHERE id = 1`)).Scan(&generation); err != nil {
			return scanErr(err)
		}
		if generation != s.Generation {
			return errSettleMoved
		}
		// A replica of the release before drafts activates a snapshot
		// without moving the generation or the row, and the text it
		// published would read as a saved edit too.
		var active string
		err := tx.QueryRowContext(ctx, r.s.q(`SELECT id FROM snapshots WHERE active = TRUE`)).Scan(&active)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r.s.mapErr(err)
		}
		if active != s.Snapshot {
			return errSettleMoved
		}
		if err := r.settleDraft(ctx, tx, s); err != nil {
			return err
		}
		return r.settleRow(ctx, tx, s)
	})
	if errors.Is(err, errSettleMoved) {
		return false, nil
	}
	return err == nil, err
}

// settleDraft creates the saved-edit draft of s, or writes Item as the next
// revision of the open slot draft under the base rule of DraftRevise, so
// the item keeps the base it was stamped with.
func (r draftRepo) settleDraft(ctx context.Context, tx *sql.Tx, s PolicySettle) error {
	if s.Draft != nil {
		_, err := r.create(ctx, tx, *s.Draft, []DraftItemRow{s.Item}, s.Rev)
		if errors.Is(err, ErrConflict) {
			return errSettleMoved
		}
		return err
	}
	if s.Slot == 0 {
		return nil
	}
	at, next := now(), s.SlotRevision+1
	res, err := tx.ExecContext(ctx, r.s.q(`UPDATE drafts SET revision = $1, updated_at = $2
		WHERE id = $3 AND state = 'open' AND revision = $4`), next, r.s.tArg(at), s.Slot, s.SlotRevision)
	if err != nil {
		return r.s.mapErr(err)
	}
	if err := movedUnless(res); err != nil {
		return err
	}
	items, err := r.keepBases(ctx, tx, s.Slot, DraftRevise{From: s.SlotRevision, Items: []DraftItemRow{s.Item}})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, r.s.q(`DELETE FROM draft_items WHERE draft_id = $1`), s.Slot); err != nil {
		return r.s.mapErr(err)
	}
	if err := r.insertItems(ctx, tx, s.Slot, items); err != nil {
		return err
	}
	rev := s.Rev
	rev.Revision, rev.CreatedAt = next, at
	return r.insertRevision(ctx, tx, s.Slot, rev)
}

// settleRow writes the row of s: an insert for a set with no row, or an
// update that lands only while the row still holds what the conversion
// read. Either way the row takes the priority of the published text, so a
// saved edit's priority never stays in it, and compiled_hash is the sha256
// of the text.
func (r draftRepo) settleRow(ctx context.Context, tx *sql.Tx, s PolicySettle) error {
	at := r.s.tArg(now())
	if s.Was == nil {
		_, err := tx.ExecContext(ctx, r.s.q(`INSERT INTO policy_sets (id, name, priority, yaml_source, compiled_hash, status,
			created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`),
			newID(), s.Name, s.Priority, s.Text, sha256Hex(s.Text), s.Status, at, at)
		if err = r.s.mapErr(err); errors.Is(err, ErrConflict) {
			return errSettleMoved
		}
		return err
	}
	res, err := tx.ExecContext(ctx, r.s.q(`UPDATE policy_sets SET status = $1, yaml_source = $2, priority = $3,
		compiled_hash = $4, updated_at = $5 WHERE id = $6 AND status = $7 AND yaml_source = $8`),
		s.Status, s.Text, s.Priority, sha256Hex(s.Text), at, s.Was.ID, s.Was.Status, s.Was.YAMLSource)
	if err != nil {
		return r.s.mapErr(err)
	}
	return movedUnless(res)
}

func (r draftRepo) PolicyMark(ctx context.Context) (PolicyMark, error) {
	return r.policyMark(ctx, r.s.db)
}

// rowsQuerier is what the mark needs from the pool or an open transaction.
type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// policyMark reads the mark on db in one statement: the generation beside
// every policy_sets row's id, status, updated_at and text length, in id
// order. Its digest is the sha256 of those rows, so it moves with any
// write to a row whatever the clock of the replica that made it said.
func (r draftRepo) policyMark(ctx context.Context, db rowsQuerier) (PolicyMark, error) {
	rows, err := db.QueryContext(ctx, r.s.q(`SELECT g.generation, p.id, p.status, p.updated_at, length(p.yaml_source)
		FROM config_generation g LEFT JOIN policy_sets p ON 1 = 1 WHERE g.id = 1 ORDER BY p.id`))
	if err != nil {
		return PolicyMark{}, r.s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var m PolicyMark
	var sets strings.Builder
	read := false
	for rows.Next() {
		var id, status sql.NullString
		var at scanTimePtr
		var size sql.NullInt64
		if err := rows.Scan(&m.Generation, &id, &status, &at, &size); err != nil {
			return PolicyMark{}, err
		}
		read = true
		if id.Valid && at.t != nil {
			fmt.Fprintf(&sets, "%s\t%s\t%s\t%d\n", id.String, status.String, at.t.UTC().Format(time.RFC3339Nano), size.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return PolicyMark{}, err
	}
	if !read {
		return PolicyMark{}, ErrNotFound
	}
	m.Digest = sha256Hex(sets.String())
	return m, nil
}

// PolicyState reads the mark first, because a repeatable read on Postgres
// takes its snapshot at the first statement, and the snapshot and the rows
// then belong to the generation it answers.
func (r draftRepo) PolicyState(ctx context.Context) (PolicyState, error) {
	var st PolicyState
	err := r.read(ctx, func(tx *sql.Tx) error {
		var err error
		if st.Mark, err = r.policyMark(ctx, tx); err != nil {
			return err
		}
		st.Snapshot, err = scanSnapshot(tx.QueryRowContext(ctx, r.s.q(`SELECT `+snapshotCols+` FROM snapshots WHERE active = TRUE`)))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		rows, err := tx.QueryContext(ctx, r.s.q(`SELECT `+policyCols+` FROM policy_sets ORDER BY name`))
		if err != nil {
			return r.s.mapErr(err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			ps, err := scanPolicy(rows)
			if err != nil {
				return err
			}
			st.Rows = append(st.Rows, ps)
		}
		return rows.Err()
	})
	if err != nil {
		return PolicyState{}, err
	}
	return st, nil
}

// movedUnless answers errSettleMoved when the compare-and-set write res
// changed no row, and the error of counting its rows when that fails.
func movedUnless(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errSettleMoved
	}
	return nil
}
