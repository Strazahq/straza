package drafts

import (
	"reflect"
	"testing"
)

// deskWorld is gainWorld with desk, a server that pins tickets: the calls
// of ticket_update name the ticket, whoami touches none, and the object
// list holds T-1, T-2 and T-3. desk-triage and desk-spare cover T-1.
// frank holds support, a business role that implies desk-triage, and
// nobody holds desk-spare.
func deskWorld() World {
	w := gainWorld()
	w.Apps["desk"] = App{ID: "a9", Name: "desk", Status: "running", Runtime: "remote", URL: "https://desk.example.com/mcp", Credential: CredentialNone,
		Offered: []string{"ticket_list", "ticket_update", "whoami"}, Exposure: []string{"*"}, AdminRole: "mcp-admin-desk", RolePrefix: "desk-",
		Pinned: &PinnedObjects{Type: "ticket", Plural: "tickets", ListTool: "ticket_list", Credential: "set", LiveType: "ticket",
			Reach: map[string]string{"ticket_update": reachArgument, "whoami": reachNone}, Argument: map[string]string{"ticket_update": "ticket"},
			IDs: map[string]string{"T-1": "t1", "T-2": "t2", "T-3": "t3"}}}
	for _, r := range []string{"desk-spare", "desk-triage"} {
		w.Roles[r] = Role{ID: "id-" + r, Name: r, Kind: RoleKindApplication, Plane: PlaneAccess, Owner: "desk", Owned: true}
		w.Access[r] = Access{ID: "b-" + r, Server: "desk", Tools: []string{"ticket_update", "whoami"}, Covers: &Scope{Objects: []ObjectRef{{Value: "T-1", ID: "t1"}}}}
	}
	w.Roles["support"] = Role{ID: "id-support", Name: "support", Kind: RoleKindBusiness, Plane: PlaneAccess}
	w.Implies["support"] = []string{"desk-triage"}
	w.Holders["support"] = []Holder{{Username: "frank", UserType: "human", AgencyMode: "interactive"}}
	for _, object := range []string{"App/desk", "Role/desk-spare", "Role/desk-triage", "Role/support"} {
		w.Fingerprints[object] = Fingerprint("fp-" + object)
	}
	return w
}

// deskRole is the Role put of the desk role name with both desk tools and
// the covers line given as YAML.
func deskRole(name, covers string) Item {
	return coversRole(name, "desk", "[ticket_update, whoami]", covers)
}

// coversLive sets the live covers line of the desk role name.
func coversLive(name string, objects ...ObjectRef) func(*World) {
	return func(w *World) {
		acc := w.Access[name]
		acc.Covers = &Scope{Objects: objects}
		w.Access[name] = acc
	}
}

// TestCoversWiderRisk pins role.covers-wider: a held role whose covers line
// comes to reach more tickets raises one tick line, once however many
// business roles imply it, that names the tickets it adds or every ticket,
// and names no holder and no count. A narrowing, a rename and a role
// nobody holds raise none.
func TestCoversWiderRisk(t *testing.T) {
	t.Parallel()
	escalation := func(w *World) {
		w.Roles["escalation"] = Role{ID: "id-escalation", Name: "escalation", Kind: RoleKindBusiness, Plane: PlaneAccess}
		w.Implies["escalation"] = []string{"desk-triage"}
		w.Holders["escalation"] = []Holder{{Username: "gail", UserType: "human", AgencyMode: "interactive"}}
	}
	cases := []struct {
		name  string
		live  func(*World)
		items []Item
		want  []string
	}{
		{"one more ticket", nil, []Item{deskRole("desk-triage", "{objects: [T-1, T-2]}")}, []string{
			"role.covers-wider Role/desk-triage (tick) desk-triage will reach more tickets on desk: T-2. | T-1 | T-1, T-2"}},
		{"two more tickets", nil, []Item{deskRole("desk-triage", "{objects: [T-3, T-1, T-2]}")}, []string{
			"role.covers-wider Role/desk-triage (tick) desk-triage will reach more tickets on desk: T-2 and T-3. | T-1 | T-1, T-2, T-3"}},
		{"another ticket in place of one", nil, []Item{deskRole("desk-triage", "{objects: [T-2]}")}, []string{
			"role.covers-wider Role/desk-triage (tick) desk-triage will reach more tickets on desk: T-2. | T-1 | T-2"}},
		{"every ticket", nil, []Item{deskRole("desk-triage", "every")}, []string{
			"role.covers-wider Role/desk-triage (tick) desk-triage will reach every ticket on desk, including tickets added later. | T-1 | every ticket"}},
		{"two business roles imply it", escalation, []Item{deskRole("desk-triage", "{objects: [T-1, T-2]}")}, []string{
			"role.covers-wider Role/desk-triage (tick) desk-triage will reach more tickets on desk: T-2. | T-1 | T-1, T-2"}},
		{"fewer tickets", coversLive("desk-triage", ObjectRef{Value: "T-1", ID: "t1"}, ObjectRef{Value: "T-2", ID: "t2"}),
			[]Item{deskRole("desk-triage", "{objects: [T-1]}")}, []string{}},
		{"from every ticket to some", func(w *World) {
			acc := w.Access["desk-triage"]
			acc.Covers = &Scope{Every: true}
			w.Access["desk-triage"] = acc
		}, []Item{deskRole("desk-triage", "{objects: [T-1, T-2, T-3]}")}, []string{}},
		{"a rename", coversLive("desk-triage", ObjectRef{Value: "T-1-old", ID: "t1"}), []Item{deskRole("desk-triage", "{objects: [T-1]}")}, []string{}},
		{"a role nobody holds", nil, []Item{deskRole("desk-spare", "every")}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := deskWorld()
			if tc.live != nil {
				tc.live(&w)
			}
			v := Check(w, stamped(w, tc.items...), CheckInput{Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			if got := only(riskLines(v.Risks), "role.covers-wider"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Risks\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestCoverageListsEveryRoleTheDraftPutsOnAPinnedServer pins
// Verdict.Objects: one entry per role the draft puts or removes on a server
// that pins an object type, in the server's own words, with the holders
// and the tickets each covers now and after, sorted by server and role. It
// shows a widening that changes no tool outcome, and a role nobody holds.
func TestCoverageListsEveryRoleTheDraftPutsOnAPinnedServer(t *testing.T) {
	t.Parallel()
	w := deskWorld()
	t1, t2 := ObjectName{ID: "t1", Value: "T-1"}, ObjectName{ID: "t2", Value: "T-2"}
	v := Check(w, stamped(w, deskRole("desk-triage", "{objects: [T-2, T-1]}"), deskRole("desk-spare", "every"),
		gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me, push_files]\n")), CheckInput{Now: checkNow})
	if len(v.Refused) > 0 {
		t.Fatalf("refused: %q", findingLines(v.Refused))
	}
	want := []Coverage{
		{Role: "desk-spare", Server: "desk", Type: "ticket", Plural: "tickets", EveryAfter: true, Before: []ObjectName{t1}, After: []ObjectName{}},
		{Role: "desk-triage", Server: "desk", Type: "ticket", Plural: "tickets", Holders: []string{"frank"}, HolderCount: 1, Before: []ObjectName{t1}, After: []ObjectName{t1, t2}},
	}
	if !reflect.DeepEqual(v.Objects, want) {
		t.Errorf("Objects\n got %+v\nwant %+v", v.Objects, want)
	}
	for _, g := range v.Gains {
		if g.Server == "desk" {
			t.Errorf("a covers line alone changed a tool outcome: %+v", g)
		}
	}
}

// TestCoverageFollowsThePin pins the entries a change of a server's pin
// adds: every role with a row on the server, as it covers today and after,
// so an unpin shows each role on some tickets coming to reach every ticket,
// and raises role.covers-wider for the role someone holds.
func TestCoverageFollowsThePin(t *testing.T) {
	t.Parallel()
	w := deskWorld()
	app := w.Apps["desk"]
	app.Manifest, app.Pinned = `{"after":true}`, nil
	v := Check(w, stamped(w, appPut("desk")), CheckInput{Apps: map[string]App{"desk": app}, Now: checkNow})
	if len(v.Refused) > 0 {
		t.Fatalf("refused: %q", findingLines(v.Refused))
	}
	t1 := ObjectName{ID: "t1", Value: "T-1"}
	want := []Coverage{
		{Role: "desk-spare", Server: "desk", Type: "ticket", Plural: "tickets", EveryAfter: true, Before: []ObjectName{t1}, After: []ObjectName{}},
		{Role: "desk-triage", Server: "desk", Type: "ticket", Plural: "tickets", Holders: []string{"frank"}, HolderCount: 1, EveryAfter: true, Before: []ObjectName{t1}, After: []ObjectName{}},
	}
	if !reflect.DeepEqual(v.Objects, want) {
		t.Errorf("Objects\n got %+v\nwant %+v", v.Objects, want)
	}
	wantRisk := []string{"role.covers-wider Role/desk-triage (tick) desk-triage will reach every ticket on desk, including tickets added later. | T-1 | every ticket"}
	if got := only(riskLines(v.Risks), "role.covers-wider"); !reflect.DeepEqual(got, wantRisk) {
		t.Errorf("Risks\n got %q\nwant %q", got, wantRisk)
	}
}

// TestCoverageIsEmptyOrNil pins the shape of Verdict.Objects: an empty
// list for a check that read who gains and found no role on a pinned
// server, and for one that stopped after the refusals with RefusalsOnly,
// and nil for a check a refusal ended.
func TestCoverageIsEmptyOrNil(t *testing.T) {
	t.Parallel()
	w := deskWorld()
	readers := gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me, push_files]\n")
	for name, in := range map[string]CheckInput{"a full check": {Now: checkNow}, "RefusalsOnly": {Now: checkNow, RefusalsOnly: true}} {
		if v := Check(w, stamped(w, readers), in); v.Objects == nil || len(v.Objects) != 0 {
			t.Errorf("%s: Objects = %#v, want an empty list", name, v.Objects)
		}
	}
	refused := Check(w, stamped(w, deskRole("desk-triage", "{objects: [T-9]}")), CheckInput{Now: checkNow})
	if len(refused.Refused) == 0 || refused.Objects != nil {
		t.Errorf("a refused check: refused %q, Objects %#v, want nil", findingLines(refused.Refused), refused.Objects)
	}
}
