package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// directRequest is a request to a direct route as kim, the root user of
// the drafts fixture, with the actor and the full standing the route's
// wrapper puts in the context.
func directRequest(f *draftsFixture) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/roles", nil)
	ctx := withActor(context.Background(), auditActor{Name: "kim", ID: f.kim.ID, Via: laneLogin})
	return r.WithContext(withStanding(ctx, adminStanding{Full: true}))
}

// itemRoute is a direct route that publishes item, recording the author
// each prepare read.
func itemRoute(item drafts.Item, authors *[]drafts.Principal) directRoute {
	return directRoute{
		prepare: func(_ drafts.World, author drafts.Principal) (directChange, bool) {
			*authors = append(*authors, author)
			return directChange{Item: item}, true
		},
		bodyStatus: http.StatusBadRequest,
		failed:     func(error) string { return "create failed" },
	}
}

// TestPublishOneMintsAPublishedDraft pins the frame of a direct route's
// publish: the
// route's one item lands as a published draft of the api door by the
// caller, every record names that draft and none is a draft.* record, the
// generation moves by one, and the same change again is a no-op that
// writes nothing.
func TestPublishOneMintsAPublishedDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	var authors []drafts.Principal
	item := drafts.Item{Kind: drafts.KindRole, Name: "helpers", Op: drafts.OpPut, Doc: draftRole("helpers", "Helpers.")}
	before, err := f.app.store.Drafts().Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	res, ok := f.app.publishOne(rec, directRequest(f), itemRoute(item, &authors))
	if !ok || res.Noop {
		t.Fatalf("publishOne = %v noop %v, answered %d %s", ok, res.Noop, rec.Code, rec.Body)
	}
	if len(authors) != 1 || authors[0].UserID != f.kim.ID || authors[0].Agent {
		t.Errorf("prepare read the authors %+v, want kim once as a person", authors)
	}
	id := strconv.FormatInt(res.Outcome.DraftID, 10)
	row, items := f.stored(t, id)
	if row.State != "published" || row.Door != "api" || row.DecidedBy.ID != f.kim.ID || row.Proposer.ID != f.kim.ID {
		t.Errorf("the draft is %s through %s, by %q proposed by %q, want published through api by kim", row.State, row.Door, row.DecidedBy.Name, row.Proposer.Name)
	}
	if len(items) != 1 || items[0].Kind != "Role" || items[0].Name != "helpers" || items[0].Op != "put" {
		t.Errorf("the draft holds %+v, want the one Role put of helpers", items)
	}
	if res.Item.Ref.Name != "helpers" || !res.Item.Created || res.Snapshot != res.Outcome.Snapshot {
		t.Errorf("the result names %+v with snapshot %q, want the created helpers", res.Item, res.Snapshot)
	}
	if after, _ := f.app.store.Drafts().Generation(ctx); after != before+1 {
		t.Errorf("the generation is %d, want %d", after, before+1)
	}
	events := outboxOf(t, f.app, id)
	if len(events) == 0 {
		t.Fatal("no record names the draft")
	}
	for _, ev := range events {
		if action, _ := ev.data["action"].(string); strings.HasPrefix(action, "draft.") {
			t.Errorf("a direct publish wrote %s", ev.raw)
		}
	}

	rec = httptest.NewRecorder()
	again, ok := f.app.publishOne(rec, directRequest(f), itemRoute(item, &authors))
	if !ok || !again.Noop || again.Outcome.DraftID != 0 || again.Snapshot != again.World.SnapshotID {
		t.Errorf("the same change again = %v %+v, want a no-op on the live snapshot", ok, again)
	}
	if after, _ := f.app.store.Drafts().Generation(ctx); after != before+1 {
		t.Errorf("the no-op moved the generation to %d", after)
	}
}

// directReq is one request to a direct route: method, path, content type
// and body.
type directReq struct {
	method, path, ctype string
	body                []byte
}

// jsonReq is a direct route request with a JSON body, or none for nil.
func jsonReq(t *testing.T, method, path string, body any) directReq {
	t.Helper()
	if body == nil {
		return directReq{method: method, path: path}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return directReq{method: method, path: path, ctype: "application/json", body: raw}
}

// send fires req as bearer and answers the status and the body.
func (f *draftsFixture) send(t *testing.T, bearer string, req directReq) (int, []byte) {
	t.Helper()
	code, out, _ := adminBytes(t, req.method, f.base+req.path, bearer, req.ctype, req.body)
	return code, out
}

// directCase is one direct route fired on the drafts
// fixture: the route as routeTable names it, the request setup builds, the
// status it answers, the one item of its draft as kind, name and op, and
// the straza.audit.admin actions its publish writes, in order.
type directCase struct {
	name   string
	route  string
	setup  func(t *testing.T, f *draftsFixture) directReq
	status int
	item   [3]string
	// also are the items the route publishes with item in the same draft,
	// such as the set a role's deletion turns off.
	also    [][3]string
	actions []string
}

// mkRole stores the role name of kind and answers it.
func mkRole(t *testing.T, f *draftsFixture, name, kind string) store.Role {
	t.Helper()
	ro, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: name, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return ro
}

// mkOwnedRole stores an application role that server owns, with no access
// row, since only a role a server owns gains a new row.
func mkOwnedRole(t *testing.T, f *draftsFixture, name string, server store.App) store.Role {
	t.Helper()
	ro, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: name, Kind: store.RoleKindApplication, OwnerAppID: server.ID})
	if err != nil {
		t.Fatal(err)
	}
	return ro
}

// directCases are the nine app and role direct routes, an owned and a
// global role create among them.
func directCases() []directCase {
	return []directCase{
		{name: "an install", route: "POST /v1/admin/apps", status: http.StatusCreated, item: [3]string{"App", "docs", "put"}, actions: []string{"apps.install"},
			setup: func(t *testing.T, f *draftsFixture) directReq {
				up := startEchoUpstream(t)
				return directReq{method: http.MethodPost, path: "/v1/admin/apps", ctype: "application/yaml",
					body: []byte(strings.Replace(echoManifest(up.URL), "echoapp", "docs", 1))}
			}},
		{name: "a server removal", route: "DELETE /v1/admin/apps/{id}", status: http.StatusOK, item: [3]string{"App", "jira", "remove"}, actions: []string{"roles.delete", "apps.remove"},
			setup: func(t *testing.T, _ *draftsFixture) directReq {
				return jsonReq(t, http.MethodDelete, "/v1/admin/apps/jira", nil)
			}},
		{name: "a global role", route: "POST /v1/admin/roles", status: http.StatusCreated, item: [3]string{"Role", "helpers", "put"}, actions: []string{"roles.create"},
			setup: func(t *testing.T, _ *draftsFixture) directReq {
				return jsonReq(t, http.MethodPost, "/v1/admin/roles", map[string]any{"name": "helpers", "description": "Helpers."})
			}},
		{name: "a role the server owns", route: "POST /v1/admin/roles", status: http.StatusCreated, item: [3]string{"Role", "github-readers", "put"}, actions: []string{"roles.create", "apps.binding.create"},
			setup: func(t *testing.T, _ *draftsFixture) directReq {
				return jsonReq(t, http.MethodPost, "/v1/admin/roles", map[string]any{"name": "github-readers", "description": "Reads.", "server": "github", "tools": []string{"get_issue"}})
			}},
		{name: "a description", route: "PATCH /v1/admin/roles/{id}", status: http.StatusOK, item: [3]string{"Role", "old", "put"}, actions: []string{"roles.update"},
			setup: func(t *testing.T, f *draftsFixture) directReq {
				ro := mkRole(t, f, "old", store.RoleKindBusiness)
				return jsonReq(t, http.MethodPatch, "/v1/admin/roles/"+ro.ID, map[string]any{"description": "New words."})
			}},
		{name: "a role removal", route: "DELETE /v1/admin/roles/{id}", status: http.StatusOK, item: [3]string{"Role", "gone", "remove"},
			also: [][3]string{{"PolicySet", "gone-access", "off"}}, actions: []string{"roles.unassign", "roles.implication.delete", "roles.delete", "policy.deactivate"},
			setup: func(t *testing.T, f *draftsFixture) directReq {
				gone, base := mkRole(t, f, "gone", store.RoleKindApplication), mkRole(t, f, "base", store.RoleKindApplication)
				if err := f.app.store.Roles().AddImplication(context.Background(), gone.ID, base.ID); err != nil {
					t.Fatal(err)
				}
				mkHuman(t, f.app, "hal", "gone")
				if code, out, _ := adminBytes(t, http.MethodPut, f.base+"/v1/admin/policies", f.root, "application/yaml", []byte(roleSet("gone-access", "{roles: [gone]}"))); code != http.StatusCreated {
					t.Fatalf("store gone-access = %d %s", code, out)
				}
				if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/policies/gone-access/activate", f.root, map[string]string{"status": "active"}, nil); code != http.StatusOK {
					t.Fatalf("activate gone-access = %d", code)
				}
				return jsonReq(t, http.MethodDelete, "/v1/admin/roles/"+gone.ID, nil)
			}},
		{name: "an implication", route: "POST /v1/admin/roles/{id}/implications", status: http.StatusCreated, item: [3]string{"Role", "lead", "put"}, actions: []string{"roles.implication.create"},
			setup: func(t *testing.T, f *draftsFixture) directReq {
				lead, coder := mkRole(t, f, "lead", store.RoleKindBusiness), mkRole(t, f, "coder", store.RoleKindApplication)
				return jsonReq(t, http.MethodPost, "/v1/admin/roles/"+lead.ID+"/implications", map[string]any{"implies_role_id": coder.ID})
			}},
		{name: "an implication removal", route: "DELETE /v1/admin/roles/{id}/implications/{implicationId}", status: http.StatusOK, item: [3]string{"Role", "chief", "put"}, actions: []string{"roles.implication.delete"},
			setup: func(t *testing.T, f *draftsFixture) directReq {
				chief, oncall := mkRole(t, f, "chief", store.RoleKindBusiness), mkRole(t, f, "oncall", store.RoleKindApplication)
				if err := f.app.store.Roles().AddImplication(context.Background(), chief.ID, oncall.ID); err != nil {
					t.Fatal(err)
				}
				return jsonReq(t, http.MethodDelete, "/v1/admin/roles/"+chief.ID+"/implications/"+oncall.ID, nil)
			}},
		{name: "an access row", route: "POST /v1/admin/apps/{id}/bindings", status: http.StatusCreated, item: [3]string{"Role", "github-app", "put"}, actions: []string{"apps.binding.create"},
			setup: func(t *testing.T, f *draftsFixture) directReq {
				mkOwnedRole(t, f, "github-app", f.github)
				return jsonReq(t, http.MethodPost, "/v1/admin/apps/github/bindings", map[string]any{"role": "github-app", "tools": []string{"search"}})
			}},
		{name: "an access row removal", route: "DELETE /v1/admin/bindings/{id}", status: http.StatusOK, item: [3]string{"Role", "gh-rm", "put"}, actions: []string{"apps.binding.delete"},
			setup: func(t *testing.T, f *draftsFixture) directReq {
				ro := mkRole(t, f, "gh-rm", store.RoleKindApplication)
				b, err := f.app.store.ToolBindings().Create(context.Background(), store.ToolBinding{RoleID: ro.ID, AppID: f.github.ID, ToolMatcher: `["search"]`})
				if err != nil {
					t.Fatal(err)
				}
				return jsonReq(t, http.MethodDelete, "/v1/admin/bindings/"+b.ID, nil)
			}},
	}
}

// newestDraft answers the draft the store holds with the highest id.
func (f *draftsFixture) newestDraft(t *testing.T) (store.DraftRow, []store.DraftItemRow) {
	t.Helper()
	rows, err := f.app.store.Drafts().List(context.Background(), store.DraftFilter{}, 0, 1)
	if err != nil || len(rows) == 0 {
		t.Fatalf("the store holds no draft: %v", err)
	}
	return f.stored(t, strconv.FormatInt(rows[0].ID, 10))
}

// gatewayMatchesTheStore fails unless this replica's gateway holds exactly
// the access rows the store holds.
func gatewayMatchesTheStore(t *testing.T, app *App) {
	t.Helper()
	st, err := app.store.Drafts().LiveState(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var want, got []string
	for _, r := range st.Access {
		want = append(want, accessKey(r.Role, r.App, r.Matchers))
	}
	for _, b := range app.gateway.bindings.Load().([]gwBinding) {
		got = append(got, accessKey(b.Role, b.App, b.Matchers))
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Errorf("the gateway holds %q, want the store's %q", got, want)
	}
}

// TestDirectRoutesPublishOneItemDrafts pins the one-item publish frame on
// the nine
// app and role routes: each answers its status, leaves exactly one draft,
// published through the api door by the caller and holding the item the
// route makes, with the items it publishes alongside, moves the generation
// by one, and leaves this replica's gateway equal to the store.
func TestDirectRoutesPublishOneItemDrafts(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	for _, tc := range directCases() {
		req := tc.setup(t, f)
		before, gen := f.drafts(t), mustGeneration(t, f.app)
		code, out := f.send(t, f.root, req)
		if code != tc.status {
			t.Errorf("%s: %s %s = %d %s, want %d", tc.name, req.method, req.path, code, out, tc.status)
			continue
		}
		if after := f.drafts(t); after != before+1 {
			t.Errorf("%s: the store holds %d drafts, want %d", tc.name, after, before+1)
			continue
		}
		row, items := f.newestDraft(t)
		if row.State != "published" || row.Door != "api" || row.Proposer.ID != f.kim.ID || row.DecidedBy.ID != f.kim.ID {
			t.Errorf("%s: the draft is %s through %s by %q, want published through api by kim", tc.name, row.State, row.Door, row.Proposer.Name)
		}
		var held [][3]string
		for _, it := range items {
			held = append(held, [3]string{it.Kind, it.Name, it.Op})
		}
		if want := append([][3]string{tc.item}, tc.also...); !slices.Equal(held, want) {
			t.Errorf("%s: the draft holds %v, want the items %v", tc.name, held, want)
		}
		if g := mustGeneration(t, f.app); g != gen+1 {
			t.Errorf("%s: the generation is %d, want %d", tc.name, g, gen+1)
		}
		gatewayMatchesTheStore(t, f.app)
	}
	if open, err := f.app.store.Drafts().List(ctx, store.DraftFilter{State: "open"}, 0, 10); err != nil || len(open) != 0 {
		t.Errorf("open drafts = %d, %v, want none", len(open), err)
	}
}

// mustGeneration reads the config generation.
func mustGeneration(t *testing.T, app *App) int64 {
	t.Helper()
	g, err := app.store.Drafts().Generation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestDirectRoutesRecordTheirDraft pins the records of the nine routes:
// each straza.audit.admin action once and in order, the
// actor kim on each, every event naming the draft and this replica as its
// source, and no draft.* record. Among them are apps.binding.create
// beside roles.create for an owned role, roles.unassign "role deleted" per
// direct holder, and the implication records.
func TestDirectRoutesRecordTheirDraft(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	for _, tc := range directCases() {
		req := tc.setup(t, f)
		if code, out := f.send(t, f.root, req); code != tc.status {
			t.Errorf("%s: %s %s = %d %s, want %d", tc.name, req.method, req.path, code, out, tc.status)
			continue
		}
		row, _ := f.newestDraft(t)
		id := strconv.FormatInt(row.ID, 10)
		var actions []string
		for _, ev := range outboxOf(t, f.app, id) {
			if ev.source != f.app.instance || ev.data["draft"] != id {
				t.Errorf("%s: %s names source %q and draft %v, want %q and %s", tc.name, ev.subject, ev.source, ev.data["draft"], f.app.instance, id)
			}
			if ev.subject != "straza.audit.admin" {
				continue
			}
			action, _ := ev.data["action"].(string)
			actions = append(actions, action)
			if ev.data["actor"] != "kim" {
				t.Errorf("%s: %s names the actor %v, want kim", tc.name, action, ev.data["actor"])
			}
			if action == "roles.unassign" && ev.data["reason"] != "role deleted" {
				t.Errorf("%s: roles.unassign reads the reason %v, want role deleted", tc.name, ev.data["reason"])
			}
		}
		if !slices.Equal(actions, tc.actions) {
			t.Errorf("%s: the records are %q, want %q", tc.name, actions, tc.actions)
		}
	}
}

// countingUpstream is the echo upstream with a count of the MCP
// initialize requests it answered, which a server start sends.
func countingUpstream(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	echo := startEchoUpstream(t)
	var inits atomic.Int32
	target, err := url.Parse(echo.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if strings.Contains(string(raw), `"initialize"`) {
				inits.Add(1)
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	return up, &inits
}

// auditActions counts the straza.audit.admin records of action.
func auditActions(t *testing.T, app *App, action string) int {
	t.Helper()
	n := 0
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == action {
			n++
		}
	}
	return n
}

// TestAChangeEqualToLiveWritesNothing pins the no-op rule on
// the direct routes: an install of the stored manifest answers 201 with no
// draft, no apps.install, no new generation and no restart of the server,
// and a PATCH that sends the description a role has answers 200 with no
// draft and no roles.update.
func TestAChangeEqualToLiveWritesNothing(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	up, inits := countingUpstream(t)
	manifest := []byte(strings.Replace(echoManifest(up.URL), "echoapp", "docs", 1))
	if code, out := f.send(t, f.root, directReq{method: http.MethodPost, path: "/v1/admin/apps", ctype: "application/yaml", body: manifest}); code != http.StatusCreated {
		t.Fatalf("first install = %d %s", code, out)
	}
	ro := mkRole(t, f, "steady", store.RoleKindBusiness)
	if code, out := f.send(t, f.root, jsonReq(t, http.MethodPatch, "/v1/admin/roles/"+ro.ID, map[string]any{"description": "Steady."})); code != http.StatusOK {
		t.Fatalf("first PATCH = %d %s", code, out)
	}
	drafts, gen, started := f.drafts(t), mustGeneration(t, f.app), inits.Load()
	installs, updates := auditActions(t, f.app, "apps.install"), auditActions(t, f.app, "roles.update")
	var answer appPayload
	if code := rawReq(t, http.MethodPost, f.base+"/v1/admin/apps", f.root, "application/yaml", manifest, &answer); code != http.StatusCreated || answer.Name != "docs" {
		t.Errorf("the same manifest again = %d %+v, want 201 naming docs", code, answer)
	}
	var role rolePayload
	if code := adminReq(t, http.MethodPatch, f.base+"/v1/admin/roles/"+ro.ID, f.root, map[string]any{"description": "Steady."}, &role); code != http.StatusOK || role.Description != "Steady." {
		t.Errorf("the same description again = %d %+v, want 200 with the description", code, role)
	}
	if f.drafts(t) != drafts || mustGeneration(t, f.app) != gen {
		t.Errorf("the changes equal to live wrote a draft or moved the generation")
	}
	if inits.Load() != started {
		t.Errorf("the same manifest restarted the server: %d initialize requests, want %d", inits.Load(), started)
	}
	if auditActions(t, f.app, "apps.install") != installs || auditActions(t, f.app, "roles.update") != updates {
		t.Errorf("the changes equal to live wrote apps.install or roles.update")
	}
}

// TestInstallRefusesTwoDocuments pins that an install whose body holds two
// documents answers 422 with bundle.split's words for a change and stores
// nothing, while its dry run keeps answering what the first document
// declares.
func TestInstallRefusesTwoDocuments(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	one := draftApp("docs", "https://docs.example/mcp", "Docs.")
	body := one + "---\n" + draftApp("more", "https://more.example/mcp", "More.")
	item := drafts.Item{Kind: drafts.KindApp, Name: "docs", Op: drafts.OpPut, Doc: body}
	fs := intakeOf(drafts.Draft{Door: drafts.DoorAPI, Items: []drafts.Item{item}}, drafts.Principal{})
	if len(fs) == 0 || fs[0].Code != "bundle.split" {
		t.Fatalf("intake of two documents = %+v, want bundle.split first", fs)
	}
	var out struct {
		Error string `json:"error"`
	}
	want := findingWords(fs[0].ForChange(item))
	if code := rawReq(t, http.MethodPost, f.base+"/v1/admin/apps", f.root, "application/yaml", []byte(body), &out); code != http.StatusUnprocessableEntity || out.Error != want {
		t.Errorf("two documents = %d %q, want 422 %q", code, out.Error, want)
	}
	if n := f.drafts(t); n != 0 {
		t.Errorf("the refused install stored %d drafts", n)
	}
	if _, err := f.app.store.Apps().GetByName(context.Background(), "docs"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the refused install stored docs: %v", err)
	}
	var dry map[string]any
	if code := rawReq(t, http.MethodPost, f.base+"/v1/admin/apps?dryRun=1", f.root, "application/yaml", []byte(body), &dry); code != http.StatusOK || dry["name"] != "docs" {
		t.Errorf("the dry run of two documents = %d %v, want 200 naming docs", code, dry)
	}
}

// secretKey is a value the secret scan reads as a secret after the word
// key in free text.
const secretKey = "key wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

// TestDirectRoutesRefuseASecret pins the secret scan on the direct routes:
// an install
// whose address carries a password answers 422, and a role whose document
// would carry a secret, sent now or already in its live description,
// answers 400, each storing nothing.
func TestDirectRoutesRefuseASecret(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ro := mkRole(t, f, "plain", store.RoleKindBusiness)
	held, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: "gh-held", Kind: store.RoleKindApplication, Description: secretKey, OwnerAppID: f.github.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		req  directReq
		want int
	}{
		{"an install with a password in its address", directReq{method: http.MethodPost, path: "/v1/admin/apps", ctype: "application/yaml",
			body: []byte(draftApp("linear", "https://bot:Hunter2Hunter2@linear.example/mcp", "x"))}, http.StatusUnprocessableEntity},
		{"a role created with a secret", jsonReq(t, http.MethodPost, "/v1/admin/roles", map[string]any{"name": "leaky", "description": secretKey}), http.StatusBadRequest},
		{"a description with a secret", jsonReq(t, http.MethodPatch, "/v1/admin/roles/"+ro.ID, map[string]any{"description": secretKey}), http.StatusBadRequest},
		{"an access row for a role whose description holds a secret", jsonReq(t, http.MethodPost, "/v1/admin/apps/github/bindings", map[string]any{"role": held.Name, "tools": []string{"search"}}), http.StatusBadRequest},
	} {
		before, gen := f.drafts(t), mustGeneration(t, f.app)
		code, out := f.send(t, f.root, tc.req)
		var answer struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(out, &answer)
		if code != tc.want || !strings.Contains(answer.Error, "A change cannot carry a secret") {
			t.Errorf("%s = %d %q, want %d with the secret scan's words", tc.name, code, answer.Error, tc.want)
		}
		if f.drafts(t) != before || mustGeneration(t, f.app) != gen {
			t.Errorf("%s stored a draft or moved the generation", tc.name)
		}
	}
}

// TestDirectRoutesWordRefusalsForTheChange pins the words of an intake
// refusal on a direct route, whose caller changed one object and sent no
// draft: the object named as that caller knows it, the fix making the
// change again, and never a draft or a document number. Each refusal
// stores nothing.
func TestDirectRoutesWordRefusalsForTheChange(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	plain := mkRole(t, f, "plain", store.RoleKindBusiness)
	held, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: "gh-held", Kind: store.RoleKindApplication, Description: secretKey, OwnerAppID: f.github.ID})
	if err != nil {
		t.Fatal(err)
	}
	tokenNamed := mkRole(t, f, "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB", store.RoleKindBusiness)
	if code, out, _ := adminBytes(t, http.MethodPut, f.base+"/v1/admin/policies", f.root, "application/yaml", []byte(draftSet("guard"))); code != http.StatusCreated {
		t.Fatalf("store guard = %d %s", code, out)
	}
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/policies/guard/activate", f.root, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate guard = %d", code)
	}
	const (
		why     = "A change cannot carry a secret, because Straza keeps the text of every change in its history, where administrators read it."
		setWhy  = why + " Once published, its text, comments included, is served to anyone who asks, without a sign-in."
		comment = "# token ghp_0123456789abcdefghijklmnopqrstuvwxyzAB\n"
	)
	docs := draftApp("docs", "https://docs.example/mcp", "Docs.")
	yamlReq := func(method, path, body string) directReq {
		return directReq{method: method, path: path, ctype: "application/yaml", body: []byte(body)}
	}
	for _, tc := range []struct {
		name  string
		req   directReq
		code  int
		words string
	}{
		{"a role description that holds a secret", jsonReq(t, http.MethodPatch, "/v1/admin/roles/"+plain.ID, map[string]any{"description": secretKey}), http.StatusBadRequest,
			"In the role plain, spec.description holds a value after a word that names a secret. " + why + " Remove the value. A role never needs a secret."},
		{"an access row for a role whose live description holds a secret", jsonReq(t, http.MethodPost, "/v1/admin/apps/github/bindings", map[string]any{"role": held.Name, "tools": []string{"search"}}),
			http.StatusBadRequest, "In the role gh-held, spec.description holds a value after a word that names a secret. " + why + " Remove the value. A role never needs a secret."},
		{"a role name with an invisible character", jsonReq(t, http.MethodPost, "/v1/admin/roles", map[string]any{"name": "dev\u200b", "kind": "business"}), http.StatusBadRequest,
			"The name of the role devU+200B holds an invisible character, zero width space (U+200B), which can make it read differently from what is stored. Rename the object and make the change again."},
		{"a role name with letters from two scripts", jsonReq(t, http.MethodPost, "/v1/admin/roles", map[string]any{"name": "\u0430dmin", "kind": "business"}), http.StatusBadRequest,
			"The name of the role \u0430dmin holds letters from both Cyrillic and Latin, which can make it pass for another name. Rename the object and make the change again."},
		{"an install of two documents", yamlReq(http.MethodPost, "/v1/admin/apps", docs+"---\n"+draftApp("more", "https://more.example/mcp", "More.")), http.StatusUnprocessableEntity,
			"The manifest of the server docs holds 2 documents, and a change takes one. Send each document as a change of its own."},
		{"an install whose name a tag writes", yamlReq(http.MethodPost, "/v1/admin/apps", strings.Replace(docs, "name: docs", "name: !!str docs", 1)), http.StatusUnprocessableEntity,
			"The manifest of the server docs writes its kind or name with a YAML tag, alias or merge key, so the name Straza reads can differ from the text a reviewer reads. " +
				"Write kind and metadata.name as plain text, and make the change again."},
		{"an install that carries a mask", yamlReq(http.MethodPost, "/v1/admin/apps", strings.Replace(docs, "value: team-a", `value: "[REDACTED]"`, 1)), http.StatusUnprocessableEntity,
			"The manifest of the server docs holds a value that Straza masked for display at server.remotes[0].headers[0].value, so publishing it would store the mask in place of the value. " +
				"Write the real value in place of the mask, or leave that place exactly as strazactl apps export prints it to keep the stored value, and make the change again."},
		{"an install with a password in its address", yamlReq(http.MethodPost, "/v1/admin/apps", draftApp("linear", "https://bot:Hunter2Hunter2@linear.example/mcp", "x")), http.StatusUnprocessableEntity,
			"In the server linear, server.remotes[0].url holds an address that carries a password or a credential before its host. " + why +
				" Remove the user name and password from the address. If the server needs the secret, make the change without it, then store it with strazactl apps secret set linear, " +
				"which asks for the value at a hidden prompt, and set credential.inject so Straza adds it for you."},
		{"a new policy set with a token in a comment", yamlReq(http.MethodPut, "/v1/admin/policies", draftSet("leaky")+comment), http.StatusBadRequest,
			"In the policy set leaky, line 13 holds what looks like a GitHub token. " + setWhy + " Remove it. A policy set never needs a secret, in a rule or in a comment."},
		{"a saved edit of a live set with a token in a comment", yamlReq(http.MethodPut, "/v1/admin/policies", draftSet("guard")+comment), http.StatusBadRequest,
			"In the policy set guard, line 13 holds what looks like a GitHub token. " + setWhy + " Remove it. A policy set never needs a secret, in a rule or in a comment."},
		{"the removal of a role whose name looks like a token", jsonReq(t, http.MethodDelete, "/v1/admin/roles/"+tokenNamed.ID, nil), http.StatusBadRequest,
			"The name of the role you asked to remove holds what looks like a GitHub token. " + why +
				" Straza does not change or remove a role whose name looks like a secret, so this one stays. Treat the secret in its name as exposed and rotate it."},
		{"an install of a 1 MiB manifest", yamlReq(http.MethodPost, "/v1/admin/apps", docs+"#"+strings.Repeat("a", 1<<20-len(docs)-2)+"\n"), http.StatusUnprocessableEntity,
			"The change to the server docs holds 1,048,586 bytes, and a change holds at most 1 MiB. Make the manifest of the server docs smaller and make the change again."},
	} {
		before := f.drafts(t)
		code, out := f.send(t, f.root, tc.req)
		var answer struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(out, &answer)
		if code != tc.code || answer.Error != tc.words {
			t.Errorf("%s = %d\n got %q\nwant %d %q", tc.name, code, answer.Error, tc.code, tc.words)
		}
		if f.drafts(t) != before {
			t.Errorf("%s stored a draft", tc.name)
		}
	}
}

// TestDirectRouteRunsAgainWhenLiveStateMoved pins the rerun rule on a
// direct route:
// a commit that met a moved generation runs again from the World read and
// lands once, three such commits answer 409 and three busy ones 503, and
// a role removed between two runs answers the route's own 404, because
// the route's refusals run again on every run.
func TestDirectRouteRunsAgainWhenLiveStateMoved(t *testing.T) {
	t.Parallel()
	f, h := hooked(t)
	ctx := context.Background()
	moved := func(context.Context, int) error { return store.PublishConflict{Generation: true} }
	busy := func(context.Context, int) error { return store.PublishConflict{Busy: true} }
	for _, tc := range []struct {
		name   string
		before func(context.Context, int) error
		req    func() directReq
		want   int
		error  string
		calls  int32
		drafts int
	}{
		{"one conflict", func(_ context.Context, n int) error {
			if n == 1 {
				return store.PublishConflict{Generation: true}
			}
			return nil
		}, func() directReq {
			return jsonReq(t, http.MethodPost, "/v1/admin/roles", map[string]any{"name": "again", "description": "Again."})
		}, http.StatusCreated, "", 2, 1},
		{"three conflicts", moved, func() directReq {
			return jsonReq(t, http.MethodPost, "/v1/admin/roles", map[string]any{"name": "never", "description": "Never."})
		}, http.StatusConflict, directMovedRefusal, 3, 0},
		{"three busy answers", busy, func() directReq {
			return jsonReq(t, http.MethodPost, "/v1/admin/roles", map[string]any{"name": "busy", "description": "Busy."})
		}, http.StatusServiceUnavailable, directBusyRefusal, 3, 0},
		{"a role removed between runs", func(ctx context.Context, n int) error {
			if n == 1 {
				ro, err := f.app.store.Roles().GetByName(ctx, "fleeting")
				if err == nil {
					err = f.app.store.Roles().Delete(ctx, ro.ID)
				}
				if err != nil {
					return err
				}
				return store.PublishConflict{Generation: true}
			}
			return nil
		}, func() directReq {
			ro := mkRole(t, f, "fleeting", store.RoleKindBusiness)
			return jsonReq(t, http.MethodPatch, "/v1/admin/roles/"+ro.ID, map[string]any{"description": "Changed."})
		}, http.StatusNotFound, "no such role", 1, 0},
	} {
		req := tc.req()
		before, gen := f.drafts(t), mustGeneration(t, f.app)
		h.arm(tc.before, nil)
		code, out := f.send(t, f.root, req)
		h.disarm()
		var answer struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(out, &answer)
		if code != tc.want || (tc.error != "" && answer.Error != tc.error) {
			t.Errorf("%s = %d %q, want %d %q", tc.name, code, answer.Error, tc.want, tc.error)
		}
		if n := h.calls.Load(); n != tc.calls {
			t.Errorf("%s called Publish %d times, want %d", tc.name, n, tc.calls)
		}
		if got := f.drafts(t) - before; got != tc.drafts {
			t.Errorf("%s stored %d drafts, want %d", tc.name, got, tc.drafts)
		}
		if tc.drafts == 0 && mustGeneration(t, f.app) != gen {
			t.Errorf("%s moved the generation", tc.name)
		}
	}
	if _, err := f.app.store.Roles().GetByName(ctx, "again"); err != nil {
		t.Errorf("the role of the run that landed is missing: %v", err)
	}
}

// TestDirectRouteHoldsTheSecondPerson pins the second-person rule on the
// direct routes: with admin.secondPerson on, a route whose one-item verdict holds
// a risk answers the direct route's 409 and writes nothing, whoever calls
// it, and a change with no risk lands.
func TestDirectRouteHoldsTheSecondPerson(t *testing.T) {
	t.Parallel()
	f, _, _ := secondPersonFixture(t)
	before, gen := f.drafts(t), mustGeneration(t, f.app)
	var answer struct {
		Error string `json:"error"`
	}
	if code := adminReq(t, http.MethodDelete, f.base+"/v1/admin/apps/srv-a", f.root, nil, &answer); code != http.StatusConflict || answer.Error != secondPersonDirectRefusal {
		t.Errorf("a removal under admin.secondPerson = %d %q, want 409 %q", code, answer.Error, secondPersonDirectRefusal)
	}
	if f.drafts(t) != before || mustGeneration(t, f.app) != gen {
		t.Error("the refused removal stored a draft or moved the generation")
	}
	if _, err := f.app.store.Apps().GetByName(context.Background(), "srv-a"); err != nil {
		t.Errorf("the refused removal took srv-a: %v", err)
	}
	mkOwnedRole(t, f, "held-app", f.jira)
	mkHuman(t, f.app, "quin", "held-app")
	answer.Error = ""
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/apps/jira/bindings", f.root, map[string]any{"role": "held-app", "tools": []string{"x"}}, &answer); code != http.StatusConflict || answer.Error != secondPersonDirectRefusal {
		t.Errorf("a held role's new row on a server nobody listed = %d %q, want 409 %q", code, answer.Error, secondPersonDirectRefusal)
	}
	if f.drafts(t) != before || mustGeneration(t, f.app) != gen {
		t.Error("the refused access row stored a draft or moved the generation")
	}
	ro := mkRole(t, f, "quiet", store.RoleKindBusiness)
	if code := adminReq(t, http.MethodPatch, f.base+"/v1/admin/roles/"+ro.ID, f.root, map[string]any{"description": "Quiet."}, nil); code != http.StatusOK {
		t.Errorf("a description under admin.secondPerson = %d, want 200", code)
	}
}

// TestDirectRoutesTakeRepeatedTools pins that a tool listed twice keeps
// answering as today: an owned role's create and an access row's create
// answer 201 with the tools as sent and store each tool once, and a role
// whose stored row lists a tool twice, as the access row route stored it
// before drafts, still takes a new description and a new implication.
func TestDirectRoutesTakeRepeatedTools(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	var owned rolePayload
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.root, map[string]any{"name": "github-dup", "server": "github", "tools": []string{"get_issue", "get_issue"}}, &owned); code != http.StatusCreated ||
		!slices.Equal(owned.Tools, []string{"get_issue", "get_issue"}) {
		t.Errorf("an owned role with a repeated tool = %d %+v, want 201 with the tools as sent", code, owned)
	}
	mkOwnedRole(t, f, "dup-app", f.github)
	var row bindingPayload
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/apps/github/bindings", f.root, map[string]any{"role": "dup-app", "tools": []string{"b", "a", "b"}}, &row); code != http.StatusCreated ||
		!slices.Equal(row.Tools, []string{"b", "a", "b"}) {
		t.Errorf("an access row with a repeated tool = %d %+v, want 201 with the tools as sent", code, row)
	}
	st, err := f.app.store.Drafts().LiveState(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Access {
		if r.Role == "dup-app" && !slices.Equal(r.Matchers, []string{"a", "b"}) {
			t.Errorf("dup-app's row stores %q, want each tool once", r.Matchers)
		}
	}
	legacy, target := mkRole(t, f, "legacy-dup", store.RoleKindApplication), mkRole(t, f, "target-app", store.RoleKindApplication)
	if _, err := f.app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: legacy.ID, AppID: f.jira.ID, ToolMatcher: `["search","search"]`}); err != nil {
		t.Fatal(err)
	}
	if code, out := f.send(t, f.root, jsonReq(t, http.MethodPatch, "/v1/admin/roles/"+legacy.ID, map[string]any{"description": "Changed."})); code != http.StatusOK {
		t.Errorf("a description of a role whose row repeats a tool = %d %s, want 200", code, out)
	}
	if code, out := f.send(t, f.root, jsonReq(t, http.MethodPost, "/v1/admin/roles/"+legacy.ID+"/implications", map[string]any{"implies_role_id": target.ID})); code != http.StatusCreated {
		t.Errorf("an implication from a role whose row repeats a tool = %d %s, want 201", code, out)
	}
}

// TestDirectRoutesKeepALegacyRow pins that a business role bound before the
// kind rules keeps its row through a new description, since the rules
// judge only a row the change adds or changes.
func TestDirectRoutesKeepALegacyRow(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	biz := mkRole(t, f, "legacy-biz", store.RoleKindBusiness)
	if _, err := f.app.store.ToolBindings().Create(context.Background(), store.ToolBinding{RoleID: biz.ID, AppID: f.github.ID, ToolMatcher: `["search"]`}); err != nil {
		t.Fatal(err)
	}
	if code, out := f.send(t, f.root, jsonReq(t, http.MethodPatch, "/v1/admin/roles/"+biz.ID, map[string]any{"description": "Changed."})); code != http.StatusOK {
		t.Errorf("a description of a business role with a live row = %d %s, want 200", code, out)
	}
}

// TestImplicationRouteNamesAnUnknownIDAndAnEdgeThatExists pins the two
// sentences the implication route answers in place of the store's
// error: an id no role has, and an edge that exists already.
func TestImplicationRouteNamesAnUnknownIDAndAnEdgeThatExists(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	lead, coder := mkRole(t, f, "lead", store.RoleKindBusiness), mkRole(t, f, "coder", store.RoleKindApplication)
	if err := f.app.store.Roles().AddImplication(context.Background(), lead.ID, coder.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, from, to, want string
	}{
		{"an unknown implied id", lead.ID, "ghost", "add implication failed: no role has the id ghost. Read the ids with strazactl roles list."},
		{"two unknown ids", "ghost-a", "ghost-b", "add implication failed: no role has the id ghost-a. Read the ids with strazactl roles list."},
		{"an edge that exists", lead.ID, coder.ID, "add implication failed: lead implies coder already."},
	} {
		var answer struct {
			Error string `json:"error"`
		}
		code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles/"+tc.from+"/implications", f.root, map[string]any{"implies_role_id": tc.to}, &answer)
		if code != http.StatusBadRequest || answer.Error != tc.want {
			t.Errorf("%s = %d %q, want 400 %q", tc.name, code, answer.Error, tc.want)
		}
	}
	if n := f.drafts(t); n != 0 {
		t.Errorf("the refused implications stored %d drafts", n)
	}
}

// TestDirectRoutesAnswerAFailedReadInTheirWords pins the rule that
// a failed World read answers the route's own 500 sentence, never the
// drafts routes' 503.
func TestDirectRoutesAnswerAFailedReadInTheirWords(t *testing.T) {
	t.Parallel()
	fail, _, preRun := failingGeneration()
	f := newDraftsFixture(t, preRun)
	lead, coder := mkRole(t, f, "lead", store.RoleKindBusiness), mkRole(t, f, "coder", store.RoleKindApplication)
	if err := f.app.store.Roles().AddImplication(context.Background(), lead.ID, coder.ID); err != nil {
		t.Fatal(err)
	}
	b, err := f.app.store.ToolBindings().Create(context.Background(), store.ToolBinding{RoleID: coder.ID, AppID: f.github.ID, ToolMatcher: `["search"]`})
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	for _, tc := range []struct {
		req  directReq
		want string
	}{
		{directReq{method: http.MethodPost, path: "/v1/admin/apps", ctype: "application/yaml", body: []byte(draftApp("docs", "https://docs.example/mcp", "Docs."))},
			"install failed: Straza could not read the stored record of docs, so it cannot tell a change from a first install. Try again, and check the strazad log if it keeps failing."},
		{jsonReq(t, http.MethodDelete, "/v1/admin/apps/jira", nil), "remove failed"},
		{jsonReq(t, http.MethodPost, "/v1/admin/roles", map[string]any{"name": "helpers"}), "create failed"},
		{jsonReq(t, http.MethodPatch, "/v1/admin/roles/"+lead.ID, map[string]any{"description": "New."}), "update failed"},
		{jsonReq(t, http.MethodDelete, "/v1/admin/roles/"+lead.ID, nil), "role lookup failed"},
		{jsonReq(t, http.MethodPost, "/v1/admin/roles/"+coder.ID+"/implications", map[string]any{"implies_role_id": lead.ID}), "list implications failed"},
		{jsonReq(t, http.MethodDelete, "/v1/admin/roles/"+lead.ID+"/implications/"+coder.ID, nil), "remove implication failed"},
		{jsonReq(t, http.MethodPost, "/v1/admin/apps/jira/bindings", map[string]any{"role": "coder"}), "the access row could not be created"},
		{jsonReq(t, http.MethodDelete, "/v1/admin/bindings/"+b.ID, nil), "role lookup failed"},
	} {
		code, out := f.send(t, f.root, tc.req)
		var answer struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(out, &answer)
		if code != http.StatusInternalServerError || answer.Error != tc.want {
			t.Errorf("%s %s = %d %q, want 500 %q", tc.req.method, tc.req.path, code, answer.Error, tc.want)
		}
	}
}

// TestDirectRouteAfterAnUnconfirmedCommit pins that a Publish error that is
// no conflict leaves the outcome unknown, so the apply runs anyway and the
// route answers 500 saying to read the object again. A commit whose answer
// was lost is live on this replica, and one that never committed changed
// nothing.
func TestDirectRouteAfterAnUnconfirmedCommit(t *testing.T) {
	t.Parallel()
	f, h := hooked(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		role   string
		before func(context.Context, int) error
		after  func(int, error) error
		landed bool
	}{
		{"the answer of a commit was lost", "lost-app", nil, func(int, error) error { return errors.New("injected lost answer") }, true},
		{"the commit failed", "failed-app", func(context.Context, int) error { return errors.New("injected failure") }, nil, false},
	} {
		mkOwnedRole(t, f, tc.role, f.github)
		h.arm(tc.before, tc.after)
		var answer struct {
			Error string `json:"error"`
		}
		code := adminReq(t, http.MethodPost, f.base+"/v1/admin/apps/github/bindings", f.root, map[string]any{"role": tc.role, "tools": []string{"search"}}, &answer)
		h.disarm()
		if code != http.StatusInternalServerError || answer.Error != directUnconfirmedRefusal {
			t.Errorf("%s = %d %q, want 500 %q", tc.name, code, answer.Error, directUnconfirmedRefusal)
		}
		st, err := f.app.store.Drafts().LiveState(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		stored := slices.ContainsFunc(st.Access, func(r store.LiveAccess) bool { return r.Role == tc.role })
		if stored != tc.landed {
			t.Errorf("%s: the row is stored %v, want %v", tc.name, stored, tc.landed)
		}
		gatewayMatchesTheStore(t, f.app)
	}
}

// TestPublishOneAnswersRefusals pins how publishOne words a refusal: an
// intake refusal answers the route's body status, a Check code the route
// maps answers the route's words, and any other Check code 409
// "{sentence} {fix}".
func TestPublishOneAnswersRefusals(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ghost := drafts.Item{Kind: drafts.KindApp, Name: "ghost", Op: drafts.OpRemove}
	leaky := drafts.Item{Kind: drafts.KindRole, Name: "leaky", Op: drafts.OpPut, Doc: draftRole("leaky", secretKey)}
	var authors []drafts.Principal
	mapped := itemRoute(ghost, &authors)
	mapped.answer = func(f drafts.Finding) (int, string, bool) {
		return http.StatusNotFound, "unknown server", f.Code == "app.remove-missing"
	}
	intake := itemRoute(leaky, &authors)
	intake.bodyStatus = http.StatusUnprocessableEntity
	check := drafts.Check(drafts.World{Apps: map[string]drafts.App{}}, drafts.Draft{Items: []drafts.Item{ghost}}, drafts.CheckInput{RefusalsOnly: true})
	if len(check.Refused) == 0 {
		t.Fatal("Check does not refuse the removal of a server live state lacks")
	}
	for _, tc := range []struct {
		name  string
		route directRoute
		want  int
		words string
	}{
		{"a mapped code", mapped, http.StatusNotFound, "unknown server"},
		{"an unmapped code", itemRoute(ghost, &authors), http.StatusConflict, findingWords(check.Refused[0])},
		{"an intake refusal", intake, http.StatusUnprocessableEntity, ""},
	} {
		rec := httptest.NewRecorder()
		if _, ok := f.app.publishOne(rec, directRequest(f), tc.route); ok {
			t.Errorf("%s: publishOne reported success", tc.name)
			continue
		}
		var answer struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &answer)
		if rec.Code != tc.want || (tc.words != "" && answer.Error != tc.words) || answer.Error == "" {
			t.Errorf("%s = %d %q, want %d %q", tc.name, rec.Code, answer.Error, tc.want, tc.words)
		}
	}
	if n := f.drafts(t); n != 0 {
		t.Errorf("the refused changes stored %d drafts", n)
	}
}

// TestAgentNeverReachesADirectRoute pins that an agent holding the root
// role is refused on a direct route before intake, with the admin API's
// refusal of anyone who is not a person, and stores no draft.
func TestAgentNeverReachesADirectRoute(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	bot, err := f.app.store.Users().GetByUsername(context.Background(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	grantAdmin(t, f.app, bot.ID)
	manifest := `apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: tool}
server: {name: straza.test/tool, version: "1.0.0"}
straza:
  runtime:
    kind: command
    command: {exec: /bin/true}
`
	var answer struct {
		Error string `json:"error"`
	}
	code := rawReq(t, http.MethodPost, f.base+"/v1/admin/apps", f.bot, "application/yaml", []byte(manifest), &answer)
	if want := fmt.Sprintf(nonPersonAdminRefusal, "bot", "an agent"); code != http.StatusForbidden || answer.Error != want {
		t.Errorf("an agent's command server = %d %q, want 403 %q", code, answer.Error, want)
	}
	if n := f.drafts(t); n != 0 {
		t.Errorf("the refused install stored %d drafts", n)
	}
}

// TestPublishOneLogsAMapped500 pins that a Check code a route maps to a
// 5xx, as policy.compile maps to 500, answers through a.fail: the body
// carries the correlation id, and exactly one Error record with that id,
// the status and the finding's sentence as its cause reaches the log.
func TestPublishOneLogsAMapped500(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	log, buf := captureLogger()
	var authors []drafts.Principal
	route := itemRoute(drafts.Item{Kind: drafts.KindApp, Name: "ghost", Op: drafts.OpRemove}, &authors)
	route.answer = func(drafts.Finding) (int, string, bool) {
		return http.StatusInternalServerError, "snapshot publish failed", true
	}
	r := directRequest(f)
	ctx := context.WithValue(r.Context(), reqIDCtxKey{}, "rid-d12")
	r = r.WithContext(context.WithValue(ctx, reqLogCtxKey{}, log.With("correlation_id", "rid-d12")))
	rec := httptest.NewRecorder()
	if _, ok := f.app.publishOne(rec, r, route); ok {
		t.Fatal("publishOne reported success")
	}
	var answer map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &answer)
	if rec.Code != http.StatusInternalServerError || answer["error"] != "snapshot publish failed" || answer[correlationKey] != "rid-d12" {
		t.Errorf("a mapped 500 = %d %v, want 500 with the route's words and the correlation id", rec.Code, answer)
	}
	record := assertOneErrorWithCorrelation(t, buf, http.StatusInternalServerError, "rid-d12")
	ghost := drafts.Draft{Items: []drafts.Item{{Kind: drafts.KindApp, Name: "ghost", Op: drafts.OpRemove}}}
	v := drafts.Check(drafts.World{Apps: map[string]drafts.App{}}, ghost, drafts.CheckInput{RefusalsOnly: true})
	if len(v.Refused) == 0 || !strings.Contains(record, "cause="+strconv.Quote(v.Refused[0].Sentence)) {
		t.Errorf("the Error record lacks the finding's sentence as its cause: %s", record)
	}
}
