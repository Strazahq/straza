package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestAppsInstallOverPausedApp: a change posted to a paused server answers
// from the stored row, stopped and paused, and the list agrees; enable then
// starts the server with the manifest the change stored.
func TestAppsInstallOverPausedApp(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)

	var installed appPayload
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(echoManifest(up.URL)), &installed); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	waitAppStatus(t, base, bearer, "echoapp", "running")
	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/disable", bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("disable = %d", code)
	}

	changed := strings.Replace(echoManifest(up.URL), `version: "1.0.0"`, `version: "2.0.0"`, 1)
	var answer appPayload
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(changed), &answer); code != http.StatusCreated {
		t.Fatalf("change to a paused server = %d", code)
	}
	if answer.ID != installed.ID || answer.Name != "echoapp" || answer.Version != "2.0.0" || answer.Runtime != "remote" ||
		answer.Status != "stopped" || answer.Source != "api" || !answer.Paused || len(answer.Tools) != 0 {
		t.Errorf("answer = %+v, want the stored row of echoapp at 2.0.0, stopped and paused, with no tools", answer)
	}

	var apps []appPayload
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	if len(apps) != 1 || apps[0].Status != "stopped" || !apps[0].Paused || apps[0].Version != "2.0.0" || len(apps[0].Tools) != 0 {
		t.Errorf("list = %+v, want one stopped and paused row at 2.0.0 with no tools", apps)
	}

	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/enable", bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("enable = %d", code)
	}
	waitAppStatus(t, base, bearer, "echoapp", "running")
	apps = nil
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
		t.Fatalf("list after enable = %d", code)
	}
	if len(apps) != 1 || apps[0].Version != "2.0.0" || apps[0].Paused {
		t.Errorf("list after enable = %+v, want echoapp at 2.0.0 and not paused", apps)
	}
}

// TestAppsInstallAndRemoveTakeAFileDefinedServer: a file in the watched
// apps directory owns nothing, so the install lane changes a server a
// present file names and the removal lane removes it, each with its
// record, and no answer names the file.
func TestAppsInstallAndRemoveTakeAFileDefinedServer(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(echoManifest(up.URL)), nil); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	path := filepath.Join(app.cfg.AppsDir(), "echoapp.yaml")
	if err := os.WriteFile(path, []byte(strings.Replace(echoManifest(up.URL), "version: \"1.0.0\"", "version: \"2.0.0\"", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	app.watcher.Sweep(context.Background())

	var changed map[string]any
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(echoManifest(up.URL+"/two")), &changed); code != http.StatusCreated {
		t.Errorf("install over a server a file names = %d %v, want 201", code, changed)
	}
	var removed map[string]any
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/echoapp", bearer, nil, &removed); code != http.StatusOK {
		t.Errorf("removal of a server a file names = %d %v, want 200", code, removed)
	}
	installs, removals := 0, 0
	for _, ev := range adminAuditEvents(t, app) {
		switch ev["action"] {
		case "apps.install":
			installs++
		case "apps.remove":
			removals++
		}
	}
	if installs != 2 || removals != 1 {
		t.Errorf("apps.install and apps.remove records = %d and %d, want 2 and 1", installs, removals)
	}
	for _, answer := range []map[string]any{changed, removed} {
		if raw, _ := json.Marshal(answer); strings.Contains(string(raw), "defined by the file") {
			t.Errorf("an answer names the file: %s", raw)
		}
	}
}

// TestAppsInstallRecordsWhatChanged: every install that changes the
// manifest writes exactly one apps.install record with the actor; a first
// install says update false and names no paths, a change says update true
// with the sorted manifest paths that differ, the same manifest again
// writes no record, and no value from the manifest reaches the
// record.
func TestAppsInstallRecordsWhatChanged(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)

	manifest := func(metadata, url, limits string) string {
		return fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: echoapp%s}
server: {name: straza.test/echo, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  limits: {%s}
`, metadata, url, limits)
	}
	first := manifest("", up.URL, "timeoutSeconds: 30")
	second := manifest(", description: echo for the change record", up.URL+"?region=abc", "timeoutSeconds: 30, rps: 5")

	steps := []struct {
		name    string
		body    string
		update  bool
		changed []string // nil when the record must carry no changed key
		records int
	}{
		{name: "a first install", body: first, update: false, changed: nil, records: 1},
		{name: "a change", body: second, update: true, changed: []string{"metadata.description", "straza.limits.rps", "straza.runtime.remote.url"}, records: 2},
		{name: "the same manifest again", body: second, update: true, changed: []string{"metadata.description", "straza.limits.rps", "straza.runtime.remote.url"}, records: 2},
	}
	for _, st := range steps {
		if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(st.body), nil); code != http.StatusCreated {
			t.Fatalf("%s: install = %d", st.name, code)
		}
		var records []map[string]any
		for _, ev := range adminAuditEvents(t, app) {
			if ev["action"] == "apps.install" {
				records = append(records, ev)
			}
		}
		if len(records) != st.records {
			t.Fatalf("%s: apps.install records = %d, want %d", st.name, len(records), st.records)
		}
		rec := records[st.records-1]
		if rec["actor"] != "kim" || rec["app"] != "echoapp" || rec["runtime"] != "remote" || rec["update"] != st.update {
			t.Errorf("%s: record = %v, want actor kim, app echoapp, runtime remote, update %v", st.name, rec, st.update)
		}
		got, has := rec["changed"]
		if st.changed == nil && has {
			t.Errorf("%s: record carries changed %v, want none", st.name, got)
		}
		if st.changed != nil {
			paths, ok := got.([]any)
			var names []string
			for _, p := range paths {
				s, _ := p.(string)
				names = append(names, s)
			}
			if !ok || strings.Join(names, ",") != strings.Join(st.changed, ",") {
				t.Errorf("%s: changed = %#v, want %v", st.name, got, st.changed)
			}
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{"region=abc", strings.TrimPrefix(up.URL, "http://"), "echo for the change record"} {
			if strings.Contains(string(raw), value) {
				t.Errorf("%s: record carries the manifest value %q: %s", st.name, value, raw)
			}
		}
	}
}

// TestAppsInstallRecordsAFailedStart: an install whose row is stored but
// whose runtime fails to start answers 500 and still writes its one
// apps.install record, because the stored manifest changed.
func TestAppsInstallRecordsAFailedStart(t *testing.T) {
	// Serial: it sets PATH and TMPDIR, which t.Setenv refuses in a parallel test.
	if runtime.GOOS == "windows" {
		t.Skip("the docker stand-in is a unix executable")
	}
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")

	// A docker stand-in on PATH lets the oci runtime past its docker check,
	// and a TMPDIR that does not exist makes it fail to start: it cannot
	// create its cidfile directory.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", missing)

	manifest := `
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: ocifail}
server: {name: straza.test/ocifail, version: "1.0.0"}
straza:
  runtime:
    kind: oci
    oci: {image: img, sandbox: none}
`
	var answer map[string]any
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(manifest), &answer); code != http.StatusInternalServerError {
		t.Fatalf("install with a runtime that cannot start = %d %v, want 500", code, answer)
	}
	var records []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "apps.install" {
			records = append(records, ev)
		}
	}
	if len(records) != 1 || records[0]["app"] != "ocifail" || records[0]["actor"] != "kim" || records[0]["update"] != false {
		t.Errorf("apps.install records = %v, want one for ocifail by kim with update false", records)
	}
}

// startTwoToolUpstream serves an MCP server with two tools, so a manifest
// can expose one of them.
func startTwoToolUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "1.0.0"}, nil)
	type args struct {
		Text string `json:"text"`
	}
	for _, name := range []string{"echo", "sum"} {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: name},
			func(_ context.Context, _ *mcp.CallToolRequest, a args) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: a.Text}}}, nil, nil
			})
	}
	up := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(up.Close)
	return up
}

// TestAppsListOffersEveryTool: the list carries every tool the server
// itself lists as offered, while tools stays the exposed set, and a stopped
// server offers nothing.
func TestAppsListOffersEveryTool(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startTwoToolUpstream(t)

	manifest := fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: echoapp}
server: {name: straza.test/echo, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  exposure: {tools: [echo]}
`, up.URL)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(manifest), nil); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	waitAppStatus(t, base, bearer, "echoapp", "running")

	var apps []appPayload
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	if len(apps) != 1 || strings.Join(apps[0].Tools, ",") != "echo" || strings.Join(apps[0].Offered, ",") != "echo,sum" {
		t.Fatalf("list = %+v, want tools echo and offered echo,sum", apps)
	}

	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/disable", bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("disable = %d", code)
	}
	apps = nil
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
		t.Fatalf("list after disable = %d", code)
	}
	if len(apps) != 1 || len(apps[0].Offered) != 0 {
		t.Errorf("stopped server offers %v, want nothing", apps[0].Offered)
	}
}

// objectsUpstream is an MCP server whose tools carry the ai.straza/* marks
// of a repo server, with a count of the initialize requests it answered,
// which a server start sends. marks replaces a tool's _meta by name.
func objectsUpstream(t *testing.T, marks map[string]mcp.Meta) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "objects", Version: "1.0.0"}, nil)
	meta := map[string]mcp.Meta{
		"repo_list": {"ai.straza/lists-objects": map[string]any{"type": "repo", "attributes": []any{map[string]any{"name": "area", "setBy": "org"}}}},
		"repo_read": {"ai.straza/object": map[string]any{"type": "repo", "argument": "repo"}},
		"whoami":    {"ai.straza/object": map[string]any{"type": "repo", "reach": "none"}},
	}
	for name, m := range marks {
		meta[name] = m
	}
	type listArgs struct {
		Cursor string `json:"cursor,omitempty"`
	}
	type repoArgs struct {
		Repo string `json:"repo"`
	}
	type noArgs struct{}
	answer := func(text string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "repo_list", Description: "list repos", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, Meta: meta["repo_list"]},
		func(context.Context, *mcp.CallToolRequest, listArgs) (*mcp.CallToolResult, any, error) {
			return answer("[]"), nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "repo_read", Description: "read a repo", Meta: meta["repo_read"]},
		func(_ context.Context, _ *mcp.CallToolRequest, a repoArgs) (*mcp.CallToolResult, any, error) {
			return answer(a.Repo), nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "whoami", Description: "who am I", Meta: meta["whoami"]},
		func(context.Context, *mcp.CallToolRequest, noArgs) (*mcp.CallToolResult, any, error) {
			return answer("me"), nil, nil
		})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	var inits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if strings.Contains(string(raw), `"initialize"`) {
				inits.Add(1)
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	return up, &inits
}

// objectsManifest is the manifest of the objects upstream at url, with
// extra YAML under straza: indented two spaces.
func objectsManifest(url, extra string) string {
	return fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: repos}
server: {name: straza.test/repos, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
%s`, url, extra)
}

const pinnedRepos = `  objects:
    type: repo
    list:
      tool: repo_list
      attributes: [{name: area, setBy: org}]
    tools:
      repo_read: {argument: repo}
      whoami: none
`

// TestAppsObjectsOnlyChangeKeepsTheServer pins that a change of the
// straza.objects block alone stores the manifest, writes apps.install with
// the straza.objects paths and keeps the running instance, whose view then
// carries the new block, while any other change still restarts it.
func TestAppsObjectsOnlyChangeKeepsTheServer(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	up, inits := objectsUpstream(t, nil)

	install := func(body string) {
		t.Helper()
		var out map[string]any
		if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(body), &out); code != http.StatusCreated {
			t.Fatalf("install = %d %v", code, out)
		}
	}
	install(objectsManifest(up.URL, ""))
	waitAppStatus(t, base, bearer, "repos", "running")
	started := inits.Load()

	install(objectsManifest(up.URL, pinnedRepos))
	var records []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "apps.install" {
			records = append(records, ev)
		}
	}
	want := "straza.objects.list.attributes,straza.objects.list.tool,straza.objects.tools.repo_read.argument,straza.objects.tools.whoami,straza.objects.type"
	if len(records) != 2 || fmt.Sprint(records[1]["changed"]) != "["+strings.ReplaceAll(want, ",", " ")+"]" {
		t.Fatalf("apps.install records = %v, want a second one changing %s", records, want)
	}
	row, err := app.store.Apps().GetByName(context.Background(), "repos")
	if err != nil || !strings.Contains(row.Manifest, `"objects":{"type":"repo"`) {
		t.Fatalf("stored manifest = %s, %v, want the objects block", row.Manifest, err)
	}
	time.Sleep(300 * time.Millisecond)
	if inits.Load() != started {
		t.Errorf("an objects-only change restarted the server: %d initialize requests, want %d", inits.Load(), started)
	}
	view, ok := app.manager.View("repos")
	if !ok || view.Manifest.Straza.Objects == nil || view.Manifest.Straza.Objects.Type != "repo" {
		t.Errorf("the running instance shows objects %+v, want the pinned repo block", view.Manifest.Straza.Objects)
	}

	install(objectsManifest(up.URL, pinnedRepos+"  limits: {rps: 5}\n"))
	waitAppStatus(t, base, bearer, "repos", "running")
	deadline := time.Now().Add(5 * time.Second)
	for inits.Load() == started && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if inits.Load() == started {
		t.Errorf("a limits change did not restart the server, so this test cannot see a restart")
	}
}
