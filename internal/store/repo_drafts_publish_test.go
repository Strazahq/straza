package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

var draftCarol = DraftActor{ID: "u-carol", Name: "carol", Via: "session", Client: "console"}

// manifestFor answers a remote server's manifest at the address url.
func manifestFor(name, url string) string {
	return `{"metadata":{"name":"` + name + `","version":"1.0.0"},"straza":{"runtime":{"kind":"remote","url":"` + url + `"}}}`
}

// fpOf answers the live fingerprint of ref as the server's World computes
// it from the rows it read, "" for an object that does not exist.
func fpOf(t *testing.T, s Store, ref ObjectRef) string {
	t.Helper()
	ctx := context.Background()
	switch ref.Kind {
	case kindApp:
		a, err := s.Apps().GetByName(ctx, ref.Name)
		if errors.Is(err, ErrNotFound) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		fp, err := FingerprintApp(a.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		return fp
	case kindRole:
		if _, err := s.Roles().GetByName(ctx, ref.Name); errors.Is(err, ErrNotFound) {
			return ""
		} else if err != nil {
			t.Fatal(err)
		}
		return FingerprintRole(readRoleConfig(t, s, ref.Name))
	}
	ps, err := s.Policies().GetByName(ctx, ref.Name)
	if errors.Is(err, ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return FingerprintPolicySet(ps)
}

func appPut(name, manifest string) PlanItem {
	fp, _ := FingerprintApp(manifest)
	return PlanItem{Ref: ObjectRef{kindApp, name}, Op: opPut, After: fp, AfterDoc: manifest,
		App: &App{Name: name, Version: "1.0.0", Manifest: manifest, RuntimeKind: "remote"}}
}

func rolePut(cfg RoleConfig) PlanItem {
	return PlanItem{Ref: ObjectRef{kindRole, cfg.Role.Name}, Op: opPut, After: FingerprintRole(cfg),
		AfterDoc: "the document of " + cfg.Role.Name, Role: &cfg}
}

// impliedPut is a role that a removal in the same plan changes into cfg.
func impliedPut(cfg RoleConfig) PlanItem {
	it := rolePut(cfg)
	it.Implied = true
	return it
}

func setPut(name, text string, priority int) PlanItem {
	return PlanItem{Ref: ObjectRef{kindPolicySet, name}, Op: opPut, AfterDoc: text,
		After:  FingerprintPolicySet(PolicySet{Name: name, Status: "active", YAMLSource: text}),
		Policy: &PolicySet{Name: name, Priority: priority, YAMLSource: text}}
}

func setOff(name, text string) PlanItem {
	return PlanItem{Ref: ObjectRef{kindPolicySet, name}, Op: opOff, AfterDoc: text,
		After:  FingerprintPolicySet(PolicySet{Name: name, Status: "draft", YAMLSource: text}),
		Policy: &PolicySet{Name: name, YAMLSource: text}}
}

func removal(kind, name string) PlanItem {
	return PlanItem{Ref: ObjectRef{kind, name}, Op: opRemove}
}

func impliedRemoval(name string) PlanItem {
	it := removal(kindRole, name)
	it.Implied = true
	return it
}

// checked stamps it with its object as a check sees it: the live
// fingerprint, the live state as the base operation, and a base document.
func checked(t *testing.T, s Store, it PlanItem) PlanItem {
	t.Helper()
	it.Base, it.BaseOp, it.BaseDoc = fpOf(t, s, it.Ref), opRemove, ""
	if it.Base == "" {
		return it
	}
	it.BaseOp, it.BaseDoc = opPut, "live "+it.Ref.Kind+"/"+it.Ref.Name
	if it.Ref.Kind == kindPolicySet {
		if ps, err := s.Policies().GetByName(context.Background(), it.Ref.Name); err == nil && ps.Status != "active" {
			it.BaseOp = opOff
		}
	}
	return it
}

// recordEach makes one outbox row per item outcome and closed draft, and
// one for the publish, each naming the draft, as the server's records do.
func recordEach(o PublishOutcome) ([]OutboxEvent, error) {
	var out []OutboxEvent
	for _, it := range o.Items {
		out = append(out, OutboxEvent{Subject: "straza.audit.admin",
			CE: fmt.Sprintf(`{"draft":"%d","object":"%s/%s","op":"%s"}`, o.DraftID, it.Ref.Kind, it.Ref.Name, it.Op)})
	}
	for _, c := range o.Closed {
		out = append(out, OutboxEvent{Subject: "straza.audit.admin",
			CE: fmt.Sprintf(`{"draft":"%d","closed":"%d"}`, o.DraftID, c.ID)})
	}
	return append(out, OutboxEvent{Subject: "straza.apps.updated",
		CE: fmt.Sprintf(`{"draft":"%d","change":"publish","snapshot":"%s"}`, o.DraftID, o.Snapshot)}), nil
}

// planOf checks items against live state and stores them as an open draft,
// and answers the plan that publishes that draft, as the server builds it.
func planOf(t *testing.T, s Store, items ...PlanItem) PublishPlan {
	t.Helper()
	ctx := context.Background()
	var rows []DraftItemRow
	for i := range items {
		items[i] = checked(t, s, items[i])
		if it := items[i]; !it.Implied {
			rows = append(rows, DraftItemRow{Kind: it.Ref.Kind, Name: it.Ref.Name, Op: it.Op, Doc: it.AfterDoc,
				Base: it.Base, BaseOp: it.BaseOp, BaseDoc: it.BaseDoc})
		}
	}
	d := createDraft(t, s, DraftRow{}, draftAlice, rows...)
	g, err := s.Drafts().Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return PublishPlan{DraftID: d.ID, Revision: d.Revision, Generation: g, BaseSnapshot: activeID(t, s), Items: items,
		Publisher: draftCarol, Acks: `{"reviewedDigest":"r1","riskDigest":"r1","ticked":["k1"]}`, Records: recordEach}
}

func activeID(t *testing.T, s Store) string {
	t.Helper()
	sn, err := s.Snapshots().GetActive(context.Background())
	if errors.Is(err, ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return sn.ID
}

// activate stores the snapshot id and makes it the active one, as the
// snapshot service's own publish does.
func activate(t *testing.T, s Store, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Snapshots().Create(ctx, Snapshot{ID: id, SignerKeyID: "k1", Blob: []byte("blob " + id)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshots().SetActive(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func mustPublish(t *testing.T, s Store, plan PublishPlan) PublishResult {
	t.Helper()
	res, err := s.Drafts().Publish(context.Background(), plan)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return res
}

func execSQL(t *testing.T, s Store, query string, args ...any) {
	t.Helper()
	if _, err := s.(*sqlStore).exec(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// outcomeOf answers the outcome of ref in res, and fails when res has none.
func outcomeOf(t *testing.T, res PublishResult, kind, name string) ItemOutcome {
	t.Helper()
	for _, o := range res.Items {
		if o.Ref == (ObjectRef{kind, name}) {
			return o
		}
	}
	t.Fatalf("the publish reported no outcome for %s/%s: %+v", kind, name, res.Items)
	return ItemOutcome{}
}

// assertPublished checks what every committed publish leaves: the draft
// published by the publisher with the snapshot and the acknowledgments,
// the generation moved by one, one change row per item that changed its
// object with the before the check saw and the after of the plan, every
// object reading its After, and the records of the draft in the outbox.
func assertPublished(t *testing.T, s Store, plan PublishPlan, res PublishResult) {
	t.Helper()
	ctx := context.Background()
	d, _ := getDraft(t, s, res.DraftID)
	if d.State != "published" || d.DecidedBy.ID != plan.Publisher.ID || d.DecidedBy.Via != plan.Publisher.Via ||
		d.DecidedAt == nil || d.PublishedSnapshot != res.Snapshot || d.Acks != plan.Acks {
		t.Errorf("draft %d reads state %s, decided by %+v at %v, snapshot %q, acks %q; want published by %+v with snapshot %q and acks %q",
			d.ID, d.State, d.DecidedBy, d.DecidedAt, d.PublishedSnapshot, d.Acks, plan.Publisher, res.Snapshot, plan.Acks)
	}
	if g, err := s.Drafts().Generation(ctx); err != nil || g != plan.Generation+1 {
		t.Errorf("the generation is %d, %v after the publish; want %d", g, err, plan.Generation+1)
	}
	var want []DraftChangeRow
	for _, it := range plan.Items {
		if it.After != it.Base {
			want = append(want, DraftChangeRow{Seq: len(want) + 1, Kind: it.Ref.Kind, Name: it.Ref.Name, Implied: it.Implied,
				BeforeOp: it.BaseOp, BeforeDoc: it.BaseDoc, BeforeFP: it.Base, AfterOp: it.Op, AfterDoc: it.AfterDoc, AfterFP: it.After})
		}
	}
	if got, err := s.Drafts().Changes(ctx, res.DraftID); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("the change record is %+v, %v; want %+v", got, err, want)
	}
	for _, it := range plan.Items {
		if got := fpOf(t, s, it.Ref); got != it.After {
			t.Errorf("after the publish %s/%s reads %q, and the plan said it would read %q", it.Ref.Kind, it.Ref.Name, got, it.After)
		}
	}
	events, err := s.Outbox().ListRecent(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	mine := 0
	for _, e := range events {
		if strings.Contains(e.CE, fmt.Sprintf(`"draft":"%d"`, res.DraftID)) {
			mine++
		}
	}
	if mine != res.Records || res.Records == 0 {
		t.Errorf("the outbox holds %d rows of draft %d, and the publish reported %d", mine, res.DraftID, res.Records)
	}
}

// dbDump reads every row of every table a publish or a settle can write,
// so that a test can tell that one wrote nothing at all.
func dbDump(t *testing.T, s Store) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range []string{"apps", "roles", "tool_bindings", "role_implications", "role_assignments", "credentials",
		"policy_sets", "snapshots", "settings", "drafts", "draft_items", "draft_revisions", "draft_changes",
		"events_outbox", "config_generation"} {
		rows, err := s.(*sqlStore).db.Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], fmt.Sprint(vals...))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		sort.Strings(out[table])
	}
	return out
}

// sameDump fails the test for every table whose rows differ.
func sameDump(t *testing.T, before, after map[string][]string) {
	t.Helper()
	for table, rows := range before {
		if !reflect.DeepEqual(rows, after[table]) {
			t.Errorf("%s changed:\n before %v\n after  %v", table, rows, after[table])
		}
	}
}

func TestPublishWritesServers(t *testing.T) {
	one, two := manifestFor("demo", "https://one.example/mcp"), manifestFor("demo", "https://two.example/mcp")
	var was App
	cases := []struct {
		name  string
		setup func(t *testing.T, s Store)
		items func(t *testing.T, s Store) []PlanItem
		check func(t *testing.T, s Store, res PublishResult)
	}{
		{
			name: "a new server gets a minted admin role and loses a pause left under its name",
			setup: func(t *testing.T, s Store) {
				if err := s.Settings().Set(context.Background(), pausedKey, `["demo","other"]`); err != nil {
					t.Fatal(err)
				}
			},
			items: func(*testing.T, Store) []PlanItem { return []PlanItem{appPut("demo", one)} },
			check: func(t *testing.T, s Store, res PublishResult) {
				ctx := context.Background()
				row, err := s.Apps().GetByName(ctx, "demo")
				if err != nil || row.Status != "pending" || row.Source != AppSourceAPI || row.Version != "1.0.0" {
					t.Fatalf("the new row is %+v, %v; want status pending, source api, version 1.0.0", row, err)
				}
				admin, err := s.Roles().GetByID(ctx, row.AdminRoleID)
				if err != nil || admin.Name != "mcp-admin-demo" || admin.Plane != RolePlaneControl {
					t.Errorf("the admin role is %+v, %v; want mcp-admin-demo on the control plane", admin, err)
				}
				o := outcomeOf(t, res, kindApp, "demo")
				if !o.Created || o.ID != row.ID || o.AdminRole.ID != row.AdminRoleID {
					t.Errorf("the outcome is %+v; want created, id %s and admin role %s", o, row.ID, row.AdminRoleID)
				}
				if paused, _ := s.Settings().Get(ctx, pausedKey); paused != `["other"]` {
					t.Errorf("the pause set is %s; want [\"other\"]", paused)
				}
			},
		},
		{
			name: "a changed server keeps its id, its admin role and the status the health loop wrote",
			setup: func(t *testing.T, s Store) {
				var err error
				if was, err = s.Apps().Create(context.Background(), App{Name: "demo", RuntimeKind: "remote", Manifest: one}); err != nil {
					t.Fatal(err)
				}
				if err := s.Apps().SetStatus(context.Background(), was.ID, "running"); err != nil {
					t.Fatal(err)
				}
			},
			items: func(*testing.T, Store) []PlanItem { return []PlanItem{appPut("demo", two)} },
			check: func(t *testing.T, s Store, res PublishResult) {
				row, err := s.Apps().GetByName(context.Background(), "demo")
				if err != nil || row.ID != was.ID || row.Status != "running" || row.AdminRoleID != was.AdminRoleID {
					t.Errorf("the row is %+v, %v; want id %s, status running and admin role %s", row, err, was.ID, was.AdminRoleID)
				}
				if o := outcomeOf(t, res, kindApp, "demo"); o.Created || o.ID != was.ID || o.AdminRole.ID != was.AdminRoleID {
					t.Errorf("the outcome is %+v; want not created, with the row's id and admin role", o)
				}
			},
		},
		{
			name: "a removed server comes back on a new row with a new id and admin role",
			setup: func(t *testing.T, s Store) {
				ctx := context.Background()
				var err error
				if was, err = s.Apps().Create(ctx, App{Name: "demo", RuntimeKind: "remote", Manifest: one}); err != nil {
					t.Fatal(err)
				}
				if err := s.Apps().SoftDelete(ctx, was.ID); err != nil {
					t.Fatal(err)
				}
				if err := s.Roles().Delete(ctx, was.AdminRoleID); err != nil {
					t.Fatal(err)
				}
			},
			items: func(*testing.T, Store) []PlanItem { return []PlanItem{appPut("demo", two)} },
			check: func(t *testing.T, s Store, res PublishResult) {
				ctx := context.Background()
				row, err := s.Apps().GetByName(ctx, "demo")
				if err != nil || row.ID == was.ID || row.Status != "pending" || row.AdminRoleID == was.AdminRoleID {
					t.Fatalf("the row is %+v, %v; want a new id, not %s, status pending and a new admin role", row, err, was.ID)
				}
				if admin, err := s.Roles().GetByID(ctx, row.AdminRoleID); err != nil || admin.Name != "mcp-admin-demo" {
					t.Errorf("the admin role is %+v, %v; want a minted mcp-admin-demo", admin, err)
				}
				if o := outcomeOf(t, res, kindApp, "demo"); !o.Created || o.ID != row.ID || o.AdminRole.ID != row.AdminRoleID {
					t.Errorf("the outcome is %+v; want created on the new id with the new admin role", o)
				}
				var n int
				if err := s.(*sqlStore).db.QueryRow(`SELECT COUNT(*) FROM apps WHERE name = 'demo'`).Scan(&n); err != nil || n != 1 {
					t.Errorf("%d apps rows are named demo, %v; want the new one alone, since the removed row went with its id", n, err)
				}
			},
		},
		{
			name:  "a removed server takes its access rows, credentials, admin role, owned roles and pause with it",
			setup: seedRemovableServer,
			items: func(t *testing.T, s Store) []PlanItem {
				dev, lead := readRoleConfig(t, s, "developer"), readRoleConfig(t, s, "lead")
				dev.Server, dev.Tools, lead.Implies = "", nil, nil
				return []PlanItem{removal(kindApp, "demo"), impliedRemoval("demo-readers"), impliedPut(dev), impliedPut(lead)}
			},
			check: func(t *testing.T, s Store, res PublishResult) {
				ctx := context.Background()
				if _, err := s.Apps().GetByName(ctx, "demo"); !errors.Is(err, ErrNotFound) {
					t.Errorf("the server reads %v; want it removed", err)
				}
				var gone int
				if err := s.(*sqlStore).queryRow(ctx, `SELECT COUNT(*) FROM apps WHERE name = 'demo' AND deleted_at IS NOT NULL`).Scan(&gone); err != nil || gone != 1 {
					t.Errorf("%d soft-deleted rows of demo, %v; want 1", gone, err)
				}
				o := outcomeOf(t, res, kindApp, "demo")
				if !reflect.DeepEqual(o.LostAccess, []string{"demo-readers", "developer"}) || o.Credentials != 1 ||
					o.AdminRole.Name != "mcp-admin-demo" || len(o.Removed) != 1 || subjects(o.Ended) != "u-admin" {
					t.Errorf("the server's outcome is %+v; want lost access of demo-readers and developer, 1 credential, and the admin role ended for u-admin", o)
				}
				if r := outcomeOf(t, res, kindRole, "demo-readers"); !r.Implied || subjects(r.Ended) != "u-reader" || len(r.Removed) != 1 {
					t.Errorf("the owned role's outcome is %+v; want implied, removed, ended for u-reader", r)
				}
				if r := outcomeOf(t, res, kindRole, "lead"); !r.Implied || !reflect.DeepEqual(r.ImpliesRemoved, []string{"demo-readers"}) {
					t.Errorf("lead's outcome is %+v; want implied, having lost demo-readers", r)
				}
				for _, table := range []string{"tool_bindings", "credentials"} {
					var n int
					if err := s.(*sqlStore).queryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 0 {
						t.Errorf("%d rows left in %s, %v; want none", n, table, err)
					}
				}
				if paused, _ := s.Settings().Get(ctx, pausedKey); paused != `[]` {
					t.Errorf("the pause set is %s; want it empty", paused)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				tc.setup(t, s)
				plan := planOf(t, s, tc.items(t, s)...)
				res := mustPublish(t, s, plan)
				assertPublished(t, s, plan, res)
				tc.check(t, s, res)
			})
		})
	}
}

// seedRemovableServer stores the server demo with everything that goes
// with its removal: its admin role held by u-admin, its owned role
// demo-readers held by u-reader, the global role developer with an access
// row on it, the role lead that implies demo-readers, a credential row and
// a pause.
func seedRemovableServer(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	demo, err := s.Apps().Create(ctx, App{Name: "demo", RuntimeKind: "remote", Manifest: manifestFor("demo", "https://one.example/mcp")})
	if err != nil {
		t.Fatal(err)
	}
	readers, _, err := s.Roles().CreateOwned(ctx, Role{Name: "demo-readers", OwnerAppID: demo.ID}, `["get_*"]`)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := s.Roles().Create(ctx, Role{Name: "developer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: dev.ID, AppID: demo.ID, ToolMatcher: `["list_*"]`}); err != nil {
		t.Fatal(err)
	}
	lead, err := s.Roles().Create(ctx, Role{Name: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Roles().AddImplication(ctx, lead.ID, readers.ID); err != nil {
		t.Fatal(err)
	}
	for subject, role := range map[string]string{"u-admin": demo.AdminRoleID, "u-reader": readers.ID} {
		if _, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: subject, RoleID: role}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Credentials().Create(ctx, Credential{AppID: demo.ID, Scope: CredScopeApp, OwnerID: demo.ID, Kind: CredStatic,
		EncPayload: []byte("sealed")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().Set(ctx, pausedKey, `["demo"]`); err != nil {
		t.Fatal(err)
	}
}

// subjects names the subjects of as, sorted and joined by commas.
func subjects(as []RoleAssignment) string {
	var out []string
	for _, a := range as {
		out = append(out, a.SubjectID)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestPublishWritesRoles(t *testing.T) {
	var bindingID string
	business := func(name, description string) Role {
		return Role{Name: name, Description: description, Kind: RoleKindBusiness, Plane: RolePlaneAccess}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, s Store)
		items []PlanItem
		check func(t *testing.T, s Store, res PublishResult)
	}{
		{
			name:  "a new business role implies a live one",
			setup: func(t *testing.T, s Store) { seedRoles(t, s, "reader") },
			items: []PlanItem{rolePut(RoleConfig{Role: business("lead", "Leads the team"), Implies: []string{"reader"}})},
			check: func(t *testing.T, s Store, res PublishResult) {
				row, err := s.Roles().GetByName(context.Background(), "lead")
				if err != nil || row.Kind != RoleKindBusiness || row.Plane != RolePlaneAccess || row.OwnerAppID != "" {
					t.Errorf("the new role is %+v, %v; want a global business role", row, err)
				}
				if o := outcomeOf(t, res, kindRole, "lead"); !o.Created || o.ID != row.ID || !reflect.DeepEqual(o.ImpliesAdded, []string{"reader"}) {
					t.Errorf("the outcome is %+v; want created with the edge to reader added", o)
				}
			},
		},
		{
			name:  "a new owned role gets its owner and its access row",
			setup: func(t *testing.T, s Store) { seedServers(t, s, "demo") },
			items: []PlanItem{rolePut(RoleConfig{Role: Role{Name: "demo-writers", Description: "Writes", Kind: RoleKindApplication},
				Owner: "demo", Server: "demo", Tools: []string{"write_*", "get_*"}})},
			check: func(t *testing.T, s Store, res PublishResult) {
				ctx := context.Background()
				demo, _ := s.Apps().GetByName(ctx, "demo")
				row, err := s.Roles().GetByName(ctx, "demo-writers")
				if err != nil || row.OwnerAppID != demo.ID || row.Kind != RoleKindApplication {
					t.Errorf("the new role is %+v, %v; want an application role owned by demo", row, err)
				}
				o := outcomeOf(t, res, kindRole, "demo-writers")
				if len(o.BindingsAdded) != 1 || o.BindingsAdded[0].AppID != demo.ID || o.BindingsAdded[0].ToolMatcher != `["write_*","get_*"]` {
					t.Errorf("the outcome is %+v; want one access row on demo with the tools as given", o)
				}
			},
		},
		{
			name:  "a new straza role is stored as a business role on the control plane",
			setup: func(*testing.T, Store) {},
			items: []PlanItem{rolePut(RoleConfig{Role: Role{Name: "auditor", Description: "Reads the audit", Kind: roleKindStraza}})},
			check: func(t *testing.T, s Store, _ PublishResult) {
				if row, err := s.Roles().GetByName(context.Background(), "auditor"); err != nil || row.Kind != RoleKindBusiness || row.Plane != RolePlaneControl {
					t.Errorf("the new role is %+v, %v; want kind business on the control plane", row, err)
				}
			},
		},
		{
			name: "a changed role gets its description, a new access row and its new edges",
			setup: func(t *testing.T, s Store) {
				seedServers(t, s, "demo", "demo2")
				seedRoles(t, s, "reader", "writer")
				bindingID = seedDeveloper(t, s, "demo", "reader")
			},
			items: []PlanItem{rolePut(RoleConfig{Role: business("developer", "Builds things"), Server: "demo2",
				Tools: []string{"list_*"}, Implies: []string{"writer"}})},
			check: func(t *testing.T, s Store, res PublishResult) {
				o := outcomeOf(t, res, kindRole, "developer")
				if len(o.BindingsRemoved) != 1 || o.BindingsRemoved[0].ID != bindingID || len(o.BindingsAdded) != 1 ||
					o.BindingsAdded[0].ID == bindingID || !reflect.DeepEqual(o.ImpliesRemoved, []string{"reader"}) ||
					!reflect.DeepEqual(o.ImpliesAdded, []string{"writer"}) || o.Created {
					t.Errorf("the outcome is %+v; want the access row %s replaced, reader dropped and writer added", o, bindingID)
				}
			},
		},
		{
			name: "a changed description keeps the access row and the edges",
			setup: func(t *testing.T, s Store) {
				seedServers(t, s, "demo")
				seedRoles(t, s, "reader")
				bindingID = seedDeveloper(t, s, "demo", "reader")
			},
			items: []PlanItem{rolePut(RoleConfig{Role: business("developer", "Builds things"), Server: "demo",
				Tools: []string{"get_*"}, Implies: []string{"reader"}})},
			check: func(t *testing.T, s Store, res PublishResult) {
				o := outcomeOf(t, res, kindRole, "developer")
				rows, err := s.ToolBindings().List(context.Background())
				if err != nil || len(rows) != 1 || rows[0].ID != bindingID || o.BindingsRemoved != nil || o.ImpliesAdded != nil {
					t.Errorf("the access rows are %+v, %v and the outcome %+v; want the row %s kept and no edge touched", rows, err, o, bindingID)
				}
			},
		},
		{
			name: "a removed role ends its memberships and the edges that implied it",
			setup: func(t *testing.T, s Store) {
				seedServers(t, s, "demo")
				seedRoles(t, s, "reader", "lead")
				seedDeveloper(t, s, "demo", "reader")
				ctx := context.Background()
				dev, _ := s.Roles().GetByName(ctx, "developer")
				lead, _ := s.Roles().GetByName(ctx, "lead")
				if err := s.Roles().AddImplication(ctx, lead.ID, dev.ID); err != nil {
					t.Fatal(err)
				}
				for _, u := range []string{"u-1", "u-2"} {
					if _, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: u, RoleID: dev.ID}); err != nil {
						t.Fatal(err)
					}
				}
			},
			items: []PlanItem{removal(kindRole, "developer"), impliedPut(RoleConfig{Role: business("lead", "")})},
			check: func(t *testing.T, s Store, res PublishResult) {
				if o := outcomeOf(t, res, kindRole, "developer"); subjects(o.Ended) != "u-1,u-2" || len(o.Removed) != 1 || o.Removed[0].Name != "developer" {
					t.Errorf("the removal's outcome is %+v; want the memberships of u-1 and u-2 ended", o)
				}
				if o := outcomeOf(t, res, kindRole, "lead"); !o.Implied || !reflect.DeepEqual(o.ImpliesRemoved, []string{"developer"}) {
					t.Errorf("lead's outcome is %+v; want implied, having lost developer", o)
				}
				var n int
				if err := s.(*sqlStore).queryRow(context.Background(), `SELECT COUNT(*) FROM role_assignments`).Scan(&n); err != nil || n != 0 {
					t.Errorf("%d memberships left, %v; want none", n, err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				tc.setup(t, s)
				plan := planOf(t, s, append([]PlanItem(nil), tc.items...)...)
				res := mustPublish(t, s, plan)
				assertPublished(t, s, plan, res)
				tc.check(t, s, res)
			})
		})
	}
}

func seedServers(t *testing.T, s Store, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := s.Apps().Create(context.Background(), App{Name: name, RuntimeKind: "remote",
			Manifest: manifestFor(name, "https://"+name+".example/mcp")}); err != nil {
			t.Fatal(err)
		}
	}
}

func seedRoles(t *testing.T, s Store, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := s.Roles().Create(context.Background(), Role{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
}

// seedDeveloper stores the global role developer with an access row on
// server for get_* and an edge to implied, and answers the row's id.
func seedDeveloper(t *testing.T, s Store, server, implied string) string {
	t.Helper()
	ctx := context.Background()
	dev, err := s.Roles().Create(ctx, Role{Name: "developer", Description: "Old words"})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := s.Apps().GetByName(ctx, server)
	b, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: dev.ID, AppID: app.ID, ToolMatcher: `["get_*"]`})
	if err != nil {
		t.Fatal(err)
	}
	to, _ := s.Roles().GetByName(ctx, implied)
	if err := s.Roles().AddImplication(ctx, dev.ID, to.ID); err != nil {
		t.Fatal(err)
	}
	return b.ID
}

func TestPublishWritesPolicySets(t *testing.T) {
	seedSet := func(status, hash string) func(*testing.T, Store) {
		return func(t *testing.T, s Store) {
			activate(t, s, "s1")
			if _, err := s.Policies().Create(context.Background(), PolicySet{Name: "guard", Priority: 5,
				YAMLSource: "guard v1", CompiledHash: hash, Status: status}); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []struct {
		name     string
		setup    func(t *testing.T, s Store)
		items    []PlanItem
		snapshot string
		check    func(t *testing.T, s Store, res PublishResult)
	}{
		{
			name:     "a new set goes on with its snapshot",
			setup:    func(t *testing.T, s Store) { activate(t, s, "s1") },
			items:    []PlanItem{setPut("guard", "guard v1", 10)},
			snapshot: "s2",
			check: func(t *testing.T, s Store, res PublishResult) {
				row := wantSet(t, s, "guard", "active", "guard v1", sha256Hex("guard v1"))
				if row.Priority != 10 || activeID(t, s) != "s2" || res.Snapshot != "s2" {
					t.Errorf("priority %d, active snapshot %s, answered snapshot %s; want 10, s2 and s2", row.Priority, activeID(t, s), res.Snapshot)
				}
				if o := outcomeOf(t, res, kindPolicySet, "guard"); !o.Created || !o.On || o.WasOn || !o.TextChanged || o.ID != row.ID {
					t.Errorf("the outcome is %+v; want created, on, text changed", o)
				}
			},
		},
		{
			name:  "a changed set takes its text, priority and hash, and the base snapshot stays without a new one",
			setup: seedSet("active", "old"),
			items: []PlanItem{setPut("guard", "guard v2", 20)},
			check: func(t *testing.T, s Store, res PublishResult) {
				if row := wantSet(t, s, "guard", "active", "guard v2", sha256Hex("guard v2")); row.Priority != 20 {
					t.Errorf("the priority is %d; want 20", row.Priority)
				}
				if res.Snapshot != "s1" || activeID(t, s) != "s1" {
					t.Errorf("the publish answered snapshot %s with %s active; want s1 both", res.Snapshot, activeID(t, s))
				}
				if o := outcomeOf(t, res, kindPolicySet, "guard"); !o.WasOn || !o.On || !o.TextChanged || o.Created {
					t.Errorf("the outcome is %+v; want was on, on, text changed", o)
				}
			},
		},
		{
			name:     "a set turned off keeps its compiled hash",
			setup:    seedSet("active", "h1"),
			items:    []PlanItem{setOff("guard", "guard v1")},
			snapshot: "s2",
			check: func(t *testing.T, s Store, res PublishResult) {
				wantSet(t, s, "guard", "draft", "guard v1", "h1")
				if o := outcomeOf(t, res, kindPolicySet, "guard"); !o.WasOn || o.On || o.TextChanged {
					t.Errorf("the outcome is %+v; want was on, now off, text unchanged", o)
				}
			},
		},
		{
			name:  "a new set stored off has no compiled hash",
			setup: func(*testing.T, Store) {},
			items: []PlanItem{setOff("fresh", "fresh v1")},
			check: func(t *testing.T, s Store, res PublishResult) {
				wantSet(t, s, "fresh", "draft", "fresh v1", "")
				if o := outcomeOf(t, res, kindPolicySet, "fresh"); !o.Created || o.On || o.WasOn {
					t.Errorf("the outcome is %+v; want created off", o)
				}
			},
		},
		{
			name:  "a removed set leaves no row",
			setup: seedSet("draft", ""),
			items: []PlanItem{removal(kindPolicySet, "guard")},
			check: func(t *testing.T, s Store, res PublishResult) {
				if _, err := s.Policies().GetByName(context.Background(), "guard"); !errors.Is(err, ErrNotFound) {
					t.Errorf("the set reads %v; want it removed", err)
				}
				if o := outcomeOf(t, res, kindPolicySet, "guard"); o.ID == "" || o.WasOn || o.On {
					t.Errorf("the outcome is %+v; want the removed row's id, off before and after", o)
				}
			},
		},
		{
			name: "a snapshot stored before is activated again as it is",
			setup: func(t *testing.T, s Store) {
				activate(t, s, "s2")
				activate(t, s, "s1")
			},
			items:    []PlanItem{setPut("guard", "guard v1", 0)},
			snapshot: "s2",
			check: func(t *testing.T, s Store, res PublishResult) {
				sn, err := s.Snapshots().GetActive(context.Background())
				if err != nil || sn.ID != "s2" || string(sn.Blob) != "blob s2" {
					t.Errorf("the active snapshot is %s with blob %q, %v; want s2 as it was stored", sn.ID, sn.Blob, err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				tc.setup(t, s)
				plan := planOf(t, s, append([]PlanItem(nil), tc.items...)...)
				if tc.snapshot != "" {
					plan.Snapshot = &Snapshot{ID: tc.snapshot, SignerKeyID: "k1", Blob: []byte("new blob")}
				}
				res := mustPublish(t, s, plan)
				assertPublished(t, s, plan, res)
				tc.check(t, s, res)
			})
		})
	}
}

// wantSet fails the test unless the set name's row holds status, text and
// hash, and answers the row.
func wantSet(t *testing.T, s Store, name, status, text, hash string) PolicySet {
	t.Helper()
	row, err := s.Policies().GetByName(context.Background(), name)
	if err != nil || row.Status != status || row.YAMLSource != text || row.CompiledHash != hash {
		t.Errorf("the set %s is %+v, %v; want status %s, text %q and hash %q", name, row, err, status, text, hash)
	}
	return row
}

// TestPublishWritesNothingOnAConflict moves live state, or the draft, after
// the plan was built, and asserts the conflict that names it and that not
// one row of any table changed, the generation included. A conflict found
// half way through the writes rolls back what came before it.
func TestPublishWritesNothingOnAConflict(t *testing.T) {
	one, two := manifestFor("demo", "https://one.example/mcp"), manifestFor("demo", "https://two.example/mcp")
	cases := []struct {
		name  string
		setup func(t *testing.T, s Store)
		items []PlanItem
		move  func(t *testing.T, s Store, plan *PublishPlan)
		want  PublishConflict
	}{
		{
			name:  "another publish moved the generation",
			setup: func(t *testing.T, s Store) { seedServers(t, s, "demo") },
			items: []PlanItem{appPut("demo", two)},
			move: func(t *testing.T, s Store, _ *PublishPlan) {
				execSQL(t, s, `UPDATE config_generation SET generation = generation + 1 WHERE id = 1`)
			},
			want: PublishConflict{Generation: true},
		},
		{
			name:  "a snapshot the publish did not read became active",
			setup: func(t *testing.T, s Store) { activate(t, s, "s1") },
			items: []PlanItem{setPut("guard", "guard v1", 0)},
			move:  func(t *testing.T, s Store, _ *PublishPlan) { activate(t, s, "s9") },
			want:  PublishConflict{Snapshot: "s9"},
		},
		{
			name:  "the draft moved on to another revision",
			setup: func(t *testing.T, s Store) { seedServers(t, s, "demo") },
			items: []PlanItem{appPut("demo", two)},
			move: func(t *testing.T, s Store, plan *PublishPlan) {
				if _, err := s.Drafts().Revise(context.Background(), plan.DraftID, DraftRevise{From: 1,
					Rev: DraftRevisionRow{Author: draftAlice, Door: "console", Digest: "d2"}}); err != nil {
					t.Fatal(err)
				}
			},
			want: PublishConflict{Draft: true},
		},
		{
			name:  "the draft was discarded",
			setup: func(t *testing.T, s Store) { seedServers(t, s, "demo") },
			items: []PlanItem{appPut("demo", two)},
			move: func(t *testing.T, s Store, plan *PublishPlan) {
				if _, err := s.Drafts().Close(context.Background(), plan.DraftID, 0, "discarded", draftAlice, "", time.Now()); err != nil {
					t.Fatal(err)
				}
			},
			want: PublishConflict{Draft: true},
		},
		{
			name:  "the server changed",
			setup: func(t *testing.T, s Store) { seedServers(t, s, "demo") },
			items: []PlanItem{appPut("demo", two)},
			move: func(t *testing.T, s Store, _ *PublishPlan) {
				row, _ := s.Apps().GetByName(context.Background(), "demo")
				row.Manifest = one
				if _, err := s.Apps().Update(context.Background(), row); err != nil {
					t.Fatal(err)
				}
			},
			want: PublishConflict{Object: ObjectRef{kindApp, "demo"}},
		},
		{
			name:  "the role changed",
			setup: func(t *testing.T, s Store) { seedRoles(t, s, "developer") },
			items: []PlanItem{rolePut(RoleConfig{Role: Role{Name: "developer", Description: "new", Kind: RoleKindBusiness}})},
			move: func(t *testing.T, s Store, _ *PublishPlan) {
				row, _ := s.Roles().GetByName(context.Background(), "developer")
				row.Description = "changed elsewhere"
				if _, err := s.Roles().Update(context.Background(), row); err != nil {
					t.Fatal(err)
				}
			},
			want: PublishConflict{Object: ObjectRef{kindRole, "developer"}},
		},
		{
			name:  "the set changed",
			setup: func(t *testing.T, s Store) { seedPolicy(t, s, "guard", "active", "guard v1") },
			items: []PlanItem{setPut("guard", "guard v2", 0)},
			move: func(t *testing.T, s Store, _ *PublishPlan) {
				row, _ := s.Policies().GetByName(context.Background(), "guard")
				row.YAMLSource = "guard saved elsewhere"
				if _, err := s.Policies().Update(context.Background(), row); err != nil {
					t.Fatal(err)
				}
			},
			want: PublishConflict{Object: ObjectRef{kindPolicySet, "guard"}},
		},
		{
			name:  "a role gained an access row on the server being removed",
			setup: func(t *testing.T, s Store) { seedServers(t, s, "demo") },
			items: []PlanItem{removal(kindApp, "demo")},
			move: func(t *testing.T, s Store, _ *PublishPlan) {
				ctx := context.Background()
				newcomer, _ := s.Roles().Create(ctx, Role{Name: "newcomer"})
				demo, _ := s.Apps().GetByName(ctx, "demo")
				if _, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: newcomer.ID, AppID: demo.ID, ToolMatcher: `["*"]`}); err != nil {
					t.Fatal(err)
				}
			},
			want: PublishConflict{Object: ObjectRef{kindRole, "newcomer"}, Cascade: true},
		},
		{
			name:  "a role began to imply the role being removed",
			setup: func(t *testing.T, s Store) { seedRoles(t, s, "developer", "lead") },
			items: []PlanItem{removal(kindRole, "developer")},
			move: func(t *testing.T, s Store, _ *PublishPlan) {
				ctx := context.Background()
				dev, _ := s.Roles().GetByName(ctx, "developer")
				lead, _ := s.Roles().GetByName(ctx, "lead")
				if err := s.Roles().AddImplication(ctx, lead.ID, dev.ID); err != nil {
					t.Fatal(err)
				}
			},
			want: PublishConflict{Object: ObjectRef{kindRole, "lead"}, Cascade: true},
		},
		{
			name:  "an edge the plan adds names a role that went, after a new server and role were written",
			setup: func(t *testing.T, s Store) { seedRoles(t, s, "ghost") },
			items: []PlanItem{appPut("fresh", manifestFor("fresh", "https://fresh.example/mcp")),
				rolePut(RoleConfig{Role: Role{Name: "lead", Kind: RoleKindBusiness}, Implies: []string{"ghost"}})},
			move: func(t *testing.T, s Store, _ *PublishPlan) {
				ghost, _ := s.Roles().GetByName(context.Background(), "ghost")
				if err := s.Roles().Delete(context.Background(), ghost.ID); err != nil {
					t.Fatal(err)
				}
			},
			want: PublishConflict{Object: ObjectRef{kindRole, "lead"}},
		},
		{
			name:  "an access row the plan adds names a server that went",
			setup: func(t *testing.T, s Store) { seedServers(t, s, "gone") },
			items: []PlanItem{rolePut(RoleConfig{Role: Role{Name: "lead", Kind: RoleKindBusiness}, Server: "gone", Tools: []string{"*"}})},
			move: func(t *testing.T, s Store, _ *PublishPlan) {
				row, _ := s.Apps().GetByName(context.Background(), "gone")
				if err := s.Apps().SoftDelete(context.Background(), row.ID); err != nil {
					t.Fatal(err)
				}
			},
			want: PublishConflict{Object: ObjectRef{kindRole, "lead"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				tc.setup(t, s)
				plan := planOf(t, s, append([]PlanItem(nil), tc.items...)...)
				tc.move(t, s, &plan)
				before := dbDump(t, s)
				_, err := s.Drafts().Publish(context.Background(), plan)
				var got PublishConflict
				if !errors.As(err, &got) || got != tc.want || !errors.Is(err, ErrConflict) {
					t.Errorf("Publish answered %v (%+v); want the conflict %+v, which is an ErrConflict", err, got, tc.want)
				}
				sameDump(t, before, dbDump(t, s))
			})
		})
	}
}

func seedPolicy(t *testing.T, s Store, name, status, text string) {
	t.Helper()
	if _, err := s.Policies().Create(context.Background(), PolicySet{Name: name, YAMLSource: text, Status: status}); err != nil {
		t.Fatal(err)
	}
}

// TestPublishWritesNothingWhenItsRecordsFail pins that the records commit
// with the rows: a Records callback that fails after every write, or that
// makes no record, from which no other replica could apply the publish,
// rolls back the rows, the history, the snapshot, the draft and the
// generation.
func TestPublishWritesNothingWhenItsRecordsFail(t *testing.T) {
	cases := []struct {
		name    string
		records func(PublishOutcome) ([]OutboxEvent, error)
		want    string
	}{
		{"the callback fails", func(PublishOutcome) ([]OutboxEvent, error) { return nil, errors.New("the record would not encode") },
			"the record would not encode"},
		{"the callback makes no record", func(PublishOutcome) ([]OutboxEvent, error) { return nil, nil }, "made no record"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				activate(t, s, "s1")
				seedRemovableServer(t, s)
				dev, lead := readRoleConfig(t, s, "developer"), readRoleConfig(t, s, "lead")
				dev.Server, dev.Tools, lead.Implies = "", nil, nil
				plan := planOf(t, s, removal(kindApp, "demo"), appPut("fresh", manifestFor("fresh", "https://fresh.example/mcp")),
					setPut("guard", "guard v1", 0), impliedRemoval("demo-readers"), impliedPut(dev), impliedPut(lead))
				plan.Snapshot = &Snapshot{ID: "s2", SignerKeyID: "k1", Blob: []byte("new blob")}
				plan.Records = tc.records
				before := dbDump(t, s)
				_, err := s.Drafts().Publish(context.Background(), plan)
				if err == nil || errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("Publish answered %v; want a refusal saying %q, which is no conflict", err, tc.want)
				}
				sameDump(t, before, dbDump(t, s))
			})
		})
	}
}

// TestPublishRefusesWritesThatDoNotReachTheirAfter pins the read back after
// the writes: a plan whose After the writes do not produce, here a live
// role's kind, which the store never changes, is refused with nothing
// written, so no change row records an after that no object has.
func TestPublishRefusesWritesThatDoNotReachTheirAfter(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		seedRoles(t, s, "developer")
		plan := planOf(t, s, rolePut(RoleConfig{Role: Role{Name: "developer", Description: "new words", Kind: RoleKindApplication}}))
		before := dbDump(t, s)
		_, err := s.Drafts().Publish(context.Background(), plan)
		if err == nil || errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "Role/developer does not read as the plan says") {
			t.Errorf("Publish answered %v; want the refusal of the read back, which is no conflict", err)
		}
		sameDump(t, before, dbDump(t, s))
	})
}

// TestPublishRevivesAServerWithoutWhatWasWrittenAfterItsRemoval pins that
// a server published again under a removed name starts afresh on a new
// row with a new id: a grant a connect stored and an access row a route
// added on the removed row after the removal go with it, so no token of
// the old server reaches the address the new one names, and a grant that
// still names the old id after the revival meets no row and is refused.
func TestPublishRevivesAServerWithoutWhatWasWrittenAfterItsRemoval(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		seedServers(t, s, "demo")
		seedRoles(t, s, "late")
		was, _ := s.Apps().GetByName(ctx, "demo")
		late, _ := s.Roles().GetByName(ctx, "late")
		mustPublish(t, s, planOf(t, s, removal(kindApp, "demo")))
		lateGrant := Credential{AppID: was.ID, Scope: CredScopeUser, OwnerID: "u-late", Kind: CredOAuth, EncPayload: []byte("sealed grant")}
		if _, err := s.Credentials().Create(ctx, lateGrant); err != nil {
			t.Fatalf("store the late grant: %v", err)
		}
		if _, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: late.ID, AppID: was.ID, ToolMatcher: `["*"]`}); err != nil {
			t.Fatalf("store the late access row: %v", err)
		}
		plan := planOf(t, s, appPut("demo", manifestFor("demo", "https://elsewhere.example/mcp")))
		res := mustPublish(t, s, plan)
		assertPublished(t, s, plan, res)
		row, _ := s.Apps().GetByName(ctx, "demo")
		creds, err := s.Credentials().ListByApp(ctx, row.ID)
		if err != nil || len(creds) != 0 || row.ID == was.ID {
			t.Errorf("the revived row %s (was %s) carries %d credential rows, %v; want a new id and none", row.ID, was.ID, len(creds), err)
		}
		if rows, err := s.ToolBindings().ListByRole(ctx, late.ID); err != nil || len(rows) != 0 {
			t.Errorf("the late access row reads %+v, %v; want it gone with the revival", rows, err)
		}
		if o := outcomeOf(t, res, kindApp, "demo"); !o.Created || o.Credentials != 1 {
			t.Errorf("the outcome is %+v; want created, having deleted one credential row", o)
		}
		if _, err := s.Credentials().Create(ctx, lateGrant); err == nil {
			t.Errorf("a grant stored under the old id %s landed; want it refused, since no row has that id", was.ID)
		}
		if creds, err := s.Credentials().ListByApp(ctx, row.ID); err != nil || len(creds) != 0 {
			t.Errorf("the new row %s carries %d credential rows, %v; want none after the late grant", row.ID, len(creds), err)
		}
	})
}

// TestPublishIgnoresAStatusOnlyWrite pins that the health loop's status
// write between the check and the publish is no conflict, since status is
// not an input of the server's fingerprint.
func TestPublishIgnoresAStatusOnlyWrite(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		seedServers(t, s, "demo")
		plan := planOf(t, s, appPut("demo", manifestFor("demo", "https://two.example/mcp")))
		row, _ := s.Apps().GetByName(context.Background(), "demo")
		if err := s.Apps().SetStatus(context.Background(), row.ID, "degraded"); err != nil {
			t.Fatal(err)
		}
		assertPublished(t, s, plan, mustPublish(t, s, plan))
	})
}

// TestPublishLandsOnAFreshReadAfterASnapshotConflict pins the store's half
// of a rerun: Publish never builds a snapshot again. A snapshot that became
// active after the check turns the publish away naming it, stores nothing
// of the snapshot built on the old base and leaves the draft open, and a
// plan made again from a fresh read, with a snapshot built on the new base,
// publishes the same draft.
func TestPublishLandsOnAFreshReadAfterASnapshotConflict(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		activate(t, s, "s1")
		plan := planOf(t, s, setPut("guard", "guard v1", 0))
		plan.Snapshot = &Snapshot{ID: "built-on-s1", SignerKeyID: "k1", Blob: []byte("guard on s1")}
		activate(t, s, "s9")
		var pc PublishConflict
		if _, err := s.Drafts().Publish(ctx, plan); !errors.As(err, &pc) || pc.Snapshot != "s9" {
			t.Fatalf("the publish answered %v; want the conflict naming s9", err)
		}
		if _, err := s.Snapshots().GetByID(ctx, "built-on-s1"); !errors.Is(err, ErrNotFound) || activeID(t, s) != "s9" {
			t.Errorf("the snapshot built on s1 reads %v with %s active; want it never stored and s9 active", err, activeID(t, s))
		}
		again := plan
		again.Generation, _ = s.Drafts().Generation(ctx)
		again.BaseSnapshot = activeID(t, s)
		again.Items = []PlanItem{checked(t, s, setPut("guard", "guard v1", 0))}
		again.Snapshot = &Snapshot{ID: "built-on-s9", SignerKeyID: "k1", Blob: []byte("guard on s9")}
		res := mustPublish(t, s, again)
		assertPublished(t, s, again, res)
		if res.Snapshot != "built-on-s9" || activeID(t, s) != "built-on-s9" {
			t.Errorf("the rerun answered snapshot %s with %s active; want built-on-s9 both", res.Snapshot, activeID(t, s))
		}
	})
}

// TestPublishEndsTheSlotDraftsItCloses pins step 8: the open draft of a
// closed slot ends as discarded by the publisher with the reason and is
// reported, while a published draft of that slot, an open draft of another
// slot and the publishing draft itself are left alone.
func TestPublishEndsTheSlotDraftsItCloses(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		seedPolicy(t, s, "guard", "active", "guard v1")
		item := stampedItem(kindPolicySet, "guard", "guard as saved", "fp")
		old := createDraft(t, s, DraftRow{Slot: "policy:guard"}, draftBob, item)
		markPublished(t, s, old.ID, draftCheckAt)
		saved := createDraft(t, s, DraftRow{Slot: "policy:guard"}, draftBob, item)
		other := createDraft(t, s, DraftRow{Slot: "policy:other"}, draftBob, stampedItem(kindPolicySet, "other", "x", "fp"))
		plan := planOf(t, s, setOff("guard", "guard v1"))
		plan.Close = []SlotClose{{Slot: "policy:guard", Reason: "the publish turned guard off"}, {Slot: "policy:none", Reason: "unused"}}
		res := mustPublish(t, s, plan)
		assertPublished(t, s, plan, res)
		if want := []ClosedDraft{{ID: saved.ID, Revision: 1, Reason: "the publish turned guard off"}}; !reflect.DeepEqual(res.Closed, want) {
			t.Errorf("the publish reports closing %+v; want %+v", res.Closed, want)
		}
		if d, _ := getDraft(t, s, saved.ID); d.State != "discarded" || d.DecidedBy.ID != draftCarol.ID ||
			d.DecidedReason != "the publish turned guard off" || d.DecidedAt == nil {
			t.Errorf("the saved edit is %s, decided by %+v for %q; want discarded by carol with the reason", d.State, d.DecidedBy, d.DecidedReason)
		}
		if d, _ := getDraft(t, s, old.ID); d.State != "published" || d.DecidedBy.ID != draftAlice.ID {
			t.Errorf("the earlier published draft is %s, decided by %+v; want it left as it was", d.State, d.DecidedBy)
		}
		if d, _ := getDraft(t, s, other.ID); d.State != "open" {
			t.Errorf("the draft of another slot is %s; want it open", d.State)
		}

		own := createDraft(t, s, DraftRow{Slot: "policy:mine"}, draftAlice, stampedItem(kindPolicySet, "mine", "x", "fp"))
		plan = PublishPlan{DraftID: own.ID, Revision: 1, Generation: plan.Generation + 1, BaseSnapshot: "",
			Close: []SlotClose{{Slot: "policy:mine", Reason: "its own slot"}}, Publisher: draftCarol, Records: recordEach}
		if res := mustPublish(t, s, plan); len(res.Closed) != 0 {
			t.Errorf("the publish of a slot's own draft closed %+v; want nothing", res.Closed)
		}
		if d, _ := getDraft(t, s, own.ID); d.State != "published" {
			t.Errorf("the slot's own draft is %s; want published", d.State)
		}
	})
}

// TestPublishMovesTheGenerationOncePerPublish pins the generation moving by
// one per publish, a plan equal to live included, which writes no object
// and no history and still marks its draft published.
func TestPublishMovesTheGenerationOncePerPublish(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		seedServers(t, s, "demo")
		before, _ := s.Apps().GetByName(ctx, "demo")
		for i, manifest := range []string{manifestFor("demo", "https://demo.example/mcp"), manifestFor("demo", "https://two.example/mcp")} {
			plan := planOf(t, s, appPut("demo", manifest))
			if plan.Generation != int64(i) {
				t.Fatalf("publish %d reads generation %d; want %d", i+1, plan.Generation, i)
			}
			res := mustPublish(t, s, plan)
			assertPublished(t, s, plan, res)
			if i > 0 {
				continue
			}
			row, _ := s.Apps().GetByName(ctx, "demo")
			if len(res.Items) != 0 || res.Records != 1 || !row.UpdatedAt.Equal(before.UpdatedAt) {
				t.Errorf("the publish equal to live reported %+v with %d records and moved the row to %v; want no outcome, one record and no write",
					res.Items, res.Records, row.UpdatedAt)
			}
		}
	})
}

// TestPublishCreatesTheDirectRouteDraftInItsTransaction pins plan.New: the
// one-item draft of a direct route is inserted and published by the same
// transaction, and a conflicting publish leaves no draft behind.
func TestPublishCreatesTheDirectRouteDraftInItsTransaction(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		it := checked(t, s, appPut("demo", manifestFor("demo", "https://one.example/mcp")))
		row := DraftItemRow{Kind: kindApp, Name: "demo", Op: opPut, Doc: it.AfterDoc, Base: it.Base, BaseOp: it.BaseOp}
		plan := PublishPlan{New: &DraftNew{Row: DraftRow{Door: "api"}, Items: []DraftItemRow{row},
			Rev: DraftRevisionRow{Author: draftBob, Digest: "one"}}, Generation: 1, Items: []PlanItem{it},
			Publisher: draftBob, Records: recordEach}
		before := dbDump(t, s)
		if _, err := s.Drafts().Publish(ctx, plan); !errors.Is(err, ErrConflict) {
			t.Fatalf("a direct publish on a moved generation answered %v; want a conflict", err)
		}
		sameDump(t, before, dbDump(t, s))

		plan.Generation = 0
		res := mustPublish(t, s, plan)
		assertPublished(t, s, plan, res)
		d, items := getDraft(t, s, res.DraftID)
		if d.Door != "api" || d.Revision != 1 || d.Proposer.ID != draftBob.ID || len(items) != 1 {
			t.Errorf("the direct route's draft is %+v with %d items; want door api, revision 1, proposed by bob, one item", d, len(items))
		}
	})
}

// TestPublishHoldsNoOtherConnectionOnSQLite runs a publish that creates,
// changes and revives servers under a two-second deadline. sqlite holds one
// connection, so any call on the pool from inside the transaction would
// wait for the deadline and fail the test instead of hanging.
func TestPublishHoldsNoOtherConnectionOnSQLite(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		if s.(*sqlStore).d != dialectSQLite {
			t.Skip("the rule is sqlite's one connection")
		}
		seedServers(t, s, "changed", "revived")
		row, _ := s.Apps().GetByName(context.Background(), "revived")
		if err := s.Apps().SoftDelete(context.Background(), row.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Roles().Delete(context.Background(), row.AdminRoleID); err != nil {
			t.Fatal(err)
		}
		plan := planOf(t, s, appPut("fresh", manifestFor("fresh", "https://fresh.example/mcp")),
			appPut("changed", manifestFor("changed", "https://two.example/mcp")),
			appPut("revived", manifestFor("revived", "https://two.example/mcp")))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		res, err := s.Drafts().Publish(ctx, plan)
		if err != nil {
			t.Fatalf("the publish failed inside its deadline, which a pool call from inside the transaction does: %v", err)
		}
		assertPublished(t, s, plan, res)
	})
}

// TestPublishRacesOnOneGeneration is two replicas publishing at once: eight
// publishers checked at one generation, over two handles on Postgres, and
// exactly one of them lands. The others find the generation moved and
// write nothing.
func TestPublishRacesOnOneGeneration(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		handles := []Store{s, s}
		if s.(*sqlStore).d == dialectPostgres {
			peer, err := Open(config.Config{Store: config.Store{Driver: config.DriverPostgres, DSN: os.Getenv("STRAZA_TEST_POSTGRES_DSN")}})
			if err != nil {
				t.Fatalf("open the second replica's handle: %v", err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			handles[1] = peer
		}
		const publishers = 8
		plans := make([]PublishPlan, publishers)
		for i := range plans {
			plans[i] = planOf(t, s, appPut("demo", manifestFor("demo", fmt.Sprintf("https://%d.example/mcp", i))))
		}
		var wg sync.WaitGroup
		errs := make([]error, publishers)
		start := make(chan struct{})
		for i := range plans {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = handles[i%2].Drafts().Publish(ctx, plans[i])
			}()
		}
		close(start)
		wg.Wait()
		won := -1
		for i, err := range errs {
			var pc PublishConflict
			switch {
			case err == nil && won < 0:
				won = i
			case err == nil:
				t.Errorf("publishers %d and %d both landed at generation 0", won, i)
			case !errors.As(err, &pc) || !pc.Generation:
				t.Errorf("publisher %d answered %v; want a generation conflict", i, err)
			}
		}
		if won < 0 {
			t.Fatal("no publisher landed")
		}
		if g, _ := s.Drafts().Generation(ctx); g != 1 {
			t.Errorf("the generation is %d after the race; want 1", g)
		}
		if got := fpOf(t, s, ObjectRef{kindApp, "demo"}); got != plans[won].Items[0].After {
			t.Errorf("the server holds another manifest than the one publisher %d landed", won)
		}
		published, err := s.Drafts().List(ctx, DraftFilter{State: "published"}, 0, 50)
		if err != nil || len(published) != 1 || published[0].ID != plans[won].DraftID {
			t.Errorf("the published drafts are %v, %v; want only draft %d", draftIDs(published), err, plans[won].DraftID)
		}
	})
}

// waitForLockWait polls pg_locks until a session of the test's own
// database waits for a lock l that matches where, and fails the test after
// ten seconds. Other databases on the server are left out, since their
// tests take locks of their own.
func waitForLockWait(t *testing.T, s Store, where string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := s.(*sqlStore).db.QueryRow(`SELECT COUNT(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE NOT l.granted AND a.datname = current_database() AND ` + where).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no lock with %s was ever waited for", where)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPublishAnswersBusyOnADeadlock builds a deadlock on Postgres: another
// transaction holds the generation row, the publish waits for it while it
// holds the publish lock, and the other then asks for the publish lock.
// The publish waited first, so its deadlock check fires first and turns it
// away, and it must answer a busy conflict and write nothing.
func TestPublishAnswersBusyOnADeadlock(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		if sq.d != dialectPostgres {
			t.Skip("sqlite's one connection meets no deadlock")
		}
		ctx := context.Background()
		plan := planOf(t, s, appPut("demo", manifestFor("demo", "https://one.example/mcp")))
		before := dbDump(t, s)
		holder, err := sq.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		if _, err := holder.ExecContext(ctx, `UPDATE config_generation SET generation = generation WHERE id = 1`); err != nil {
			t.Fatal(err)
		}
		published := make(chan error, 1)
		go func() {
			_, err := s.Drafts().Publish(ctx, plan)
			published <- err
		}()
		waitForLockWait(t, s, `l.locktype = 'transactionid'`)
		locked := make(chan error, 1)
		go func() {
			_, err := holder.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, draftPublishLockKey)
			locked <- err
		}()
		var pc PublishConflict
		if err := <-published; !errors.As(err, &pc) || !pc.Busy {
			t.Fatalf("the publish answered %v; want a busy conflict", err)
		}
		if err := <-locked; err != nil {
			t.Fatalf("the other transaction did not get the publish lock once the publish rolled back: %v", err)
		}
		if err := holder.Rollback(); err != nil {
			t.Fatal(err)
		}
		sameDump(t, before, dbDump(t, s))
	})
}

// TestPublishHoldsTheRowsItCheckedOnPostgres pins the row locks of the
// checks: a writer outside the publish lock that changes a role the
// publish read but did not write waits for the publish to commit. The
// records callback holds the transaction open to show it.
func TestPublishHoldsTheRowsItCheckedOnPostgres(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		if sq.d != dialectPostgres {
			t.Skip("sqlite's one connection lets no writer in beside a publish")
		}
		ctx := context.Background()
		seedRoles(t, s, "reader")
		if _, err := s.Roles().Create(ctx, Role{Name: "developer", Description: "Builds"}); err != nil {
			t.Fatal(err)
		}
		plan := planOf(t, s, rolePut(RoleConfig{Role: Role{Name: "developer", Description: "Builds", Kind: RoleKindBusiness},
			Implies: []string{"reader"}}))
		inside, release := make(chan struct{}), make(chan struct{})
		plan.Records = func(o PublishOutcome) ([]OutboxEvent, error) {
			close(inside)
			<-release
			return recordEach(o)
		}
		published := make(chan error, 1)
		go func() {
			_, err := s.Drafts().Publish(ctx, plan)
			published <- err
		}()
		<-inside
		updated := make(chan error, 1)
		go func() {
			_, err := sq.db.ExecContext(ctx, `UPDATE roles SET description = 'changed elsewhere' WHERE name = 'developer'`)
			updated <- err
		}()
		waitForLockWait(t, s, `l.locktype IN ('transactionid', 'tuple')`)
		select {
		case err := <-updated:
			t.Fatalf("the writer changed the role while the publish held it: %v", err)
		default:
		}
		close(release)
		if err := <-published; err != nil {
			t.Fatal(err)
		}
		if err := <-updated; err != nil {
			t.Fatal(err)
		}
		if row, _ := s.Roles().GetByName(ctx, "developer"); row.Description != "changed elsewhere" {
			t.Errorf("the role reads %q; want the writer's change, landed after the publish", row.Description)
		}
	})
}

// TestPublishAnswersTheObjectWhenItsNameIsTakenMeanwhile pins that a
// unique key a write meets is the item's conflict: another writer inserts
// a role of the name the publish creates, uncommitted when the publish
// checks, so the publish's insert waits for it and then meets the name.
func TestPublishAnswersTheObjectWhenItsNameIsTakenMeanwhile(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		if sq.d != dialectPostgres {
			t.Skip("sqlite's one connection lets no writer in beside a publish")
		}
		ctx := context.Background()
		plan := planOf(t, s, rolePut(RoleConfig{Role: Role{Name: "lead", Description: "Leads", Kind: RoleKindBusiness}}))
		holder, err := sq.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		at := sq.tArg(now())
		if _, err := holder.ExecContext(ctx, `INSERT INTO roles (id, name, description, kind, plane, created_at, updated_at)
			VALUES ($1, 'lead', 'made elsewhere', 'business', 'access', $2, $3)`, newID(), at, at); err != nil {
			t.Fatal(err)
		}
		published := make(chan error, 1)
		go func() {
			_, err := s.Drafts().Publish(ctx, plan)
			published <- err
		}()
		waitForLockWait(t, s, `l.locktype = 'transactionid'`)
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		var pc PublishConflict
		if err := <-published; !errors.As(err, &pc) || pc != (PublishConflict{Object: ObjectRef{kindRole, "lead"}}) {
			t.Fatalf("the publish answered %v; want the conflict on Role/lead", err)
		}
		if g, _ := s.Drafts().Generation(ctx); g != 0 {
			t.Errorf("the generation is %d; want 0, nothing published", g)
		}
		if row, err := s.Roles().GetByName(ctx, "lead"); err != nil || row.Description != "made elsewhere" {
			t.Errorf("the role reads %+v, %v; want the other writer's row alone", row, err)
		}
		if d, _ := getDraft(t, s, plan.DraftID); d.State != "open" {
			t.Errorf("the draft is %s; want it still open", d.State)
		}
	})
}

// TestPublishReleasesTheLockWhateverEndsIt pins that no way out of a
// publish keeps the publish lock on Postgres or sqlite's one connection: a
// malformed removal is refused before the lock, and a records callback that
// panics inside the transaction rolls it back as the panic leaves. The next
// publish then lands inside a two-second deadline.
func TestPublishReleasesTheLockWhateverEndsIt(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		seedServers(t, s, "demo")
		good := planOf(t, s, appPut("demo", manifestFor("demo", "https://two.example/mcp")))
		malformed := planOf(t, s, removal(kindRole, "ghost"))
		malformed.Items[0].After = "not empty"
		caller, endCaller := context.WithCancel(context.Background())
		defer endCaller()
		if _, err := s.Drafts().Publish(caller, malformed); err == nil || errors.Is(err, ErrConflict) {
			t.Errorf("the malformed removal answered %v; want a refusal", err)
		}
		panicking := planOf(t, s, setPut("guard", "guard v1", 0))
		panicking.Records = func(PublishOutcome) ([]OutboxEvent, error) { panic("the records callback broke") }
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Error("the callback's panic did not reach the caller")
				}
			}()
			_, _ = s.Drafts().Publish(caller, panicking)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		res, err := s.Drafts().Publish(ctx, good)
		if err != nil {
			t.Fatalf("the next publish answered %v while the earlier callers still live; want it to land", err)
		}
		assertPublished(t, s, good, res)
	})
}

// TestPublishKeepsAPauseAnotherReplicaWritesMeanwhile pins the row lock on
// the pause set: another replica pauses a server while a publish drops the
// removed server's name, and the publish waits for that pause and keeps it,
// since losing an operator's pause would start the server again.
func TestPublishKeepsAPauseAnotherReplicaWritesMeanwhile(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		if sq.d != dialectPostgres {
			t.Skip("sqlite's one connection lets no writer in beside a publish")
		}
		ctx := context.Background()
		seedServers(t, s, "demo")
		if err := s.Settings().Set(ctx, pausedKey, `["demo"]`); err != nil {
			t.Fatal(err)
		}
		plan := planOf(t, s, removal(kindApp, "demo"))
		holder, err := sq.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		if _, err := holder.ExecContext(ctx, `UPDATE settings SET value = '["demo","other"]' WHERE key = 'manager.paused'`); err != nil {
			t.Fatal(err)
		}
		published := make(chan error, 1)
		go func() {
			_, err := s.Drafts().Publish(ctx, plan)
			published <- err
		}()
		waitForLockWait(t, s, `l.locktype = 'transactionid'`)
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-published; err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Settings().Get(ctx, pausedKey); got != `["other"]` {
			t.Errorf("the pause set is %s after the publish; want [\"other\"], the other replica's pause kept", got)
		}
	})
}

// TestPublishNamesTheSnapshotThatBecameActiveWhileItWaited pins the
// snapshot conflict on Postgres: a publish whose locked read waits while
// another transaction activates s2 finds the old row no longer active, and
// its conflict names s2, never an empty store.
func TestPublishNamesTheSnapshotThatBecameActiveWhileItWaited(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		if sq.d != dialectPostgres {
			t.Skip("sqlite's one connection lets no writer in beside a publish")
		}
		ctx := context.Background()
		activate(t, s, "s1")
		if _, err := s.Snapshots().Create(ctx, Snapshot{ID: "s2", SignerKeyID: "k1", Blob: []byte("blob s2")}); err != nil {
			t.Fatal(err)
		}
		plan := planOf(t, s, setPut("guard", "guard v1", 0))
		holder, err := sq.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		for _, q := range []string{`SELECT id FROM snapshots WHERE active = TRUE FOR UPDATE`, `UPDATE snapshots SET active = (id = 's2')`} {
			if _, err := holder.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		published := make(chan error, 1)
		go func() {
			_, err := s.Drafts().Publish(ctx, plan)
			published <- err
		}()
		waitForLockWait(t, s, `l.locktype IN ('transactionid', 'tuple')`)
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		var pc PublishConflict
		if err := <-published; !errors.As(err, &pc) || pc != (PublishConflict{Snapshot: "s2"}) {
			t.Errorf("the publish answered %v; want the conflict naming s2", err)
		}
	})
}

// TestPublishAnswersTheObjectWhenATargetGoesMeanwhile pins that a foreign
// key a write meets is the item's conflict, as a unique key is: another
// writer deletes the role an added edge names while the publish runs, and
// the publish answers the conflict on the role that implies it.
func TestPublishAnswersTheObjectWhenATargetGoesMeanwhile(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		if sq.d != dialectPostgres {
			t.Skip("sqlite's one connection lets no writer in beside a publish")
		}
		ctx := context.Background()
		seedRoles(t, s, "lead", "ghost")
		plan := planOf(t, s, rolePut(RoleConfig{Role: Role{Name: "lead", Kind: RoleKindBusiness}, Implies: []string{"ghost"}}))
		holder, err := sq.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		if _, err := holder.ExecContext(ctx, `DELETE FROM roles WHERE name = 'ghost'`); err != nil {
			t.Fatal(err)
		}
		published := make(chan error, 1)
		go func() {
			_, err := s.Drafts().Publish(ctx, plan)
			published <- err
		}()
		waitForLockWait(t, s, `l.locktype IN ('transactionid', 'tuple')`)
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		var pc PublishConflict
		if err := <-published; !errors.As(err, &pc) || pc != (PublishConflict{Object: ObjectRef{kindRole, "lead"}}) {
			t.Errorf("the publish answered %v; want the conflict on Role/lead", err)
		}
	})
}

func TestPublishRefusesAPlanItCannotWrite(t *testing.T) {
	put := appPut("demo", manifestFor("demo", "https://one.example/mcp"))
	put.BaseOp = opRemove
	noBase := put
	noBase.BaseOp = ""
	offApp := put
	offApp.Op = opOff
	impliedApp := put
	impliedApp.Implied = true
	noDoc := put
	noDoc.App = nil
	afterRemove := removal(kindRole, "ghost")
	afterRemove.BaseOp, afterRemove.After = opRemove, "not empty"
	cases := []struct {
		name string
		plan PublishPlan
		want string
	}{
		{"no records", PublishPlan{DraftID: 1}, "has no Records"},
		{"no draft", PublishPlan{Records: recordEach}, "names no draft"},
		{"a slot close without a slot", PublishPlan{DraftID: 1, Records: recordEach, Close: []SlotClose{{Reason: "x"}}}, "without naming the slot"},
		{"an object named twice", PublishPlan{DraftID: 1, Records: recordEach, Items: []PlanItem{put, put}}, "names App/demo twice"},
		{"an item never checked", PublishPlan{DraftID: 1, Records: recordEach, Items: []PlanItem{noBase}}, "has no base"},
		{"a server turned off", PublishPlan{DraftID: 1, Records: recordEach, Items: []PlanItem{offApp}}, "not a PolicySet"},
		{"an implied server", PublishPlan{DraftID: 1, Records: recordEach, Items: []PlanItem{impliedApp}}, "only a Role put or remove"},
		{"a put without its document", PublishPlan{DraftID: 1, Records: recordEach, Items: []PlanItem{noDoc}}, "no document"},
		{"a removal that leaves something", PublishPlan{DraftID: 1, Records: recordEach, Items: []PlanItem{afterRemove}}, "removes the object and leaves"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				before := dbDump(t, s)
				_, err := s.Drafts().Publish(context.Background(), tc.plan)
				if err == nil || errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("Publish answered %v; want a refusal saying %q", err, tc.want)
				}
				sameDump(t, before, dbDump(t, s))
			})
		})
	}
}
