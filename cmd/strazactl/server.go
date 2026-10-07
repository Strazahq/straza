package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// localAnnotation marks a command that never contacts strazad. Server
// resolution skips those commands entirely, so `strazactl policy validate` and
// friends keep working on a machine with no login and no --server.
//
// The default is the safe direction: a command is assumed to need a server
// unless it says otherwise, so forgetting the annotation on a new local
// command is a loud demand for a login, never a silent wrong target.
const localAnnotation = "straza.local"

// local is the annotation map for a command that needs no server. Setting it
// on a group covers every subcommand underneath.
var local = map[string]string{localAnnotation: "true"}

// needsServer reports whether cmd must resolve a target before it runs. The
// annotation is inherited from parent groups; cobra's own help and completion
// machinery is always local (`__complete` is generated at Execute time, so it
// cannot be annotated at construction).
func needsServer(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[localAnnotation] == "true" {
			return false
		}
		switch c.Name() {
		case "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			return false
		}
	}
	return true
}

// overrideNotice renders the one-line stderr notice owed to an operator whose
// --server or $STRAZA_SERVER points somewhere other than the deployment their
// stored credentials came from. Empty when the two agree, when nothing
// overrides, or when there is no login to disagree with.
//
// Two commands read the mismatch differently, so the notice is keyed by name:
// `strazactl login --server X` IS the switch, so advising the operator to run
// the command they are already running would look like a bug, so they get a note
// that they are moving instead. `strazactl logout` cannot honour an override at
// all (the only session it can end is the stored one), so it refuses with its
// own message and a generic warning here would just be noise in front of it.
func overrideNotice(t ctl.Target, cmdName string) string {
	if !t.Overridden() {
		return ""
	}
	switch cmdName {
	case "login":
		return fmt.Sprintf("note: logging in at %s; you were logged into %s", t.Server, t.LoginServer)
	case "logout":
		return ""
	}
	return fmt.Sprintf(
		"warning: %s targets %s but you are logged into %s. Credentials may not be valid there; "+
			"run `strazactl login --server %s` to switch",
		t.Source, t.Server, t.LoginServer, t.Server)
}
