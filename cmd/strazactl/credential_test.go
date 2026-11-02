package main

import (
	"bytes"
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
// records the method, the URI and the bearer of every request.
type credServer struct {
	mu   sync.Mutex
	seen []string
}

func (s *credServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/admin/roles":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"r9","name":"release-team","kind":"business"}`))
		case "GET /v1/connect":
			_, _ = w.Write([]byte(`[{"app":"github","kind":"token","agents":"owner","connected":true,"fingerprint":"ab12","allow_agents":false}]`))
		case "PATCH /v1/connect/github":
			_, _ = w.Write([]byte(`{"app":"github","user":"joe","allow_agents":true}`))
		case "DELETE /v1/connect/github":
			_, _ = w.Write([]byte(`{"app":"github","user":"joe","status":"disconnected"}`))
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

// noTokenWords is the refusal of a run without a token.
const noTokenWords = "strazactl needs an admin API token in STRAZA_API_TOKEN. A person mints one in the console under Settings, API tokens, " +
	"with the scope these commands need and an expiry"

// TestAPITokenFromTheEnvironment pins STRAZA_API_TOKEN end to end: the token
// is the bearer of every call, stdout carries the command's own output, and
// nothing is said on stderr, whether the server comes from STRAZA_SERVER or
// from --server.
func TestAPITokenFromTheEnvironment(t *testing.T) {
	for _, viaFlag := range []bool{false, true} {
		name := "STRAZA_SERVER"
		if viaFlag {
			name = "--server"
		}
		t.Run(name, func(t *testing.T) {
			s := &credServer{}
			srv := s.start(t)
			creds := onToken(t, srv.URL)
			args := []string{"roles", "create", "release-team", "--kind", "business"}
			if viaFlag {
				t.Setenv("STRAZA_SERVER", "")
				args = append([]string{"--server", srv.URL}, args...)
			}
			stdout, stderr, err := runCLI(t, creds, args...)
			if err != nil {
				t.Fatalf("roles create on a token: %v", err)
			}
			if want := "created role release-team (r9)\n"; stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want nothing", stderr)
			}
			want := []string{"POST /v1/admin/roles Bearer " + testToken}
			if got := s.requests(); strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("requests = %q, want %q", got, want)
			}
		})
	}
}

// TestNoTokenNamesTheConsole pins the refusal of a run without a token: a
// command that needs one sends nothing and says where a person mints one,
// and when the old login file is still there it says the file works nowhere
// and can be deleted. Without a token the retired login and logout give the
// same answer, with the exit status of a retired verb, whatever flags the
// old verbs took.
func TestNoTokenNamesTheConsole(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		oldLogin bool
		wantCode int
	}{
		{name: "a command with no token", args: []string{"roles", "list"}, wantCode: 1},
		{name: "a command with the old login still on disk", args: []string{"roles", "list"}, oldLogin: true, wantCode: 1},
		{name: "a write with no token", args: []string{"roles", "create", "release-team", "--kind", "business"}, wantCode: 1},
		{name: "login", args: []string{"login"}, wantCode: 2},
		{name: "login with the old flags", args: []string{"login", "--break-glass", "--use-device-code"}, oldLogin: true, wantCode: 2},
		{name: "logout", args: []string{"logout"}, oldLogin: true, wantCode: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &credServer{}
			t.Setenv("STRAZA_SERVER", s.start(t).URL)
			t.Setenv(apiTokenEnv, "")
			path := noCreds(t)
			want := noTokenWords
			if tc.oldLogin {
				if err := os.WriteFile(path, []byte(`{"server":"https://old.example","session_token":"stok-1"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				want += ". strazactl no longer signs in, so the login kept in " + path + " works nowhere and can be deleted"
			}
			var out bytes.Buffer
			root := newRootCmd(path)
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(tc.args)
			cmd, err := root.ExecuteC()
			if err == nil || err.Error() != want {
				t.Fatalf("err\n got %v\nwant %s", err, want)
			}
			if code := exitCode(cmd, err); code != tc.wantCode {
				t.Errorf("exit = %d, want %d", code, tc.wantCode)
			}
			if out.Len() != 0 {
				t.Errorf("printed %q, want nothing", out.String())
			}
			if got := s.requests(); len(got) != 0 {
				t.Errorf("a run without a token sent %q", got)
			}
		})
	}
}

// TestRetiredLoginWithAToken pins login and logout while STRAZA_API_TOKEN
// is set: whatever flags the old verbs took, they say there is nothing to
// sign in or out of, send nothing and exit 2.
func TestRetiredLoginWithAToken(t *testing.T) {
	const want = "strazactl no longer signs in. It runs on the admin API token in STRAZA_API_TOKEN, so there is nothing to sign in or out of"
	for _, args := range [][]string{{"login"}, {"login", "--break-glass", "--use-device-code"}, {"logout"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := &credServer{}
			path := onToken(t, s.start(t).URL)
			if err := os.WriteFile(path, []byte(`{"server":"https://old.example","session_token":"stok-1"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			root := newRootCmd(path)
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(args)
			cmd, err := root.ExecuteC()
			if err == nil || err.Error() != want {
				t.Fatalf("err\n got %v\nwant %s", err, want)
			}
			if code := exitCode(cmd, err); code != 2 || out.Len() != 0 {
				t.Errorf("exit %d, printed %q, want 2 and nothing printed", code, out.String())
			}
			if got := s.requests(); len(got) != 0 {
				t.Errorf("a retired verb sent %q", got)
			}
		})
	}
}

// TestNoTokenExitsOneThroughMain runs strazactl's main in a child process
// with no token and the old login file under HOME, and pins the refusal's
// exit status and its one stderr line, which names that file.
func TestNoTokenExitsOneThroughMain(t *testing.T) {
	if os.Getenv("STRAZACTL_TEST_MAIN") == "1" {
		os.Args = []string{"strazactl", "--server", "http://127.0.0.1:1", "roles", "list"}
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
	cmd := exec.Command(os.Args[0], "-test.run=^TestNoTokenExitsOneThroughMain$")
	cmd.Env = append(os.Environ(), "STRAZACTL_TEST_MAIN=1", "HOME="+home, "STRAZA_SERVER=", apiTokenEnv+"=")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want status 1\nstderr: %s", err, stderr.String())
	}
	want := "strazactl: " + noTokenWords + ". strazactl no longer signs in, so the login kept in " + creds + " works nowhere and can be deleted\n"
	if stderr.String() != want {
		t.Errorf("stderr\n got %q\nwant %q", stderr.String(), want)
	}
}

// TestConnectNamesAUser pins connect and disconnect on a token: without
// --user they refuse before anything is sent and name the self-service page
// where a person connects their own account, and with --user each call goes
// out on the token for the named user.
func TestConnectNamesAUser(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantErr      bool
		wantRequests []string
	}{
		{name: "connect without --user", args: []string{"connect", "github"}, wantErr: true},
		{name: "connect with no server and no --user", args: []string{"connect"}, wantErr: true},
		{name: "disconnect without --user", args: []string{"disconnect", "github"}, wantErr: true},
		{
			name:         "connect lists the user's connections",
			args:         []string{"connect", "--user", "joe"},
			wantRequests: []string{"GET /v1/connect?user=joe Bearer " + testToken},
		},
		{
			name:         "connect switches allow-agents for the user",
			args:         []string{"connect", "github", "--user", "joe", "--allow-agents"},
			wantRequests: []string{"PATCH /v1/connect/github Bearer " + testToken},
		},
		{
			name:         "disconnect removes the user's connection",
			args:         []string{"disconnect", "github", "--user", "joe"},
			wantRequests: []string{"DELETE /v1/connect/github?user=joe Bearer " + testToken},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &credServer{}
			srv := s.start(t)
			_, _, err := runCLI(t, onToken(t, srv.URL), tc.args...)
			if tc.wantErr {
				want := "strazactl changes other users' connections, on an admin API token, so name the user with --user. " +
					"Connect your own account with straza connect or on the Credentials tab of the self-service page: " + srv.URL + "/self-service/credentials"
				if err == nil || err.Error() != want {
					t.Fatalf("err\n got %v\nwant %s", err, want)
				}
			} else if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if got := s.requests(); strings.Join(got, "|") != strings.Join(tc.wantRequests, "|") {
				t.Errorf("requests = %q, want %q", got, tc.wantRequests)
			}
		})
	}
}
