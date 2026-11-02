package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// objectsServer is the admin surface apps objects walks: the object list of
// each server by name, the refresh and the draft pre-fill. Timestamps are
// made relative to now, so the ages the status line prints are stable.
func objectsServer(t *testing.T) (*httptest.Server, *appsServer) {
	t.Helper()
	ago := func(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }
	pinned := `"type":"repo","plural":"repos","attributes":[{"name":"area","setBy":"org"}],` +
		`"pinned":{"type":"repo","list":{"tool":"repo_list","attributes":[{"name":"area","setBy":"org"}]}},"declared":null,"declaredFaults":[]`
	two := `"objects":[{"value":"Strazahq/straza","id":"9012331","title":"straza","attributes":{"area":"product"},"new":false,"coveredBy":["objects-demo-tools"]},` +
		`{"value":"Strazahq/site","id":"9012332","title":"","attributes":{"area":["web","docs"]},"new":true,"coveredBy":[]}]`
	declared := `"declared":{"type":"repo","list":{"tool":"repo_list","attributes":[{"name":"area","setBy":"org"}]},` +
		`"tools":{"repo_read":{"argument":"repo"},"search_code":"all"}}`
	lists := map[string]string{
		"objects-demo": `{"app":"objects-demo",` + pinned + `,"drift":false,"listing":{"tool":"repo_list","credential":"set","outcome":"ok",` +
			`"version":3,"count":10,"joined":2,"left":0,"listedAt":"` + ago(190*time.Second) + `"},` + two + `,"total":10,"next":"Strazahq/site"}`,
		"first-demo": `{"app":"first-demo",` + pinned + `,"drift":false,"listing":{"tool":"repo_list","credential":"not-needed","outcome":"ok",` +
			`"version":1,"count":2,"joined":2,"left":0,"listedAt":"` + ago(190*time.Second) + `"},` + two + `,"total":2}`,
		"calm-demo": `{"app":"calm-demo",` + pinned + `,"drift":false,"listing":{"tool":"repo_list","credential":"set","outcome":"ok",` +
			`"version":4,"count":2,"joined":0,"left":0,"listedAt":"` + ago(2*time.Hour+5*time.Minute) + `"},` + two + `,"total":2}`,
		"churn-demo": `{"app":"churn-demo",` + pinned + `,"drift":true,"listing":{"tool":"repo_list","credential":"set","outcome":"ok",` +
			`"version":5,"count":1,"joined":1,"left":2,"listedAt":"` + ago(10*time.Second) + `"},"objects":[],"total":1}`,
		"broken-demo": `{"app":"broken-demo",` + pinned + `,"drift":false,"listing":{"tool":"repo_list","credential":"set","outcome":"failed",` +
			`"reason":"repo_list answered an error: upstream 500. Check broken-demo's logs on its Activity tab.",` +
			`"version":2,"count":2,"joined":0,"left":0,"listedAt":"` + ago(2*time.Hour+5*time.Minute) + `"},` + two + `,"total":2}`,
		"fresh-demo": `{"app":"fresh-demo",` + pinned + `,"drift":false,"listing":{"tool":"repo_list","credential":"missing","outcome":"never",` +
			`"version":0,"count":0,"joined":0,"left":0},"objects":[],"total":0}`,
		"plain-tools": `{"app":"plain-tools","type":"","plural":"","attributes":[],"pinned":null,` + declared +
			`,"declaredFaults":[],"drift":false,"listing":null,"objects":[],"total":0}`,
		"faulty-tools": `{"app":"faulty-tools","type":"","plural":"","attributes":[],"pinned":null,` + declared +
			`,"declaredFaults":["repo_list and tag_list both carry ai.straza/lists-objects, so Straza cannot tell which tool lists the repos."],` +
			`"drift":false,"listing":null,"objects":[],"total":0}`,
		"bare-tools": `{"app":"bare-tools","type":"","plural":"","attributes":[],"pinned":null,"declared":null,"declaredFaults":[],` +
			`"drift":false,"listing":null,"objects":[],"total":0}`,
		"busy-demo": `{"app":"busy-demo",` + pinned + `,"drift":false,"listing":{"tool":"repo_list","credential":"set","outcome":"ok",` +
			`"version":1,"count":0,"joined":0,"left":0,"listedAt":"` + ago(20*time.Second) + `"},"objects":[],"total":0}`,
	}
	s := &appsServer{}
	mux := http.NewServeMux()
	jsonOut := func(w http.ResponseWriter, code int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("GET /v1/admin/apps/{id}/objects", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		body, ok := lists[r.PathValue("id")]
		if !ok {
			jsonOut(w, http.StatusNotFound, `{"error":"unknown server"}`)
			return
		}
		jsonOut(w, http.StatusOK, body)
	})
	mux.HandleFunc("POST /v1/admin/apps/{id}/objects/refresh", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		switch app := r.PathValue("id"); app {
		case "busy-demo":
			jsonOut(w, http.StatusTooManyRequests, `{"error":"busy-demo was listed 20 seconds ago. Refresh again in 40 seconds."}`)
		case "plain-tools":
			jsonOut(w, http.StatusConflict, `{"error":"plain-tools pins no object type, so there is nothing to list. `+
				`Pin its objects first with strazactl apps objects draft plain-tools, then publish the draft."}`)
		default:
			jsonOut(w, http.StatusAccepted, `{"app":"`+app+`","dueAt":"`+ago(0)+`"}`)
		}
	})
	mux.HandleFunc("POST /v1/admin/apps/{id}/objects/draft", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		switch r.PathValue("id") {
		case "objects-demo":
			jsonOut(w, http.StatusOK, `{"unchanged":true,"sentence":"objects-demo's pinned repos already match its marks. Nothing to draft."}`)
		case "bare-tools":
			jsonOut(w, http.StatusConflict, `{"error":"bare-tools declares no objects: none of its tools carries an ai.straza/object mark. `+
				`Write straza.objects in its manifest by hand, or ask the server's author to mark its tools."}`)
		default:
			jsonOut(w, http.StatusCreated, `{"draft":{"id":"d-7","revision":1,"state":"open","door":"api",`+
				`"items":[{"kind":"App","name":"plain-tools","op":"put","doc":"kind: App","existed":true}]},`+
				`"verdict":{"refused":[],"risks":[],"warnings":[],"unchecked":[],"passed":[],"info":[]}}`)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, s
}

// TestAppsObjects pins apps objects: the status line per listing state, the
// table with one column per attribute and the covering roles, the next page
// command, and what a server that pins no type prints instead.
func TestAppsObjects(t *testing.T) {
	const r1 = "fresh-demo has no listing credential, so Straza cannot list its repos. " +
		"Add one with strazactl apps secret set fresh-demo --listing, then refresh with strazactl apps objects refresh fresh-demo.\n"
	table := "VALUE            ID       TITLE   AREA       COVERED BY\n" +
		"Strazahq/straza  9012331  straza  product    objects-demo-tools\n" +
		"Strazahq/site    9012332  -       web, docs  no role\n"
	tests := []struct {
		name    string
		args    []string
		uri     string
		want    string
		wantErr string
	}{
		{name: "a changed listing with a next page", args: []string{"objects-demo", "--limit", "2"},
			uri: "/v1/admin/apps/objects-demo/objects?limit=2",
			want: "objects-demo lists its repos with repo_list. Last listing 3 minutes ago: 10 repos, 2 new.\n" + table +
				"More repos follow. Next page: strazactl apps objects objects-demo --after Strazahq/site --limit 2\n"},
		{name: "the first listing names no change", args: []string{"first-demo"},
			uri:  "/v1/admin/apps/first-demo/objects",
			want: "first-demo lists its repos with repo_list. Last listing 3 minutes ago: 2 repos.\n" + table},
		{name: "a listing that changed nothing", args: []string{"calm-demo"},
			uri:  "/v1/admin/apps/calm-demo/objects",
			want: "calm-demo lists its repos with repo_list. Last listing 2 hours ago: 2 repos, no change.\n" + table},
		{name: "joined and left with drift, past the last page", args: []string{"churn-demo", "--after", "Strazahq/zz"},
			uri: "/v1/admin/apps/churn-demo/objects?after=Strazahq%2Fzz",
			want: "churn-demo lists its repos with repo_list. Last listing less than a minute ago: 1 repo, 1 new, 2 gone.\n" +
				"churn-demo's marks differ from the pinned repos. Draft the change with strazactl apps objects draft churn-demo.\n" +
				"No repos after Strazahq/zz.\n"},
		{name: "a failed listing keeps the last good list", args: []string{"broken-demo"},
			uri: "/v1/admin/apps/broken-demo/objects",
			want: "broken-demo lists its repos with repo_list.\n" +
				"repo_list answered an error: upstream 500. Check broken-demo's logs on its Activity tab.\n" +
				"The last good list stays: 2 repos, listed 2 hours ago.\n" + table},
		{name: "never listed and no listing credential", args: []string{"fresh-demo"},
			uri:  "/v1/admin/apps/fresh-demo/objects",
			want: "fresh-demo lists its repos with repo_list. It has not been listed yet.\n" + r1},
		{name: "no pinned type prints what the marks declare", args: []string{"plain-tools"},
			uri: "/v1/admin/apps/plain-tools/objects",
			want: "plain-tools pins no object type. Its tool marks declare this block:\n" +
				"straza:\n  objects:\n    type: repo\n    list:\n      tool: repo_list\n      attributes:\n" +
				"        - name: area\n          setBy: org\n    tools:\n      repo_read:\n        argument: repo\n      search_code: all\n" +
				"Pin it with strazactl apps objects draft plain-tools, then publish the draft.\n"},
		{name: "marks with a fault name it and the fix", args: []string{"faulty-tools"},
			uri: "/v1/admin/apps/faulty-tools/objects",
			want: "faulty-tools pins no object type. Its tool marks declare this block:\n" +
				"straza:\n  objects:\n    type: repo\n    list:\n      tool: repo_list\n      attributes:\n" +
				"        - name: area\n          setBy: org\n    tools:\n      repo_read:\n        argument: repo\n      search_code: all\n" +
				"repo_list and tag_list both carry ai.straza/lists-objects, so Straza cannot tell which tool lists the repos.\n" +
				"Fix the marks on the server, or write straza.objects in its manifest by hand.\n"},
		{name: "no type and no marks", args: []string{"bare-tools"},
			uri: "/v1/admin/apps/bare-tools/objects",
			want: "bare-tools pins no object type and none of its tools carries an ai.straza/object mark. " +
				"Write straza.objects in its manifest by hand, or ask the server's author to mark its tools.\n"},
		{name: "an unknown server is the server's sentence", args: []string{"ghost"},
			uri: "/v1/admin/apps/ghost/objects", wantErr: "unknown server"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, s := objectsServer(t)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, onToken(t, srv.URL), append([]string{"apps", "objects"}, tc.args...)...)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("%v: %v", tc.args, err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if stdout != tc.want {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout, tc.want)
			}
			s.mu.Lock()
			reqs := s.reqs
			s.mu.Unlock()
			if len(reqs) != 1 || reqs[0].uri != tc.uri {
				t.Errorf("wire = %+v, want one GET %s", reqs, tc.uri)
			}
		})
	}
}

// TestAppsObjectsJSON pins --json: the server's answer, indented, and no
// status line around it.
func TestAppsObjectsJSON(t *testing.T) {
	srv, _ := objectsServer(t)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runCLI(t, onToken(t, srv.URL), "apps", "objects", "objects-demo", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "{\n  \"app\": \"objects-demo\",\n  \"type\": \"repo\",") || strings.Contains(stdout, "lists its repos") {
		t.Errorf("stdout = %s", stdout)
	}
}

// TestAppsObjectsRefreshAndDraft pins refresh, which reads the plural first
// and then queues a listing, and draft, which opens a draft of the declared
// block or says why not. Every refusal is the server's own sentence.
func TestAppsObjectsRefreshAndDraft(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		want     string
		wantErr  string
		wantCode int
		wire     []string
	}{
		{name: "refresh queues a listing", args: []string{"refresh", "objects-demo"},
			want: "Queued: objects-demo lists its repos within 30 seconds.\n",
			wire: []string{"GET /v1/admin/apps/objects-demo/objects?limit=1", "POST /v1/admin/apps/objects-demo/objects/refresh"}},
		{name: "refresh too soon is the server's sentence", args: []string{"refresh", "busy-demo"},
			wantErr: "busy-demo was listed 20 seconds ago. Refresh again in 40 seconds.",
			wire:    []string{"GET /v1/admin/apps/busy-demo/objects?limit=1", "POST /v1/admin/apps/busy-demo/objects/refresh"}},
		{name: "refresh of a server with no type is the server's sentence", args: []string{"refresh", "plain-tools"},
			wantErr: "plain-tools pins no object type, so there is nothing to list. Pin its objects first with strazactl apps objects draft plain-tools, then publish the draft.",
			wire:    []string{"GET /v1/admin/apps/plain-tools/objects?limit=1", "POST /v1/admin/apps/plain-tools/objects/refresh"}},
		{name: "refresh of an unknown server stops at the read", args: []string{"refresh", "ghost"},
			wantErr: "unknown server", wire: []string{"GET /v1/admin/apps/ghost/objects?limit=1"}},
		{name: "draft opens a draft with its console page", args: []string{"draft", "plain-tools"},
			want: "Created draft d-7 with 1 document. Nothing changes until a person publishes it.\n" +
				"  ~   App/plain-tools\n" +
				"Publish draft d-7 in the console under Drafts: {server}/console/drafts/d-7\n",
			wire: []string{"POST /v1/admin/apps/plain-tools/objects/draft"}},
		{name: "draft with nothing to change says so", args: []string{"draft", "objects-demo"},
			want: "objects-demo's pinned repos already match its marks. Nothing to draft.\n",
			wire: []string{"POST /v1/admin/apps/objects-demo/objects/draft"}},
		{name: "draft refused is the server's sentence and exits 1", args: []string{"draft", "bare-tools"},
			wantErr: "bare-tools declares no objects: none of its tools carries an ai.straza/object mark. " +
				"Write straza.objects in its manifest by hand, or ask the server's author to mark its tools.",
			wantCode: 1, wire: []string{"POST /v1/admin/apps/bare-tools/objects/draft"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, s := objectsServer(t)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, onToken(t, srv.URL), append([]string{"apps", "objects"}, tc.args...)...)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("%v: %v", tc.args, err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			var coded exitCodeErr
			if tc.wantCode != 0 && (!errors.As(err, &coded) || coded.code != tc.wantCode) {
				t.Errorf("exit code of %v, want %d", err, tc.wantCode)
			}
			if want := strings.ReplaceAll(tc.want, "{server}", srv.URL); stdout != want {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
			}
			var wire []string
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, r := range s.reqs {
				wire = append(wire, fmt.Sprintf("%s %s", r.method, r.uri))
			}
			if strings.Join(wire, "\n") != strings.Join(tc.wire, "\n") {
				t.Errorf("wire = %q, want %q", wire, tc.wire)
			}
		})
	}
}

// TestAgeWords pins the age words of the status line.
func TestAgeWords(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{d: 0, want: "less than a minute ago"},
		{d: 59 * time.Second, want: "less than a minute ago"},
		{d: time.Minute, want: "1 minute ago"},
		{d: 59 * time.Minute, want: "59 minutes ago"},
		{d: time.Hour, want: "1 hour ago"},
		{d: 47 * time.Hour, want: "47 hours ago"},
		{d: 48 * time.Hour, want: "2 days ago"},
		{d: -time.Minute, want: "less than a minute ago"},
	} {
		if got := ageWords(tc.d); got != tc.want {
			t.Errorf("ageWords(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
