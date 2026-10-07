package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/ctl"
)

// runCLI drives the real cobra tree with an injected credentials path, so a
// test never touches the developer's own ~/.straza.
func runCLI(t *testing.T, credsPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	root := newRootCmd(credsPath)
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// writeCreds lays down a credentials file recording server as the logged-in
// deployment, and returns its path.
func writeCreds(t *testing.T, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	body := fmt.Sprintf(`{"server":%q,"session_token":"stok-1","session_id":"ses-1"}`, server)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	return path
}

// noCreds returns a path where no credentials file exists.
func noCreds(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "credentials.json")
}

// TestNoServerAnywhereIsALoudError: with no --server, no $STRAZA_SERVER and no
// login, a server-touching command must say so instead of quietly dialling
// 127.0.0.1:8420, which aims it at whatever listens there.
func TestNoServerAnywhereIsALoudError(t *testing.T) {
	t.Setenv("STRAZA_SERVER", "")
	_, _, err := runCLI(t, noCreds(t), "users", "list")
	if !errors.Is(err, ctl.ErrNoServer) {
		t.Fatalf("err = %v, want ErrNoServer", err)
	}
	const want = "not logged in and no server given. Run `strazactl login --server <url>`"
	if err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
}

// TestOverrideWarning pins the mismatch guard: an override that disagrees
// with the stored login warns on STDERR and proceeds, for the env var just
// as loudly as for the flag, because an env var that decides silently sends
// commands to a target the operator did not see.
func TestOverrideWarning(t *testing.T) {
	const login = "https://prod.example"
	tests := []struct {
		name     string
		flagArgs []string
		env      string
		creds    string // "" = no credentials file
		want     string // "" = no warning at all
	}{
		{
			name:     "flag override warns and names the flag",
			flagArgs: []string{"--server", "http://127.0.0.1:8420"}, creds: login,
			want: "warning: --server targets http://127.0.0.1:8420 but you are logged into https://prod.example" +
				". Credentials may not be valid there; run `strazactl login --server http://127.0.0.1:8420` to switch",
		},
		{
			name: "env override warns and names the variable",
			env:  "http://127.0.0.1:8420", creds: login,
			want: "warning: $STRAZA_SERVER targets http://127.0.0.1:8420 but you are logged into https://prod.example" +
				". Credentials may not be valid there; run `strazactl login --server http://127.0.0.1:8420` to switch",
		},
		{
			name:     "flag agreeing with the login is silent",
			flagArgs: []string{"--server", login}, creds: login,
		},
		{
			name:     "a bare trailing slash is not a mismatch",
			flagArgs: []string{"--server", login + "/"}, creds: login,
		},
		{
			name: "env agreeing with the login is silent",
			env:  login, creds: login,
		},
		{
			name:     "no credentials means nothing to disagree with",
			flagArgs: []string{"--server", "http://127.0.0.1:8420"},
		},
		{
			name:  "no override at all is silent",
			creds: login,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STRAZA_SERVER", tc.env)
			credsPath := noCreds(t)
			if tc.creds != "" {
				credsPath = writeCreds(t, tc.creds)
			}
			// `users list` is a plain server command; the target is unreachable
			// on purpose; the warning is emitted before the call is made.
			args := append(append([]string{}, tc.flagArgs...), "users", "list")
			_, stderr, _ := runCLI(t, credsPath, args...)
			got := strings.TrimSpace(stderr)
			if got != tc.want {
				t.Fatalf("stderr = %q, want %q", got, tc.want)
			}
			if tc.want != "" && strings.Count(stderr, "warning:") != 1 {
				t.Errorf("warning must be emitted exactly once per invocation:\n%s", stderr)
			}
		})
	}
}

// TestLocalCommandsNeedNoServer keeps resolution lazy: a command that never
// contacts strazad runs with no login, no flag and no environment. Failing
// this would make `strazactl policy validate` in CI demand a deployment.
func TestLocalCommandsNeedNoServer(t *testing.T) {
	const policyExample = "../../spec/policyset/examples/valid-minimal.yaml"
	tests := []struct {
		name   string
		args   []string
		wantOK bool
	}{
		{"version", []string{"version"}, true},
		{"help subcommand", []string{"help"}, true},
		{"help flag on a server command", []string{"users", "list", "--help"}, true},
		{"completion script", []string{"completion", "bash"}, true},
		{"policy validate", []string{"policy", "validate", "-f", policyExample}, true},
		{"spec validate", []string{"spec", "validate", "policyset", "-f", policyExample}, true},
		{"spec conformance rejects its own args, not the server", []string{"spec", "conformance"}, false},
		{"apps import fails on the file, not the server", []string{"apps", "import", "/nonexistent/server.json"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STRAZA_SERVER", "")
			_, stderr, err := runCLI(t, noCreds(t), tc.args...)
			if errors.Is(err, ctl.ErrNoServer) {
				t.Fatalf("%v is local but demanded a server: %v", tc.args, err)
			}
			if tc.wantOK && err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if strings.Contains(stderr, "warning:") {
				t.Errorf("local command warned about servers:\n%s", stderr)
			}
		})
	}
}

// TestStatusPrintsResolvedServer: "where am I pointed" must be the first line
// status prints, before any health detail.
func TestStatusPrintsResolvedServer(t *testing.T) {
	srv := fakeStatusServer(t)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	first, _, _ := strings.Cut(stdout, "\n")
	if !strings.HasPrefix(first, "server ") || !strings.Contains(first, srv.URL) {
		t.Fatalf("first status line = %q, want a server line naming %s", first, srv.URL)
	}
}

// TestStatusPrintsServerWhenUnreachable: the target matters most when the dial
// fails, so the server line comes out before the health probe.
func TestStatusPrintsServerWhenUnreachable(t *testing.T) {
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runCLI(t, writeCreds(t, "http://127.0.0.1:1"), "status")
	if err == nil {
		t.Fatal("want an unreachable error")
	}
	if !strings.HasPrefix(stdout, "server ") || !strings.Contains(stdout, "http://127.0.0.1:1") {
		t.Fatalf("stdout = %q, want the server line even on failure", stdout)
	}
}

// TestBareLoginReusesStoredServer: `strazactl login` with credentials present
// renews against the server it is already logged into, and says which.
func TestBareLoginReusesStoredServer(t *testing.T) {
	srv := fakeLoginServer(t)
	t.Setenv("STRAZA_SERVER", "")
	stdout, stderr, err := runCLI(t, writeCreds(t, srv.URL), "login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(stdout, "Logging in again at "+srv.URL) {
		t.Fatalf("login did not name the stored server:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Logged in.") {
		t.Fatalf("login did not complete:\n%s", stdout)
	}
	if strings.Contains(stderr, "warning:") {
		t.Errorf("renewing against the stored server is not a mismatch:\n%s", stderr)
	}
}

// TestBareLoginWithoutCredentialsIsLoud: nothing stored and nothing given is
// the one case login cannot guess its way out of.
func TestBareLoginWithoutCredentialsIsLoud(t *testing.T) {
	t.Setenv("STRAZA_SERVER", "")
	if _, _, err := runCLI(t, noCreds(t), "login"); !errors.Is(err, ctl.ErrNoServer) {
		t.Fatalf("err = %v, want ErrNoServer", err)
	}
}

// TestLoginSwitchNoticeNotAWarning: `login --server X` while logged into Y IS
// the switch, so it gets a note rather than advice to run the command the
// operator is already running.
func TestLoginSwitchNoticeNotAWarning(t *testing.T) {
	srv := fakeLoginServer(t)
	t.Setenv("STRAZA_SERVER", "")
	_, stderr, err := runCLI(t, writeCreds(t, "https://old.example"), "login", "--server", srv.URL)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	// The first line is the switch note. The login note after it is pinned
	// by TestLoginNoteFollowsASuccessfulLogin.
	want := fmt.Sprintf("note: logging in at %s; you were logged into https://old.example", srv.URL)
	if first, _, _ := strings.Cut(stderr, "\n"); first != want {
		t.Fatalf("stderr = %q, want its first line %q", stderr, want)
	}
	if strings.Contains(stderr, "warning:") {
		t.Errorf("the switch is not a mismatch warning:\n%s", stderr)
	}
}

// fakeStatusServer serves the /version and /readyz surface `status` reads.
func fakeStatusServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"v0.1.0","commit":"abc1234","profile":"standalone"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","components":{"store":"ok","bus":"ok"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeLoginServer is the minimal device-flow surface `login` walks: a 404 on
// idp discovery sends it to the built-in issuer paths, the grant is already
// approved, and checkin/enroll succeed.
func fakeLoginServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/straza/idp.json", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("POST /oidc/device_authorization", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"dc-1","user_code":"WXYZ-2345","verification_uri_complete":"https://issuer.example/device","expires_in":600,"interval":1}`))
	})
	mux.HandleFunc("POST /oidc/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id_token":"id-token-xyz"}`))
	})
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-1"}`))
	})
	mux.HandleFunc("POST /v1/enroll", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_token":"dtok-1"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
