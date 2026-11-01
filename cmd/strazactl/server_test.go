package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/ctl"
)

// runCLI drives the real cobra tree with an injected path of the old login
// file, so a test never touches the developer's own ~/.straza.
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

// testToken is the admin API token onToken puts in STRAZA_API_TOKEN.
const testToken = "wat_test"

// onToken aims the rest of the test at server on testToken, through
// STRAZA_SERVER and STRAZA_API_TOKEN, and returns the path of an old login
// file that does not exist, for runCLI.
func onToken(t *testing.T, server string) string {
	t.Helper()
	t.Setenv("STRAZA_SERVER", server)
	t.Setenv(apiTokenEnv, testToken)
	return noCreds(t)
}

// noCreds returns a path where no old login file exists.
func noCreds(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "credentials.json")
}

// TestNoServerAnywhereIsALoudError: with a token but no --server and no
// $STRAZA_SERVER, a server-touching command must say so instead of quietly
// dialling 127.0.0.1:8420, which aims it and its token at whatever listens
// there.
func TestNoServerAnywhereIsALoudError(t *testing.T) {
	t.Setenv("STRAZA_SERVER", "")
	t.Setenv(apiTokenEnv, testToken)
	_, _, err := runCLI(t, noCreds(t), "users", "list")
	if !errors.Is(err, ctl.ErrNoServer) {
		t.Fatalf("err = %v, want ErrNoServer", err)
	}
	const want = "no server is given. Pass --server <url> or set STRAZA_SERVER to the server that issued the token"
	if err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
}

// TestLocalCommandsNeedNoServer keeps resolution lazy: a command that never
// contacts strazad runs with no token, no flag and no environment. Failing
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
			t.Setenv(apiTokenEnv, "")
			_, stderr, err := runCLI(t, noCreds(t), tc.args...)
			if errors.Is(err, ctl.ErrNoServer) || err != nil && strings.Contains(err.Error(), apiTokenEnv) {
				t.Fatalf("%v is local but demanded a server or a token: %v", tc.args, err)
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
// status prints, before any health detail. status sends no credential, so it
// runs without a token.
func TestStatusPrintsResolvedServer(t *testing.T) {
	srv := fakeStatusServer(t)
	t.Setenv("STRAZA_SERVER", srv.URL)
	t.Setenv(apiTokenEnv, "")
	stdout, _, err := runCLI(t, noCreds(t), "status")
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
	stdout, _, err := runCLI(t, onToken(t, "http://127.0.0.1:1"), "status")
	if err == nil {
		t.Fatal("want an unreachable error")
	}
	if !strings.HasPrefix(stdout, "server ") || !strings.Contains(stdout, "http://127.0.0.1:1") {
		t.Fatalf("stdout = %q, want the server line even on failure", stdout)
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
