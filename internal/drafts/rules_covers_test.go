package drafts

import (
	"reflect"
	"strings"
	"testing"
)

// ghappPinned is the pinned repo type of ghapp as a check reads it: two
// argument tools, a tool that touches no repo, one that reaches every repo,
// one whose credential limits it, the listing tool unnamed, and the object
// list holding three repos.
func ghappPinned() *PinnedObjects {
	return &PinnedObjects{Type: "repo", Plural: "repos", ListTool: "repo_list", Credential: "set", LiveType: "repo",
		Reach: map[string]string{"pr_create": "argument", "issue_create": "argument", "get_me": "none", "repo_create": "all", "query": "credential"},
		IDs:   map[string]string{"Strazahq/pay-api": "101", "Strazahq/pay-web": "102", "Strazahq/site": "103"}}
}

// coversWorld is checkWorld with ghapp, a server that pins repos, its role
// on every repo, its role on one repo, a role from before the column with
// no covers line, and brave-search, a server that pins nothing.
func coversWorld() World {
	w := checkWorld()
	w.Apps["ghapp"] = App{ID: "app-ghapp", Name: "ghapp", Status: "running", AdminRole: "mcp-admin-ghapp", RolePrefix: "ghapp-",
		Runtime: "remote", URL: "https://ghapp.example.com/mcp", Credential: CredentialToken,
		Offered: []string{"get_me", "issue_create", "pr_create", "query", "repo_create", "repo_list"}, Pinned: ghappPinned()}
	w.Apps["brave-search"] = App{ID: "app-brave", Name: "brave-search", Status: "running", AdminRole: "mcp-admin-brave-search",
		RolePrefix: "brave-search-", Runtime: "remote", URL: "https://brave.example.com/mcp", Credential: CredentialNone}
	for _, name := range []string{"ghapp-read", "ghapp-write-payments", "ghapp-legacy", "brave-search-users"} {
		server := strings.SplitN(name, "-", 2)[0]
		if strings.HasPrefix(name, "brave") {
			server = "brave-search"
		}
		w.Roles[name] = Role{ID: "id-" + name, Name: name, Kind: RoleKindApplication, Plane: PlaneAccess, Owner: server, Owned: true}
		w.Fingerprints["Role/"+name] = Fingerprint("fp-Role/" + name)
	}
	w.Access["ghapp-read"] = Access{ID: "b-read", Server: "ghapp", Tools: []string{"get_me", "pr_create"}, Covers: &Scope{Every: true}}
	w.Access["ghapp-write-payments"] = Access{ID: "b-pay", Server: "ghapp", Tools: []string{"pr_create"},
		Covers: &Scope{Objects: []ObjectRef{{Value: "Strazahq/pay-api", ID: "101"}}}}
	w.Access["ghapp-legacy"] = Access{ID: "b-legacy", Server: "ghapp", Tools: []string{"get_me"}}
	w.Access["brave-search-users"] = Access{ID: "b-brave", Server: "brave-search", Tools: []string{"search"}}
	w.Fingerprints["App/ghapp"] = "fp-App/ghapp"
	w.HolderCounts = map[string]int{}
	return w
}

// coversRole is the Role put of a role ghapp owns with tools and the
// covers line given as YAML, empty for none.
func coversRole(name, server, tools, covers string) Item {
	spec := "    kind: application\n    server: " + server + "\n    bindings:\n        - app: " + server + "\n          tools: " + tools + "\n"
	if covers != "" {
		spec += "          covers: " + covers + "\n"
	}
	return intakeRole(name, spec)
}

// coversFindings answers the code, object and sentence of every finding
// of the covers rules and the objects notes in v, in verdict order.
func coversFindings(v Verdict) []string {
	var out []string
	for _, list := range [][]Finding{v.Refused, v.Warnings, v.Info} {
		for _, f := range list {
			if strings.HasPrefix(f.Code, "access.covers") || strings.Contains(f.Code, "objects-") {
				out = append(out, f.Code+" "+f.Object+" | "+f.Sentence)
			}
		}
	}
	return out
}

// TestCheckRefusesACoversLineTheGateCannotKeep pins the draft rules of the
// covers line: a binding on a pinned server must say which repos it covers,
// each named repo must be in the object list, once, a role on some repos
// holds no tool that nothing keeps to them, and a server that pins nothing
// takes no covers line. A row the put keeps as live state holds it, by id,
// meets none of them.
func TestCheckRefusesACoversLineTheGateCannotKeep(t *testing.T) {
	t.Parallel()
	const (
		r1     = "access.covers-missing Role/ghapp-write | ghapp-write binds tools that work on repos. Say which repos it covers: every repo, or these repos by name."
		r2     = "access.covers-unknown Role/ghapp-write | Strazahq/pay-old is not in ghapp's repo list. Compare the name with ghapp's Repos tab or strazactl apps objects ghapp. If the repo is new, refresh the list with strazactl apps objects refresh ghapp, then try again."
		r3     = "access.covers-reach Role/ghapp-write | repo_create cannot be in a role on some repos: nothing keeps it to them. Make a role on every repo for it."
		r3l    = "access.covers-reach Role/ghapp-write | repo_list cannot be in a role on some repos: nothing keeps it to them. Make a role on every repo for it."
		r3c    = "access.covers-reach Role/ghapp-write | query cannot be in a role on some repos: its calls do not name a repo, and Straza does not check a role's own login yet. Make a role on every repo for it."
		r5     = "access.covers-unpinned Role/brave-search-users | brave-search-users has a covers line on brave-search, which pins no object type. Remove covers from that binding, or pin brave-search's objects first."
		r7     = "access.covers-twice Role/ghapp-write | ghapp-write names Strazahq/pay-api twice under covers. Name each repo once."
		r8     = "access.covers-empty Role/ghapp-write | ghapp-write's covers line names no repos. Name at least one, or write covers: every."
		r7same = "access.covers-twice Role/ghapp-write | ghapp-write names Strazahq/pay-api and Strazahq/pay-api-v2, which are the same repo. Name it once."
		glob   = "access.covers-reach Role/ghapp-write | * cannot be in a role on some repos: nothing keeps it to them. Make a role on every repo for it."
	)
	cases := []struct {
		name string
		item Item
		want []string
	}{
		{"no covers line on a pinned server", coversRole("ghapp-write", "ghapp", "[pr_create]", ""), []string{r1}},
		{"every repo", coversRole("ghapp-write", "ghapp", "[pr_create, repo_create, query]", "every"), nil},
		{"repos the list holds", coversRole("ghapp-write", "ghapp", "[get_me, issue_create, pr_create]", "{objects: [Strazahq/pay-api, Strazahq/site]}"), nil},
		{"a repo the list does not hold", coversRole("ghapp-write", "ghapp", "[pr_create]", "{objects: [Strazahq/pay-api, Strazahq/pay-old]}"), []string{r2}},
		{"a tool that reaches every repo", coversRole("ghapp-write", "ghapp", "[pr_create, repo_create]", "{objects: [Strazahq/pay-api]}"), []string{r3}},
		{"a tool its credential limits", coversRole("ghapp-write", "ghapp", "[query]", "{objects: [Strazahq/pay-api]}"), []string{r3c}},
		{"a glob over every tool", coversRole("ghapp-write", "ghapp", "[\"*\"]", "{objects: [Strazahq/pay-api]}"), []string{r3c, r3, r3l}},
		{"a glob on a server nobody listed", coversRole("ghapp-write", "ghapp", "[\"*\", pr_create]", "{objects: [Strazahq/pay-api]}"), []string{glob}},
		{"a repo named twice", coversRole("ghapp-write", "ghapp", "[pr_create]", "{objects: [Strazahq/pay-api, Strazahq/pay-api]}"), []string{r7}},
		{"no repos", coversRole("ghapp-write", "ghapp", "[pr_create]", "{objects: []}"), []string{r8}},
		{"a covers line on a server that pins nothing", coversRole("brave-search-users", "brave-search", "[search]", "every"), []string{r5}},
		{"two names of one repo", coversRole("ghapp-write", "ghapp", "[pr_create]", "{objects: [Strazahq/pay-api, Strazahq/pay-api-v2]}"), []string{r7same}},
		{"a kept row from before the column", coversRole("ghapp-legacy", "ghapp", "[get_me]", ""), nil},
		{"a kept row whose repo the list no longer names", coversRole("ghapp-write-payments", "ghapp", "[pr_create]", "{objects: [Strazahq/pay-api]}"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := coversWorld()
			app := w.Apps["ghapp"]
			switch tc.name {
			case "a kept row whose repo the list no longer names", "two names of one repo":
				// Facts read at another moment than the live row can name
				// one id under two values.
				app.Pinned.IDs = map[string]string{"Strazahq/pay-api-v2": "101", "Strazahq/pay-api": "101"}
			case "a glob on a server nobody listed":
				app.Offered = nil
			}
			w.Apps["ghapp"] = app
			v := Check(w, stamped(w, tc.item), CheckInput{Now: checkNow, RefusalsOnly: true})
			got := coversFindings(v)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("findings\n got %q\nwant %q\nall refusals %+v", got, tc.want, v.Refused)
			}
		})
	}
}

// TestCheckReadsARenameAsNoChange pins that a covers line compares by id: a
// draft that names a repo by its new name keeps the row, so the draft
// changes nothing, and one that names another repo changes the row.
func TestCheckReadsARenameAsNoChange(t *testing.T) {
	t.Parallel()
	w := coversWorld()
	acc := w.Access["ghapp-write-payments"]
	acc.Covers = &Scope{Objects: []ObjectRef{{Value: "Strazahq/pay-api-old", ID: "101"}}}
	w.Access["ghapp-write-payments"] = acc
	renamed := Check(w, stamped(w, coversRole("ghapp-write-payments", "ghapp", "[pr_create]", "{objects: [Strazahq/pay-api]}")), CheckInput{Now: checkNow})
	if len(renamed.Refused) > 0 || !hasCode(renamed.Info, "info.no-change") || len(renamed.Gains) > 0 {
		t.Errorf("a rename reads as a change: refused %+v, info %+v, gains %+v", renamed.Refused, renamed.Info, renamed.Gains)
	}
	other := Check(w, stamped(w, coversRole("ghapp-write-payments", "ghapp", "[pr_create]", "{objects: [Strazahq/site]}")), CheckInput{Now: checkNow})
	if hasCode(other.Info, "info.no-change") {
		t.Errorf("another repo reads as no change: %+v", other.Info)
	}
}

// hasCode reports whether fs holds a finding with code.
func hasCode(fs []Finding, code string) bool {
	for _, f := range fs {
		if f.Code == code {
			return true
		}
	}
	return false
}

// TestCheckJudgesAChangeOfThePinnedType pins the App put rules of the
// covers line: the first pin keeps today's reach and names the roles that
// now cover every repo, warns when Straza cannot list the repos, a type
// change is refused while a role names repos of the old type, an unpin
// names the roles that lose their repos, and a re-pin that makes a tool
// reach every repo, or the credential's repos, warns for each role on some
// repos that binds it.
func TestCheckJudgesAChangeOfThePinnedType(t *testing.T) {
	t.Parallel()
	const (
		n1 = "info.objects-first-pin App/ghapp | Pinning repos on ghapp keeps today's reach: ghapp-legacy, ghapp-read and ghapp-write-payments now cover every repo. Narrow them in this draft or later."
		n2 = "ready.objects-listing App/ghapp | ghapp has no listing credential, so Straza cannot list its repos, and only roles on every repo can use its repo tools until it has one. After you publish, set one under Server authentication on the Overview of ghapp, or with strazactl apps secret set ghapp --listing."
		r6 = "app.objects-type App/ghapp | ghapp's pinned type changes from repo to project while ghapp-write-payments names repos. Make those roles cover every project, or remove their access rows on ghapp, in this draft."
		n3 = "info.objects-unpin App/ghapp | Removing ghapp's pinned repos makes every role on ghapp reach every repo again: ghapp-write-payments loses its list of repos."
		m1 = "access.covers-missing Role/ghapp-write-payments | ghapp-write-payments binds tools that work on projects. Say which projects it covers: every project, or these projects by name."
		w1 = "ready.objects-reach App/ghapp | After this draft, pr_create reaches every repo on ghapp, so ghapp-write-payments, a role on some repos, can no longer call it. Take pr_create out of that role, or make it cover every repo, in this draft or later."
		w2 = "ready.objects-reach App/ghapp | After this draft, pr_create reaches every repo on ghapp, so ghapp-write-payments and ghapp-write-site, roles on some repos, can no longer call it. Take pr_create out of those roles, or make them cover every repo, in this draft or later."
		wc = "ready.objects-reach App/ghapp | After this draft, the calls of pr_create on ghapp name no repo, and Straza does not check a role's own login yet, so ghapp-write-payments, a role on some repos, can no longer call it. Take pr_create out of that role, or make it cover every repo, in this draft or later."
	)
	repin := func(tool, reach string) func(*PinnedObjects) *PinnedObjects {
		return func(p *PinnedObjects) *PinnedObjects {
			if reach == "" {
				delete(p.Reach, tool)
			} else {
				p.Reach[tool] = reach
			}
			return p
		}
	}
	project := func(p *PinnedObjects) *PinnedObjects {
		p.Type, p.Plural = "project", "projects"
		return p
	}
	secondRole := func(w World) {
		w.Roles["ghapp-write-site"] = Role{ID: "id-ghapp-write-site", Name: "ghapp-write-site", Kind: RoleKindApplication, Plane: PlaneAccess, Owner: "ghapp", Owned: true}
		w.Access["ghapp-write-site"] = Access{ID: "b-site", Server: "ghapp", Tools: []string{"get_me", "pr_create"},
			Covers: &Scope{Objects: []ObjectRef{{Value: "Strazahq/site", ID: "103"}}}}
	}
	firstPin := func(w World) {
		app := w.Apps["ghapp"]
		app.Pinned = nil
		w.Apps["ghapp"] = app
		for _, name := range []string{"ghapp-read", "ghapp-write-payments"} {
			acc := w.Access[name]
			acc.Covers = nil
			w.Access[name] = acc
		}
	}
	cases := []struct {
		name  string
		live  func(World)
		after func(*PinnedObjects) *PinnedObjects
		items []Item
		want  []string
	}{
		{"the first pin", firstPin, func(p *PinnedObjects) *PinnedObjects { p.LiveType = ""; return p }, nil, []string{n1}},
		{"the first pin with no listing credential", firstPin, func(p *PinnedObjects) *PinnedObjects {
			p.LiveType, p.Credential = "", "missing"
			return p
		}, nil, []string{n2, n1}},
		{"the first pin with a role narrowed in the draft", firstPin, func(p *PinnedObjects) *PinnedObjects { p.LiveType = ""; return p },
			[]Item{coversRole("ghapp-legacy", "ghapp", "[get_me]", "{objects: [Strazahq/site]}")},
			[]string{strings.Replace(n1, "ghapp-legacy, ghapp-read and", "ghapp-read and", 1)}},
		{"a type change while a role names repos", nil, project, nil, []string{r6}},
		{"a type change once the role covers every project", nil, project,
			[]Item{coversRole("ghapp-write-payments", "ghapp", "[pr_create]", "every")}, nil},
		{"a type change once the role's covers line is removed", nil, project,
			[]Item{coversRole("ghapp-write-payments", "ghapp", "[pr_create]", "")}, []string{m1}},
		{"a type change once the role's access row is removed", nil, project,
			[]Item{intakeRole("ghapp-write-payments", "    kind: application\n    server: ghapp\n")}, nil},
		{"an unpin", nil, func(*PinnedObjects) *PinnedObjects { return nil }, nil, []string{n3}},
		{"a re-pin that makes pr_create reach every repo", nil, repin("pr_create", reachAll), nil, []string{w1}},
		{"a re-pin that makes pr_create reach every repo for two roles", secondRole, repin("pr_create", reachAll), nil, []string{w2}},
		{"a re-pin to credential", nil, repin("pr_create", reachCredential), nil, []string{wc}},
		{"a tool left out of the pinned block", nil, repin("pr_create", ""), nil, []string{w1}},
		{"a re-pin of a tool no role on some repos binds", nil, repin("issue_create", reachAll), nil, nil},
		{"a re-pin with the role narrowed in the same draft", nil, repin("pr_create", reachAll),
			[]Item{coversRole("ghapp-write-payments", "ghapp", "[get_me]", "{objects: [Strazahq/pay-api]}")}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := coversWorld()
			if tc.live != nil {
				tc.live(w)
			}
			app := coversWorld().Apps["ghapp"]
			app.Manifest, app.Pinned = `{"after":true}`, tc.after(ghappPinned())
			d := stamped(w, append([]Item{appPut("ghapp")}, tc.items...)...)
			v := Check(w, d, CheckInput{Apps: map[string]App{"ghapp": app}, Now: checkNow})
			if got := coversFindings(v); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("findings\n got %q\nwant %q\nrefused %+v", got, tc.want, v.Refused)
			}
		})
	}
}

// TestWhoGainsReadsTheCoversLine pins the granted fact the probe carries on
// a pinned server: a role on some repos is granted the tools whose calls
// name a repo and the tools that touch none, never a tool that reaches
// every repo, so widening the role to every repo gains that tool alone.
func TestWhoGainsReadsTheCoversLine(t *testing.T) {
	t.Parallel()
	w := coversWorld()
	w.Holders["ghapp-write-payments"] = []Holder{{Username: "erin", UserType: "human", AgencyMode: "interactive"}}
	acc := w.Access["ghapp-write-payments"]
	acc.Tools = []string{"get_me", "pr_create", "repo_create"}
	w.Access["ghapp-write-payments"] = acc
	d := stamped(w, coversRole("ghapp-write-payments", "ghapp", "[get_me, pr_create, repo_create]", "every"))
	v := Check(w, d, CheckInput{Now: checkNow})
	want := []string{"ghapp-write-payments ghapp/repo_create not-reachable>runs erin"}
	if got := gainLines(v.Gains); !reflect.DeepEqual(got, want) {
		t.Errorf("gains\n got %q\nwant %q\nrefused %+v", got, want, v.Refused)
	}
}

// TestServerAdminsCoverEvery pins who may make a role cover every object:
// a global admin always, a server admin on a role their server owns while
// admin.serverAdminsCoverEvery is on, and nobody else. A server admin may
// always name the objects, and a row that already covered every object
// does not newly cover it.
func TestServerAdminsCoverEvery(t *testing.T) {
	t.Parallel()
	const r4 = "On this deployment only a global admin may make ghapp-write cover every repo, including the repos ghapp lists later. Name the repos it covers, or ask a global admin."
	full := Standing{Full: true}
	erin := Standing{Servers: map[string]bool{"app-ghapp": true}}
	every, some := &Scope{Every: true}, &Scope{Objects: []ObjectRef{{Value: "Strazahq/pay-api"}}}
	cases := []struct {
		name    string
		st      Standing
		setting bool
		covers  *Scope
		want    string
	}{
		{"a global admin, the setting off", full, false, every, ""},
		{"a server admin, the setting on", erin, true, every, ""},
		{"a server admin, the setting off", erin, false, every, r4},
		{"a server admin names repos, the setting off", erin, false, some, ""},
		{"a server admin, no covers line", erin, false, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := coversWorld()
			w.ServerAdminsCoverEvery = tc.setting
			w.Roles["ghapp-write"] = Role{ID: "id-gw", Name: "ghapp-write", Kind: RoleKindApplication, Plane: PlaneAccess, Owner: "ghapp", Owned: true}
			w.HolderCounts["ghapp-write"] = 0
			kind := RefusalKind(0)
			if tc.want != "" {
				kind = RefusalInvalid
			}
			create := RoleSpec{Name: "ghapp-write2", Server: "ghapp", Tools: []string{"pr_create"}, Covers: tc.covers}
			want := strings.Replace(tc.want, "ghapp-write ", "ghapp-write2 ", 1)
			wantRefusal(t, RoleCreateRefusal(w, tc.st, create), kind, want)
			wantRefusal(t, AccessCreateRefusal(w, tc.st, "ghapp-write", "ghapp", []string{"pr_create"}, tc.covers != nil && tc.covers.Every), kind, tc.want)
		})
	}
}

// TestOverlayResolvesCoversByTheLiveRowFirst pins how a draft's values
// become ids: a value the role's live row names keeps the live id, so
// restating a role never moves it onto an object created later under a
// deleted object's name, any other value takes the object list's id, and a
// value neither holds keeps an empty id.
func TestOverlayResolvesCoversByTheLiveRowFirst(t *testing.T) {
	t.Parallel()
	w := coversWorld()
	app := w.Apps["ghapp"]
	app.Pinned.IDs = map[string]string{"Strazahq/pay-api": "201", "Strazahq/site": "103"}
	w.Apps["ghapp"] = app
	d := Draft{Items: []Item{
		coversRole("ghapp-write-payments", "ghapp", "[pr_create]", "{objects: [Strazahq/pay-api, Strazahq/site, Strazahq/gone]}"),
		coversRole("ghapp-new", "ghapp", "[pr_create]", "{objects: [Strazahq/pay-api]}"),
	}}
	after := w.Overlay(d, nil)
	want := map[string]*Scope{
		"ghapp-write-payments": {Objects: []ObjectRef{{Value: "Strazahq/pay-api", ID: "101"}, {Value: "Strazahq/site", ID: "103"}, {Value: "Strazahq/gone"}}},
		"ghapp-new":            {Objects: []ObjectRef{{Value: "Strazahq/pay-api", ID: "201"}}},
	}
	for role, scope := range want {
		if got := after.Access[role].Covers; !reflect.DeepEqual(got, scope) {
			t.Errorf("%s covers %+v, want %+v", role, got, scope)
		}
	}
	if got := w.Access["ghapp-write-payments"].Covers.Objects; len(got) != 1 || got[0].ID != "101" {
		t.Errorf("the overlay changed live state: %+v", got)
	}
}

// TestCheckNeverKeepsAnUnreadableRow pins that a live row whose covers line
// cannot be read is not kept by a put that restates it with no covers
// line, so the put is asked for one.
func TestCheckNeverKeepsAnUnreadableRow(t *testing.T) {
	t.Parallel()
	w := coversWorld()
	acc := w.Access["ghapp-legacy"]
	acc.Fault = "Straza leaves the access row b-legacy out, because its covers line cannot be read."
	w.Access["ghapp-legacy"] = acc
	v := Check(w, stamped(w, coversRole("ghapp-legacy", "ghapp", "[get_me]", "")), CheckInput{Now: checkNow, RefusalsOnly: true})
	want := []string{"access.covers-missing Role/ghapp-legacy | ghapp-legacy binds tools that work on repos. Say which repos it covers: every repo, or these repos by name."}
	if got := coversFindings(v); !reflect.DeepEqual(got, want) {
		t.Errorf("findings\n got %q\nwant %q", got, want)
	}
}
