package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestCreateNHIType pins `strazactl users create-nhi`: --type is required and
// takes agent or service, a missing or other value is refused with the
// sentence naming both before any request, and the chosen type travels as
// user_type on the create.
func TestCreateNHIType(t *testing.T) {
	const hint = "Use --type agent for an AI agent that acts for a person, or --type service for a technical account with no agency"
	tests := []struct {
		name     string
		args     []string
		wantErr  string
		wantType string
		wantOut  string
	}{
		{
			name:    "no --type",
			args:    []string{"users", "create-nhi", "build-bot"},
			wantErr: "create-nhi needs --type, because Straza must know whether build-bot is an AI agent or a service account. " + hint,
		},
		{
			name:    "a person's type",
			args:    []string{"users", "create-nhi", "build-bot", "--type", "human"},
			wantErr: `--type "human" is neither agent nor service. ` + hint,
		},
		{
			name:     "agent",
			args:     []string{"users", "create-nhi", "build-bot", "--type", "agent"},
			wantType: "agent",
			wantOut:  "created the AI agent build-bot (u-bot). Register its key: strazactl users nhi-key set build-bot <publicKey>\n",
		},
		{
			name:     "service",
			args:     []string{"users", "create-nhi", "build-bot", "--type", "service"},
			wantType: "service",
			wantOut:  "created the service account build-bot (u-bot). Register its key: strazactl users nhi-key set build-bot <publicKey>\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var creates []map[string]any
			mux := http.NewServeMux()
			mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
				if tc.wantErr != "" {
					t.Error("a refused create-nhi refreshed its session")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
			})
			mux.HandleFunc("POST /v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
				if tc.wantErr != "" {
					t.Error("a refused create-nhi reached the server")
				}
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				_ = json.Unmarshal(raw, &body)
				mu.Lock()
				creates = append(creates, body)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"u-bot","username":"build-bot","kind":"nhi","user_type":"` + tc.wantType + `","status":"active"}`))
			})
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			t.Setenv("STRAZA_SERVER", "")

			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), tc.args...)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if stdout != tc.wantOut {
				t.Errorf("stdout = %q, want %q", stdout, tc.wantOut)
			}
			if len(creates) != 1 || creates[0]["user_type"] != tc.wantType || creates[0]["kind"] != "nhi" {
				t.Fatalf("creates = %v, want one kind nhi with user_type %q", creates, tc.wantType)
			}
		})
	}
}
