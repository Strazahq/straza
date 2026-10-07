package agentguard

import (
	"os/exec"
	"syscall"
)

// applyDetach keeps the drain helper invisible and independent: no console
// window flash on the developer's desktop, no tie to the hook's lifetime.
func applyDetach(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	const detachedProcess = 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow | detachedProcess,
	}
}
