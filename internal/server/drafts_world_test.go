package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestDraftsWordsMatchTheStore pins the words the drafts rules compare with
// to the words the store, the manager and the admin API write, because the
// rules cannot import them.
func TestDraftsWordsMatchTheStore(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, rules, live string }{
		{"the business kind", drafts.RoleKindBusiness, store.RoleKindBusiness},
		{"the application kind", drafts.RoleKindApplication, store.RoleKindApplication},
		{"the approver kind", drafts.RoleKindApprover, store.RoleKindApprover},
		{"the straza kind", drafts.RoleKindStraza, RoleKindStraza},
		{"the control plane", drafts.PlaneControl, store.RolePlaneControl},
		{"the root role", drafts.AdminRole, AdminRole},
		{"the global MCP admin role", drafts.MCPAdminRole, MCPAdminRole},
		{"the minted role prefix", drafts.AppAdminRolePrefix, store.AppAdminRolePrefix},
		{"the credential kind none", drafts.CredentialNone, manager.CredentialNone},
		{"the credential kind oauth", drafts.CredentialOAuth, manager.CredentialOAuth},
		{"the credential kind token", drafts.CredentialToken, manager.CredentialToken},
		{"the shared agents value", drafts.AgentsShared, manager.AgentsShared},
	}
	for _, tc := range cases {
		if tc.rules != tc.live {
			t.Errorf("%s: the rules say %q, live state says %q", tc.name, tc.rules, tc.live)
		}
	}
}

// TestRefusalStatus pins the HTTP status of every refusal kind.
func TestRefusalStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind drafts.RefusalKind
		want int
	}{
		{drafts.RefusalInvalid, http.StatusBadRequest},
		{drafts.RefusalMissing, http.StatusNotFound},
		{drafts.RefusalForbidden, http.StatusForbidden},
		{drafts.RefusalConflict, http.StatusConflict},
		{drafts.RefusalExists, http.StatusConflict},
		{drafts.RefusalUnread, http.StatusInternalServerError},
		{drafts.RefusalKind(0), http.StatusBadRequest},
	}
	for _, tc := range cases {
		if got := refusalStatus(tc.kind); got != tc.want {
			t.Errorf("status of kind %d = %d, want %d", tc.kind, got, tc.want)
		}
	}
}

// TestReadWorld pins the full read of live state: servers with their admin
// role, prefix and manifest facts, owned roles by their server's name, an
// owned role and a row whose server is gone, the edges, the access rows,
// the direct holders in force with agents and service accounts marked and
// their typology, accountable sponsor and devices, a holder count for every
// role equal to the store's own count, the knowledge packs of a role, the
// providers of the config, the local tool default, the push lane as the
// approval service reads it, and docker on this host.
func TestReadWorld(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t, func(c *config.Config) {
		c.OAuth.Providers = map[string]config.OAuthProvider{
			"keycloak": {ClientID: "straza", ClientSecret: "s", AuthURL: "http://kc/auth", TokenURL: "http://kc/token",
				ClientCredentials: &config.ClientCredentials{AssertionAudience: "http://kc/realm"}},
			"plain": {ClientID: "straza", ClientSecret: "s", AuthURL: "http://p/auth", TokenURL: "http://p/token"},
		}
	})
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	remote, err := app.store.Apps().Create(ctx, store.App{Name: "w-remote", RuntimeKind: "remote", Manifest: `{"apiVersion":"straza.dev/v1beta1","kind":"App",` +
		`"metadata":{"name":"w-remote"},"server":{"name":"w","version":"1"},"straza":{"runtime":{"kind":"remote","remote":{"url":"http://127.0.0.1:1/mcp"}},` +
		`"credential":{"kind":"token","agents":"shared","inject":{"as":"header","name":"Authorization"}}}}`})
	must(err)
	gone, err := app.store.Apps().Create(ctx, store.App{Name: "w-gone", RuntimeKind: "remote"})
	must(err)
	readers, _, err := app.store.Roles().CreateOwned(ctx, store.Role{Name: "w-remote-readers", OwnerAppID: remote.ID}, `["echo"]`)
	must(err)
	_, _, err = app.store.Roles().CreateOwned(ctx, store.Role{Name: "w-gone-readers", OwnerAppID: gone.ID}, `["echo"]`)
	must(err)
	must(app.store.Apps().SoftDelete(ctx, gone.ID))
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "w-dev", Kind: store.RoleKindApplication})
	must(err)
	_, err = app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: dev.ID, AppID: remote.ID, ToolMatcher: `["*"]`})
	must(err)
	team, err := app.store.Roles().Create(ctx, store.Role{Name: "w-team"})
	must(err)
	must(app.store.Roles().AddImplication(ctx, team.ID, dev.ID))
	assign := func(name, userType, sponsor, roleID string, until *time.Time) store.User {
		u, err := app.store.Users().Create(ctx, store.User{Username: name, Email: name + "@x.io", UserType: userType, Sponsor: sponsor,
			AgencyMode: map[string]string{store.UserTypeAgent: "autonomous"}[userType]})
		must(err)
		_, err = app.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: roleID, ValidTo: until})
		must(err)
		return u
	}
	ended := time.Now().Add(-time.Hour)
	ann := assign("w-ann", store.UserTypeHuman, "", team.ID, nil)
	assign("w-bot", store.UserTypeAgent, "w-ann", dev.ID, nil)
	assign("w-svc", store.UserTypeService, "w-bot", dev.ID, nil)
	assign("w-cal", store.UserTypeHuman, "", readers.ID, &ended)
	_, err = app.store.Approvers().InsertDevice(ctx, store.ApproverDevice{UserID: ann.ID, Name: "phone", Platform: "android", PublicKey: "key"})
	must(err)
	pack, err := app.store.Packs().Create(ctx, store.KnowledgePack{Name: "w-pack", Version: "1", Content: "use gofmt", Checksum: "c"})
	must(err)
	must(app.store.Packs().Bind(ctx, team.ID, pack.ID))

	w, err := app.readWorld(ctx)
	if err != nil {
		t.Fatalf("readWorld: %v", err)
	}
	adminRole, err := app.store.Roles().GetByID(ctx, remote.AdminRoleID)
	must(err)
	if got := w.Apps["w-remote"]; got.ID != remote.ID || got.AdminRole != adminRole.Name || got.RolePrefix != "w-remote-" ||
		got.Credential != drafts.CredentialToken || got.Agents != drafts.AgentsShared {
		t.Errorf("w-remote = %+v, want its id, admin role %s, prefix w-remote- and credential token shared", got, adminRole.Name)
	}
	if got := w.Apps["w-remote"]; got.Runtime != manager.RuntimeRemote || got.URL != "http://127.0.0.1:1/mcp" || got.Auth != manager.AuthInject ||
		got.InjectAs != manager.InjectHeader || !reflect.DeepEqual(got.Exposure, []string{"*"}) {
		t.Errorf("w-remote = %+v, want the remote runtime at its address, injected as a header, exposing every tool", got)
	}
	if _, ok := w.Apps["w-gone"]; ok {
		t.Error("a removed server is in the World")
	}
	roleCases := []struct {
		name, owner string
		owned       bool
	}{
		{"w-remote-readers", "w-remote", true},
		{"w-gone-readers", "", true},
		{"w-dev", "", false},
	}
	for _, tc := range roleCases {
		if got := w.Roles[tc.name]; got.Owner != tc.owner || got.Owned != tc.owned {
			t.Errorf("role %s owner %q owned %v, want %q %v", tc.name, got.Owner, got.Owned, tc.owner, tc.owned)
		}
	}
	accessCases := []struct{ role, server, tools string }{
		{"w-remote-readers", "w-remote", "[echo]"},
		{"w-gone-readers", "", "[echo]"},
		{"w-dev", "w-remote", "[*]"},
	}
	for _, tc := range accessCases {
		if got := w.Access[tc.role]; got.ID == "" || got.Server != tc.server || fmt.Sprint(got.Tools) != tc.tools {
			t.Errorf("access row of %s = %+v, want server %q and tools %s", tc.role, got, tc.server, tc.tools)
		}
	}
	if !reflect.DeepEqual(w.Implies["w-team"], []string{"w-dev"}) {
		t.Errorf("w-team implies %v, want [w-dev]", w.Implies["w-team"])
	}
	holderCases := []struct {
		role string
		want []drafts.Holder
	}{
		{"w-team", []drafts.Holder{{Username: "w-ann", UserType: store.UserTypeHuman, Devices: 1}}},
		{"w-dev", []drafts.Holder{
			{Username: "w-bot", Agent: true, UserType: store.UserTypeAgent, AgencyMode: "autonomous", Sponsor: "w-ann"},
			{Username: "w-svc", Agent: true, UserType: store.UserTypeService},
		}},
		{"w-remote-readers", nil},
	}
	for _, tc := range holderCases {
		if got := w.Holders[tc.role]; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("direct holders of %s = %+v, want %+v", tc.role, got, tc.want)
		}
	}
	for name, ro := range w.Roles {
		want, err := app.store.Roles().HolderCount(ctx, ro.ID)
		must(err)
		if got, ok := w.HolderCounts[name]; !ok || got != want {
			t.Errorf("holder count of %s = %d (counted %v), the store counts %d", name, got, ok, want)
		}
	}
	if w.HolderCounts["w-dev"] != 3 || w.HolderCounts["w-remote-readers"] != 1 {
		t.Errorf("holder counts = %v, want w-dev 3 with w-ann through w-team, and w-remote-readers 1 from an ended window", w.HolderCounts)
	}
	wantProviders := map[string]drafts.Provider{
		"keycloak": {Name: "keycloak", ClientCredentials: true},
		"plain":    {Name: "plain"},
	}
	if !reflect.DeepEqual(w.Providers, wantProviders) {
		t.Errorf("providers = %+v, want %+v", w.Providers, wantProviders)
	}
	if got := w.Roles["w-team"].Packs; !reflect.DeepEqual(got, []string{"w-pack"}) {
		t.Errorf("packs of w-team = %v, want [w-pack]", got)
	}
	if w.LocalToolDefault != config.EffectAllow || w.DockerOnPath != manager.DockerOnPath() {
		t.Errorf("local default %q, docker %v, want allow and this host's docker", w.LocalToolDefault, w.DockerOnPath)
	}
	if w.Push || w.Slack || channelConfiguredBy(t, app, policy.NotifyPush) || channelConfiguredBy(t, app, policy.NotifySlack) {
		t.Errorf("push %v, Slack %v, want neither, as the approval service runs neither", w.Push, w.Slack)
	}
}

// channelConfiguredBy answers whether the approval service of app runs the
// channel name, as its channel status says.
func channelConfiguredBy(t *testing.T, app *App, name string) bool {
	t.Helper()
	statuses, err := app.approval.ChannelStatuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statuses {
		if s.Name == name {
			return s.Configured
		}
	}
	t.Fatalf("the approval service lists no channel %s", name)
	return false
}

// TestReadWorldPoliciesAreThePublishedText pins that the World's policy sets
// are the active snapshot's: a saved but unpublished edit of a live set is
// not live, and a set the store marks active that no snapshot carries is not
// live either.
func TestReadWorldPoliciesAreThePublishedText(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	set := func(name, reason string) string {
		return `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: ` + name + `
spec:
  priority: 120
  match:
    roles: [dev]
  rules:
    - id: no-rm
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *"]
      effect: deny
      reason: "Straza: ` + reason + `"
`
	}
	published, saved := set("w-probe", "published"), set("w-probe", "saved")
	ps, err := app.store.Policies().Create(ctx, store.PolicySet{Name: "w-probe", Priority: 120, YAMLSource: published, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	id := activateTexts(t, app, map[string]string{"w-probe": published})
	ps.YAMLSource = saved
	if _, err := app.store.Policies().Update(ctx, ps); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{Name: "w-stray", Priority: 110, YAMLSource: set("w-stray", "stray"), Status: "active"}); err != nil {
		t.Fatal(err)
	}

	w, err := app.readWorld(ctx)
	if err != nil {
		t.Fatalf("readWorld: %v", err)
	}
	if w.SnapshotID != id {
		t.Errorf("snapshot = %s, want the one the publish activated, %s", w.SnapshotID, id)
	}
	if got := w.Policies["w-probe"]; got.Name != "w-probe" || got.Text != published {
		t.Errorf("w-probe = %+v, want the published text", got)
	}
	if _, ok := w.Policies["w-stray"]; ok {
		t.Error("a set no snapshot carries is in the World")
	}
}

// dwReads counts the role and server reads of the direct routes and fails
// the reads a case names.
type dwReads struct {
	mu   sync.Mutex
	n    map[string]int
	fail map[string]bool
}

// reset starts a case that fails the reads named in fail.
func (d *dwReads) reset(fail ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n, d.fail = map[string]int{}, map[string]bool{}
	for _, f := range fail {
		d.fail[f] = true
	}
}

// read counts one read of name and answers the failure a case injected.
func (d *dwReads) read(name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n[name]++
	if d.fail[name] {
		return errors.New("injected store failure")
	}
	return nil
}

// count answers how often name was read since the last reset.
func (d *dwReads) count(name string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n[name]
}

// dwStore is a store whose role lists, holder counts and server lists
// dwReads counts and fails.
type dwStore struct {
	store.Store
	reads *dwReads
}

func (s dwStore) Roles() store.RoleRepo { return dwRoles{s.Store.Roles(), s.reads} }
func (s dwStore) Apps() store.AppRepo   { return dwApps{s.Store.Apps(), s.reads} }

type dwRoles struct {
	store.RoleRepo
	reads *dwReads
}

func (r dwRoles) List(ctx context.Context) ([]store.Role, error) {
	if err := r.reads.read("Roles.List"); err != nil {
		return nil, err
	}
	return r.RoleRepo.List(ctx)
}

func (r dwRoles) HolderCount(ctx context.Context, id string) (int, error) {
	if err := r.reads.read("Roles.HolderCount"); err != nil {
		return 0, err
	}
	return r.RoleRepo.HolderCount(ctx, id)
}

type dwApps struct {
	store.AppRepo
	reads *dwReads
}

func (a dwApps) List(ctx context.Context) ([]store.App, error) {
	if err := a.reads.read("Apps.List"); err != nil {
		return nil, err
	}
	return a.AppRepo.List(ctx)
}

// TestDirectRoutesReadWhatTheRuleNeeds pins that a direct route reads only
// what the rule that answers needs, in the order the rules run, so a failed
// read the rule never needed cannot turn a refusal or a success into a 500.
// A route that publishes a one-item draft reads the World once instead,
// holder counts included, and a failed read of it answers the route's own
// 500 sentence.
// It also pins that the implication route reads each id as an id: an id no
// role has passes the role rules, and two such ids close a cycle only when
// they are one id.
func TestDirectRoutesReadWhatTheRuleNeeds(t *testing.T) {
	t.Parallel()
	reads := &dwReads{}
	reads.reset()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.store = dwStore{a.store, reads} }})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", root, "application/yaml", []byte(echoManifest(up.URL)), nil); code != http.StatusCreated {
		t.Fatalf("install echoapp = %d", code)
	}
	mkHuman(t, app, "erin", "mcp-admin-echoapp")
	rig := &ownedRig{app: app, base: base, root: root, erin: loginDeviceFlow(t, base, "erin", "hunter2!")}
	for _, body := range []map[string]any{
		{"name": "racer", "kind": "application"}, {"name": "eng", "kind": "business"}, {"name": "sec-approvers", "kind": "approver"},
	} {
		if code, msg := rig.call(t, "POST", "/v1/admin/roles", root, body); code != http.StatusCreated {
			t.Fatalf("create %v = %d %q", body, code, msg)
		}
	}
	if code, msg := rig.call(t, "POST", "/v1/admin/roles", rig.erin, map[string]any{"name": "echoapp-readers", "server": "echoapp", "tools": []string{"echo"}}); code != http.StatusCreated {
		t.Fatalf("erin creates echoapp-readers = %d %q", code, msg)
	}
	mkHuman(t, app, "uma", "echoapp-readers")
	// The global role's row is a store fixture: only a role a server owns
	// gains a new row through the API.
	if _, err := app.store.ToolBindings().Create(context.Background(), store.ToolBinding{RoleID: mustRole(t, app, "racer").ID,
		AppID: mustApp(t, app, "echoapp").ID, ToolMatcher: `["echo"]`}); err != nil {
		t.Fatal(err)
	}
	racerRow, readersRow := rig.bindingOf(t, "racer", "echoapp"), rig.bindingOf(t, "echoapp-readers", "echoapp")
	id := func(name string) string {
		t.Helper()
		ro, err := app.store.Roles().GetByName(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		return ro.ID
	}
	dev, reader, readers, approvers := id("dev"), id("reader"), id("echoapp-readers"), id("sec-approvers")
	set := func(name, match, pool string) string {
		return `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: ` + name + ` }
spec:
  priority: 100
  match: { ` + match + ` }
  rules:
    - id: held
      tools: [shell.exec]
      command: { allowPatterns: ["echo *"] }
      effect: allow
      mode: approve
      approve:
        roles: [` + pool + `]
      reason: "Straza: held"
`
	}
	plain := `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: dw-plain }
spec:
  priority: 90
  match: { users: [kim] }
  rules:
    - id: no-rm
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "Straza: blocked"
`
	pooled := set("dw-pooled", "roles: [racer]", "sec-approvers")
	if _, err := app.store.Policies().Create(context.Background(), store.PolicySet{Name: "dw-pooled", Priority: 100, YAMLSource: pooled, Status: "draft"}); err != nil {
		t.Fatal(err)
	}
	imply := func(to string) map[string]any { return map[string]any{"implies_role_id": to} }
	const failed = "add implication failed: "
	cycle := "reader would imply dev, and dev implies reader, so the two would compose each other in a circle. Drop this implication, or the path that leads back from dev to reader"
	itself := "the role would imply itself, and a role never composes itself. Drop the implication"
	approver := "sec-approvers is an approver role, which stands alone: no role composes it and it composes no role, because who may decide is its direct member list, certified as it stands. Drop the implication, and assign sec-approvers directly to each person who decides instead"
	frozen := "the role echoapp-readers has 1 holder. Its tools change only by the global admin or by a new role"
	noCredential := "the MCP server echoapp declares no credential (credential.kind none), so a secret would never be used. Change the manifest's credential block first."
	cases := []struct {
		name              string
		fail              []string
		method, path, tok string
		body              any
		want              int
		sentence          string
		prefix            bool
		reads             map[string]int
	}{
		{"a role's id implying another role's name", nil, "POST", "/v1/admin/roles/" + reader + "/implications", root, imply("dev"), http.StatusBadRequest, failed, true, map[string]int{"Apps.List": 1}},
		{"a role's id implying an approver role's name", nil, "POST", "/v1/admin/roles/" + dev + "/implications", root, imply("sec-approvers"), http.StatusBadRequest, failed, true, nil},
		{"a role's name implying a role's id", nil, "POST", "/v1/admin/roles/reader/implications", root, imply(dev), http.StatusBadRequest, failed, true, nil},
		{"an unknown id implying itself", nil, "POST", "/v1/admin/roles/ghost/implications", root, imply("ghost"), http.StatusConflict, itself, false, nil},
		{"two unknown ids", nil, "POST", "/v1/admin/roles/ghost-a/implications", root, imply("ghost-b"), http.StatusBadRequest, failed, true, nil},
		{"an owned role implying an unknown id", nil, "POST", "/v1/admin/roles/" + readers + "/implications", root, imply("ghost"), http.StatusBadRequest, ownedRoleImpliesErr, false, nil},
		{"an unknown id implying an approver role", nil, "POST", "/v1/admin/roles/ghost/implications", root, imply(approvers), http.StatusBadRequest, approver, false, nil},
		{"an edge that closes a cycle", nil, "POST", "/v1/admin/roles/" + reader + "/implications", root, imply(dev), http.StatusConflict, cycle, false, map[string]int{"Roles.List": 1, "Apps.List": 1}},
		{"validate a set that names no role", nil, "POST", "/v1/admin/policies/validate", root, plain, http.StatusOK, "", false, map[string]int{"Roles.List": 0, "Apps.List": 0}},
		{"validate a set that names no role while the role list fails", []string{"Roles.List"}, "POST", "/v1/admin/policies/validate", root, plain, http.StatusOK, "", false, nil},
		{"validate a set with a pool and a selector", nil, "POST", "/v1/admin/policies/validate", root, pooled, http.StatusOK, "", false, map[string]int{"Roles.List": 1, "Apps.List": 0}},
		{"validate a refused pool", nil, "POST", "/v1/admin/policies/validate", root, set("dw-refused", "roles: [racer]", "dev"), http.StatusBadRequest,
			`rule "held": approve.roles names "dev" (application role): only approver roles or straza-admin may decide. Create one (strazactl roles create <name> --kind approver), assign it in your identity manager, then name it here`, false, map[string]int{"Roles.List": 1}},
		{"validate a set with a pool while the role list fails", []string{"Roles.List"}, "POST", "/v1/admin/policies/validate", root, pooled, http.StatusInternalServerError, "role lookup failed", false, nil},
		{"activate a set with a pool and a selector", nil, "POST", "/v1/admin/policies/dw-pooled/activate", root, map[string]any{"status": "active"}, http.StatusOK, "", false, map[string]int{"Roles.List": 2, "Apps.List": 1}},
		{"a server admin binds a business role while the holder count fails", []string{"Roles.HolderCount"}, "POST", "/v1/admin/apps/echoapp/bindings", rig.erin, map[string]any{"role": "eng", "tools": []string{"echo"}}, http.StatusBadRequest,
			"business role: it composes application roles and reaches tools through them. Give an application role access instead.", false, map[string]int{"Roles.HolderCount": 0}},
		{"a server admin unbinds a global role while the holder count fails", []string{"Roles.HolderCount"}, "DELETE", "/v1/admin/bindings/" + racerRow, rig.erin, nil, http.StatusConflict,
			"racer is not a role of the server echoapp. A server admin gives access only to the roles their server owns", false, map[string]int{"Roles.HolderCount": 0}},
		{"a server admin's every-tool matcher while the holder count fails", []string{"Roles.HolderCount"}, "POST", "/v1/admin/apps/echoapp/bindings", rig.erin, map[string]any{"role": "echoapp-readers", "tools": []string{"*"}}, http.StatusBadRequest,
			ownedRoleToolsErr, false, map[string]int{"Roles.HolderCount": 0}},
		{"a server admin rebinds a held role", nil, "POST", "/v1/admin/apps/echoapp/bindings", rig.erin, map[string]any{"role": "echoapp-readers", "tools": []string{"echo"}}, http.StatusConflict, frozen, false, map[string]int{"Roles.HolderCount": 0}},
		{"a server admin unbinds a held role", nil, "DELETE", "/v1/admin/bindings/" + readersRow, rig.erin, nil, http.StatusConflict, frozen, false, map[string]int{"Roles.HolderCount": 0}},
		{"a server admin rebinds a held role while the World read fails", []string{"Apps.List"}, "POST", "/v1/admin/apps/echoapp/bindings", rig.erin, map[string]any{"role": "echoapp-readers", "tools": []string{"echo"}}, http.StatusInternalServerError, "the access row could not be created", false, nil},
		{"the server's own secret while the role list fails", []string{"Roles.List"}, "POST", "/v1/admin/apps/echoapp/secrets", root, map[string]any{"value": "v"}, http.StatusBadRequest, noCredential, false, map[string]int{"Roles.List": 0, "Apps.List": 0}},
		{"a role's secret the manifest refuses while the role list fails", []string{"Roles.List"}, "POST", "/v1/admin/apps/echoapp/secrets", root, map[string]any{"value": "v", "role": "dev"}, http.StatusBadRequest, noCredential, false, map[string]int{"Roles.List": 0}},
	}
	for _, tc := range cases {
		reads.reset(tc.fail...)
		var raw []byte
		ct := "application/json"
		if s, ok := tc.body.(string); ok {
			raw, ct = []byte(s), "application/yaml"
		} else if tc.body != nil {
			raw = mustJSON(tc.body)
		}
		code, body, _ := adminBytes(t, tc.method, base+tc.path, tc.tok, ct, raw)
		counted := map[string]int{}
		for name := range tc.reads {
			counted[name] = reads.count(name)
		}
		reads.reset()
		var out struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &out)
		okSentence := out.Error == tc.sentence || (tc.prefix && strings.HasPrefix(out.Error, tc.sentence))
		if code != tc.want || !okSentence {
			t.Errorf("%s: %d %q, want %d %q", tc.name, code, out.Error, tc.want, tc.sentence)
		}
		for name, n := range tc.reads {
			if counted[name] != n {
				t.Errorf("%s: %s read %d times, want %d", tc.name, name, counted[name], n)
			}
		}
	}
}

// orderKey marks the context of the reads a readOrder records.
type orderKey struct{}

// readOrder records, in order, the store reads made with a context that
// orderKey marks, so reads of other goroutines never mix in.
type readOrder struct {
	mu    sync.Mutex
	reads []string
}

func (o *readOrder) add(ctx context.Context, name string) {
	if ctx.Value(orderKey{}) == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reads = append(o.reads, name)
}

// orderStore is a store whose config generation and role list reads a
// readOrder records.
type orderStore struct {
	store.Store
	order *readOrder
}

func (s orderStore) Drafts() store.DraftRepo { return orderDrafts{s.Store.Drafts(), s.order} }
func (s orderStore) Roles() store.RoleRepo   { return orderRoles{s.Store.Roles(), s.order} }

type orderDrafts struct {
	store.DraftRepo
	order *readOrder
}

func (d orderDrafts) Generation(ctx context.Context) (int64, error) {
	d.order.add(ctx, "Drafts.Generation")
	return d.DraftRepo.Generation(ctx)
}

type orderRoles struct {
	store.RoleRepo
	order *readOrder
}

func (r orderRoles) List(ctx context.Context) ([]store.Role, error) {
	r.order.add(ctx, "Roles.List")
	return r.RoleRepo.List(ctx)
}

// TestDraftWorld pins draftWorld over a real store: the config generation
// read before anything else, the fingerprint of every item and every
// implied object equal to the store's own function over the rows read
// straight from the store, the credential facts of the servers the draft
// names and never a value, the facts of the Apps it puts, the proposer as
// its user row reads, and the push lane and Slack as the approval service
// runs them.
// A base equal to the live fingerprint checks fresh and any other stale.
func TestDraftWorld(t *testing.T) {
	t.Parallel()
	order := &readOrder{}
	app, _ := testAppPreRun(t, []func(*App){func(a *App) { a.store = orderStore{a.store, order} }}, func(c *config.Config) {
		c.Approval.Push.AllowedPushHosts = []string{"ntfy.example.com"}
		c.Approval.Channels.Slack = config.SlackChannel{Enabled: true, BotToken: "xoxb-test", SigningSecret: "test-signing", Channel: "C0TEST"}
	})
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	srv, err := app.store.Apps().Create(ctx, store.App{Name: "dw-srv", RuntimeKind: "remote", Manifest: appFactsOf(t, appDoc("dw-srv", "https://srv.example.com/mcp", "", "")).Manifest})
	must(err)
	readers, _, err := app.store.Roles().CreateOwned(ctx, store.Role{Name: "dw-srv-readers", OwnerAppID: srv.ID}, `["get_me"]`)
	must(err)
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dw-dev", Kind: store.RoleKindApplication, Description: "Developers"})
	must(err)
	_, err = app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: dev.ID, AppID: srv.ID, ToolMatcher: `["push_files","get_me"]`})
	must(err)
	team, err := app.store.Roles().Create(ctx, store.Role{Name: "dw-team"})
	must(err)
	must(app.store.Roles().AddImplication(ctx, team.ID, dev.ID))
	_, err = app.store.Policies().Create(ctx, store.PolicySet{Name: "dw-set", Priority: 100, YAMLSource: "saved text", Status: "draft"})
	must(err)
	ann, err := app.store.Users().Create(ctx, store.User{Username: "dw-ann", Email: "dw-ann@x.io", UserType: store.UserTypeHuman})
	must(err)
	bot, err := app.store.Users().Create(ctx, store.User{Username: "dw-bot", Email: "dw-bot@x.io", UserType: store.UserTypeAgent,
		AgencyMode: "supervised", SwarmID: "fleet-1", Sponsor: "dw-ann"})
	must(err)
	sub, err := app.store.Users().Create(ctx, store.User{Username: "dw-sub", Email: "dw-sub@x.io", UserType: store.UserTypeAgent, Sponsor: "dw-bot"})
	must(err)
	for _, c := range []store.Credential{
		{AppID: srv.ID, Scope: store.CredScopeApp, OwnerID: srv.ID, Kind: store.CredStatic, EncPayload: []byte("sealed-app-value")},
		{AppID: srv.ID, Scope: store.CredScopeRole, OwnerID: readers.ID, Kind: store.CredStatic, EncPayload: []byte("sealed-role-value")},
		{AppID: srv.ID, Scope: store.CredScopeUser, OwnerID: ann.ID, Kind: store.CredToken, EncPayload: []byte("sealed-ann-value")},
		{AppID: srv.ID, Scope: store.CredScopeUser, OwnerID: bot.ID, Kind: store.CredToken, EncPayload: []byte("sealed-bot-value")},
	} {
		_, err := app.store.Credentials().Create(ctx, c)
		must(err)
	}
	roleFP := func(name string) drafts.Fingerprint {
		t.Helper()
		ro, err := app.store.Roles().GetByName(ctx, name)
		must(err)
		c := store.RoleConfig{Role: ro}
		if owner, err := app.store.Apps().GetByID(ctx, ro.OwnerAppID); err == nil {
			c.Owner = owner.Name
		}
		rows, err := app.store.ToolBindings().ListByRole(ctx, ro.ID)
		must(err)
		for _, b := range rows {
			if server, err := app.store.Apps().GetByID(ctx, b.AppID); err == nil {
				c.Server = server.Name
			}
			must(json.Unmarshal([]byte(b.ToolMatcher), &c.Tools))
		}
		imps, err := app.store.Roles().ListImplications(ctx)
		must(err)
		for _, imp := range imps {
			if imp.RoleID == ro.ID {
				to, err := app.store.Roles().GetByID(ctx, imp.ImpliesRoleID)
				must(err)
				c.Implies = append(c.Implies, to.Name)
			}
		}
		return drafts.Fingerprint(store.FingerprintRole(c))
	}
	srvRow, err := app.store.Apps().GetByName(ctx, "dw-srv")
	must(err)
	appFP, err := store.FingerprintApp(srvRow.Manifest)
	must(err)
	setRow, err := app.store.Policies().GetByName(ctx, "dw-set")
	must(err)

	d := drafts.Draft{ID: "7", Authors: []drafts.Principal{{UserID: bot.ID, Username: "dw-bot", Agent: true, Via: laneSession}},
		Items: []drafts.Item{
			{Kind: drafts.KindApp, Name: "dw-srv", Op: drafts.OpRemove},
			{Kind: drafts.KindPolicySet, Name: "dw-set", Op: drafts.OpOff, Doc: "new text"},
			{Kind: drafts.KindRole, Name: "dw-team", Op: drafts.OpPut, Doc: roleText(t, "dw-team", "", "Team", "")},
			{Kind: drafts.KindApp, Name: "dw-new", Op: drafts.OpPut, Doc: appDoc("dw-new", "https://new.example.com/mcp", "", "")},
			{Kind: drafts.KindApp, Name: "dw-bad", Op: drafts.OpPut, Doc: "not a manifest"},
		}}
	w, in, err := app.draftWorld(context.WithValue(ctx, orderKey{}, true), d)
	if err != nil {
		t.Fatalf("draftWorld: %v", err)
	}
	if order.reads[0] != "Drafts.Generation" || !slices.Contains(order.reads, "Roles.List") {
		t.Errorf("reads = %v, want the config generation first", order.reads)
	}
	if gen, err := app.store.Drafts().Generation(ctx); err != nil || w.Generation != gen {
		t.Errorf("generation = %d, the store holds %d (%v)", w.Generation, gen, err)
	}
	wantFP := map[string]drafts.Fingerprint{
		"App/dw-srv": drafts.Fingerprint(appFP), "PolicySet/dw-set": drafts.Fingerprint(store.FingerprintPolicySet(setRow)),
		"Role/dw-team": roleFP("dw-team"), "Role/dw-srv-readers": roleFP("dw-srv-readers"), "Role/dw-dev": roleFP("dw-dev"),
	}
	if !reflect.DeepEqual(w.Fingerprints, wantFP) {
		t.Errorf("fingerprints = %v, want %v", w.Fingerprints, wantFP)
	}
	if got := w.Apps["dw-srv"]; !got.SharedSecret || !reflect.DeepEqual(got.RoleSecrets, []string{"dw-srv-readers"}) || got.UserCredentials != 2 {
		t.Errorf("credential facts of dw-srv = %v %v %d, want its own secret, dw-srv-readers and 2 users", got.SharedSecret, got.RoleSecrets, got.UserCredentials)
	}
	if strings.Contains(fmt.Sprintf("%+v %+v", w, in), "sealed-") {
		t.Error("a credential value reached the World")
	}
	next, ok := in.Apps["dw-new"]
	if !ok || next.Runtime != manager.RuntimeRemote || next.URL != "https://new.example.com/mcp" || next.Manifest == "" ||
		next.AdminRole != store.AppAdminRoleName("dw-new") || next.RolePrefix != "dw-new-" {
		t.Errorf("facts of dw-new = %+v, want its remote runtime, manifest, admin role name and prefix", next)
	}
	if _, ok := in.Apps["dw-bad"]; ok || len(in.Apps) != 1 {
		t.Errorf("item facts = %v, want dw-new alone: no facts for a removal or a manifest that does not parse", in.Apps)
	}
	wantProposer := drafts.Holder{Username: "dw-bot", Agent: true, UserType: store.UserTypeAgent, AgencyMode: "supervised", SwarmID: "fleet-1", Sponsor: "dw-ann"}
	if in.Proposer != wantProposer {
		t.Errorf("proposer = %+v, want %+v", in.Proposer, wantProposer)
	}
	if len(in.Changed) != 0 || in.Advisories == nil || in.Now.IsZero() {
		t.Errorf("changed %v, advisories set %v, now %v: want no publish on record, the server's advisories and the time", in.Changed, in.Advisories != nil, in.Now)
	}
	if !w.Push || !channelConfiguredBy(t, app, policy.NotifyPush) || !w.Slack || !channelConfiguredBy(t, app, policy.NotifySlack) {
		t.Errorf("push %v and Slack %v, want both, as the approval service runs an allowlist of push hosts and the Slack channel", w.Push, w.Slack)
	}

	stamped := d
	stamped.Items = slices.Clone(d.Items)
	for i, it := range stamped.Items {
		stamped.Items[i].Base = w.Fingerprints[it.Object()]
	}
	stale := func(v drafts.Verdict) []string {
		var out []string
		for _, f := range v.Refused {
			if f.Code == "draft.stale" {
				out = append(out, f.Object)
			}
		}
		return out
	}
	if got := stale(drafts.Check(w, stamped, in)); len(got) != 0 {
		t.Errorf("a draft stamped from the World reads stale: %v", got)
	}
	stamped.Items[2].Base = "moved"
	if got := stale(drafts.Check(w, stamped, in)); !reflect.DeepEqual(got, []string{"Role/dw-team"}) {
		t.Errorf("stale = %v, want Role/dw-team, whose base moved", got)
	}

	proposers := []struct {
		name   string
		author drafts.Principal
		want   drafts.Holder
	}{
		{"an admin API token named like a user", drafts.Principal{UserID: "t1", Username: "dw-ann", Via: laneAdminAPI}, drafts.Holder{}},
		{"the apps directory", drafts.Principal{UserID: "strazad", Username: "strazad", Via: "file"}, drafts.Holder{}},
		{"a user whose row is gone", drafts.Principal{UserID: "gone", Username: "old-bot", Agent: true, Via: laneSession, SponsorID: ann.ID, SponsorName: "dw-ann"},
			drafts.Holder{Username: "old-bot", Agent: true, Sponsor: "dw-ann"}},
		{"an agent sponsored by an agent", drafts.Principal{UserID: sub.ID, Username: "dw-sub", Agent: true, Via: laneLogin},
			drafts.Holder{Username: "dw-sub", Agent: true, UserType: store.UserTypeAgent}},
		{"a person", drafts.Principal{UserID: ann.ID, Username: "dw-ann", Via: laneLogin}, drafts.Holder{Username: "dw-ann", UserType: store.UserTypeHuman}},
	}
	for _, tc := range proposers {
		got, err := app.proposerOf(ctx, drafts.Draft{Authors: []drafts.Principal{tc.author}})
		if err != nil || got != tc.want {
			t.Errorf("%s: proposer %+v %v, want %+v", tc.name, got, err, tc.want)
		}
	}
}

// changesStore is a store whose draft repository is drafts.
type changesStore struct {
	store.Store
	drafts store.DraftRepo
}

func (s changesStore) Drafts() store.DraftRepo { return s.drafts }

// stubChanges answers LatestChange from rows, records every object asked
// for, and fails every call while fail is set.
type stubChanges struct {
	store.DraftRepo
	rows  map[store.ObjectRef]store.DraftRow
	asked []store.ObjectRef
	fail  error
}

func (s *stubChanges) LatestChange(_ context.Context, ref store.ObjectRef) (store.DraftRow, error) {
	s.asked = append(s.asked, ref)
	if s.fail != nil {
		return store.DraftRow{}, s.fail
	}
	if row, ok := s.rows[ref]; ok {
		return row, nil
	}
	return store.DraftRow{}, store.ErrNotFound
}

// TestLatestChanges pins the last publish that draft.stale names:
// only an item whose base is not its live fingerprint asks for it, a
// publish on record names who published which draft and when, an object
// with none has no entry, and a failed read fails the whole read.
func TestLatestChanges(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 24, 7, 5, 0, 0, time.UTC)
	repo := &stubChanges{rows: map[store.ObjectRef]store.DraftRow{
		{Kind: "Role", Name: "dev"}: {ID: 40, DecidedBy: store.DraftActor{ID: "u-bob", Name: "bob"}, DecidedAt: &at},
	}}
	a := &App{store: changesStore{drafts: repo}}
	w := drafts.World{Fingerprints: map[string]drafts.Fingerprint{"Role/dev": "fp2", "App/github": "fpA"}}
	d := drafts.Draft{Items: []drafts.Item{
		{Kind: drafts.KindRole, Name: "dev", Op: drafts.OpPut, Base: "fp1"},
		{Kind: drafts.KindApp, Name: "github", Op: drafts.OpPut, Base: "fpA"},
		{Kind: drafts.KindPolicySet, Name: "p", Op: drafts.OpPut, Base: "fpP"},
	}}
	got, err := a.latestChanges(context.Background(), w, d)
	want := map[string]drafts.LastChange{"Role/dev": {Draft: 40, Publisher: "bob", At: at}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v %v, want %v", got, err, want)
	}
	if asked := []store.ObjectRef{{Kind: "Role", Name: "dev"}, {Kind: "PolicySet", Name: "p"}}; !reflect.DeepEqual(repo.asked, asked) {
		t.Errorf("asked for %v, want the two stale items %v", repo.asked, asked)
	}
	repo.fail = errors.New("injected store failure")
	if _, err := a.latestChanges(context.Background(), w, d); err == nil || !strings.Contains(err.Error(), "the last publish of Role/dev cannot be read") {
		t.Errorf("a failed read answered %v, want the object named", err)
	}
}

// TestManifestFacts pins the one reading of a manifest for live servers and
// draft items: each runtime's facts, env entries by name and never by
// value, the credential's provider and injection, the exposure with its
// default, and the address of the registry record the server block copies.
func TestManifestFacts(t *testing.T) {
	t.Parallel()
	head := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: mf}\n"
	cases := []struct {
		name, doc string
		want      drafts.App
	}{
		{"a remote server that copies a registry record", head + `server:
  name: example.com/mf
  version: "1.0.0"
  remotes:
    - {type: sse, url: "https://sse.example.com/mcp"}
    - {type: streamable-http, url: "https://registry.example.com/mcp"}
straza:
  runtime: {kind: remote, remote: {url: "https://api.example.com/mcp"}}
  credential:
    kind: oauth
    oauth: {provider: keycloak}
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
`, drafts.App{Name: "mf", Credential: manager.CredentialOAuth, Agents: manager.AgentsOwn, Provider: "keycloak", InjectAs: manager.InjectHeader,
			Runtime: manager.RuntimeRemote, URL: "https://api.example.com/mcp", Auth: manager.AuthInject, Exposure: []string{"*"},
			RegistryURL: "https://registry.example.com/mcp"}},
		{"a command server", head + `server: {name: example.com/mf, version: "1.0.0"}
straza:
  runtime:
    kind: command
    command:
      exec: npx
      args: ["-y", "@acme/mcp"]
      workdir: /srv/mf
      env: [{name: TOKEN_B, value: env-value-b}, {name: A, value: env-value-a}]
  credential:
    kind: static
    inject: {as: env, name: API_KEY}
  exposure: {tools: ["get_*"]}
`, drafts.App{Name: "mf", Credential: manager.CredentialStatic, InjectAs: manager.InjectEnv, Runtime: manager.RuntimeCommand,
			Exec: "npx", Args: []string{"-y", "@acme/mcp"}, Workdir: "/srv/mf", EnvNames: []string{"A", "TOKEN_B"}, Exposure: []string{"get_*"}}},
		{"a container server", head + `server: {name: example.com/mf, version: "1.0.0"}
straza:
  runtime:
    kind: oci
    oci: {image: "ghcr.io/acme/mcp:1", sandbox: none, env: [{name: B, value: env-value-c}]}
`, drafts.App{Name: "mf", Credential: manager.CredentialNone, Runtime: manager.RuntimeOCI, Image: "ghcr.io/acme/mcp:1", Sandbox: "none",
			EnvNames: []string{"B"}, Exposure: []string{"*"}}},
	}
	for _, tc := range cases {
		got := appFactsOf(t, tc.doc)
		got.Manifest = ""
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
		if strings.Contains(fmt.Sprintf("%+v", got), "env-value") {
			t.Errorf("%s: an env value reached the facts: %+v", tc.name, got)
		}
	}
}

// TestPushLaneConfigured pins the push lane the World reports to the
// approval service's own test of its config.
func TestPushLaneConfigured(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		push config.ApprovalPush
		want bool
	}{
		{"nothing configured", config.ApprovalPush{}, false},
		{"FCM on", config.ApprovalPush{FCM: config.FCMPush{Enabled: true}}, true},
		{"an allowlist of push hosts", config.ApprovalPush{AllowedPushHosts: []string{"ntfy.example.com"}}, true},
		{"a WebPush key", config.ApprovalPush{WebPush: config.WebPush{VAPIDKeyFile: "/data/vapid.pem"}}, true},
		{"an APNs key", config.ApprovalPush{APNS: config.APNSPush{KeyFile: "/data/apns.p8"}}, true},
		{"the relay on", config.ApprovalPush{Relay: config.RelayPush{Enabled: true}}, true},
	}
	for _, tc := range cases {
		if got := pushLaneConfigured(tc.push); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestWorldReadRefusal pins the 503 sentence of a failed read of live
// state.
func TestWorldReadRefusal(t *testing.T) {
	t.Parallel()
	got := worldReadRefusal(errors.New("the config generation cannot be read: database is locked"))
	want := "Straza could not read live state to check the draft: the config generation cannot be read: database is locked. " +
		"Nothing was saved or published. Try again, and read the strazad log if it keeps failing."
	if got != want {
		t.Errorf("%q, want %q", got, want)
	}
}
