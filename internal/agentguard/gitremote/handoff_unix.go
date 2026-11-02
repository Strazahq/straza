//go:build unix

package gitremote

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

// handoff replaces this process with `git remote-https <remote> <url>`,
// which inherits stdin, stdout and stderr untouched. It returns only when
// git could not start, with exit code 1.
func handoff(remote, url string, errw io.Writer) int {
	git, err := exec.LookPath("git")
	if err == nil {
		err = syscall.Exec(git, []string{"git", "remote-https", remote, url}, os.Environ()) // #nosec G204 -- git's own transport with the arguments git gave this helper
	}
	_, _ = fmt.Fprintf(errw, sentHandoff+"\n", url, err)
	return 1
}
