package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type policyRepo struct{ s *sqlStore }

const policyCols = "id, name, priority, yaml_source, compiled_hash, status, created_at, updated_at"

func scanPolicy(row scanner) (PolicySet, error) {
	var p PolicySet
	var ca, ua scanTime
	if err := row.Scan(&p.ID, &p.Name, &p.Priority, &p.YAMLSource, &p.CompiledHash, &p.Status, &ca, &ua); err != nil {
		return PolicySet{}, scanErr(err)
	}
	p.CreatedAt, p.UpdatedAt = ca.t, ua.t
	return p, nil
}

func (r policyRepo) Create(ctx context.Context, p PolicySet) (PolicySet, error) {
	p.ID = newID()
	p.CreatedAt = now()
	p.UpdatedAt = p.CreatedAt
	if p.Status == "" {
		p.Status = "draft"
	}
	_, err := r.s.exec(ctx, `INSERT INTO policy_sets (id, name, priority, yaml_source, compiled_hash, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		p.ID, p.Name, p.Priority, p.YAMLSource, p.CompiledHash, p.Status,
		r.s.tArg(p.CreatedAt), r.s.tArg(p.UpdatedAt))
	if err != nil {
		return PolicySet{}, err
	}
	return p, nil
}

func (r policyRepo) GetByID(ctx context.Context, id string) (PolicySet, error) {
	return scanPolicy(r.s.queryRow(ctx, `SELECT `+policyCols+` FROM policy_sets WHERE id = $1`, id))
}

func (r policyRepo) GetByName(ctx context.Context, name string) (PolicySet, error) {
	return scanPolicy(r.s.queryRow(ctx, `SELECT `+policyCols+` FROM policy_sets WHERE name = $1`, name))
}

func (r policyRepo) List(ctx context.Context) ([]PolicySet, error) {
	return r.list(ctx, policyCols)
}

// ListMeta selects a literal ” for yaml_source: same rows, same order, no
// YAML bytes on the wire from the database.
func (r policyRepo) ListMeta(ctx context.Context) ([]PolicySet, error) {
	return r.list(ctx, "id, name, priority, '' AS yaml_source, compiled_hash, status, created_at, updated_at")
}

func (r policyRepo) list(ctx context.Context, cols string) ([]PolicySet, error) {
	rows, err := r.s.query(ctx, `SELECT `+cols+` FROM policy_sets ORDER BY priority DESC, name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PolicySet
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r policyRepo) Update(ctx context.Context, p PolicySet) (PolicySet, error) {
	p.UpdatedAt = now()
	err := mustAffect(r.s.exec(ctx, `UPDATE policy_sets SET name = $1, priority = $2, yaml_source = $3,
		compiled_hash = $4, status = $5, updated_at = $6 WHERE id = $7`,
		p.Name, p.Priority, p.YAMLSource, p.CompiledHash, p.Status, r.s.tArg(p.UpdatedAt), p.ID))
	if err != nil {
		return PolicySet{}, err
	}
	return r.GetByID(ctx, p.ID)
}

func (r policyRepo) Delete(ctx context.Context, id string) error {
	return mustAffect(r.s.exec(ctx, `DELETE FROM policy_sets WHERE id = $1`, id))
}

type snapshotRepo struct{ s *sqlStore }

const snapshotCols = "id, signer_key_id, size, blob, active, created_at"

func scanSnapshot(row scanner) (Snapshot, error) {
	var sn Snapshot
	var ca scanTime
	if err := row.Scan(&sn.ID, &sn.SignerKeyID, &sn.Size, &sn.Blob, &sn.Active, &ca); err != nil {
		return Snapshot{}, scanErr(err)
	}
	sn.CreatedAt = ca.t
	return sn, nil
}

func (r snapshotRepo) Create(ctx context.Context, sn Snapshot) (Snapshot, error) {
	sn.CreatedAt = now()
	sn.Size = int64(len(sn.Blob))
	_, err := r.s.exec(ctx, `INSERT INTO snapshots (id, signer_key_id, size, blob, active, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		sn.ID, sn.SignerKeyID, sn.Size, sn.Blob, sn.Active, r.s.tArg(sn.CreatedAt))
	if err != nil {
		return Snapshot{}, err
	}
	return sn, nil
}

func (r snapshotRepo) GetByID(ctx context.Context, id string) (Snapshot, error) {
	return scanSnapshot(r.s.queryRow(ctx, `SELECT `+snapshotCols+` FROM snapshots WHERE id = $1`, id))
}

func (r snapshotRepo) GetActive(ctx context.Context) (Snapshot, error) {
	return scanSnapshot(r.s.queryRow(ctx, `SELECT `+snapshotCols+` FROM snapshots WHERE active = TRUE`))
}

func (r snapshotRepo) SetActive(ctx context.Context, id string) error {
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// One statement recomputes every row's flag, so concurrent activations
	// serialize on the row locks and can never BOTH stay active. A
	// clear-then-set pair would interleave under read-committed when two pods
	// of a fresh install Recompile at their first boot at once.
	// TestSnapshotSetActiveExclusive pins the contract.
	if _, err := tx.ExecContext(ctx, r.s.q(`UPDATE snapshots SET active = (id = $1)`), id); err != nil {
		return r.s.mapErr(err)
	}
	// A full-table statement cannot signal a missing id via rows-affected;
	// verify inside the tx so a bad id rolls the deactivation back.
	var n int
	if err := tx.QueryRowContext(ctx, r.s.q(`SELECT COUNT(*) FROM snapshots WHERE id = $1`), id).Scan(&n); err != nil {
		return r.s.mapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (r snapshotRepo) SetActiveFrom(ctx context.Context, id, from string) error {
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	read := `SELECT id FROM snapshots WHERE active = TRUE`
	if r.s.d == dialectPostgres {
		// The row lock makes a second publish on the same base wait here
		// until the first commits, and its read then finds no active row
		// with that id. A conditional UPDATE alone is not enough under read
		// committed, because its subquery is not checked again after a
		// lock wait.
		read += ` FOR UPDATE`
	}
	// SQLite needs no lock: one strazad owns the file, and its snapshot
	// service serializes its publishes.
	var active string
	switch err := tx.QueryRowContext(ctx, r.s.q(read)).Scan(&active); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: no snapshot is active, so %s is no longer the active snapshot", ErrConflict, from)
	case err != nil:
		return r.s.mapErr(err)
	case active != from:
		return fmt.Errorf("%w: the active snapshot is %s, not %s", ErrConflict, active, from)
	}
	if _, err := tx.ExecContext(ctx, r.s.q(`UPDATE snapshots SET active = (id = $1)`), id); err != nil {
		return r.s.mapErr(err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, r.s.q(`SELECT COUNT(*) FROM snapshots WHERE id = $1`), id).Scan(&n); err != nil {
		return r.s.mapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (r snapshotRepo) List(ctx context.Context) ([]Snapshot, error) {
	rows, err := r.s.query(ctx, `SELECT `+snapshotCols+` FROM snapshots ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Snapshot
	for rows.Next() {
		sn, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

type outboxRepo struct{ s *sqlStore }

const outboxCols = "id, subject, ce, published, attempts, created_at"

func scanOutbox(row scanner) (OutboxEvent, error) {
	var e OutboxEvent
	var ca scanTime
	if err := row.Scan(&e.ID, &e.Subject, &e.CE, &e.Published, &e.Attempts, &ca); err != nil {
		return OutboxEvent{}, scanErr(err)
	}
	e.CreatedAt = ca.t
	return e, nil
}

func (r outboxRepo) Insert(ctx context.Context, e OutboxEvent) (OutboxEvent, error) {
	if e.ID == "" {
		e.ID = newID()
	}
	e.CreatedAt = now()
	_, err := r.s.exec(ctx, `INSERT INTO events_outbox (id, subject, ce, published, attempts, created_at)
		VALUES ($1, $2, $3, FALSE, 0, $4)`,
		e.ID, e.Subject, e.CE, r.s.tArg(e.CreatedAt))
	if err != nil {
		return OutboxEvent{}, err
	}
	return e, nil
}

func (r outboxRepo) ListUnpublished(ctx context.Context, limit int) ([]OutboxEvent, error) {
	return r.listUnpublished(ctx, `SELECT `+outboxCols+` FROM events_outbox
		WHERE published = FALSE ORDER BY created_at LIMIT $1`, limit)
}

// ListRecent reads newest-first regardless of published state: the
// observation seam. ListUnpublished is correct for DRAINING and wrong for
// asserting "this emit happened": rows vanish from it the moment the
// relay delivers them.
func (r outboxRepo) ListRecent(ctx context.Context, limit int) ([]OutboxEvent, error) {
	return r.listUnpublished(ctx, `SELECT `+outboxCols+` FROM events_outbox
		ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
}

// ListUnpublishedControl narrows to control-plane subjects: everything
// OUTSIDE straza.audit.> (the dots are literal in LIKE; '%' is the only
// wildcard in the pattern). Order within the class is preserved.
func (r outboxRepo) ListUnpublishedControl(ctx context.Context, limit int) ([]OutboxEvent, error) {
	return r.listUnpublished(ctx, `SELECT `+outboxCols+` FROM events_outbox
		WHERE published = FALSE AND subject NOT LIKE 'straza.audit.%'
		ORDER BY created_at LIMIT $1`, limit)
}

func (r outboxRepo) listUnpublished(ctx context.Context, query string, limit int) ([]OutboxEvent, error) {
	rows, err := r.s.query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OutboxEvent
	for rows.Next() {
		e, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r outboxRepo) MarkPublished(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	_, err := r.s.exec(ctx,
		`UPDATE events_outbox SET published = TRUE WHERE id IN (`+strings.Join(ph, ", ")+`)`, args...)
	return err
}

func (r outboxRepo) IncAttempts(ctx context.Context, id string) error {
	return mustAffect(r.s.exec(ctx,
		`UPDATE events_outbox SET attempts = attempts + 1 WHERE id = $1`, id))
}

// Head returns the newest outbox id ("" on an empty outbox), the change
// feed's fast-forward point: a first-time liveSync consumer adopts the head
// as its cursor and reconciles once, instead of paging the whole history.
func (r outboxRepo) Head(ctx context.Context) (string, error) {
	var id string
	err := r.s.queryRow(ctx, `SELECT id FROM events_outbox ORDER BY id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ListAfter pages the outbox as an ordered history: id > after, optionally
// narrowed to exact subjects, oldest first. uuidv7 ids sort by mint time, so
// the id doubles as the change-feed cursor. Caveat (documented in the feed
// contract): multi-pod deployments mint ids on each pod's clock; ordering
// across pods is only as good as their clocks, which is why liveSync
// consumers keep periodic reconciliation as the safety net.
func (r outboxRepo) ListAfter(ctx context.Context, after string, subjects []string, limit int) ([]OutboxEvent, error) {
	q := `SELECT ` + outboxCols + ` FROM events_outbox WHERE id > $1`
	args := []any{after}
	if len(subjects) > 0 {
		ph := make([]string, len(subjects))
		for i, s := range subjects {
			ph[i] = fmt.Sprintf("$%d", len(args)+1)
			args = append(args, s)
		}
		q += ` AND subject IN (` + strings.Join(ph, ", ") + `)`
	}
	q += fmt.Sprintf(` ORDER BY id LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := r.s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []OutboxEvent
	for rows.Next() {
		e, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type auditRepo struct{ s *sqlStore }

func (r auditRepo) Append(ctx context.Context, ceID, ce, prevHash, hash string) (AuditRecord, error) {
	rec := AuditRecord{CE: ce, PrevHash: prevHash, Hash: hash, CreatedAt: now()}
	err := r.s.queryRow(ctx, `INSERT INTO audit_log (ce_id, ce, prev_hash, hash, created_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING seq`,
		ceID, ce, prevHash, hash, r.s.tArg(rec.CreatedAt)).Scan(&rec.Seq)
	if err != nil {
		return AuditRecord{}, r.s.mapErr(err)
	}
	return rec, nil
}

func (r auditRepo) ExistsCE(ctx context.Context, ceID string) (bool, error) {
	if ceID == "" {
		return false, nil
	}
	var one int
	err := r.s.queryRow(ctx, `SELECT 1 FROM audit_log WHERE ce_id = $1 LIMIT 1`, ceID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r auditRepo) Last(ctx context.Context) (AuditRecord, error) {
	return r.scanRecord(r.s.queryRow(ctx,
		`SELECT seq, ce, prev_hash, hash, created_at FROM audit_log ORDER BY seq DESC LIMIT 1`))
}

func (r auditRepo) LastHash(ctx context.Context) (int64, string, error) {
	var seq int64
	var hash string
	err := r.s.queryRow(ctx,
		`SELECT seq, hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&seq, &hash)
	if err != nil {
		return 0, "", scanErr(err)
	}
	return seq, hash, nil
}

func (r auditRepo) List(ctx context.Context, afterSeq int64, limit int) ([]AuditRecord, error) {
	return r.ListFiltered(ctx, AuditFilter{}, afterSeq, limit)
}

// ListRecent is the newest-first browsing read (`audit tail`); chain
// verification stays on List's ascending cursor.
func (r auditRepo) ListRecent(ctx context.Context, limit int) ([]AuditRecord, error) {
	return r.ListRecentFiltered(ctx, AuditFilter{}, limit)
}

// ListFiltered pages ascending matches: the filter lands in SQL before the
// LIMIT, so the seq cursor walks matches, never raw rows.
func (r auditRepo) ListFiltered(ctx context.Context, f AuditFilter, afterSeq int64, limit int) ([]AuditRecord, error) {
	args := []any{afterSeq}
	q := `SELECT seq, ce, prev_hash, hash, created_at FROM audit_log WHERE seq > $1` +
		r.filterSQL(f, &args)
	args = append(args, limit)
	q += ` ORDER BY seq LIMIT $` + strconv.Itoa(len(args))
	return r.listQuery(ctx, q, args...)
}

// ListRecentFiltered is the newest-first window of matches (ListRecent with
// the filter applied before the LIMIT).
func (r auditRepo) ListRecentFiltered(ctx context.Context, f AuditFilter, limit int) ([]AuditRecord, error) {
	var args []any
	q := `SELECT seq, ce, prev_hash, hash, created_at FROM audit_log`
	if where := r.filterSQL(f, &args); where != "" {
		q += ` WHERE` + strings.TrimPrefix(where, ` AND`)
	}
	args = append(args, limit)
	q += ` ORDER BY seq DESC LIMIT $` + strconv.Itoa(len(args))
	return r.listQuery(ctx, q, args...)
}

// ListSince pages the window the overview decision block counts: rows
// written at or after since, ascending by seq so the caller walks with the
// last seq it saw. created_at carries no index, so both dialects read the
// window as a scan; the limit bounds each pass.
func (r auditRepo) ListSince(ctx context.Context, since time.Time, afterSeq int64, limit int) ([]AuditRecord, error) {
	bound := any(since.UTC())
	if r.s.d == dialectSQLite {
		// sqlite keeps created_at as RFC3339Nano TEXT, a layout that drops
		// trailing zeros, so a whole second renders shorter than the same
		// second with a fraction and compares GREATER than it. Padding the
		// bound's fraction makes the text comparison agree with the instant
		// comparison; an hour-aligned bound would otherwise skip every row
		// written in the window's first second.
		bound = since.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	}
	return r.listQuery(ctx,
		`SELECT seq, ce, prev_hash, hash, created_at FROM audit_log
		 WHERE seq > $1 AND created_at >= $2 ORDER BY seq LIMIT $3`,
		afterSeq, bound, limit)
}

// filterSQL renders an AuditFilter as ` AND ...` clauses over the ce TEXT
// column, appending its arguments to args. Every stored ce is
// server-marshaled JSON (both Append sites and normalizeClientCE go through
// json.Marshal), so the Postgres jsonb cast cannot throw mid-scan; sqlite's
// json_extract returns NULL on anything malformed. Both effect comparisons
// drop NULL naturally, which IS the contract: a record carrying no decision
// effect never matches an effect filter.
func (r auditRepo) filterSQL(f AuditFilter, args *[]any) string {
	ph := func(v any) string {
		*args = append(*args, v)
		return "$" + strconv.Itoa(len(*args))
	}
	w := ""
	if f.Q != "" {
		w += ` AND lower(ce) LIKE ` + ph("%"+likeEscape(strings.ToLower(f.Q))+"%") + ` ESCAPE '\'`
	}
	if f.Effect != "" {
		if r.s.d == dialectPostgres {
			w += ` AND (ce::jsonb #>> '{data,effect}') = ` + ph(f.Effect)
		} else {
			w += ` AND json_extract(ce, '$.data.effect') = ` + ph(f.Effect)
		}
	}
	if f.UserID != "" || f.Username != "" {
		// The user filter lands before the limit like q and effect, so a
		// quiet user's page is never emptied by busier users' rows. The
		// data.userId term reaches the authn records, whose data.user is the
		// username and which the janitor's session ends carry on its own.
		if r.s.d == dialectPostgres {
			w += ` AND ((ce::jsonb #>> '{data,user}') IN (` + ph(f.UserID) + `, ` + ph(f.Username) + `)` +
				` OR (ce::jsonb #>> '{data,userId}') = ` + ph(f.UserID) +
				` OR (ce::jsonb #>> '{data,actorId}') = ` + ph(f.UserID) +
				` OR (ce::jsonb #>> '{data,actor}') = ` + ph(f.Username) + `)`
		} else {
			w += ` AND (json_extract(ce, '$.data.user') IN (` + ph(f.UserID) + `, ` + ph(f.Username) + `)` +
				` OR json_extract(ce, '$.data.userId') = ` + ph(f.UserID) +
				` OR json_extract(ce, '$.data.actorId') = ` + ph(f.UserID) +
				` OR json_extract(ce, '$.data.actor') = ` + ph(f.Username) + `)`
		}
	}
	return w
}

func (r auditRepo) listQuery(ctx context.Context, q string, args ...any) ([]AuditRecord, error) {
	rows, err := r.s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AuditRecord
	for rows.Next() {
		rec, err := r.scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (auditRepo) scanRecord(row scanner) (AuditRecord, error) {
	var rec AuditRecord
	var ca scanTime
	if err := row.Scan(&rec.Seq, &rec.CE, &rec.PrevHash, &rec.Hash, &ca); err != nil {
		return AuditRecord{}, scanErr(err)
	}
	rec.CreatedAt = ca.t
	return rec, nil
}
