//go:build !windows

package service

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestStateDirErrSentences pins that a permission refusal on the state
// directory reads as a sentence with its remedy, and that any other failure
// keeps its cause.
func TestStateDirErrSentences(t *testing.T) {
	const dir = "/var/lib/straza-service"
	eio := &fs.PathError{Op: "mkdir", Path: dir, Err: syscall.EIO}
	cases := []struct {
		name    string
		got     error
		want    []string
		wrapped error
	}{
		{
			name: "create refused",
			got:  createStateDirErr(dir, &fs.PathError{Op: "mkdir", Path: dir, Err: syscall.EACCES}),
			want: []string{"cannot create its state directory " + dir, "may not write to /var/lib.", "Make that directory writable", "start the service again"},
		},
		{
			name: "restrict refused",
			got:  restrictStateDirErr(dir, &fs.PathError{Op: "chmod", Path: dir, Err: syscall.EPERM}),
			want: []string{"cannot use " + dir + " as its state directory", "belongs to another account", "mount the volume one level above", "start the service again"},
		},
		{name: "create fails otherwise", got: createStateDirErr(dir, eio), want: []string{"create the state directory " + dir}, wrapped: eio},
		{name: "restrict fails otherwise", got: restrictStateDirErr(dir, eio), want: []string{"restrict the state directory " + dir}, wrapped: eio},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, part := range c.want {
				if !strings.Contains(c.got.Error(), part) {
					t.Errorf("got %q, want it to contain %q", c.got, part)
				}
			}
			if c.wrapped != nil && !errors.Is(c.got, c.wrapped) {
				t.Errorf("got %q, want it to wrap %v", c.got, c.wrapped)
			}
		})
	}
}

// TestPrepareStateDirParentNotWritable pins that the service names the
// remedy when its account may not create the state directory.
func TestPrepareStateDirParentNotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root may write to any directory")
	}
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	err := prepareStateDir(filepath.Join(parent, "state"))
	if err == nil || !strings.Contains(err.Error(), "may not write to "+parent) {
		t.Fatalf("got %v, want the sentence that names %s", err, parent)
	}
}
