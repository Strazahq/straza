package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// draftPublishLockKey is the advisory-lock key that serializes publishes and
// the policy conversion's settles across replicas on Postgres, an arbitrary
// constant beside auditChainLockKey.
const draftPublishLockKey = 74218502

// pausedKey is the settings key of the manager's admin-pause set, the JSON
// array of the names of the servers it keeps stopped.
const pausedKey = "manager.paused"

// The kinds and operations of a plan item, as the drafts tables spell them.
const (
	kindApp       = "App"
	kindRole      = "Role"
	kindPolicySet = "PolicySet"
	opPut         = "put"
	opOff         = "off"
	opRemove      = "remove"
)

// Error says in plain words why the publish wrote nothing.
func (c PublishConflict) Error() string {
	const why = "store: the publish wrote nothing, because "
	switch {
	case c.Draft:
		return why + "the draft is no longer open at the revision that was checked"
	case c.Generation:
		return why + "another publish changed live config after the check read it"
	case c.Busy:
		return why + "the database turned the transaction away while other changes were written"
	case c.Cascade:
		return why + "what goes with a removal changed after the check read it, starting with " + c.Object.Kind + "/" + c.Object.Name
	case c.Object != (ObjectRef{}):
		return why + c.Object.Kind + "/" + c.Object.Name + " changed after the check read it"
	case c.Snapshot == "":
		return why + "no policy snapshot is active, and the check read one"
	}
	return why + "the active policy snapshot is " + c.Snapshot + ", not the one the check read"
}

// Unwrap answers ErrConflict, so that every PublishConflict is one.
func (c PublishConflict) Unwrap() error { return ErrConflict }

func (r draftRepo) Publish(ctx context.Context, plan PublishPlan) (PublishResult, error) {
	if err := checkPlan(plan); err != nil {
		return PublishResult{}, err
	}
	var res PublishResult
	err := r.inTx(ctx, func(tx *sql.Tx) error {
		p := &publishTx{r: r, tx: tx, plan: plan, at: now()}
		var err error
		res, err = p.run(ctx)
		return err
	})
	if isBusy(err) {
		return PublishResult{}, PublishConflict{Busy: true}
	}
	if err != nil {
		return PublishResult{}, err
	}
	return res, nil
}

// inTx runs fn in one transaction and commits when fn answers nil. The
// deferred rollback ends the transaction on every other way out, a panic
// in the plan's Records callback included, so no exit keeps the publish
// lock on Postgres or sqlite's one connection.
func (r draftRepo) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return r.s.mapErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return r.s.mapErr(tx.Commit())
}

// isBusy reports whether Postgres turned a transaction away as a
// serialization failure or a deadlock, which a later run can pass.
func isBusy(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

// isForeignKey reports whether a write met a foreign key on Postgres: a
// row it points at went after the publish resolved its name.
func isForeignKey(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// checkPlan refuses a plan that Publish cannot write as given, before any
// statement runs: no records, no draft, a slot close without a slot, which
// would match every draft that has none, an object named twice, and an
// item Publish cannot read.
func checkPlan(plan PublishPlan) error {
	switch {
	case plan.Records == nil:
		return errors.New("store: the publish plan has no Records, and a publish writes its records with its rows")
	case plan.New == nil && plan.DraftID <= 0:
		return errors.New("store: the publish plan names no draft")
	}
	for _, c := range plan.Close {
		if c.Slot == "" {
			return errors.New("store: the publish plan closes a slot draft without naming the slot")
		}
	}
	seen := make(map[ObjectRef]bool, len(plan.Items))
	for _, it := range plan.Items {
		if seen[it.Ref] {
			return fmt.Errorf("store: the publish plan names %s/%s twice", it.Ref.Kind, it.Ref.Name)
		}
		seen[it.Ref] = true
		if why := checkItem(it); why != "" {
			return fmt.Errorf("store: the publish plan item %s/%s %s", it.Ref.Kind, it.Ref.Name, why)
		}
	}
	return nil
}

// checkItem answers what makes one plan item unwritable, "" when nothing
// does.
func checkItem(it PlanItem) string {
	ops := map[string]bool{opPut: true, opOff: true, opRemove: true}
	var doc bool
	switch it.Ref.Kind {
	case kindApp:
		doc = it.App != nil
	case kindRole:
		doc = it.Role != nil
	case kindPolicySet:
		doc = it.Policy != nil
	default:
		return "has a kind that is not App, Role or PolicySet"
	}
	switch {
	case !ops[it.Op]:
		return "has the operation " + strconv.Quote(it.Op) + ", which is not put, off or remove"
	case !ops[it.BaseOp]:
		return "has no base, so it was never checked"
	case it.Op == opOff && it.Ref.Kind != kindPolicySet:
		return "turns off an object that is not a PolicySet"
	case it.Implied && (it.Ref.Kind != kindRole || it.Op == opOff):
		return "is implied, and only a Role put or remove can be"
	case it.Op == opRemove && it.After != "":
		return "removes the object and leaves something behind, since its After is not empty"
	case !doc && !it.Implied && it.Op != opRemove:
		return "has no document of its kind to write"
	}
	return ""
}

// publishTx is one run of Publish on its transaction: the plan, what the
// checks read of every item, and the outcome the writes build.
type publishTx struct {
	r        draftRepo
	tx       *sql.Tx
	plan     PublishPlan
	at       time.Time
	revision int
	live     []liveObject
	gone     map[string]bool
	outcomes []*ItemOutcome
	out      PublishOutcome
}

// liveObject is one plan item's object as the transaction read it, with
// its fingerprint. A Role holds its access row with the server's name and
// the tools, and its edges by implied name with the implied role's id. An
// App remove holds the admin role that goes with the server and the
// roles whose access row on it goes.
type liveObject struct {
	fp      string
	app     *App
	role    *Role
	binding *ToolBinding
	server  string
	tools   []string
	edges   map[string]string
	set     *PolicySet
	admin   *Role
	lost    []string
}

// run is steps 1 to 11 of the publish, in order. Any error rolls every
// write back.
func (p *publishTx) run(ctx context.Context) (PublishResult, error) {
	if err := p.r.publishLock(ctx, p.tx); err != nil {
		return PublishResult{}, err
	}
	active, moved, err := p.activeSnapshot(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	if err := p.moveGeneration(ctx); err != nil {
		return PublishResult{}, err
	}
	if moved || active != p.plan.BaseSnapshot {
		return PublishResult{}, PublishConflict{Snapshot: active}
	}
	p.out.Snapshot = active
	for _, step := range []func(context.Context) error{
		p.openDraft, p.readLive, p.checkCascades, p.write, p.verifyAfter, p.closeSlots, p.recordHistory,
	} {
		if err := step(ctx); err != nil {
			return PublishResult{}, err
		}
	}
	n, err := p.insertRecords(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	if err := p.markPublished(ctx); err != nil {
		return PublishResult{}, err
	}
	return PublishResult{PublishOutcome: p.out, Records: n}, nil
}

// publishLock takes the publish lock on tx. On Postgres it is a
// transaction advisory lock that serializes publishes and settles across
// replicas. sqlite's one connection already serializes every transaction.
func (r draftRepo) publishLock(ctx context.Context, tx *sql.Tx) error {
	if r.s.d != dialectPostgres {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, draftPublishLockKey)
	return err
}

// forUpdate is the clause that locks the rows a check reads until the
// commit on Postgres, so that a writer outside the publish lock, such as
// the snapshot service, an older admin route or a membership write, waits
// instead of changing a row between its check and its write.
func (p *publishTx) forUpdate() string {
	if p.r.s.d == dialectPostgres {
		return " FOR UPDATE"
	}
	return ""
}

// activeSnapshot answers the active snapshot's id, "" when none is active,
// and whether it moved while the locked read waited. On Postgres a locked
// read that waited for another activation finds the row it waited for no
// longer active and answers no row, so the id is read again without the
// lock, and a row found then became active during the wait.
func (p *publishTx) activeSnapshot(ctx context.Context) (string, bool, error) {
	const read = `SELECT id FROM snapshots WHERE active = TRUE`
	var id string
	err := p.tx.QueryRowContext(ctx, p.r.s.q(read+p.forUpdate())).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) && p.r.s.d == dialectPostgres {
		if err = p.tx.QueryRowContext(ctx, p.r.s.q(read)).Scan(&id); err == nil {
			return id, true, nil
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, false, p.r.s.mapErr(err)
}

func (p *publishTx) moveGeneration(ctx context.Context) error {
	res, err := p.tx.ExecContext(ctx, p.r.s.q(`UPDATE config_generation SET generation = generation + 1
		WHERE id = 1 AND generation = $1`), p.plan.Generation)
	if err != nil {
		return p.r.s.mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return PublishConflict{Generation: true}
	}
	return nil
}

// openDraft inserts the plan's new draft, or checks that its draft is open
// at the plan's revision.
func (p *publishTx) openDraft(ctx context.Context) error {
	if n := p.plan.New; n != nil {
		d, err := p.r.create(ctx, p.tx, n.Row, n.Items, n.Rev)
		if err != nil {
			return err
		}
		p.out.DraftID, p.revision = d.ID, d.Revision
		return nil
	}
	var state string
	var revision int
	err := p.tx.QueryRowContext(ctx, p.r.s.q(`SELECT state, revision FROM drafts WHERE id = $1`+p.forUpdate()),
		p.plan.DraftID).Scan(&state, &revision)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return PublishConflict{Draft: true}
	case err != nil:
		return p.r.s.mapErr(err)
	case state != "open" || revision != p.plan.Revision:
		return PublishConflict{Draft: true}
	}
	p.out.DraftID, p.revision = p.plan.DraftID, p.plan.Revision
	return nil
}

// readLive reads every item's object and refuses the plan when one's live
// fingerprint is not the base its check saw.
func (p *publishTx) readLive(ctx context.Context) error {
	p.live = make([]liveObject, len(p.plan.Items))
	for i, it := range p.plan.Items {
		lo, err := p.readObject(ctx, it.Ref)
		if err != nil {
			return err
		}
		if lo.fp != it.Base {
			return PublishConflict{Object: it.Ref}
		}
		p.live[i] = lo
	}
	return nil
}

// verifyAfter reads every item's object again after the writes and
// refuses the publish when one does not read as the plan's After, so a
// plan the writes cannot carry out commits nothing and no change row
// records an after that no object has.
func (p *publishTx) verifyAfter(ctx context.Context) error {
	for _, it := range p.plan.Items {
		lo, err := p.readObject(ctx, it.Ref)
		if err != nil {
			return err
		}
		if lo.fp != it.After {
			return fmt.Errorf("store: the publish wrote nothing, because %s/%s does not read as the plan says after the writes, so the draft's check and the store disagree about this change",
				it.Ref.Kind, it.Ref.Name)
		}
	}
	return nil
}

// readObject reads the object ref names with the queries its fingerprint
// is defined over, a zero liveObject when it does not exist.
func (p *publishTx) readObject(ctx context.Context, ref ObjectRef) (liveObject, error) {
	var lo liveObject
	switch ref.Kind {
	case kindApp:
		a, err := scanApp(p.tx.QueryRowContext(ctx, p.r.s.q(`SELECT `+appCols+` FROM apps
			WHERE name = $1 AND deleted_at IS NULL`+p.forUpdate()), ref.Name))
		if errors.Is(err, ErrNotFound) {
			return lo, nil
		}
		if err != nil {
			return lo, err
		}
		lo.app = &a
		lo.fp, err = FingerprintApp(a.Manifest)
		return lo, err
	case kindRole:
		return p.readRole(ctx, ref.Name)
	}
	ps, err := scanPolicy(p.tx.QueryRowContext(ctx, p.r.s.q(`SELECT `+policyCols+` FROM policy_sets
		WHERE name = $1`+p.forUpdate()), ref.Name))
	if errors.Is(err, ErrNotFound) {
		return lo, nil
	}
	if err != nil {
		return lo, err
	}
	lo.set, lo.fp = &ps, FingerprintPolicySet(ps)
	return lo, nil
}

// readRole reads a role as the World does: the row, the name of the live
// server that owns it, its access row with the live server's name and the
// tools, a matcher that does not decode reading as none, and the names of
// the roles it implies.
func (p *publishTx) readRole(ctx context.Context, name string) (liveObject, error) {
	var lo liveObject
	ro, err := scanRole(p.tx.QueryRowContext(ctx, p.r.s.q(`SELECT `+roleCols+` FROM roles WHERE name = $1`+p.forUpdate()), name))
	if errors.Is(err, ErrNotFound) {
		return lo, nil
	}
	if err != nil {
		return lo, err
	}
	cfg := RoleConfig{Role: ro}
	if ro.OwnerAppID != "" {
		if cfg.Owner, err = p.one(ctx, `SELECT name FROM apps WHERE id = $1 AND deleted_at IS NULL`, ro.OwnerAppID); err != nil {
			return lo, err
		}
	}
	var b ToolBinding
	var ca scanTime
	err = p.tx.QueryRowContext(ctx, p.r.s.q(`SELECT b.id, b.role_id, b.app_id, b.tool_matcher, b.effect, b.created_at,
		COALESCE(a.name, '') FROM tool_bindings b LEFT JOIN apps a ON a.id = b.app_id AND a.deleted_at IS NULL
		WHERE b.role_id = $1`), ro.ID).Scan(&b.ID, &b.RoleID, &b.AppID, &b.ToolMatcher, &b.Effect, &ca, &cfg.Server)
	switch {
	case err == nil:
		b.CreatedAt = ca.t
		lo.binding = &b
		_ = json.Unmarshal([]byte(b.ToolMatcher), &cfg.Tools)
	case !errors.Is(err, sql.ErrNoRows):
		return lo, p.r.s.mapErr(err)
	}
	if lo.edges, err = p.pairs(ctx, `SELECT im.name, im.id FROM role_implications ri
		JOIN roles im ON im.id = ri.implies_role_id WHERE ri.role_id = $1`, ro.ID); err != nil {
		return lo, err
	}
	for implied := range lo.edges {
		cfg.Implies = append(cfg.Implies, implied)
	}
	lo.role, lo.server, lo.tools, lo.fp = &ro, cfg.Server, cfg.Tools, FingerprintRole(cfg)
	return lo, nil
}

// one answers the one text column query selects for arg, "" when it
// selects no row.
func (p *publishTx) one(ctx context.Context, query string, arg any) (string, error) {
	var v string
	err := p.tx.QueryRowContext(ctx, p.r.s.q(query), arg).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, p.r.s.mapErr(err)
}

// pairs answers the two text columns query selects for arg as a map from
// the first to the second, closing the rows before the next statement.
func (p *publishTx) pairs(ctx context.Context, query string, arg any) (map[string]string, error) {
	rows, err := p.tx.QueryContext(ctx, p.r.s.q(query), arg)
	if err != nil {
		return nil, p.r.s.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// checkCascades refuses a plan whose implied items are not what its
// removals take with them in the rows now, by the rule the draft's check
// used: every role a removed server owns goes, and every other role that
// the draft does not name and that loses an access row on a removed server
// or an edge to a role that goes changes. A removed server's admin role
// goes too, as part of the server's own item.
func (p *publishTx) checkCascades(ctx context.Context) error {
	named, implied := map[string]bool{}, map[string]string{}
	for _, it := range p.plan.Items {
		switch {
		case it.Ref.Kind != kindRole:
		case it.Implied:
			implied[it.Ref.Name] = it.Op
		default:
			named[it.Ref.Name] = true
		}
	}
	gone, owned, changed := map[string]string{}, map[string]bool{}, map[string]bool{}
	for i, it := range p.plan.Items {
		lo := &p.live[i]
		switch {
		case it.Op != opRemove || it.Implied:
		case lo.role != nil:
			gone[lo.role.ID] = lo.role.Name
		case lo.app != nil:
			if err := p.readRemoval(ctx, lo, gone, owned, changed); err != nil {
				return err
			}
		}
	}
	p.gone = map[string]bool{}
	for id, name := range gone {
		p.gone[name] = true
		losers, err := p.pairs(ctx, `SELECT r.name, r.id FROM role_implications ri
			JOIN roles r ON r.id = ri.role_id WHERE ri.implies_role_id = $1`, id)
		if err != nil {
			return err
		}
		for loser := range losers {
			changed[loser] = true
		}
	}
	want := map[string]string{}
	for name := range changed {
		if !named[name] && !p.gone[name] {
			want[name] = opPut
		}
	}
	for name := range owned {
		if !named[name] {
			want[name] = opRemove
		}
	}
	names := make([]string, 0, len(want)+len(implied))
	for name := range want {
		names = append(names, name)
	}
	for name := range implied {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if want[name] != implied[name] {
			return PublishConflict{Object: ObjectRef{kindRole, name}, Cascade: true}
		}
	}
	return nil
}

// readRemoval reads what goes with the removal of the live server lo.app:
// its admin role, the roles it owns, and the roles whose access row on it
// goes, in lo.lost sorted by name.
func (p *publishTx) readRemoval(ctx context.Context, lo *liveObject, gone map[string]string, owned, changed map[string]bool) error {
	admin, err := p.roleByID(ctx, lo.app.AdminRoleID)
	if err != nil {
		return err
	}
	if admin.ID != "" {
		lo.admin, gone[admin.ID] = &admin, admin.Name
	}
	own, err := p.pairs(ctx, `SELECT name, id FROM roles WHERE owner_app_id = $1`, lo.app.ID)
	if err != nil {
		return err
	}
	for name, id := range own {
		gone[id], owned[name] = name, true
	}
	access, err := p.pairs(ctx, `SELECT r.name, r.id FROM tool_bindings b JOIN roles r ON r.id = b.role_id
		WHERE b.app_id = $1`, lo.app.ID)
	if err != nil {
		return err
	}
	for name := range access {
		lo.lost = append(lo.lost, name)
		changed[name] = true
	}
	slices.Sort(lo.lost)
	return nil
}

// roleByID reads the role with id, locked on Postgres, and the zero Role
// when no role has it.
func (p *publishTx) roleByID(ctx context.Context, id string) (Role, error) {
	ro, err := scanRole(p.tx.QueryRowContext(ctx, p.r.s.q(`SELECT `+roleCols+` FROM roles WHERE id = $1`+p.forUpdate()), id))
	if errors.Is(err, ErrNotFound) {
		return Role{}, nil
	}
	return ro, err
}

// affectOne answers the error of a write that must change exactly one row.
// A row that went since the check read it is a conflict on ref.
func (p *publishTx) affectOne(res sql.Result, err error, ref ObjectRef) error {
	if err != nil {
		return p.r.s.mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return PublishConflict{Object: ref}
	}
	return nil
}

// sha256Hex answers the hex sha256 of text, the compiled_hash a set's row
// keeps of the text it was published with.
func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
