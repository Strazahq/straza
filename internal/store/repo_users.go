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

type userRepo struct{ s *sqlStore }

const userCols = "id, external_id, username, email, display, title, status, origin, password_hash, attrs, user_type, agency_mode, sponsor, swarm_id, ephemeral, entra_agent_id, created_at, updated_at, deleted_at"

// scanner abstracts *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanUser(row scanner) (User, error) {
	var u User
	var ca, ua scanTime
	var da scanTimePtr
	if err := row.Scan(&u.ID, &u.ExternalID, &u.Username, &u.Email, &u.Display, &u.Title,
		&u.Status, &u.Origin, &u.PasswordHash, &u.Attrs,
		&u.UserType, &u.AgencyMode, &u.Sponsor, &u.SwarmID, &u.Ephemeral, &u.EntraAgentID, &ca, &ua, &da); err != nil {
		return User{}, scanErr(err)
	}
	u.CreatedAt, u.UpdatedAt, u.DeletedAt = ca.t, ua.t, da.t
	return u, nil
}

func (r userRepo) Create(ctx context.Context, u User) (User, error) {
	u.ID = newID()
	u.CreatedAt = now()
	u.UpdatedAt = u.CreatedAt
	u.DeletedAt = nil
	if u.Status == "" {
		u.Status = UserActive
	}
	if u.Origin == "" {
		u.Origin = OriginLocal
	}
	if u.Attrs == "" {
		u.Attrs = "{}"
	}
	_, err := r.s.exec(ctx, `INSERT INTO users (id, external_id, username, email, display, title, status, origin, password_hash, attrs, user_type, agency_mode, sponsor, swarm_id, ephemeral, entra_agent_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		u.ID, u.ExternalID, u.Username, u.Email, u.Display, u.Title, u.Status, u.Origin,
		u.PasswordHash, u.Attrs, u.UserType, u.AgencyMode, u.Sponsor, u.SwarmID, u.Ephemeral,
		u.EntraAgentID, r.s.tArg(u.CreatedAt), r.s.tArg(u.UpdatedAt))
	if err != nil {
		return User{}, err
	}
	return u, nil
}

func (r userRepo) GetByID(ctx context.Context, id string) (User, error) {
	return scanUser(r.s.queryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE id = $1 AND deleted_at IS NULL`, id))
}

func (r userRepo) GetByUsername(ctx context.Context, username string) (User, error) {
	return scanUser(r.s.queryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE username = $1 AND deleted_at IS NULL`, username))
}

func (r userRepo) ListByEmail(ctx context.Context, email string) ([]User, error) {
	if email == "" {
		return nil, nil
	}
	rows, err := r.s.query(ctx,
		`SELECT `+userCols+` FROM users WHERE email = $1 AND deleted_at IS NULL
			ORDER BY created_at, id`, email)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r userRepo) GetByExternalID(ctx context.Context, externalID string) (User, error) {
	if externalID == "" {
		return User{}, ErrNotFound
	}
	return scanUser(r.s.queryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE external_id = $1 AND deleted_at IS NULL`, externalID))
}

// GetByEntraAgentID answers the live user that holds the Entra agent id,
// which the caller passes normalized. An empty id is ErrNotFound.
func (r userRepo) GetByEntraAgentID(ctx context.Context, id string) (User, error) {
	if id == "" {
		return User{}, ErrNotFound
	}
	return scanUser(r.s.queryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE entra_agent_id = $1 AND deleted_at IS NULL`, id))
}

func (r userRepo) GetByIDs(ctx context.Context, ids []string) ([]User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := r.s.query(ctx,
		`SELECT `+userCols+` FROM users WHERE id IN (`+strings.Join(ph, ",")+`) AND deleted_at IS NULL`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r userRepo) List(ctx context.Context) ([]User, error) {
	rows, err := r.s.query(ctx,
		`SELECT `+userCols+` FROM users WHERE deleted_at IS NULL ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// userSortKeys are the keys Page accepts: the id (newest first, the
// default), the username, the status, and the newest last_seen over the
// user's sessions, read through one grouped join so a page costs one pass
// over sessions and never a lookup per row. A user with no session sorts as
// the zero time, so ascending puts the never-seen first.
func userSortKeys(s *sqlStore) map[string]sortKey {
	return map[string]sortKey{
		"":          column("id"),
		"created":   column("id"),
		"name":      column("username"),
		"status":    column("status"),
		"last_seen": {isTime: true, expr: func(ph func(any) string) string { return "COALESCE(ls.last_seen, " + ph(s.tArg(time.Time{})) + ")" }},
	}
}

// Page returns limit users after the cursor in the order srt names,
// skipping soft-deleted rows like List, narrowed by f (UserFilter documents
// the semantics). The role lane matches direct
// assignments with the validity window evaluated in SQL, mirroring
// identity.assignmentValidAt (from inclusive, to exclusive); time
// comparison rides tArg exactly like sessions.CloseIdle, with the text
// timestamp caveat LastSeenByUsers accepts on SQLite.
func (r userRepo) Page(ctx context.Context, f UserFilter, srt Sort, c Cursor, limit int) ([]User, error) {
	key, ok := userSortKeys(r.s)[srt.Key]
	if !ok {
		return nil, ErrBadSort
	}
	args := []any{}
	ph := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	q := `SELECT ` + userCols + ` FROM users`
	if srt.Key == "last_seen" {
		q += ` LEFT JOIN (SELECT user_id, MAX(last_seen) AS last_seen FROM sessions GROUP BY user_id) ls ON ls.user_id = users.id`
	}
	q += ` WHERE deleted_at IS NULL`
	if f.Q != "" {
		needle := "%" + strings.ToLower(f.Q) + "%"
		q += ` AND (lower(username) LIKE ` + ph(needle) +
			` OR lower(email) LIKE ` + ph(needle) +
			` OR lower(external_id) LIKE ` + ph(needle) + `)`
	}
	if f.Status != "" {
		q += ` AND status = ` + ph(f.Status)
	}
	if f.Sponsor != "" {
		q += ` AND sponsor = ` + ph(f.Sponsor)
	}
	if len(f.RoleIDs) > 0 {
		nw := f.Now
		if nw.IsZero() {
			nw = now()
		}
		in := func() string {
			ps := make([]string, len(f.RoleIDs))
			for i, id := range f.RoleIDs {
				ps[i] = ph(id)
			}
			return strings.Join(ps, ", ")
		}
		window := func() string {
			return ` AND (ra.valid_from IS NULL OR ra.valid_from <= ` + ph(r.s.tArg(nw)) + `)` +
				` AND (ra.valid_to IS NULL OR ra.valid_to > ` + ph(r.s.tArg(nw)) + `)`
		}
		q += ` AND EXISTS (SELECT 1 FROM role_assignments ra` +
			` WHERE ra.subject_kind = ` + ph(SubjectUser) +
			` AND ra.subject_id = users.id AND ra.role_id IN (` + in() + `)` + window() + `)`
	}
	where, order, err := r.s.keyset(key, "id", srt, c, ph)
	if err != nil {
		return nil, err
	}
	q += where + order + ` LIMIT ` + ph(limit)
	rows, err := r.s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SponsoredCounts answers how many agents each named user sponsors in one
// grouped query over the page's usernames, so a list never counts per row.
// A user sponsoring nobody is absent from the map.
func (r userRepo) SponsoredCounts(ctx context.Context, usernames []string) (map[string]int, error) {
	out := map[string]int{}
	if len(usernames) == 0 {
		return out, nil
	}
	ph := make([]string, len(usernames))
	args := make([]any, len(usernames))
	for i, n := range usernames {
		ph[i] = "$" + strconv.Itoa(i+1)
		args[i] = n
	}
	rows, err := r.s.query(ctx, `SELECT sponsor, COUNT(*) FROM users WHERE deleted_at IS NULL
		AND sponsor IN (`+strings.Join(ph, ", ")+`) GROUP BY sponsor`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

// Update writes every column of the live user u and answers the row read
// back. An AI agent or a service account stores no password hash, because
// it signs in with its key, so a write that leaves u a non-person clears
// the hash, as a SCIM retype of a person does.
func (r userRepo) Update(ctx context.Context, u User) (User, error) {
	u.UpdatedAt = now()
	if u.IsNHI() {
		u.PasswordHash = ""
	}
	err := mustAffect(r.s.exec(ctx, `UPDATE users SET external_id = $1, username = $2, email = $3,
		display = $4, title = $5, status = $6, password_hash = $7, attrs = $8,
		user_type = $9, agency_mode = $10, sponsor = $11, swarm_id = $12, ephemeral = $13, entra_agent_id = $14, updated_at = $15
		WHERE id = $16 AND deleted_at IS NULL`,
		u.ExternalID, u.Username, u.Email, u.Display, u.Title, u.Status, u.PasswordHash,
		u.Attrs, u.UserType, u.AgencyMode, u.Sponsor, u.SwarmID, u.Ephemeral, u.EntraAgentID,
		r.s.tArg(u.UpdatedAt), u.ID))
	if err != nil {
		return User{}, err
	}
	return r.GetByID(ctx, u.ID)
}

// UserFields names the columns of a partial user write: a nil field leaves
// its column as it is.
type UserFields struct {
	Email, Display, Title, Status, PasswordHash *string
	UserType, AgencyMode, Sponsor, SwarmID      *string
	EntraAgentID                                *string
	Ephemeral                                   *bool
}

// ChangedUserFields answers the partial write that turns the row read into
// want: every column whose value differs between the two, and no other.
func ChangedUserFields(read, want User) UserFields {
	var f UserFields
	for _, c := range []struct {
		dst      **string
		was, now *string
	}{
		{&f.Email, &read.Email, &want.Email}, {&f.Display, &read.Display, &want.Display},
		{&f.Title, &read.Title, &want.Title}, {&f.Status, &read.Status, &want.Status},
		{&f.PasswordHash, &read.PasswordHash, &want.PasswordHash}, {&f.UserType, &read.UserType, &want.UserType},
		{&f.AgencyMode, &read.AgencyMode, &want.AgencyMode}, {&f.Sponsor, &read.Sponsor, &want.Sponsor},
		{&f.SwarmID, &read.SwarmID, &want.SwarmID}, {&f.EntraAgentID, &read.EntraAgentID, &want.EntraAgentID},
	} {
		if *c.was != *c.now {
			*c.dst = c.now
		}
	}
	if read.Ephemeral != want.Ephemeral {
		f.Ephemeral = &want.Ephemeral
	}
	return f
}

// UserRevocation is the revocation of a user that a user write carries in
// its own transaction: the reason and origin of the user's revocation row,
// and the straza.revocation.user event that tells the other servers, which
// the caller builds and the store bounds. An event that carries an id keeps
// it, so the caller can ask RevocationCommitted about the write.
type UserRevocation struct {
	Reason, Origin string
	Event          OutboxEvent
}

// UpdateFields sets only the columns f names, and updated_at with them, on
// the live user id and answers the row read back after the write, so two
// writes that name different columns never undo each other. An f that names
// no column, with a nil revoke, writes nothing and answers the row. A
// non-nil revoke writes the user's revocation row and its event in the
// transaction of the column write, so the database takes all of them or
// none, and the row is read back inside it. A missing or soft-deleted id is
// ErrNotFound.
func (r userRepo) UpdateFields(ctx context.Context, id string, f UserFields, revoke *UserRevocation) (User, error) {
	var set []string
	var args []any
	ph := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	for _, c := range []struct {
		col string
		v   *string
	}{
		{"email", f.Email}, {"display", f.Display}, {"title", f.Title}, {"status", f.Status},
		{"password_hash", f.PasswordHash}, {"user_type", f.UserType}, {"agency_mode", f.AgencyMode},
		{"sponsor", f.Sponsor}, {"swarm_id", f.SwarmID}, {"entra_agent_id", f.EntraAgentID},
	} {
		if c.v != nil {
			set = append(set, c.col+" = "+ph(*c.v))
		}
	}
	if f.Ephemeral != nil {
		set = append(set, "ephemeral = "+ph(*f.Ephemeral))
	}
	if len(set) == 0 && revoke == nil {
		return r.GetByID(ctx, id)
	}
	set = append(set, "updated_at = "+ph(r.s.tArg(now())))
	where := ` WHERE id = ` + ph(id) + ` AND deleted_at IS NULL`
	update := `UPDATE users SET ` + strings.Join(set, ", ") + where
	if revoke == nil {
		if err := mustAffect(r.s.exec(ctx, update, args...)); err != nil {
			return User{}, err
		}
		return r.GetByID(ctx, id)
	}
	ev := revoke.Event
	ev.CE = boundRecord(ev.CE)
	var u User
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, r.s.q(update), args...)
		if err := mustAffect(res, r.s.mapErr(err)); err != nil {
			return err
		}
		if err := r.s.writeRevocation(ctx, tx, RevokeUser, []string{id}, revoke.Reason, revoke.Origin, ev); err != nil {
			return err
		}
		// A read back after the commit could fail on a change that
		// committed, and the caller would then report a revoke that holds as
		// one that did not happen.
		u, err = scanUser(tx.QueryRowContext(ctx, r.s.q(`SELECT `+userCols+` FROM users WHERE id = $1 AND deleted_at IS NULL`), id))
		return err
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// SoftDelete marks the user deleted and disabled and deletes its role
// assignment rows in the same transaction. No getter revives a soft-deleted
// user, so a row that kept its grants would stay a ghost in every assignments
// read; the removed rows come back so the caller can chain one audit record
// per ended grant. A non-nil revoke writes the user's revocation row and its
// event in that transaction too. A missing or already deleted id is
// ErrNotFound and the transaction rolls back before any assignment row is
// touched.
func (r userRepo) SoftDelete(ctx context.Context, id string, revoke *UserRevocation) ([]RoleAssignment, error) {
	t := r.s.tArg(now())
	var ev OutboxEvent
	if revoke != nil {
		ev = revoke.Event
		ev.CE = boundRecord(ev.CE)
	}
	var removed []RoleAssignment
	err := r.s.tx(ctx, func(tx *sql.Tx) error {
		if err := mustAffect(tx.ExecContext(ctx, r.s.q(`UPDATE users SET deleted_at = $1, status = 'disabled', updated_at = $2
			WHERE id = $3 AND deleted_at IS NULL`), t, t, id)); err != nil {
			return r.s.mapErr(err)
		}
		var err error
		if removed, err = heldAssignments(ctx, tx, r.s, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, r.s.q(`DELETE FROM role_assignments WHERE subject_kind = $1 AND subject_id = $2`), SubjectUser, id)
		if err != nil || revoke == nil {
			return r.s.mapErr(err)
		}
		return r.s.writeRevocation(ctx, tx, RevokeUser, []string{id}, revoke.Reason, revoke.Origin, ev)
	})
	if err != nil {
		return nil, err
	}
	return removed, nil
}

// RevocationCommitted reports whether the user write that carried the
// revocation event eventID committed, for a writer whose write ended with an
// error that leaves the commit open. Only that write holds the event's id,
// so the answer tells it apart from another write that disabled the same
// user. It also answers the user's row as stored, soft-deleted after a
// delete. A user with no row is ErrNotFound. It writes nothing and rolls
// back, so it never waits on a commit of its own.
func (r userRepo) RevocationCommitted(ctx context.Context, userID, eventID string) (User, bool, error) {
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, false, r.s.mapErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	read := `SELECT ` + userCols + ` FROM users WHERE id = $1`
	if r.s.d == dialectPostgres {
		// The write that lost its answer may still hold the row, as a commit
		// does while it waits for a synchronous standby, and its event shows
		// only once it ends. The share lock waits for that, so the event read
		// after it sees the write's outcome. SQLite serializes every
		// statement on its one connection, so the read alone holds there.
		read += ` FOR SHARE`
	}
	u, err := scanUser(tx.QueryRowContext(ctx, r.s.q(read), userID))
	if err != nil {
		return User{}, false, r.s.mapErr(err)
	}
	var one int
	switch err := tx.QueryRowContext(ctx, r.s.q(`SELECT 1 FROM events_outbox WHERE id = $1`), eventID).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return u, false, nil
	case err != nil:
		return User{}, false, r.s.mapErr(err)
	}
	return u, true, nil
}

// heldAssignments reads the user's assignment rows inside the delete
// transaction and closes the cursor before the next statement runs on it.
func heldAssignments(ctx context.Context, tx *sql.Tx, s *sqlStore, userID string) ([]RoleAssignment, error) {
	rows, err := tx.QueryContext(ctx, s.q(`SELECT `+assignmentCols+` FROM role_assignments
		WHERE subject_kind = $1 AND subject_id = $2 ORDER BY created_at`), SubjectUser, userID)
	if err != nil {
		return nil, s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]RoleAssignment, 0)
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
