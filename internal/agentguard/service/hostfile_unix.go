//go:build !windows

package service

import (
	"errors"
	"io/fs"
	"os"
)

// prepareGitDir creates the host file's folder dir when it is missing,
// with mode 0755, set explicitly because the system unit's umask would
// leave it 0700.
func prepareGitDir(dir string) error {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- git in every local user's agent reads the host file
			return err
		}
		if err := os.Chmod(dir, 0o755); err != nil { // #nosec G302 -- git in every local user's agent reads the host file
			return err
		}
	}
	return nil
}
