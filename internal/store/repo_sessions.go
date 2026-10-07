package store

import (
	"context"
	"strconv"
	"strings"
	"time"
)

type deviceRepo struct{ s *sqlStore }

const deviceCols = "id, user_id, name, fingerprint, platform, status, enrolled_at, client_kind"

func scanDevice(row scanner) (Device, error) {
	var d Device
	var ea scanTime
	if err := row.Scan(&d.ID, &d.UserID, &d.Name, &d.Fingerprint, &d.Platform, &d.Status, &ea, &d.ClientKind); err != nil {
		return Device{}, scanErr(err)
	}
	d.EnrolledAt = ea.t
	return d, nil
}

func (r deviceRepo) Create(ctx context.Context, d Device) (Device, error) {
	d.ID = newID()
	d.EnrolledAt = now()
	if d.Status == "" {
		d.Status = "active"
	}
	_, err := r.s.exec(ctx, `INSERT INTO devices (id, user_id, name, fingerprint, platform, status, enrolled_at, client_kind)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		d.ID, d.UserID, d.Name, d.Fingerprint, d.Platform, d.Status, r.s.tArg(d.EnrolledAt), d.ClientKind)
	if err != nil {
		return Device{}, err
	}
	return d, nil
}

func (r deviceRepo) GetByID(ctx context.Context, id string) (Device, error) {
	return scanDevice(r.s.queryRow(ctx, `SELECT `+deviceCols+` FROM devices WHERE id = $1`, id))
}

func (r deviceRepo) ListByUser(ctx context.Context, userID string) ([]Device, error) {
	rows, err := r.s.query(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE user_id = $1 ORDER BY enrolled_at`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r deviceRepo) Update(ctx context.Context, d Device) (Device, error) {
	err := mustAffect(r.s.exec(ctx, `UPDATE devices SET name = $1, fingerprint = $2, platform = $3, status = $4, client_kind = $5
		WHERE id = $6`, d.Name, d.Fingerprint, d.Platform, d.Status, d.ClientKind, d.ID))
	if err != nil {
		return Device{}, err
	}
	return r.GetByID(ctx, d.ID)
}

func (r deviceRepo) Delete(ctx context.Context, id string) error {
	return mustAffect(r.s.exec(ctx, `DELETE FROM devices WHERE id = $1`, id))
}

type sessionRepo struct{ s *sqlStore }

const sessionCols = "id, user_id, device_id, harness_name, harness_version, attestation_level, attestation_hashes, status, started_at, last_seen, client_version"

func scanSession(row scanner) (Session, error) {
	var s Session
	var sa, ls scanTime
	if err := row.Scan(&s.ID, &s.UserID, &s.DeviceID, &s.HarnessName, &s.HarnessVersion,
		&s.AttestationLevel, &s.AttestationHashes, &s.Status, &sa, &ls, &s.ClientVersion); err != nil {
		return Session{}, scanErr(err)
	}
	s.StartedAt, s.LastSeen = sa.t, ls.t
	return s, nil
}

func (r sessionRepo) Create(ctx context.Context, s Session) (Session, error) {
	if s.ID == "" {
		s.ID = newID()
	}
	s.StartedAt = now()
	s.LastSeen = s.StartedAt
	if s.Status == "" {
		s.Status = SessionActive
	}
	if s.AttestationLevel == "" {
		s.AttestationLevel = AttestationNone
	}
	if s.AttestationHashes == "" {
		s.AttestationHashes = "{}"
	}
	_, err := r.s.exec(ctx, `INSERT INTO sessions (id, user_id, device_id, harness_name, harness_version, attestation_level, attestation_hashes, status, started_at, last_seen, client_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		s.ID, s.UserID, s.DeviceID, s.HarnessName, s.HarnessVersion,
		s.AttestationLevel, s.AttestationHashes, s.Status, r.s.tArg(s.StartedAt), r.s.tArg(s.LastSeen), s.ClientVersion)
	if err != nil {
		return Session{}, err
	}
	return s, nil
}

func (r sessionRepo) GetByID(ctx context.Context, id string) (Session, error) {
	return scanSession(r.s.queryRow(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id = $1`, id))
}

// List and ListByUser answer newest-first: every read surface (console,
// strazactl) leads with current activity, so a truncated first page cuts
// old sessions, not live ones.
func (r sessionRepo) List(ctx context.Context, status string) ([]Session, error) {
	if status == "" {
		return r.sessions(ctx, `SELECT `+sessionCols+` FROM sessions ORDER BY started_at DESC`)
	}
	return r.sessions(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE status = $1 ORDER BY started_at DESC`, status)
}

func (r sessionRepo) ListByUser(ctx context.Context, userID string) ([]Session, error) {
	return r.sessions(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE user_id = $1 ORDER BY started_at DESC`, userID)
}

// Page is the keyset window over sessions: newest-first by UUIDv7 id
// (creation order, so it matches List's started_at DESC), optionally
// narrowed by status and/or user. beforeID "" = the newest page.
// sessionSortKeys are the keys Page accepts: the id (newest first, the
// default), last_seen, status, the attestation level, and the owner's
// username through a join on users, empty when the user row is gone.
var sessionSortKeys = map[string]sortKey{
	"":            column("s.id"),
	"started":     column("s.id"),
	"last_seen":   {isTime: true, expr: func(func(any) string) string { return "s.last_seen" }},
	"status":      column("s.status"),
	"attestation": column("s.attestation_level"),
	"user":        column("COALESCE(u.username, '')"),
}

// sessionColsQualified is sessionCols under the alias the joined Page uses.
var sessionColsQualified = "s." + strings.ReplaceAll(sessionCols, ", ", ", s.")

// Page is the keyset window over sessions in the order srt names,
// narrowed by status and/or user; c is the row the page continues after.
func (r sessionRepo) Page(ctx context.Context, status, userID string, srt Sort, c Cursor, limit int) ([]Session, error) {
	key, ok := sessionSortKeys[srt.Key]
	if !ok {
		return nil, ErrBadSort
	}
	var args []any
	ph := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	q := `SELECT ` + sessionColsQualified + ` FROM sessions s`
	if srt.Key == "user" {
		q += ` LEFT JOIN users u ON u.id = s.user_id AND u.deleted_at IS NULL`
	}
	var conds []string
	if status != "" {
		conds = append(conds, "s.status = "+ph(status))
	}
	if userID != "" {
		conds = append(conds, "s.user_id = "+ph(userID))
	}
	where, order, err := r.s.keyset(key, "s.id", srt, c, ph)
	if err != nil {
		return nil, err
	}
	if where != "" {
		conds = append(conds, strings.TrimPrefix(where, " AND "))
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += order + " LIMIT " + ph(limit)
	return r.sessions(ctx, q, args...)
}

func (r sessionRepo) Touch(ctx context.Context, id string, at time.Time) error {
	return mustAffect(r.s.exec(ctx,
		`UPDATE sessions SET last_seen = $1 WHERE id = $2`, r.s.tArg(at.UTC()), id))
}

func (r sessionRepo) SetStatus(ctx context.Context, id, status string) error {
	return mustAffect(r.s.exec(ctx,
		`UPDATE sessions SET status = $1 WHERE id = $2`, status, id))
}

// SetStatusIfChanged is SetStatus with a truthful transition report. The
// distinction cannot come from RowsAffected on a plain UPDATE (both dialects
// count a same-value write as affected), so the WHERE clause carries it, and
// a 0-row outcome is disambiguated with one lookup (absent vs already-there).
// The row is the durable signal on purpose: an in-memory check (denylist)
// answers wrongly on a restarted or peer pod, whose maps are rebuilt FROM
// these rows.
func (r sessionRepo) SetStatusIfChanged(ctx context.Context, id, status string) (bool, error) {
	// status rides twice: the dialect rewriter takes $1..$n strictly in order.
	res, err := r.s.exec(ctx,
		`UPDATE sessions SET status = $1 WHERE id = $2 AND status <> $3`, status, id, status)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if _, err := r.GetByID(ctx, id); err != nil {
		return false, err // ErrNotFound for an absent session
	}
	return false, nil // already carried the target status
}

// CloseIdle closes every active session not seen since the cutoff: such a
// session's token expired long ago and can never refresh (an expired token
// is refused at checkin), so the row is dead bookkeeping, not a live
// principal. Returns how many were closed. Revoked rows keep their status.
// LastSeenByUsers batches "when was this user last seen" for a page of user
// ids into one grouped query, because the users list must never do per-row
// session lookups. MAX over tArg-formatted timestamps carries the same
// coarse sub-second caveat CloseIdle accepts.
func (r sessionRepo) LastSeenByUsers(ctx context.Context, userIDs []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(userIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(userIDs))
	ps := make([]string, len(userIDs))
	for i, id := range userIDs {
		args[i] = id
		ps[i] = "$" + strconv.Itoa(i+1)
	}
	rows, err := r.s.query(ctx, `SELECT user_id, MAX(last_seen) FROM sessions
		WHERE user_id IN (`+strings.Join(ps, ", ")+`) GROUP BY user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var ls scanTime
		if err := rows.Scan(&id, &ls); err != nil {
			return nil, err
		}
		out[id] = ls.t
	}
	return out, rows.Err()
}

func (r sessionRepo) CloseIdle(ctx context.Context, cutoff time.Time) ([]ClosedSession, error) {
	return r.closeActive(ctx,
		`UPDATE sessions SET status = $1 WHERE status = $2 AND last_seen < $3 RETURNING id, user_id`, cutoff)
}

// CloseStartedBefore closes every active session started before the cutoff,
// whether or not it is still being refreshed: past the absolute lifetime the
// row ends and the client starts a fresh session from its device credential.
func (r sessionRepo) CloseStartedBefore(ctx context.Context, cutoff time.Time) ([]ClosedSession, error) {
	return r.closeActive(ctx,
		`UPDATE sessions SET status = $1 WHERE status = $2 AND started_at < $3 RETURNING id, user_id`, cutoff)
}

// closeActive runs one of the two janitor closes above and collects the
// closed (id, user_id) pairs. query binds $1 = closed, $2 = active and $3 =
// the cutoff.
func (r sessionRepo) closeActive(ctx context.Context, query string, cutoff time.Time) ([]ClosedSession, error) {
	// RETURNING works on both dialects (PostgreSQL, and SQLite since 3.35);
	// the rewriter only touches placeholders.
	rows, err := r.s.query(ctx, query, SessionClosed, SessionActive, r.s.tArg(cutoff.UTC()))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ClosedSession
	for rows.Next() {
		var c ClosedSession
		if err := rows.Scan(&c.ID, &c.UserID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r sessionRepo) sessions(ctx context.Context, query string, args ...any) ([]Session, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type revocationRepo struct{ s *sqlStore }

const revocationCols = "id, kind, target_id, reason, origin, created_at"

func scanRevocation(row scanner) (Revocation, error) {
	var rv Revocation
	var ca scanTime
	if err := row.Scan(&rv.ID, &rv.Kind, &rv.TargetID, &rv.Reason, &rv.Origin, &ca); err != nil {
		return Revocation{}, scanErr(err)
	}
	rv.CreatedAt = ca.t
	return rv, nil
}

func (r revocationRepo) Create(ctx context.Context, rv Revocation) (Revocation, error) {
	rv.ID = newID()
	rv.CreatedAt = now()
	if rv.Origin == "" {
		// A missing origin gets the strongest survival class: a row nobody
		// claims must never be liftable by an IdM write (fail closed).
		rv.Origin = RevocationOriginAdmin
	}
	_, err := r.s.exec(ctx, `INSERT INTO revocations (id, kind, target_id, reason, origin, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		rv.ID, rv.Kind, rv.TargetID, rv.Reason, rv.Origin, r.s.tArg(rv.CreatedAt))
	if err != nil {
		return Revocation{}, err
	}
	return rv, nil
}

func (r revocationRepo) ListSince(ctx context.Context, t time.Time) ([]Revocation, error) {
	return r.revocations(ctx, `SELECT `+revocationCols+` FROM revocations
		WHERE created_at >= $1 ORDER BY created_at`, r.s.tArg(t.UTC()))
}

func (r revocationRepo) List(ctx context.Context) ([]Revocation, error) {
	return r.revocations(ctx, `SELECT `+revocationCols+` FROM revocations ORDER BY created_at`)
}

func (r revocationRepo) Delete(ctx context.Context, kind, targetID string) error {
	_, err := r.s.exec(ctx, `DELETE FROM revocations WHERE kind = $1 AND target_id = $2`, kind, targetID)
	return err
}

// DeleteByOrigin is the selective lift: the SCIM
// reactivation deletes only its own lane's rows, so admin/external locks
// survive any IdM write.
func (r revocationRepo) DeleteByOrigin(ctx context.Context, kind, targetID, origin string) error {
	_, err := r.s.exec(ctx, `DELETE FROM revocations WHERE kind = $1 AND target_id = $2 AND origin = $3`,
		kind, targetID, origin)
	return err
}

func (r revocationRepo) ListByTarget(ctx context.Context, kind, targetID string) ([]Revocation, error) {
	return r.revocations(ctx, `SELECT `+revocationCols+` FROM revocations
		WHERE kind = $1 AND target_id = $2 ORDER BY created_at`, kind, targetID)
}

// ListByTargets is the page-batched sibling of ListByTarget (the users list
// must never do per-row lock lookups). Targets with no rows are absent.
func (r revocationRepo) ListByTargets(ctx context.Context, kind string, targetIDs []string) (map[string][]Revocation, error) {
	out := map[string][]Revocation{}
	if len(targetIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(targetIDs)+1)
	args = append(args, kind)
	ph := make([]string, len(targetIDs))
	for i, id := range targetIDs {
		ph[i] = "$" + strconv.Itoa(i+2)
		args = append(args, id)
	}
	rows, err := r.revocations(ctx, `SELECT `+revocationCols+` FROM revocations
		WHERE kind = $1 AND target_id IN (`+strings.Join(ph, ", ")+`) ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	for _, rv := range rows {
		out[rv.TargetID] = append(out[rv.TargetID], rv)
	}
	return out, nil
}

func (r revocationRepo) revocations(ctx context.Context, query string, args ...any) ([]Revocation, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Revocation
	for rows.Next() {
		rv, err := scanRevocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rv)
	}
	return out, rows.Err()
}
