//go:build linux

package service

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestListenSystemdVariables pins how the activation variables are read:
// they are ignored when LISTEN_PID names another process, and a count
// other than one stops the service and unsets them.
func TestListenSystemdVariables(t *testing.T) {
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()+1))
	t.Setenv("LISTEN_FDS", "1")
	if ln, err := systemdListener(); ln != nil || err != nil {
		t.Fatalf("variables of another process = %v, %v; want them ignored", ln, err)
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "2")
	if _, err := systemdListener(); err == nil || !strings.Contains(err.Error(), "serves exactly one") {
		t.Errorf("two sockets = %v, want the one-socket sentence", err)
	}
	for _, k := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s=%q is still set", k, v)
		}
	}
}

// TestListenSystemdSocket pins socket activation end to end: a child
// process handed a listening socket on fd 3 with LISTEN_FDS=1 serves on
// it and not on its own path, and names its caller.
func TestListenSystemdSocket(t *testing.T) {
	home := t.TempDir()
	socket, _, _ := localapi.Paths(home)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	file, err := ln.File()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSystemdChild$") // #nosec G204 -- the test binary itself
	cmd.Env = append(os.Environ(), "STRAZA_TEST_SYSTEMD_CHILD="+filepath.Join(home, "unused.sock"), "LISTEN_FDS=1")
	cmd.ExtraFiles = []*os.File{file}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := localapi.NewClient(home).Status(ctx)
	if err != nil || st.Custody != localapi.CustodyUser {
		t.Fatalf("status through the activated socket = %+v, %v", st, err)
	}
	if _, err := os.Lstat(filepath.Join(home, "unused.sock")); err == nil {
		t.Error("the activated service also listened on its own path")
	}
}

// TestSystemdChild is the child of TestListenSystemdSocket. It takes
// LISTEN_PID for its own, as systemd sets it, and serves the passed
// socket for ten seconds.
func TestSystemdChild(t *testing.T) {
	own := os.Getenv("STRAZA_TEST_SYSTEMD_CHILD")
	if own == "" {
		t.Skip("runs only as the child of TestListenSystemdSocket")
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	ln, err := listen(Options{Socket: own, UserMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := os.LookupEnv("LISTEN_FDS"); ok {
		t.Fatalf("LISTEN_FDS=%q survived activation", v)
	}
	a, _ := testAPI(&apiDouble{}, true)
	a.self = strconv.Itoa(os.Getuid())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = serve(ctx, ln, a)
}
