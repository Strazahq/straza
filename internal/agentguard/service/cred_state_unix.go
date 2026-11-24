//go:build !windows

package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// prepareStateDir creates dir and restricts it to the service's account. When
// the account may not create dir, or does not own it, the error is a sentence
// that names the remedy.
func prepareStateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return createStateDirErr(dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a directory needs its search bit, and 0700 keeps it to the service's account
		return restrictStateDirErr(dir, err)
	}
	return nil
}

// createStateDirErr words a failure to create the state directory dir.
func createStateDirErr(dir string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("`straza service` cannot create its state directory %s, because the account it runs as (uid %d) may not write to %s. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Make that directory writable for the account, then start the service again.",
			dir, os.Getuid(), filepath.Dir(dir))
	}
	return fmt.Errorf("create the state directory %s: %w", dir, err)
}

// restrictStateDirErr words a failure to restrict the state directory dir.
// Only a directory's owner may change its mode, so a refusal means that dir
// belongs to another account, as a volume's mount point does.
func restrictStateDirErr(dir string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("`straza service` cannot use %s as its state directory, because the directory belongs to another account, and only its owner may close it to other accounts. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Hand the directory to the account the service runs as (uid %d), or let the service create it: when the directory is the mount point of a volume, mount the volume one level above. "+
			"Then start the service again.",
			dir, os.Getuid())
	}
	return fmt.Errorf("restrict the state directory %s to the service's account: %w", dir, err)
}

// replaceDurably renames tmp over path and syncs the directory, so the
// rename survives a crash. It removes tmp when the rename fails.
func replaceDurably(tmp, path string) error {
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir syncs the directory entry of a rename, which the counter rule
// needs: a counter written before a crash must still be there after it.
func syncDir(dir string) error {
	d, err := os.Open(dir) // #nosec G304 -- the service's own state directory
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
