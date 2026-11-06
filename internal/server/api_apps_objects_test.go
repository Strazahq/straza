package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// listingUpstream is a remote MCP server whose tools carry the object
// marks: repo_list answers its five repos only to the listing token, and
// records every Authorization header it gets.
type listingUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
}

func (u *listingUpstream) listingAuths() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.auths...)
}

func startListingUpstream(t *testing.T) *listingUpstream {
	t.Helper()
	u := &listingUpstream{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "objects-demo", Version: "1.0.0"}, nil)
	object := func(reach map[string]any) mcp.Meta { return mcp.Meta{"ai.straza/object": reach} }
	srv.AddTool(&mcp.Tool{Name: "repo_list", InputSchema: map[string]any{"type": "object"},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Meta: mcp.Meta{"ai.straza/lists-objects": map[string]any{"type": "repo",
			"attributes": []any{map[string]any{"name": "area", "setBy": "org"}}}}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			auth := req.Extra.Header.Get("Authorization")
			u.mu.Lock()
			u.auths = append(u.auths, auth)
			u.mu.Unlock()
			if auth != "Bearer listing-tok-NEVER-LEAK" {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "repo_list answers the listing token only"}}}, nil
			}
			items := []any{}
			for i, v := range []string{"Strazahq/straza", "Strazahq/site", "Strazahq/pay-api", "Strazahq/docs", "Strazahq/helm"} {
				items = append(items, map[string]any{"value": v, "id": 9012330 + i, "title": strings.TrimPrefix(v, "Strazahq/"),
					"attributes": map[string]any{"area": "product", "topic": "drop me"}})
			}
			return &mcp.CallToolResult{StructuredContent: map[string]any{"objects": items}}, nil
		})
	srv.AddTool(&mcp.Tool{Name: "repo_read", Meta: object(map[string]any{"type": "repo", "argument": "repo"}),
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"repo": map[string]any{"type": "string"}}, "required": []any{"repo"}}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	srv.AddTool(&mcp.Tool{Name: "whoami", Meta: object(map[string]any{"type": "repo", "reach": "none"}), InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	u.Server = httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(u.Close)
	return u
}

// listingManifest is the objects-demo manifest pinning what its marks
// declare, with the server's own static credential.
func listingManifest(url string) string {
	return fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: objects-demo}
server: {name: straza.test/objects-demo, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  credential:
    kind: static
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
  objects:
    type: repo
    list:
      tool: repo_list
      attributes: [{name: area, setBy: org}]
    tools:
      repo_read: {argument: repo}
      whoami: none
`, url)
}

// TestAppObjectsAPI pins the object list routes end to end on one replica:
// the answer before a listing credential exists, a listing that fails with
// the missing credential sentence, the credential routes, a good listing
// with coverage and paging, the refresh limit, a delete that fails the next
// listing and keeps the last good list, and the records each step writes,
// with the listing token never in an answer or a record.
func TestAppObjectsAPI(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Apps.Objects.Enabled = true })
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()
	up := startListingUpstream(t)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(listingManifest(up.URL)), nil); code != http.StatusCreated {
		t.Fatalf("install objects-demo = %d", code)
	}
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(echoManifest(startEchoUpstream(t).URL)), nil); code != http.StatusCreated {
		t.Fatalf("install echoapp = %d", code)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/apps/objects-demo/secrets", bearer, map[string]any{"value": "app-tok"}, nil); code != http.StatusCreated {
		t.Fatalf("set the server's own secret = %d", code)
	}
	row, err := app.store.Apps().GetByName(ctx, "objects-demo")
	if err != nil {
		t.Fatal(err)
	}
	bind := func(role, matcher string) {
		t.Helper()
		ro, err := app.store.Roles().Create(ctx, store.Role{Name: role, Kind: store.RoleKindApplication})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ro.ID, AppID: row.ID, ToolMatcher: matcher, Scope: &store.ObjectScope{Every: true}}); err != nil {
			t.Fatal(err)
		}
	}
	bind("objects-demo-tools", `["*"]`)
	bind("objects-demo-whoami", `["whoami"]`)
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}

	get := func(query string) objectsAnswer {
		t.Helper()
		code, raw, _ := adminBytes(t, "GET", base+"/v1/admin/apps/objects-demo/objects"+query, bearer, "", nil)
		if code != http.StatusOK {
			t.Fatalf("GET objects%s = %d %s", query, code, raw)
		}
		assertSchema(t, "ObjectList", raw)
		var out objectsAnswer
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	waitFor := func(what string, done func(objectsAnswer) bool) objectsAnswer {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			out := get("")
			if done(out) {
				return out
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: last answer %+v", what, out)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Before the listing credential: the pinned block, the declared one
	// from the live marks, no drift, and a listing never made.
	first := waitFor("the marks reach the answer", func(o objectsAnswer) bool { return o.Declared != nil })
	if first.Type != "repo" || first.Plural != "repos" || first.Pinned == nil || first.Drift || len(first.DeclaredFaults) != 0 ||
		len(first.Attributes) != 1 || first.Attributes[0] != (manager.ObjectAttribute{Name: "area", SetBy: "org"}) {
		t.Fatalf("answer before a listing = %+v", first)
	}
	if l := first.Listing; l == nil || l.Tool != "repo_list" || l.Credential != "missing" || len(first.Objects) != 0 {
		t.Fatalf("listing before the credential = %+v, objects %d", l, len(first.Objects))
	}

	// A refresh without the listing credential fails with R1 and never
	// falls back to the server's own secret.
	var queued map[string]string
	if code := adminReq(t, "POST", base+"/v1/admin/apps/objects-demo/objects/refresh", bearer, nil, &queued); code != http.StatusAccepted ||
		queued["app"] != "objects-demo" || queued["dueAt"] == "" {
		t.Fatalf("refresh = %d %v", code, queued)
	}
	const r1 = "objects-demo has no listing credential, so Straza cannot list its repos. Set one on its server page under Server authentication, or with strazactl apps secret set objects-demo --listing. Then press List again on its repos tab, or run strazactl apps objects refresh objects-demo."
	failed := waitFor("the failed listing", func(o objectsAnswer) bool { return o.Listing.Outcome == store.ObjectsFailed })
	if failed.Listing.Reason != r1 || len(up.listingAuths()) != 0 {
		t.Fatalf("failed listing = %+v, listing calls %v", failed.Listing, up.listingAuths())
	}

	// The credential routes: refusals, then a set that lists at once.
	cases := []struct {
		app, value string
		code       int
		want       string
	}{
		{app: "objects-demo", code: http.StatusBadRequest, want: "value is required"},
		{app: "echoapp", value: "x", code: http.StatusConflict,
			want: "echoapp pins no object type, so a listing credential has no use. Pin its objects first with strazactl apps objects draft echoapp, then publish the draft."},
		{app: "nope", value: "x", code: http.StatusNotFound, want: "unknown server"},
	}
	for _, tc := range cases {
		var out map[string]any
		if code := adminReq(t, "POST", base+"/v1/admin/apps/"+tc.app+"/objects/credential", bearer, map[string]any{"value": tc.value}, &out); code != tc.code || out["error"] != tc.want {
			t.Errorf("set on %s = %d %v, want %d %q", tc.app, code, out, tc.code, tc.want)
		}
	}
	code, raw, _ := adminBytes(t, "POST", base+"/v1/admin/apps/objects-demo/objects/credential", bearer, "application/json", []byte(`{"value":"listing-tok-NEVER-LEAK"}`))
	var set map[string]string
	if err := json.Unmarshal(raw, &set); err != nil || code != http.StatusCreated ||
		set["app"] != "objects-demo" || set["fingerprint"] != secrets.Fingerprint("listing-tok-NEVER-LEAK") || set["setAt"] == "" {
		t.Fatalf("set the listing credential = %d %s", code, raw)
	}
	assertSchema(t, "ListingCredential", raw)
	ok := waitFor("the good listing", func(o objectsAnswer) bool { return o.Listing.Outcome == store.ObjectsOK })
	if l := ok.Listing; l.Credential != "set" || l.Version != 1 || l.Count != 5 || l.Joined != 5 || l.ListedAt == "" || l.Reason != "" || ok.Total != 5 {
		t.Fatalf("good listing = %+v total %d", l, ok.Total)
	}
	for _, auth := range up.listingAuths() {
		if auth != "Bearer listing-tok-NEVER-LEAK" {
			t.Errorf("repo_list got %q, want exactly the listing token", auth)
		}
	}
	wantValues := []string{"Strazahq/docs", "Strazahq/helm", "Strazahq/pay-api", "Strazahq/site", "Strazahq/straza"}
	for i, o := range ok.Objects {
		if o.Value != wantValues[i] || o.New || len(o.CoveredBy) != 1 || o.CoveredBy[0] != "objects-demo-tools" ||
			len(o.Attributes) != 1 || o.Attributes["area"].Values[0] != "product" {
			t.Errorf("object %d = %+v", i, o)
		}
	}
	if ok.Objects[4].ID != "9012330" || ok.Objects[4].Title != "straza" {
		t.Errorf("Strazahq/straza = %+v, want id 9012330 as a string", ok.Objects[4])
	}

	// Paging and the filter.
	pg := get("?limit=2")
	if len(pg.Objects) != 2 || pg.Next != "Strazahq/helm" || pg.Total != 5 {
		t.Errorf("first page = %d objects next %q total %d", len(pg.Objects), pg.Next, pg.Total)
	}
	pg = get("?limit=2&after=Strazahq/helm")
	if len(pg.Objects) != 2 || pg.Objects[0].Value != "Strazahq/pay-api" || pg.Next != "Strazahq/site" {
		t.Errorf("second page = %+v next %q", pg.Objects, pg.Next)
	}
	pg = get("?limit=2&after=Strazahq/site")
	if len(pg.Objects) != 1 || pg.Next != "" {
		t.Errorf("last page = %+v next %q", pg.Objects, pg.Next)
	}
	pg = get("?q=PAY")
	if pg.Total != 1 || len(pg.Objects) != 1 || pg.Objects[0].Value != "Strazahq/pay-api" {
		t.Errorf("filter PAY = %+v total %d", pg.Objects, pg.Total)
	}
	var bad map[string]any
	if code := adminReq(t, "GET", base+"/v1/admin/apps/objects-demo/objects?limit=1001", bearer, nil, &bad); code != http.StatusBadRequest ||
		bad["error"] != "limit must be a whole number from 1 to 1000." {
		t.Errorf("limit 1001 = %d %v", code, bad)
	}

	// A refresh within the minute is refused with when to try again.
	if code := adminReq(t, "POST", base+"/v1/admin/apps/objects-demo/objects/refresh", bearer, nil, &bad); code != http.StatusTooManyRequests ||
		!regexp.MustCompile(`^objects-demo was listed [0-9]+ seconds? ago\. Refresh again in [0-9]+ seconds?\.$`).MatchString(fmt.Sprint(bad["error"])) {
		t.Errorf("refresh within the minute = %d %v", code, bad)
	}

	// A server that pins no type answers its declaration only, and refuses
	// a refresh.
	code, raw, _ = adminBytes(t, "GET", base+"/v1/admin/apps/echoapp/objects", bearer, "", nil)
	var plain objectsAnswer
	if err := json.Unmarshal(raw, &plain); err != nil || code != http.StatusOK ||
		plain.Type != "" || plain.Pinned != nil || plain.Declared != nil || plain.Listing != nil || len(plain.Objects) != 0 {
		t.Errorf("echoapp = %d %s", code, raw)
	}
	assertSchema(t, "ObjectList", raw)
	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/objects/refresh", bearer, nil, &bad); code != http.StatusConflict ||
		bad["error"] != "echoapp pins no object type, so there is nothing to list. Pin its objects first with strazactl apps objects draft echoapp, then publish the draft." {
		t.Errorf("refresh echoapp = %d %v", code, bad)
	}
	if code := adminReq(t, "GET", base+"/v1/admin/apps/nope/objects", bearer, nil, &bad); code != http.StatusNotFound || bad["error"] != "unknown server" {
		t.Errorf("unknown server = %d %v", code, bad)
	}

	// Delete, then delete again.
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/objects-demo/objects/credential", bearer, nil, nil); code != http.StatusNoContent {
		t.Errorf("delete the listing credential = %d", code)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/objects-demo/objects/credential", bearer, nil, &bad); code != http.StatusNotFound ||
		bad["error"] != "objects-demo has no listing credential stored, so there is nothing to remove." {
		t.Errorf("second delete = %d %v", code, bad)
	}
	gone := waitFor("the listing after the delete", func(o objectsAnswer) bool { return o.Listing.Outcome == store.ObjectsFailed })
	if gone.Listing.Reason != r1 || gone.Listing.Credential != "missing" || gone.Listing.Version != 1 || len(gone.Objects) != 5 {
		t.Errorf("after the delete = %+v with %d objects, want R1 and the last good list", gone.Listing, len(gone.Objects))
	}

	assertObjectsRecords(t, app, row.ID)
}

// assertObjectsRecords pins the admin records of TestAppObjectsAPI: one
// actorless apps.objects.list per outcome flip or change with the fields of
// the spec, one apps.secret.set and one apps.secret.remove of scope listing
// with the actor, and the listing token in none of them.
func assertObjectsRecords(t *testing.T, app *App, appID string) {
	t.Helper()
	var lists []map[string]any
	sets, removes := 0, 0
	for _, ev := range adminAuditEvents(t, app) {
		if raw, _ := json.Marshal(ev); strings.Contains(string(raw), "NEVER-LEAK") {
			t.Fatal("the listing token leaked into an admin record")
		}
		switch {
		case ev["action"] == "apps.objects.list":
			lists = append(lists, ev)
		case ev["scope"] == store.CredScopeListing && ev["action"] == "apps.secret.set":
			sets++
		case ev["scope"] == store.CredScopeListing && ev["action"] == "apps.secret.remove":
			removes++
		default:
			continue
		}
		if ev["action"] != "apps.objects.list" && (ev["actor"] != "kim" || ev["role"] != "" || ev["app"] != "objects-demo") {
			t.Errorf("credential record = %v", ev)
		}
	}
	if sets != 1 || removes != 1 {
		t.Errorf("listing credential records: set %d remove %d, want one each", sets, removes)
	}
	if len(lists) != 3 {
		t.Fatalf("apps.objects.list records = %v, want the failure, the good listing and the failure after the delete", lists)
	}
	common := map[string]any{"action": "apps.objects.list", "target": appID, "app": "objects-demo", "tool": "repo_list", "type": "repo"}
	for i, rec := range lists {
		for k, v := range common {
			if rec[k] != v {
				t.Errorf("record %d %s = %v, want %v", i, k, rec[k], v)
			}
		}
		for _, k := range []string{"actor", "actorId", "actorVia"} {
			if _, ok := rec[k]; ok {
				t.Errorf("record %d carries %s: %v", i, k, rec)
			}
		}
	}
	if f := lists[0]; f["outcome"] != "failed" || f["count"] != float64(0) || f["version"] != float64(0) || !strings.HasPrefix(fmt.Sprint(f["reason"]), "objects-demo has no listing credential") {
		t.Errorf("failed record = %v", f)
	}
	if o := lists[1]; o["outcome"] != "ok" || o["count"] != float64(5) || o["version"] != float64(1) || o["joined"] != float64(5) || o["left"] != float64(0) {
		t.Errorf("ok record = %v", o)
	}
	if f := lists[2]; f["outcome"] != "failed" || f["count"] != float64(5) || f["version"] != float64(1) {
		t.Errorf("failed record after the delete = %v, want the last good list's count and version", f)
	}
}

// assertSchema validates one answer against its openapi schema.
func assertSchema(t *testing.T, schema string, raw []byte) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v in %s", schema, err, raw)
	}
	if err := openAPISchema(t, schema).Validate(inst); err != nil {
		t.Errorf("%s does not validate %s: %v", schema, raw, err)
	}
}

// TestListingCredentialRefusal pins which servers keep a listing
// credential and the sentence each other one answers.
func TestListingCredentialRefusal(t *testing.T) {
	pinned := &manager.ObjectsSpec{Type: "repo", List: manager.ObjectListSpec{Tool: "repo_list"}}
	mf := func(runtime, kind string, objects *manager.ObjectsSpec) manager.Manifest {
		m := manager.Manifest{Straza: manager.Extensions{Runtime: manager.RuntimeSpec{Kind: runtime}, Objects: objects}}
		if kind != manager.CredentialNone {
			m.Straza.Credential = &manager.CredentialSpec{Kind: kind}
		}
		return m
	}
	cases := []struct {
		name string
		m    manager.Manifest
		want string
	}{
		{name: "remote static", m: mf(manager.RuntimeRemote, manager.CredentialStatic, pinned)},
		{name: "remote oauth", m: mf(manager.RuntimeRemote, manager.CredentialOAuth, pinned)},
		{name: "no pinned type", m: mf(manager.RuntimeRemote, manager.CredentialStatic, nil),
			want: "demo pins no object type, so a listing credential has no use. Pin its objects first with strazactl apps objects draft demo, then publish the draft."},
		{name: "command runtime", m: mf(manager.RuntimeCommand, manager.CredentialStatic, pinned),
			want: "demo runs as one process with one identity, so Straza lists its objects with that identity and keeps no listing credential for it."},
		{name: "oci runtime", m: mf(manager.RuntimeOCI, manager.CredentialNone, pinned),
			want: "demo runs as one process with one identity, so Straza lists its objects with that identity and keeps no listing credential for it."},
		{name: "remote without a credential", m: mf(manager.RuntimeRemote, manager.CredentialNone, pinned),
			want: "demo takes no credential, so Straza lists its objects without one and keeps no listing credential for it."},
	}
	for _, tc := range cases {
		if got := listingCredentialRefusal("demo", tc.m); got != tc.want {
			t.Errorf("%s: %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestAppObjectsHandPinnedFaults pins that the objects answer of a server
// whose tools carry no marks, pinned by hand, names each pinned argument
// the live input schema no longer requires as a string, and nothing for an
// argument the schema keeps.
func TestAppObjectsHandPinnedFaults(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	str := map[string]any{"type": "string"}
	schema := func(arg string) map[string]any {
		return map[string]any{"type": "object", "properties": map[string]any{arg: str}, "required": []any{arg}}
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "plainrepos", Version: "1.0.0"}, nil)
	done := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}
	srv.AddTool(&mcp.Tool{Name: "repo_list", InputSchema: map[string]any{"type": "object"}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, done)
	srv.AddTool(&mcp.Tool{Name: "pr_create", InputSchema: schema("repo")}, done)
	srv.AddTool(&mcp.Tool{Name: "pr_close", InputSchema: schema("repository")}, done)
	up := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(up.Close)
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: plainrepos}
server: {name: straza.test/plainrepos, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  objects:
    type: repo
    list: {tool: repo_list}
    tools:
      pr_create: {argument: repo}
      pr_close: {argument: repo}
`, up.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.manager.Install(context.Background(), mf, store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := app.manager.LiveInventory("plainrepos"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plainrepos never listed its tools")
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/apps/plainrepos/objects", nil)
	req.SetPathValue("id", "plainrepos")
	w := httptest.NewRecorder()
	app.handleAppObjects(w, req)
	var out objectsAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil {
		t.Fatalf("GET objects = %d %s", w.Code, w.Body)
	}
	cases := []struct {
		tool  string
		fault bool
	}{
		{"pr_create", false},
		{"pr_close", true},
	}
	for _, tc := range cases {
		want := tc.tool + " has no argument repo that is a required top-level string in its input schema, so Straza cannot read the repo a call names. Name the argument that carries the repo, or pin the reach all."
		if got := slices.Contains(out.DeclaredFaults, want); got != tc.fault {
			t.Errorf("%s: fault shown %v, want %v, in %q", tc.tool, got, tc.fault, out.DeclaredFaults)
		}
	}
	if out.Declared != nil || len(out.DeclaredFaults) != 1 {
		t.Errorf("declared = %+v, faults = %q; want no declared block and the one fault", out.Declared, out.DeclaredFaults)
	}
}
