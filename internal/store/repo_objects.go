package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type objectListRepo struct{ s *sqlStore }

// ObjectLists answers the object list repo over the same sqlStore.
func (s *sqlStore) ObjectLists() ObjectListRepo { return objectListRepo{s} }

const objectListCols = "app_id, type, decl_hash, version, list_hash, object_count, joined_count, left_count, " +
	"outcome, reason, listed_at, attempted_at, due_at, claim_owner, claim_until"

// objectBatch is how many objects one INSERT carries: six parameters each,
// far below the parameter limit of either driver.
const objectBatch = 500

func scanObjectList(row scanner) (ObjectList, error) {
	var o ObjectList
	var listed, attempted, until scanTimePtr
	var due scanTime
	if err := row.Scan(&o.AppID, &o.Type, &o.DeclHash, &o.Version, &o.ListHash, &o.Count, &o.Joined, &o.Left,
		&o.Outcome, &o.Reason, &listed, &attempted, &due, &o.ClaimOwner, &until); err != nil {
		return ObjectList{}, scanErr(err)
	}
	o.ListedAt, o.AttemptedAt, o.DueAt, o.ClaimUntil = listed.t, attempted.t, due.t, until.t
	return o, nil
}

func (r objectListRepo) List(ctx context.Context) ([]ObjectList, error) {
	rows, err := r.s.query(ctx, `SELECT `+objectListCols+` FROM object_lists ORDER BY app_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ObjectList
	for rows.Next() {
		o, err := scanObjectList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r objectListRepo) Get(ctx context.Context, appID string) (ObjectList, error) {
	return scanObjectList(r.s.queryRow(ctx, `SELECT `+objectListCols+` FROM object_lists WHERE app_id = $1`, appID))
}

func (r objectListRepo) Ensure(ctx context.Context, appID, typ, declHash string, due time.Time) error {
	_, err := r.s.exec(ctx, `INSERT INTO object_lists (app_id, type, decl_hash, due_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (app_id) DO NOTHING`, appID, typ, declHash, r.s.tArg(due))
	return err
}

func (r objectListRepo) Reset(ctx context.Context, appID, typ, declHash string, due time.Time) error {
	return r.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, r.s.q(`UPDATE object_lists SET type = $1, decl_hash = $2, list_hash = '',
			object_count = 0, joined_count = 0, left_count = 0, outcome = 'never', reason = '', listed_at = NULL,
			attempted_at = NULL, due_at = $3, claim_owner = '', claim_until = NULL
			WHERE app_id = $4 AND decl_hash <> $5`), typ, declHash, r.s.tArg(due), appID, declHash)
		if err := mustAffect(res, r.s.mapErr(err)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, r.s.q(`DELETE FROM objects WHERE app_id = $1`), appID)
		return r.s.mapErr(err)
	})
}

func (r objectListRepo) Delete(ctx context.Context, appID string) error {
	_, err := r.s.exec(ctx, `DELETE FROM object_lists WHERE app_id = $1`, appID)
	return err
}

func (r objectListRepo) Claim(ctx context.Context, appID, owner string, now, until time.Time) (ObjectList, bool, error) {
	res, err := r.s.exec(ctx, `UPDATE object_lists SET claim_owner = $1, claim_until = $2
		WHERE app_id = $3 AND NOT (`+r.s.timeAfter("due_at", "$4")+`)
		AND (claim_owner = '' OR claim_until IS NULL OR `+r.s.timeAfter("$5", "claim_until")+`)`,
		owner, r.s.tArg(until), appID, r.s.tArg(now), r.s.tArg(now))
	if err != nil {
		return ObjectList{}, false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ObjectList{}, false, err
	}
	row, err := r.Get(ctx, appID)
	return row, err == nil, err
}

func (r objectListRepo) Replace(ctx context.Context, appID, owner, declHash, listHash string, objs []ListedObject, l Listing) (ObjectList, error) {
	var out ObjectList
	err := r.inTx(ctx, func(tx *sql.Tx) error {
		// The fenced update comes first, so the row is held from here to
		// the commit and a lost claim writes nothing.
		res, err := tx.ExecContext(ctx, r.s.q(`UPDATE object_lists SET version = version + 1, list_hash = $1,
			object_count = $2, outcome = 'ok', reason = '', listed_at = $3, attempted_at = $4, due_at = $5,
			claim_owner = '', claim_until = NULL WHERE app_id = $6 AND claim_owner = $7 AND decl_hash = $8`),
			listHash, len(objs), r.s.tArg(l.At), r.s.tArg(l.At), r.s.tArg(l.Due), appID, owner, declHash)
		if err := mustAffect(res, r.s.mapErr(err)); err != nil {
			return err
		}
		var version int64
		if err := tx.QueryRowContext(ctx, r.s.q(`SELECT version FROM object_lists WHERE app_id = $1`), appID).Scan(&version); err != nil {
			return scanErr(err)
		}
		prev, err := r.storedObjects(ctx, tx, appID)
		if err != nil {
			return err
		}
		rows := make([]ListedObject, len(objs))
		joined := 0
		var renamed []string
		for i, o := range objs {
			o.AddedVersion = version
			if p, ok := prev[o.ID]; ok {
				o.AddedVersion = p.AddedVersion
				if p.Value != o.Value {
					renamed = append(renamed, o.ID)
				}
				delete(prev, o.ID)
			} else {
				joined++
			}
			rows[i] = o
		}
		if _, err := tx.ExecContext(ctx, r.s.q(`UPDATE object_lists SET joined_count = $1, left_count = $2 WHERE app_id = $3`),
			joined, len(prev), appID); err != nil {
			return r.s.mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, r.s.q(`DELETE FROM objects WHERE app_id = $1`), appID); err != nil {
			return r.s.mapErr(err)
		}
		if err := r.insertObjects(ctx, tx, appID, rows); err != nil {
			return err
		}
		if out, err = scanObjectList(tx.QueryRowContext(ctx, r.s.q(`SELECT `+objectListCols+` FROM object_lists WHERE app_id = $1`), appID)); err != nil {
			return err
		}
		sort.Strings(renamed)
		out.Renamed = renamed
		return nil
	})
	return out, err
}

// storedObjects answers the value of each stored object of appID and the
// version it joined at, by id.
func (r objectListRepo) storedObjects(ctx context.Context, tx *sql.Tx, appID string) (map[string]ListedObject, error) {
	rows, err := tx.QueryContext(ctx, r.s.q(`SELECT id, value, added_version FROM objects WHERE app_id = $1`), appID)
	if err != nil {
		return nil, r.s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]ListedObject{}
	for rows.Next() {
		var o ListedObject
		if err := rows.Scan(&o.ID, &o.Value, &o.AddedVersion); err != nil {
			return nil, err
		}
		out[o.ID] = o
	}
	return out, rows.Err()
}

func (r objectListRepo) insertObjects(ctx context.Context, tx *sql.Tx, appID string, objs []ListedObject) error {
	for start := 0; start < len(objs); start += objectBatch {
		batch := objs[start:min(start+objectBatch, len(objs))]
		var b strings.Builder
		b.WriteString(`INSERT INTO objects (app_id, id, value, title, attributes, added_version) VALUES `)
		args := make([]any, 0, 6*len(batch))
		for i, o := range batch {
			if i > 0 {
				b.WriteString(", ")
			}
			n := 6 * i
			fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d)", n+1, n+2, n+3, n+4, n+5, n+6)
			attrs := o.Attributes
			if attrs == "" {
				attrs = "{}"
			}
			args = append(args, appID, o.ID, o.Value, o.Title, attrs, o.AddedVersion)
		}
		if _, err := tx.ExecContext(ctx, r.s.q(b.String()), args...); err != nil {
			return r.s.mapErr(err)
		}
	}
	return nil
}

func (r objectListRepo) Settle(ctx context.Context, appID, owner, declHash string, l Listing) (ObjectList, error) {
	set, args := `outcome = $1, reason = $2, attempted_at = $3, due_at = $4`, []any{l.Outcome, l.Reason, r.s.tArg(l.At), r.s.tArg(l.Due)}
	if l.Outcome == ObjectsOK {
		set, args = set+`, listed_at = $5`, append(args, r.s.tArg(l.At))
	}
	n := len(args)
	err := mustAffect(r.s.exec(ctx, fmt.Sprintf(`UPDATE object_lists SET %s, claim_owner = '', claim_until = NULL
		WHERE app_id = $%d AND claim_owner = $%d AND decl_hash = $%d`, set, n+1, n+2, n+3),
		append(args, appID, owner, declHash)...))
	if err != nil {
		return ObjectList{}, err
	}
	return r.Get(ctx, appID)
}

func (r objectListRepo) MarkDue(ctx context.Context, appID string, due time.Time) error {
	return mustAffect(r.s.exec(ctx, `UPDATE object_lists SET due_at = $1 WHERE app_id = $2`, r.s.tArg(due), appID))
}

func (r objectListRepo) MarkDueIfIdle(ctx context.Context, appID string, due, attemptedBefore time.Time) (bool, error) {
	err := mustAffect(r.s.exec(ctx, `UPDATE object_lists SET due_at = $1 WHERE app_id = $2
		AND (attempted_at IS NULL OR `+r.s.timeAfter("$3", "attempted_at")+`)`,
		r.s.tArg(due), appID, r.s.tArg(attemptedBefore)))
	if errors.Is(err, ErrNotFound) {
		if _, gerr := r.Get(ctx, appID); gerr != nil {
			return false, gerr
		}
		return false, nil
	}
	return err == nil, err
}

func (r objectListRepo) Objects(ctx context.Context, appID string) ([]ListedObject, error) {
	rows, err := r.s.query(ctx, `SELECT id, value, title, attributes, added_version FROM objects WHERE app_id = $1`, appID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ListedObject
	for rows.Next() {
		var o ListedObject
		if err := rows.Scan(&o.ID, &o.Value, &o.Title, &o.Attributes, &o.AddedVersion); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	// Sorted here by bytes, because a Postgres collation may order text
	// by locale.
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out, rows.Err()
}

func (r objectListRepo) ObjectsByValues(ctx context.Context, appID string, values []string) (map[string]string, error) {
	return r.lookup(ctx, appID, "value", "id", values)
}

func (r objectListRepo) ObjectsByIDs(ctx context.Context, appID string, ids []string) (map[string]string, error) {
	return r.lookup(ctx, appID, "id", "value", ids)
}

// lookup answers the column want of the objects of appID whose column by
// is one of keys, by key, in batches. A row counts only when its key
// equals a requested key byte for byte, whatever the database's collation
// calls equal. A key with a NUL byte or bytes that are not UTF-8 is left
// out before the query, because no list holds one and Postgres refuses
// such text, so both drivers answer it as absent.
func (r objectListRepo) lookup(ctx context.Context, appID, by, want string, keys []string) (map[string]string, error) {
	asked := make(map[string]bool, len(keys))
	for _, k := range keys {
		if utf8.ValidString(k) && !strings.ContainsRune(k, 0) {
			asked[k] = true
		}
	}
	uniq := make([]string, 0, len(asked))
	for k := range asked {
		uniq = append(uniq, k)
	}
	out := make(map[string]string, len(uniq))
	for start := 0; start < len(uniq); start += objectBatch {
		batch := uniq[start:min(start+objectBatch, len(uniq))]
		marks := make([]string, len(batch))
		args := make([]any, 0, len(batch)+1)
		args = append(args, appID)
		for i, k := range batch {
			marks[i] = fmt.Sprintf("$%d", i+2)
			args = append(args, k)
		}
		rows, err := r.s.query(ctx, `SELECT `+by+`, `+want+` FROM objects WHERE app_id = $1 AND `+by+
			` IN (`+strings.Join(marks, ", ")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if asked[k] {
				out[k] = v
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// inTx runs fn in one transaction and commits when it answers nil.
func (r objectListRepo) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
