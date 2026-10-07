package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// usersServer is the admin surface the status verbs walk: the checkin a
// stored token refreshes through, the users list with one user in the
// status the case sets, and the status PATCH, whose bodies it records.
type usersServer struct {
	mu      sync.Mutex
	status  string
	patches []string // "<id> <body>"
}

func (s *usersServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("GET /v1/admin/users", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"u-joe","username":"joe","status":"` + s.status + `"}]`))
	})
	mux.HandleFunc("PATCH /v1/admin/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.patches = append(s.patches, r.PathValue("id")+" "+strings.TrimSpace(string(body)))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"u-joe","username":"joe","status":"active"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestUsersStatusVerbs pins enable and disable: the status each one writes,
// the no-op when the user is already active, the unknown username, and the
// line that says what happened and what lifts it.
func TestUsersStatusVerbs(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		args        []string
		wantPatches []string
		wantOut     string
		wantErr     string
	}{
		{
			name:        "enable a disabled user",
			status:      "disabled",
			args:        []string{"users", "enable", "joe"},
			wantPatches: []string{`u-joe {"status":"active"}`},
			wantOut:     "enabled joe. The status is active and the user's revocations are lifted. Sessions revoked earlier stay ended. A device the user enrolled starts a new session at its next check-in with no new enrollment, unless it was revoked on its own or its device credential expired meanwhile: such a device needs straza enroll again\n",
		},
		{
			name:    "enable an active user writes nothing",
			status:  "active",
			args:    []string{"users", "enable", "joe"},
			wantOut: "joe is already active. A lock is lifted with `strazactl users unlock joe`\n",
		},
		{
			name:        "disable an active user",
			status:      "active",
			args:        []string{"users", "disable", "joe"},
			wantPatches: []string{`u-joe {"status":"disabled"}`},
			wantOut:     "disabled joe. Sessions revoked, lift with `strazactl users enable joe`\n",
		},
		{
			name:    "enable an unknown user names it",
			status:  "active",
			args:    []string{"users", "enable", "nobody"},
			wantErr: `no user named "nobody"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &usersServer{status: tc.status}
			srv := s.start(t)
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
			if strings.Join(s.patches, "\n") != strings.Join(tc.wantPatches, "\n") {
				t.Errorf("patches = %v, want %v", s.patches, tc.wantPatches)
			}
		})
	}
}
