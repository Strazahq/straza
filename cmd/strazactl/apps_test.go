package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/ctl"
)

// appsServer is the admin surface the apps family walks: the app list, the
// install route, the tool inventory, the access rows, the secrets and the
// catalog preview.
type appsServer struct {
	mu   sync.Mutex
	reqs []rolesRequest
}

func (s *appsServer) record(r *http.Request, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, rolesRequest{method: r.Method, uri: r.URL.RequestURI(), body: string(body)})
}

func (s *appsServer) writes() []rolesRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rolesRequest
	for _, req := range s.reqs {
		if req.method == http.MethodGet {
			continue
		}
		out = append(out, req)
	}
	return out
}

const appsList = `[{"id":"a1","name":"scout-tools","version":"0","runtime":"remote","status":"degraded",` +
	`"detail":"requires a credential and none is stored, and this reason is long enough to be cut on the list","source":"api",` +
	`"tools":[],"last_probe_at":"","last_healthy_at":"","reached_by":[],` +
	`"admin_role":"mcp-admin-scout-tools","admin_role_id":"r-1"},` +
	`{"id":"a2","name":"demo-tools","version":"2026.7.4","runtime":"remote","status":"running","detail":"","source":"gitops",` +
	`"tools":["echo","get-sum","get-env"],"reached_by":["dev-tools","scout-role"],` +
	`"admin_role":"mcp-admin-demo-tools","admin_role_id":"r-2"}]`

func (s *appsServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	jsonOut := func(w http.ResponseWriter, code int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		jsonOut(w, http.StatusOK, `{"session_id":"ses-1","session_token":"stok-2"}`)
	})
	mux.HandleFunc("GET /v1/admin/apps", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		jsonOut(w, http.StatusOK, appsList)
	})
	mux.HandleFunc("POST /v1/admin/apps", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.record(r, body)
		jsonOut(w, http.StatusCreated, `{"id":"a1","name":"scout-tools","runtime":"remote","status":"degraded",`+
			`"detail":"requires a credential and none is stored","tools":[]}`)
	})
	mux.HandleFunc("GET /v1/admin/tools", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		jsonOut(w, http.StatusOK, `[{"id":"demo-tools:echo","app":"demo-tools","name":"echo","description":"Echoes back the input string"},`+
			`{"id":"midpoint:get_user","app":"midpoint","name":"get_user","description":"Reads a user"}]`)
	})
	mux.HandleFunc("POST /v1/admin/apps/{id}/bindings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Role  string   `json:"role"`
			Tools []string `json:"tools"`
		}
		body, _ := io.ReadAll(r.Body)
		s.record(r, body)
		_ = json.Unmarshal(body, &req)
		switch req.Role {
		case "dev":
			jsonOut(w, http.StatusBadRequest, `{"error":"business role: it composes application roles and reaches tools through them. Give an application role access instead."}`)
		case "ghost":
			jsonOut(w, http.StatusNotFound, `{"error":"role \"ghost\" does not exist. Pick one from strazactl roles list, or create one on its MCP server with strazactl roles create <server>-<word> --app <server> --tools <tool,...>."}`)
		default:
			tools, _ := json.Marshal(req.Tools)
			if len(req.Tools) == 0 {
				tools = []byte(`["*"]`)
			}
			jsonOut(w, http.StatusCreated, `{"id":"b9","app":"`+r.PathValue("id")+`","role":"`+req.Role+`","tools":`+string(tools)+`}`)
		}
	})
	mux.HandleFunc("GET /v1/admin/bindings", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		jsonOut(w, http.StatusOK, `[{"id":"b1","app":"demo-tools","role":"dev-tools","tools":["*"]},`+
			`{"id":"b2","app":"demo-tools","role":"scout-role","tools":["echo","get-sum"]},`+
			`{"id":"b3","app":"midpoint","role":"dev-tools","tools":["*"]}]`)
	})
	mux.HandleFunc("DELETE /v1/admin/bindings/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		jsonOut(w, http.StatusOK, `{"id":"`+r.PathValue("id")+`","status":"deleted"}`)
	})
	mux.HandleFunc("POST /v1/admin/apps/{id}/secrets", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Role string `json:"role"`
		}
		body, _ := io.ReadAll(r.Body)
		s.record(r, body)
		_ = json.Unmarshal(body, &req)
		switch {
		case r.PathValue("id") == "demo-tools":
			jsonOut(w, http.StatusBadRequest, `{"error":"the MCP server demo-tools declares no credential (credential.kind none), so a secret would never be used. Change the manifest's credential block first."}`)
		case req.Role == "":
			jsonOut(w, http.StatusCreated, `{"id":"s1","app":"scout-tools","scope":"app","role":"","fingerprint":"a1c4"}`)
		default:
			jsonOut(w, http.StatusCreated, `{"id":"s2","app":"scout-tools","scope":"role","role":"`+req.Role+`","fingerprint":"b7e0"}`)
		}
	})
	mux.HandleFunc("GET /v1/admin/apps/{id}/secrets", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		jsonOut(w, http.StatusOK, `[{"id":"s1","scope":"app","role":"","kind":"static","fingerprint":"a1c4","set_at":"2026-09-04T07:41:00Z"}]`)
	})
	mux.HandleFunc("DELETE /v1/admin/apps/{id}/secrets", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		jsonOut(w, http.StatusOK, `{"id":"s1","status":"deleted"}`)
	})
	mux.HandleFunc("DELETE /v1/admin/apps/{id}/secrets/{role}", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		if r.PathValue("role") == "ghost" {
			jsonOut(w, http.StatusNotFound, `{"error":"no secret is stored for the MCP server scout-tools (role ghost)"}`)
			return
		}
		jsonOut(w, http.StatusOK, `{"id":"s2","status":"deleted"}`)
	})
	mux.HandleFunc("GET /v1/admin/catalog/preview", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		if r.URL.Query().Get("role") == "scout-role" {
			jsonOut(w, http.StatusOK, `{"subject":{"roles":["scout-role"]},"entries":[`+
				`{"app":"demo-tools","tool":"echo","status":"visible","hint":"has access, no policy gates it"},`+
				`{"app":"demo-tools","tool":"get-sum","status":"approve_gated","setName":"scout-role-access","ruleId":"demo-tools-get-sum-approve"},`+
				`{"app":"demo-tools","tool":"get-env","status":"matcher_miss","hint":"not in the role's access row on this server"}]}`)
			return
		}
		jsonOut(w, http.StatusOK, `{"subject":{"roles":["dev-tools"]},"entries":[`+
			`{"app":"demo-tools","tool":"echo","status":"visible"},{"app":"demo-tools","tool":"get-sum","status":"visible"},`+
			`{"app":"demo-tools","tool":"get-env","status":"hidden_policy","setName":"dev-guardrails","ruleId":"no-env"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// runApps drives the CLI against a fresh appsServer.
func runApps(t *testing.T, args ...string) (stdout string, s *appsServer, err error) {
	t.Helper()
	s = &appsServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err = runCLI(t, writeCreds(t, srv.URL), args...)
	return stdout, s, err
}

// TestInstalledLine pins the sentence apps install prints per health state:
// the status, the reason, and the command that follows from it.
func TestInstalledLine(t *testing.T) {
	tests := []struct {
		name string
		info ctl.AppInfo
		want string
	}{
		{
			name: "running with tools",
			info: ctl.AppInfo{Name: "scout-tools", Runtime: "remote", Status: "running", Tools: []string{"echo", "get-sum", "get-env"}},
			want: "installed scout-tools (remote runtime). Health: running, 3 tools.",
		},
		{
			name: "degraded for want of a credential names the next command",
			info: ctl.AppInfo{Name: "scout-tools", Runtime: "remote", Status: "degraded", Detail: "requires a credential and none is stored"},
			want: "installed scout-tools (remote runtime). Health: degraded, requires a credential and none is stored. Set one with strazactl apps secret set scout-tools, which asks for the value at a hidden prompt.",
		},
		{
			name: "degraded for another reason points at recheck",
			info: ctl.AppInfo{Name: "scout-tools", Runtime: "remote", Status: "degraded", Detail: "Straza could not reach http://demo-tools:3001/mcp (connection refused)"},
			want: "installed scout-tools (remote runtime). Health: degraded, Straza could not reach http://demo-tools:3001/mcp (connection refused). Fix the cause, then run strazactl apps recheck scout-tools.",
		},
		{
			name: "a reason that ends in a period is not given a second one",
			info: ctl.AppInfo{Name: "lf-loop", Runtime: "remote", Status: "degraded", Detail: "Straza does not dial 127.0.0.1 for the server lf-loop because it is a loopback address on strazad's own host. An administrator publishes an address other machines can reach."},
			want: "installed lf-loop (remote runtime). Health: degraded, Straza does not dial 127.0.0.1 for the server lf-loop because it is a loopback address on strazad's own host. An administrator publishes an address other machines can reach. Fix the cause, then run strazactl apps recheck lf-loop.",
		},
		{
			name: "a status with no reason stands alone",
			info: ctl.AppInfo{Name: "scout-cmd", Runtime: "command", Status: "stopped"},
			want: "installed scout-cmd (command runtime). Health: stopped.",
		},
		{
			name: "a paused server stays paused and names enable",
			info: ctl.AppInfo{Name: "scout-tools", Runtime: "remote", Status: "stopped", Paused: true},
			want: "installed scout-tools (remote runtime). Health: stopped. It stays paused: run strazactl apps enable scout-tools to start it with this manifest.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := installedLine(tc.info); got != tc.want {
				t.Errorf("installedLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAppsInstallPrintsTheHealthLine runs the real command against a manifest
// file and the install route.
func TestAppsInstallPrintsTheHealthLine(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "scout-tools.yaml")
	doc := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: scout-tools\nserver:\n  name: scout-tools\n  version: \"0\"\n" +
		"straza:\n  runtime:\n    kind: remote\n    remote:\n      url: http://demo-tools:3001/mcp\n"
	if err := os.WriteFile(manifest, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, s, err := runApps(t, "apps", "install", "-f", manifest)
	if err != nil {
		t.Fatalf("apps install: %v", err)
	}
	want := "installed scout-tools (remote runtime). Health: degraded, requires a credential and none is stored. Set one with strazactl apps secret set scout-tools, which asks for the value at a hidden prompt.\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	writes := s.writes()
	if len(writes) != 1 || writes[0].uri != "/v1/admin/apps" {
		t.Errorf("wire = %+v, want one POST /v1/admin/apps", writes)
	}
}

// TestAppsListColumns pins the REASON and REACHED BY columns: the reason is
// cut to the column width with a marker, and an app nobody reaches reads "-".
func TestAppsListColumns(t *testing.T) {
	stdout, _, err := runApps(t, "apps", "list")
	if err != nil {
		t.Fatalf("apps list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want header plus two rows:\n%s", len(lines), stdout)
	}
	for _, col := range []string{"NAME", "STATUS", "REASON", "SOURCE", "TOOLS", "REACHED BY", "CHECKED"} {
		if !strings.Contains(lines[0], col) {
			t.Errorf("header missing %q: %s", col, lines[0])
		}
	}
	if !strings.Contains(lines[1], "requires a credential and none is stored, and this reason is long enough to be cut on the list") {
		t.Errorf("a pipe should get the full reason: %s", lines[1])
	}
	if !strings.Contains(lines[1], "  -  ") {
		t.Errorf("an app nobody reaches should read \"-\" under REACHED BY: %s", lines[1])
	}
	if !strings.Contains(lines[2], "dev-tools,scout-role") {
		t.Errorf("reached-by roles missing: %s", lines[2])
	}
	// The admin role is an apps show fact, not a list column: the nine
	// columns already overrun a terminal, so a tenth would only eat REASON.
	if strings.Contains(lines[0], "ADMIN ROLE") {
		t.Errorf("apps list carries no admin role column: %s", lines[0])
	}
}

// TestTrimReason pins the column cut, the no-cut width and the empty case.
func TestTrimReason(t *testing.T) {
	long := strings.Repeat("x", reasonMax+10)
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"", reasonMax, "-"},
		{"short", reasonMax, "short"},
		{strings.Repeat("x", reasonMax), reasonMax, strings.Repeat("x", reasonMax)},
		{long, reasonMax, strings.Repeat("x", reasonMax-3) + "..."},
		{long, 0, long},
		{long, 10, "xxxxxxx..."},
	}
	for _, tc := range tests {
		if got := trimReason(tc.in, tc.width); got != tc.want {
			t.Errorf("trimReason(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
	}
}

// TestReasonWidth pins the cut rule of apps list: a pipe gets the full
// text, a terminal without a usable COLUMNS keeps the fixed cut, and a
// terminal with COLUMNS gets the room left beside the other columns as the
// tabwriter pads them, never under the header's width.
func TestReasonWidth(t *testing.T) {
	rows := [][]string{
		{"NAME", "VERSION", "RUNTIME", "STATUS", "REASON", "SOURCE", "TOOLS", "REACHED BY", "CHECKED"},
		{"scout-tools", "2026.7.4", "remote", "degraded", strings.Repeat("r", 200), "gitops", "0", "dev-tools,scout-role", "42s ago"},
	}
	// Every column but REASON at its widest, plus two spaces of padding
	// after each of the eight tab-terminated cells.
	const others = 11 + 8 + 7 + 8 + 6 + 5 + 20 + 7 + 8*2
	tests := []struct {
		name     string
		terminal bool
		columns  string
		want     int
	}{
		{"a pipe never cuts", false, "200", 0},
		{"a terminal without COLUMNS keeps the fixed cut", true, "", reasonMax},
		{"a terminal with a COLUMNS that is not a number keeps the fixed cut", true, "wide", reasonMax},
		{"a terminal with COLUMNS gets the room left", true, "160", 160 - others},
		{"a narrow terminal keeps the header's width", true, "40", len("REASON")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reasonWidth(tc.terminal, tc.columns, rows); got != tc.want {
				t.Errorf("reasonWidth(%v, %q) = %d, want %d", tc.terminal, tc.columns, got, tc.want)
			}
		})
	}
}

// TestAppsBindSpeaksOfAccess pins the access wording of apps bind and the
// server's refusals surfacing verbatim.
func TestAppsBindSpeaksOfAccess(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantOut  string
		wantErr  string
		wantBody string
	}{
		{
			name:     "named tools",
			args:     []string{"apps", "bind", "scout-tools", "--role", "scout-role", "--tools", "echo,get-sum"},
			wantOut:  "gave scout-role access to scout-tools: echo, get-sum. They run unless a policy gates them. Assign the role on Users or through your identity manager.\n",
			wantBody: `{"role":"scout-role","tools":["echo","get-sum"]}`,
		},
		{
			name:     "no tool list is the glob",
			args:     []string{"apps", "bind", "scout-tools", "--role", "scout-role"},
			wantOut:  "gave scout-role access to scout-tools: every tool, including tools added later. They run unless a policy gates them. Assign the role on Users or through your identity manager.\n",
			wantBody: `{"role":"scout-role","tools":null}`,
		},
		{
			name:     "a business role is refused by the server",
			args:     []string{"apps", "bind", "scout-tools", "--role", "dev"},
			wantErr:  "business role: it composes application roles and reaches tools through them. Give an application role access instead.",
			wantBody: `{"role":"dev","tools":null}`,
		},
		{
			name:     "an unknown role gets the create sentence",
			args:     []string{"apps", "bind", "scout-tools", "--role", "ghost"},
			wantErr:  `role "ghost" does not exist. Pick one from strazactl roles list, or create one on its MCP server with strazactl roles create <server>-<word> --app <server> --tools <tool,...>.`,
			wantBody: `{"role":"ghost","tools":null}`,
		},
		{
			name:    "the role is required",
			args:    []string{"apps", "bind", "scout-tools"},
			wantErr: "--role is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, s, err := runApps(t, tc.args...)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("%v: %v", tc.args, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if stdout != tc.wantOut {
				t.Errorf("stdout = %q, want %q", stdout, tc.wantOut)
			}
			writes := s.writes()
			if tc.wantBody == "" {
				if len(writes) != 0 {
					t.Errorf("wire = %+v, want nothing", writes)
				}
				return
			}
			if len(writes) != 1 || strings.TrimSpace(writes[0].body) != tc.wantBody {
				t.Errorf("wire = %+v, want one POST with body %s", writes, tc.wantBody)
			}
		})
	}
}

// TestAppsUnbindNamesTheRow pins that unbind reads the row first so the line
// names the role and the app, and that an unknown id never reaches DELETE.
func TestAppsUnbindNamesTheRow(t *testing.T) {
	stdout, s, err := runApps(t, "apps", "unbind", "b2")
	if err != nil {
		t.Fatalf("apps unbind: %v", err)
	}
	if stdout != "removed scout-role's access to demo-tools\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if writes := s.writes(); len(writes) != 1 || writes[0].method != http.MethodDelete || writes[0].uri != "/v1/admin/bindings/b2" {
		t.Errorf("wire = %+v, want one DELETE /v1/admin/bindings/b2", writes)
	}

	_, s, err = runApps(t, "apps", "unbind", "nope")
	if err == nil || !strings.Contains(err.Error(), `no access row has the id "nope". Find the id with strazactl bindings list`) {
		t.Fatalf("err = %v, want the find hint", err)
	}
	if writes := s.writes(); len(writes) != 0 {
		t.Errorf("an unknown id must not reach DELETE: %+v", writes)
	}
}

// TestAppsSecretSetAndRemove pins the server's own secret as the default,
// the role override, the value source and the refusals.
func TestAppsSecretSetAndRemove(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		args    []string
		wantOut string
		wantErr string
		wantReq rolesRequest
	}{
		{
			name:    "no role stores the server's own secret",
			env:     "s3cret",
			args:    []string{"apps", "secret", "set", "scout-tools"},
			wantOut: "secret set for the MCP server scout-tools: the server's own secret, fingerprint a1c4\n",
			wantReq: rolesRequest{method: http.MethodPost, uri: "/v1/admin/apps/scout-tools/secrets", body: `{"value":"s3cret"}`},
		},
		{
			name:    "a role stores the override",
			env:     "s3cret",
			args:    []string{"apps", "secret", "set", "scout-tools", "--role", "scout-role"},
			wantOut: "secret set for the MCP server scout-tools, role scout-role, fingerprint b7e0\n",
			wantReq: rolesRequest{method: http.MethodPost, uri: "/v1/admin/apps/scout-tools/secrets", body: `{"role":"scout-role","value":"s3cret"}`},
		},
		{
			name:    "no value anywhere off a terminal is a loud error before the wire",
			args:    []string{"apps", "secret", "set", "scout-tools"},
			wantErr: "no secret value given: stdin is not a terminal, so strazactl cannot ask for it at a hidden prompt. Run the command in a terminal and type the value at the prompt, or in a script set STRAZA_SECRET_VALUE from a secret manager",
		},
		{
			name:    "a kind none app is refused by the server",
			env:     "s3cret",
			args:    []string{"apps", "secret", "set", "demo-tools"},
			wantErr: "the MCP server demo-tools declares no credential (credential.kind none), so a secret would never be used",
			wantReq: rolesRequest{method: http.MethodPost, uri: "/v1/admin/apps/demo-tools/secrets", body: `{"value":"s3cret"}`},
		},
		{
			name:    "remove the server's own secret",
			args:    []string{"apps", "secret", "remove", "scout-tools"},
			wantOut: "removed the server's own secret for the MCP server scout-tools\n",
			wantReq: rolesRequest{method: http.MethodDelete, uri: "/v1/admin/apps/scout-tools/secrets"},
		},
		{
			name:    "remove a role's override",
			args:    []string{"apps", "secret", "remove", "scout-tools", "--role", "scout-role"},
			wantOut: "removed the secret for the MCP server scout-tools, role scout-role\n",
			wantReq: rolesRequest{method: http.MethodDelete, uri: "/v1/admin/apps/scout-tools/secrets/scout-role"},
		},
		{
			name:    "removing a row that is not there surfaces the server's sentence",
			args:    []string{"apps", "secret", "remove", "scout-tools", "--role", "ghost"},
			wantErr: "no secret is stored for the MCP server scout-tools (role ghost)",
			wantReq: rolesRequest{method: http.MethodDelete, uri: "/v1/admin/apps/scout-tools/secrets/ghost"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STRAZA_SECRET_VALUE", tc.env)
			stubSecretPrompt(t, false, "", nil)
			stdout, s, err := runApps(t, tc.args...)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("%v: %v", tc.args, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if stdout != tc.wantOut {
				t.Errorf("stdout = %q, want %q", stdout, tc.wantOut)
			}
			writes := s.writes()
			if tc.wantReq.method == "" {
				if len(writes) != 0 {
					t.Errorf("wire = %+v, want nothing", writes)
				}
				return
			}
			if len(writes) != 1 || writes[0].method != tc.wantReq.method || writes[0].uri != tc.wantReq.uri ||
				strings.TrimSpace(writes[0].body) != tc.wantReq.body {
				t.Errorf("wire = %+v, want %+v", writes, tc.wantReq)
			}
		})
	}
}

// stubSecretPrompt replaces the terminal check and the hidden reader of apps
// secret set for one test, and returns the count of reads it served.
func stubSecretPrompt(t *testing.T, terminal bool, typed string, readErr error) *int {
	t.Helper()
	reads := 0
	prevTerminal, prevRead := secretStdinIsTerminal, readSecretHidden
	secretStdinIsTerminal = func() bool { return terminal }
	readSecretHidden = func() ([]byte, error) {
		reads++
		return []byte(typed), readErr
	}
	t.Cleanup(func() { secretStdinIsTerminal, readSecretHidden = prevTerminal, prevRead })
	return &reads
}

// TestAppsSecretSetPrompt pins where the secret comes from: --value, then
// STRAZA_SECRET_VALUE, then a hidden prompt on a terminal that names the
// server and the role. A non-terminal stdin, an empty answer and a failed
// read each refuse before anything reaches the server.
func TestAppsSecretSetPrompt(t *testing.T) {
	set := []string{"apps", "secret", "set", "scout-tools"}
	tests := []struct {
		name       string
		env        string
		args       []string
		terminal   bool
		typed      string
		readErr    error
		wantBody   string
		wantPrompt string
		wantReads  int
		wantErr    string
	}{
		{name: "the environment variable wins over the prompt", env: "from-env", args: set, terminal: true, typed: "typed",
			wantBody: `{"value":"from-env"}`},
		{name: "--value is used without a prompt", args: append(append([]string{}, set...), "--value", "from-flag"), terminal: true, typed: "typed",
			wantBody: `{"value":"from-flag"}`},
		{name: "a terminal asks at a hidden prompt", args: set, terminal: true, typed: "typed-secret",
			wantBody: `{"value":"typed-secret"}`, wantPrompt: "Secret for the MCP server scout-tools (input hidden): ", wantReads: 1},
		{name: "the prompt names the role", args: append(append([]string{}, set...), "--role", "scout-role"), terminal: true, typed: "typed-secret",
			wantBody: `{"role":"scout-role","value":"typed-secret"}`, wantPrompt: "Secret for the MCP server scout-tools, role scout-role (input hidden): ", wantReads: 1},
		{name: "a non-terminal stdin refuses", args: set, terminal: false,
			wantErr: "no secret value given: stdin is not a terminal, so strazactl cannot ask for it at a hidden prompt. Run the command in a terminal and type the value at the prompt, or in a script set STRAZA_SECRET_VALUE from a secret manager"},
		{name: "an empty answer refuses", args: set, terminal: true, typed: "  ",
			wantPrompt: "Secret for the MCP server scout-tools (input hidden): ", wantReads: 1,
			wantErr: "no secret typed, so nothing was stored. Run the command again and type the value at the prompt"},
		{name: "a failed read refuses", args: set, terminal: true, readErr: errors.New("input/output error"), wantReads: 1,
			wantErr: "could not read the secret at the prompt (input/output error), so nothing was stored. Run the command again in a terminal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STRAZA_SECRET_VALUE", tc.env)
			reads := stubSecretPrompt(t, tc.terminal, tc.typed, tc.readErr)
			s := &appsServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			_, stderr, err := runCLI(t, writeCreds(t, srv.URL), tc.args...)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("%v: %v", tc.args, err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if *reads != tc.wantReads {
				t.Errorf("hidden reads = %d, want %d", *reads, tc.wantReads)
			}
			if tc.wantPrompt != "" && !strings.Contains(stderr, tc.wantPrompt) {
				t.Errorf("stderr = %q, want the prompt %q", stderr, tc.wantPrompt)
			}
			writes := s.writes()
			if tc.wantBody == "" {
				if len(writes) != 0 {
					t.Errorf("wire = %+v, want nothing sent", writes)
				}
				return
			}
			if len(writes) != 1 || strings.TrimSpace(writes[0].body) != tc.wantBody {
				t.Errorf("wire = %+v, want one POST with %s", writes, tc.wantBody)
			}
		})
	}
}

// TestAppsToolsFilter pins --app: one app's rows, and a plain sentence with
// the reasons when the listing has no row for that name.
func TestAppsToolsFilter(t *testing.T) {
	stdout, _, err := runApps(t, "apps", "tools", "--app", "demo-tools")
	if err != nil {
		t.Fatalf("apps tools: %v", err)
	}
	if !strings.Contains(stdout, "echo") || strings.Contains(stdout, "midpoint") {
		t.Errorf("filter did not keep one app:\n%s", stdout)
	}
	stdout, _, err = runApps(t, "apps", "tools", "--app", "scout-tools")
	if err != nil {
		t.Fatalf("apps tools: %v", err)
	}
	if !strings.Contains(stdout, "no tools known for the MCP server scout-tools: it has not answered a health probe yet, or it is stopped, or no server has that name. Run strazactl apps show scout-tools for the reason.") {
		t.Errorf("empty filter result should say so:\n%s", stdout)
	}
}

// TestAppsShow pins the one-read page: head, access rows with the engine's
// word per tool from the preview, and the secrets by fingerprint.
func TestAppsShow(t *testing.T) {
	stdout, s, err := runApps(t, "apps", "show", "demo-tools")
	if err != nil {
		t.Fatalf("apps show: %v", err)
	}
	for _, want := range []string{
		"demo-tools: running (remote runtime, source gitops, version 2026.7.4)",
		"tools:       3: echo, get-sum, get-env",
		"reached by:  dev-tools,scout-role",
		"admin role:  mcp-admin-demo-tools (its holders administer this server and no other)",
		"ACCESS",
		"ID  ROLE        TOOLS                                    HOW THEY RUN",
		"b1  dev-tools   every tool, including tools added later  echo runs, get-sum runs, get-env denied (dev-guardrails)",
		"b2  scout-role  echo, get-sum                            echo runs, get-sum needs approval (scout-role-access)",
		"SECRETS",
		"SCOPE  ROLE  KIND    FINGERPRINT  SET",
		"app    -     static  a1c4         2026-09-04T07:41:00Z",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "reason:") {
		t.Errorf("a running app has no reason line:\n%s", stdout)
	}
	if strings.Contains(stdout, "b3") {
		t.Errorf("another app's access row leaked in:\n%s", stdout)
	}
	previews := 0
	for _, r := range s.reqs {
		if strings.HasPrefix(r.uri, "/v1/admin/catalog/preview?app=demo-tools&role=") {
			previews++
		}
	}
	if previews != 2 {
		t.Errorf("previews = %d, want one per role", previews)
	}

	stdout, _, err = runApps(t, "apps", "show", "scout-tools")
	if err != nil {
		t.Fatalf("apps show: %v", err)
	}
	for _, want := range []string{
		"reason:      requires a credential and none is stored, and this reason is long enough to be cut on the list",
		"tools:       none known",
		"reached by:  -",
		"admin role:  mcp-admin-scout-tools (its holders administer this server and no other)",
		"no role has access to scout-tools. Create one with strazactl roles create scout-tools-<word> --app scout-tools --tools <tool,...>.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}

	_, _, err = runApps(t, "apps", "show", "ghost")
	if err == nil || err.Error() != `no MCP server named "ghost". List them with strazactl apps list` {
		t.Errorf("err = %v", err)
	}
}

// TestAdminRoleFact pins the admin role line of apps show: the role with
// what holding it means, and the upgrade sentence when an older strazad
// answered without the field.
func TestAdminRoleFact(t *testing.T) {
	tests := []struct {
		name string
		role string
		want string
	}{
		{"a minted role", "mcp-admin-demo-tools", "mcp-admin-demo-tools (its holders administer this server and no other)"},
		{"an older strazad", "", "none reported, so this strazad speaks an admin API older than 0.127.0. " +
			"Upgrade strazad to see the role that administers this server."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := adminRoleFact(tc.role); got != tc.want {
				t.Errorf("adminRoleFact(%q) = %q, want %q", tc.role, got, tc.want)
			}
		})
	}
}

// TestHowTheyRun pins the words per preview status and the two empty cases.
func TestHowTheyRun(t *testing.T) {
	tests := []struct {
		name    string
		entries []ctl.CatalogEntry
		want    string
	}{
		{"nothing known", nil, "nothing runs yet: no tool in the access row is known to the server"},
		{"grant misses every tool", []ctl.CatalogEntry{{Tool: "x", Status: "matcher_miss"}}, "nothing runs yet: no tool in the access row is known to the server"},
		{"server down with a hint", []ctl.CatalogEntry{{Status: "not_running", Hint: "has access, but the server is degraded: requires a credential and none is stored"}},
			"has access, but the server is degraded: requires a credential and none is stored"},
		{"server down without a hint", []ctl.CatalogEntry{{Status: "not_running"}}, "the server is not running, so nothing runs yet"},
		{"three states", []ctl.CatalogEntry{
			{Tool: "echo", Status: "visible"},
			{Tool: "get-sum", Status: "approve_gated", SetName: "scout-role-access"},
			{Tool: "get-env", Status: "hidden_policy", SetName: "scout-role-access"},
		}, "echo runs, get-sum needs approval (scout-role-access), get-env denied (scout-role-access)"},
		{"the default deny is not a block", []ctl.CatalogEntry{
			{Tool: "get-env", Status: "hidden_policy", Default: true},
			{Tool: "get-sum", Status: "hidden_policy", SetName: "dev-guardrails"},
		}, "get-env no policy yet, get-sum denied (dev-guardrails)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := howTheyRun(ctl.CatalogPreview{Entries: tc.entries}); got != tc.want {
				t.Errorf("howTheyRun = %q, want %q", got, tc.want)
			}
		})
	}
}

// runCLIIn is runCLI with a scripted stdin, for the commands that confirm.
func runCLIIn(t *testing.T, credsPath, stdin string, args ...string) (stdout string, err error) {
	t.Helper()
	var out bytes.Buffer
	root := newRootCmd(credsPath)
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), err
}
