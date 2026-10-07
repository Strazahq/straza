package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// appDoc is an App document of a remote server at addr with a description
// and the credential block extra, which may be empty.
func appDoc(name, addr, description, extra string) string {
	return fmt.Sprintf(`apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: %s
  description: %q
server:
  name: example.com/%s
  version: "1.0.0"
straza:
  runtime:
    kind: remote
    remote:
      url: %s
%s`, name, description, name, addr, extra)
}

// appFactsOf is the facts of the App document doc, with its manifest as the
// store keeps it.
func appFactsOf(t *testing.T, doc string) drafts.App {
	t.Helper()
	mf, err := manager.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse %s: %v", doc, err)
	}
	manifest, err := mf.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return manifestFacts(drafts.App{Name: mf.Metadata.Name, Manifest: manifest}, mf)
}

// roleText is the canonical Role document of name: owned by server when it
// is set, with a description, and an access row on bind with tools when
// bind is set.
func roleText(t *testing.T, name, server, description, bind string, tools ...string) string {
	t.Helper()
	d := drafts.RoleDoc{APIVersion: drafts.RoleAPIVersion, Kind: string(drafts.KindRole), Metadata: drafts.RoleDocMeta{Name: name},
		Spec: drafts.RoleDocSpec{Kind: drafts.RoleKindApplication, Server: server, Description: description}}
	if bind != "" {
		d.Spec.Bindings = []drafts.RoleBinding{{App: bind, Tools: tools}}
	}
	b, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// standingWorld is live state for the standing rules: the servers github
// and jira, the global application role dev with a row on github, two
// roles github owns, readers that nobody holds and writers that one user
// holds, and the global role stranded, whose row's server is gone.
func standingWorld(t *testing.T) drafts.World {
	server := func(id, name string) drafts.App {
		app := appFactsOf(t, appDoc(name, "https://"+name+".example.com/mcp", "", ""))
		app.ID, app.AdminRole, app.RolePrefix = id, "mcp-admin-"+name, name+"-"
		return app
	}
	role := func(id, name, owner string) drafts.Role {
		return drafts.Role{ID: id, Name: name, Kind: drafts.RoleKindApplication, Plane: drafts.PlaneAccess, Owner: owner, Owned: owner != ""}
	}
	return drafts.World{
		Apps: map[string]drafts.App{"github": server("a1", "github"), "jira": server("a2", "jira")},
		Roles: map[string]drafts.Role{"dev": role("r1", "dev", ""), "github-readers": role("r2", "github-readers", "github"),
			"github-writers": role("r3", "github-writers", "github"), "stranded": role("r4", "stranded", "")},
		Access: map[string]drafts.Access{
			"dev":            {ID: "b1", Server: "github", Tools: []string{"get_me"}},
			"github-readers": {ID: "b2", Server: "github", Tools: []string{"get_me"}},
			"github-writers": {ID: "b3", Server: "github", Tools: []string{"push_files"}},
			"stranded":       {ID: "b4", Server: "", Tools: []string{"get_me"}},
		},
		HolderCounts: map[string]int{"dev": 2, "github-readers": 0, "github-writers": 1, "stranded": 0},
	}
}

// TestStandingRefusal pins the words of step 4 of a publish:
// the first item whose standing the publisher lacks answers, in the words
// of today's rule, or, when the publisher lacks the object's whole area,
// with the standing the verdict's needs name. The standing matches today's
// direct routes, which TestStandingMatchesTheDirectRoutes pins against the
// routes themselves, so this table pins the words and the cases that must
// not tighten what a route allows today.
func TestStandingRefusal(t *testing.T) {
	t.Parallel()
	w := standingWorld(t)
	const (
		d41             = "You cannot publish draft 41: "
		needApps        = "the scope apps:write or the role straza-global-mcp-admin"
		needServer      = "the scope apps:write, the role straza-global-mcp-admin, or the server's admin role mcp-admin-github"
		needIdent       = "the scope identity:write"
		needGlobalBoth  = "the scope identity:write, and the scope apps:write or the role straza-global-mcp-admin for its access row"
		needOwnedRoles  = "the scope identity:write or apps:write, the role straza-global-mcp-admin, or mcp-admin-github"
		needOwnedRow    = "the scope apps:write, the role straza-global-mcp-admin, or mcp-admin-github while nobody holds the role"
		needOwnedRemove = "the scope identity:write, or while nobody holds the role the scope apps:write, the role straza-global-mcp-admin or mcp-admin-github"
		needPolicy      = "the scope policy:write"
		live            = "https://github.example.com/mcp"
		agentTokens     = `  credential:
    kind: oauth
    agents: client_credentials
    oauth: {provider: keycloak}
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
`
	)
	app := func(name, addr, description, extra string) drafts.Item {
		return drafts.Item{Kind: drafts.KindApp, Name: name, Op: drafts.OpPut, Doc: appDoc(name, addr, description, extra)}
	}
	remove := func(kind drafts.Kind, name string) drafts.Item {
		return drafts.Item{Kind: kind, Name: name, Op: drafts.OpRemove}
	}
	role := func(name, server, description, bind string, tools ...string) drafts.Item {
		return drafts.Item{Kind: drafts.KindRole, Name: name, Op: drafts.OpPut, Doc: roleText(t, name, server, description, bind, tools...)}
	}
	set := func(op drafts.Op) drafts.Item {
		return drafts.Item{Kind: drafts.KindPolicySet, Name: "p", Op: op, Doc: "text"}
	}
	erin := administering(personCaller("erin"), map[string]string{"a1": "github"})
	jo := administering(personCaller("jo"), map[string]string{"a2": "jira"})
	ida := personCaller("ida", "identity:write")
	maxi := personCaller("max", "apps:read", "apps:write")
	both := personCaller("both", "identity:write", "apps:write")
	pat := personCaller("pat", "policy:write")
	cases := []struct {
		name  string
		c     draftCaller
		items []drafts.Item
		need  string
		want  string
	}{
		{"root changes a server's address", rootCaller("kim"), []drafts.Item{app("github", "https://other.example.com/mcp", "", "")}, needApps, ""},
		{"the global MCP admin changes a server's address", maxi, []drafts.Item{app("github", "https://other.example.com/mcp", "", "")}, needApps, ""},
		{"a server admin changes its server's description", erin, []drafts.Item{app("github", live, "GitHub tools", "")}, needServer, ""},
		{"a server admin changes its server's address", erin, []drafts.Item{app("github", "https://other.example.com/mcp", "", "")}, needApps,
			d41 + "changing a server's address needs the scope apps:write or the role straza-global-mcp-admin, because the server's credentials and every caller's token are sent to that address. Ask a holder of straza-global-mcp-admin to make that change."},
		{"a server admin gives agents tokens of their own", erin, []drafts.Item{app("github", live, "", agentTokens)}, needApps,
			d41 + "setting credential.agents to client_credentials, or changing the provider of a server that uses it, needs the scope apps:write or the role straza-global-mcp-admin, because every agent that calls the server then gets a token of its own at the provider and the token is sent to the server's address. Ask a holder of straza-global-mcp-admin to make that change."},
		{"a server admin changes another server", jo, []drafts.Item{app("github", live, "GitHub tools", "")}, needServer,
			d41 + "registering a new server needs the scope apps:write or the role straza-global-mcp-admin. You administer 1 server and may change it."},
		{"a server admin removes its server", erin, []drafts.Item{remove(drafts.KindApp, "github")}, needApps,
			d41 + "this draft removes the server github, which needs the scope apps:write or the role straza-global-mcp-admin."},
		{"an identity admin changes a server", ida, []drafts.Item{app("github", live, "GitHub tools", "")}, needServer,
			d41 + "this draft changes the server github, which needs " + needServer + "."},
		{"an identity admin changes a global role's description", ida, []drafts.Item{role("dev", "", "Developers", "github", "get_me")}, needIdent, ""},
		{"an identity admin removes a global role", ida, []drafts.Item{remove(drafts.KindRole, "dev")}, needIdent, ""},
		{"an identity admin changes a global role's access row", ida, []drafts.Item{role("dev", "", "", "github", "get_me", "push_files")}, needApps,
			d41 + "this draft changes the role dev, which needs " + needApps + "."},
		{"the global MCP admin changes only a global role's access row", maxi, []drafts.Item{role("dev", "", "", "github", "get_me", "push_files")}, needApps, ""},
		{"the global MCP admin changes a global role's description", maxi, []drafts.Item{role("dev", "", "Developers", "github", "get_me")}, needIdent,
			d41 + "this draft changes the role dev, which needs the scope identity:write."},
		{"an identity admin creates a global role with an access row", ida, []drafts.Item{role("ops", "", "", "jira", "*")}, needGlobalBoth,
			d41 + "this draft changes the role ops, which needs " + needGlobalBoth + "."},
		{"an identity admin creates a global role with no access row", ida, []drafts.Item{role("ops", "", "Operations", "")}, needIdent, ""},
		{"an identity and apps admin changes a global role and its access row", both, []drafts.Item{role("dev", "", "Developers", "github", "get_me", "push_files")}, needGlobalBoth, ""},
		{"a role document that does not read asks for both areas", ida, []drafts.Item{{Kind: drafts.KindRole, Name: "ops", Op: drafts.OpPut, Doc: "not: a role"}}, needGlobalBoth,
			d41 + "this draft changes the role ops, which needs " + needGlobalBoth + "."},
		{"a put that changes nothing needs nothing", personCaller("nell"), []drafts.Item{role("github-writers", "github", "", "github", "push_files")}, "nothing", ""},
		{"a server admin changes the description of a held role it owns", erin, []drafts.Item{role("github-writers", "github", "Writers", "github", "push_files")}, needOwnedRoles, ""},
		{"an identity admin changes the description of a role a server owns", ida, []drafts.Item{role("github-readers", "github", "Readers", "github", "get_me")}, needOwnedRoles, ""},
		{"an identity admin changes the tools of a role a server owns", ida, []drafts.Item{role("github-readers", "github", "", "github", "get_me", "list_issues")}, needOwnedRow,
			d41 + "this draft changes the role github-readers, which needs " + needOwnedRow + "."},
		{"the global MCP admin changes the tools of a held role github owns", maxi, []drafts.Item{role("github-writers", "github", "", "github", "push_files", "get_me")}, needOwnedRow, ""},
		{"a server admin changes the tools of a role nobody holds", erin, []drafts.Item{role("github-readers", "github", "", "github", "get_me", "list_issues")}, needOwnedRow, ""},
		{"a server admin gives a role it owns every tool", erin, []drafts.Item{role("github-readers", "github", "", "github", "*")}, needOwnedRow,
			d41 + "a server-owned role lists the tools it reaches by name. Only a global admin may give it every tool, and the tools the server adds later, with the * matcher."},
		{"a server admin changes the tools of a held role", erin, []drafts.Item{role("github-writers", "github", "", "github", "push_files", "get_me")}, needOwnedRow,
			d41 + "the role github-writers has 1 holder. Its tools change only by the global admin or by a new role."},
		{"a server admin removes a held role", erin, []drafts.Item{remove(drafts.KindRole, "github-writers")}, needOwnedRemove,
			d41 + "the role github-writers has 1 holder. The identity manager removes them first, then delete it."},
		{"the global MCP admin removes a held role github owns", maxi, []drafts.Item{remove(drafts.KindRole, "github-writers")}, needOwnedRemove,
			d41 + "the role github-writers has 1 holder. The identity manager removes them first, then delete it."},
		{"an identity admin removes a held role github owns", ida, []drafts.Item{remove(drafts.KindRole, "github-writers")}, needOwnedRemove, ""},
		{"a server admin removes a role nobody holds", erin, []drafts.Item{remove(drafts.KindRole, "github-readers")}, needOwnedRemove, ""},
		{"a server admin creates a role of its server", erin, []drafts.Item{role("github-triage", "github", "", "github", "get_me")}, needOwnedRoles, ""},
		{"the global MCP admin creates a role of a server the draft adds", maxi,
			[]drafts.Item{app("gitlab", "https://gitlab.example.com/mcp", "", ""), role("gitlab-readers", "gitlab", "", "gitlab", "get_me")}, needApps, ""},
		{"a server admin creates a role of a server the draft adds", erin,
			[]drafts.Item{role("gitlab-readers", "gitlab", "", "gitlab", "get_me"), app("gitlab", "https://gitlab.example.com/mcp", "", "")}, needOwnedRoles,
			d41 + "this server's admin role is mcp-admin-gitlab, which you do not hold. Ask your identity manager for mcp-admin-gitlab, or a holder of straza-global-mcp-admin to make the change."},
		{"a server admin creates a role without its server's prefix", erin, []drafts.Item{role("triage", "github", "", "github", "get_me")}, needOwnedRoles,
			d41 + "a role of the server github is named github-<suffix>. The server's page fills the prefix for you."},
		{"a server admin of another server changes a role github owns", jo, []drafts.Item{role("github-readers", "github", "Readers", "github", "get_me")}, needOwnedRoles,
			d41 + "this server's admin role is mcp-admin-github, which you do not hold. Ask your identity manager for mcp-admin-github, or a holder of straza-global-mcp-admin to make the change."},
		{"the global MCP admin changes a policy set", maxi, []drafts.Item{set(drafts.OpPut)}, needPolicy, d41 + "this draft changes policy sets, which needs the scope policy:write."},
		{"a policy admin turns a set off", pat, []drafts.Item{set(drafts.OpOff)}, needPolicy, ""},
		{"a policy admin removes a set", pat, []drafts.Item{set(drafts.OpRemove)}, needPolicy, ""},
		{"a server admin takes a row on a removed server", erin, []drafts.Item{role("stranded", "", "", "")}, needApps,
			d41 + "the role stranded keeps an access row on a server that was removed, and only the scope apps:write or the role straza-global-mcp-admin changes that row. Ask a holder of straza-global-mcp-admin to publish this draft."},
	}
	for _, tc := range cases {
		d := drafts.Draft{ID: "41", Items: tc.items}
		in := drafts.CheckInput{Apps: map[string]drafts.App{}}
		var needs []drafts.Need
		for _, it := range tc.items {
			if it.Kind == drafts.KindApp && it.Op == drafts.OpPut {
				facts := appFactsOf(t, it.Doc)
				facts.AdminRole, facts.RolePrefix = store.AppAdminRoleName(it.Name), store.OwnedRolePrefix(it.Name)
				in.Apps[it.Name] = facts
			}
			needs = append(needs, drafts.Need{Object: it.Object(), Standing: tc.need})
		}
		if got := tc.c.standingRefusal(d, w, in, needs); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestStandingRefusalNamesTheFirstItem pins the order of step 4: the first
// item the publisher lacks standing for answers, "also" when an item before
// it passed, and plain words when no verdict names the standing.
func TestStandingRefusalNamesTheFirstItem(t *testing.T) {
	t.Parallel()
	w := standingWorld(t)
	dev := drafts.Item{Kind: drafts.KindRole, Name: "dev", Op: drafts.OpPut, Doc: roleText(t, "dev", "", "Developers", "github", "get_me")}
	set := drafts.Item{Kind: drafts.KindPolicySet, Name: "p", Op: drafts.OpPut, Doc: "text"}
	needs := []drafts.Need{{Object: "Role/dev", Standing: "the scope identity:write"}, {Object: "PolicySet/p", Standing: "the scope policy:write"}}
	ida := personCaller("ida", "identity:write")
	cases := []struct {
		name  string
		items []drafts.Item
		needs []drafts.Need
		want  string
	}{
		{"after an item the publisher may change", []drafts.Item{dev, set}, needs,
			"You cannot publish draft 41: this draft also changes policy sets, which needs the scope policy:write."},
		{"as the first item", []drafts.Item{set, dev}, needs,
			"You cannot publish draft 41: this draft changes policy sets, which needs the scope policy:write."},
		{"with no verdict at hand", []drafts.Item{set}, nil,
			"You cannot publish draft 41: this draft changes policy sets, which needs standing you do not hold."},
	}
	for _, tc := range cases {
		d := drafts.Draft{ID: "41", Items: tc.items}
		if got := ida.standingRefusal(d, w, drafts.CheckInput{}, tc.needs); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestPublishRefusal pins what GET answers as may_publish and
// publish_refusal: step 1 first, so an admin API token reads its own
// sentence whatever it holds, then the standing of every item.
func TestPublishRefusal(t *testing.T) {
	t.Parallel()
	w := standingWorld(t)
	d := drafts.Draft{ID: "41", Items: []drafts.Item{{Kind: drafts.KindPolicySet, Name: "p", Op: drafts.OpPut, Doc: "text"}}}
	v := drafts.Verdict{Needs: []drafts.Need{{Object: "PolicySet/p", Standing: "the scope policy:write"}}}
	fullToken := adminAPICaller("ci")
	fullToken.p.root = true
	cases := []struct {
		name string
		c    draftCaller
		want string
	}{
		{"an admin API token with the scope full", fullToken, publishRefusalAdminAPI},
		{"a person without the standing", personCaller("ida", "identity:write"), "You cannot publish draft 41: this draft changes policy sets, which needs the scope policy:write."},
		{"a person with the standing", personCaller("pat", "policy:write"), ""},
	}
	for _, tc := range cases {
		if got := tc.c.publishRefusal(d, w, drafts.CheckInput{}, v); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSecondPersonRefusal pins admin.secondPerson on the drafts route:
// with the setting on and a risk in the
// verdict, nobody who wrote a revision that is not mechanical publishes, an
// admin API token counting as the person who minted it, through tokens
// that minted tokens too, and a token whose minter is gone refusing every
// publisher. Nor does the sponsor of an agent that wrote one, as the
// agent's row names its sponsor now. A draft with no risk publishes by its
// author, and a revision list with nothing in it fails closed.
func TestSecondPersonRefusal(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Admin.SecondPerson = true })
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	lee := mkHuman(t, app, "lee", AdminRole)
	ada := mkHuman(t, app, "ada")
	mint := func(bearer, name, scope string) string {
		t.Helper()
		var minted struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		}
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", bearer, map[string]any{"name": name, "scope": scope}, &minted); code != http.StatusCreated {
			t.Fatalf("mint %s = %d", name, code)
		}
		return minted.ID + " " + minted.Token
	}
	idOf := func(minted string) string { return strings.SplitN(minted, " ", 2)[0] }
	kimLogin, leeLogin := loginDeviceFlow(t, base, "kim", "hunter2!"), loginDeviceFlow(t, base, "lee", "hunter2!")
	kimCI := idOf(mint(kimLogin, "kim-ci", "identity:read"))
	kimRoot := mint(kimLogin, "kim-root", "full")
	chained := idOf(mint(strings.SplitN(kimRoot, " ", 2)[1], "chained", "identity:read"))
	gone := idOf(mint(leeLogin, "gone-ci", "identity:read"))
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/api-tokens/"+gone, leeLogin, nil, nil); code != http.StatusOK {
		t.Fatalf("revoke gone-ci = %d", code)
	}
	bot := seedAgent(t, app, "bot", "lee", "dev")
	gonebot := seedAgent(t, app, "gonebot", "kim", "dev")
	if _, err := app.store.Users().SoftDelete(ctx, gonebot.ID); err != nil {
		t.Fatal(err)
	}
	as := func(u store.User) draftCaller {
		c := personCaller(u.ID)
		c.author.Username = u.Username
		return c
	}
	rev := func(a store.DraftActor, mechanical bool) store.DraftRevisionRow {
		return store.DraftRevisionRow{Author: a, Mechanical: mechanical}
	}
	person := func(u store.User) store.DraftActor {
		return store.DraftActor{ID: u.ID, Name: u.Username, Via: laneSession, Client: "console"}
	}
	token := func(id, name string) store.DraftActor {
		return store.DraftActor{ID: id, Name: name, Via: laneAdminAPI, Client: clientAdminAPI}
	}
	agent := func(u store.User, sponsorID, sponsor string) store.DraftActor {
		return store.DraftActor{ID: u.ID, Name: u.Username, Via: laneLogin, Client: clientLogin, Agent: true, SponsorID: sponsorID, SponsorName: sponsor}
	}
	risky := drafts.Verdict{Risks: []drafts.Finding{{Code: "access.ungated", Class: drafts.ClassRisk, Object: "Role/dev"}}}
	safe := drafts.Verdict{Risks: []drafts.Finding{}}
	off := &App{}
	const (
		author  = "You changed this draft, and this deployment needs a second person to publish a change that widens access (admin.secondPerson). Ask another administrator with standing over every object in it to review and publish it."
		minted  = "You minted the admin API token %s, which changed this draft, and this deployment needs a second person to publish a change that widens access (admin.secondPerson). Ask another administrator with standing over every object in it to review and publish it."
		unknown = "The admin API token gone-ci changed this draft, and Straza cannot tell who minted it, so it cannot tell whether you are a second person, which this deployment needs to publish a change that widens access (admin.secondPerson). Create a new draft from its documents and ask another administrator to publish it."
		sponsor = "You sponsor %s, which proposed this draft, and this deployment needs a publisher other than the proposer and its sponsor for a change that widens access (admin.secondPerson). Ask another administrator to review and publish it."
		unread  = "Straza could not read who wrote this draft, so it cannot tell whether you are a second person, and it refuses the publish. Try again, and check the strazad log if it keeps failing."
	)
	cases := []struct {
		name string
		a    *App
		c    draftCaller
		revs []store.DraftRevisionRow
		v    drafts.Verdict
		kind drafts.RefusalKind
		want string
	}{
		{"the setting off lets an author publish a risk", off, as(ada), []store.DraftRevisionRow{rev(person(ada), false)}, risky, 0, ""},
		{"an author publishes a draft with no risk", app, as(ada), []store.DraftRevisionRow{rev(person(ada), false)}, safe, 0, ""},
		{"an author of a risky draft", app, as(ada), []store.DraftRevisionRow{rev(agent(bot, "", "lee"), false), rev(person(ada), false)}, risky, drafts.RefusalConflict, author},
		{"a mechanical revision makes no author", app, as(ada), []store.DraftRevisionRow{rev(person(kim), false), rev(person(ada), true)}, risky, 0, ""},
		{"the minter of a token that wrote a revision", app, as(kim), []store.DraftRevisionRow{rev(token(kimCI, "kim-ci"), false)}, risky, drafts.RefusalConflict, fmt.Sprintf(minted, "kim-ci")},
		{"the minter of a token a token of theirs minted", app, as(kim), []store.DraftRevisionRow{rev(token(chained, "chained"), false)}, risky, drafts.RefusalConflict, fmt.Sprintf(minted, "chained")},
		{"another administrator than the token's minter", app, as(lee), []store.DraftRevisionRow{rev(token(kimCI, "kim-ci"), false)}, risky, 0, ""},
		{"a token whose minter is gone", app, as(kim), []store.DraftRevisionRow{rev(token(gone, "gone-ci"), false)}, risky, drafts.RefusalConflict, unknown},
		{"the agent's sponsor now, whom the revision did not record", app, as(lee), []store.DraftRevisionRow{rev(agent(bot, kim.ID, "kim"), false)}, risky, drafts.RefusalConflict, fmt.Sprintf(sponsor, "bot")},
		{"the sponsor the revision recorded, who sponsors the agent no more", app, as(kim), []store.DraftRevisionRow{rev(agent(bot, kim.ID, "kim"), false)}, risky, 0, ""},
		{"the recorded sponsor of an agent whose row is gone", app, as(kim), []store.DraftRevisionRow{rev(agent(gonebot, kim.ID, "kim"), false)}, risky, drafts.RefusalConflict, fmt.Sprintf(sponsor, "gonebot")},
		{"an agent whose only revision is mechanical", app, as(lee), []store.DraftRevisionRow{rev(person(ada), false), rev(agent(bot, "", "lee"), true)}, risky, 0, ""},
		{"no revision to judge by", app, as(lee), nil, risky, drafts.RefusalUnread, unread},
	}
	for _, tc := range cases {
		got, err := tc.a.secondPersonRefusal(ctx, tc.c, tc.revs, tc.v)
		switch {
		case err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want == "" && got != nil:
			t.Errorf("%s: refused %+v, want a pass", tc.name, got)
		case tc.want != "" && (got == nil || got.Kind != tc.kind || got.Sentence != tc.want):
			t.Errorf("%s:\n got %+v\nwant %d %q", tc.name, got, tc.kind, tc.want)
		}
	}
}

// TestRolesRouteFailsClosedWithoutACount pins that a removal that turns on
// who holds a role refuses when the World did not count them, as the
// access row rules do, so a World read for one rule never passes a held
// role.
func TestRolesRouteFailsClosedWithoutACount(t *testing.T) {
	t.Parallel()
	w := standingWorld(t)
	w.HolderCounts = nil
	erin := administering(personCaller("erin"), map[string]string{"a1": "github"})
	d := drafts.Draft{ID: "41", Items: []drafts.Item{{Kind: drafts.KindRole, Name: "github-writers", Op: drafts.OpRemove}}}
	want := "Straza did not count who holds github-writers, so it cannot tell whether the role may be removed. Try again, and read the strazad log if it keeps failing."
	if got := erin.directStandingRefusal(d, w, drafts.CheckInput{}); got == nil || got.Kind != drafts.RefusalUnread || got.Sentence != want {
		t.Errorf("removal with no count = %+v, want the unread refusal %q", got, want)
	}
}

// TestDirectSecondPersonRefusal pins admin.secondPerson on a direct admin
// route: a one-item verdict with a risk is refused whoever calls it,
// and nothing else is.
func TestDirectSecondPersonRefusal(t *testing.T) {
	t.Parallel()
	on, off := &App{}, &App{}
	on.cfg.Admin.SecondPerson = true
	risky := drafts.Verdict{Risks: []drafts.Finding{{Code: "server.new-host", Class: drafts.ClassRisk, Object: "App/github"}}}
	cases := []struct {
		name string
		a    *App
		v    drafts.Verdict
		want string
	}{
		{"on, with a risk", on, risky,
			"This deployment needs a second person to publish a change that widens access (admin.secondPerson), so this route cannot make it alone. Save it as a draft on the console under Drafts or with strazactl drafts create, and ask another administrator to publish it."},
		{"on, with no risk", on, drafts.Verdict{}, ""},
		{"off, with a risk", off, risky, ""},
	}
	for _, tc := range cases {
		if got := tc.a.directSecondPersonRefusal(tc.v); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestReviseRefusal pins the revise rule: a person revises any
// draft, and an admin API token or an agent only a draft it alone wrote.
func TestReviseRefusal(t *testing.T) {
	t.Parallel()
	ada := drafts.Principal{UserID: "u-ada", Username: "ada", Via: laneSession}
	ci := drafts.Principal{UserID: "t-ci", Username: "ci", Via: laneAdminAPI}
	bot := drafts.Principal{UserID: "u-bot", Username: "bot", Via: laneLogin, Agent: true}
	cases := []struct {
		name    string
		c       draftCaller
		authors []drafts.Principal
		want    string
	}{
		{"a person revises another's draft", personCaller("u-lee"), []drafts.Principal{ci, bot}, ""},
		{"a token revises the draft it wrote", adminAPICaller("t-ci"), []drafts.Principal{ci}, ""},
		{"a token revises a draft a person also wrote", adminAPICaller("t-ci"), []drafts.Principal{ci, ada},
			"An admin API token changes only the drafts it wrote, and draft 41 was written by someone else. A person can revise it on the console or with strazactl."},
		{"an agent revises the draft it wrote", agentCaller("u-bot"), []drafts.Principal{bot}, ""},
		{"an agent revises a person's draft", agentCaller("u-bot"), []drafts.Principal{ada},
			"An agent changes only the drafts it wrote, and draft 41 was written by someone else. A person can revise it on the console or with strazactl."},
	}
	for _, tc := range cases {
		if got := tc.c.reviseRefusal("41", tc.authors); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDiscardRefusal pins the discard rule over live state: its
// authors, root, and a person with standing over every item discard a
// draft, and nobody else does.
func TestDiscardRefusal(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	if _, err := app.store.Roles().Create(ctx, store.Role{Name: "dr-dev", Kind: store.RoleKindApplication}); err != nil {
		t.Fatal(err)
	}
	ada := drafts.Principal{UserID: "u-ada", Username: "ada", Via: laneSession}
	d := drafts.Draft{ID: "41", Authors: []drafts.Principal{ada},
		Items: []drafts.Item{{Kind: drafts.KindRole, Name: "dr-dev", Op: drafts.OpPut, Doc: roleText(t, "dr-dev", "", "Developers", "")}}}
	refusal := "Discarding draft 41 needs being one of its authors, the root role, or standing over every object in it. Ask one of its authors or an administrator."
	fullToken := adminAPICaller("t-full")
	fullToken.p.root = true
	cases := []struct {
		name string
		c    draftCaller
		want string
	}{
		{"its author", personCaller("u-ada"), ""},
		{"root", rootCaller("u-kim"), ""},
		{"a person with standing over every item", personCaller("u-ida", "identity:write"), ""},
		{"a person without it", personCaller("u-max", "apps:read", "apps:write"), refusal},
		{"an admin API token that did not write it", adminAPICaller("t-ci", "drafts:write", "identity:write"), refusal},
		{"an admin API token with the scope full that did not write it", fullToken, refusal},
		{"an agent that did not write it", agentCaller("u-bot", "identity:write"), refusal},
	}
	for _, tc := range cases {
		got, err := app.discardRefusal(ctx, tc.c, d)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q %v, want %q", tc.name, got, err, tc.want)
		}
	}
}

// TestConfigAnswerShowsTheSecondPersonSetting pins the admin block of GET
// /v1/admin/config: admin.second_person is the file's admin.secondPerson,
// false by default.
func TestConfigAnswerShowsTheSecondPersonSetting(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{false, true} {
		app, base := testApp(t, func(c *config.Config) { c.Admin.SecondPerson = on })
		kim := seedIdentity(t, app)
		grantAdmin(t, app, kim.ID)
		var out struct {
			Admin map[string]any `json:"admin"`
		}
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/config", loginDeviceFlow(t, base, "kim", "hunter2!"), nil, &out); code != http.StatusOK {
			t.Fatalf("config = %d", code)
		}
		if got, ok := out.Admin["second_person"].(bool); !ok || got != on || len(out.Admin) != 1 {
			t.Errorf("admin block = %v with the setting %v, want second_person %v alone", out.Admin, on, on)
		}
	}
}
