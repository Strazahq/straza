package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestUsersEntraAgentID pins `strazactl users entra-agent-id`: set sends the
// value and prints the id the server stored, unset sends an empty value,
// show prints the id, how to set one on an agent or why anyone else cannot
// hold one, and a refusal prints the server's sentence as it came.
func TestUsersEntraAgentID(t *testing.T) {
	const (
		id      = "dc013442-2b7a-4c1e-9f1a-0123456789ab"
		refused = "dc013442-2b7a-4c1e-9f1a-0123456789ab is already the Entra agent identity of joe, and two agents cannot share one."
	)
	tests := []struct {
		name     string
		args     []string
		held     string // the id the listed user holds
		userType string // the listed user's user_type: agent when empty, left out when "none"
		patch    string // the PATCH answer, empty for no PATCH
		code     int
		wantBody map[string]any
		wantOut  string
		wantErr  string
	}{
		{
			name: "set", args: []string{"users", "entra-agent-id", "set", "sam-sre-agent", "DC013442-2B7A-4C1E-9F1A-0123456789AB"},
			patch: `{"id":"u-sam","username":"sam-sre-agent","entra_agent_id":"` + id + `"}`, code: http.StatusOK,
			wantBody: map[string]any{"entra_agent_id": "DC013442-2B7A-4C1E-9F1A-0123456789AB"},
			wantOut:  "sam-sre-agent now acts at Entra as the agent identity " + id + ".\n",
		},
		{
			name: "unset", args: []string{"users", "entra-agent-id", "unset", "sam-sre-agent"}, held: id,
			patch: `{"id":"u-sam","username":"sam-sre-agent"}`, code: http.StatusOK,
			wantBody: map[string]any{"entra_agent_id": ""},
			wantOut:  "sam-sre-agent no longer has an Entra agent identity.\n",
		},
		{
			name: "show an id", args: []string{"users", "entra-agent-id", "show", "sam-sre-agent"}, held: id,
			wantOut: "sam-sre-agent acts at Entra as the agent identity " + id + ".\n",
		},
		{
			name: "show none on an agent", args: []string{"users", "entra-agent-id", "show", "sam-sre-agent"},
			wantOut: "sam-sre-agent has no Entra agent identity. Set one with strazactl users entra-agent-id set sam-sre-agent <client id>.\n",
		},
		{
			name: "show none on a person", args: []string{"users", "entra-agent-id", "show", "sam-sre-agent"}, userType: "human",
			wantOut: "sam-sre-agent has no Entra agent identity and cannot hold one, because sam-sre-agent is a person and Straza gets Entra tokens for agents only. " +
				"If sam-sre-agent is an agent, set its type to agent first, from the identity manager with userType agent or over the admin API with user_type agent, " +
				"then run strazactl users entra-agent-id set sam-sre-agent <client id>.\n",
		},
		{
			name: "show none on a service account", args: []string{"users", "entra-agent-id", "show", "sam-sre-agent"}, userType: "service",
			wantOut: "sam-sre-agent has no Entra agent identity and cannot hold one, because sam-sre-agent is a service account and Straza gets Entra tokens for agents only. " +
				"If sam-sre-agent is an agent, set its type to agent first, from the identity manager with userType agent or over the admin API with user_type agent, " +
				"then run strazactl users entra-agent-id set sam-sre-agent <client id>.\n",
		},
		{
			name: "show none on a user with no type", args: []string{"users", "entra-agent-id", "show", "sam-sre-agent"}, userType: "none",
			wantOut: "sam-sre-agent has no Entra agent identity and cannot hold one, because sam-sre-agent is unclassified, with no user type, and Straza gets Entra tokens for agents only. " +
				"If sam-sre-agent is an agent, set its type to agent first, from the identity manager with userType agent or over the admin API with user_type agent, " +
				"then run strazactl users entra-agent-id set sam-sre-agent <client id>.\n",
		},
		{
			name: "a refusal", args: []string{"users", "entra-agent-id", "set", "sam-sre-agent", id},
			patch: `{"error":"` + refused + `"}`, code: http.StatusConflict,
			wantBody: map[string]any{"entra_agent_id": id}, wantErr: refused,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var patches []map[string]any
			mux := http.NewServeMux()
			mux.HandleFunc("GET /v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				userType := `,"user_type":"` + tc.userType + `"`
				switch tc.userType {
				case "":
					userType = `,"user_type":"agent"`
				case "none":
					userType = ""
				}
				_, _ = w.Write([]byte(`[{"id":"u-sam","username":"sam-sre-agent","entra_agent_id":"` + tc.held + `"` + userType + `}]`))
			})
			mux.HandleFunc("PATCH /v1/admin/users/u-sam", func(w http.ResponseWriter, r *http.Request) {
				if tc.patch == "" {
					t.Error("show sent a PATCH")
				}
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				_ = json.Unmarshal(raw, &body)
				mu.Lock()
				patches = append(patches, body)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.patch))
			})
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			t.Setenv("STRAZA_SERVER", "")

			stdout, _, err := runCLI(t, onToken(t, srv.URL), tc.args...)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if stdout != tc.wantOut {
				t.Errorf("stdout = %q, want %q", stdout, tc.wantOut)
			}
			if tc.wantBody == nil {
				if len(patches) != 0 {
					t.Fatalf("patches = %v, want none", patches)
				}
				return
			}
			if len(patches) != 1 || len(patches[0]) != 1 || patches[0]["entra_agent_id"] != tc.wantBody["entra_agent_id"] {
				t.Fatalf("patches = %v, want one with only %v", patches, tc.wantBody)
			}
		})
	}
}
