package server

import (
	"context"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// stubGraph is Entra with one member list for every group it is asked to
// make, enough to drive the server's wiring of the sync.
type stubGraph struct {
	names   map[string]string // uniqueName to group id
	members []entragraph.Member
	// identities are the agent identities every group lists, and agents
	// what Entra says about each. One that agents does not hold is gone.
	identities []entragraph.Member
	agents     map[string]entragraph.Agent
}

func (g *stubGraph) EnsureGroup(_ context.Context, spec entragraph.GroupSpec) (entragraph.Group, error) {
	if g.names == nil {
		g.names = map[string]string{}
	}
	if _, ok := g.names[spec.UniqueName]; !ok {
		g.names[spec.UniqueName] = "group-of-" + spec.UniqueName
	}
	return entragraph.Group{ID: g.names[spec.UniqueName], DisplayName: spec.DisplayName, Description: spec.Description, UniqueName: spec.UniqueName}, nil
}

func (g *stubGraph) GroupByUniqueName(_ context.Context, name string) (entragraph.Group, error) {
	return entragraph.Group{ID: g.names[name], UniqueName: name}, nil
}
func (g *stubGraph) UpdateGroup(context.Context, string, string, string) error { return nil }
func (g *stubGraph) DeleteGroup(context.Context, string) error                 { return nil }
func (g *stubGraph) TransitiveMembers(context.Context, string) ([]entragraph.Member, error) {
	return g.members, nil
}
func (g *stubGraph) UsersDelta(context.Context, string) (entragraph.UsersResult, error) {
	return entragraph.UsersResult{Link: "https://graph.example/users"}, nil
}
func (g *stubGraph) AgentIdentities(context.Context, string) ([]entragraph.Member, error) {
	return g.identities, nil
}
func (g *stubGraph) AgentUserParents(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (g *stubGraph) Agents(_ context.Context, ids []string) (map[string]entragraph.Agent, error) {
	out := map[string]entragraph.Agent{}
	for _, id := range ids {
		a, ok := g.agents[id]
		if !ok {
			a = entragraph.Agent{ID: id, Missing: true}
		}
		out[id] = a
	}
	return out, nil
}
func (g *stubGraph) Delta(context.Context, string, []string) (entragraph.DeltaResult, error) {
	return entragraph.DeltaResult{Link: "https://graph.example/delta"}, nil
}

// entraSyncApp is a test app whose Entra sync reads the kinds read, reads
// every group in every pass and has the default removal guard. The config
// is set before the app runs, so no test writes it beside the run loop.
// The identity.entra section stays off, so the app starts no sync of its
// own and the test drives one over a stub.
func entraSyncApp(t *testing.T, read ...string) *App {
	t.Helper()
	app, _ := testAppPreRun(t, []func(*App){func(a *App) {
		a.cfg.Identity.Entra.Read = read
		a.cfg.Identity.Entra.ReconcileInterval = time.Nanosecond
		a.cfg.Identity.Entra.RemovalGuard.Percent, a.cfg.Identity.Entra.RemovalGuard.MinMembers = 50, 5
	}})
	return app
}

// TestEntraSyncAudit pins what the server writes when the Entra sync gives
// and takes a role: one roles.assign and one roles.unassign record with the
// user, the role, the origin entra, a reason and no actor, one record for
// the group's create, and the cached subject of the user who lost the role
// dropped.
func TestEntraSyncAudit(t *testing.T) {
	t.Parallel()
	app := entraSyncApp(t, "people")
	ctx := context.Background()
	roles, err := app.store.Roles().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	role, err := app.store.Roles().Create(ctx, store.Role{Name: "entra-audit", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := app.store.Users().Create(ctx, store.User{Username: "bob", ExternalID: "oid-bob"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := app.store.Sessions().Create(ctx, store.Session{UserID: bob.ID, HarnessName: "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	graph := &stubGraph{}
	syncer := app.entraSyncer(graph)

	syncer.Pass(ctx) // creates the groups
	enabled := true
	graph.members = []entragraph.Member{{ID: "oid-bob", Kind: entragraph.KindUser, Enabled: &enabled, UPN: "bob"}}
	syncer.Pass(ctx)
	syncer.Pass(ctx) // changes nothing, so it records nothing

	creates := 0
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "entra.group.create" {
			creates++
			noActorIn(t, ev)
		}
	}
	if want := len(roles) + 1; creates != want {
		t.Errorf("%d entra.group.create records, want one for each of the %d roles", creates, want)
	}
	got := assignmentAuditRecords(t, app)
	var mine []map[string]any
	for _, ev := range got {
		if ev["roleId"] == role.ID {
			mine = append(mine, ev)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("records of the role after bob joined the group = %v, want one roles.assign", mine)
	}
	check := func(ev map[string]any, action string) {
		t.Helper()
		noActorIn(t, ev)
		if ev["action"] != action || ev["user"] != bob.ID || ev["role"] != role.Name || ev["origin"] != store.OriginEntra {
			t.Errorf("record = %v, want %s of bob on %s with origin entra", ev, action, role.Name)
		}
		if reason, _ := ev["reason"].(string); reason == "" {
			t.Errorf("record %v carries no reason", ev)
		}
	}
	check(mine[0], actionRolesAssign)

	app.subjects.put(ses.ID, policy.Subject{User: "bob", Roles: []string{role.Name}}, time.Time{})
	graph.members = []entragraph.Member{}
	syncer.Pass(ctx)
	mine = nil
	for _, ev := range assignmentAuditRecords(t, app) {
		if ev["roleId"] == role.ID {
			mine = append(mine, ev)
		}
	}
	if len(mine) != 2 {
		t.Fatalf("records after bob left the group = %v, want the assign and one roles.unassign", mine)
	}
	check(mine[1], actionRolesUnassign)
	if _, ok := app.subjects.get(ses.ID); ok {
		t.Error("the session of the user who left the group kept its cached subject")
	}
}
