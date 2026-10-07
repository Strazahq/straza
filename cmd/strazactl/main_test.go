package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// TestMain runs the suite outside any coding agent and without an admin API
// token, whatever the shell that runs go test holds. The suite drives writes
// on a login, which the coding-agent guard would refuse. A test that needs a
// marker or a token sets it with t.Setenv.
func TestMain(m *testing.M) {
	for marker := ctl.CodingAgentMarker(os.Getenv); marker != ""; marker = ctl.CodingAgentMarker(os.Getenv) {
		if err := os.Unsetenv(marker); err != nil {
			panic(err)
		}
	}
	if err := os.Unsetenv(apiTokenEnv); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// TestExitCode pins the status main gives a run: an exitCodeErr keeps its
// code, any other failure of the drafts group or a drafts verb is 2,
// because a drafts verb keeps 1 for the server's no, and every other
// command fails with 1 as before.
func TestExitCode(t *testing.T) {
	root := newRootCmd(noCreds(t))
	find := func(args ...string) *cobra.Command {
		t.Helper()
		c, _, err := root.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}
		return c
	}
	failure := errors.New("a failure")
	tests := []struct {
		name string
		cmd  *cobra.Command
		err  error
		want int
	}{
		{"success", find("drafts", "check"), nil, 0},
		{"the server's no", find("drafts", "check"), exitCodeErr{code: 1}, 1},
		{"a code the command chose", find("policy", "diff"), exitCodeErr{2, failure}, 2},
		{"a failure of a drafts verb", find("drafts", "publish"), failure, 2},
		{"a failure of the drafts group", find("drafts"), failure, 2},
		{"a failure elsewhere", find("roles", "create"), failure, 1},
		{"a failure of the root", root, failure, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(tc.cmd, tc.err); got != tc.want {
				t.Errorf("exitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDraftsFailureExitsTwoThroughMain runs main in a child process and pins
// that main speaks exitCode: a drafts verb run without its -f exits 2 with
// its sentence on stderr.
func TestDraftsFailureExitsTwoThroughMain(t *testing.T) {
	if os.Getenv("STRAZACTL_TEST_MAIN") == "1" {
		os.Args = []string{"strazactl", "--server", "http://127.0.0.1:1", "drafts", "check"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDraftsFailureExitsTwoThroughMain$")
	cmd.Env = append(os.Environ(), "STRAZACTL_TEST_MAIN=1", "HOME="+t.TempDir(), "STRAZA_SERVER=", apiTokenEnv+"=")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("exit = %v, want status 2\nstderr: %s", err, stderr.String())
	}
	if want := "strazactl: strazactl drafts check needs its documents with -f"; !strings.HasPrefix(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to start with %q", stderr.String(), want)
	}
}

// TestGroupRefusesAnUnknownVerb pins that every group, a command that holds
// verbs and runs nothing of its own, refuses a verb it does not have with
// the sentence that names its help, prints nothing, and fails the run with
// status 1, where cobra alone printed the group's help with status 0. The
// drafts group keeps its status 2, and a group given no verb still prints
// its help. The groups are read from the tree, so a new one is covered.
func TestGroupRefusesAnUnknownVerb(t *testing.T) {
	t.Setenv("STRAZA_SERVER", "")
	var groups [][]string
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		for _, sub := range c.Commands() {
			p := append(slices.Clone(path), sub.Name())
			if sub.HasSubCommands() && sub.Use == sub.Name() {
				groups = append(groups, p)
			}
			walk(sub, p)
		}
	}
	walk(newRootCmd(noCreds(t)), nil)
	if len(groups) < 15 {
		t.Fatalf("the walk found %d groups, %v, and the tree holds more", len(groups), groups)
	}
	for _, path := range groups {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			group := "strazactl " + strings.Join(path, " ")
			want := group + " has no verb frob. Run " + group + " --help for the verbs"
			wantCode := 1
			if path[0] == "drafts" {
				wantCode = 2
			}
			var out bytes.Buffer
			root := newRootCmd(noCreds(t))
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(append(slices.Clone(path), "frob"))
			cmd, err := root.ExecuteC()
			if err == nil || err.Error() != want || exitCode(cmd, err) != wantCode || out.Len() != 0 {
				t.Errorf("%s frob: err %v, exit %d, printed %q\nwant %q, exit %d and nothing printed", group, err, exitCode(cmd, err), out.String(), want, wantCode)
			}
			out.Reset()
			root = newRootCmd(noCreds(t))
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(path)
			if cmd, err := root.ExecuteC(); err != nil || exitCode(cmd, err) != 0 || !strings.Contains(out.String(), "Available Commands:") {
				t.Errorf("%s alone: err %v, printed %q, want its help and status 0", group, err, out.String())
			}
		})
	}
}

// TestUnknownVerbExitsOneThroughMain runs main in a child process and pins
// that a group's refusal of a verb reaches the shell as status 1 with the
// sentence on stderr.
func TestUnknownVerbExitsOneThroughMain(t *testing.T) {
	if os.Getenv("STRAZACTL_TEST_MAIN") == "1" {
		os.Args = []string{"strazactl", "policy", "frob"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestUnknownVerbExitsOneThroughMain$")
	cmd.Env = append(os.Environ(), "STRAZACTL_TEST_MAIN=1", "HOME="+t.TempDir(), "STRAZA_SERVER=", apiTokenEnv+"=")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want status 1\nstderr: %s", err, stderr.String())
	}
	if want := "strazactl: strazactl policy has no verb frob. Run strazactl policy --help for the verbs"; !strings.HasPrefix(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to start with %q", stderr.String(), want)
	}
}
