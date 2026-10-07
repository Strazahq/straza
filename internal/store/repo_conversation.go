package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type conversationRepo struct{ s *sqlStore }

// Insert stores one turn (the batch path with one element, so the summary
// table stays consistent no matter which entry point wrote the turn). A
// duplicate CE id maps to ErrConflict for callers that pin the old contract.
func (r conversationRepo) Insert(ctx context.Context, t ConversationTurn) (ConversationTurn, error) {
	n, err := r.InsertBatch(ctx, []ConversationTurn{t})
	if err != nil {
		return ConversationTurn{}, err
	}
	if n == 0 {
		return ConversationTurn{}, ErrConflict
	}
	return t, nil
}

// previewLen is the length of the inbox preview of a session's latest turn.
const previewLen = 200

// InsertBatch stores turns in one transaction: already-stored CE ids
// are filtered inside the tx (at-least-once redelivery; a cross-pod race on
// the same id aborts the tx with ErrConflict and the caller's redelivery
// converges), the survivors land as one multi-row insert, and each session's
// summary row is upserted: count, first/last activity, latest preview.
func (r conversationRepo) InsertBatch(ctx context.Context, turns []ConversationTurn) (int, error) {
	if len(turns) == 0 {
		return 0, nil
	}
	inserted := 0
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		existing, err := existingTurnCEIDs(ctx, tx, r.s, turns)
		if err != nil {
			return err
		}
		type sessAgg struct {
			userID          string
			count           int
			firstAt, lastAt time.Time
			preview         string
		}
		aggs := map[string]*sessAgg{}
		var order []string
		var values []string
		var args []any
		seen := map[string]bool{}
		n := 0
		for _, t := range turns {
			if t.CEID != "" && (existing[t.CEID] || seen[t.CEID]) {
				continue
			}
			if t.CEID != "" {
				seen[t.CEID] = true
			}
			if t.At.IsZero() {
				t.At = now()
			}
			base := n * 12
			ph := make([]string, 12)
			for i := range ph {
				ph[i] = fmt.Sprintf("$%d", base+i+1)
			}
			values = append(values, "("+strings.Join(ph, ", ")+")")
			// An external turn stores NO content: the caller persisted
			// the body under ContentHash before this insert, and previews below
			// still come from the in-memory content.
			stored := t.Content
			if t.BodyExternal {
				stored = ""
			}
			args = append(args, newID(), t.CEID, t.SessionID, t.UserID, t.Kind, t.Mode,
				stored, t.Truncated, t.ContentHash, t.AgentType, t.BodyExternal, r.s.tArg(t.At))
			n++

			a, ok := aggs[t.SessionID]
			if !ok {
				a = &sessAgg{userID: t.UserID, firstAt: t.At, lastAt: t.At,
					preview: previewOf(t.Content)}
				aggs[t.SessionID] = a
				order = append(order, t.SessionID)
			}
			a.count++
			if t.UserID != "" {
				a.userID = t.UserID
			}
			if t.At.Before(a.firstAt) {
				a.firstAt = t.At
			}
			if !t.At.Before(a.lastAt) {
				a.lastAt = t.At
				a.preview = previewOf(t.Content)
			}
		}
		if n == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, r.s.q(`INSERT INTO conversation_turns
			(id, ce_id, session_id, user_id, kind, mode, content, truncated, content_hash, agent_type, body_external, at)
			VALUES `+strings.Join(values, ", ")), args...)
		if err != nil {
			return r.s.mapErr(err)
		}
		// Summary upserts. The timestamp CASEs run in SQL so concurrent
		// batches for the same session cannot regress last_at (sqlite's
		// RFC3339Nano text compare is imprecise only within a sub-second
		// tie, matching the store's existing ORDER BY semantics).
		for _, sid := range order {
			a := aggs[sid]
			_, err := tx.ExecContext(ctx, r.s.q(`INSERT INTO conversation_sessions
				(session_id, user_id, turns, first_at, last_at, preview)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (session_id) DO UPDATE SET
					turns    = conversation_sessions.turns + excluded.turns,
					user_id  = CASE WHEN excluded.user_id <> '' THEN excluded.user_id ELSE conversation_sessions.user_id END,
					first_at = CASE WHEN excluded.first_at < conversation_sessions.first_at THEN excluded.first_at ELSE conversation_sessions.first_at END,
					last_at  = CASE WHEN excluded.last_at > conversation_sessions.last_at THEN excluded.last_at ELSE conversation_sessions.last_at END,
					preview  = CASE WHEN excluded.last_at >= conversation_sessions.last_at THEN excluded.preview ELSE conversation_sessions.preview END`),
				sid, a.userID, a.count, r.s.tArg(a.firstAt), r.s.tArg(a.lastAt), a.preview)
			if err != nil {
				return r.s.mapErr(err)
			}
		}
		inserted = n
		return nil
	})
	return inserted, err
}

func previewOf(content string) string {
	if len(content) > previewLen {
		// The cut is by byte and may split a multibyte character; the
		// dangling lead bytes are dropped (Postgres refuses invalid UTF-8).
		return strings.ToValidUTF8(content[:previewLen], "")
	}
	return content
}

// existingTurnCEIDs returns which of the batch's non-empty CE ids are
// already stored, in one query.
func existingTurnCEIDs(ctx context.Context, tx *sql.Tx, s *sqlStore, turns []ConversationTurn) (map[string]bool, error) {
	var ph []string
	var args []any
	for _, t := range turns {
		if t.CEID == "" {
			continue
		}
		ph = append(ph, fmt.Sprintf("$%d", len(args)+1))
		args = append(args, t.CEID)
	}
	existing := map[string]bool{}
	if len(args) == 0 {
		return existing, nil
	}
	rows, err := tx.QueryContext(ctx,
		s.q(`SELECT ce_id FROM conversation_turns WHERE ce_id IN (`+strings.Join(ph, ", ")+`)`),
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

func (r conversationRepo) ListBySession(ctx context.Context, sessionID string, limit int) ([]ConversationTurn, error) {
	if limit <= 0 {
		limit = 1000
	}
	turns, err := r.list(ctx, `SELECT id, ce_id, session_id, user_id, kind, mode, content, truncated, content_hash, agent_type, body_external, at
		FROM conversation_turns WHERE session_id = $1 ORDER BY at, id LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	// The order in time is taken here, because sqlite keeps at as
	// RFC3339Nano text, whose trailing zeros are dropped, so its text order
	// is right to the second and not always inside one. The query's order
	// still picks the rows under limit, and it puts turns of one instant in
	// id order, which the stable sort keeps.
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].At.Before(turns[j].At) })
	return turns, nil
}

// ListRecent returns the newest captured turns, bounded: the console
// Transcripts landing view (browse first, hunt second). An empty userID
// lists org-wide; zero limit means the Search default (100).
func (r conversationRepo) ListRecent(ctx context.Context, userID string, limit int) ([]ConversationTurn, error) {
	if limit <= 0 {
		limit = 100
	}
	if userID != "" {
		return r.list(ctx, `SELECT id, ce_id, session_id, user_id, kind, mode, content, truncated, content_hash, agent_type, body_external, at
			FROM conversation_turns WHERE user_id = $1 ORDER BY at DESC, id DESC LIMIT $2`, userID, limit)
	}
	return r.list(ctx, `SELECT id, ce_id, session_id, user_id, kind, mode, content, truncated, content_hash, agent_type, body_external, at
		FROM conversation_turns ORDER BY at DESC, id DESC LIMIT $1`, limit)
}

// ListConversations serves the Transcripts inbox from the maintained
// summary table: one indexed read of the newest sessions, instead of
// re-aggregating every stored turn on every screen visit.
func (r conversationRepo) ListConversations(ctx context.Context, limit int) ([]ConversationSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.s.query(ctx, `SELECT session_id, user_id, turns, first_at, last_at, preview
		FROM conversation_sessions ORDER BY last_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConversationSummary
	for rows.Next() {
		var c ConversationSummary
		var first, last scanTime
		if err := rows.Scan(&c.SessionID, &c.UserID, &c.Turns, &first, &last, &c.Preview); err != nil {
			return nil, scanErr(err)
		}
		c.FirstAt, c.LastAt = first.t, last.t
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r conversationRepo) Search(ctx context.Context, q ConversationSearch) ([]ConversationTurn, error) {
	if q.Substring == "" && q.ContentHash == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	where := ""
	args := []any{}
	n := func() int { return len(args) + 1 }
	switch {
	case q.Substring != "" && q.ContentHash != "":
		args = append(args, "%"+likeEscape(q.Substring)+"%", q.ContentHash)
		where = `(content LIKE $1 ESCAPE '\' OR content_hash = $2)`
	case q.Substring != "":
		args = append(args, "%"+likeEscape(q.Substring)+"%")
		where = `content LIKE $1 ESCAPE '\'`
	default:
		args = append(args, q.ContentHash)
		where = `content_hash = $1`
	}
	if q.UserID != "" {
		where += ` AND user_id = $` + strconv.Itoa(n())
		args = append(args, q.UserID)
	}
	args = append(args, limit)
	return r.list(ctx, `SELECT id, ce_id, session_id, user_id, kind, mode, content, truncated, content_hash, agent_type, body_external, at
		FROM conversation_turns WHERE `+where+` ORDER BY at DESC, id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
}

// conversationPurgeBatch bounds one retention DELETE. A single unbounded
// DELETE over a large turns table is one giant transaction: a WAL burst and
// a long-pinned vacuum horizon on Postgres, and on SQLite (one connection
// for the whole process) it blocks every other query for its full duration.
// Batching keeps each transaction small; a var so tests can shrink it.
var conversationPurgeBatch = 10_000

func (r conversationRepo) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	touched := map[string]bool{}
	for {
		rows, err := r.s.query(ctx, `DELETE FROM conversation_turns WHERE id IN
			(SELECT id FROM conversation_turns WHERE at < $1 LIMIT `+strconv.Itoa(conversationPurgeBatch)+`)
			RETURNING session_id`, r.s.tArg(cutoff))
		if err != nil {
			return total, err
		}
		n := int64(0)
		for rows.Next() {
			var sid string
			if err := rows.Scan(&sid); err != nil {
				_ = rows.Close()
				return total, err
			}
			touched[sid] = true
			n++
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return total, err
		}
		_ = rows.Close()
		total += n
		if n < int64(conversationPurgeBatch) {
			break
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
	// Reconcile the summaries the purge touched: recount and re-floor
	// first_at from the surviving turns, and drop sessions with nothing left.
	// last_at and preview describe the NEWEST turn, which a purge-by-age
	// never removes ahead of older ones.
	for sid := range touched {
		if _, err := r.s.exec(ctx, `DELETE FROM conversation_sessions
			WHERE session_id = $1 AND NOT EXISTS
				(SELECT 1 FROM conversation_turns WHERE session_id = $2)`, sid, sid); err != nil {
			return total, err
		}
		if _, err := r.s.exec(ctx, `UPDATE conversation_sessions SET
				turns = (SELECT count(*) FROM conversation_turns WHERE session_id = $1),
				first_at = COALESCE((SELECT min(at) FROM conversation_turns WHERE session_id = $2), first_at)
			WHERE session_id = $3`, sid, sid, sid); err != nil {
			return total, err
		}
	}
	return total, nil
}

func (r conversationRepo) list(ctx context.Context, query string, args ...any) ([]ConversationTurn, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConversationTurn
	for rows.Next() {
		var t ConversationTurn
		var at scanTime
		if err := rows.Scan(&t.ID, &t.CEID, &t.SessionID, &t.UserID, &t.Kind, &t.Mode,
			&t.Content, &t.Truncated, &t.ContentHash, &t.AgentType, &t.BodyExternal, &at); err != nil {
			return nil, scanErr(err)
		}
		t.At = at.t
		out = append(out, t)
	}
	return out, rows.Err()
}

// StorageBytes implements the per-dialect measurement documented on the
// interface. Both queries are constant (no placeholders) and run on the
// janitor cadence only.
func (r conversationRepo) StorageBytes(ctx context.Context) (int64, error) {
	var n int64
	if r.s.d == dialectPostgres {
		err := r.s.db.QueryRowContext(ctx,
			`SELECT pg_total_relation_size('conversation_turns')`).Scan(&n)
		return n, err
	}
	err := r.s.db.QueryRowContext(ctx,
		`SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&n)
	return n, err
}

// likeEscape neutralizes LIKE wildcards in user-supplied search input so a
// literal "%" or "_" matches itself.
func likeEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '%', '_', '\\':
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}
