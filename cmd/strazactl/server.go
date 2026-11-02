package main

import "github.com/spf13/cobra"

// localAnnotation marks a command that never contacts strazad. Token and
// server resolution skip those commands entirely, so `strazactl policy
// validate` and friends keep working on a machine with no token and no
// --server.
//
// The default is the safe direction: a command is assumed to need a server
// unless it says otherwise, so forgetting the annotation on a new local
// command is a loud demand for a token, never a silent wrong target.
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
