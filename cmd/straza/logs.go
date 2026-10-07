package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/agentguard"
)

// ranHook flips the moment hookCmd's RunE executes: errors from inside the
// hook flow are recorded there (RecordHookFailure / the fail-closed choke
// point), so the main() boundary must only record errors that never made it
// that far: the parse layer, where cobra rejects mangled argv (the live
// codex 0.146 Windows quoting shape) before any in-command logging can run.
var ranHook bool

// recordHookBoundary is called from main() on a non-deny-coded Execute error:
// if the operator (or a harness) was invoking `straza hook` and the hook code
// never ran, the failure happened at the parse layer; record it with the
// argv actually received, or the error log is blind exactly where the wiring
// broke. Best-effort like every errorlog write.
func recordHookBoundary(args []string, err error) {
	if ranHook || len(args) < 2 || args[1] != "hook" {
		return
	}
	agentguard.RecordHookArgvFailure(args, err)
}

// logsCmd prints the client error log: the local, size-capped record of hook,
// spool, and drain failures (event name + error + timestamp, never payload
// content). One canonical form, no flags, no env:
// the log is bounded by rotation, so the whole thing is always printable, and
// filtering is the shell's job. An empty log is a healthy answer, not an error.
func logsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Print the client error log (hook/spool/drain failures; never payload content)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			errs, unreadable := agentguard.ClientErrors(store)
			if len(errs) == 0 && unreadable == 0 {
				fmt.Printf("no client errors recorded (log: %s)\n", store.ErrorLogPath())
				return nil
			}
			for _, e := range errs {
				fmt.Println(agentguard.RenderClientError(e))
			}
			if unreadable > 0 {
				fmt.Printf("(%d unreadable line(s) skipped)\n", unreadable)
			}
			return nil
		},
	}
}
