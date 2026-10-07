package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// rolesRequest is one recorded call: the wire shape the CLI produced.
type rolesRequest struct {
	method string
	uri    string
	body   string
}

// rolesServer is the admin surface the roles family walks: the role list the
// client resolves names against, role creation, and the implication edges.
type rolesServer struct {
	mu   sync.Mutex
	reqs []rolesRequest
}

func (s *rolesServer) record(r *http.Request, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, rolesRequest{method: r.Method, uri: r.URL.RequestURI(), body: string(body)})
}

// writes returns every recorded request that was not a name resolution, the
// calls a test actually cares about.
func (s *rolesServer) writes() []rolesRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rolesRequest
	for _, req := range s.reqs {
		if req.method == http.MethodGet && (req.uri == "/v1/admin/roles" || req.uri == "/v1/admin/users") {
			continue
		}
		out = append(out, req)
	}
	return out
}

func (s *rolesServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("GET /v1/admin/roles", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"r1","name":"dev","description":"builders","kind":"business"},` +
			`{"id":"r2","name":"ops","description":"runners","kind":"business"}]`))
	})
	mux.HandleFunc("POST /v1/admin/roles", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name   string   `json:"name"`
			Kind   string   `json:"kind"`
			Server string   `json:"server"`
			Tools  []string `json:"tools"`
		}
		body, _ := io.ReadAll(r.Body)
		s.record(r, body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Name == "dev":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"role already exists"}`))
		case req.Kind != "" && req.Kind != "business" && req.Kind != "application" && req.Kind != "approver" && req.Kind != "straza":
			// The server owns the enum: a bad kind comes back with its reason.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"kind must be business, application, approver or straza, and it is \"` + req.Kind + `\". Set it to one of the four"}`))
		case len(req.Tools) == 1 && req.Tools[0] == "*":
			// Who may send the every-tool matcher is the server's to judge,
			// so this fake answers as it answers a server admin.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"a server-owned role lists the tools it reaches by name. Only a global admin may give it every tool, and the tools the server adds later, with the * matcher"}`))
		case req.Server != "":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"r9","name":"` + req.Name + `","kind":"application","server":"` + req.Server + `"}`))
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"r9","name":"` + req.Name + `","kind":"` + req.Kind + `"}`))
		}
	})
	// dev's deletion turns off two sets and names them; ops's turns off
	// none and answers the older shape with no sets_off field.
	mux.HandleFunc("DELETE /v1/admin/roles/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") == "r1" {
			_, _ = w.Write([]byte(`{"status":"deleted","sets_off":["dev-access","dev-guardrails"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"deleted"}`))
	})
	mux.HandleFunc("GET /v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"u1","username":"alice","status":"active"}]`))
	})
	// The list answers the same rows whatever the query, with another
	// user's row of the same role first, so the client must match on the
	// subject and the role together.
	mux.HandleFunc("GET /v1/admin/assignments", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"a-foreign","subject_kind":"user","subject_id":"u2","role_id":"r2","origin":"admin"},` +
			`{"id":"a-ops","subject_kind":"user","subject_id":"u1","role_id":"r2","origin":"admin"}]`))
	})
	mux.HandleFunc("POST /v1/admin/assignments", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.record(r, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"a-new","subject_kind":"user","subject_id":"u1","role_id":"r1","origin":"admin"}`))
	})
	mux.HandleFunc("DELETE /v1/admin/assignments/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") != "a-ops" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such assignment"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"deleted"}`))
	})
	mux.HandleFunc("GET /v1/admin/roles/{id}/implications", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"r2","implies_id":"r2","implies_name":"ops"}]`))
	})
	mux.HandleFunc("POST /v1/admin/roles/{id}/implications", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.record(r, body)
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") == "r2" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"ops would imply dev, and dev implies ops, so the two would compose each other in a circle. Drop this implication, or the path that leads back from dev to ops"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"role_id":"r1","implies_role_id":"r2"}`))
	})
	mux.HandleFunc("DELETE /v1/admin/roles/{id}/implications/{implicationId}", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("implicationId") != "r2" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such implication"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"deleted"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestRolesCreateKind pins `roles create --kind`: the flag is required for a
// role that belongs to no server, because the CLI takes no silent default,
// and a missing kind is refused before any call. A set kind rides the create
// body as typed and is never client-validated: the server owns the enum and
// its 400 reason is what the operator sees.
func TestRolesCreateKind(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantBody map[string]any
		wantErr  string
	}{
		{
			name:    "no kind flag is refused before the call",
			args:    []string{"roles", "create", "payments"},
			wantErr: "roles create needs --kind or --app, because a role's kind is fixed at create and Straza must know what payments is for.",
		},
		{
			name: "the refusal names every kind with its meaning",
			args: []string{"roles", "create", "payments", "--description", "pays"},
			wantErr: "Use --app <server> --tools <tool,...> for a role that reaches an MCP server's tools, --kind application for a role that policy sets match and that reaches no MCP server, " +
				"business for a role that bundles application roles for a job, approver for a role that decides approval requests, or straza for a role with capabilities in Straza itself",
		},
		{
			name:     "business",
			args:     []string{"roles", "create", "payments", "--kind", "business"},
			wantBody: map[string]any{"name": "payments", "description": "", "kind": "business"},
		},
		{
			name:     "application",
			args:     []string{"roles", "create", "payments", "--kind", "application"},
			wantBody: map[string]any{"name": "payments", "description": "", "kind": "application"},
		},
		{
			name:     "straza kind rides alongside the description flag",
			args:     []string{"roles", "create", "supers", "--kind", "straza", "--description", "control plane only"},
			wantBody: map[string]any{"name": "supers", "description": "control plane only", "kind": "straza"},
		},
		{
			name:     "create approver kind",
			args:     []string{"roles", "create", "sec-approvers", "--kind", "approver", "--description", "deciders"},
			wantBody: map[string]any{"name": "sec-approvers", "description": "deciders", "kind": "approver"},
		},
		{
			name:     "unknown kind is passed through and the server rejects it",
			args:     []string{"roles", "create", "payments", "--kind", "wat"},
			wantBody: map[string]any{"name": "payments", "description": "", "kind": "wat"},
			wantErr:  `kind must be business, application, approver or straza, and it is "wat". Set it to one of the four`,
		},
		{
			name:     "duplicate name surfaces the server's 409",
			args:     []string{"roles", "create", "dev", "--kind", "business"},
			wantBody: map[string]any{"name": "dev", "description": "", "kind": "business"},
			wantErr:  "role already exists",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &rolesServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			credsPath := writeCreds(t, srv.URL)

			_, _, err := runCLI(t, credsPath, tc.args...)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("roles create: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			writes := s.writes()
			if tc.wantBody == nil {
				if len(writes) != 0 {
					t.Fatalf("wire = %+v, want nothing sent", writes)
				}
				return
			}
			if len(writes) != 1 || writes[0].method != http.MethodPost || writes[0].uri != "/v1/admin/roles" {
				t.Fatalf("wire = %+v, want one POST /v1/admin/roles", writes)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(writes[0].body), &got); err != nil {
				t.Fatalf("request body %q: %v", writes[0].body, err)
			}
			if !reflect.DeepEqual(got, tc.wantBody) {
				t.Errorf("body = %v, want %v", got, tc.wantBody)
			}
		})
	}
}

// TestRolesCreateServerOwned pins `roles create --app` and `--tools`: the
// pair rides the create body together, neither rides alone, and the tool
// names travel as typed so the server's own refusal is what the operator
// reads. A role that belongs to no server sends neither field, so the body
// an older strazad reads is unchanged.
func TestRolesCreateServerOwned(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantBody map[string]any
		wantOut  string
		wantErr  string
	}{
		{
			name:     "server and tools ride together",
			args:     []string{"roles", "create", "demo-tools-readers", "--app", "demo-tools", "--tools", "read_file,list_dir"},
			wantBody: map[string]any{"name": "demo-tools-readers", "description": "", "server": "demo-tools", "tools": []any{"read_file", "list_dir"}},
			wantOut:  "created role demo-tools-readers (r9) on the server demo-tools\n",
		},
		{
			name: "one tool and a description",
			args: []string{"roles", "create", "demo-tools-readers", "--app", "demo-tools", "--tools", "read_file", "--description", "reads files"},
			wantBody: map[string]any{"name": "demo-tools-readers", "description": "reads files",
				"server": "demo-tools", "tools": []any{"read_file"}},
			wantOut: "created role demo-tools-readers (r9) on the server demo-tools\n",
		},
		{
			name:     "the every-tool matcher travels and the server judges who may send it",
			args:     []string{"roles", "create", "demo-tools-readers", "--app", "demo-tools", "--tools", "*"},
			wantBody: map[string]any{"name": "demo-tools-readers", "description": "", "server": "demo-tools", "tools": []any{"*"}},
			wantErr:  "a server-owned role lists the tools it reaches by name. Only a global admin may give it every tool, and the tools the server adds later, with the * matcher",
		},
		{
			name:     "an owning server takes kind application as typed",
			args:     []string{"roles", "create", "demo-tools-readers", "--app", "demo-tools", "--tools", "read_file", "--kind", "application"},
			wantBody: map[string]any{"name": "demo-tools-readers", "description": "", "kind": "application", "server": "demo-tools", "tools": []any{"read_file"}},
			wantOut:  "created role demo-tools-readers (r9) on the server demo-tools\n",
		},
		{
			name:    "an owning server refuses any other kind before the call",
			args:    []string{"roles", "create", "demo-tools-readers", "--app", "demo-tools", "--tools", "read_file", "--kind", "business"},
			wantErr: "--kind business does not fit --app, because a role that belongs to an MCP server is always an application role. Leave --kind out or set it to application",
		},
		{
			name: "an owning server without tools is refused before the call",
			args: []string{"roles", "create", "demo-tools-readers", "--app", "demo-tools"},
			wantErr: "--tools is required with --app, because a role that belongs to an MCP server reaches only the tools it lists. Add the names, for example --tools read_file,list_dir, " +
				"and run `strazactl apps tools --app demo-tools` to see what the server offers. A global admin may send --tools '*' for every tool, the ones the server adds later included",
		},
		{
			name: "tools without an owning server is refused before the call",
			args: []string{"roles", "create", "readers", "--tools", "read_file"},
			wantErr: "--tools needs --app <server>, because only a role that an MCP server owns reaches tools, and it is created on that server. " +
				"Run `strazactl roles create <server>-readers --app <server> --tools read_file`, with a server name from strazactl apps list",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &rolesServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			credsPath := writeCreds(t, srv.URL)

			stdout, _, err := runCLI(t, credsPath, tc.args...)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("roles create: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if stdout != tc.wantOut {
				t.Errorf("stdout = %q, want %q", stdout, tc.wantOut)
			}
			writes := s.writes()
			if tc.wantBody == nil {
				if len(writes) != 0 {
					t.Fatalf("wire = %+v, want nothing sent", writes)
				}
				return
			}
			if len(writes) != 1 || writes[0].method != http.MethodPost || writes[0].uri != "/v1/admin/roles" {
				t.Fatalf("wire = %+v, want one POST /v1/admin/roles", writes)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(writes[0].body), &got); err != nil {
				t.Fatalf("request body %q: %v", writes[0].body, err)
			}
			if !reflect.DeepEqual(got, tc.wantBody) {
				t.Errorf("body = %v, want %v", got, tc.wantBody)
			}
		})
	}
}

// TestRolesListServerColumn pins the SERVER column: it appears once a role
// in the answer names an owning server, a role without one reads "-", and a
// list of global roles alone keeps the four columns it has today.
func TestRolesListServerColumn(t *testing.T) {
	tests := []struct {
		name  string
		list  string
		cells [][]string
	}{
		{
			name: "no server-owned role keeps the columns of today",
			list: `[{"id":"r1","name":"dev","description":"builders","kind":"business"}]`,
			cells: [][]string{
				{"NAME", "KIND", "DESCRIPTION", "ID"},
				{"dev", "business", "builders", "r1"},
			},
		},
		{
			name: "one server-owned role adds the column for the whole table",
			list: `[{"id":"r1","name":"dev","description":"builders","kind":"business"},` +
				`{"id":"r9","name":"demo-tools-readers","description":"reads","kind":"application","server":"demo-tools"}]`,
			cells: [][]string{
				{"NAME", "KIND", "SERVER", "DESCRIPTION", "ID"},
				{"dev", "business", "-", "builders", "r1"},
				{"demo-tools-readers", "application", "demo-tools", "reads", "r9"},
			},
		},
	}
	gap := regexp.MustCompile(`\s{2,}`)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
			})
			mux.HandleFunc("GET /v1/admin/roles", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.list))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			t.Setenv("STRAZA_SERVER", "")
			credsPath := writeCreds(t, srv.URL)

			stdout, _, err := runCLI(t, credsPath, "roles", "list")
			if err != nil {
				t.Fatalf("roles list: %v", err)
			}
			var got [][]string
			for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
				got = append(got, gap.Split(strings.TrimRight(line, " "), -1))
			}
			if !reflect.DeepEqual(got, tc.cells) {
				t.Errorf("table =\n%q\nwant\n%q", got, tc.cells)
			}
		})
	}
}

// TestRolesImplicationsWiring pins the subcommand tree: the bare form still
// lists (it must not become a subcommand-only node), add and remove reach the
// existing routes with names resolved to ids, and each prints its exact line.
func TestRolesImplicationsWiring(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantWrites []rolesRequest
		wantOut    string
		wantErr    string
	}{
		{
			name: "bare form still lists",
			args: []string{"roles", "implications", "dev"},
			wantWrites: []rolesRequest{
				{method: http.MethodGet, uri: "/v1/admin/roles/r1/implications"},
			},
		},
		{
			name: "add resolves both names and posts the edge",
			args: []string{"roles", "implications", "add", "dev", "ops"},
			wantWrites: []rolesRequest{
				{method: http.MethodPost, uri: "/v1/admin/roles/r1/implications", body: `{"implies_role_id":"r2"}`},
			},
			wantOut: "added: holding dev now also holds ops\n",
		},
		{
			name: "remove addresses the edge by the implied role's id",
			args: []string{"roles", "implications", "remove", "dev", "ops"},
			wantWrites: []rolesRequest{
				{method: http.MethodDelete, uri: "/v1/admin/roles/r1/implications/r2"},
			},
			wantOut: "removed\n",
		},
		{
			name: "add surfaces the server's cycle refusal",
			args: []string{"roles", "implications", "add", "ops", "dev"},
			wantWrites: []rolesRequest{
				{method: http.MethodPost, uri: "/v1/admin/roles/r2/implications", body: `{"implies_role_id":"r1"}`},
			},
			wantErr: "ops would imply dev, and dev implies ops, so the two would compose each other in a circle. Drop this implication, or the path that leads back from dev to ops",
		},
		{
			name:    "add rejects an unknown role before writing",
			args:    []string{"roles", "implications", "add", "ghost", "ops"},
			wantErr: `no role named "ghost"`,
		},
		{
			name:    "add rejects an unknown implied role before writing",
			args:    []string{"roles", "implications", "add", "dev", "ghost"},
			wantErr: `no role named "ghost"`,
		},
		{
			name:    "add needs two operands",
			args:    []string{"roles", "implications", "add", "dev"},
			wantErr: "accepts 2 arg",
		},
		{
			name:    "remove needs two operands",
			args:    []string{"roles", "implications", "remove", "dev", "ops", "extra"},
			wantErr: "accepts 2 arg",
		},
		{
			name:    "the bare form still takes exactly one operand",
			args:    []string{"roles", "implications", "dev", "ops"},
			wantErr: "accepts 1 arg",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &rolesServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			credsPath := writeCreds(t, srv.URL)

			stdout, _, err := runCLI(t, credsPath, tc.args...)
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
			if len(writes) != len(tc.wantWrites) {
				t.Fatalf("wire = %+v, want %+v", writes, tc.wantWrites)
			}
			for i, want := range tc.wantWrites {
				if writes[i].method != want.method || writes[i].uri != want.uri ||
					strings.TrimSpace(writes[i].body) != want.body {
					t.Errorf("wire[%d] = %+v, want %+v", i, writes[i], want)
				}
			}
		})
	}
}

// TestAssignWiring pins the top-level assign and unassign verbs, which take
// the same form. Both resolve the user and the role by name. assign posts
// the row. unassign reads the user's own assignments with the subject kind
// and the subject id together, deletes the row of that role and never
// another user's row of it, and says so when the user does not hold the
// role directly. Both print through the command's output.
func TestAssignWiring(t *testing.T) {
	const own = "/v1/admin/assignments?subject_kind=user&subject_id=u1"
	tests := []struct {
		name       string
		args       []string
		wantWrites []rolesRequest
		wantOut    string
		wantErr    string
	}{
		{
			name: "assign posts the row",
			args: []string{"assign", "dev", "--user", "alice"},
			wantWrites: []rolesRequest{
				{method: http.MethodPost, uri: "/v1/admin/assignments", body: `{"role_id":"r1","subject_id":"u1","subject_kind":"user"}`},
			},
			wantOut: "assigned dev to alice\n",
		},
		{
			name: "unassign deletes the user's own row of the role",
			args: []string{"unassign", "ops", "--user", "alice"},
			wantWrites: []rolesRequest{
				{method: http.MethodGet, uri: own},
				{method: http.MethodDelete, uri: "/v1/admin/assignments/a-ops"},
			},
			wantOut: "unassigned ops from alice\n",
		},
		{
			name:       "unassign of a role the user does not hold directly",
			args:       []string{"unassign", "dev", "--user", "alice"},
			wantWrites: []rolesRequest{{method: http.MethodGet, uri: own}},
			wantErr:    "alice does not hold dev directly, so there is no assignment to remove. A role reached through another role goes with that role, so take that role away instead",
		},
		{
			name:    "unassign needs --user",
			args:    []string{"unassign", "ops"},
			wantErr: "strazactl unassign needs --user, the username of the person who holds the role, as in strazactl unassign ops --user alice",
		},
		{
			name:    "unassign of an unknown user sends nothing",
			args:    []string{"unassign", "ops", "--user", "ghost"},
			wantErr: `no user named "ghost"`,
		},
		{
			name:    "unassign of an unknown role sends nothing",
			args:    []string{"unassign", "ghost", "--user", "alice"},
			wantErr: `no role named "ghost"`,
		},
		{
			name:    "unassign takes one role",
			args:    []string{"unassign", "ops", "dev", "--user", "alice"},
			wantErr: "accepts 1 arg",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &rolesServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), tc.args...)
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
			if len(writes) != len(tc.wantWrites) {
				t.Fatalf("wire = %+v, want %+v", writes, tc.wantWrites)
			}
			for i, want := range tc.wantWrites {
				if writes[i].method != want.method || writes[i].uri != want.uri || strings.TrimSpace(writes[i].body) != want.body {
					t.Errorf("wire[%d] = %+v, want %+v", i, writes[i], want)
				}
			}
		})
	}
}

// TestRolesDeleteNamesTheSetsTurnedOff pins what roles delete prints: the
// deleted line, then one line per policy set the server turned off with
// the role, each naming the set and the verb that deletes it, and nothing
// more when the answer names no set or, from an older server, carries no
// such field.
func TestRolesDeleteNamesTheSetsTurnedOff(t *testing.T) {
	const off = "turned off the policy set %s, which matched only this role. Delete it with strazactl policy delete --yes %s when you no longer need it.\n"
	tests := []struct {
		name       string
		args       []string
		wantWrites []rolesRequest
		wantOut    string
		wantErr    string
	}{
		{
			name:       "each set turned off gets its line",
			args:       []string{"roles", "delete", "dev", "--yes"},
			wantWrites: []rolesRequest{{method: http.MethodDelete, uri: "/v1/admin/roles/r1"}},
			wantOut:    "deleted role dev\n" + fmt.Sprintf(off, "dev-access", "dev-access") + fmt.Sprintf(off, "dev-guardrails", "dev-guardrails"),
		},
		{
			name:       "no set turned off prints the deleted line alone",
			args:       []string{"roles", "delete", "ops", "--yes"},
			wantWrites: []rolesRequest{{method: http.MethodDelete, uri: "/v1/admin/roles/r2"}},
			wantOut:    "deleted role ops\n",
		},
		{
			name:    "an unknown role sends nothing",
			args:    []string{"roles", "delete", "ghost", "--yes"},
			wantErr: `no role named "ghost"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &rolesServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), tc.args...)
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
			if len(writes) != len(tc.wantWrites) {
				t.Fatalf("wire = %+v, want %+v", writes, tc.wantWrites)
			}
			for i, want := range tc.wantWrites {
				if writes[i].method != want.method || writes[i].uri != want.uri || strings.TrimSpace(writes[i].body) != want.body {
					t.Errorf("wire[%d] = %+v, want %+v", i, writes[i], want)
				}
			}
		})
	}
}
