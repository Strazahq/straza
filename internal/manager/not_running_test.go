package manager

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestNotRunningSentences pins the refusals an agent reads after the
// gateway's "upstream call failed: " and an operator reads as a health
// reason: each names the server and the next step in plain words, carries
// no package name, and still matches its sentinel, or keeps its cause, for
// errors.Is and errors.Unwrap.
func TestNotRunningSentences(t *testing.T) {
	mgr, _ := testManager(t)
	ctx := context.Background()
	stopped := NewRemoteRuntime("gone", RemoteSpec{URL: "http://127.0.0.1:1/mcp"}, nil, nil)
	stopped.Stop()
	refused := NewRemoteRuntime("far", RemoteSpec{URL: "http://127.0.0.1:1/mcp"}, nil, nil)
	refused.ConnectTimeout = 5 * time.Second
	refused.AllowLoopback = true

	notRunning := func(name string) string {
		return name + " was not called because the MCP server is not running. An administrator reads the reason with strazactl apps show " + name
	}
	cases := []struct {
		name string
		call func() error
		// want is the whole sentence; prefix and suffix bound one whose
		// middle is the cause's own text.
		want, prefix, suffix string
	}{
		{name: "a credential for a server with no instance", want: notRunning("ghost"), call: func() error {
			_, err := mgr.Credential(ctx, "ghost", Caller{})
			return err
		}},
		{name: "a call to a server with no instance", want: notRunning("ghost"), call: func() error {
			_, err := mgr.Call(ctx, "ghost", "echo", nil, nil)
			return err
		}},
		{name: "a token probe on a server with no instance", want: notRunning("ghost"), call: func() error {
			return mgr.ProbeWith(ctx, "ghost", nil)
		}},
		{name: "a command server with no session", want: notRunning("cmd"), call: func() error {
			_, err := NewCommandRuntime("cmd", CommandSpec{Exec: "true"}, nil, nil).Tools(ctx, nil)
			return err
		}},
		{name: "a stopped remote server", want: notRunning("gone"), call: func() error {
			_, err := stopped.Tools(ctx, nil)
			return err
		}},
		{name: "a stopped remote server probed with a token", want: notRunning("gone"), call: func() error {
			return stopped.Probe(ctx, &Secret{ID: "probe", Value: "t"})
		}},
		{name: "a remote server that refuses the connection",
			prefix: "the connection to MCP server far failed: ",
			suffix: ". An administrator checks that the server runs and answers at the address in its manifest",
			call: func() error {
				_, err := refused.Tools(ctx, nil)
				return err
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("err = nil, want a refusal")
			}
			msg := err.Error()
			if strings.Contains(msg, "manager:") {
				t.Errorf("%q names the package", msg)
			}
			if tc.want != "" {
				if msg != tc.want {
					t.Errorf("err = %q\nwant %q", msg, tc.want)
				}
				if !errors.Is(err, ErrNotReady) {
					t.Errorf("errors.Is(%q, ErrNotReady) = false", msg)
				}
				return
			}
			if !strings.HasPrefix(msg, tc.prefix) || !strings.HasSuffix(msg, tc.suffix) || len(msg) <= len(tc.prefix)+len(tc.suffix) {
				t.Errorf("err = %q\nwant %q, the cause, then %q", msg, tc.prefix, tc.suffix)
			}
			if errors.Unwrap(err) == nil {
				t.Errorf("%q dropped its cause", msg)
			}
		})
	}
}
