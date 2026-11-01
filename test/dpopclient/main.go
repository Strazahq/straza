// Command dpopclient is a test client for the device key lanes of strazad.
// It enrolls an Ed25519 key, checks in on the key lane, calls session routes
// with a DPoP proof (RFC 9449) and refreshes, so the eval live-fire can fire
// every lane the shipped clients do not speak yet. Every enroll makes a new
// key, replaces the key file with it once the server has bound it, and
// starts the counter at 0. It is never built into a release, and it never
// prints the key or a token.
//
//	dpopclient enroll  -server URL -id-token-file F -key K -state S -fingerprint FP -binding B
//	dpopclient checkin -key K -state S -harness claude-code/2.1.0 [-counter N]
//	dpopclient call    -key K -state S -method POST -path /v1/decide -body JSON [-bearer] [-replay]
//	dpopclient refresh -key K -state S
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "dpopclient: "+err.Error())
		os.Exit(1)
	}
}

// run dispatches one subcommand and writes every answer to out.
func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("name a subcommand: enroll, checkin, call or refresh")
	}
	switch args[0] {
	case "enroll":
		return runEnroll(args[1:], out)
	case "checkin":
		return runCheckin(args[1:], out)
	case "call":
		return runCall(args[1:], out)
	case "refresh":
		return runRefresh(args[1:], out)
	}
	return fmt.Errorf("unknown subcommand %q: use enroll, checkin, call or refresh", args[0])
}
