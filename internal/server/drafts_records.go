package server

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// publishFields are the fields of the draft.publish record, which only the
// drafts route writes: the revision published and its items,
// the risk digest now and the one the publisher reviewed, the keys of the
// risks acknowledged by a tick and by typing, never a typed text, the
// proposer, the client the publisher used, and the draft an undo reverses.
type publishFields struct {
	Revision                   int
	Items                      []drafts.Item
	RiskDigest, ReviewedDigest string
	Acknowledged, Typed        []string
	Proposer                   store.DraftActor
	Client, Reverts            string
}

// recordContext is what the records of a publish read beside what Publish
// wrote, all of it known before the transaction opens: the live state the
// check read, the plan's items, the manifest paths each put of a live
// server changes, a server missing from changed being one whose manifests
// could not be compared, the set texts Build compiled, whether it built a
// snapshot and how many sets that holds, and the draft.publish fields, nil
// on a direct route.
type recordContext struct {
	w       drafts.World
	items   []store.PlanItem
	changed map[string][]string
	changes map[string][]byte
	built   bool
	sets    int
	publish *publishFields
}

// record is one event of a publish before its envelope.
type record struct {
	subject string
	data    map[string]any
}

// recordsFor answers the Records callback of a publish: the records
// publishRecords makes of the outcome, each in the envelope cloudEvent
// gives it with the actor ctx carries. It reads no store, so it may run
// inside the publish transaction.
func (a *App) recordsFor(ctx context.Context, rc recordContext) func(store.PublishOutcome) ([]store.OutboxEvent, error) {
	return func(out store.PublishOutcome) ([]store.OutboxEvent, error) {
		recs := publishRecords(rc, out)
		events := make([]store.OutboxEvent, len(recs))
		for i, rec := range recs {
			ev, err := a.cloudEvent(ctx, rec.subject, rec.data)
			if err != nil {
				return nil, err
			}
			events[i] = ev
		}
		return events, nil
	}
}

// cloudEvent is the CloudEvent of one event this replica writes to the
// outbox. Its source is this replica's instance id, so this replica's
// converge consumer skips it as its own. A straza.audit.admin record
// carries the actor ctx holds, as actor, actorId and actorVia, and a
// context without one leaves the fields out, since an unknown actor is
// never made up (spec/events §2). It reads and writes nothing.
func (a *App) cloudEvent(ctx context.Context, subject string, data map[string]any) (store.OutboxEvent, error) {
	if subject == "straza.audit.admin" {
		if act, ok := actorFrom(ctx); ok {
			if act.Name != "" {
				data["actor"] = act.Name
			}
			if act.ID != "" {
				data["actorId"] = act.ID
			}
			data["actorVia"] = act.Via
		}
	}
	payload, err := json.Marshal(map[string]any{
		"specversion": "1.0",
		"id":          uuid.NewString(),
		"type":        subject,
		"source":      a.instance,
		"time":        time.Now().UTC().Format(time.RFC3339Nano),
		"data":        data,
	})
	if err != nil {
		return store.OutboxEvent{}, err
	}
	return store.OutboxEvent{Subject: subject, CE: string(payload)}, nil
}

// recordBuilder collects the records of one publish in order, with the
// role ids, the servers whose access rows changed and the servers the
// publish wrote, which the records every publish writes once name.
type recordBuilder struct {
	rc       recordContext
	draft    string
	snapshot string
	recs     []record
	// created maps the name of every role the publish created to its id.
	created  map[string]string
	ids      []string
	bindings []string
	apps     []string
	// gone holds the servers the publish removed, whose rows no binding
	// event names.
	gone map[string]bool
}

// The subjects a publish writes beside straza.audit.admin.
const (
	subjectIdentityUpdated = "straza.identity.updated"
	subjectAppsUpdated     = "straza.apps.updated"
	subjectAppsRemoved     = "straza.apps.removed"
	subjectPolicyUpdated   = "straza.policy.updated"
)

// publishRecords answers the records of a publish that wrote out: those
// of each item outcome in plan order, then those every publish writes once.
// Every event names the published draft in draft but a
// draft.discard, which names the draft it closed and the publishing draft
// in closedBy. No note, document or typed text enters a record.
func publishRecords(rc recordContext, out store.PublishOutcome) []record {
	b := &recordBuilder{rc: rc, draft: strconv.FormatInt(out.DraftID, 10), snapshot: out.Snapshot, created: map[string]string{}, gone: map[string]bool{}}
	items := make(map[store.ObjectRef]store.PlanItem, len(rc.items))
	for _, it := range rc.items {
		items[it.Ref] = it
	}
	for _, o := range out.Items {
		switch {
		case o.Ref.Kind == string(drafts.KindRole) && o.Created:
			b.created[o.Ref.Name] = o.ID
		case o.Ref.Kind == string(drafts.KindApp) && o.Op == string(drafts.OpRemove):
			b.gone[o.Ref.Name] = true
		}
	}
	grouped := map[store.ObjectRef]bool{}
	for _, o := range out.Items {
		if grouped[o.Ref] {
			continue
		}
		it := items[o.Ref]
		switch kind, remove := drafts.Kind(o.Ref.Kind), o.Op == string(drafts.OpRemove); {
		case kind == drafts.KindApp && remove:
			b.removeServer(o, out.Items, grouped)
		case kind == drafts.KindApp:
			b.install(o, it)
		case kind == drafts.KindRole && remove:
			// An implied removal is a role its removed server owned, and
			// removeServer records it with that server.
			b.endRole(removedRole(o), o.Ended, b.rc.w.Roles[o.Ref.Name].Owner, "")
			b.identity(o.ID)
			if row, ok := b.rc.w.Access[o.Ref.Name]; ok {
				b.binding(row.Server)
			}
		case kind == drafts.KindRole:
			b.putRole(o, it)
		default:
			b.set(o, it)
		}
	}
	for _, c := range out.Closed {
		b.recs = append(b.recs, record{"straza.audit.admin", map[string]any{"action": "draft.discard", "draft": strconv.FormatInt(c.ID, 10),
			"revision": c.Revision, "reason": c.Reason, "closedBy": b.draft}})
	}
	for _, id := range b.ids {
		b.add(subjectIdentityUpdated, map[string]any{"id": id})
	}
	for _, app := range b.bindings {
		b.add(subjectAppsUpdated, map[string]any{"change": "binding", "app": app})
	}
	if rc.built {
		b.add(subjectPolicyUpdated, map[string]any{"snapshot": b.snapshot, "sets": rc.sets})
	}
	b.add(subjectAppsUpdated, map[string]any{"change": "publish", "snapshot": b.snapshot, "apps": nonNilStrings(b.apps)})
	if p := rc.publish; p != nil {
		b.audit("draft.publish", b.publishData(p))
	}
	return b.recs
}

// add appends an event of subject with data, naming the draft.
func (b *recordBuilder) add(subject string, data map[string]any) {
	data["draft"] = b.draft
	b.recs = append(b.recs, record{subject, data})
}

// audit appends the straza.audit.admin record of action with data.
func (b *recordBuilder) audit(action string, data map[string]any) {
	data["action"] = action
	b.add("straza.audit.admin", data)
}

// identity names the role id in one straza.identity.updated, once.
func (b *recordBuilder) identity(id string) {
	if id != "" && !slices.Contains(b.ids, id) {
		b.ids = append(b.ids, id)
	}
}

// binding names a server whose access rows changed in one binding event,
// once, unless the publish removed it or no server is named.
func (b *recordBuilder) binding(app string) {
	if app != "" && !b.gone[app] && !slices.Contains(b.bindings, app) {
		b.bindings = append(b.bindings, app)
	}
}

// roleID answers the id of the role name: the one this publish created it
// with, else the live one.
func (b *recordBuilder) roleID(name string) string {
	if id, ok := b.created[name]; ok {
		return id
	}
	return b.rc.w.Roles[name].ID
}

// install records a server put as today's install route does: apps.install
// with whether it updated a live server, the manifest paths it changed
// when they could be compared, and its admin role, minted with a new
// server.
func (b *recordBuilder) install(o store.ItemOutcome, it store.PlanItem) {
	name := o.Ref.Name
	data := map[string]any{"app": name, "update": !o.Created}
	if it.App != nil {
		data["runtime"] = it.App.RuntimeKind
	}
	if paths, ok := b.rc.changed[name]; ok && !o.Created {
		data["changed"] = nonNilStrings(paths)
	}
	if o.AdminRole.Name != "" {
		data["adminRole"] = o.AdminRole.Name
	}
	b.audit("apps.install", data)
	if o.Created {
		b.identity(o.AdminRole.ID)
	}
	b.apps = append(b.apps, name)
}

// removeServer records a server removal as today's removal does, as one
// group: its admin role, then each role it owned that the plan removes
// with it, each with its ended memberships, its edges and its deletion,
// then apps.remove with the roles that lost their access row, then
// straza.apps.removed, which the change feed reads as the server's delete.
func (b *recordBuilder) removeServer(o store.ItemOutcome, all []store.ItemOutcome, grouped map[store.ObjectRef]bool) {
	name := o.Ref.Name
	if o.AdminRole.ID != "" {
		b.endRole(o.AdminRole, o.Ended, "", name)
		b.identity(o.AdminRole.ID)
	}
	for _, r := range all {
		if r.Ref.Kind != string(drafts.KindRole) || !r.Implied || r.Op != string(drafts.OpRemove) || b.rc.w.Roles[r.Ref.Name].Owner != name {
			continue
		}
		grouped[r.Ref] = true
		b.endRole(removedRole(r), r.Ended, name, name)
		b.identity(r.ID)
	}
	data := map[string]any{"app": name, "roles": nonNilStrings(o.LostAccess)}
	if o.AdminRole.Name != "" {
		data["adminRole"] = o.AdminRole.Name
	}
	b.audit("apps.remove", data)
	b.add(subjectAppsRemoved, map[string]any{"app": o.ID, "name": name})
	b.apps = append(b.apps, name)
}

// removedRole is the role a removal outcome deleted.
func removedRole(o store.ItemOutcome) store.Role {
	if len(o.Removed) > 0 {
		return o.Removed[0]
	}
	return store.Role{ID: o.ID, Name: o.Ref.Name}
}

// endRole records a role that goes: one roles.unassign per direct
// membership that ended, one roles.implication.delete per edge out of it,
// so every edge that goes has its record, and roles.delete naming the
// server that owned it. withServer names the removed server the role goes
// with, empty for a role removed on its own, and words the reasons.
func (b *recordBuilder) endRole(role store.Role, ended []store.RoleAssignment, owner, withServer string) {
	why := "role deleted"
	if withServer != "" {
		why = "server removed"
	}
	for _, as := range ended {
		b.audit("roles.unassign", map[string]any{"target": as.ID, "user": as.SubjectID, "role": role.Name, "roleId": as.RoleID,
			"origin": as.Origin, "reason": why})
	}
	edges := slices.Sorted(slices.Values(b.rc.w.Implies[role.Name]))
	for _, implied := range edges {
		b.audit("roles.implication.delete", map[string]any{"target": role.ID, "role": role.Name, "implies": implied, "impliesId": b.roleID(implied)})
	}
	data := map[string]any{"target": role.ID, "role": role.Name}
	if owner != "" {
		data["server"] = owner
	}
	if withServer != "" {
		data["reason"] = "removed with the server " + withServer
	}
	b.audit("roles.delete", data)
}

// putRole records a role put: roles.create for a new role, roles.update
// when a live role's description changed, the access row it replaced and
// the one it added, and the edges it dropped and added. An implied put
// records only the edges it lost with a removed role, since a role that
// lost its row with a removed server is named in that server's apps.remove.
func (b *recordBuilder) putRole(o store.ItemOutcome, it store.PlanItem) {
	name := o.Ref.Name
	b.identity(o.ID)
	edge := func(action, implied string) {
		b.audit(action, map[string]any{"target": o.ID, "role": name, "implies": implied, "impliesId": b.roleID(implied)})
	}
	if o.Implied || it.Role == nil {
		for _, implied := range o.ImpliesRemoved {
			edge("roles.implication.delete", implied)
		}
		return
	}
	cfg := it.Role
	switch {
	case o.Created && cfg.Owner != "":
		b.audit("roles.create", map[string]any{"target": o.ID, "role": name, "server": cfg.Owner})
	case o.Created:
		b.audit("roles.create", map[string]any{"target": o.ID, "role": name})
	case cfg.Role.Description != b.rc.w.Roles[name].Description:
		b.audit("roles.update", map[string]any{"target": o.ID, "role": name})
	}
	for _, row := range o.BindingsRemoved {
		server := b.rc.w.Access[name].Server
		b.audit("apps.binding.delete", map[string]any{"app": server, "role": name, "tools": matchersOf(row), "bindingId": row.ID})
		b.binding(server)
	}
	for _, row := range o.BindingsAdded {
		b.audit("apps.binding.create", map[string]any{"app": cfg.Server, "role": name, "tools": matchersOf(row)})
		b.binding(cfg.Server)
	}
	for _, implied := range o.ImpliesRemoved {
		edge("roles.implication.delete", implied)
	}
	for _, implied := range o.ImpliesAdded {
		edge("roles.implication.create", implied)
	}
}

// matchersOf answers the tool matchers of an access row, never nil.
func matchersOf(row store.ToolBinding) []string {
	var tools []string
	_ = json.Unmarshal([]byte(row.ToolMatcher), &tools)
	return nonNilStrings(tools)
}

// set records a set: policy.create for a new row and policy.update when an
// existing row's text changed, both with the priority, policy.activate when
// its text enters the snapshot and policy.deactivate when it leaves it,
// both naming the snapshot, and policy.delete for a removal.
func (b *recordBuilder) set(o store.ItemOutcome, it store.PlanItem) {
	name := o.Ref.Name
	priority := 0
	if it.Policy != nil {
		priority = it.Policy.Priority
	}
	switch {
	case o.Created:
		b.audit("policy.create", map[string]any{"name": name, "id": o.ID, "priority": priority})
	case o.TextChanged && o.Op != string(drafts.OpRemove):
		b.audit("policy.update", map[string]any{"name": name, "id": o.ID, "priority": priority})
	}
	switch text, placed := b.rc.changes[name]; {
	case placed && text != nil:
		b.audit("policy.activate", map[string]any{"name": name, "id": o.ID, "snapshot": b.snapshot})
	case placed:
		b.audit("policy.deactivate", map[string]any{"name": name, "id": o.ID, "snapshot": b.snapshot})
	}
	if o.Op == string(drafts.OpRemove) {
		b.audit("policy.delete", map[string]any{"name": name, "id": o.ID})
	}
}

// publishData is the data of draft.publish from p: the keys acknowledged
// and typed are always present, and reverts only on an undo.
func (b *recordBuilder) publishData(p *publishFields) map[string]any {
	items := make([]map[string]string, len(p.Items))
	for i, it := range p.Items {
		items[i] = map[string]string{"kind": string(it.Kind), "name": it.Name, "op": string(it.Op)}
	}
	data := map[string]any{"revision": p.Revision, "items": items, "snapshot": b.snapshot, "riskDigest": p.RiskDigest,
		"reviewedDigest": p.ReviewedDigest, "acknowledged": nonNilStrings(p.Acknowledged), "typed": nonNilStrings(p.Typed),
		"proposer": p.Proposer.Name, "proposerId": p.Proposer.ID, "proposerVia": p.Proposer.Via, "client": p.Client}
	if p.Reverts != "" {
		data["reverts"] = p.Reverts
	}
	return data
}
