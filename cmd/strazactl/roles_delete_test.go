package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rolesDeleteServer is the roles list plus the delete route: r1 (dev)
// deletes, r2 (ops) is a decider in a live set and is refused.
func rolesDeleteServer(t *testing.T, s *rolesServer) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("GET /v1/admin/roles", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"r1","name":"dev","kind":"business"},{"id":"r2","name":"ops","kind":"approver"}]`))
	})
	mux.HandleFunc("DELETE /v1/admin/roles/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		if r.PathValue("id") == "r2" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"role \"ops\" is named among the deciders of a live policy (dev-guardrails): deleting it would leave those approvals to expire unanswered. Remove it from approve.roles and publish the set again, or turn the set off, then delete the role"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"deleted"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestRolesDelete pins the confirm shape (the question, y or --yes, an
// abort with no write), the name resolution, and the server's refusal.
func TestRolesDelete(t *testing.T) {
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
			args:       []string{"roles", "delete", "dev", "--yes"},
			wantOut:    "deleted role dev\n",
			wantDelete: "/v1/admin/roles/r1",
		},
		{
			name:       "y at the prompt deletes",
			args:       []string{"roles", "delete", "dev"},
			stdin:      "y\n",
			wantOut:    "delete role dev? Its assignments and access rows go with it. [y/N] deleted role dev\n",
			wantDelete: "/v1/admin/roles/r1",
		},
		{
			name:    "an empty answer aborts before any write",
			args:    []string{"roles", "delete", "dev"},
			stdin:   "\n",
			wantOut: "delete role dev? Its assignments and access rows go with it. [y/N] ",
			wantErr: "aborted (pass --yes for non-interactive runs)",
		},
		{
			name:    "an unknown role never reaches the wire",
			args:    []string{"roles", "delete", "ghost", "--yes"},
			wantErr: `no role named "ghost"`,
		},
		{
			name:       "the server's refusal surfaces verbatim",
			args:       []string{"roles", "delete", "ops", "--yes"},
			wantErr:    `role "ops" is named among the deciders of a live policy (dev-guardrails)`,
			wantDelete: "/v1/admin/roles/r2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &rolesServer{}
			srv := rolesDeleteServer(t, s)
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
