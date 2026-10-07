package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// recordsWorld is the live state the records tests read: the servers
// github and jira with their admin roles, jira-triage owned by jira, and
// the roles readers, devs, ops, old and jira-users with their edges and
// access rows, and the set guard on.
func recordsWorld() drafts.World {
	return drafts.World{
		Apps: map[string]drafts.App{
			"github": {ID: "app-gh", Name: "github", AdminRole: "mcp-admin-github"},
			"jira":   {ID: "app-jira", Name: "jira", AdminRole: "mcp-admin-jira"},
		},
		Roles: map[string]drafts.Role{
			"mcp-admin-github": {ID: "r-adm-gh", Name: "mcp-admin-github"},
			"mcp-admin-jira":   {ID: "r-adm-jira", Name: "mcp-admin-jira"},
			"jira-triage":      {ID: "r-jt", Name: "jira-triage", Owned: true, Owner: "jira"},
			"jira-users":       {ID: "r-ju", Name: "jira-users"},
			"readers":          {ID: "r-readers", Name: "readers", Description: "Readers."},
			"devs":             {ID: "r-devs", Name: "devs", Description: "Developers."},
			"ops":              {ID: "r-ops", Name: "ops"},
			"old":              {ID: "r-old", Name: "old"},
		},
		Implies: map[string][]string{
			"jira-triage": {"readers"}, "ops": {"jira-triage"}, "old": {"readers"}, "devs": {"readers"}, "mcp-admin-jira": {"readers"},
		},
		Access: map[string]drafts.Access{
			"jira-triage": {ID: "b-jt", Server: "jira", Tools: []string{"*"}},
			"jira-users":  {ID: "b-ju", Server: "jira", Tools: []string{"get_*"}},
			"devs":        {ID: "b-devs", Server: "github", Tools: []string{"get_*"}},
			"old":         {ID: "b-old", Server: "github", Tools: []string{"*"}},
		},
		Policies: map[string]drafts.Policy{"guard": {Name: "guard", Text: "old text"}},
	}
}

// renderRecords writes each record as its subject and its data as JSON,
// with the draft field left out, after checking that every record names
// the published draft 41, or, for a draft.discard, names it in closedBy.
func renderRecords(t *testing.T, recs []record) []string {
	t.Helper()
	out := make([]string, len(recs))
	for i, rec := range recs {
		data := map[string]any{}
		for k, v := range rec.data {
			data[k] = v
		}
		switch {
		case data["action"] == "draft.discard":
			if data["closedBy"] != "41" {
				t.Errorf("record %d, %v, is closed by %v, want draft 41", i, data, data["closedBy"])
			}
		case data["draft"] != "41":
			t.Errorf("record %d, %s %v, names the draft %v, want 41", i, rec.subject, data, data["draft"])
		}
		if data["action"] != "draft.discard" {
			delete(data, "draft")
		}
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = rec.subject + " " + string(raw)
	}
	return out
}

// TestPublishRecordsFollowThePlan pins the publish records, each
// written once, in plan order, then the ones every publish writes once.
func TestPublishRecordsFollowThePlan(t *testing.T) {
	t.Parallel()
	const admin, identity, apps, removed, policyUpdated = "straza.audit.admin ", "straza.identity.updated ", "straza.apps.updated ", "straza.apps.removed ", "straza.policy.updated "
	publishEvent := func(names string) string {
		return apps + `{"apps":[` + names + `],"change":"publish","snapshot":"snap-2"}`
	}
	role := func(id, name string) store.Role { return store.Role{ID: id, Name: name} }
	put := func(kind, name string) store.PlanItem {
		return store.PlanItem{Ref: store.ObjectRef{Kind: kind, Name: name}, Op: "put"}
	}
	remove := func(kind, name string) store.PlanItem {
		return store.PlanItem{Ref: store.ObjectRef{Kind: kind, Name: name}, Op: "remove"}
	}
	withApp := func(it store.PlanItem, runtime string) store.PlanItem {
		it.App = &store.App{Name: it.Ref.Name, RuntimeKind: runtime}
		return it
	}
	withRole := func(it store.PlanItem, cfg store.RoleConfig) store.PlanItem {
		it.Role = &cfg
		return it
	}
	withSet := func(it store.PlanItem, op string, priority int) store.PlanItem {
		it.Op, it.Policy = op, &store.PolicySet{Name: it.Ref.Name, Priority: priority}
		return it
	}
	implied := func(it store.PlanItem) store.PlanItem {
		it.Implied = true
		return it
	}
	ref := func(kind, name string) store.ObjectRef { return store.ObjectRef{Kind: kind, Name: name} }
	fields := &publishFields{Revision: 2, Items: []drafts.Item{{Kind: drafts.KindRole, Name: "helpers", Op: drafts.OpPut, Doc: "doc"}},
		RiskDigest: "d-new", ReviewedDigest: "d-old", Acknowledged: []string{"k1"}, Typed: []string{"k2"},
		Proposer: store.DraftActor{ID: "tok-1", Name: "ci", Via: laneAdminAPI}, Client: "console"}
	undo := *fields
	undo.Reverts = "12"
	cases := []struct {
		name    string
		items   []store.PlanItem
		changed map[string][]string
		changes map[string][]byte
		sets    int
		publish *publishFields
		out     store.PublishOutcome
		want    []string
	}{
		{
			name:  "a new server",
			items: []store.PlanItem{withApp(put("App", "docs"), "remote")},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("App", "docs"), Op: "put", Created: true, ID: "app-docs", AdminRole: role("r-adm-docs", "mcp-admin-docs")},
			}},
			want: []string{
				admin + `{"action":"apps.install","adminRole":"mcp-admin-docs","app":"docs","runtime":"remote","update":false}`,
				identity + `{"id":"r-adm-docs"}`,
				publishEvent(`"docs"`),
			},
		},
		{
			name:    "a changed server",
			items:   []store.PlanItem{withApp(put("App", "github"), "remote")},
			changed: map[string][]string{"github": {"straza.runtime.remote.url"}},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("App", "github"), Op: "put", ID: "app-gh", AdminRole: role("r-adm-gh", "mcp-admin-github")},
			}},
			want: []string{
				admin + `{"action":"apps.install","adminRole":"mcp-admin-github","app":"github","changed":["straza.runtime.remote.url"],"runtime":"remote","update":true}`,
				publishEvent(`"github"`),
			},
		},
		{
			name:  "a changed server whose manifests cannot be compared",
			items: []store.PlanItem{withApp(put("App", "github"), "remote")},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("App", "github"), Op: "put", ID: "app-gh", AdminRole: role("r-adm-gh", "mcp-admin-github")},
			}},
			want: []string{
				admin + `{"action":"apps.install","adminRole":"mcp-admin-github","app":"github","runtime":"remote","update":true}`,
				publishEvent(`"github"`),
			},
		},
		{
			name: "a removed server with its admin role, an owned role and the roles that lose a row or an edge",
			items: []store.PlanItem{remove("App", "jira"), implied(remove("Role", "jira-triage")), implied(put("Role", "jira-users")),
				implied(put("Role", "ops"))},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("App", "jira"), Op: "remove", ID: "app-jira", AdminRole: role("r-adm-jira", "mcp-admin-jira"),
					Ended:   []store.RoleAssignment{{ID: "as-1", SubjectID: "u-1", RoleID: "r-adm-jira", Origin: "admin"}},
					Removed: []store.Role{role("r-adm-jira", "mcp-admin-jira")}, LostAccess: []string{"jira-triage", "jira-users"}},
				{Ref: ref("Role", "jira-triage"), Op: "remove", Implied: true, ID: "r-jt",
					Ended:   []store.RoleAssignment{{ID: "as-2", SubjectID: "u-2", RoleID: "r-jt", Origin: "scim"}},
					Removed: []store.Role{role("r-jt", "jira-triage")}},
				{Ref: ref("Role", "jira-users"), Op: "put", Implied: true, ID: "r-ju"},
				{Ref: ref("Role", "ops"), Op: "put", Implied: true, ID: "r-ops", ImpliesRemoved: []string{"jira-triage"}},
			}},
			want: []string{
				admin + `{"action":"roles.unassign","origin":"admin","reason":"server removed","role":"mcp-admin-jira","roleId":"r-adm-jira","target":"as-1","user":"u-1"}`,
				admin + `{"action":"roles.implication.delete","implies":"readers","impliesId":"r-readers","role":"mcp-admin-jira","target":"r-adm-jira"}`,
				admin + `{"action":"roles.delete","reason":"removed with the server jira","role":"mcp-admin-jira","target":"r-adm-jira"}`,
				admin + `{"action":"roles.unassign","origin":"scim","reason":"server removed","role":"jira-triage","roleId":"r-jt","target":"as-2","user":"u-2"}`,
				admin + `{"action":"roles.implication.delete","implies":"readers","impliesId":"r-readers","role":"jira-triage","target":"r-jt"}`,
				admin + `{"action":"roles.delete","reason":"removed with the server jira","role":"jira-triage","server":"jira","target":"r-jt"}`,
				admin + `{"action":"apps.remove","adminRole":"mcp-admin-jira","app":"jira","roles":["jira-triage","jira-users"]}`,
				removed + `{"app":"app-jira","name":"jira"}`,
				admin + `{"action":"roles.implication.delete","implies":"jira-triage","impliesId":"r-jt","role":"ops","target":"r-ops"}`,
				identity + `{"id":"r-adm-jira"}`,
				identity + `{"id":"r-jt"}`,
				identity + `{"id":"r-ju"}`,
				identity + `{"id":"r-ops"}`,
				publishEvent(`"jira"`),
			},
		},
		{
			name: "a new global role with an access row and an edge",
			items: []store.PlanItem{withRole(put("Role", "helpers"), store.RoleConfig{Role: store.Role{Name: "helpers", Description: "Helpers."},
				Server: "github", Tools: []string{"get_*"}, Implies: []string{"readers"}})},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("Role", "helpers"), Op: "put", Created: true, ID: "r-h",
					BindingsAdded: []store.ToolBinding{{ID: "b-h", RoleID: "r-h", AppID: "app-gh", ToolMatcher: `["get_*"]`}}, ImpliesAdded: []string{"readers"}},
			}},
			want: []string{
				admin + `{"action":"roles.create","role":"helpers","target":"r-h"}`,
				admin + `{"action":"apps.binding.create","app":"github","role":"helpers","tools":["get_*"]}`,
				admin + `{"action":"roles.implication.create","implies":"readers","impliesId":"r-readers","role":"helpers","target":"r-h"}`,
				identity + `{"id":"r-h"}`,
				apps + `{"app":"github","change":"binding"}`,
				publishEvent(``),
			},
		},
		{
			name: "a new role github owns that implies a role the same publish creates",
			items: []store.PlanItem{
				withRole(put("Role", "newbie"), store.RoleConfig{Role: store.Role{Name: "newbie"}}),
				withRole(put("Role", "github-helpers"), store.RoleConfig{Role: store.Role{Name: "github-helpers"}, Owner: "github",
					Server: "github", Tools: []string{"*"}, Implies: []string{"newbie"}}),
			},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("Role", "newbie"), Op: "put", Created: true, ID: "r-new"},
				{Ref: ref("Role", "github-helpers"), Op: "put", Created: true, ID: "r-gh",
					BindingsAdded: []store.ToolBinding{{ID: "b-gh", RoleID: "r-gh", AppID: "app-gh", ToolMatcher: `["*"]`}}, ImpliesAdded: []string{"newbie"}},
			}},
			want: []string{
				admin + `{"action":"roles.create","role":"newbie","target":"r-new"}`,
				admin + `{"action":"roles.create","role":"github-helpers","server":"github","target":"r-gh"}`,
				admin + `{"action":"apps.binding.create","app":"github","role":"github-helpers","tools":["*"]}`,
				admin + `{"action":"roles.implication.create","implies":"newbie","impliesId":"r-new","role":"github-helpers","target":"r-gh"}`,
				identity + `{"id":"r-new"}`,
				identity + `{"id":"r-gh"}`,
				apps + `{"app":"github","change":"binding"}`,
				publishEvent(``),
			},
		},
		{
			name: "a changed role with a new description, its row moved, one edge dropped and one added",
			items: []store.PlanItem{withRole(put("Role", "devs"), store.RoleConfig{Role: store.Role{Name: "devs", Description: "Builders."},
				Server: "jira", Tools: []string{"*"}, Implies: []string{"ops"}})},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("Role", "devs"), Op: "put", ID: "r-devs",
					BindingsRemoved: []store.ToolBinding{{ID: "b-devs", RoleID: "r-devs", AppID: "app-gh", ToolMatcher: `["get_*"]`}},
					BindingsAdded:   []store.ToolBinding{{ID: "b-new", RoleID: "r-devs", AppID: "app-jira", ToolMatcher: `["*"]`}},
					ImpliesRemoved:  []string{"readers"}, ImpliesAdded: []string{"ops"}},
			}},
			want: []string{
				admin + `{"action":"roles.update","role":"devs","target":"r-devs"}`,
				admin + `{"action":"apps.binding.delete","app":"github","bindingId":"b-devs","role":"devs","tools":["get_*"]}`,
				admin + `{"action":"apps.binding.create","app":"jira","role":"devs","tools":["*"]}`,
				admin + `{"action":"roles.implication.delete","implies":"readers","impliesId":"r-readers","role":"devs","target":"r-devs"}`,
				admin + `{"action":"roles.implication.create","implies":"ops","impliesId":"r-ops","role":"devs","target":"r-devs"}`,
				identity + `{"id":"r-devs"}`,
				apps + `{"app":"github","change":"binding"}`,
				apps + `{"app":"jira","change":"binding"}`,
				publishEvent(``),
			},
		},
		{
			name: "a role that only gains an edge keeps its description",
			items: []store.PlanItem{withRole(put("Role", "readers"), store.RoleConfig{Role: store.Role{Name: "readers", Description: "Readers."},
				Implies: []string{"ops"}})},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("Role", "readers"), Op: "put", ID: "r-readers", ImpliesAdded: []string{"ops"}},
			}},
			want: []string{
				admin + `{"action":"roles.implication.create","implies":"ops","impliesId":"r-ops","role":"readers","target":"r-readers"}`,
				identity + `{"id":"r-readers"}`,
				publishEvent(``),
			},
		},
		{
			name:  "a removed role with a holder, an edge out and an access row",
			items: []store.PlanItem{remove("Role", "old")},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("Role", "old"), Op: "remove", ID: "r-old", Removed: []store.Role{role("r-old", "old")},
					Ended: []store.RoleAssignment{{ID: "as-3", SubjectID: "u-3", RoleID: "r-old", Origin: "admin"}}},
			}},
			want: []string{
				admin + `{"action":"roles.unassign","origin":"admin","reason":"role deleted","role":"old","roleId":"r-old","target":"as-3","user":"u-3"}`,
				admin + `{"action":"roles.implication.delete","implies":"readers","impliesId":"r-readers","role":"old","target":"r-old"}`,
				admin + `{"action":"roles.delete","role":"old","target":"r-old"}`,
				identity + `{"id":"r-old"}`,
				apps + `{"app":"github","change":"binding"}`,
				publishEvent(``),
			},
		},
		{
			name:  "two removed roles with an edge between them record it once",
			items: []store.PlanItem{remove("Role", "devs"), remove("Role", "readers")},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("Role", "devs"), Op: "remove", ID: "r-devs", Removed: []store.Role{role("r-devs", "devs")}},
				{Ref: ref("Role", "readers"), Op: "remove", ID: "r-readers", Removed: []store.Role{role("r-readers", "readers")}},
			}},
			want: []string{
				admin + `{"action":"roles.implication.delete","implies":"readers","impliesId":"r-readers","role":"devs","target":"r-devs"}`,
				admin + `{"action":"roles.delete","role":"devs","target":"r-devs"}`,
				admin + `{"action":"roles.delete","role":"readers","target":"r-readers"}`,
				identity + `{"id":"r-devs"}`,
				identity + `{"id":"r-readers"}`,
				apps + `{"app":"github","change":"binding"}`,
				publishEvent(``),
			},
		},
		{
			name:    "a set created on",
			items:   []store.PlanItem{withSet(put("PolicySet", "fresh"), "put", 10)},
			changes: map[string][]byte{"fresh": []byte("text")},
			sets:    3,
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("PolicySet", "fresh"), Op: "put", Created: true, ID: "ps-1", TextChanged: true, On: true},
			}},
			want: []string{
				admin + `{"action":"policy.create","id":"ps-1","name":"fresh","priority":10}`,
				admin + `{"action":"policy.activate","id":"ps-1","name":"fresh","snapshot":"snap-2"}`,
				policyUpdated + `{"sets":3,"snapshot":"snap-2"}`,
				publishEvent(``),
			},
		},
		{
			name:    "a set changed while on",
			items:   []store.PlanItem{withSet(put("PolicySet", "guard"), "put", 20)},
			changes: map[string][]byte{"guard": []byte("new text")},
			sets:    1,
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("PolicySet", "guard"), Op: "put", ID: "ps-g", TextChanged: true, WasOn: true, On: true},
			}},
			want: []string{
				admin + `{"action":"policy.update","id":"ps-g","name":"guard","priority":20}`,
				admin + `{"action":"policy.activate","id":"ps-g","name":"guard","snapshot":"snap-2"}`,
				policyUpdated + `{"sets":1,"snapshot":"snap-2"}`,
				publishEvent(``),
			},
		},
		{
			name:    "a set turned off",
			items:   []store.PlanItem{withSet(put("PolicySet", "guard"), "off", 20)},
			changes: map[string][]byte{"guard": nil},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("PolicySet", "guard"), Op: "off", ID: "ps-g", WasOn: true},
			}},
			want: []string{
				admin + `{"action":"policy.deactivate","id":"ps-g","name":"guard","snapshot":"snap-2"}`,
				policyUpdated + `{"sets":0,"snapshot":"snap-2"}`,
				publishEvent(``),
			},
		},
		{
			name:    "a set removed while on",
			items:   []store.PlanItem{remove("PolicySet", "guard")},
			changes: map[string][]byte{"guard": nil},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("PolicySet", "guard"), Op: "remove", ID: "ps-g", WasOn: true},
			}},
			want: []string{
				admin + `{"action":"policy.deactivate","id":"ps-g","name":"guard","snapshot":"snap-2"}`,
				admin + `{"action":"policy.delete","id":"ps-g","name":"guard"}`,
				policyUpdated + `{"sets":0,"snapshot":"snap-2"}`,
				publishEvent(``),
			},
		},
		{
			name:  "a set stored off with a new text",
			items: []store.PlanItem{withSet(put("PolicySet", "spare"), "off", 5)},
			out: store.PublishOutcome{Items: []store.ItemOutcome{
				{Ref: ref("PolicySet", "spare"), Op: "off", ID: "ps-s", TextChanged: true},
			}},
			want: []string{
				admin + `{"action":"policy.update","id":"ps-s","name":"spare","priority":5}`,
				publishEvent(``),
			},
		},
		{
			name:  "a closed slot draft on a direct route",
			items: []store.PlanItem{withSet(put("PolicySet", "spare"), "off", 5)},
			out: store.PublishOutcome{Items: []store.ItemOutcome{{Ref: ref("PolicySet", "spare"), Op: "off", ID: "ps-s", TextChanged: true}},
				Closed: []store.ClosedDraft{{ID: 7, Revision: 2, Reason: "draft 41 turned spare off"}}},
			want: []string{
				admin + `{"action":"policy.update","id":"ps-s","name":"spare","priority":5}`,
				admin + `{"action":"draft.discard","closedBy":"41","draft":"7","reason":"draft 41 turned spare off","revision":2}`,
				publishEvent(``),
			},
		},
		{
			name:    "a publish equal to live on the drafts route",
			publish: fields,
			want: []string{
				publishEvent(``),
				admin + `{"acknowledged":["k1"],"action":"draft.publish","client":"console","items":[{"kind":"Role","name":"helpers","op":"put"}],` +
					`"proposer":"ci","proposerId":"tok-1","proposerVia":"api-token","reviewedDigest":"d-old","revision":2,"riskDigest":"d-new","snapshot":"snap-2","typed":["k2"]}`,
			},
		},
		{
			name:    "an undo on the drafts route",
			publish: &undo,
			want: []string{
				publishEvent(``),
				admin + `{"acknowledged":["k1"],"action":"draft.publish","client":"console","items":[{"kind":"Role","name":"helpers","op":"put"}],` +
					`"proposer":"ci","proposerId":"tok-1","proposerVia":"api-token","reverts":"12","reviewedDigest":"d-old","revision":2,"riskDigest":"d-new","snapshot":"snap-2","typed":["k2"]}`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rc := recordContext{w: recordsWorld(), items: tc.items, changed: tc.changed, changes: tc.changes,
				built: len(tc.changes) > 0, sets: tc.sets, publish: tc.publish}
			out := tc.out
			out.DraftID, out.Snapshot = 41, "snap-2"
			got := renderRecords(t, publishRecords(rc, out))
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("records:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// TestPublishRecordsCarryTheEnvelope pins the envelope recordsFor puts
// around every record of a publish: this replica's instance as the source,
// so its converge consumer skips it, the subject as the type, and the
// publisher as the actor on the straza.audit.admin records alone.
func TestPublishRecordsCarryTheEnvelope(t *testing.T) {
	t.Parallel()
	a := &App{instance: "strazad/test-instance"}
	ctx := withActor(context.Background(), auditActor{Name: "kim", ID: "u-kim", Via: laneSession})
	rc := recordContext{w: recordsWorld(), items: []store.PlanItem{{Ref: store.ObjectRef{Kind: "Role", Name: "old"}, Op: "remove"}}}
	events, err := a.recordsFor(ctx, rc)(store.PublishOutcome{DraftID: 41, Snapshot: "snap-2", Items: []store.ItemOutcome{
		{Ref: store.ObjectRef{Kind: "Role", Name: "old"}, Op: "remove", ID: "r-old", Removed: []store.Role{{ID: "r-old", Name: "old"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("events = %d, want the implication, the removal, identity, binding and publish events", len(events))
	}
	for _, ev := range events {
		var ce struct {
			Type   string         `json:"type"`
			Source string         `json:"source"`
			ID     string         `json:"id"`
			Data   map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(ev.CE), &ce); err != nil {
			t.Fatalf("%s: %v", ev.CE, err)
		}
		audit := ev.Subject == "straza.audit.admin"
		_, hasActor := ce.Data["actor"]
		switch {
		case ce.Type != ev.Subject || ce.Source != a.instance || ce.ID == "":
			t.Errorf("envelope %s, want type %s from %s with an id", ev.CE, ev.Subject, a.instance)
		case audit && (ce.Data["actor"] != "kim" || ce.Data["actorId"] != "u-kim" || ce.Data["actorVia"] != laneSession):
			t.Errorf("audit record %v, want kim as its actor", ce.Data)
		case !audit && hasActor:
			t.Errorf("%s carries an actor: %v", ev.Subject, ce.Data)
		}
	}
}
