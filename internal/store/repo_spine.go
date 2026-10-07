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

// Spine hot-path repo methods: the claimed outbox
// drain and the batched chain append. Both run whole passes inside one
// transaction, so they live here with the store's only tx helper rather than
// in the per-table repo files.

// tx runs fn inside a transaction; rollback on error, mapErr on commit.
func (s *sqlStore) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return s.mapErr(tx.Commit())
}

// InsertBatch stores a whole accepted ingest batch in ONE transaction,
// because per-event autocommit inserts drag checkin p99 into seconds at
// ingest saturation, sqlite worst. It writes 200-row statements inside one
// tx, and each row's created_at steps one microsecond past the previous so
// the drain's created_at order replays submission order exactly as
// sequential Inserts would: capture prompt/reply pairs must chain in
// emission order. All-or-nothing by design: a per-event skip could
// partial-accept under a store fault while the client deletes its spool on
// any 200, silently losing records, and one tx means the caller can answer
// retryable instead. It returns only once the last row's created_at has
// passed, so a write that begins after it returns sorts after the batch.
func (r outboxRepo) InsertBatch(ctx context.Context, events []OutboxEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	base := now()
	began := time.Now()
	for i := range events {
		if events[i].ID == "" {
			events[i].ID = newID()
		}
		// One µs step per row keeps drain order == submission order. The
		// extra nanosecond forces RFC3339Nano to render all 9 fractional
		// digits: sqlite compares created_at TEXT lexically, and Nano
		// FORMATTING drops trailing zeros, so mixed-width fractions sort
		// wrong. Fixed width makes lexical == numeric inside the batch. PG
		// rounds the ns away and orders by the µs step.
		events[i].CreatedAt = base.Add(time.Duration(i)*time.Microsecond + time.Nanosecond)
	}
	const chunk = 200 // 4 params/row: far inside the PG 65535-param cap
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		for at := 0; at < len(events); at += chunk {
			end := min(at+chunk, len(events))
			values := make([]string, 0, end-at)
			args := make([]any, 0, (end-at)*4)
			for i, e := range events[at:end] {
				b := i * 4
				values = append(values, fmt.Sprintf("($%d, $%d, $%d, FALSE, 0, $%d)", b+1, b+2, b+3, b+4))
				args = append(args, e.ID, e.Subject, e.CE, r.s.tArg(e.CreatedAt))
			}
			q := r.s.q(`INSERT INTO events_outbox (id, subject, ce, published, attempts, created_at) VALUES ` +
				strings.Join(values, ", "))
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return r.s.mapErr(err)
			}
		}
		return nil
	})
	// The rows are stamped one microsecond apart from base. Only a batch
	// that took less than a microsecond per row has any of that span left,
	// and it is waited out on an error too, because a commit that failed to
	// answer may have stored the rows. The wait is measured from began on
	// the monotonic clock, so a step of the wall clock cannot stretch it.
	time.Sleep(time.Duration(len(events))*time.Microsecond - time.Since(began))
	if err != nil {
		return 0, err
	}
	return len(events), nil
}

// DrainClaimed claims up to limit unpublished rows (control-plane subjects
// only when controlOnly: everything outside straza.audit.>), publishes each
// via publish in created_at order, and marks the successes published, all in
// ONE transaction. On Postgres the claim is FOR UPDATE SKIP LOCKED:
// concurrent pods take DISJOINT row sets and share the drain instead of
// duplicating every publish, and a pod that dies mid-pass rolls back and its
// rows return to the pool. SQLite's single connection serializes passes
// entirely.
//
// A publish error ends the pass: rows already published are still marked
// (publish-then-mark keeps at-least-once; consumers dedupe by CE id), the
// rest stay unpublished for the next pass, and the error is returned with
// the count. Row locks are held across the publish calls; that is the
// point: under SKIP LOCKED nobody waits on them, and the mark can never race
// another pod re-claiming the same rows.
func (r outboxRepo) DrainClaimed(ctx context.Context, controlOnly bool, limit int, publish func(OutboxEvent) error) (int, error) {
	q := `SELECT ` + outboxCols + ` FROM events_outbox WHERE published = FALSE`
	if controlOnly {
		q += ` AND subject NOT LIKE 'straza.audit.%'`
	}
	q += ` ORDER BY created_at LIMIT $1`
	if r.s.d == dialectPostgres {
		q += ` FOR UPDATE SKIP LOCKED`
	}

	published := 0
	var pubErr error
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, r.s.q(q), limit)
		if err != nil {
			return err
		}
		var events []OutboxEvent
		for rows.Next() {
			e, err := scanOutbox(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			events = append(events, e)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close() // fully drained before the next statement on this tx

		var ids []string
		for _, e := range events {
			if err := publish(e); err != nil {
				pubErr = err
				break
			}
			ids = append(ids, e.ID)
		}
		published = len(ids)
		if len(ids) == 0 {
			return nil
		}
		ph := make([]string, len(ids))
		args := make([]any, len(ids))
		for i, id := range ids {
			ph[i] = fmt.Sprintf("$%d", i+1)
			args[i] = id
		}
		_, err = tx.ExecContext(ctx,
			r.s.q(`UPDATE events_outbox SET published = TRUE WHERE id IN (`+strings.Join(ph, ", ")+`)`),
			args...)
		return err
	})
	if err != nil {
		return 0, err
	}
	return published, pubErr
}

// CountUnpublished counts unpublished outbox rows, capped at ceil.
func (r outboxRepo) CountUnpublished(ctx context.Context, ceil int) (int, error) {
	var n int
	err := r.s.queryRow(ctx, `SELECT count(*) FROM
		(SELECT 1 FROM events_outbox WHERE published = FALSE LIMIT $1) t`, ceil).Scan(&n)
	return n, err
}

// outboxPruneBatch bounds one prune DELETE (same rationale as the
// conversation janitor batch); a var so tests can shrink it.
var outboxPruneBatch = 10_000

// PruneBulkPublished deletes published straza.audit.> rows older than
// cutoff, batched.
func (r outboxRepo) PruneBulkPublished(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		res, err := r.s.exec(ctx, `DELETE FROM events_outbox WHERE id IN
			(SELECT id FROM events_outbox WHERE published = TRUE
				AND subject LIKE 'straza.audit.%' AND created_at < $1
				LIMIT `+strconv.Itoa(outboxPruneBatch)+`)`, r.s.tArg(cutoff))
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < int64(outboxPruneBatch) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// ChainEvent is one event bound for the audit hash chain.
type ChainEvent struct {
	CEID string // "" = untracked (defensive path): always appended, never deduped
	CE   string
}

// auditChainLockKey is the advisory-lock key serializing chain appends on
// Postgres (an arbitrary constant, unique within this codebase).
const auditChainLockKey = 74218501

// AppendChained appends a batch to the audit chain in ONE transaction under
// a single-writer guarantee: on Postgres a transaction-scoped advisory lock
// serializes writers, so multiple pods may batch CONCURRENTLY and the chain
// stays linear, and SQLite's single connection serializes naturally. Inside
// the lock: already-chained CE ids are filtered (at-least-once redelivery),
// the previous hash is read, every event is linked with link(prev, ce), and
// the batch lands as one multi-row insert. Returns how many rows were
// appended. An error appends nothing (atomic), and the caller redelivers the
// whole batch and the filter converges.
func (r auditRepo) AppendChained(ctx context.Context, events []ChainEvent, genesis string, link func(prevHash, ce string) string) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	appended := 0
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		if r.s.d == dialectPostgres {
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, auditChainLockKey); err != nil {
				return err
			}
		}

		existing, err := existingCEIDs(ctx, tx, r.s, events)
		if err != nil {
			return err
		}
		prev := genesis
		var last string
		err = tx.QueryRowContext(ctx, `SELECT hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&last)
		if err == nil {
			prev = last
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var values []string
		var args []any
		seen := map[string]bool{}
		n := 0
		created := r.s.tArg(now())
		for _, e := range events {
			if e.CEID != "" && (existing[e.CEID] || seen[e.CEID]) {
				continue // already chained, or a duplicate within this batch
			}
			if e.CEID != "" {
				seen[e.CEID] = true
			}
			hash := link(prev, e.CE)
			base := n * 5
			values = append(values, fmt.Sprintf("($%d, $%d, $%d, $%d, $%d)",
				base+1, base+2, base+3, base+4, base+5))
			args = append(args, e.CEID, e.CE, prev, hash, created)
			prev = hash
			n++
		}
		if n == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx,
			r.s.q(`INSERT INTO audit_log (ce_id, ce, prev_hash, hash, created_at) VALUES `+strings.Join(values, ", ")),
			args...)
		if err != nil {
			return r.s.mapErr(err)
		}
		appended = n
		return nil
	})
	return appended, err
}

// existingCEIDs returns which of the batch's non-empty CE ids are already
// chained, in one query.
func existingCEIDs(ctx context.Context, tx *sql.Tx, s *sqlStore, events []ChainEvent) (map[string]bool, error) {
	var ph []string
	var args []any
	for _, e := range events {
		if e.CEID == "" {
			continue
		}
		ph = append(ph, fmt.Sprintf("$%d", len(args)+1))
		args = append(args, e.CEID)
	}
	existing := map[string]bool{}
	if len(args) == 0 {
		return existing, nil
	}
	rows, err := tx.QueryContext(ctx,
		s.q(`SELECT ce_id FROM audit_log WHERE ce_id IN (`+strings.Join(ph, ", ")+`)`),
		args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		existing[id] = true
	}
	return existing, rows.Err()
}
