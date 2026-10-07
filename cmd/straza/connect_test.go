package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestKitDoesNotLinkTheAdminClient: the kit runs on every governed machine
// and shares the connect verb with strazactl through internal/connect. It
// must never pull in the admin client, which carries the operator's login
// and every admin call.
func TestKitDoesNotLinkTheAdminClient(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool on PATH")
	}
	out, err := exec.Command(goBin, "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasSuffix(dep, "/internal/ctl") {
			t.Fatalf("cmd/straza links %s; share code through internal/connect instead", dep)
		}
	}
}

// TestConnectHelpNamesOneOperand: the verb takes the server as its one
// positional operand and refuses a second.
func TestConnectHelpNamesOneOperand(t *testing.T) {
	cmd := connectCmd()
	if cmd.Use != "connect [server]" {
		t.Errorf("Use = %q", cmd.Use)
	}
	if err := cmd.Args(cmd, []string{"midpoint", "github"}); err == nil {
		t.Error("two operands were accepted")
	}
	if cmd.Flags().Lookup("user") != nil {
		t.Error("the kit verb carries --user; acting for another user is strazactl's lane")
	}
	if cmd.Flags().Lookup("remove") != nil {
		t.Error("connect carries --remove; removal is the disconnect verb, one form only")
	}
}

// TestDisconnectIsItsOwnVerb: removal has one form, the disconnect verb with
// the server as its operand, and the kit's form acts for the caller only.
func TestDisconnectIsItsOwnVerb(t *testing.T) {
	cmd := disconnectCmd()
	if cmd.Use != "disconnect <server>" {
		t.Errorf("Use = %q", cmd.Use)
	}
	if cmd.Flags().Lookup("user") != nil {
		t.Error("the kit verb carries --user; acting for another user is strazactl's lane")
	}
	root := rootCmd()
	if c, _, err := root.Find([]string{"disconnect"}); err != nil || c.Name() != "disconnect" {
		t.Errorf("disconnect is not registered on the root: %v", err)
	}
}
