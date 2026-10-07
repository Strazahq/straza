package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type draftRepo struct{ s *sqlStore }

// draftSelect reads a draft row. The proposer's via and client come from
// revision 1, the only place the tables record them.
const draftSelect = `SELECT d.id, d.revision, d.state, d.door, d.source, d.source_hash, d.slot, d.note, d.refusal,
	COALESCE(d.reverts, 0), d.proposer_id, d.proposer_name, d.proposer_agent,
	COALESCE(r1.author_via, ''), COALESCE(r1.author_client, ''), d.sponsor_id, d.sponsor_name,
	d.checked_revision, d.checked_at, d.checked_snapshot, d.check_counts, d.agent_verdict,
	d.created_at, d.updated_at, d.expires_at, d.decided_at, d.decided_by_id, d.decided_by_name,
	d.decided_via, d.decided_client, d.decided_reason, d.published_snapshot, d.acks
	FROM drafts d LEFT JOIN draft_revisions r1 ON r1.draft_id = d.id AND r1.revision = 1`

const draftItemCols = "draft_id, seq, kind, name, op, doc, base, base_op, base_doc, offered"

func scanDraft(row scanner) (DraftRow, error) {
	var d DraftRow
	var ca, ua scanTime
	var chk, exp, dec scanTimePtr
	p, by := &d.Proposer, &d.DecidedBy
	if err := row.Scan(&d.ID, &d.Revision, &d.State, &d.Door, &d.Source, &d.SourceHash, &d.Slot, &d.Note, &d.Refusal,
		&d.Reverts, &p.ID, &p.Name, &p.Agent, &p.Via, &p.Client, &p.SponsorID, &p.SponsorName,
		&d.CheckedRevision, &chk, &d.CheckedSnapshot, &d.CheckCounts, &d.AgentVerdict,
		&ca, &ua, &exp, &dec, &by.ID, &by.Name, &by.Via, &by.Client, &d.DecidedReason, &d.PublishedSnapshot, &d.Acks); err != nil {
		return DraftRow{}, scanErr(err)
	}
	d.CheckedAt, d.CreatedAt, d.UpdatedAt, d.ExpiresAt, d.DecidedAt = chk.t, ca.t, ua.t, exp.t, dec.t
	return d, nil
}

func scanDraftItem(row scanner) (int64, DraftItemRow, error) {
	var id int64
	var it DraftItemRow
	err := row.Scan(&id, &it.Seq, &it.Kind, &it.Name, &it.Op, &it.Doc, &it.Base, &it.BaseOp, &it.BaseDoc, &it.Offered)
	return id, it, err
}

// read runs fn in one read-only transaction. On Postgres it is repeatable
// read, so a draft and its items come from one moment even while a
// revision commits between the two statements. sqlite's one connection
// already serializes every transaction.
func (r draftRepo) read(ctx context.Context, fn func(tx *sql.Tx) error) error {
	var opts *sql.TxOptions
	if r.s.d == dialectPostgres {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	tx, err := r.s.db.BeginTx(ctx, opts)
	if err != nil {
		return r.s.mapErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return r.s.mapErr(tx.Commit())
}

func (r draftRepo) Create(ctx context.Context, d DraftRow, items []DraftItemRow, rev DraftRevisionRow) (DraftRow, error) {
	var out DraftRow
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = r.create(ctx, tx, d, items, rev)
		return err
	})
	if err != nil {
		return DraftRow{}, err
	}
	return out, nil
}

// create is Create on an open transaction, so that a publish can insert a
// direct route's one-item draft inside its own transaction.
func (r draftRepo) create(ctx context.Context, tx *sql.Tx, d DraftRow, items []DraftItemRow, rev DraftRevisionRow) (DraftRow, error) {
	if d.Proposer != (DraftActor{}) && d.Proposer != rev.Author {
		return DraftRow{}, fmt.Errorf("store: the proposer of a draft is the author of its revision 1, and the row names %+v where the revision names %+v",
			d.Proposer, rev.Author)
	}
	switch d.CheckedRevision {
	case 0:
	case 1:
		if err := allStamped(items); err != nil {
			return DraftRow{}, err
		}
	default:
		return DraftRow{}, fmt.Errorf("store: a new draft is checked at revision 1 or not yet, not at revision %d", d.CheckedRevision)
	}
	at := now()
	var reverts any
	if d.Reverts != 0 {
		reverts = d.Reverts
	}
	a := rev.Author
	var id int64
	err := tx.QueryRowContext(ctx, r.s.q(`INSERT INTO drafts (revision, state, door, source, source_hash, slot, note, refusal,
		reverts, proposer_id, proposer_name, proposer_agent, sponsor_id, sponsor_name, checked_revision, checked_at,
		checked_snapshot, check_counts, agent_verdict, created_at, updated_at, expires_at)
		VALUES (1, 'open', $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		RETURNING id`),
		d.Door, d.Source, d.SourceHash, d.Slot, d.Note, d.Refusal, reverts, a.ID, a.Name, a.Agent, a.SponsorID,
		a.SponsorName, d.CheckedRevision, r.s.tArgPtr(d.CheckedAt), d.CheckedSnapshot, d.CheckCounts, d.AgentVerdict,
		r.s.tArg(at), r.s.tArg(at), r.s.tArgPtr(d.ExpiresAt)).Scan(&id)
	if err != nil {
		return DraftRow{}, r.s.mapErr(err)
	}
	if err := r.insertItems(ctx, tx, id, items); err != nil {
		return DraftRow{}, err
	}
	rev.Revision, rev.Door, rev.CreatedAt = 1, d.Door, at
	if err := r.insertRevision(ctx, tx, id, rev); err != nil {
		return DraftRow{}, err
	}
	return scanDraft(tx.QueryRowContext(ctx, r.s.q(draftSelect+` WHERE d.id = $1`), id))
}

// allStamped refuses a checked revision that holds an item without a base.
// Nothing would stamp that item later, because a revision's check lands
// once and a publish refuses an item without a base.
func allStamped(items []DraftItemRow) error {
	for _, it := range items {
		if it.BaseOp == "" {
			return fmt.Errorf("store: a checked revision needs a base for every item, and %s/%s has none", it.Kind, it.Name)
		}
	}
	return nil
}

// insertItems writes items as seq 1 to n of draft id. An object named twice
// is refused here, because the primary key would answer it as ErrConflict,
// which a caller reads as an open draft already holding the slot.
func (r draftRepo) insertItems(ctx context.Context, tx *sql.Tx, id int64, items []DraftItemRow) error {
	seen := make(map[ObjectRef]bool, len(items))
	for i, it := range items {
		ref := ObjectRef{it.Kind, it.Name}
		if seen[ref] {
			return fmt.Errorf("store: a draft names %s/%s twice", it.Kind, it.Name)
		}
		seen[ref] = true
		_, err := tx.ExecContext(ctx, r.s.q(`INSERT INTO draft_items (draft_id, seq, kind, name, op, doc, base, base_op, base_doc, offered)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`),
			id, i+1, it.Kind, it.Name, it.Op, it.Doc, it.Base, it.BaseOp, it.BaseDoc, it.Offered)
		if err != nil {
			return r.s.mapErr(err)
		}
	}
	return nil
}

func (r draftRepo) insertRevision(ctx context.Context, tx *sql.Tx, id int64, rev DraftRevisionRow) error {
	a := rev.Author
	_, err := tx.ExecContext(ctx, r.s.q(`INSERT INTO draft_revisions (draft_id, revision, author_id, author_name, author_agent,
		author_via, author_client, sponsor_id, sponsor_name, door, digest, mechanical, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`),
		id, rev.Revision, a.ID, a.Name, a.Agent, a.Via, a.Client, a.SponsorID, a.SponsorName,
		rev.Door, rev.Digest, rev.Mechanical, r.s.tArg(rev.CreatedAt))
	return r.s.mapErr(err)
}

func (r draftRepo) Get(ctx context.Context, id int64) (DraftRow, []DraftItemRow, error) {
	return r.withItems(ctx, draftSelect+` WHERE d.id = $1`, id)
}

func (r draftRepo) BySlot(ctx context.Context, slot string) (DraftRow, []DraftItemRow, error) {
	if slot == "" {
		return DraftRow{}, nil, ErrNotFound
	}
	return r.withItems(ctx, draftSelect+` WHERE d.slot = $1 AND d.state = 'open'`, slot)
}

// withItems reads the one draft query selects and its items in one read.
func (r draftRepo) withItems(ctx context.Context, query string, arg any) (DraftRow, []DraftItemRow, error) {
	var d DraftRow
	var items []DraftItemRow
	err := r.read(ctx, func(tx *sql.Tx) error {
		var err error
		if d, err = scanDraft(tx.QueryRowContext(ctx, r.s.q(query), arg)); err != nil {
			return err
		}
		items, err = r.itemsOf(ctx, tx, d.ID)
		return err
	})
	if err != nil {
		return DraftRow{}, nil, err
	}
	return d, items, nil
}

// itemsOf reads the items of draft id on tx in seq order.
func (r draftRepo) itemsOf(ctx context.Context, tx *sql.Tx, id int64) ([]DraftItemRow, error) {
	rows, err := tx.QueryContext(ctx, r.s.q(`SELECT `+draftItemCols+` FROM draft_items WHERE draft_id = $1 ORDER BY seq`), id)
	if err != nil {
		return nil, r.s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []DraftItemRow
	for rows.Next() {
		_, it, err := scanDraftItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r draftRepo) Items(ctx context.Context, ids []int64) (map[int64][]DraftItemRow, error) {
	out := make(map[int64][]DraftItemRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	in, args := inList(ids)
	rows, err := r.s.query(ctx, `SELECT `+draftItemCols+` FROM draft_items WHERE draft_id IN (`+in+`) ORDER BY draft_id, seq`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		id, it, err := scanDraftItem(rows)
		if err != nil {
			return nil, err
		}
		out[id] = append(out[id], it)
	}
	return out, rows.Err()
}

// inList is the placeholders $1 to $n of an IN list of ids, and the ids as
// the query's arguments.
func inList(ids []int64) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i], args[i] = "$"+strconv.Itoa(i+1), id
	}
	return strings.Join(ph, ", "), args
}

func (r draftRepo) Revisions(ctx context.Context, id int64) ([]DraftRevisionRow, error) {
	revs, err := r.RevisionsOf(ctx, []int64{id})
	return revs[id], err
}

func (r draftRepo) RevisionsOf(ctx context.Context, ids []int64) (map[int64][]DraftRevisionRow, error) {
	out := make(map[int64][]DraftRevisionRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	in, args := inList(ids)
	rows, err := r.s.query(ctx, `SELECT draft_id, revision, author_id, author_name, author_agent, author_via, author_client,
		sponsor_id, sponsor_name, door, digest, mechanical, created_at
		FROM draft_revisions WHERE draft_id IN (`+in+`) ORDER BY draft_id, revision`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var v DraftRevisionRow
		var ca scanTime
		a := &v.Author
		if err := rows.Scan(&id, &v.Revision, &a.ID, &a.Name, &a.Agent, &a.Via, &a.Client, &a.SponsorID, &a.SponsorName,
			&v.Door, &v.Digest, &v.Mechanical, &ca); err != nil {
			return nil, err
		}
		v.CreatedAt = ca.t
		out[id] = append(out[id], v)
	}
	return out, rows.Err()
}

func (r draftRepo) Changes(ctx context.Context, id int64) ([]DraftChangeRow, error) {
	rows, err := r.s.query(ctx, `SELECT seq, kind, name, implied, before_op, before_doc, before_fp, after_op, after_doc, after_fp
		FROM draft_changes WHERE draft_id = $1 ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DraftChangeRow
	for rows.Next() {
		var c DraftChangeRow
		if err := rows.Scan(&c.Seq, &c.Kind, &c.Name, &c.Implied, &c.BeforeOp, &c.BeforeDoc, &c.BeforeFP,
			&c.AfterOp, &c.AfterDoc, &c.AfterFP); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r draftRepo) List(ctx context.Context, f DraftFilter, before int64, limit int) ([]DraftRow, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	for _, eq := range []struct{ col, v string }{
		{"d.state", f.State}, {"d.door", f.Door}, {"d.proposer_id", f.ProposerID}, {"d.source", f.Source},
	} {
		if eq.v != "" {
			where = append(where, eq.col+" = "+arg(eq.v))
		}
	}
	if f.AuthorID != "" {
		where = append(where, "EXISTS (SELECT 1 FROM draft_revisions a WHERE a.draft_id = d.id AND a.author_id = "+arg(f.AuthorID)+")")
	}
	if _, name, _ := strings.Cut(f.SlotPrefix, ":"); name != "" {
		// A prefix that names a whole slot matches that slot only, so the
		// saved edit of guard never lists the one of guard2.
		where = append(where, "d.slot = "+arg(f.SlotPrefix))
	} else if f.SlotPrefix != "" {
		// substr counts characters on both drivers, and an equality is
		// exact where sqlite's LIKE would ignore case.
		where = append(where, "substr(d.slot, 1, "+arg(utf8.RuneCountInString(f.SlotPrefix))+") = "+arg(f.SlotPrefix))
	}
	if f.Object.Kind != "" || f.Object.Name != "" {
		cond := "EXISTS (SELECT 1 FROM draft_items o WHERE o.draft_id = d.id"
		if f.Object.Kind != "" {
			cond += " AND o.kind = " + arg(f.Object.Kind)
		}
		if f.Object.Name != "" {
			cond += " AND o.name = " + arg(f.Object.Name)
		}
		where = append(where, cond+")")
	}
	if before > 0 {
		where = append(where, "d.id < "+arg(before))
	}
	query := draftSelect
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY d.id DESC LIMIT " + arg(pageLimit(limit))
	return r.drafts(ctx, query, args...)
}

// pageLimit answers limit, or 50 for none, so that a zero or negative
// limit reads the same page on both drivers: sqlite reads a negative LIMIT
// as no limit, and Postgres refuses it.
func pageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	return limit
}

func (r draftRepo) drafts(ctx context.Context, query string, args ...any) ([]DraftRow, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DraftRow
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r draftRepo) CountOpen(ctx context.Context, proposerID string) (int, error) {
	var n int
	err := r.s.queryRow(ctx, `SELECT COUNT(*) FROM drafts WHERE proposer_id = $1 AND state = 'open' AND slot = ''`,
		proposerID).Scan(&n)
	return n, err
}

func (r draftRepo) Revise(ctx context.Context, id int64, rv DraftRevise) (DraftRow, error) {
	var out DraftRow
	moved := false
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		at, next := now(), rv.From+1
		var note, expires any
		if rv.Note != nil {
			note = *rv.Note
		}
		if rv.ExpiresAt != nil {
			expires = r.s.tArg(*rv.ExpiresAt)
		}
		set := `revision = $1, note = COALESCE($2, note), expires_at = COALESCE($3, expires_at), updated_at = $4`
		args := []any{next, note, expires, r.s.tArg(at)}
		if c := rv.Checked; c != nil {
			set += `, checked_revision = $5, checked_at = $6, checked_snapshot = $7, check_counts = $8, agent_verdict = $9`
			args = append(args, next, r.s.tArg(c.At), c.Snapshot, c.Counts, c.AgentVerdict)
		}
		// The update is the compare-and-set: it takes the row lock, and a
		// second writer at From finds the revision moved once it gets it.
		query := `UPDATE drafts SET ` + set + ` WHERE id = $` + strconv.Itoa(len(args)+1) +
			` AND state = 'open' AND revision = $` + strconv.Itoa(len(args)+2)
		res, err := tx.ExecContext(ctx, r.s.q(query), append(args, id, rv.From)...)
		if err != nil {
			return r.s.mapErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			cur, err := scanDraft(tx.QueryRowContext(ctx, r.s.q(draftSelect+` WHERE d.id = $1`), id))
			if err != nil {
				return err
			}
			out, moved = cur, true
			return fmt.Errorf("%w: draft %d is %s at revision %d, not open at revision %d", ErrConflict, id, cur.State, cur.Revision, rv.From)
		}
		items, err := r.keepBases(ctx, tx, id, rv)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, r.s.q(`DELETE FROM draft_items WHERE draft_id = $1`), id); err != nil {
			return r.s.mapErr(err)
		}
		if err := r.insertItems(ctx, tx, id, items); err != nil {
			return err
		}
		rev := rv.Rev
		rev.Revision, rev.CreatedAt = next, at
		if err := r.insertRevision(ctx, tx, id, rev); err != nil {
			return err
		}
		out, err = scanDraft(tx.QueryRowContext(ctx, r.s.q(draftSelect+` WHERE d.id = $1`), id))
		return err
	})
	switch {
	case err == nil, moved && errors.Is(err, ErrConflict):
		return out, err
	default:
		return DraftRow{}, err
	}
}

// keepBases answers rv.Items under the base rule of DraftRevise, read
// against the items draft id holds now. A held item that was never stamped
// takes the given base, because its first check is this one.
func (r draftRepo) keepBases(ctx context.Context, tx *sql.Tx, id int64, rv DraftRevise) ([]DraftItemRow, error) {
	stored, err := r.itemsOf(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	held := make(map[ObjectRef]DraftItemRow, len(stored))
	for _, it := range stored {
		held[ObjectRef{it.Kind, it.Name}] = it
	}
	items := append([]DraftItemRow(nil), rv.Items...)
	for i := range items {
		old, ok := held[ObjectRef{items[i].Kind, items[i].Name}]
		if !ok {
			continue
		}
		items[i].Offered = old.Offered
		if old.BaseOp != "" && !rv.Rebase {
			items[i].Base, items[i].BaseOp, items[i].BaseDoc = old.Base, old.BaseOp, old.BaseDoc
		}
	}
	if rv.Checked != nil {
		if err := allStamped(items); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (r draftRepo) Stamp(ctx context.Context, id int64, revision int, bases []DraftItemRow, c DraftCheck) (bool, error) {
	landed := false
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, r.s.q(`UPDATE drafts SET checked_revision = $1, checked_at = $2, checked_snapshot = $3,
			check_counts = $4, agent_verdict = $5
			WHERE id = $6 AND state = 'open' AND revision = $7 AND checked_revision < $8`),
			revision, r.s.tArg(c.At), c.Snapshot, c.Counts, c.AgentVerdict, id, revision, revision)
		if err != nil {
			return r.s.mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		items, err := r.itemsOf(ctx, tx, id)
		if err != nil {
			return err
		}
		byRef := make(map[ObjectRef]DraftItemRow, len(bases))
		for _, b := range bases {
			byRef[ObjectRef{b.Kind, b.Name}] = b
		}
		for _, it := range items {
			if it.BaseOp != "" {
				continue
			}
			b := byRef[ObjectRef{it.Kind, it.Name}]
			if b.BaseOp == "" {
				return fmt.Errorf("store: the check of draft %d at revision %d has no base for %s/%s", id, revision, it.Kind, it.Name)
			}
			if _, err := tx.ExecContext(ctx, r.s.q(`UPDATE draft_items SET base = $1, base_op = $2, base_doc = $3
				WHERE draft_id = $4 AND kind = $5 AND name = $6`),
				b.Base, b.BaseOp, b.BaseDoc, id, it.Kind, it.Name); err != nil {
				return r.s.mapErr(err)
			}
		}
		landed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return landed, nil
}

func (r draftRepo) Close(ctx context.Context, id int64, revision int, state string, by DraftActor, reason string, at time.Time) (bool, error) {
	if state != "discarded" && state != "expired" {
		return false, fmt.Errorf("store: a draft closes as discarded or expired, not as %q", state)
	}
	query := `UPDATE drafts SET state = $1, decided_at = $2, decided_by_id = $3, decided_by_name = $4, decided_via = $5,
		decided_client = $6, decided_reason = $7, updated_at = $8 WHERE id = $9 AND state = 'open'`
	args := []any{state, r.s.tArg(at), by.ID, by.Name, by.Via, by.Client, reason, r.s.tArg(at), id}
	if revision != 0 {
		query += ` AND revision = $10`
		args = append(args, revision)
	}
	res, err := r.s.exec(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r draftRepo) ListUnchecked(ctx context.Context, limit int) ([]DraftRow, error) {
	return r.drafts(ctx, draftSelect+` WHERE d.state = 'open' AND d.checked_revision < d.revision
		ORDER BY d.id LIMIT $1`, pageLimit(limit))
}

func (r draftRepo) ListExpirable(ctx context.Context, at time.Time, limit int) ([]DraftRow, error) {
	bound := r.s.tArg(at)
	if r.s.d == dialectSQLite {
		// sqlite keeps times as RFC3339Nano text, which drops trailing
		// zeros, so the bound's fraction is padded to nine digits for the
		// text comparison to follow the instants, as auditRepo.ListSince
		// does.
		bound = at.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	}
	return r.drafts(ctx, draftSelect+` WHERE d.state = 'open' AND d.expires_at IS NOT NULL AND d.expires_at < $1
		ORDER BY d.expires_at, d.id LIMIT $2`, bound, pageLimit(limit))
}

func (r draftRepo) LatestChange(ctx context.Context, ref ObjectRef) (DraftRow, error) {
	return scanDraft(r.s.queryRow(ctx, draftSelect+` WHERE d.state = 'published'
		AND d.id IN (SELECT c.draft_id FROM draft_changes c WHERE c.kind = $1 AND c.name = $2)
		ORDER BY d.decided_at DESC, d.id DESC LIMIT 1`, ref.Kind, ref.Name))
}

func (r draftRepo) Generation(ctx context.Context) (int64, error) {
	var g int64
	if err := r.s.queryRow(ctx, `SELECT generation FROM config_generation WHERE id = 1`).Scan(&g); err != nil {
		return 0, scanErr(err)
	}
	return g, nil
}

func (r draftRepo) SetOffered(ctx context.Context, id int64, revision int, ref ObjectRef, offered string) error {
	res, err := r.s.exec(ctx, `UPDATE draft_items SET offered = $1 WHERE draft_id = $2 AND kind = $3 AND name = $4
		AND EXISTS (SELECT 1 FROM drafts d WHERE d.id = $5 AND d.state = 'open' AND d.revision = $6)`,
		offered, id, ref.Kind, ref.Name, id, revision)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	// Nothing was written: a draft that moved on, an item it does not
	// hold, or no such draft.
	var state string
	var at int
	if err := r.s.queryRow(ctx, `SELECT state, revision FROM drafts WHERE id = $1`, id).Scan(&state, &at); err != nil {
		return scanErr(err)
	}
	if state != "open" || at != revision {
		return fmt.Errorf("%w: draft %d is %s at revision %d, not open at revision %d", ErrConflict, id, state, at, revision)
	}
	return fmt.Errorf("%w: draft %d holds no %s/%s", ErrNotFound, id, ref.Kind, ref.Name)
}
