package entrasync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/membership"
	"github.com/strazahq/straza/internal/store"
)

// fakeGraph is Entra as the sync sees it: groups by uniqueName with their
// members, and the failures a test plants.
type fakeGraph struct {
	groups   map[string]*fakeGroup // by uniqueName
	fail     error                 // every request fails with it
	denyWr   map[string]bool       // group ids whose writes answer 403
	changed  []string              // group ids the next delta reports
	deltaErr error
	calls    []string
	next     int
	// users are the changes the next read of the users feed reports,
	// usersAsked the links it was asked with, and usersErr fails the next
	// read that continues from a link.
	users      []entragraph.UserChange
	usersAsked []string
	usersErr   error
	// usersRefused answers every read of the users feed with 403.
	usersRefused bool
	// identities are the service principals that are direct members of a
	// group, by group id, parents the agent identity of each agent user,
	// and agents what Entra says about an agent identity. An identity that
	// agents does not hold reads as missing. agentsErr fails every read of
	// the agents, and agentsAsked lists the ids of each read.
	identities  map[string][]entragraph.Member
	parents     map[string]string
	agents      map[string]entragraph.Agent
	agentsErr   error
	agentsAsked [][]string
}

type fakeGroup struct {
	id, name, desc string
	members        []entragraph.Member
	gone           bool
}

func newFakeGraph() *fakeGraph {
	return &fakeGraph{groups: map[string]*fakeGroup{}, denyWr: map[string]bool{},
		identities: map[string][]entragraph.Member{}, parents: map[string]string{}, agents: map[string]entragraph.Agent{}}
}

func (f *fakeGraph) AgentIdentities(_ context.Context, groupID string) ([]entragraph.Member, error) {
	if err := f.call("identities:" + groupID); err != nil {
		return nil, err
	}
	return append([]entragraph.Member{}, f.identities[groupID]...), nil
}

func (f *fakeGraph) AgentUserParents(_ context.Context, groupID string) (map[string]string, error) {
	if err := f.call("parents:" + groupID); err != nil {
		return nil, err
	}
	return f.parents, nil
}

func (f *fakeGraph) Agents(_ context.Context, ids []string) (map[string]entragraph.Agent, error) {
	if err := f.call("agents"); err != nil {
		return nil, err
	}
	f.agentsAsked = append(f.agentsAsked, append([]string{}, ids...))
	if f.agentsErr != nil {
		return nil, f.agentsErr
	}
	out := map[string]entragraph.Agent{}
	for _, id := range ids {
		a, ok := f.agents[id]
		if !ok {
			a = entragraph.Agent{ID: id, Missing: true}
		}
		out[id] = a
	}
	return out, nil
}

func graphErr(status int) error { return &entragraph.Error{Status: status, Message: "planted"} }

func (f *fakeGraph) byID(id string) *fakeGroup {
	for _, g := range f.groups {
		if g.id == id && !g.gone {
			return g
		}
	}
	return nil
}

func (f *fakeGraph) call(name string) error {
	f.calls = append(f.calls, name)
	return f.fail
}

func (f *fakeGraph) EnsureGroup(_ context.Context, spec entragraph.GroupSpec) (entragraph.Group, error) {
	if err := f.call("ensure"); err != nil {
		return entragraph.Group{}, err
	}
	g, ok := f.groups[spec.UniqueName]
	if !ok {
		f.next++
		g = &fakeGroup{id: "g" + string(rune('0'+f.next)), name: spec.DisplayName, desc: spec.Description}
		f.groups[spec.UniqueName] = g
	}
	return entragraph.Group{ID: g.id, DisplayName: g.name, Description: g.desc, UniqueName: spec.UniqueName}, nil
}

func (f *fakeGraph) GroupByUniqueName(_ context.Context, name string) (entragraph.Group, error) {
	if err := f.call("get"); err != nil {
		return entragraph.Group{}, err
	}
	g, ok := f.groups[name]
	if !ok || g.gone {
		return entragraph.Group{}, graphErr(404)
	}
	return entragraph.Group{ID: g.id, DisplayName: g.name, Description: g.desc, UniqueName: name}, nil
}

func (f *fakeGraph) UpdateGroup(_ context.Context, id, displayName, description string) error {
	if err := f.call("update"); err != nil {
		return err
	}
	g := f.byID(id)
	switch {
	case g == nil:
		return graphErr(404)
	case f.denyWr[id]:
		return graphErr(403)
	}
	g.name, g.desc = displayName, description
	return nil
}

func (f *fakeGraph) DeleteGroup(_ context.Context, id string) error {
	if err := f.call("delete"); err != nil {
		return err
	}
	g := f.byID(id)
	if g == nil {
		return graphErr(404)
	}
	g.gone = true
	return nil
}

func (f *fakeGraph) TransitiveMembers(_ context.Context, id string) ([]entragraph.Member, error) {
	if err := f.call("members:" + id); err != nil {
		return nil, err
	}
	g := f.byID(id)
	if g == nil {
		return nil, graphErr(404)
	}
	return append([]entragraph.Member{}, g.members...), nil
}

func (f *fakeGraph) Delta(_ context.Context, link string, _ []string) (entragraph.DeltaResult, error) {
	if err := f.call("delta"); err != nil {
		return entragraph.DeltaResult{}, err
	}
	if link != "" && f.deltaErr != nil {
		err := f.deltaErr
		f.deltaErr = nil
		return entragraph.DeltaResult{}, err
	}
	res := entragraph.DeltaResult{Link: "https://graph.example/delta?token=next"}
	if link != "" {
		for _, id := range f.changed {
			res.Groups = append(res.Groups, entragraph.GroupChange{ID: id})
		}
		f.changed = nil
	}
	return res, nil
}

func (f *fakeGraph) UsersDelta(_ context.Context, link string) (entragraph.UsersResult, error) {
	if err := f.call("users"); err != nil {
		return entragraph.UsersResult{}, err
	}
	f.usersAsked = append(f.usersAsked, link)
	if f.usersRefused {
		return entragraph.UsersResult{}, graphErr(403)
	}
	if link != "" && f.usersErr != nil {
		err := f.usersErr
		f.usersErr = nil
		return entragraph.UsersResult{}, err
	}
	res := entragraph.UsersResult{Link: fmt.Sprintf("https://graph.example/users?token=%d", len(f.usersAsked))}
	if link != "" {
		res.Users, f.users = f.users, nil
	}
	return res, nil
}

// reads lists the group ids whose members the fake was asked for since the
// last call of reads.
func (f *fakeGraph) reads() []string {
	var out []string
	for _, c := range f.calls {
		if id, ok := strings.CutPrefix(c, "members:"); ok {
			out = append(out, id)
		}
	}
	f.calls = nil
	sort.Strings(out)
	return out
}

// rig is a Syncer over a real sqlite store and a fakeGraph, with every
// record and membership change it reports.
type rig struct {
	t       *testing.T
	st      store.Store
	graph   *fakeGraph
	sync    *Syncer
	now     time.Time
	records []map[string]any
	changes []membership.Change
	// people lists what the sync reported and asked about persons.
	people []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(t.TempDir(), "sync.db")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, st: st, graph: newFakeGraph(), now: time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)}
	r.sync = New(Deps{
		Graph: r.graph, Roles: st.Roles(), Users: st.Users(), Groups: st.EntraGroups(), Agents: st.EntraAgents(), Settings: st.Settings(),
		Membership: membership.Deps{
			Roles:           st.Roles(),
			IdentityChanged: func(context.Context, string) {},
			Changed:         func(_ context.Context, c membership.Change) { r.changes = append(r.changes, c) },
		},
		People: r.peopleDeps(),
		Record: func(_ context.Context, data map[string]any) { r.records = append(r.records, data) },
		Config: config.Entra{
			Read: []string{config.EntraReadPeople}, GroupNamePrefix: "Straza: ",
			DeltaInterval: time.Minute, ReconcileInterval: time.Hour,
			RemovalGuard: config.EntraRemovalGuard{Percent: 50, MinMembers: 5},
		},
		Deployment: "abc123",
		Protected:  func(context.Context) string { return "" },
		Now:        func() time.Time { return r.now },
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return r
}

// pass moves the clock by d and runs one pass.
func (r *rig) pass(d time.Duration) {
	r.now = r.now.Add(d)
	r.sync.Pass(context.Background())
}

func (r *rig) role(name string) store.Role {
	r.t.Helper()
	role, err := r.st.Roles().Create(context.Background(), store.Role{Name: name, Kind: store.RoleKindBusiness, Description: "reads " + name})
	if err != nil {
		r.t.Fatal(err)
	}
	return role
}

// user makes a Straza user whose external id is the Entra object id oid.
func (r *rig) user(name, oid string) store.User {
	r.t.Helper()
	u, err := r.st.Users().Create(context.Background(), store.User{Username: name, ExternalID: oid, Origin: store.OriginSCIM})
	if err != nil {
		r.t.Fatal(err)
	}
	return u
}

func (r *rig) row(role store.Role) store.EntraRoleGroup {
	r.t.Helper()
	g, err := r.st.EntraGroups().Get(context.Background(), role.ID)
	if err != nil {
		r.t.Fatalf("the group row of %s: %v", role.Name, err)
	}
	return g
}

// group answers Entra's group of the role.
func (r *rig) group(role store.Role) *fakeGroup {
	r.t.Helper()
	g, ok := r.graph.groups[UniqueName("abc123", role.ID)]
	if !ok {
		r.t.Fatalf("Entra holds no group for %s", role.Name)
	}
	return g
}

// holders lists who holds the role as "user/origin", sorted.
func (r *rig) holders(role store.Role) string {
	r.t.Helper()
	asg, err := r.st.Roles().AssignmentsByRole(context.Background(), role.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, a := range asg {
		u, err := r.st.Users().GetByID(context.Background(), a.SubjectID)
		if err != nil {
			r.t.Fatal(err)
		}
		out = append(out, u.Username+"/"+a.Origin)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// actions lists the actions of the records since the last call, in order.
func (r *rig) actions() string {
	var out []string
	for _, rec := range r.records {
		out = append(out, rec["action"].(string))
	}
	r.records = nil
	return strings.Join(out, ",")
}

// person is an enabled account as a member read lists it. Its sign-in
// name is the object id without the prefix oid-, which is the name rig.user
// gives the Straza user of that id.
func person(oid string) entragraph.Member {
	enabled := true
	return entragraph.Member{ID: oid, Kind: entragraph.KindUser, Enabled: &enabled, UPN: strings.TrimPrefix(oid, "oid-")}
}

// peopleDeps are the server's part in a person's write, done on the store
// alone, with each call noted in r.people.
func (r *rig) peopleDeps() People {
	status := func(id, to, note string) (store.User, error) {
		u, err := r.st.Users().UpdateFields(context.Background(), id, store.UserFields{Status: &to}, nil)
		if err == nil {
			r.people = append(r.people, note+":"+u.Username)
		}
		return u, err
	}
	return People{
		Created: func(_ context.Context, u store.User, reason string) {
			r.people = append(r.people, "create:"+u.Username+":"+reason)
		},
		Updated: func(_ context.Context, u store.User, changed []string, reason string) {
			r.people = append(r.people, "update:"+u.Username+":"+strings.Join(changed, "+")+":"+reason)
		},
		Disable: func(_ context.Context, id, reason string) (store.User, error) {
			return status(id, store.UserDisabled, "disable:"+reason)
		},
		Enable: func(_ context.Context, id string) (store.User, error) { return status(id, store.UserActive, "enable") },
	}
}

// persons lists what the sync reported and asked about persons since the
// last call, in order.
func (r *rig) persons() string {
	out := strings.Join(r.people, ",")
	r.people = nil
	return out
}

func (r *rig) want(what, got, want string) {
	r.t.Helper()
	if got != want {
		r.t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func TestGroupLifecycle(t *testing.T) {
	r := newRig(t)
	role := r.role("finance-readers")
	r.pass(0)
	g := r.group(role)
	r.want("the group's name", g.name, "Straza: finance-readers")
	r.want("the group's description", g.desc, "Members of this group hold the Straza role finance-readers. reads finance-readers. Straza created this group and manages it. Do not rename or delete it.")
	r.want("the row's state", r.row(role).State, store.EntraGroupLinked)
	r.want("the row's group", r.row(role).GroupID, g.id)
	r.want("records of the first pass", r.actions(), "entra.group.create")

	r.pass(time.Minute)
	r.want("records of a pass that changed nothing", r.actions(), "")
	if len(r.graph.groups) != 1 {
		t.Errorf("Entra holds %d groups after a second pass, want 1", len(r.graph.groups))
	}

	g.name = "renamed in Entra"
	r.pass(time.Hour)
	r.want("the name after a full pass", g.name, "Straza: finance-readers")
	r.want("records of the repair", r.actions(), "entra.group.update")

	old := "reads finance-readers\nManaged by Straza. Role kind: business."
	g.desc = old
	r.pass(time.Hour)
	if g.desc == old {
		t.Error("a full pass left the description of an earlier release on the group")
	}
	if len(r.records) == 1 {
		r.want("the name on the update record", fmt.Sprint(r.records[0]["name"]), g.name)
		r.want("the description on the update record", fmt.Sprint(r.records[0]["description"]), g.desc)
	}
	r.want("records of the rewrite", r.actions(), "entra.group.update")

	g.name = "renamed again"
	r.graph.denyWr[g.id] = true
	r.pass(time.Hour)
	r.want("the row's sentence when Straza is no owner", r.row(role).Error, notOwner)
	r.want("the row's state when Straza is no owner", r.row(role).State, store.EntraGroupLinked)
	r.want("records when the repair is refused", r.actions(), "")
	delete(r.graph.denyWr, g.id)

	if err := r.st.Roles().Delete(context.Background(), role.ID); err != nil {
		t.Fatal(err)
	}
	r.pass(time.Minute)
	if !g.gone {
		t.Error("the group of a deleted role is still in Entra")
	}
	if rows, _ := r.st.EntraGroups().List(context.Background()); len(rows) != 0 {
		t.Errorf("rows after the role's group was deleted = %+v, want none", rows)
	}
	r.want("records of the delete", r.actions(), "entra.group.delete")
}

func TestGroupNotOwnedIsNotLinked(t *testing.T) {
	r := newRig(t)
	role := r.role("squatted")
	r.graph.groups[UniqueName("abc123", role.ID)] = &fakeGroup{id: "theirs", members: []entragraph.Member{person("oid-eve")}}
	r.graph.denyWr["theirs"] = true
	r.user("eve", "oid-eve")
	r.pass(0)
	r.pass(time.Minute)
	row := r.row(role)
	r.want("the state of a group Straza cannot write", row.State, store.EntraGroupCreating)
	r.want("its sentence", row.Error, notOwnerOnCreate)
	r.want("its group id", row.GroupID, "")
	r.want("holders from a group Straza does not own", r.holders(role), "")
	r.want("records", r.actions(), "")
	if got := r.graph.reads(); len(got) != 0 {
		t.Errorf("members were read from a group Straza does not own: %v", got)
	}
}

func TestMembersApplied(t *testing.T) {
	r := newRig(t)
	role := r.role("readers")
	ada, ben := r.user("ada", "oid-ada"), r.user("ben", "oid-ben")
	glass := r.user("break-glass", "oid-glass")
	r.sync.d.Protected = func(context.Context) string { return glass.ID }
	if _, err := r.st.Roles().Assign(context.Background(), store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: ben.ID, RoleID: role.ID, Origin: store.OriginSCIM}); err != nil {
		t.Fatal(err)
	}
	r.pass(0)
	g := r.group(role)
	off := false
	g.members = []entragraph.Member{
		person("oid-ada"), person("oid-ben"), person("oid-glass"), person("oid-stranger"),
		{ID: "oid-off", Kind: entragraph.KindUser, Enabled: &off, UPN: "off"},
		{ID: "oid-agent-user", Kind: entragraph.KindAgentUser, ParentID: "oid-agent"},
		{ID: "oid-agent", Kind: entragraph.KindServicePrincipal},
		{ID: "oid-typed-only", Kind: entragraph.KindOther},
		{ID: "nested-1", Kind: entragraph.KindGroup},
	}
	r.pass(time.Hour)
	r.want("holders", r.holders(role), "ada/entra,ben/entra,ben/scim,off/entra,stranger/entra")
	var skipped []NotApplied
	if err := json.Unmarshal([]byte(r.row(role).NotApplied), &skipped); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range skipped {
		got = append(got, s.ID+"="+s.Reason)
	}
	r.want("members not applied", strings.Join(got, ","),
		"oid-agent=unread,oid-agent-user=not-read,oid-typed-only=unread")
	if len(r.changes) != 4 {
		t.Fatalf("membership changes = %+v, want one assign each for ada, ben, off and stranger", r.changes)
	}
	for _, c := range r.changes {
		if c.Origin != store.OriginEntra || c.Action != membership.Assign {
			t.Errorf("change = %+v, want an entra assign", c)
		}
	}
	if r.row(role).ReadAt == nil {
		t.Error("the row has no time of a good read after one")
	}

	r.changes = nil
	g.members = []entragraph.Member{person("oid-ben")}
	r.pass(time.Hour)
	r.want("holders after ada left the group", r.holders(role), "ben/entra,ben/scim")
	left := 0
	for _, c := range r.changes {
		if c.Action == membership.Unassign && c.UserID == ada.ID {
			left++
		}
	}
	if len(r.changes) != 3 || left != 1 {
		t.Errorf("changes after ada, off and stranger left = %+v, want three unassigns, one of them ada's", r.changes)
	}

	r.sync.d.Config.Read = nil
	r.pass(time.Hour)
	r.want("holders when people are not read from Entra", r.holders(role), "ben/scim")
	r.want("the reason", r.row(role).NotApplied, `[{"id":"oid-ben","kind":"user","reason":"not-read"}]`)
}

func TestFailedReadKeepsHolders(t *testing.T) {
	r := newRig(t)
	role := r.role("kept")
	r.user("ada", "oid-ada")
	r.pass(0)
	r.group(role).members = []entragraph.Member{person("oid-ada")}
	r.pass(time.Hour)
	r.want("holders", r.holders(role), "ada/entra")
	good := *r.row(role).ReadAt
	r.actions()

	r.group(role).members = nil
	r.graph.fail = graphErr(500)
	for i := 0; i < 3; i++ {
		r.pass(time.Hour)
	}
	r.want("holders after three failed passes", r.holders(role), "ada/entra")
	r.want("records of three failed passes", r.actions(), "entra.read.stale")
	if got := *r.row(role).ReadAt; !got.Equal(good) {
		t.Errorf("the time of the last good read moved to %v during failed passes, want %v", got, good)
	}
	st, err := ReadStatus(context.Background(), r.st.Settings())
	if err != nil || st.StaleSince == nil || st.Error == "" || st.LastGood == nil || !st.LastGood.Before(*st.StaleSince) {
		t.Errorf("status while stale = %+v, %v", st, err)
	}

	r.graph.fail = nil
	r.pass(time.Minute)
	r.want("holders after the read came back and listed nobody", r.holders(role), "")
	r.want("records of the recovery", r.actions(), "entra.read.recovered")
	if st, _ := ReadStatus(context.Background(), r.st.Settings()); st.StaleSince != nil || st.Error != "" {
		t.Errorf("status after the recovery = %+v, want not stale", st)
	}
}

func TestThrottledPassWaits(t *testing.T) {
	r := newRig(t)
	r.role("throttled")
	r.pass(0)
	r.graph.calls = nil
	r.graph.fail = &entragraph.Error{Status: 429, RetryAfter: 10 * time.Minute}
	r.pass(time.Hour)
	r.graph.fail = nil
	r.graph.calls = nil
	r.pass(5 * time.Minute)
	if len(r.graph.calls) != 0 {
		t.Errorf("requests %v were sent inside the time Graph named", r.graph.calls)
	}
	r.pass(6 * time.Minute)
	if len(r.graph.calls) == 0 {
		t.Error("no request was sent after the time Graph named")
	}
}

func TestRemovalGuard(t *testing.T) {
	r := newRig(t)
	role := r.role("guarded")
	var all []entragraph.Member
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		r.user(n, "oid-"+n)
		all = append(all, person("oid-"+n))
	}
	r.user("new", "oid-new")
	r.pass(0)
	r.group(role).members = all
	r.pass(time.Hour)
	r.want("holders", r.holders(role), "a/entra,b/entra,c/entra,d/entra,e/entra,f/entra")
	r.actions()

	r.group(role).members = []entragraph.Member{person("oid-a"), person("oid-new")}
	r.pass(time.Hour)
	r.pass(time.Hour)
	r.want("holders while the pass is held", r.holders(role), "a/entra,b/entra,c/entra,d/entra,e/entra,f/entra,new/entra")
	r.want("the state", r.row(role).State, store.EntraGroupHeld)
	if !strings.Contains(r.row(role).Error, "5 of this role's 7 holders") {
		t.Errorf("the held sentence = %q, want it to count 5 of the 7 holders after new was added", r.row(role).Error)
	}
	r.want("records of two held passes", r.actions(), "entra.pass.held")

	if ok, err := r.st.EntraGroups().SetState(context.Background(), role.ID, store.EntraGroupHeld, store.EntraGroupReleased); err != nil || !ok {
		t.Fatalf("release = %v, %v", ok, err)
	}
	r.pass(time.Minute) // a pass that reads only what changed still applies a release
	r.want("holders after the release", r.holders(role), "a/entra,new/entra")
	r.want("the state after the release", r.row(role).State, store.EntraGroupLinked)
	r.want("the sentence after the release", r.row(role).Error, "")

	r.group(role).members = []entragraph.Member{person("oid-a")}
	r.pass(time.Hour)
	r.want("holders after a removal under the guard's floor", r.holders(role), "a/entra")
}

func TestGroupDeletedInEntra(t *testing.T) {
	r := newRig(t)
	role := r.role("lost")
	r.user("ada", "oid-ada")
	r.pass(0)
	r.group(role).members = []entragraph.Member{person("oid-ada")}
	r.pass(time.Hour)
	r.actions()
	r.group(role).gone = true
	r.pass(time.Hour)
	r.pass(time.Hour)
	r.want("the state", r.row(role).State, store.EntraGroupMissing)
	r.want("the sentence", r.row(role).Error, groupGone)
	r.want("holders of a role whose group is gone", r.holders(role), "ada/entra")
	r.want("records over two passes", r.actions(), "entra.group.missing")
	if st, _ := ReadStatus(context.Background(), r.st.Settings()); st.StaleSince != nil {
		t.Errorf("one deleted group made the whole read stale: %+v", st)
	}
}

func TestDeltaReadsOnlyChangedGroups(t *testing.T) {
	r := newRig(t)
	one, two := r.role("one"), r.role("two")
	r.user("ada", "oid-ada")
	r.pass(0)
	g1, g2 := r.group(one), r.group(two)
	g1.members = []entragraph.Member{{ID: "nested-1", Kind: entragraph.KindGroup}}
	r.pass(time.Hour)   // finds the nested group
	r.pass(time.Minute) // the watched set grew, so one more full pass
	r.graph.reads()

	r.pass(time.Minute)
	if got := r.graph.reads(); len(got) != 0 {
		t.Errorf("a delta pass with no change read %v, want nothing", got)
	}
	g2.members = []entragraph.Member{person("oid-ada")}
	r.graph.changed = []string{g2.id}
	r.pass(time.Minute)
	r.want("groups read after a change in one", strings.Join(r.graph.reads(), ","), g2.id)
	r.want("holders of the changed role", r.holders(two), "ada/entra")

	r.graph.changed = []string{"nested-1"}
	r.pass(time.Minute)
	r.want("groups read after a change in a nested group", strings.Join(r.graph.reads(), ","), g1.id)

	r.graph.deltaErr = graphErr(410)
	r.pass(time.Minute)
	r.want("groups read after the delta link ended", strings.Join(r.graph.reads(), ","), g1.id+","+g2.id)
	if st, _ := ReadStatus(context.Background(), r.st.Settings()); st.StaleSince != nil {
		t.Errorf("an ended delta link made the read stale: %+v", st)
	}
}
