package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/manager"
)

// objectsDraftServer serves the objects draft route of app behind the
// server admin guard, as the route table registers it.
func objectsDraftServer(t *testing.T, app *App) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/admin/apps/{id}/objects/draft", app.requireServerAdmin(app.handleAppObjectsDraft))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestAppObjectsDraft pins the pre-fill route: a server whose tools carry
// no mark, one that is not running and one whose marks break a rule are
// refused with the sentence that says what to do; a server whose pinned
// block already matches its marks gets nothing to draft; and otherwise one
// draft of the api door opens whose server put is the live manifest with
// the declared block, keeping the pinned plural and every pinned mark
// stricter than the server's.
func TestAppObjectsDraft(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	route := objectsDraftServer(t, app)
	twoListers, _ := objectsUpstream(t, map[string]mcp.Meta{"whoami": {"ai.straza/lists-objects": map[string]any{"type": "repo"}}})
	marked, _ := objectsUpstream(t, nil)
	plain := startEchoUpstream(t)

	install := func(name, url, block string) {
		t.Helper()
		body := strings.Replace(objectsManifest(url, block), "name: repos}", "name: "+name+"}", 1)
		var out map[string]any
		if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(body), &out); code != http.StatusCreated {
			t.Fatalf("install %s = %d %v", name, code, out)
		}
		waitAppStatus(t, base, bearer, name, "running")
	}
	install("plain", plain.URL, "")
	install("faulty", twoListers.URL, "")
	install("stopped", marked.URL, "")
	install("unpinned", marked.URL, "")
	install("matching", marked.URL, pinnedRepos)
	install("stricter", marked.URL, `  objects:
    type: repo
    plural: repositories
    list: {tool: repo_list}
    tools:
      repo_read: all
`)
	if code := adminReq(t, "POST", base+"/v1/admin/apps/stopped/disable", bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("disable = %d", code)
	}

	refusals := []struct {
		app, want string
		code      int
	}{
		{"nothing", "unknown server", http.StatusNotFound},
		{"plain", "plain declares no objects: none of its tools carries an ai.straza/object mark. Write straza.objects in its manifest by hand, or ask the server's author to mark its tools.", http.StatusConflict},
		{"stopped", "stopped is not running, so Straza cannot read its tool marks. Start it, then draft again.", http.StatusConflict},
		{"faulty", "faulty marks 2 listing tools: repo_list, whoami. A server has one listing tool, so its author must keep the ai.straza/lists-objects mark on one of them. Fix the marks on the server, or write straza.objects in its manifest by hand.", http.StatusConflict},
	}
	for _, tc := range refusals {
		var out map[string]any
		if code := adminReq(t, "POST", route+"/v1/admin/apps/"+tc.app+"/objects/draft", bearer, nil, &out); code != tc.code || out["error"] != tc.want {
			t.Errorf("%s: draft = %d %v, want %d %q", tc.app, code, out, tc.code, tc.want)
		}
	}

	var same map[string]any
	if code := adminReq(t, "POST", route+"/v1/admin/apps/matching/objects/draft", bearer, nil, &same); code != http.StatusOK ||
		same["unchanged"] != true || same["sentence"] != "matching's pinned repos already match its marks. Nothing to draft." {
		t.Errorf("matching: draft = %d %v, want 200 unchanged", code, same)
	}

	declared := manager.ObjectsSpec{Type: "repo",
		List: manager.ObjectListSpec{Tool: "repo_list", Attributes: []manager.ObjectAttribute{{Name: "area", SetBy: "org"}}},
		Tools: map[string]manager.ToolReach{
			"repo_read": {Reach: manager.ReachArgument, Argument: "repo"},
			"whoami":    {Reach: manager.ReachNone},
		}}
	stricter := declared
	stricter.Plural = "repositories"
	stricter.Tools = map[string]manager.ToolReach{"repo_read": {Reach: manager.ReachAll}, "whoami": {Reach: manager.ReachNone}}
	for name, want := range map[string]manager.ObjectsSpec{"unpinned": declared, "stricter": stricter} {
		var out struct {
			Draft   draftPayload `json:"draft"`
			Verdict struct {
				Refused []map[string]any `json:"refused"`
			} `json:"verdict"`
		}
		if code := adminReq(t, "POST", route+"/v1/admin/apps/"+name+"/objects/draft", bearer, nil, &out); code != http.StatusCreated || len(out.Verdict.Refused) > 0 {
			t.Errorf("%s: draft = %d refused %v, want 201 with nothing refused", name, code, out.Verdict.Refused)
			continue
		}
		d := out.Draft
		plural := want.PluralWord()
		if d.Door != "api" || d.Note != "Pins the "+plural+" that "+name+" declares in its tool marks." || len(d.Items) != 1 ||
			d.Items[0].Kind != "App" || d.Items[0].Name != name || d.Items[0].Op != "put" {
			t.Errorf("%s: draft = %+v, want one App put of the api door with the note", name, d)
			continue
		}
		mf, err := manager.Parse([]byte(d.Items[0].Doc))
		if err != nil {
			t.Errorf("%s: the drafted manifest does not parse: %v", name, err)
			continue
		}
		if mf.Straza.Objects == nil || !reflect.DeepEqual(*mf.Straza.Objects, want) {
			t.Errorf("%s: drafted objects = %+v, want %+v", name, mf.Straza.Objects, want)
		}
		row, err := app.store.Apps().GetByName(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		live, _ := manager.FromJSON(row.Manifest)
		live.Straza.Objects, mf.Straza.Objects = nil, nil
		if a, b := objectsTestJSON(t, live), objectsTestJSON(t, mf); a != b {
			t.Errorf("%s: the drafted manifest differs from live beyond objects:\n%s\n%s", name, b, a)
		}
	}
}

func objectsTestJSON(t *testing.T, m manager.Manifest) string {
	t.Helper()
	s, err := m.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
