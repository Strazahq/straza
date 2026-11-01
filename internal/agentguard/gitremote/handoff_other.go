//go:build !unix

package gitremote

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// handoff runs `git remote-https <remote> <url>` as a child process with
// this process's stdin, stdout and stderr, because Windows has no exec, and
// returns its exit code.
func handoff(remote, url string, errw io.Writer) int {
	cmd := exec.Command("git", "remote-https", remote, url) // #nosec G204 -- git's own transport with the arguments git gave this helper
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	}
	_, _ = fmt.Fprintf(errw, sentHandoff+"\n", url, err)
	return 1
}
