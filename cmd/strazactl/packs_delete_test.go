package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// packsDeleteServer is the packs list the client resolves names against plus
// the delete route: p1 (fin-rules) deletes, p2 (held) is bound to a role and
// is refused with the server's sentence.
func packsDeleteServer(t *testing.T, s *rolesServer) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("GET /v1/admin/packs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"p1","name":"fin-rules","version":"1","content":"no wire transfers"},` +
			`{"id":"p2","name":"held","version":"1","content":"kept"}]`))
	})
	mux.HandleFunc("DELETE /v1/admin/packs/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") == "p2" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"the knowledge pack is still bound to the role finance, and deleting it would change what that role's sessions receive. Unbind it from the role first, then delete it"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"deleted"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestPacksDelete pins the confirm shape (the question, y or --yes, an abort
// with no write), the name resolution, and the server's refusal.
func TestPacksDelete(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		stdin      string
		wantOut    string
		wantErr    string
		wantDelete string
	}{
		{
			name:       "--yes deletes without a question",
			args:       []string{"packs", "delete", "fin-rules", "--yes"},
			wantOut:    "deleted knowledge pack fin-rules\n",
			wantDelete: "/v1/admin/packs/p1",
		},
		{
			name:       "y at the prompt deletes",
			args:       []string{"packs", "delete", "fin-rules"},
			stdin:      "y\n",
			wantOut:    "delete knowledge pack fin-rules? [y/N] deleted knowledge pack fin-rules\n",
			wantDelete: "/v1/admin/packs/p1",
		},
		{
			name:    "an empty answer aborts before any write",
			args:    []string{"packs", "delete", "fin-rules"},
			stdin:   "\n",
			wantOut: "delete knowledge pack fin-rules? [y/N] ",
			wantErr: "aborted (pass --yes for non-interactive runs)",
		},
		{
			name:    "an unknown pack never reaches the wire",
			args:    []string{"packs", "delete", "ghost", "--yes"},
			wantErr: `no pack named "ghost"`,
		},
		{
			name:       "the server's refusal surfaces verbatim",
			args:       []string{"packs", "delete", "held", "--yes"},
			wantErr:    "the knowledge pack is still bound to the role finance",
			wantDelete: "/v1/admin/packs/p2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &rolesServer{}
			srv := packsDeleteServer(t, s)
			t.Setenv("STRAZA_SERVER", "")
			stdout, err := runCLIIn(t, writeCreds(t, srv.URL), tc.stdin, tc.args...)
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
			switch {
			case tc.wantDelete == "" && len(writes) != 0:
				t.Errorf("wire = %+v, want no write", writes)
			case tc.wantDelete != "" && (len(writes) != 1 || writes[0].method != http.MethodDelete || writes[0].uri != tc.wantDelete):
				t.Errorf("wire = %+v, want one DELETE %s", writes, tc.wantDelete)
			}
		})
	}
}
