package agentguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/strazahq/straza/internal/policy"
)

// Tier-3 exec wrapper: hookless agents get exactly one way to run
// things, and that way is Straza. Every invocation is a canonical
// tool.pre/shell.exec event through the SAME local PDP, spool, and
// fail-closed semantics as the harness hooks. No second decision path
// exists. ADVISORY on an open machine (an agent that can run anything
// can skip the shim); boundary-grade only where the environment makes
// the shim exclusive (sandbox/managed profile; see the Hookless
// processes page of the docs).

// Exec exit codes. Deny mirrors the hook contract's exit 2; spawn failures
// use the shell's 127 convention so scripts can tell "policy said no" from
// "no such binary".
const (
	execDenyExit  = 2
	execSpawnExit = 127
)

// execHarness is the Tier-3 session identity: PolicySets can gate what an
// exec-wrapper session may ever do (policy may require Tier-1 attestation for
// sensitive roles), and the audit trail names the tier.
const execHarness = "exec-wrapper"

// Exec decides argv against the local snapshot and, when allowed, runs the
// child with inherited stdio, passing its exit code through. Every error
// path fails closed: no session, no snapshot, or no decision means nothing runs.
func Exec(ctx context.Context, argv []string, errOut io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(errOut, "Straza: nothing to run. Usage: straza exec -- <command> [args...]")
		return execDenyExit
	}

	store, err := OpenStore()
	if err != nil {
		fmt.Fprintf(errOut, "Straza: state unavailable (%v). Refusing to run\n", err)
		return execDenyExit
	}
	// Unlike hooks there is no harness session.start: ensure a session
	// lazily via the normal checkin (shared cross-process lock, adopts an
	// existing session, one governed session per machine/user, same as the
	// MCP proxy). Failure = fail closed with the remedy.
	if cfg, cfgErr := store.LoadConfig(); cfgErr == nil {
		if _, err := ensureSession(ctx, store, NewClient(cfg.ServerURL), execHarness); err != nil {
			fmt.Fprintf(errOut, "Straza: no session and checkin failed (%v). Run `straza enroll`; refusing to run\n", err)
			return execDenyExit
		}
	} else {
		fmt.Fprintln(errOut, "Straza: not configured. Run `straza enroll`; refusing to run")
		return execDenyExit
	}

	cwd, _ := os.Getwd()
	n := Normalized{
		HarnessName: execHarness,
		Event: policy.Event{
			Kind:      policy.EventToolPre,
			Tool:      policy.ToolShellExec,
			Command:   strings.Join(argv, " "),
			Argv:      argv,
			Workspace: cwd,
		},
	}
	// The same decide path as hooks: verified snapshot, RefreshIfStale,
	// spool + detached drain. Nothing exec-specific in the engine.
	decision := liveDecider{store: store}.Decide(n)
	if decision.Effect != policy.EffectAllow {
		fmt.Fprintln(errOut, denyLine(decision.Reason))
		return execDenyExit
	}

	child := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- running the agent's command is this tool's entire purpose, post-decision
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := child.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode() // the child ran; its exit code is the answer
		}
		fmt.Fprintf(errOut, "Straza: exec %s: %v\n", argv[0], err)
		return execSpawnExit
	}
	return 0
}

// denyLine returns the stderr line for a denied call: the policy reason
// behind one Straza marker. A reason that already opens with the word Straza,
// as the seeded starter policy's "Straza starter policy:" does, is printed as
// it is so the marker never doubles. An empty reason says denied by policy.
func denyLine(reason string) string {
	if reason == "" {
		reason = "denied by policy"
	}
	if strings.HasPrefix(reason, "Straza:") || strings.HasPrefix(reason, "Straza ") {
		return reason
	}
	return "Straza: " + reason
}
