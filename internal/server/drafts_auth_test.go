package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/tokenscopes"
)

// grantsOf is a scope that holds grants.
func grantsOf(grants ...string) tokenscopes.Scope {
	s := tokenscopes.Scope{Grants: map[string]bool{}}
	for _, g := range grants {
		s.Grants[g] = true
	}
	return s
}

// personCaller is a person with the id id, signed in with a login token,
// whose roles carry grants.
func personCaller(id string, grants ...string) draftCaller {
	return draftCaller{
		p:      adminPrincipal{actor: auditActor{Name: id, ID: id, Via: laneLogin}, roles: []store.Role{}, scope: grantsOf(grants...)},
		person: true, door: drafts.DoorAPI,
		author: drafts.Principal{UserID: id, Username: id, Via: laneLogin, Client: clientLogin},
	}
}

// agentCaller is personCaller for a user who is not a person.
func agentCaller(id string, grants ...string) draftCaller {
	c := personCaller(id, grants...)
	c.person, c.author.Agent = false, true
	return c
}

// rootCaller is a person who holds straza-admin.
func rootCaller(id string) draftCaller {
	c := personCaller(id)
	c.p.root = true
	return c
}

// adminAPICaller is an admin API token with the id id and grants.
func adminAPICaller(id string, grants ...string) draftCaller {
	return draftCaller{
		p:      adminPrincipal{actor: auditActor{Name: id, ID: id, Via: laneAdminAPI}, scope: grantsOf(grants...)},
		door:   drafts.DoorAPI,
		author: drafts.Principal{UserID: id, Username: id, Via: laneAdminAPI, Client: clientAdminAPI},
	}
}

// administering is c holding the admin role of the servers, by id and name.
func administering(c draftCaller, servers map[string]string) draftCaller {
	c.servers = servers
	return c
}

// TestDraftsAccess pins who reaches the drafts routes: root, a drafts
// grant at the verb of the method, and a person whose grants hold apps,
// identity or policy at that verb or who administers a server. An admin API
// token and an agent pass on root or a drafts grant only.
func TestDraftsAccess(t *testing.T) {
	t.Parallel()
	fullToken := adminAPICaller("ci")
	fullToken.p.root, fullToken.p.scope = true, tokenscopes.Scope{Full: true}
	github := map[string]string{"a1": "github"}
	cases := []struct {
		name        string
		c           draftCaller
		read, write bool
	}{
		{"root", rootCaller("kim"), true, true},
		{"an admin API token with the scope full", fullToken, true, true},
		{"an admin API token with drafts:read", adminAPICaller("ci", "drafts:read"), true, false},
		{"an admin API token with drafts:write", adminAPICaller("ci", "drafts:write"), false, true},
		{"an admin API token with apps:write and no drafts grant", adminAPICaller("ci", "apps:read", "apps:write"), false, false},
		{"a person with identity:read", personCaller("ada", "identity:read"), true, false},
		{"a person with policy:write", personCaller("pat", "policy:write"), false, true},
		{"the global MCP admin, whose role carries apps:read and apps:write", personCaller("max", "apps:read", "apps:write"), true, true},
		{"a person who administers a server", administering(personCaller("erin"), github), true, true},
		{"a person with drafts:write alone", personCaller("dan", "drafts:write"), false, true},
		{"a person with no grant and no server", personCaller("nell"), false, false},
		{"an agent with apps:write", agentCaller("bot", "apps:read", "apps:write"), false, false},
		{"an agent that administers a server", administering(agentCaller("bot"), github), false, false},
		{"an agent with drafts:write", agentCaller("bot", "drafts:write"), false, true},
	}
	for _, tc := range cases {
		if got := tc.c.mayUse(http.MethodGet); got != tc.read {
			t.Errorf("%s: GET passes %v, want %v", tc.name, got, tc.read)
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			if got := tc.c.mayUse(method); got != tc.write {
				t.Errorf("%s: %s passes %v, want %v", tc.name, method, got, tc.write)
			}
		}
	}
}

// TestRequireDrafts pins the wrapper of the drafts routes against real
// credentials: who passes, the refusal everyone else reads, and the author
// and door a pass hands on. The door comes from the credential, never the
// request: a console session is console, a strazactl session strazactl,
// and a login, a self-service session and an admin API token api. An agent
// is refused before its standing is read, whatever role it holds, because
// only a person uses the admin API.
func TestRequireDrafts(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Admin.RoleAreas = map[string][]string{"auditors": {"identity:read"}}
	})
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	kimLogin := loginDeviceFlow(t, base, "kim", "hunter2!")
	if _, err := app.store.Roles().Create(ctx, store.Role{Name: "auditors"}); err != nil {
		t.Fatal(err)
	}
	srv, err := app.store.Apps().Create(ctx, store.App{Name: "rd-srv", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	srvAdmin, err := app.store.Roles().GetByID(ctx, srv.AdminRoleID)
	if err != nil {
		t.Fatal(err)
	}
	mkHuman(t, app, "ada", "auditors")
	mkHuman(t, app, "erin", srvAdmin.Name)
	mkHuman(t, app, "nell")
	seedAgent(t, app, "bot", "kim", MCPAdminRole)
	seedAgent(t, app, "rootbot", "kim", AdminRole)
	login := func(name string) string { return loginDeviceFlow(t, base, name, "hunter2!") }
	session := func(harness string) string {
		tok, _ := checkinTokenAs(t, base, harness)
		return tok
	}
	mint := func(name, scope string) string {
		var minted struct {
			Token string `json:"token"`
		}
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", kimLogin, map[string]any{"name": name, "scope": scope}, &minted); code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("mint %s = %d", name, code)
		}
		return minted.Token
	}
	kimAs := func(via, client string, door drafts.Door) func(*testing.T, draftCaller) {
		return func(t *testing.T, c draftCaller) {
			want := drafts.Principal{UserID: kim.ID, Username: "kim", Via: via, Client: client}
			if c.author != want || c.door != door || !c.person {
				t.Errorf("caller = %+v %s person %v, want %+v %s person", c.author, c.door, c.person, want, door)
			}
		}
	}
	cases := []struct {
		name, bearer, method string
		want                 int
		says                 string
		check                func(*testing.T, draftCaller)
	}{
		{"root's login", kimLogin, http.MethodPost, http.StatusNoContent, "", kimAs(laneLogin, clientLogin, drafts.DoorAPI)},
		{"root's console session", session("console"), http.MethodPost, http.StatusNoContent, "", kimAs(laneSession, "console", drafts.DoorConsole)},
		{"root's strazactl session", session("strazactl"), http.MethodPost, http.StatusNoContent, "", kimAs(laneSession, "strazactl", drafts.DoorStrazactl)},
		{"root's self-service session", session("self-service"), http.MethodPost, http.StatusNoContent, "", kimAs(laneSession, "self-service", drafts.DoorAPI)},
		{"root's coding harness session", session("claude-code"), http.MethodGet, http.StatusForbidden, `coding harness "claude-code"`, nil},
		{"a person with identity:read, reading", login("ada"), http.MethodGet, http.StatusNoContent, "", nil},
		{"a person with identity:read, writing", login("ada"), http.MethodPost, http.StatusForbidden, draftsAccessRefusal, nil},
		{"a server admin", login("erin"), http.MethodPost, http.StatusNoContent, "", func(t *testing.T, c draftCaller) {
			if !reflect.DeepEqual(c.servers, map[string]string{srv.ID: "rd-srv"}) {
				t.Errorf("servers = %v, want rd-srv", c.servers)
			}
		}},
		{"a person with no standing", login("nell"), http.MethodGet, http.StatusForbidden, draftsAccessRefusal, nil},
		{"an agent holding the global MCP admin", login("bot"), http.MethodGet, http.StatusForbidden, fmt.Sprintf(nonPersonAdminRefusal, "bot", "an agent"), nil},
		{"an agent holding root", login("rootbot"), http.MethodPost, http.StatusForbidden, fmt.Sprintf(nonPersonAdminRefusal, "rootbot", "an agent"), nil},
		{"an admin API token with apps:write", mint("apps-writer", "apps:write"), http.MethodPost, http.StatusForbidden,
			"This token holds no drafts scope, and writing or checking a draft needs drafts:write. Mint a token with the scopes drafts:read and drafts:write with strazactl api-token create.", nil},
		{"an admin API token with drafts:write, reading", mint("drafts-writer", "drafts:write"), http.MethodGet, http.StatusForbidden,
			"This token holds drafts:write, and reading drafts needs drafts:read. Mint a token with the scopes drafts:read and drafts:write with strazactl api-token create.", nil},
		{"an admin API token with drafts:read, writing", mint("drafts-reader", "drafts:read"), http.MethodPost, http.StatusForbidden,
			"This token holds drafts:read, and writing or checking a draft needs drafts:write. Mint a token with the scopes drafts:read and drafts:write with strazactl api-token create.", nil},
		{"an admin API token with the scope full", mint("root-token", "full"), http.MethodPost, http.StatusNoContent, "", func(t *testing.T, c draftCaller) {
			if c.author.Username != "root-token" || c.author.Via != laneAdminAPI || c.author.Client != clientAdminAPI || c.author.Agent || c.person || c.door != drafts.DoorAPI {
				t.Errorf("caller = %+v %s person %v, want the token on the api door", c.author, c.door, c.person)
			}
		}},
		{"no bearer", "", http.MethodGet, http.StatusUnauthorized, "missing bearer token", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *draftCaller
			guarded := app.requireDrafts(func(w http.ResponseWriter, r *http.Request, c draftCaller) {
				got = &c
				if act, ok := actorFrom(r.Context()); !ok || act.Name == "" {
					t.Error("the handler got no audit actor")
				}
				if st := standingFrom(r.Context()); st.Full || len(st.Apps) > 0 {
					t.Errorf("the handler reads the standing %+v, want none, so a stray read fails closed", st)
				}
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(tc.method, base+"/v1/admin/drafts", nil)
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			rec := httptest.NewRecorder()
			guarded(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if (got != nil) != (tc.want == http.StatusNoContent) {
				t.Fatalf("handler reached = %v for status %d", got != nil, rec.Code)
			}
			if tc.says != "" {
				var out struct {
					Error string `json:"error"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &out)
				if !strings.Contains(out.Error, tc.says) {
					t.Errorf("refusal %q does not say %q", out.Error, tc.says)
				}
			}
			if got != nil && tc.check != nil {
				tc.check(t, *got)
			}
		})
	}
}

// unreadUsers is a store whose reads of a user by id fail for the one user
// id armed holds, once the reads that passes counts have passed.
type unreadUsers struct {
	store.Store
	armed  *atomic.Value
	passes *atomic.Int32
}

func (s unreadUsers) Users() store.UserRepo {
	return unreadUserRepo{s.Store.Users(), s.armed, s.passes}
}

type unreadUserRepo struct {
	store.UserRepo
	armed  *atomic.Value
	passes *atomic.Int32
}

func (r unreadUserRepo) GetByID(ctx context.Context, id string) (store.User, error) {
	if armed, _ := r.armed.Load().(string); armed != "" && armed == id && r.passes.Add(-1) < 0 {
		return store.User{}, errors.New("injected user read failure")
	}
	return r.UserRepo.GetByID(ctx, id)
}

// TestRequireDraftsFailsClosedOnAnUnreadUser pins that a caller whose user
// row cannot be read is refused, because whether it is a person decides
// what it may do, and that the publish route names the publish. The read
// of the session judgment passes and the drafts routes' own read fails, as
// when the store fails between the two.
func TestRequireDraftsFailsClosedOnAnUnreadUser(t *testing.T) {
	t.Parallel()
	armed, passes := &atomic.Value{}, &atomic.Int32{}
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.store = unreadUsers{a.store, armed, passes} }})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	console, _ := checkinTokenAs(t, base, "console")
	armed.Store(kim.ID)
	const says = "Straza could not read your user record, so it cannot tell whether you are a person, and it refuses %s. Try again, and check the strazad log if it keeps failing."
	cases := []struct{ method, path, what string }{
		{http.MethodGet, "/v1/admin/drafts", "the request"},
		{http.MethodPost, "/v1/admin/drafts/41/publish", "the publish"},
	}
	for _, tc := range cases {
		passes.Store(1)
		reached := false
		guarded := app.requireDrafts(func(w http.ResponseWriter, r *http.Request, c draftCaller) { reached = true })
		req := httptest.NewRequest(tc.method, base+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+console)
		rec := httptest.NewRecorder()
		guarded(rec, req)
		var out struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if want := strings.Replace(says, "%s", tc.what, 1); rec.Code != http.StatusForbidden || out.Error != want || reached {
			t.Errorf("%s %s = %d %q reached %v, want 403 %q", tc.method, tc.path, rec.Code, out.Error, reached, want)
		}
	}
}

// TestDraftVisibility pins who lists and reads which draft: root and
// drafts:read every draft, anyone else the drafts they wrote and the drafts
// whose every item is an App, or a role owned by an App, of a server they
// administer. A Role item names its owner from the live document it was
// stamped with, so a document that claims a server opens nothing.
func TestDraftVisibility(t *testing.T) {
	t.Parallel()
	roleDoc := func(server string) string {
		b, err := drafts.RoleDoc{APIVersion: drafts.RoleAPIVersion, Kind: string(drafts.KindRole), Metadata: drafts.RoleDocMeta{Name: "readers"},
			Spec: drafts.RoleDocSpec{Kind: drafts.RoleKindApplication, Server: server}}.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	server := func(name string) store.DraftItemRow {
		return store.DraftItemRow{Kind: "App", Name: name, Op: "put", BaseOp: "put"}
	}
	role := func(op, doc, baseOp, baseDoc string) store.DraftItemRow {
		return store.DraftItemRow{Kind: "Role", Name: "readers", Op: op, Doc: doc, BaseOp: baseOp, BaseDoc: baseDoc}
	}
	set := store.DraftItemRow{Kind: "PolicySet", Name: "p", Op: "put", BaseOp: "remove"}
	erin := administering(personCaller("erin"), map[string]string{"a1": "github"})
	cases := []struct {
		name  string
		c     draftCaller
		wrote bool
		items []store.DraftItemRow
		want  bool
	}{
		{"root reads every draft", rootCaller("kim"), false, []store.DraftItemRow{set}, true},
		{"drafts:read reads every draft", adminAPICaller("ci", "drafts:read"), false, []store.DraftItemRow{set}, true},
		{"an author reads its draft", personCaller("ada", "identity:read"), true, []store.DraftItemRow{set}, true},
		{"a server admin reads a draft of its server", erin, false, []store.DraftItemRow{server("github"), role("put", roleDoc("github"), "put", roleDoc("github"))}, true},
		{"a server admin reads no draft of another server", erin, false, []store.DraftItemRow{server("jira")}, false},
		{"a server admin reads no draft that also changes a policy set", erin, false, []store.DraftItemRow{server("github"), set}, false},
		{"a server admin reads no draft that changes a global role", erin, false, []store.DraftItemRow{role("put", roleDoc(""), "put", roleDoc(""))}, false},
		{"a document that claims the server opens no global role", erin, false, []store.DraftItemRow{role("put", roleDoc("github"), "put", roleDoc(""))}, false},
		{"a new role of the server opens its draft", erin, false, []store.DraftItemRow{role("put", roleDoc("github"), "remove", "")}, true},
		{"the removal of a role of the server opens its draft", erin, false, []store.DraftItemRow{role("remove", "", "put", roleDoc("github"))}, true},
		{"an item not stamped yet opens nothing", erin, false, []store.DraftItemRow{role("put", roleDoc("github"), "", "")}, false},
		{"a draft with no item opens nothing", erin, false, nil, false},
		{"the global MCP admin reads a draft of any server", personCaller("max", "apps:read", "apps:write"), false, []store.DraftItemRow{server("jira")}, true},
		{"identity:read reads no draft it did not write", personCaller("ada", "identity:read"), false, []store.DraftItemRow{role("put", roleDoc(""), "put", roleDoc(""))}, false},
	}
	for _, tc := range cases {
		if got := tc.c.mayRead(tc.wrote, tc.items); got != tc.want {
			t.Errorf("%s: reads %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestDraftCallerWrote pins that an author is the same user or the same
// admin API token, never a token whose id equals a user's.
func TestDraftCallerWrote(t *testing.T) {
	t.Parallel()
	ada := drafts.Principal{UserID: "u1", Username: "ada", Via: laneSession}
	token := drafts.Principal{UserID: "u1", Username: "ci", Via: laneAdminAPI}
	cases := []struct {
		name    string
		c       draftCaller
		authors []drafts.Principal
		want    bool
	}{
		{"the same user on another lane", personCaller("u1"), []drafts.Principal{ada}, true},
		{"another user", personCaller("u2"), []drafts.Principal{ada}, false},
		{"a token whose id is the user's", adminAPICaller("u1"), []drafts.Principal{ada}, false},
		{"the same token", adminAPICaller("u1"), []drafts.Principal{ada, token}, true},
		{"no author", personCaller("u1"), nil, false},
	}
	for _, tc := range cases {
		if got := tc.c.wrote(tc.authors); got != tc.want {
			t.Errorf("%s: wrote %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPublisherRefusal pins step 1 of a publish: only a person
// publishes, an agent not even with root, and only while the user row is
// active and the lock denylist does not block it.
func TestPublisherRefusal(t *testing.T) {
	t.Parallel()
	rootAgent := agentCaller("bot")
	rootAgent.p.root = true
	disabled, locked := rootCaller("dora"), rootCaller("lars")
	disabled.disabled, locked.locked = true, true
	cases := []struct {
		name string
		c    draftCaller
		want string
	}{
		{"a disabled person", disabled, "The user dora is disabled, so it cannot publish a draft. Ask an administrator to enable it again."},
		{"a locked person", locked, "The user lars is locked, so it cannot publish a draft. An administrator lifts the lock with strazactl users unlock lars."},
		{"an admin API token", adminAPICaller("ci", "drafts:write"),
			"An admin API token carries no person and cannot publish a draft. It may create drafts. A person publishes them on the console or with strazactl."},
		{"an agent holding root", rootAgent,
			"This identity is an agent, and an agent cannot publish a draft, even with an administrator role. A person with standing over every object in the draft publishes it on the console or with strazactl."},
		{"a person", personCaller("ada"), ""},
	}
	for _, tc := range cases {
		if got := tc.c.publisherRefusal(); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDraftsRefuseADisabledOrLockedSession pins that a session whose user
// was disabled or locked after it began never reaches a drafts route:
// requireDrafts refuses it with the refresh's words, and an active person's
// session passes with nothing that stops step 1 of a publish.
func TestDraftsRefuseADisabledOrLockedSession(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	session := func(name string) string {
		t.Helper()
		_, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
			"id_token":    loginDeviceFlow(t, base, name, "hunter2!"),
			"harness":     map[string]string{"name": "console", "version": "1"},
			"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
		})
		tok, _ := checkin["session_token"].(string)
		if tok == "" {
			t.Fatalf("no session for %s: %v", name, checkin)
		}
		return tok
	}
	dora, lars := mkHuman(t, app, "dora", AdminRole), mkHuman(t, app, "lars", AdminRole)
	doraSession, larsSession, kimSession := session("dora"), session("lars"), session("kim")
	dora.Status = store.UserDisabled
	if _, err := app.store.Users().Update(ctx, dora); err != nil {
		t.Fatal(err)
	}
	app.denylist.RevokeUser(lars.ID)
	cases := []struct{ name, bearer, says string }{
		{"a person whose user was disabled", doraSession, "user is disabled. Contact your administrator"},
		{"a person whose user was locked", larsSession, revokedIdentityMsg},
		{"an active person", kimSession, ""},
	}
	for _, tc := range cases {
		var got *draftCaller
		guarded := app.requireDrafts(func(w http.ResponseWriter, r *http.Request, c draftCaller) { got = &c })
		req := httptest.NewRequest(http.MethodPost, base+"/v1/admin/drafts/41/publish", nil)
		req.Header.Set("Authorization", "Bearer "+tc.bearer)
		rec := httptest.NewRecorder()
		guarded(rec, req)
		if tc.says != "" {
			var out struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if got != nil || rec.Code != http.StatusForbidden || out.Error != tc.says {
				t.Errorf("%s: requireDrafts answers %d %q, want 403 %q before the handler", tc.name, rec.Code, out.Error, tc.says)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s: requireDrafts refused the session: %d %s", tc.name, rec.Code, rec.Body.String())
			continue
		}
		if msg := got.publisherRefusal(); msg != "" {
			t.Errorf("%s: step 1 answers %q, want nothing", tc.name, msg)
		}
	}
}

// TestReadRefusal pins what create, update and check refuse: an item
// outside what the caller may read today.
// A server and a role a server owns need the scope apps:read or that
// server's admin role, a global role identity:read, and a policy set
// policy:read, so a draft never shows its author another server's tools
// or a document its read routes would not show.
func TestReadRefusal(t *testing.T) {
	t.Parallel()
	w := standingWorld(t)
	role := func(name, server, bind string, tools ...string) drafts.Item {
		return drafts.Item{Kind: drafts.KindRole, Name: name, Op: drafts.OpPut, Doc: roleText(t, name, server, "", bind, tools...)}
	}
	app := func(name string) drafts.Item {
		return drafts.Item{Kind: drafts.KindApp, Name: name, Op: drafts.OpPut, Doc: appDoc(name, "https://"+name+".example.com/mcp", "", "")}
	}
	set := drafts.Item{Kind: drafts.KindPolicySet, Name: "p", Op: drafts.OpPut, Doc: "text"}
	erin := administering(personCaller("erin"), map[string]string{"a1": "github"})
	ida := personCaller("ida", "identity:read", "identity:write")
	maxi := personCaller("max", "apps:read", "apps:write")
	changing := func(c draftCaller) draftCaller {
		c.Change = true
		return c
	}
	const why = ", because a draft shows the live config of the servers it names and what each gives a role. Ask an administrator for that grant, or leave "
	const changeWhy = ", because a change is checked against the live config of the servers it names and what each gives a role. "
	cases := []struct {
		name  string
		c     draftCaller
		items []drafts.Item
		want  string
	}{
		{"root drafts anything", rootCaller("kim"), []drafts.Item{role("probe-reach", "", "jira", "*"), app("jira"), set}, ""},
		{"a server admin drafts a global role that reaches another server", erin, []drafts.Item{role("probe-reach", "", "jira", "*")},
			"Drafting the role probe-reach needs the scope identity:read, because a draft shows the live document of every role it names. Ask an administrator for that grant, or leave the role probe-reach out of the draft."},
		{"a server admin drafts its server and a role it owns", erin, []drafts.Item{app("github"), role("github-readers", "github", "github", "get_me", "list_issues")}, ""},
		{"a server admin drafts another server", erin, []drafts.Item{app("jira")},
			"Drafting the server jira needs the scope apps:read or the role mcp-admin-jira of the server jira" + why + "the server jira out of the draft."},
		{"a server admin drafts a role of another server", erin, []drafts.Item{role("jira-triage", "jira", "jira", "get_me")},
			"Drafting the role jira-triage needs the scope apps:read or the role mcp-admin-jira of the server jira" + why + "the role jira-triage out of the draft."},
		{"a server admin drafts a new server", erin, []drafts.Item{app("gitlab")},
			"Drafting the server gitlab needs the scope apps:read" + why + "the server gitlab out of the draft."},
		{"an identity admin drafts a global role", ida, []drafts.Item{role("dev", "", "github", "get_me")}, ""},
		{"an identity admin drafts a role a server owns", ida, []drafts.Item{role("github-readers", "github", "github", "get_me")},
			"Drafting the role github-readers needs the scope apps:read or the role mcp-admin-github of the server github" + why + "the role github-readers out of the draft."},
		{"the global MCP admin drafts servers and the roles they own", maxi, []drafts.Item{app("jira"), role("github-readers", "github", "github", "get_me")}, ""},
		{"a policy writer without policy:read drafts a set", personCaller("pat", "policy:write"), []drafts.Item{set},
			"Drafting the policy set p needs the scope policy:read, because a draft shows the published text of the sets it names and what they do for each role. Ask an administrator for that grant, or leave the policy set p out of the draft."},
		{"a policy reader drafts a set", personCaller("pia", "policy:read"), []drafts.Item{set}, ""},
		{"an admin API token with drafts:write alone drafts a server", adminAPICaller("ci", "drafts:write"), []drafts.Item{app("github")},
			"Drafting the server github needs the scope apps:read, because a draft shows the live config of the servers it names and what each gives a role. " +
				"Mint a token that also holds that scope with strazactl api-token create, or leave the server github out of the draft."},
		{"a server admin drafts a live global role whose document claims its server", erin, []drafts.Item{role("dev", "github", "github", "get_me")},
			"Drafting the role dev needs the scope identity:read, because a draft shows the live document of every role it names. Ask an administrator for that grant, or leave the role dev out of the draft."},
		{"every item the caller may not read answers in one sentence", erin,
			[]drafts.Item{app("github"), app("jira"), role("jira-triage", "jira", "jira", "get_me"), role("probe-reach", "", "jira", "*"), role("ops", "", "github", "get_me"), set},
			"Drafting the server jira and the role jira-triage needs the scope apps:read or the role mcp-admin-jira of the server jira, " +
				"drafting the role probe-reach and the role ops needs the scope identity:read, and drafting the policy set p needs the scope policy:read, " +
				"because a draft shows the live config of every object it names. Ask an administrator for those grants, or leave those objects out of the draft."},
		{"an admin API token names every scope it lacks", adminAPICaller("ci", "drafts:write"), []drafts.Item{app("github"), role("ops", "", "github", "get_me")},
			"Drafting the server github needs the scope apps:read, and drafting the role ops needs the scope identity:read, " +
				"because a draft shows the live config of every object it names. Mint a token that also holds those scopes with strazactl api-token create, or leave those objects out of the draft."},
		{"a server admin changes another server directly", changing(erin), []drafts.Item{app("jira")},
			"Changing the server jira needs the scope apps:read or the role mcp-admin-jira of the server jira" + changeWhy + "Ask an administrator for that grant, then make the change again."},
		{"an admin API token without read standing changes a server directly", changing(adminAPICaller("ci", "apps:write")), []drafts.Item{app("github")},
			"Changing the server github needs the scope apps:read" + changeWhy + "Mint a token that also holds that scope with strazactl api-token create, then make the change again."},
		{"a server admin changes a global role directly", changing(erin), []drafts.Item{role("dev", "", "github", "get_me")},
			"Changing the role dev needs the scope identity:read, because a change is checked against the live document of every role it names. Ask an administrator for that grant, then make the change again."},
		{"a direct change names every object in one sentence", changing(adminAPICaller("ci", "drafts:write")), []drafts.Item{app("github"), role("ops", "", "github", "get_me")},
			"Changing the server github needs the scope apps:read, and changing the role ops needs the scope identity:read, " +
				"because a change is checked against the live config of every object it names. Mint a token that also holds those scopes with strazactl api-token create, then make the change again."},
	}
	for _, tc := range cases {
		if got := tc.c.readRefusal(drafts.Draft{ID: "41", Items: tc.items}, w); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
