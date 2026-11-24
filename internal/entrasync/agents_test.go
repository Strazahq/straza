package entrasync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/store"
)

// The Entra agent identities of the agent tests.
const (
	idLedger = "aaaaaaaa-0000-4000-8000-000000000001"
	idReport = "aaaaaaaa-0000-4000-8000-000000000002"
)

// agentRig is a rig whose deployment takes every kind from Entra, with one
// role whose group exists in Entra.
func agentRig(t *testing.T, kinds ...string) (*rig, store.Role, *fakeGroup) {
	t.Helper()
	r := newRig(t)
	if len(kinds) == 0 {
		kinds = []string{config.EntraReadPeople, config.EntraReadAgentUsers, config.EntraReadAgentIdentities}
	}
	r.sync.d.Config.Read = kinds
	role := r.role("lab-echo-callers")
	r.pass(0)
	r.actions()
	return r, role, r.group(role)
}

// identity is an agent identity as the read of a group's service
// principals lists it.
func identity(id string) entragraph.Member {
	return entragraph.Member{ID: id, Kind: entragraph.KindAgentIdentity}
}

// entraAgent is what Entra says about an enabled agent identity with the
// sponsors of the object ids sponsors, each a user.
func entraAgent(id, name string, sponsors ...string) entragraph.Agent {
	enabled := true
	a := entragraph.Agent{ID: id, DisplayName: name, Enabled: &enabled}
	for _, sp := range sponsors {
		a.Sponsors = append(a.Sponsors, entragraph.Sponsor{ID: sp, Kind: entragraph.KindUser})
	}
	return a
}

func (r *rig) agentByID(id string) store.User {
	r.t.Helper()
	u, err := r.st.Users().GetByEntraAgentID(context.Background(), id)
	if err != nil {
		r.t.Fatalf("the agent of %s: %v", id, err)
	}
	return u
}

func (r *rig) agentRow(u store.User) store.EntraAgent {
	r.t.Helper()
	row, err := r.st.EntraAgents().Get(context.Background(), u.ID)
	if err != nil {
		r.t.Fatalf("the Entra row of %s: %v", u.Username, err)
	}
	return row
}

// did lists what the sync reported and asked about users since the last
// call, in order.
func (r *rig) did() string {
	out := strings.Join(r.people, ",")
	r.people = nil
	return out
}

func (r *rig) notApplied(role store.Role) string {
	r.t.Helper()
	var skipped []NotApplied
	if err := json.Unmarshal([]byte(r.row(role).NotApplied), &skipped); err != nil {
		r.t.Fatal(err)
	}
	var got []string
	for _, s := range skipped {
		got = append(got, s.ID+"="+s.Kind+"/"+s.Reason)
	}
	return strings.Join(got, ",")
}

// TestAgentsApplied pins that an agent identity and an agent user in a
// role's group are one Straza agent with the role once, that an agent
// identity in a nested group is found, that the sponsor comes from Entra,
// and that an application and an agent user without a parent get nothing.
func TestAgentsApplied(t *testing.T) {
	r, role, g := agentRig(t)
	r.user("ben", "oid-ben")
	on := true
	g.members = []entragraph.Member{
		{ID: "user-of-ledger", Kind: entragraph.KindAgentUser, Enabled: &on, UPN: "ledger@example.com"},
		{ID: "user-without-parent", Kind: entragraph.KindAgentUser, Enabled: &on},
		{ID: "nested-1", Kind: entragraph.KindGroup},
	}
	r.graph.parents["user-of-ledger"] = strings.ToUpper(idLedger)
	r.graph.identities[g.id] = []entragraph.Member{identity(idLedger), {ID: "app-1", Kind: entragraph.KindServicePrincipal}}
	r.graph.identities["nested-1"] = []entragraph.Member{identity(idReport), identity(idLedger)}
	r.graph.agents[idLedger] = entraAgent(idLedger, "ledger-agent", "oid-ben")
	r.graph.agents[idReport] = entraAgent(idReport, "report-agent")
	r.pass(time.Hour)

	r.want("holders", r.holders(role), "ledger-agent/entra,report-agent/entra")
	r.want("not applied", r.notApplied(role), "app-1=servicePrincipal/not-agent,user-without-parent=agentUser/unread")
	ledger := r.agentByID(idLedger)
	if ledger.UserType != store.UserTypeAgent || !ledger.IsNHI() || ledger.Origin != store.OriginEntra ||
		ledger.Sponsor != "ben" || ledger.Status != store.UserActive || ledger.ExternalID != "" {
		t.Errorf("the ledger agent = %+v, want an active agent of origin entra sponsored by ben", ledger)
	}
	if row := r.agentRow(ledger); row.Identity != store.EntraObjectEnabled || row.AgentUser != store.EntraObjectEnabled ||
		row.AgentUserID != "user-of-ledger" || row.SponsorState != "" {
		t.Errorf("the ledger agent's row = %+v, want both objects enabled and a sponsor", row)
	}
	report := r.agentByID(idReport)
	if row := r.agentRow(report); report.Sponsor != "" || row.SponsorState != store.EntraSponsorNone || row.AgentUser != "" {
		t.Errorf("the report agent = %+v with row %+v, want no sponsor, the state none and no agent user", report, row)
	}
	r.want("what the sync reported", r.did(), "create:ledger-agent:"+reasonAgentCreated+",create:report-agent:"+reasonAgentCreated)
	r.want("records", r.actions(), "entra.sponsor.unresolved")
	if n := len(r.graph.agentsAsked); n != 1 || len(r.graph.agentsAsked[0]) != 2 {
		t.Errorf("reads of the agents = %v, want one read of both", r.graph.agentsAsked)
	}

	// A pass in which nothing changed reads the agents again and writes
	// nothing.
	r.graph.agentsAsked = nil
	r.pass(time.Minute)
	r.want("what a quiet pass reported", r.did(), "")
	r.want("records of a quiet pass", r.actions(), "")
	if n := len(r.graph.agentsAsked); n != 1 || len(r.graph.agentsAsked[0]) != 2 {
		t.Errorf("reads of the agents in a quiet pass = %v, want one read of both", r.graph.agentsAsked)
	}
}

// TestAgentSponsor pins when the sync takes a sponsor from Entra: only
// when Entra lists exactly one, a person, whom Straza has by the Entra
// object id. Every other case leaves the agent without a sponsor, keeps
// what Entra lists and writes one record on the way in and one on the way
// out.
func TestAgentSponsor(t *testing.T) {
	r, _, g := agentRig(t)
	r.user("ben", "oid-ben")
	r.user("ada", "oid-ada")
	// carol has the sign-in name of an Entra account and not its object id,
	// so a name never makes her a sponsor.
	r.user("carol@example.com", "")
	bot, err := r.st.Users().Create(context.Background(), store.User{Username: "other-bot", ExternalID: "oid-bot", UserType: store.UserTypeAgent})
	if err != nil {
		t.Fatal(err)
	}
	_ = bot
	r.graph.identities[g.id] = []entragraph.Member{identity(idLedger)}
	sp := func(id string, kind entragraph.Kind) entragraph.Sponsor {
		return entragraph.Sponsor{ID: id, Kind: kind}
	}
	for _, tc := range []struct {
		name     string
		sponsors []entragraph.Sponsor
		sponsor  string
		state    string
		records  string
	}{
		{"one person", []entragraph.Sponsor{sp("oid-ben", entragraph.KindUser)}, "ben", "", ""},
		{"none", nil, "", store.EntraSponsorNone, "entra.sponsor.unresolved"},
		{"two people", []entragraph.Sponsor{sp("oid-ben", entragraph.KindUser), sp("oid-ada", entragraph.KindUser)}, "", store.EntraSponsorSeveral, ""},
		{"a group", []entragraph.Sponsor{sp("group-1", entragraph.KindGroup)}, "", store.EntraSponsorNotPerson, ""},
		{"an agent user", []entragraph.Sponsor{sp("oid-ben", entragraph.KindAgentUser)}, "", store.EntraSponsorNotPerson, ""},
		{"an agent Straza has", []entragraph.Sponsor{sp("oid-bot", entragraph.KindUser)}, "", store.EntraSponsorNotPerson, ""},
		{"a person Straza does not have", []entragraph.Sponsor{sp("carol@example.com", entragraph.KindUser)}, "", store.EntraSponsorNoUser, ""},
		{"another person", []entragraph.Sponsor{sp("oid-ada", entragraph.KindUser)}, "ada", "", "entra.sponsor.resolved"},
		{"the same person again", []entragraph.Sponsor{sp("oid-ada", entragraph.KindUser)}, "ada", "", ""},
	} {
		a := entraAgent(idLedger, "ledger-agent")
		a.Sponsors = tc.sponsors
		r.graph.agents[idLedger] = a
		r.pass(time.Hour)
		u := r.agentByID(idLedger)
		row := r.agentRow(u)
		if u.Sponsor != tc.sponsor || row.SponsorState != tc.state {
			t.Errorf("%s: sponsor %q with state %q, want %q with %q", tc.name, u.Sponsor, row.SponsorState, tc.sponsor, tc.state)
		}
		if tc.state != "" && len(row.Sponsors) != len(tc.sponsors) {
			t.Errorf("%s: the row keeps %v, want what Entra lists, %v", tc.name, row.Sponsors, tc.sponsors)
		}
		if tc.state == "" && len(row.Sponsors) != 0 {
			t.Errorf("%s: the row keeps %v, want nothing while the sponsor is set", tc.name, row.Sponsors)
		}
		if got := r.actions(); got != tc.records {
			t.Errorf("%s: records %q, want %q", tc.name, got, tc.records)
		}
	}
}

// TestAgentState pins when Entra's state disables and enables an agent:
// either of its two objects disables it, only both together enable it, an
// agent identity that is gone disables it after an earlier good read, and
// a deleted agent user no longer counts.
func TestAgentState(t *testing.T) {
	r, role, g := agentRig(t)
	r.user("ben", "oid-ben")
	r.graph.identities[g.id] = []entragraph.Member{identity(idLedger)}
	r.graph.agents[idLedger] = entraAgent(idLedger, "ledger-agent", "oid-ben")
	r.pass(time.Hour)
	r.did()
	set := func(enabled bool) {
		a := entraAgent(idLedger, "ledger-agent", "oid-ben")
		a.Enabled = &enabled
		r.graph.agents[idLedger] = a
	}
	feed := func(enabled *bool, removed bool) {
		r.graph.users = []entragraph.UserChange{{ID: "user-of-ledger", ParentID: idLedger, Enabled: enabled, Removed: removed}}
	}
	yes, no := true, false
	for _, tc := range []struct {
		name   string
		change func()
		status string
		did    string
	}{
		{"the identity is disabled", func() { set(false) }, store.UserDisabled, "disable:" + reasonIdentityDisabled + ":ledger-agent"},
		{"the identity stays disabled", func() {}, store.UserDisabled, ""},
		{"the identity is enabled", func() { set(true) }, store.UserActive, "enable:ledger-agent"},
		{"the agent user is disabled", func() { feed(&no, false) }, store.UserDisabled, "disable:" + reasonAgentUserOff + ":ledger-agent"},
		{"the identity is disabled as well", func() { set(false) }, store.UserDisabled, ""},
		{"the agent user alone is enabled", func() { feed(&yes, false) }, store.UserDisabled, ""},
		{"the identity is enabled too", func() { set(true) }, store.UserActive, "enable:ledger-agent"},
		{"a feed entry without a state", func() { feed(nil, false) }, store.UserActive, ""},
		{"the agent user is disabled again", func() { feed(&no, false) }, store.UserDisabled, "disable:" + reasonAgentUserOff + ":ledger-agent"},
		{"the agent user is deleted", func() { r.graph.users = []entragraph.UserChange{{ID: "user-of-ledger", Removed: true}} }, store.UserActive, "enable:ledger-agent"},
		{"an unknown account is deleted", func() { r.graph.users = []entragraph.UserChange{{ID: "user-of-nobody", Removed: true}} }, store.UserActive, ""},
		{"the identity is gone", func() { delete(r.graph.agents, idLedger) }, store.UserDisabled, "disable:" + reasonIdentityDeleted + ":ledger-agent"},
		{"the identity is back", func() { set(true) }, store.UserActive, "enable:ledger-agent"},
	} {
		tc.change()
		r.pass(time.Minute)
		if u := r.agentByID(idLedger); u.Status != tc.status {
			t.Errorf("%s: status %s, want %s", tc.name, u.Status, tc.status)
		}
		if got := r.did(); got != tc.did {
			t.Errorf("%s: the sync did %q, want %q", tc.name, got, tc.did)
		}
	}
	r.want("holders after every change", r.holders(role), "ledger-agent/entra")

	// A disabled agent user no longer counts once the deployment stops
	// taking agent users from Entra.
	feed(&no, false)
	r.pass(time.Minute)
	r.want("the status with the agent user disabled", r.agentByID(idLedger).Status, store.UserDisabled)
	r.sync.d.Config.Read = []string{config.EntraReadAgentIdentities}
	r.pass(time.Minute)
	r.want("the status once agent users are not read", r.agentByID(idLedger).Status, store.UserActive)
	if row := r.agentRow(r.agentByID(idLedger)); row.AgentUser != "" || row.AgentUserID != "" {
		t.Errorf("the row once agent users are not read = %+v, want no agent user", row)
	}
}

// TestAgentReadFails pins that a failed or incomplete read of the agents
// changes nobody: the pass turns stale and the agent keeps its status, its
// role and its sponsor.
func TestAgentReadFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(r *rig)
		want  string
	}{
		{"Graph refuses the read", func(r *rig) { r.graph.agentsErr = graphErr(403) }, "planted"},
		{"Graph fails", func(r *rig) { r.graph.agentsErr = graphErr(500) }, "planted"},
		{"no enabled state", func(r *rig) {
			a := r.graph.agents[idLedger]
			a.Enabled = nil
			r.graph.agents[idLedger] = a
		}, "AgentIdentity.Read.All"},
	} {
		r, role, g := agentRig(t)
		r.user("ben", "oid-ben")
		r.graph.identities[g.id] = []entragraph.Member{identity(idLedger)}
		r.graph.agents[idLedger] = entraAgent(idLedger, "ledger-agent", "oid-ben")
		r.pass(time.Hour)
		r.did()
		r.actions()
		tc.plant(r)
		r.pass(time.Hour)
		u := r.agentByID(idLedger)
		if u.Status != store.UserActive || u.Sponsor != "ben" || r.holders(role) != "ledger-agent/entra" || r.did() != "" {
			t.Errorf("%s: the agent = %+v with holders %q, want it unchanged", tc.name, u, r.holders(role))
		}
		if got := r.actions(); got != "entra.read.stale" {
			t.Errorf("%s: records %q, want the read marked stale once", tc.name, got)
		}
		st, err := ReadStatus(context.Background(), r.st.Settings())
		if err != nil || !strings.Contains(st.Error, tc.want) {
			t.Errorf("%s: the state of the read says %q, %v; want it to name %q", tc.name, st.Error, err, tc.want)
		}
	}
}

// TestAgentTakeOverAndRename pins that an agent another source wrote is
// found by its Entra agent id, taken over and renamed to the agent
// identity's name, that a later rename in Entra is followed, that a name
// another user has is taken with the start of the agent identity's id, and
// that the agent takes the plain name once it is free.
func TestAgentTakeOverAndRename(t *testing.T) {
	r, role, g := agentRig(t)
	ctx := context.Background()
	old, err := r.st.Users().Create(ctx, store.User{
		Username: "ledger@example.com", ExternalID: "user-of-ledger", UserType: store.UserTypeAgent,
		Origin: store.OriginSCIM, EntraAgentID: idLedger, Sponsor: "someone",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.graph.identities[g.id] = []entragraph.Member{identity(idLedger)}
	r.graph.agents[idLedger] = entraAgent(idLedger, "ledger-agent")
	r.pass(time.Hour)
	u := r.agentByID(idLedger)
	if u.ID != old.ID || u.Origin != store.OriginEntra || u.Username != "ledger-agent" || u.Display != "ledger-agent" ||
		u.Sponsor != "" || u.ExternalID != "user-of-ledger" {
		t.Errorf("the agent after the takeover = %+v, want the same user and external id, origin entra, the new names and no sponsor", u)
	}
	r.want("what the takeover reported", r.did(),
		"update:ledger@example.com:origin:"+reasonAgentTakenOver+",update:ledger-agent:username:"+reasonAgentChanged+
			",update:ledger-agent:display:"+reasonAgentChanged+",update:ledger-agent:sponsor:"+reasonSponsorChanged)
	r.want("holders", r.holders(role), "ledger-agent/entra")

	r.graph.agents[idLedger] = entraAgent(idLedger, "ledger-agent-2")
	r.pass(time.Minute)
	r.want("the name after a rename in Entra", r.agentByID(idLedger).Username, "ledger-agent-2")
	r.did()

	r.user("taken", "oid-taken")
	r.graph.agents[idLedger] = entraAgent(idLedger, "taken")
	r.pass(time.Minute)
	r.want("the name when another user has the new one", r.agentByID(idLedger).Username, "taken (aaaaaaaa)")
	r.want("what the clash reported", r.did(), "update:taken (aaaaaaaa):username:"+reasonAgentChanged+",update:taken (aaaaaaaa):display:"+reasonAgentChanged)
	r.pass(time.Minute)
	r.want("what a pass during the clash reported", r.did(), "")

	// The other user gives the name up, and the agent takes the plain name.
	holder, err := r.st.Users().GetByUsername(ctx, "taken")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.st.Users().Rename(ctx, holder.ID, "given-up"); err != nil {
		t.Fatal(err)
	}
	r.pass(time.Minute)
	r.want("the name once the plain one is free", r.agentByID(idLedger).Username, "taken")
	r.did()

	// Another row holds the agent identity's id as its external id. The
	// takeover of a second agent writes no external id, so it meets no key.
	r.user("holder", idReport)
	if _, err := r.st.Users().Create(ctx, store.User{Username: "report@example.com", UserType: store.UserTypeAgent, Origin: store.OriginSCIM, EntraAgentID: idReport}); err != nil {
		t.Fatal(err)
	}
	r.graph.identities[g.id] = append(r.graph.identities[g.id], identity(idReport))
	r.graph.agents[idReport] = entraAgent(idReport, "report-agent")
	r.actions()
	r.pass(time.Hour)
	r.want("holders after the second takeover", r.holders(role), "report-agent/entra,taken/entra")
	if got := r.actions(); strings.Contains(got, "entra.read.stale") {
		t.Errorf("records %q, want no stale read", got)
	}
}

// TestAgentNotCreated pins the name of an agent whose display name another
// user has, and the members that get no agent: a name taken in both forms,
// an agent identity Entra does not answer for, and the kinds the deployment
// does not take from Entra.
func TestAgentNotCreated(t *testing.T) {
	on := true
	agentUser := entragraph.Member{ID: "user-of-ledger", Kind: entragraph.KindAgentUser, Enabled: &on}
	for _, tc := range []struct {
		name       string
		kinds      []string
		plant      func(r *rig)
		holders    string
		notApplied string
	}{
		{"the name is taken", nil, func(r *rig) { r.user("ledger-agent", "oid-other") }, "ledger-agent (aaaaaaaa)/entra", ""},
		{"the name and the name with the id are taken", nil, func(r *rig) {
			r.user("ledger-agent", "oid-other")
			r.user("ledger-agent (aaaaaaaa)", "oid-third")
		}, "", idLedger + "=agentIdentity/name-taken,user-of-ledger=agentUser/name-taken"},
		{"Entra does not know the identity", nil, func(r *rig) { delete(r.graph.agents, idLedger) }, "",
			idLedger + "=agentIdentity/unread,user-of-ledger=agentUser/unread"},
		{"only people are read", []string{config.EntraReadPeople}, func(*rig) {}, "", "user-of-ledger=agentUser/not-read"},
		{"only agent identities are read", []string{config.EntraReadAgentIdentities}, func(*rig) {}, "ledger-agent/entra",
			"user-of-ledger=agentUser/not-read"},
		{"only agent users are read", []string{config.EntraReadAgentUsers}, func(*rig) {}, "ledger-agent/entra", ""},
	} {
		r, role, g := agentRig(t, tc.kinds...)
		g.members = []entragraph.Member{agentUser}
		r.graph.parents["user-of-ledger"] = idLedger
		r.graph.identities[g.id] = []entragraph.Member{identity(idLedger)}
		r.graph.agents[idLedger] = entraAgent(idLedger, "ledger-agent")
		tc.plant(r)
		r.pass(time.Hour)
		if got := r.holders(role); got != tc.holders {
			t.Errorf("%s: holders %q, want %q", tc.name, got, tc.holders)
		}
		if got := r.notApplied(role); got != tc.notApplied {
			t.Errorf("%s: not applied %q, want %q", tc.name, got, tc.notApplied)
		}
		if len(tc.kinds) == 1 && tc.kinds[0] == config.EntraReadPeople && len(r.graph.agentsAsked) != 0 {
			t.Errorf("%s: the agents were read %v, want no read", tc.name, r.graph.agentsAsked)
		}
	}
}

// TestAgentCreatedUnderTheClashName pins the reason on the create record of
// an agent named with the start of its Entra agent ID: it says that another
// user has the display name, whether a live user holds the name or a
// deleted one still holds it under the unique key.
func TestAgentCreatedUnderTheClashName(t *testing.T) {
	const want = "create:ledger-agent (aaaaaaaa):an agent identity in a role's group in Entra. " +
		"Another Straza user has the name ledger-agent, so this agent carries the start of its Entra agent ID."
	for _, tc := range []struct {
		name  string
		plant func(r *rig)
	}{
		{"a live user has the name", func(r *rig) { r.user("ledger-agent", "oid-other") }},
		{"a deleted user holds the name", func(r *rig) {
			if _, err := r.st.Users().SoftDelete(context.Background(), r.user("ledger-agent", "oid-other").ID, nil); err != nil {
				r.t.Fatal(err)
			}
		}},
	} {
		r, role, g := agentRig(t)
		r.graph.identities[g.id] = []entragraph.Member{identity(idLedger)}
		r.graph.agents[idLedger] = entraAgent(idLedger, "ledger-agent")
		tc.plant(r)
		r.pass(time.Hour)
		if got := r.holders(role); got != "ledger-agent (aaaaaaaa)/entra" {
			t.Errorf("%s: holders %q, want the agent under the clash name", tc.name, got)
		}
		if got := r.did(); got != want {
			t.Errorf("%s: the sync reported %q, want %q", tc.name, got, want)
		}
	}
}

// TestAgentUserFeedLeavesOthersAlone pins that a change of an agent user
// reaches only an agent the sync owns: an agent of another origin and an
// identity Straza has no agent for are not touched.
func TestAgentUserFeedLeavesOthersAlone(t *testing.T) {
	r, _, _ := agentRig(t)
	ctx := context.Background()
	local, err := r.st.Users().Create(ctx, store.User{Username: "local-agent", UserType: store.UserTypeAgent, EntraAgentID: idLedger})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	r.graph.users = []entragraph.UserChange{
		{ID: "user-of-ledger", ParentID: idLedger, Enabled: &off},
		{ID: "user-of-nobody", ParentID: idReport, Enabled: &off},
		{ID: "user-with-a-bad-parent", ParentID: "not-a-guid", Enabled: &off},
	}
	r.pass(time.Minute)
	if u, err := r.st.Users().GetByID(ctx, local.ID); err != nil || u.Status != store.UserActive {
		t.Errorf("the local agent = %+v, %v; want it active", u, err)
	}
	if _, err := r.st.EntraAgents().Get(ctx, local.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the local agent's Entra row reads %v, want none", err)
	}
	r.want("what the sync did", r.did(), "")
	r.want("records", r.actions(), "")
}

// TestFeedLeavesPeopleToTheirSource pins that the users feed, which also
// runs for agent users alone, writes no person where Entra does not own
// people: a person of another source keeps origin, name and status.
func TestFeedLeavesPeopleToTheirSource(t *testing.T) {
	r, _, _ := agentRig(t, config.EntraReadAgentUsers)
	ctx := context.Background()
	ada := r.user("ada", "oid-ada")
	off := store.UserDisabled
	if _, err := r.st.Users().UpdateFields(ctx, ada.ID, store.UserFields{Status: &off}, nil); err != nil {
		t.Fatal(err)
	}
	on := true
	r.graph.users = []entragraph.UserChange{
		{ID: "oid-ada", Enabled: &on, UPN: "ada@example.com", DisplayName: "Ada Lind"},
		{ID: "oid-ada", Removed: true},
	}
	r.pass(time.Minute)
	got, err := r.st.Users().GetByID(ctx, ada.ID)
	if err != nil || got.Origin != store.OriginSCIM || got.Username != "ada" || got.Status != store.UserDisabled {
		t.Errorf("ada = %+v, %v; want her as the identity manager wrote her", got, err)
	}
	r.want("what the sync did", r.did(), "")
}

// TestAgentUserWithoutState pins that an agent user whose enabled state
// did not come back fails the read, so a disabled one is never missed.
func TestAgentUserWithoutState(t *testing.T) {
	r, role, g := agentRig(t)
	g.members = []entragraph.Member{{ID: "user-of-ledger", Kind: entragraph.KindAgentUser}}
	r.graph.parents["user-of-ledger"] = idLedger
	r.graph.agents[idLedger] = entraAgent(idLedger, "ledger-agent")
	r.pass(time.Hour)
	r.want("holders", r.holders(role), "")
	r.want("records", r.actions(), "entra.read.stale")
	st, err := ReadStatus(context.Background(), r.st.Settings())
	if err != nil || !strings.Contains(st.Error, "User.Read.All") {
		t.Errorf("the state of the read says %q, %v; want it to name User.Read.All", st.Error, err)
	}
}

// TestSponsorRecordForNone pins that the record of an agent with no
// sponsor in Entra carries no sponsors key.
func TestSponsorRecordForNone(t *testing.T) {
	r, _, g := agentRig(t)
	r.graph.identities[g.id] = []entragraph.Member{identity(idLedger)}
	r.graph.agents[idLedger] = entraAgent(idLedger, "ledger-agent")
	r.pass(time.Hour)
	if len(r.records) != 1 || r.records[0]["action"] != "entra.sponsor.unresolved" {
		t.Fatalf("records = %v, want one entra.sponsor.unresolved", r.records)
	}
	if _, ok := r.records[0]["sponsors"]; ok {
		t.Errorf("the record = %v, want no sponsors key when Entra lists none", r.records[0])
	}
}
