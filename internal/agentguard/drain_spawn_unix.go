//go:build !windows

package agentguard

import (
	"os/exec"
	"syscall"
)

// applyDetach puts the drain helper in its own session so it outlives the
// hook process and never receives the harness's terminal signals.
func applyDetach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
