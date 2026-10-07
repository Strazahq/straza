package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// credServer answers the reads and writes the credential tests drive and
// records the method, path and bearer of every request, check-ins included.
type credServer struct {
	mu   sync.Mutex
	seen []string
}

func (s *credServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/checkin":
			_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
		case "GET /v1/admin/roles":
			_, _ = w.Write([]byte(`[{"id":"r1","name":"dev-tools","kind":"application","description":"reaches demo-tools"}]`))
		case "POST /v1/admin/roles":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"r9","name":"release-team","kind":"business"}`))
		case "POST /v1/session/revoke":
			_, _ = w.Write([]byte(`{"session":"ses-1"}`))
		case "POST /v1/admin/policies/simulate":
			_, _ = w.Write([]byte(`{"active":{"effect":"deny","ruleId":"no-rm-rf","setName":"standalone-starter",` +
				`"reason":"Straza: recursive force-delete is denied"},"subject":{"roles":["dev"]},"snapshot":"4f2a9c01d7e6b3a8"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such route in the test server"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *credServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// TestAPITokenFromTheEnvironment pins STRAZA_API_TOKEN end to end: the token
// is the bearer of every admin call, no check-in happens, stdout carries the
// command's own output, and one stderr line names the variable and the
// server the token goes to. A --server away from the stored login gets that
// line alone, because the override warning advises a login, which a token
// replaces.
func TestAPITokenFromTheEnvironment(t *testing.T) {
	for _, loginAt := range []string{"", "https://prod.example"} {
		name := "login on the same server"
		if loginAt != "" {
			name = "--server away from the login"
		}
		t.Run(name, func(t *testing.T) {
			s := &credServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			t.Setenv(apiTokenEnv, "wat_abc")
			args := []string{"roles", "create", "release-team", "--kind", "business"}
			creds := writeCreds(t, srv.URL)
			if loginAt != "" {
				creds = writeCreds(t, loginAt)
				args = append([]string{"--server", srv.URL}, args...)
			}
			stdout, stderr, err := runCLI(t, creds, args...)
			if err != nil {
				t.Fatalf("roles create on a token: %v", err)
			}
			if want := "created role release-team (r9)\n"; stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			if want := tokenNotice(srv.URL) + "\n"; stderr != want {
				t.Errorf("stderr = %q, want the one notice %q", stderr, want)
			}
			want := []string{"POST /v1/admin/roles Bearer wat_abc"}
			if got := s.requests(); strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("requests = %q, want %q", got, want)
			}
		})
	}
}

// TestPersonVerbsUnderAToken pins what the person verbs do while
// STRAZA_API_TOKEN is set: login refuses before any request, because the
// login would never be used. connect and disconnect refuse before the token
// leaves the machine, because their routes take a person's session. logout
// ends the stored login on its own session token and says the token keeps
// working.
func TestPersonVerbsUnderAToken(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantErr      string
		wantRequests []string
		wantStderr   string
	}{
		{
			name:    "login refuses",
			args:    []string{"login"},
			wantErr: errLoginWithToken.Error(),
		},
		{
			name:       "connect refuses",
			args:       []string{"connect", "github"},
			wantErr:    "this command acts as a person and needs a strazactl login, but STRAZA_API_TOKEN is set",
			wantStderr: "note: strazactl uses the admin API token in STRAZA_API_TOKEN",
		},
		{
			name:       "disconnect refuses",
			args:       []string{"disconnect", "github"},
			wantErr:    "Unset STRAZA_API_TOKEN, then run the command again on your login",
			wantStderr: "note: strazactl uses the admin API token in STRAZA_API_TOKEN",
		},
		{
			name:         "logout ends the login alone",
			args:         []string{"logout"},
			wantRequests: []string{"POST /v1/session/revoke Bearer stok-1"},
			wantStderr:   logoutTokenNote + "\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &credServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			t.Setenv(apiTokenEnv, "wat_abc")
			_, stderr, err := runCLI(t, writeCreds(t, srv.URL), tc.args...)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if got := s.requests(); strings.Join(got, "|") != strings.Join(tc.wantRequests, "|") {
				t.Errorf("requests = %q, want %q", got, tc.wantRequests)
			}
			if !strings.HasPrefix(stderr, tc.wantStderr) {
				t.Errorf("stderr = %q, want it to start with %q", stderr, tc.wantStderr)
			}
			if strings.HasPrefix(tc.name, "logout") && strings.Contains(stderr, "strazactl uses the admin API token") {
				t.Errorf("logout never sends the token, so it prints no token notice:\n%s", stderr)
			}
		})
	}
}

// TestTokenWithoutServerNamesTheWayOut pins the no-server error under a
// token: the generic one advises a login, which a token refuses.
func TestTokenWithoutServerNamesTheWayOut(t *testing.T) {
	t.Setenv("STRAZA_SERVER", "")
	t.Setenv(apiTokenEnv, "wat_abc")
	_, _, err := runCLI(t, noCreds(t), "roles", "list")
	if !errors.Is(err, errTokenWithoutServer) {
		t.Fatalf("err = %v, want errTokenWithoutServer", err)
	}
}

// TestGuardInsideACodingAgent pins the guard end to end: inside a coding
// agent a write on the login is refused before any request, a read on the
// login works, and a write on a token works.
func TestGuardInsideACodingAgent(t *testing.T) {
	tests := []struct {
		name         string
		token        string
		args         []string
		wantErr      string
		wantRequests []string
	}{
		{
			name:    "a write on the login is refused",
			args:    []string{"roles", "create", "release-team", "--kind", "business"},
			wantErr: "GEMINI_CLI is set, so strazactl runs inside a coding agent, and this command changes Straza",
		},
		{
			name:         "a read on the login works",
			args:         []string{"roles", "list"},
			wantRequests: []string{"POST /v1/checkin ", "GET /v1/admin/roles Bearer stok-2"},
		},
		{
			name:         "a write on a token works",
			token:        "wat_abc",
			args:         []string{"roles", "create", "release-team", "--kind", "business"},
			wantRequests: []string{"POST /v1/admin/roles Bearer wat_abc"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &credServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			t.Setenv("GEMINI_CLI", "1")
			t.Setenv(apiTokenEnv, tc.token)
			_, _, err := runCLI(t, writeCreds(t, srv.URL), tc.args...)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if got := s.requests(); strings.Join(got, "|") != strings.Join(tc.wantRequests, "|") {
				t.Errorf("requests = %q, want %q", got, tc.wantRequests)
			}
		})
	}
}

// TestGuardLetsSimulateOutInsideACodingAgent pins the change-free list end to
// end: inside a coding agent, policy simulate runs on the login, and policy
// apply, which stores the set, is still refused before it is sent.
func TestGuardLetsSimulateOutInsideACodingAgent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "set-a.yaml")
	if err := os.WriteFile(file, []byte(splitDocA), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		args        []string
		wantErr     string
		wantStdout  string
		wantRequest string
	}{
		{
			name:        "simulate runs on the login",
			args:        []string{"policy", "simulate", "--roles", "dev", "--tool", "shell.exec", "--command", "rm -rf /tmp/x"},
			wantStdout:  "This call would be denied.\n",
			wantRequest: "POST /v1/admin/policies/simulate Bearer stok-2",
		},
		{
			name:    "apply is refused",
			args:    []string{"policy", "apply", "-f", file},
			wantErr: "CLAUDECODE is set, so strazactl runs inside a coding agent, and this command changes Straza",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &credServer{}
			srv := s.start(t)
			t.Setenv("STRAZA_SERVER", "")
			t.Setenv("CLAUDECODE", "1")
			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), tc.args...)
			got := s.requests()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				if len(got) != 0 {
					t.Errorf("the refused command reached the server: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("simulate inside a coding agent: %v", err)
			}
			if !strings.HasPrefix(stdout, tc.wantStdout) {
				t.Errorf("stdout = %q, want it to start with %q", stdout, tc.wantStdout)
			}
			if !strings.Contains(strings.Join(got, "|"), tc.wantRequest) {
				t.Errorf("requests = %q, want %q among them", got, tc.wantRequest)
			}
		})
	}
}

// TestGuardRefusalExitsOne runs strazactl's main in a child process inside a
// coding agent and pins the refusal's exit status and its one stderr line.
// The child is this test binary, which TestMain strips of markers, so the
// marker travels under another name and is set after TestMain ran.
func TestGuardRefusalExitsOne(t *testing.T) {
	if marker := os.Getenv("STRAZACTL_TEST_MARKER"); marker != "" {
		if err := os.Setenv(marker, "1"); err != nil {
			t.Fatal(err)
		}
		os.Args = []string{"strazactl", "--server", "http://127.0.0.1:1", "roles", "create", "release-team", "--kind", "business"}
		main()
		return
	}
	home := t.TempDir()
	creds := filepath.Join(home, ".straza", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(creds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte(`{"server":"http://127.0.0.1:1","session_token":"stok-1","session_id":"ses-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGuardRefusalExitsOne$")
	cmd.Env = append(os.Environ(), "STRAZACTL_TEST_MARKER=CLAUDECODE", "HOME="+home, "STRAZA_SERVER=", apiTokenEnv+"=")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want status 1\nstderr: %s", err, stderr.String())
	}
	if want := "strazactl: CLAUDECODE is set, so strazactl runs inside a coding agent"; !strings.HasPrefix(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to start with %q", stderr.String(), want)
	}
}

// TestLoginNoteWords pins the login note, which names the built-in straza
// MCP server as the place an agent proposes config changes.
func TestLoginNoteWords(t *testing.T) {
	const want = "note: this login can change Straza's configuration. Keep it away from coding agents. " +
		"Automation uses an admin API token in STRAZA_API_TOKEN, and an agent proposes config changes through the built-in straza MCP server."
	if loginNote != want {
		t.Errorf("loginNote\n got %s\nwant %s", loginNote, want)
	}
}

// TestLoginNoteFollowsASuccessfulLogin pins the line a login prints on
// stderr once it succeeded, and only then.
func TestLoginNoteFollowsASuccessfulLogin(t *testing.T) {
	t.Setenv("STRAZA_SERVER", "")
	t.Run("a login prints it last", func(t *testing.T) {
		srv := fakeLoginServer(t)
		_, stderr, err := runCLI(t, writeCreds(t, srv.URL), "login")
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		if want := loginNote + "\n"; !strings.HasSuffix(stderr, want) {
			t.Errorf("stderr = %q, want it to end with %q", stderr, want)
		}
	})
	t.Run("a refused login does not", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /.well-known/straza/idp.json", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		mux.HandleFunc("POST /oidc/device_authorization", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"device_code":"dc-1","user_code":"WXYZ-2345","verification_uri_complete":"https://issuer.example/device","expires_in":600,"interval":1}`))
		})
		mux.HandleFunc("POST /oidc/token", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"error":"access_denied"}`))
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		_, stderr, err := runCLI(t, writeCreds(t, srv.URL), "login")
		if err == nil {
			t.Fatal("a denied device login succeeded")
		}
		if strings.Contains(stderr, loginNote) {
			t.Errorf("a refused login printed the login note:\n%s", stderr)
		}
	})
}
