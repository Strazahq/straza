package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// write runs the writes of the plan in this order: servers put, role rows,
// role links, role removals, server removals, sets, then the snapshot, so
// that every name a later write resolves exists and a removal meets the
// roles it takes with it. An item whose After equals its Base writes
// nothing and gets no outcome. A unique key a write meets is a row of the
// item's name, or an edge it adds, that appeared after the check read the
// rows, and a foreign key is a row it points at that went since, so either
// answers the item's conflict.
func (p *publishTx) write(ctx context.Context) error {
	p.outcomes = make([]*ItemOutcome, len(p.plan.Items))
	for i, it := range p.plan.Items {
		if it.After == it.Base {
			continue
		}
		// A removal and an implied put change a live object, which the
		// fingerprint and cascade checks found, and no write reads through
		// an object that is not there.
		if lo := p.live[i]; (it.Op == opRemove || it.Implied) && lo.app == nil && lo.role == nil && lo.set == nil {
			return PublishConflict{Object: it.Ref}
		}
		p.outcomes[i] = &ItemOutcome{Ref: it.Ref, Op: it.Op, Implied: it.Implied}
	}
	for _, phase := range []func(context.Context, int) error{
		p.putApp, p.putRoleRow, p.putRoleLinks, p.removeRole, p.removeApp, p.writeSet,
	} {
		for i, out := range p.outcomes {
			if out == nil {
				continue
			}
			err := phase(ctx, i)
			var moved PublishConflict
			if !errors.As(err, &moved) && (errors.Is(err, ErrConflict) || isForeignKey(err)) {
				return PublishConflict{Object: p.plan.Items[i].Ref}
			}
			if err != nil {
				return err
			}
		}
	}
	for _, out := range p.outcomes {
		if out != nil {
			p.out.Items = append(p.out.Items, *out)
		}
	}
	return p.activateSnapshot(ctx)
}

// putApp writes a server put: the config columns of a live row, whose
// status the health loop owns, or a new row with status pending and its
// admin role, which replaces the removed row of the name when there is one.
func (p *publishTx) putApp(ctx context.Context, i int) error {
	it, lo, out := p.plan.Items[i], &p.live[i], p.outcomes[i]
	if it.Ref.Kind != kindApp || it.Op != opPut {
		return nil
	}
	a := *it.App
	a.Name = it.Ref.Name
	if a.Manifest == "" {
		a.Manifest = "{}"
	}
	if a.Source == "" {
		a.Source = AppSourceAPI
	}
	if lo.app != nil {
		res, err := p.tx.ExecContext(ctx, p.r.s.q(`UPDATE apps SET version = $1, manifest = $2, runtime_kind = $3,
			source = $4, updated_at = $5 WHERE id = $6 AND deleted_at IS NULL`),
			a.Version, a.Manifest, a.RuntimeKind, a.Source, p.r.s.tArg(p.at), lo.app.ID)
		if err := p.affectOne(res, err, it.Ref); err != nil {
			return err
		}
		out.ID = lo.app.ID
		out.AdminRole, err = p.roleByID(ctx, lo.app.AdminRoleID)
		return err
	}
	id, admin, creds, err := p.reviveApp(ctx, a)
	if err != nil {
		return err
	}
	out.ID, out.AdminRole, out.Created, out.Credentials = id, admin, true, creds
	return p.dropPause(ctx, a.Name)
}

// reviveApp inserts the new row of a's name with a fresh id and a minted
// admin role, after deleting the removed row of that name when there is
// one, and answers the id, the admin role and how many credential rows the
// removed row still carried. A server published again under a removed
// name is a new server, so nothing written under the old id, a grant a
// connect stored after the removal included, reaches it.
func (p *publishTx) reviveApp(ctx context.Context, a App) (string, Role, int, error) {
	apps := appRepo{p.r.s}
	old, err := p.one(ctx, `SELECT id FROM apps WHERE name = $1 AND deleted_at IS NOT NULL`+p.forUpdate(), a.Name)
	if err != nil {
		return "", Role{}, 0, err
	}
	var creds int64
	if old != "" {
		// The removal deleted every credential row it saw, so any row left
		// on the removed server came after it. Its count is the outcome's;
		// the row's delete takes it and the access rows with it through
		// their foreign keys, and a write that still names the old id then
		// meets no row.
		res, err := p.tx.ExecContext(ctx, p.r.s.q(`DELETE FROM credentials WHERE app_id = $1`), old)
		if err != nil {
			return "", Role{}, 0, p.r.s.mapErr(err)
		}
		if creds, err = res.RowsAffected(); err != nil {
			return "", Role{}, 0, err
		}
		res, err = p.tx.ExecContext(ctx, p.r.s.q(`DELETE FROM apps WHERE id = $1`), old)
		if err := p.affectOne(res, err, ObjectRef{kindApp, a.Name}); err != nil {
			return "", Role{}, 0, err
		}
	}
	role, err := apps.mintAdminRole(ctx, p.tx, a.Name)
	if err != nil {
		return "", Role{}, 0, err
	}
	id := newID()
	_, err = p.tx.ExecContext(ctx, p.r.s.q(`INSERT INTO apps (id, name, version, manifest, runtime_kind, status, source,
		admin_role_id, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, $8, $9)`),
		id, a.Name, a.Version, a.Manifest, a.RuntimeKind, a.Source, role.ID, p.r.s.tArg(p.at), p.r.s.tArg(p.at))
	return id, role, int(creds), p.r.s.mapErr(err)
}

// dropPause takes name out of the pause set, as the manager does when a
// server is removed or installed under a new name, so that a server
// published under a name starts afresh. The set's row is read for update,
// so a pause another replica writes meanwhile is kept, not overwritten. A
// missing set holds nothing, and a set that does not decode fails the
// publish, as it fails the manager's start.
func (p *publishTx) dropPause(ctx context.Context, name string) error {
	raw, err := p.one(ctx, `SELECT value FROM settings WHERE key = $1`+p.forUpdate(), pausedKey)
	if err != nil || raw == "" {
		return err
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return fmt.Errorf("store: the pause set under the settings key %s does not decode, so nothing was published: %w", pausedKey, err)
	}
	kept := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == name })
	if len(kept) == len(names) {
		return nil
	}
	slices.Sort(kept)
	b, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	_, err = p.tx.ExecContext(ctx, p.r.s.q(`UPDATE settings SET value = $1, updated_at = CURRENT_TIMESTAMP WHERE key = $2`),
		string(b), pausedKey)
	return p.r.s.mapErr(err)
}

// putRoleRow writes a role put's row: a new role with its owner resolved
// by name, kind straza stored as kind business on the control plane as the
// create route stores it, or a changed description. The kind and name of a
// live role never change. An implied put writes nothing here, because its
// change is the effect of the removal that implies it, and it names the
// roles that go which it implied.
func (p *publishTx) putRoleRow(ctx context.Context, i int) error {
	it, lo, out := p.plan.Items[i], &p.live[i], p.outcomes[i]
	if it.Ref.Kind != kindRole || it.Op != opPut {
		return nil
	}
	if it.Implied {
		out.ID = lo.role.ID
		for _, name := range sortedKeys(lo.edges) {
			if p.gone[name] {
				out.ImpliesRemoved = append(out.ImpliesRemoved, name)
			}
		}
		return nil
	}
	want := it.Role.Role
	if lo.role != nil {
		out.ID = lo.role.ID
		if lo.role.Description == want.Description {
			return nil
		}
		res, err := p.tx.ExecContext(ctx, p.r.s.q(`UPDATE roles SET description = $1, updated_at = $2 WHERE id = $3`),
			want.Description, p.r.s.tArg(p.at), lo.role.ID)
		return p.affectOne(res, err, it.Ref)
	}
	role := Role{ID: newID(), Name: it.Ref.Name, Description: want.Description, Kind: want.Kind, Plane: RolePlaneAccess,
		CreatedAt: p.at, UpdatedAt: p.at}
	if role.Kind == roleKindStraza || want.Plane == RolePlaneControl {
		role.Kind, role.Plane = RoleKindBusiness, RolePlaneControl
	}
	if role.Kind == "" {
		role.Kind = RoleKindBusiness
	}
	if owner := it.Role.Owner; owner != "" {
		id, err := p.one(ctx, `SELECT id FROM apps WHERE name = $1 AND deleted_at IS NULL`, owner)
		if err != nil {
			return err
		}
		if id == "" {
			return PublishConflict{Object: it.Ref}
		}
		role.OwnerAppID = id
	}
	if err := (roleRepo{p.r.s}).insertRole(ctx, p.tx, role); err != nil {
		return err
	}
	lo.role = &role
	out.ID, out.Created = role.ID, true
	return nil
}

// putRoleLinks writes an explicit role put's access row and edges. The row
// is replaced with a new id only when its server or its tools differ from
// the live row. The edges the item drops are deleted and the ones it adds
// are inserted, their names resolved inside the transaction.
func (p *publishTx) putRoleLinks(ctx context.Context, i int) error {
	it, lo, out := p.plan.Items[i], &p.live[i], p.outcomes[i]
	if it.Ref.Kind != kindRole || it.Op != opPut || it.Implied {
		return nil
	}
	cfg := it.Role
	if cfg.Server != lo.server || !slices.Equal(sortedNames(cfg.Tools), sortedNames(lo.tools)) {
		if lo.binding != nil {
			if _, err := p.tx.ExecContext(ctx, p.r.s.q(`DELETE FROM tool_bindings WHERE role_id = $1`), lo.role.ID); err != nil {
				return p.r.s.mapErr(err)
			}
			out.BindingsRemoved = []ToolBinding{*lo.binding}
		}
		if cfg.Server != "" {
			b, err := p.insertAccess(ctx, it.Ref, lo.role.ID, cfg)
			if err != nil {
				return err
			}
			out.BindingsAdded = []ToolBinding{b}
		}
	}
	keep := make(map[string]bool, len(cfg.Implies))
	for _, name := range cfg.Implies {
		keep[name] = true
	}
	for _, name := range sortedKeys(lo.edges) {
		if keep[name] {
			continue
		}
		if _, err := p.tx.ExecContext(ctx, p.r.s.q(`DELETE FROM role_implications WHERE role_id = $1 AND implies_role_id = $2`),
			lo.role.ID, lo.edges[name]); err != nil {
			return p.r.s.mapErr(err)
		}
		out.ImpliesRemoved = append(out.ImpliesRemoved, name)
	}
	for _, name := range sortedKeys(keep) {
		if _, held := lo.edges[name]; held {
			continue
		}
		id, err := p.one(ctx, `SELECT id FROM roles WHERE name = $1`, name)
		if err != nil {
			return err
		}
		if id == "" {
			return PublishConflict{Object: it.Ref}
		}
		if _, err := p.tx.ExecContext(ctx, p.r.s.q(`INSERT INTO role_implications (role_id, implies_role_id) VALUES ($1, $2)`),
			lo.role.ID, id); err != nil {
			return p.r.s.mapErr(err)
		}
		out.ImpliesAdded = append(out.ImpliesAdded, name)
	}
	return nil
}

// insertAccess inserts the access row cfg names for the role, on the live
// server of its name, with a new id.
func (p *publishTx) insertAccess(ctx context.Context, ref ObjectRef, roleID string, cfg *RoleConfig) (ToolBinding, error) {
	appID, err := p.one(ctx, `SELECT id FROM apps WHERE name = $1 AND deleted_at IS NULL`, cfg.Server)
	if err != nil {
		return ToolBinding{}, err
	}
	if appID == "" {
		return ToolBinding{}, PublishConflict{Object: ref}
	}
	matcher, err := json.Marshal(append([]string{}, cfg.Tools...))
	if err != nil {
		return ToolBinding{}, err
	}
	b := ToolBinding{ID: newID(), RoleID: roleID, AppID: appID, ToolMatcher: string(matcher), Effect: "allow", CreatedAt: p.at}
	_, err = p.tx.ExecContext(ctx, p.r.s.q(`INSERT INTO tool_bindings (id, role_id, app_id, tool_matcher, effect, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`), b.ID, b.RoleID, b.AppID, b.ToolMatcher, b.Effect, p.r.s.tArg(b.CreatedAt))
	return b, p.r.s.mapErr(err)
}

// removeRole deletes a removed role, explicit or implied, and names the
// memberships that ended with it.
func (p *publishTx) removeRole(ctx context.Context, i int) error {
	it, lo, out := p.plan.Items[i], &p.live[i], p.outcomes[i]
	if it.Ref.Kind != kindRole || it.Op != opRemove {
		return nil
	}
	ended, err := p.endRole(ctx, *lo.role, it.Ref)
	out.ID, out.Ended, out.Removed = lo.role.ID, ended, []Role{*lo.role}
	return err
}

// endRole reads the role's direct memberships and deletes its row, whose
// foreign keys end those memberships, its edges, its access row and its
// pack bindings. The row was locked when it was read, so no membership
// lands between the read and the delete.
func (p *publishTx) endRole(ctx context.Context, role Role, ref ObjectRef) ([]RoleAssignment, error) {
	ended, err := roleRepo{p.r.s}.txAssignments(ctx, p.tx, `SELECT `+assignmentCols+` FROM role_assignments
		WHERE role_id = $1 ORDER BY created_at`, role.ID)
	if err != nil {
		return nil, err
	}
	res, err := p.tx.ExecContext(ctx, p.r.s.q(`DELETE FROM roles WHERE id = $1`), role.ID)
	return ended, p.affectOne(res, err, ref)
}

// removeApp removes a server: its access rows and credential rows go, the
// row is soft-deleted, its admin role goes with its memberships, and
// its name leaves the pause set.
func (p *publishTx) removeApp(ctx context.Context, i int) error {
	it, lo, out := p.plan.Items[i], &p.live[i], p.outcomes[i]
	if it.Ref.Kind != kindApp || it.Op != opRemove {
		return nil
	}
	out.ID, out.LostAccess = lo.app.ID, lo.lost
	if _, err := p.tx.ExecContext(ctx, p.r.s.q(`DELETE FROM tool_bindings WHERE app_id = $1`), lo.app.ID); err != nil {
		return p.r.s.mapErr(err)
	}
	res, err := p.tx.ExecContext(ctx, p.r.s.q(`DELETE FROM credentials WHERE app_id = $1`), lo.app.ID)
	if err != nil {
		return p.r.s.mapErr(err)
	}
	creds, err := res.RowsAffected()
	if err != nil {
		return err
	}
	out.Credentials = int(creds)
	res, err = p.tx.ExecContext(ctx, p.r.s.q(`UPDATE apps SET deleted_at = $1, status = 'stopped', updated_at = $2
		WHERE id = $3 AND deleted_at IS NULL`), p.r.s.tArg(p.at), p.r.s.tArg(p.at), lo.app.ID)
	if err := p.affectOne(res, err, it.Ref); err != nil {
		return err
	}
	if lo.admin != nil {
		ended, err := p.endRole(ctx, *lo.admin, it.Ref)
		if err != nil {
			return err
		}
		out.AdminRole, out.Ended, out.Removed = *lo.admin, ended, []Role{*lo.admin}
	}
	return p.dropPause(ctx, lo.app.Name)
}

// writeSet writes a set: a put stores it on with its text, its priority and
// the sha256 of the text as compiled_hash, an off stores it off with its
// text and keeps compiled_hash, and a remove deletes the row.
func (p *publishTx) writeSet(ctx context.Context, i int) error {
	it, lo, out := p.plan.Items[i], &p.live[i], p.outcomes[i]
	if it.Ref.Kind != kindPolicySet {
		return nil
	}
	was := lo.set
	if was != nil {
		out.ID, out.WasOn = was.ID, was.Status == "active"
	}
	if it.Op == opRemove {
		res, err := p.tx.ExecContext(ctx, p.r.s.q(`DELETE FROM policy_sets WHERE name = $1`), it.Ref.Name)
		return p.affectOne(res, err, it.Ref)
	}
	ps := it.Policy
	status, hash := "draft", ""
	switch {
	case it.Op == opPut:
		status, hash = "active", sha256Hex(ps.YAMLSource)
	case was != nil:
		hash = was.CompiledHash
	}
	out.On, out.TextChanged = it.Op == opPut, was == nil || was.YAMLSource != ps.YAMLSource
	at := p.r.s.tArg(p.at)
	if was == nil {
		out.ID, out.Created = newID(), true
		_, err := p.tx.ExecContext(ctx, p.r.s.q(`INSERT INTO policy_sets (id, name, priority, yaml_source, compiled_hash, status,
			created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`),
			out.ID, it.Ref.Name, ps.Priority, ps.YAMLSource, hash, status, at, at)
		return p.r.s.mapErr(err)
	}
	res, err := p.tx.ExecContext(ctx, p.r.s.q(`UPDATE policy_sets SET priority = $1, yaml_source = $2, compiled_hash = $3,
		status = $4, updated_at = $5 WHERE id = $6`), ps.Priority, ps.YAMLSource, hash, status, at, was.ID)
	return p.affectOne(res, err, it.Ref)
}

// activateSnapshot stores the plan's snapshot, whose content-addressed id
// may be stored already, and makes it the one active snapshot. Without one
// the base stays active.
func (p *publishTx) activateSnapshot(ctx context.Context) error {
	sn := p.plan.Snapshot
	if sn == nil {
		return nil
	}
	if _, err := p.tx.ExecContext(ctx, p.r.s.q(`INSERT INTO snapshots (id, signer_key_id, size, blob, active, created_at)
		VALUES ($1, $2, $3, $4, FALSE, $5) ON CONFLICT (id) DO NOTHING`),
		sn.ID, sn.SignerKeyID, int64(len(sn.Blob)), sn.Blob, p.r.s.tArg(p.at)); err != nil {
		return p.r.s.mapErr(err)
	}
	if _, err := p.tx.ExecContext(ctx, p.r.s.q(`UPDATE snapshots SET active = (id = $1)`), sn.ID); err != nil {
		return p.r.s.mapErr(err)
	}
	p.out.Snapshot = sn.ID
	return nil
}

// closeSlots ends the open draft of every slot the plan closes, other than
// the draft being published, as discarded by the publisher with the reason
// given. A slot draft another replica opened a moment before is closed
// too, because the statement runs inside the transaction.
func (p *publishTx) closeSlots(ctx context.Context) error {
	by := p.plan.Publisher
	for _, c := range p.plan.Close {
		rows, err := p.tx.QueryContext(ctx, p.r.s.q(`UPDATE drafts SET state = 'discarded', decided_at = $1,
			decided_by_id = $2, decided_by_name = $3, decided_via = $4, decided_client = $5, decided_reason = $6,
			updated_at = $7 WHERE slot = $8 AND state = 'open' AND id <> $9 RETURNING id, revision`),
			p.r.s.tArg(p.at), by.ID, by.Name, by.Via, by.Client, c.Reason, p.r.s.tArg(p.at), c.Slot, p.out.DraftID)
		if err != nil {
			return p.r.s.mapErr(err)
		}
		for rows.Next() {
			d := ClosedDraft{Reason: c.Reason}
			if err := rows.Scan(&d.ID, &d.Revision); err != nil {
				_ = rows.Close()
				return err
			}
			p.out.Closed = append(p.out.Closed, d)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return p.r.s.mapErr(err)
		}
	}
	return nil
}

// recordHistory writes one change row per item that wrote something, in
// plan order: the before from the base the check saw and the after from
// the plan.
func (p *publishTx) recordHistory(ctx context.Context) error {
	seq := 0
	for i, it := range p.plan.Items {
		if p.outcomes[i] == nil {
			continue
		}
		seq++
		if _, err := p.tx.ExecContext(ctx, p.r.s.q(`INSERT INTO draft_changes (draft_id, seq, kind, name, implied,
			before_op, before_doc, before_fp, after_op, after_doc, after_fp) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`),
			p.out.DraftID, seq, it.Ref.Kind, it.Ref.Name, it.Implied, it.BaseOp, it.BaseDoc, it.Base,
			it.Op, it.AfterDoc, it.After); err != nil {
			return p.r.s.mapErr(err)
		}
	}
	return nil
}

// recordChunk bounds the rows of one outbox insert, 4 parameters a row, far
// inside the Postgres limit of 65,535 parameters.
const recordChunk = 200

// insertRecords inserts the outbox rows the plan's Records makes of the
// outcome, each created_at one microsecond past the one before, as
// InsertBatch steps them, so the drain delivers them in the order given.
func (p *publishTx) insertRecords(ctx context.Context) (int, error) {
	events, err := p.plan.Records(p.out)
	if err != nil {
		return 0, fmt.Errorf("store: the records of the publish could not be made, so nothing was published: %w", err)
	}
	if len(events) == 0 {
		return 0, errors.New("store: the records of the publish made no record, and the other replicas apply a publish from its records, so nothing was published")
	}
	base := now()
	for at := 0; at < len(events); at += recordChunk {
		end := min(at+recordChunk, len(events))
		values := make([]string, 0, end-at)
		args := make([]any, 0, 4*(end-at))
		for i := at; i < end; i++ {
			e := events[i]
			if e.ID == "" {
				e.ID = newID()
			}
			// The extra nanosecond makes RFC3339Nano write all nine digits,
			// so sqlite's text order is the time order, as in InsertBatch.
			created := base.Add(time.Duration(i)*time.Microsecond + time.Nanosecond)
			n := 4 * (i - at)
			values = append(values, fmt.Sprintf("($%d, $%d, $%d, FALSE, 0, $%d)", n+1, n+2, n+3, n+4))
			args = append(args, e.ID, e.Subject, e.CE, p.r.s.tArg(created))
		}
		if _, err := p.tx.ExecContext(ctx, p.r.s.q(`INSERT INTO events_outbox (id, subject, ce, published, attempts, created_at)
			VALUES `+strings.Join(values, ", ")), args...); err != nil {
			return 0, p.r.s.mapErr(err)
		}
	}
	return len(events), nil
}

// markPublished marks the draft published by the publisher, with the
// active snapshot after the publish, the acknowledgments and, when the
// plan carries it, the check the publish ran as the check of the revision.
// A draft that left the checked revision in the meantime is the draft's
// conflict.
func (p *publishTx) markPublished(ctx context.Context) error {
	by := p.plan.Publisher
	set := `state = 'published', decided_at = $1, decided_by_id = $2, decided_by_name = $3, decided_via = $4,
		decided_client = $5, published_snapshot = $6, acks = $7, updated_at = $8`
	args := []any{p.r.s.tArg(p.at), by.ID, by.Name, by.Via, by.Client, p.out.Snapshot, p.plan.Acks, p.r.s.tArg(p.at)}
	where := ` WHERE id = $9 AND state = 'open' AND revision = $10`
	if c := p.plan.Checked; c != nil {
		set += `, checked_revision = $9, checked_at = $10, checked_snapshot = $11, check_counts = $12, agent_verdict = $13`
		args = append(args, p.revision, p.r.s.tArg(c.At), c.Snapshot, c.Counts, c.AgentVerdict)
		where = ` WHERE id = $14 AND state = 'open' AND revision = $15`
	}
	res, err := p.tx.ExecContext(ctx, p.r.s.q(`UPDATE drafts SET `+set+where), append(args, p.out.DraftID, p.revision)...)
	if err != nil {
		return p.r.s.mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return PublishConflict{Draft: true}
	}
	return nil
}

// sortedKeys answers the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
