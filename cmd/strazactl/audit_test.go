package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// auditServer answers the tail read with three records: two mcp records
// for different apps and one admin record that mentions the app name in
// its reason only, the case the data.app match must drop. A case that sets
// rows gets that JSON array instead.
type auditServer struct {
	mu   sync.Mutex
	uris []string
	rows string
}

func (s *auditServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("GET /v1/admin/audit", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.uris = append(s.uris, r.URL.RequestURI())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if s.rows != "" {
			_, _ = w.Write([]byte(s.rows))
			return
		}
		_, _ = w.Write([]byte(`[` +
			`{"seq":12,"ce":"{\"type\":\"straza.audit.mcp\",\"data\":{\"app\":\"scout-tools\",\"toolName\":\"echo\"}}","prevHash":"p","hash":"h","username":"joe"},` +
			`{"seq":11,"ce":"{\"type\":\"straza.audit.mcp\",\"data\":{\"app\":\"demo-tools\",\"toolName\":\"echo\"}}","prevHash":"p","hash":"h","username":"joe"},` +
			`{"seq":10,"ce":"{\"type\":\"straza.audit.admin\",\"data\":{\"action\":\"policy.create\",\"reason\":\"gate for scout-tools\"}}","prevHash":"p","hash":"h","username":"alice"}` +
			`]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestAuditTailFilters pins the type and app flags: the type passes through, the
// app rides the server's text search as q and is then matched on data.app,
// and the window keeps its tail(1) order.
func TestAuditTailFilters(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantURI  string
		wantSeqs []string
	}{
		{
			name:     "no filter prints the window oldest first",
			args:     []string{"audit", "tail", "--limit", "3"},
			wantURI:  "/v1/admin/audit?order=desc&limit=3",
			wantSeqs: []string{"#10", "#11", "#12"},
		},
		{
			name:     "type passes through",
			args:     []string{"audit", "tail", "--type", "straza.audit.mcp"},
			wantURI:  "/v1/admin/audit?order=desc&limit=50&type=straza.audit.mcp",
			wantSeqs: []string{"#10", "#11", "#12"},
		},
		{
			name:     "app rides q and keeps only data.app matches",
			args:     []string{"audit", "tail", "--type", "straza.audit.mcp", "--app", "scout-tools"},
			wantURI:  "/v1/admin/audit?order=desc&limit=50&type=straza.audit.mcp&q=scout-tools",
			wantSeqs: []string{"#12"},
		},
		{
			name:     "user, type and app compose",
			args:     []string{"audit", "tail", "--user", "joe", "--app", "demo-tools"},
			wantURI:  "/v1/admin/audit?order=desc&limit=50&user=joe&q=demo-tools",
			wantSeqs: []string{"#11"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &auditServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), tc.args...)
			if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if len(s.uris) != 1 || s.uris[0] != tc.wantURI {
				t.Errorf("wire = %v, want %s", s.uris, tc.wantURI)
			}
			var seqs []string
			for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
				if line != "" {
					seqs = append(seqs, strings.Fields(line)[0])
				}
			}
			if strings.Join(seqs, " ") != strings.Join(tc.wantSeqs, " ") {
				t.Errorf("printed %v, want %v:\n%s", seqs, tc.wantSeqs, stdout)
			}
		})
	}
}

// TestAuditTailUserPrintsEveryKind pins that the user filter is the
// server's alone: the CLI sends it on the wire and prints every row the
// server returns for that user, a decision record and an identity record
// alike, with the bracketed username only when the server resolved one.
// An identity record carries the user as an id inside its CloudEvent, so a
// client-side match on the row's username would drop it the moment the
// server cannot name the id.
func TestAuditTailUserPrintsEveryKind(t *testing.T) {
	tests := []struct {
		name string
		row  string
		want string
	}{
		{
			name: "decision record",
			row:  `{"seq":21,"ce":"{\"type\":\"straza.audit.mcp\",\"data\":{\"app\":\"demo-tools\",\"toolName\":\"echo\",\"user\":\"u-joe\"}}","prevHash":"p","hash":"h","username":"joe"}`,
			want: `#21 [joe] {"type":"straza.audit.mcp","data":{"app":"demo-tools","toolName":"echo","user":"u-joe"}}`,
		},
		{
			name: "identity record with the username resolved",
			row:  `{"seq":22,"ce":"{\"type\":\"straza.audit.identity\",\"data\":{\"action\":\"user.killed\",\"origin\":\"scim\",\"user\":\"u-joe\"}}","prevHash":"p","hash":"h","username":"joe"}`,
			want: `#22 [joe] {"type":"straza.audit.identity","data":{"action":"user.killed","origin":"scim","user":"u-joe"}}`,
		},
		{
			name: "identity record the server could not name",
			row:  `{"seq":23,"ce":"{\"type\":\"straza.audit.identity\",\"data\":{\"action\":\"user.killed\",\"user\":\"u-joe\"}}","prevHash":"p","hash":"h"}`,
			want: `#23 {"type":"straza.audit.identity","data":{"action":"user.killed","user":"u-joe"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &auditServer{rows: "[" + tc.row + "]"}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "audit", "tail", "--user", "joe")
			if err != nil {
				t.Fatalf("audit tail --user joe: %v", err)
			}
			if want := "/v1/admin/audit?order=desc&limit=50&user=joe"; len(s.uris) != 1 || s.uris[0] != want {
				t.Errorf("wire = %v, want %s", s.uris, want)
			}
			if got := strings.TrimSpace(stdout); got != tc.want {
				t.Errorf("printed %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAuditApp pins the data.app read, including records without one.
func TestAuditApp(t *testing.T) {
	tests := []struct{ ce, want string }{
		{`{"data":{"app":"scout-tools"}}`, "scout-tools"},
		{`{"data":{"action":"policy.create"}}`, ""},
		{`not json`, ""},
	}
	for _, tc := range tests {
		if got := auditApp(tc.ce); got != tc.want {
			t.Errorf("auditApp(%s) = %q, want %q", tc.ce, got, tc.want)
		}
	}
}
