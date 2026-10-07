package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// policyDeleteServer answers the delete route: a set that is off deletes, the active
// set dev-guardrails is refused, an unknown name is 404.
func policyDeleteServer(t *testing.T, s *rolesServer) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("DELETE /v1/admin/policies/{name}", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, nil)
		w.Header().Set("Content-Type", "application/json")
		switch r.PathValue("name") {
		case "dev-guardrails":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"the policy set is live. Turn it off first, then delete"}`))
		case "scout-role-access":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such policy set"}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestPolicyDelete pins the confirm shape and the server's answers.
func TestPolicyDelete(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		stdin      string
		wantOut    string
		wantErr    string
		wantDelete string
	}{
		{
			name:       "--yes deletes a set that is off",
			args:       []string{"policy", "delete", "scout-role-access", "--yes"},
			wantOut:    "deleted policy set scout-role-access\n",
			wantDelete: "/v1/admin/policies/scout-role-access",
		},
		{
			name:       "yes at the prompt deletes",
			args:       []string{"policy", "delete", "scout-role-access"},
			stdin:      "yes\n",
			wantOut:    "delete policy set scout-role-access? [y/N] deleted policy set scout-role-access\n",
			wantDelete: "/v1/admin/policies/scout-role-access",
		},
		{
			name:    "n aborts before any write",
			args:    []string{"policy", "delete", "scout-role-access"},
			stdin:   "n\n",
			wantOut: "delete policy set scout-role-access? [y/N] ",
			wantErr: "aborted (pass --yes for non-interactive runs)",
		},
		{
			name:       "an active set is refused with the server's sentence",
			args:       []string{"policy", "delete", "dev-guardrails", "--yes"},
			wantErr:    "the policy set is live. Turn it off first, then delete",
			wantDelete: "/v1/admin/policies/dev-guardrails",
		},
		{
			name:       "an unknown set is the server's 404",
			args:       []string{"policy", "delete", "ghost", "--yes"},
			wantErr:    "no such policy set",
			wantDelete: "/v1/admin/policies/ghost",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &rolesServer{}
			srv := policyDeleteServer(t, s)
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
