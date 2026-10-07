package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type approvalRepo struct{ s *sqlStore }

// The ticket columns (class, consumed_at, consumed_by, grant_expires_at),
// grant_ttl_seconds, the args-preview columns (args_preview, args_truncated,
// args_bytes), notify, the decided-attribution pair (decided_reason,
// decided_device_id) and approver_users come last, so the scan positions of
// the columns before them stay as they are.
const approvalCols = "id, session_id, user_id, username, rule_id, set_name, argv_hash, lane, " +
	"summary, justification, approver_roles, self_approval, timeout_seconds, retry_ttl_seconds, " +
	"state, decided_by, decided_by_name, channel, channel_refs, created_at, expires_at, decided_at, " +
	"class, consumed_at, consumed_by, grant_expires_at, grant_ttl_seconds, " +
	"args_preview, args_truncated, args_bytes, notify, mode, decided_reason, decided_device_id, " +
	"approver_users"

func scanApproval(row scanner) (Approval, error) {
	var a Approval
	var roles, users, refs, notify string
	var ca, ea scanTime
	var da, coa, gea scanTimePtr
	var cb sql.NullString
	if err := row.Scan(&a.ID, &a.SessionID, &a.UserID, &a.Username, &a.RuleID, &a.SetName,
		&a.ArgvHash, &a.Lane, &a.Summary, &a.Justification, &roles, &a.SelfApproval,
		&a.TimeoutSeconds, &a.RetryTTLSeconds, &a.State, &a.DecidedBy, &a.DecidedByName,
		&a.Channel, &refs, &ca, &ea, &da, &a.Class, &coa, &cb, &gea, &a.GrantTTLSeconds,
		&a.ArgsPreview, &a.ArgsTruncated, &a.ArgsBytes, &notify, &a.Mode,
		&a.DecidedReason, &a.DecidedDeviceID, &users); err != nil {
		return Approval{}, scanErr(err)
	}
	a.ApproverRoles = decodeStrings(roles)
	a.ApproverUsers = decodeStrings(users)
	a.Notify = decodeStrings(notify)
	a.ChannelRefs = decodeStringMap(refs)
	a.CreatedAt, a.ExpiresAt, a.DecidedAt = ca.t, ea.t, da.t
	a.ConsumedAt, a.GrantExpiresAt, a.ConsumedBy = coa.t, gea.t, cb.String
	return a, nil
}

func (r approvalRepo) Insert(ctx context.Context, a Approval) (Approval, error) {
	a.ID = newID()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now()
	}
	if a.State == "" {
		a.State = "pending"
	}
	if a.Class == "" {
		a.Class = "hold"
	}
	if a.Mode == "" {
		a.Mode = "approve"
	}
	_, err := r.s.exec(ctx, `INSERT INTO approvals
		(id, session_id, user_id, username, rule_id, set_name, argv_hash, lane, summary,
		 justification, approver_roles, self_approval, timeout_seconds, retry_ttl_seconds,
		 state, decided_by, decided_by_name, channel, channel_refs, created_at, expires_at, decided_at,
		 class, consumed_at, consumed_by, grant_expires_at, grant_ttl_seconds,
		 args_preview, args_truncated, args_bytes, notify, mode, decided_reason, decided_device_id,
		 approver_users)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
		 $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35)`,
		a.ID, a.SessionID, a.UserID, a.Username, a.RuleID, a.SetName, a.ArgvHash, a.Lane, a.Summary,
		a.Justification, encodeStrings(a.ApproverRoles), a.SelfApproval, a.TimeoutSeconds, a.RetryTTLSeconds,
		a.State, a.DecidedBy, a.DecidedByName, a.Channel, encodeStringMap(a.ChannelRefs),
		r.s.tArg(a.CreatedAt), r.s.tArg(a.ExpiresAt), r.s.tArgPtr(a.DecidedAt),
		a.Class, r.s.tArgPtr(a.ConsumedAt), nullStr(a.ConsumedBy), r.s.tArgPtr(a.GrantExpiresAt), a.GrantTTLSeconds,
		a.ArgsPreview, a.ArgsTruncated, a.ArgsBytes, encodeStrings(a.Notify), a.Mode,
		a.DecidedReason, a.DecidedDeviceID, encodeStrings(a.ApproverUsers))
	if err != nil {
		return Approval{}, err
	}
	return a, nil
}

// nullStr maps "" to a SQL NULL so a nullable text column stays NULL rather
// than storing the empty string.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (r approvalRepo) GetByID(ctx context.Context, id string) (Approval, error) {
	return scanApproval(r.s.queryRow(ctx, `SELECT `+approvalCols+` FROM approvals WHERE id = $1`, id))
}

func (r approvalRepo) List(ctx context.Context, state string) ([]Approval, error) {
	if state == "" {
		return r.list(ctx, `SELECT `+approvalCols+` FROM approvals ORDER BY created_at DESC LIMIT 500`)
	}
	return r.list(ctx, `SELECT `+approvalCols+` FROM approvals WHERE state = $1 ORDER BY created_at DESC LIMIT 500`, state)
}

// ListBefore returns the newest `limit` records with id strictly less than
// beforeID (empty beforeID = the newest page), ordered DESC by the UUIDv7 PK.
// The primary key backs the keyset seek, so there is no scan wall and no extra
// index; this is the changes-feed keyset (store.go ListAfter) inverted for a
// newest-first human feed. `limit` <= 0 falls back to a sane default page.
func (r approvalRepo) ListBefore(ctx context.Context, beforeID string, limit int) ([]Approval, error) {
	if limit <= 0 {
		limit = 50
	}
	// Two forms (as List does for state): an empty cursor is the newest page;
	// otherwise seek strictly past beforeID. Each placeholder is used once so the
	// $1..$n sqlite rewrite stays valid (sqlstore.q).
	if beforeID == "" {
		return r.list(ctx, `SELECT `+approvalCols+` FROM approvals ORDER BY id DESC LIMIT $1`, limit)
	}
	return r.list(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE id < $1 ORDER BY id DESC LIMIT $2`, beforeID, limit)
}

// PageByState is ListBefore with the admin state filter: state "" =
// all states; same UUIDv7 id-keyset window, no row wall.
func (r approvalRepo) PageByState(ctx context.Context, state, beforeID string, limit int) ([]Approval, error) {
	if limit <= 0 {
		limit = 50
	}
	switch {
	case state == "" && beforeID == "":
		return r.list(ctx, `SELECT `+approvalCols+` FROM approvals ORDER BY id DESC LIMIT $1`, limit)
	case beforeID == "":
		return r.list(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE state = $1 ORDER BY id DESC LIMIT $2`, state, limit)
	case state == "":
		return r.list(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE id < $1 ORDER BY id DESC LIMIT $2`, beforeID, limit)
	default:
		return r.list(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE state = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, state, beforeID, limit)
	}
}

// CountByUser counts the approvals a user raised (idx_approvals_user).
func (r approvalRepo) CountByUser(ctx context.Context, userID string) (int, error) {
	var n int
	err := r.s.queryRow(ctx, `SELECT COUNT(*) FROM approvals WHERE user_id = $1`, userID).Scan(&n)
	return n, err
}

func (r approvalRepo) FindPendingByKey(ctx context.Context, sessionID, ruleID, argvHash string) (Approval, error) {
	return scanApproval(r.s.queryRow(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE session_id = $1 AND rule_id = $2 AND argv_hash = $3 AND state = 'pending'`,
		sessionID, ruleID, argvHash))
}

// FindOpenTicketByUser returns the newest still-pending ticket for
// (userID, ruleID, argvHash), the user-scoped half of the pending dedupe,
// ErrNotFound when none. Tickets outlive the session that
// raised them and their grants follow the requester, so a fresh session must
// attach to the ticket an earlier session already put in front of a human
// rather than mint a second one. Ordered by the UUIDv7 PK DESC so a database
// that predates the unique index (duplicates still un-pre-cleaned) still
// resolves deterministically to the newest row. Hold rows are excluded: holds
// stay session-scoped (FindPendingByKey).
func (r approvalRepo) FindOpenTicketByUser(ctx context.Context, userID, ruleID, argvHash string) (Approval, error) {
	return scanApproval(r.s.queryRow(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE user_id = $1 AND rule_id = $2 AND argv_hash = $3
		  AND class = 'ticket' AND state = 'pending'
		ORDER BY id DESC LIMIT 1`,
		userID, ruleID, argvHash))
}

// MarkDecided flips exactly one pending row to a terminal verdict and, in the
// same atomic UPDATE, stamps the use deadline. grantExpiresAt is computed by
// the caller (an approved ticket ⇒ decidedAt + grantTTLSeconds, an approved
// hold ⇒ decidedAt + retryTTLSeconds; nil for a denial) and materialized here
// via COALESCE so a nil leaves the column untouched: a pending row never
// carries a deadline, so this is precisely "set it on approve, otherwise
// preserve". Computing the deadline in Go (not sqlite interval math) keeps every
// timestamp on the store's single RFC3339Nano text path. The bool reports
// whether THIS caller won the one-time cross-pod gate. reason and deviceID
// ride the same atomic UPDATE: the decider's words and the
// signing device must never be attachable to a row someone else already flipped.
func (r approvalRepo) MarkDecided(ctx context.Context, id, state, decidedBy, decidedByName, channel string, at time.Time, grantExpiresAt *time.Time, reason, deviceID string) (bool, error) {
	res, err := r.s.exec(ctx, `UPDATE approvals
		SET state = $1, decided_by = $2, decided_by_name = $3, channel = $4, decided_at = $5,
		    grant_expires_at = COALESCE($6, grant_expires_at),
		    decided_reason = $7, decided_device_id = $8
		WHERE id = $9 AND state = 'pending'`,
		state, decidedBy, decidedByName, channel, r.s.tArg(at), r.s.tArgPtr(grantExpiresAt),
		reason, deviceID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// FindLatestTicketByKey returns the most recent ticket-class row for
// (userID, ruleID, argvHash) regardless of state (ErrNotFound when none); the
// terminal deny-final lookup for the PEP: a still-in-window denial must block a
// fresh call rather than open a new ticket. Ordered by the UUIDv7 PK DESC
// (time-ordered), so "latest" is the newest decision on this exact call.
func (r approvalRepo) FindLatestTicketByKey(ctx context.Context, userID, ruleID, argvHash string) (Approval, error) {
	return scanApproval(r.s.queryRow(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE user_id = $1 AND rule_id = $2 AND argv_hash = $3 AND class = 'ticket'
		ORDER BY id DESC LIMIT 1`,
		userID, ruleID, argvHash))
}

// MarkConsumed is the atomic single-use gate of every approval use, a ticket's
// grant (spec/policyset revision 6) and a hold's approval (revision 23): it
// consumes exactly one live approved row and reports whether THIS caller won
// the race. A row is consumable only while it is approved, unconsumed, and
// inside its use window (grant_expires_at > asOf); a pending row, a used row,
// or a closed window all lose (false, nil). The guard in the WHERE clause lets
// one UPDATE win across every replica. asOf is the moment the window is judged
// at, and consumed_at records now, the real time of the use. consumedBy
// records the consuming session. A row with no grant_expires_at (NULL) is
// never matched.
func (r approvalRepo) MarkConsumed(ctx context.Context, id, consumedBy string, now, asOf time.Time) (bool, error) {
	res, err := r.s.exec(ctx, `UPDATE approvals
		SET consumed_at = $1, consumed_by = $2
		WHERE id = $3 AND state = 'approved' AND consumed_at IS NULL AND `+r.s.timeAfter("grant_expires_at", "$4"),
		r.s.tArg(now), consumedBy, id, r.s.tArg(asOf))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// FindConsumableHold resolves the approved, unused hold of (userID,
// sessionID, ruleID, argvHash) still inside its use window, ErrNotFound when
// none. It leads with user_id, argv_hash and the literal approved state so
// idx_approvals_grant_consume serves it. A hold approved by a build that set
// no use deadline (grant_expires_at NULL) never matches, so its call asks
// again. The use itself is the atomic MarkConsumed by id.
func (r approvalRepo) FindConsumableHold(ctx context.Context, userID, sessionID, ruleID, argvHash string, now time.Time) (Approval, error) {
	return scanApproval(r.s.queryRow(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE user_id = $1 AND argv_hash = $2 AND state = 'approved' AND class = 'hold'
		  AND session_id = $3 AND rule_id = $4
		  AND consumed_at IS NULL AND `+r.s.timeAfter("grant_expires_at", "$5")+`
		ORDER BY grant_expires_at LIMIT 1`,
		userID, argvHash, sessionID, ruleID, r.s.tArg(now)))
}

// FindConsumableGrant resolves the live ticket grant bound to (userID,
// argvHash) for the retry/plan-gate path: an approved, unconsumed ticket still
// inside its consume window. Grants follow the requester + fingerprint, not the
// session, so a fresh session can find one a planning session raised. Returns
// ErrNotFound when none, and ties break by the soonest grant expiry
// (use-it-before-you-lose-it). Consumption is still the atomic MarkConsumed by
// id, and this lookup only selects the candidate.
func (r approvalRepo) FindConsumableGrant(ctx context.Context, userID, argvHash string, now time.Time) (Approval, error) {
	return scanApproval(r.s.queryRow(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE user_id = $1 AND argv_hash = $2 AND class = 'ticket' AND state = 'approved'
		  AND consumed_at IS NULL AND `+r.s.timeAfter("grant_expires_at", "$3")+`
		ORDER BY grant_expires_at LIMIT 1`,
		userID, argvHash, r.s.tArg(now)))
}

func (r approvalRepo) ListExpirable(ctx context.Context, at time.Time, limit int) ([]Approval, error) {
	if limit <= 0 {
		limit = 256
	}
	return r.list(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE state = 'pending' AND expires_at < $1 ORDER BY expires_at LIMIT $2`, r.s.tArg(at), limit)
}

func (r approvalRepo) ClaimExpired(ctx context.Context, id string, at time.Time) (bool, error) {
	res, err := r.s.exec(ctx, `UPDATE approvals SET state = 'expired', decided_at = $1
		WHERE id = $2 AND state = 'pending' AND expires_at < $3`, r.s.tArg(at), id, r.s.tArg(at))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListTicketsForReminder returns pending, ticket-class rows whose decision
// window closes within (now, dueBefore] and that carry no reminder_pushed_at
// marker yet: the near-expiry reminder sweep input (mirrors ListExpirable).
// The NOT LIKE guard matches the serialized channel_refs key: reminder_pushed_at
// is chosen to be unambiguous (no other ref key contains it), and while SQL LIKE
// treats '_' as a single-char wildcard, the literal underscores here still only
// match themselves, so the whole token must appear; an already-reminded row is
// excluded from this and every later scan.
func (r approvalRepo) ListTicketsForReminder(ctx context.Context, now, dueBefore time.Time, limit int) ([]Approval, error) {
	if limit <= 0 {
		limit = 256
	}
	return r.list(ctx, `SELECT `+approvalCols+` FROM approvals
		WHERE state = 'pending' AND class = 'ticket'
		  AND expires_at > $1 AND expires_at <= $2
		  AND channel_refs NOT LIKE '%reminder_pushed_at%'
		ORDER BY expires_at LIMIT $3`, r.s.tArg(now), r.s.tArg(dueBefore), limit)
}

// ClaimTicketReminder atomically stamps the near-expiry reminder marker onto one
// pending ticket. refs is the row's channel_refs map extended with
// reminder_pushed_at (the caller clones + adds it); the write only lands while
// the row is still pending and not yet reminded, so RowsAffected == 1 is the
// single-reminder win (same discipline as ClaimExpired). A concurrent claimant,
// or a resolution that raced in, sees the guard fail and loses (false, nil).
func (r approvalRepo) ClaimTicketReminder(ctx context.Context, id string, refs map[string]string) (bool, error) {
	res, err := r.s.exec(ctx, `UPDATE approvals SET channel_refs = $1
		WHERE id = $2 AND state = 'pending' AND channel_refs NOT LIKE '%reminder_pushed_at%'`,
		encodeStringMap(refs), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (r approvalRepo) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.s.exec(ctx, `DELETE FROM approvals WHERE state != 'pending' AND created_at < $1`, r.s.tArg(cutoff))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (r approvalRepo) SetChannelRefs(ctx context.Context, id string, refs map[string]string) error {
	return mustAffect(r.s.exec(ctx, `UPDATE approvals SET channel_refs = $1 WHERE id = $2`,
		encodeStringMap(refs), id))
}

func (r approvalRepo) list(ctx context.Context, query string, args ...any) ([]Approval, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// JSON column helpers: approver_roles and channel_refs travel as text so both
// dialects store them identically (no array/jsonb split).

func encodeStrings(v []string) string {
	if len(v) == 0 {
		return "[]"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeStrings(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func encodeStringMap(v map[string]string) string {
	if len(v) == 0 {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decodeStringMap(s string) map[string]string {
	if s == "" || s == "{}" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
