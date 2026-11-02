//go:build unix

package localapi

import (
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// accountAs makes the service's account look up as uid until the test
// ends, or as missing when uid is "".
func accountAs(t *testing.T, uid string) {
	t.Helper()
	old := lookupAccount
	lookupAccount = func(name string) (*user.User, error) {
		if name != ServiceAccount || uid == "" {
			return nil, user.UnknownUserError(name)
		}
		return &user.User{Username: name, Uid: uid}, nil
	}
	t.Cleanup(func() { lookupAccount = old })
}

// TestOwnerRefusal pins whom the thin client trusts to own the socket: root
// or the service's account for the system service, and only the caller for
// a user-mode service.
func TestOwnerRefusal(t *testing.T) {
	own := os.Getuid()
	for _, row := range []struct {
		name     string
		owner    int
		userMode bool
		account  string // the uid the service's account looks up as, "" for none
		want     string // "" means trusted
	}{
		{"root", 0, false, "", ""},
		{"the service's account", 4242, false, "4242", ""},
		{"another uid", 4243, false, "4242", "belongs to uid 4243, which is not the " + ServiceAccount + " account"},
		{"no service account here", 4243, false, "", "belongs to uid 4243, which is not the " + ServiceAccount + " account"},
		{"user mode, the caller", own, true, "", ""},
		{"user mode, root", 0, true, "", "belongs to uid 0, not to this user"},
		{"user mode, the service's account", 4242, true, "4242", "belongs to uid 4242, not to this user"},
	} {
		t.Run(row.name, func(t *testing.T) {
			if row.want != "" && row.userMode && row.owner == own {
				t.Skip("the test runs as root")
			}
			accountAs(t, row.account)
			err := ownerRefusal("/s.sock", row.owner, row.userMode)
			if (row.want == "") != (err == nil) || (err != nil && !strings.HasSuffix(err.Error(), row.want)) {
				t.Errorf("ownerRefusal = %v, want %q", err, row.want)
			}
		})
	}
}

// TestCheckOwner pins the check the thin client makes before it dials: a
// socket of an account it trusts passes, and a socket of another uid, a
// file that is not a socket, a link to a socket and a missing path are each
// refused.
func TestCheckOwner(t *testing.T) {
	dir, err := os.MkdirTemp("", "straza-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(socket, link); err != nil {
		t.Fatal(err)
	}
	own := strconv.Itoa(os.Getuid())
	for _, row := range []struct {
		name, path, account string
		userMode            bool
		want                string // "" means trusted
	}{
		{"a socket of the service's account", socket, own, false, ""},
		{"a socket of the caller in user mode", socket, "", true, ""},
		{"a socket of another uid", socket, "4242", false, "which is not the " + ServiceAccount + " account"},
		{"a file", file, own, false, file + " is not a socket"},
		{"a link to a socket", link, own, false, link + " is not a socket"},
		{"a missing path", filepath.Join(dir, "missing"), own, false, "no such file or directory"},
	} {
		t.Run(row.name, func(t *testing.T) {
			if row.want != "" && row.path == socket && os.Getuid() == 0 {
				t.Skip("root owns the socket")
			}
			accountAs(t, row.account)
			err := checkOwner(row.path, row.userMode)
			if (row.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), row.want)) {
				t.Errorf("checkOwner = %v, want %q", err, row.want)
			}
		})
	}
}
