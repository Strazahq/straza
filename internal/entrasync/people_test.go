package entrasync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/store"
)

// member is a person as a member read lists it, with its names and its
// enabled state.
func member(oid, upn, display, mail string, enabled bool) entragraph.Member {
	return entragraph.Member{ID: oid, Kind: entragraph.KindUser, Enabled: &enabled, UPN: upn, DisplayName: display, Mail: mail}
}

func (r *rig) byName(name string) store.User {
	r.t.Helper()
	u, err := r.st.Users().GetByUsername(context.Background(), name)
	if err != nil {
		r.t.Fatalf("the user %s: %v", name, err)
	}
	return u
}

// TestPersonCreatedAndMirrored pins what the sync does to a member that has
// no Straza user: it creates a person of origin entra with the names Entra
// gives and the role, mirrors a rename, and disables and enables the person
// as accountEnabled says while the role stays.
func TestPersonCreatedAndMirrored(t *testing.T) {
	r := newRig(t)
	role := r.role("readers")
	r.pass(0)
	g := r.group(role)
	g.members = []entragraph.Member{
		member("oid-dan", "dan@example.com", "Dan Lab", "dan@mail.example.com", true),
		member("oid-eve", "eve@example.com", "Eve Off", "", false),
	}
	r.pass(time.Hour)
	dan, eve := r.byName("dan@example.com"), r.byName("eve@example.com")
	if dan.Origin != store.OriginEntra || dan.ExternalID != "oid-dan" || dan.UserType != store.UserTypeHuman ||
		dan.Display != "Dan Lab" || dan.Email != "dan@mail.example.com" || dan.Status != store.UserActive {
		t.Errorf("dan = %+v, want an active person of origin entra with Entra's names", dan)
	}
	if eve.Status != store.UserDisabled || eve.Origin != store.OriginEntra {
		t.Errorf("eve = %+v, want a disabled person of origin entra", eve)
	}
	r.want("persons after the first read", r.persons(), "create:dan@example.com:"+reasonCreated+",create:eve@example.com:"+reasonCreated)
	r.want("holders", r.holders(role), "dan@example.com/entra,eve@example.com/entra")
	r.want("members not applied", r.row(role).NotApplied, "[]")

	g.members = []entragraph.Member{
		member("oid-dan", "dan.lab@example.com", "Dan Lab", "", false),
		member("oid-eve", "eve@example.com", "Eve Off", "", true),
	}
	r.pass(time.Hour)
	r.want("persons after the rename, the disable and the enable", r.persons(),
		"update:dan.lab@example.com:username+email:"+reasonChanged+",disable:"+reasonDisabled+":dan.lab@example.com,enable:eve@example.com")
	r.want("holders after the disable", r.holders(role), "dan.lab@example.com/entra,eve@example.com/entra")

	r.pass(time.Hour)
	r.want("persons after a read that changed nothing", r.persons(), "")
}

// TestPersonFound pins how a member is matched to a user that exists: by
// the Entra object id with a takeover of another origin, by the sign-in
// name only for an active person that holds no object id, and never for
// the break-glass account or an agent.
func TestPersonFound(t *testing.T) {
	const oid = "d6d6d6d6-0000-4000-8000-000000000006"
	const other = "d4d4d4d4-0000-4000-8000-000000000004"
	cases := []struct {
		name string
		row  store.User // the user that exists before the read
		gone bool       // the row is deleted before the read
		// wantOrigin and wantExt are the row's origin and external id after
		// the read, wantPersons what the sync reported, and wantReason the
		// reason the member is not applied, "" for a member that got the role.
		wantOrigin, wantExt, wantPersons, wantReason string
	}{
		{name: "a SCIM person with the object id is taken over",
			row:        store.User{Username: "ada@example.com", ExternalID: oid, Origin: store.OriginSCIM},
			wantOrigin: store.OriginEntra, wantExt: oid, wantPersons: "update:ada@example.com:origin:" + reasonTakenOver},
		{name: "a person of origin entra is left as it is",
			row:        store.User{Username: "ada@example.com", ExternalID: oid, Origin: store.OriginEntra},
			wantOrigin: store.OriginEntra, wantExt: oid},
		{name: "an active person linked by the sign-in's subject is linked by name",
			row:        store.User{Username: "ada@example.com", ExternalID: "6UifeRb10MzQbNBLRM9jF1HWW4-VHICHesh7sCCQ0-I", Origin: store.OriginSCIM},
			wantOrigin: store.OriginEntra, wantExt: oid, wantPersons: "update:ada@example.com:external_id+origin:" + reasonLinkedByName},
		{name: "an active local person with no external id is linked by name",
			row:        store.User{Username: "ada@example.com"},
			wantOrigin: store.OriginEntra, wantExt: oid, wantPersons: "update:ada@example.com:external_id+origin:" + reasonLinkedByName},
		{name: "a disabled person is not linked by name",
			row:        store.User{Username: "ada@example.com", ExternalID: "sub-of-a-leaver", Origin: store.OriginSCIM, Status: store.UserDisabled},
			wantOrigin: store.OriginSCIM, wantExt: "sub-of-a-leaver", wantReason: ReasonNameTaken},
		{name: "a person that holds another object id is not linked by name",
			row:        store.User{Username: "ada@example.com", ExternalID: other, Origin: store.OriginSCIM},
			wantOrigin: store.OriginSCIM, wantExt: other, wantReason: ReasonNameTaken},
		{name: "an agent is not linked by name",
			row:        store.User{Username: "ada@example.com", UserType: store.UserTypeAgent},
			wantOrigin: store.OriginLocal, wantReason: ReasonNameTaken},
		{name: "a deleted user keeps the name taken",
			row: store.User{Username: "ada@example.com"}, gone: true, wantReason: ReasonNameTaken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r := newRig(t)
			role := r.role("readers")
			row, err := r.st.Users().Create(ctx, tc.row)
			if err != nil {
				t.Fatal(err)
			}
			if tc.gone {
				if _, err := r.st.Users().SoftDelete(ctx, row.ID, nil); err != nil {
					t.Fatal(err)
				}
			}
			r.pass(0)
			r.group(role).members = []entragraph.Member{member(oid, "ada@example.com", "", "", true)}
			r.pass(time.Hour)

			r.want("persons", r.persons(), tc.wantPersons)
			if tc.wantReason == "" {
				r.want("holders", r.holders(role), "ada@example.com/entra")
				r.want("members not applied", r.row(role).NotApplied, "[]")
			} else {
				r.want("holders", r.holders(role), "")
				r.want("members not applied", r.row(role).NotApplied, `[{"id":"`+oid+`","kind":"user","reason":"`+tc.wantReason+`"}]`)
			}
			if tc.gone {
				return
			}
			after, err := r.st.Users().GetByID(ctx, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Origin != tc.wantOrigin || after.ExternalID != tc.wantExt {
				t.Errorf("the row after the read has origin %q and external id %q, want %q and %q", after.Origin, after.ExternalID, tc.wantOrigin, tc.wantExt)
			}
		})
	}
}

// TestBreakGlassIsNeverWritten pins that the break-glass account is not
// taken over, not linked by name and not given a role.
func TestBreakGlassIsNeverWritten(t *testing.T) {
	for _, ext := range []string{"oid-glass", ""} {
		r := newRig(t)
		role := r.role("readers")
		glass, err := r.st.Users().Create(context.Background(), store.User{Username: "break-glass", ExternalID: ext})
		if err != nil {
			t.Fatal(err)
		}
		r.sync.d.Protected = func(context.Context) string { return glass.ID }
		r.pass(0)
		r.group(role).members = []entragraph.Member{member("oid-glass", "break-glass", "", "", false)}
		r.pass(time.Hour)
		r.want("persons", r.persons(), "")
		r.want("holders", r.holders(role), "")
		if after := r.byName("break-glass"); after.Origin != store.OriginLocal || after.ExternalID != ext || after.Status != store.UserActive {
			t.Errorf("the break-glass account with external id %q after the read = %+v, want it untouched", ext, after)
		}
	}
}

// TestMissingEnabledStateIsStale pins the read of a service app that holds
// no User.Read.All: a member without accountEnabled makes the read
// incomplete, so nobody loses the role, nobody is created, and the state
// turns stale once with the sentence that names the permission.
func TestMissingEnabledStateIsStale(t *testing.T) {
	r := newRig(t)
	role := r.role("readers")
	r.user("ada", "oid-ada")
	r.pass(0)
	g := r.group(role)
	g.members = []entragraph.Member{person("oid-ada")}
	r.pass(time.Hour)
	r.want("holders", r.holders(role), "ada/entra")
	r.persons()
	r.actions()

	g.members = []entragraph.Member{{ID: "oid-new", Kind: entragraph.KindUser, UPN: "new"}}
	r.pass(time.Hour)
	r.pass(time.Minute)
	r.want("holders after the incomplete read", r.holders(role), "ada/entra")
	r.want("persons after the incomplete read", r.persons(), "")
	r.want("records", r.actions(), "entra.read.stale")
	st, err := ReadStatus(context.Background(), r.st.Settings())
	if err != nil || st.StaleSince == nil || !strings.Contains(st.Error, "User.Read.All") {
		t.Errorf("status = %+v, %v; want stale with a sentence that names User.Read.All", st, err)
	}
}

// TestUsersFeed pins the feed of changed users: it starts from now, its
// link is stored and a second syncer continues from it, a disable, a delete
// and an enable reach the person of that account, a change that comes twice
// is applied once, and an account with no person and an agent user are
// dropped.
func TestUsersFeed(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.role("readers")
	on, off := true, false
	ada := r.user("ada", "oid-ada")
	ben := r.user("ben", "oid-ben")
	agent, err := r.st.Users().Create(ctx, store.User{Username: "build-agent", ExternalID: "oid-agent-user", UserType: store.UserTypeAgent})
	if err != nil {
		t.Fatal(err)
	}

	r.pass(0)
	r.want("the first read of the feed", strings.Join(r.graph.usersAsked, ","), "")
	link, err := r.st.Settings().Get(ctx, usersLinkKey)
	if err != nil || link != "https://graph.example/users?token=1" {
		t.Fatalf("the stored link = %q, %v; want the link of the first read", link, err)
	}
	r.want("persons after the first read", r.persons(), "")

	r.graph.users = []entragraph.UserChange{
		{ID: "oid-ada", Enabled: &off, UPN: "ada"},
		{ID: "oid-ada", Enabled: &off, UPN: "ada"},
		{ID: "oid-ben", Removed: true},
		{ID: "oid-stranger", Enabled: &on, UPN: "stranger"},
		{ID: "oid-agent-user", Enabled: &off, UPN: "build-agent", ParentID: "oid-agent"},
		{ID: "oid-agent-user", Enabled: &off, UPN: "build-agent"},
	}
	r.pass(time.Minute)
	r.want("persons after the changes", r.persons(),
		"update:ada:origin:"+reasonTakenOver+",disable:"+reasonDisabled+":ada,"+
			"update:ben:origin:"+reasonTakenOver+",disable:"+reasonDeleted+":ben")
	if _, err := r.st.Users().GetByUsername(ctx, "stranger"); err == nil {
		t.Error("the feed created a user for an account Straza has no person for")
	}
	if after, _ := r.st.Users().GetByID(ctx, agent.ID); after.Status != store.UserActive || after.Origin != store.OriginLocal {
		t.Errorf("the agent after the feed = %+v, want it untouched", after)
	}
	for _, u := range []store.User{ada, ben} {
		if after, _ := r.st.Users().GetByID(ctx, u.ID); after.Status != store.UserDisabled || after.Origin != store.OriginEntra {
			t.Errorf("%s after the feed = %+v, want disabled with origin entra", u.Username, after)
		}
	}

	// Another replica takes the lease and continues from the stored link.
	second := New(r.sync.d)
	r.graph.usersAsked = nil
	r.graph.users = []entragraph.UserChange{{ID: "oid-ada", Enabled: &on, UPN: "ada"}, {ID: "oid-ben", Enabled: &on, UPN: "ben.restored"}}
	r.now = r.now.Add(time.Minute)
	second.Pass(ctx)
	r.want("the link the second syncer asked with", strings.Join(r.graph.usersAsked, ","), "https://graph.example/users?token=2")
	r.want("persons after the enable and the restore", r.persons(),
		"enable:ada,update:ben.restored:username:"+reasonChanged+",enable:ben.restored")

	// Entra no longer knows the link, so the feed starts from now.
	r.graph.usersAsked = nil
	r.graph.usersErr = graphErr(410)
	r.pass(time.Minute)
	r.want("the reads after a lost link", strings.Join(r.graph.usersAsked, "|"), "https://graph.example/users?token=1|")
	if st, _ := ReadStatus(ctx, r.st.Settings()); st.StaleSince != nil {
		t.Errorf("status after a lost link = %+v, want no stale state", st)
	}

	// A failed read keeps the stored link and turns the state stale.
	before, _ := r.st.Settings().Get(ctx, usersLinkKey)
	r.graph.usersErr = graphErr(503)
	r.pass(time.Minute)
	if after, _ := r.st.Settings().Get(ctx, usersLinkKey); after != before {
		t.Errorf("the stored link after a failed read = %q, want %q", after, before)
	}
	if st, _ := ReadStatus(ctx, r.st.Settings()); st.StaleSince == nil {
		t.Error("a failed read of the feed did not turn the state stale")
	}

	// Without people in the setting the feed is not read.
	r.graph.usersAsked = nil
	r.sync.d.Config.Read = nil
	r.pass(time.Minute)
	r.want("reads of the feed when people are not read", strings.Join(r.graph.usersAsked, ","), "")
}

// TestRenameToATakenName pins a rename in Entra to a sign-in name another
// Straza user holds: the display name and the mail are still written, and
// the sign-in name stays.
func TestRenameToATakenName(t *testing.T) {
	r := newRig(t)
	role := r.role("readers")
	r.user("ada", "oid-ada")
	r.user("ben", "oid-holds-the-name")
	r.pass(0)
	g := r.group(role)
	g.members = []entragraph.Member{person("oid-ada")}
	r.pass(time.Hour)
	r.persons()

	g.members = []entragraph.Member{member("oid-ada", "ben", "Ada Lind", "ada@mail.example.com", true)}
	r.pass(time.Hour)
	ada := r.byName("ada")
	if ada.Display != "Ada Lind" || ada.Email != "ada@mail.example.com" {
		t.Errorf("ada after the rename to a taken name = %+v, want the display name and the mail written", ada)
	}
	r.want("persons", r.persons(), "update:ada:display+email:"+reasonChanged)
	r.pass(time.Hour)
	r.want("persons after the next read", r.persons(), "")
}

// TestFeedRefusedNamesThePermission pins the read of the users feed by a
// service app that holds no User.Read.All: the state turns stale with the
// sentence that names the permission.
func TestFeedRefusedNamesThePermission(t *testing.T) {
	r := newRig(t)
	r.role("readers")
	r.graph.usersRefused = true
	r.pass(0)
	st, err := ReadStatus(context.Background(), r.st.Settings())
	if err != nil || st.StaleSince == nil || !strings.Contains(st.Error, "Grant the service app the application permission User.Read.All") {
		t.Errorf("status = %+v, %v; want stale with the sentence that names User.Read.All", st, err)
	}
}

// TestLeaverFreesTheName pins a newcomer who gets the sign-in name of a
// person Entra deleted: the leaver's disabled record is renamed, the agent
// the leaver sponsored follows the leaver and not the name, and the
// newcomer gets a user of their own and the role. A second leaver of the
// same name gets the start of their object id in the new name.
func TestLeaverFreesTheName(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	role := r.role("readers")
	r.pass(0)
	g := r.group(role)
	g.members = []entragraph.Member{member("oid-first", "ada@example.com", "Ada One", "", true)}
	r.pass(time.Hour)
	first := r.byName("ada@example.com")
	agent, err := r.st.Users().Create(ctx, store.User{Username: "ada-agent", UserType: store.UserTypeAgent, Sponsor: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	r.persons()

	// Entra deletes the first ada and gives her name to a newcomer.
	r.graph.users = []entragraph.UserChange{{ID: "oid-first", Removed: true}}
	g.members = []entragraph.Member{member("oid-second", "ada@example.com", "Ada Two", "", true)}
	r.pass(time.Hour)
	r.want("persons", r.persons(),
		"disable:"+reasonDeleted+":ada@example.com,"+
			"update:ada@example.com (left):username:"+reasonNameFreed+","+
			"update:ada-agent:sponsor:"+reasonSponsorMoved+","+
			"create:ada@example.com:"+reasonCreated)
	if after, _ := r.st.Users().GetByID(ctx, first.ID); after.Username != "ada@example.com (left)" || after.Status != store.UserDisabled || after.ExternalID != "oid-first" {
		t.Errorf("the leaver after the newcomer came = %+v, want the name with (left), disabled, with her object id", after)
	}
	second := r.byName("ada@example.com")
	if second.ID == first.ID || second.ExternalID != "oid-second" || second.Display != "Ada Two" {
		t.Errorf("the newcomer = %+v, want a user of their own", second)
	}
	if after, _ := r.st.Users().GetByID(ctx, agent.ID); after.Sponsor != "ada@example.com (left)" {
		t.Errorf("the sponsor of the leaver's agent = %q, want it to stay with the leaver", after.Sponsor)
	}
	r.want("holders", r.holders(role), "ada@example.com/entra")
	r.want("members not applied", r.row(role).NotApplied, "[]")

	// The second ada leaves too, and a third gets the name.
	r.graph.users = []entragraph.UserChange{{ID: "oid-second", Removed: true}}
	g.members = []entragraph.Member{member("oid-third", "ada@example.com", "Ada Three", "", true)}
	r.pass(time.Hour)
	if after, _ := r.st.Users().GetByID(ctx, second.ID); after.Username != "ada@example.com (left oid-seco)" {
		t.Errorf("the second leaver's name = %q, want the start of her object id in it", after.Username)
	}
	if third := r.byName("ada@example.com"); third.ExternalID != "oid-third" {
		t.Errorf("the third ada = %+v, want a user of their own", third)
	}
}

// TestNameOfALivePersonStaysTaken pins the holders of a sign-in name that
// are never renamed for a newcomer: an active person that Entra owns, and
// a disabled person of another origin.
func TestNameOfALivePersonStaysTaken(t *testing.T) {
	for _, row := range []store.User{
		{Username: "ada@example.com", ExternalID: "d4d4d4d4-0000-4000-8000-000000000004", Origin: store.OriginEntra},
		{Username: "ada@example.com", ExternalID: "sub-of-a-leaver", Origin: store.OriginSCIM, Status: store.UserDisabled},
	} {
		r := newRig(t)
		role := r.role("readers")
		if _, err := r.st.Users().Create(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		r.pass(0)
		r.group(role).members = []entragraph.Member{member("oid-new", "ada@example.com", "", "", true)}
		r.pass(time.Hour)
		r.want("persons", r.persons(), "")
		r.want("holders", r.holders(role), "")
		if after := r.byName("ada@example.com"); after.ExternalID != row.ExternalID {
			t.Errorf("the holder of the name after the read = %+v, want it untouched", after)
		}
	}
}

// TestSponsorFollowsARename pins a person renamed in Entra: the agent the
// person sponsors is pointed at the new sign-in name.
func TestSponsorFollowsARename(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	role := r.role("readers")
	r.user("ada", "oid-ada")
	agent, err := r.st.Users().Create(ctx, store.User{Username: "ada-agent", UserType: store.UserTypeAgent, Sponsor: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	r.pass(0)
	g := r.group(role)
	g.members = []entragraph.Member{person("oid-ada")}
	r.pass(time.Hour)
	r.persons()
	g.members = []entragraph.Member{member("oid-ada", "ada.lind", "", "", true)}
	r.pass(time.Hour)
	r.want("persons", r.persons(), "update:ada.lind:username:"+reasonChanged+",update:ada-agent:sponsor:"+reasonSponsorMoved)
	if after, _ := r.st.Users().GetByID(ctx, agent.ID); after.Sponsor != "ada.lind" {
		t.Errorf("the agent's sponsor after the rename = %q, want ada.lind", after.Sponsor)
	}
}
