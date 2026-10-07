package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jsonServer answers each GET path with a fixed body, the way strazad's
// encoder writes it: one line, HTML characters escaped, a trailing newline.
func jsonServer(t *testing.T, bodies map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/checkin" {
			_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
			return
		}
		body, ok := bodies[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such route in the test server"}`))
			return
		}
		_, _ = w.Write([]byte(body + "\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// futureFields is a member no ctl type knows, with number literals a float
// round trip would change and an escaped HTML character.
const futureFields = `"future":{"n":[1,2.50,12345678901234567890],"note":"a <b> tag"}`

// indented is what --json must print for body: the same document, indented.
func indented(t *testing.T, body string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(body), "", "  "); err != nil {
		t.Fatal(err)
	}
	return buf.String() + "\n"
}

// TestJSONPrintsTheServerAnswer pins --json on the five inventory reads: the
// server's answer, indented, is the only thing on stdout, with every member
// kept, number literals and escapes included, so openapi stays the schema.
// A token run keeps its notice on stderr.
func TestJSONPrintsTheServerAnswer(t *testing.T) {
	tools := `[{"id":"demo-tools:echo","app":"demo-tools","name":"echo","description":"Echoes",` + futureFields + `},` +
		`{"id":"midpoint:get_user","app":"midpoint","name":"get_user","description":"Reads a user"},` +
		`{"id":"demo-tools:get-sum","app":"demo-tools","name":"get-sum","description":"Adds"}]`
	bodies := map[string]string{
		"GET /v1/admin/apps":     `[{"id":"a1","name":"demo-tools","status":"running","manifest":{"kind":"App"},` + futureFields + `}]`,
		"GET /v1/admin/tools":    tools,
		"GET /v1/admin/roles":    `[{"id":"r1","name":"dev-tools","kind":"application",` + futureFields + `}]`,
		"GET /v1/admin/bindings": `[{"id":"b1","app":"demo-tools","role":"dev-tools","tools":["*"],` + futureFields + `}]`,
		"GET /v1/admin/policies": `{"items":[{"id":"ps-1","name":"set-a","priority":0,"status":"active"}],"total":1,"limit":0,"offset":0,"roles":[],` + futureFields + `}`,
	}
	tests := []struct {
		name       string
		args       []string
		token      string
		wantStdout string
		wantStderr string
	}{
		{name: "apps list", args: []string{"apps", "list", "--json"}, wantStdout: indented(t, bodies["GET /v1/admin/apps"])},
		{name: "apps tools", args: []string{"apps", "tools", "--json"}, wantStdout: indented(t, tools)},
		{name: "roles list", args: []string{"roles", "list", "--json"}, wantStdout: indented(t, bodies["GET /v1/admin/roles"])},
		{name: "bindings list", args: []string{"bindings", "list", "--json"}, wantStdout: indented(t, bodies["GET /v1/admin/bindings"])},
		{name: "policy list", args: []string{"policy", "list", "--json"}, wantStdout: indented(t, bodies["GET /v1/admin/policies"])},
		{
			name: "apps tools of one server keeps its entries whole",
			args: []string{"apps", "tools", "--app", "demo-tools", "--json"},
			wantStdout: indented(t, `[{"id":"demo-tools:echo","app":"demo-tools","name":"echo","description":"Echoes",`+futureFields+`},`+
				`{"id":"demo-tools:get-sum","app":"demo-tools","name":"get-sum","description":"Adds"}]`),
		},
		{
			name:       "apps tools of an unknown server is an empty list and a sentence on stderr",
			args:       []string{"apps", "tools", "--app", "nosuch", "--json"},
			wantStdout: "[]\n",
			wantStderr: noToolsFor("nosuch") + "\n",
		},
		{
			name:       "a token run keeps stdout to the document",
			args:       []string{"roles", "list", "--json"},
			token:      "wat_abc",
			wantStdout: indented(t, bodies["GET /v1/admin/roles"]),
			wantStderr: "note: strazactl uses the admin API token in STRAZA_API_TOKEN for ",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := jsonServer(t, bodies)
			t.Setenv("STRAZA_SERVER", "")
			t.Setenv(apiTokenEnv, tc.token)
			stdout, stderr, err := runCLI(t, writeCreds(t, srv.URL), tc.args...)
			if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if stdout != tc.wantStdout {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout, tc.wantStdout)
			}
			if !json.Valid([]byte(stdout)) {
				t.Errorf("stdout is not one JSON document:\n%s", stdout)
			}
			if (tc.wantStderr == "" && stderr != "") || !strings.HasPrefix(stderr, tc.wantStderr) {
				t.Errorf("stderr = %q, want %q", stderr, tc.wantStderr)
			}
		})
	}
}

// TestJSONRefusesAnAnswerThatIsNotJSON pins that --json never prints what it
// cannot vouch for, such as a proxy's HTML page.
func TestJSONRefusesAnAnswerThatIsNotJSON(t *testing.T) {
	srv := jsonServer(t, map[string]string{"GET /v1/admin/roles": "<html>sign in</html>"})
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "roles", "list", "--json")
	if err == nil || !strings.Contains(err.Error(), "the server's answer is not JSON") {
		t.Fatalf("err = %v, want the not-JSON refusal", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}
