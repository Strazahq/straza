//go:build !windows

package service

import (
	"fmt"
	"os"
	"path/filepath"
)

// prepareStateDir creates dir and restricts it to the service's account.
func prepareStateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the state directory %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a directory needs its search bit, and 0700 keeps it to the service's account
		return fmt.Errorf("restrict the state directory %s to the service's account: %w", dir, err)
	}
	return nil
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
