//go:build linux || darwin

package service

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// shortTempDir returns a new folder under the system's temporary folder,
// removed when the test ends. macOS caps a socket path at 104 bytes, and
// t.TempDir, whose path carries the test's name, can pass that there.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "straza-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// serveOn serves a over ln until the test ends.
func serveOn(t *testing.T, ln net.Listener, a *api) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, a) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
}

// TestRunServesAndHoldsTheStateDir pins Run end to end over the real
// credential half: it serves the caller on its socket, answers a session
// start of a caller who never enrolled with the not-enrolled sentence, and
// a second service on the same state directory stops with the
// second-service sentence before it listens.
func TestRunServesAndHoldsTheStateDir(t *testing.T) {
	home := shortTempDir(t)
	socket, state, userMode := localapi.Paths(home)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, Options{Socket: socket, StateDir: state, UserMode: userMode}) }()
	client := localapi.NewClient(home)
	var st localapi.Status
	var err error
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if st, err = client.Status(context.Background()); err == nil {
			break
		}
	}
	if err != nil || st.Custody != localapi.CustodyUser || !st.UserMode || st.Enrollment != nil {
		t.Fatalf("status = %+v, %v; want a user-mode service with custody user and no enrollment", st, err)
	}
	_, err = client.StartSession(context.Background(), localapi.SessionRequest{Harness: localapi.Harness{Name: "claude-code"}})
	var refusal *localapi.RefusalError
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Msg, "isn't enrolled for you yet") {
		t.Errorf("session start = %v, want the not-enrolled sentence", err)
	}
	other := filepath.Join(home, "other.sock")
	err = Run(context.Background(), Options{Socket: other, StateDir: state, UserMode: true})
	if err == nil || !strings.Contains(err.Error(), "Another Straza background service already uses this state directory") {
		t.Errorf("second service = %v, want the second-service sentence", err)
	}
	if _, serr := os.Lstat(other); serr == nil {
		t.Error("the second service listened before it stopped")
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v after its context ended, want nil", err)
	}
	if _, serr := os.Lstat(socket); serr == nil {
		t.Error("the socket outlived the service")
	}
}

// TestRunReadyAfterListen pins Options.Ready: the service that holds the
// state directory calls it once, when its socket already listens, and a
// service that stops before it listens never calls it, whether the state
// directory's lock or the socket path refused it.
func TestRunReadyAfterListen(t *testing.T) {
	home := shortTempDir(t)
	socket, state, userMode := localapi.Paths(home)
	readyCalls := make(chan bool, 4)
	ready := func() {
		fi, err := os.Lstat(socket)
		readyCalls <- err == nil && fi.Mode().Type() == fs.ModeSocket
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, Options{Socket: socket, StateDir: state, UserMode: userMode, Ready: ready}) }()
	select {
	case listening := <-readyCalls:
		if !listening {
			t.Fatal("Ready ran before the socket listened")
		}
	case err := <-done:
		t.Fatalf("Run = %v before Ready", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Ready did not run")
	}

	notSocket := filepath.Join(shortTempDir(t), "service.sock")
	if err := os.WriteFile(notSocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name, socket, state, want string
	}{
		{"the state directory's lock", filepath.Join(home, "other.sock"), state,
			"Another Straza background service already uses this state directory"},
		{"a socket path that is not a socket", notSocket, t.TempDir(), "exists and is not a socket"},
	} {
		t.Run(row.name, func(t *testing.T) {
			err := Run(context.Background(), Options{Socket: row.socket, StateDir: row.state, UserMode: true, Ready: ready})
			if err == nil || !strings.Contains(err.Error(), row.want) {
				t.Errorf("Run = %v, want the refusal %q", err, row.want)
			}
		})
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v after its context ended, want nil", err)
	}
	if n := len(readyCalls); n != 0 {
		t.Errorf("Ready ran %d more times, want once in all", n)
	}
}

// TestListenPeerCredNamesCaller pins that a real connection's caller is
// the uid and pid the kernel names, through SO_PEERCRED on Linux and
// LOCAL_PEERCRED on macOS: the thin client's own, with custody user because
// the service runs as the same user.
func TestListenPeerCredNamesCaller(t *testing.T) {
	home := shortTempDir(t)
	socket, _, userMode := localapi.Paths(home)
	ln, err := listenSocket(socket, userMode)
	if err != nil {
		t.Fatal(err)
	}
	f := &apiDouble{}
	a, _ := testAPI(f, userMode)
	a.self = strconv.Itoa(os.Getuid())
	serveOn(t, ln, a)

	st, err := localapi.NewClient(home).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Caller{UID: strconv.Itoa(os.Getuid()), PID: int32(os.Getpid())}
	if len(f.callers) != 1 || f.callers[0] != want || st.Custody != localapi.CustodyUser || !st.UserMode {
		t.Errorf("callers %+v, status %+v; want %+v with custody user", f.callers, st, want)
	}
}

// TestListenSocketModes pins the socket's mode: every uid may connect in
// system mode, where the service checks each caller, and only the user
// may connect in user mode, in a directory only the user may enter.
func TestListenSocketModes(t *testing.T) {
	for _, row := range []struct {
		name          string
		userMode      bool
		sock, dirMode fs.FileMode
	}{
		{"system mode", false, 0o666, 0o755},
		{"user mode", true, 0o600, 0o700},
	} {
		t.Run(row.name, func(t *testing.T) {
			path := filepath.Join(shortTempDir(t), "run", "service.sock")
			ln, err := listenSocket(path, row.userMode)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			fi, err := os.Lstat(path)
			if err != nil || fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != row.sock {
				t.Errorf("socket = %v (%v), want a socket with mode %v", fi.Mode(), err, row.sock)
			}
			if di, err := os.Stat(filepath.Dir(path)); err != nil || di.Mode().Perm()&^row.dirMode != 0 {
				t.Errorf("directory mode = %v (%v), want at most %v", di.Mode().Perm(), err, row.dirMode)
			}
		})
	}
}

// TestListenStaleSocket pins that a socket file nothing answers on is
// replaced, and that a live socket or a file of another kind stops the
// service with a sentence.
func TestListenStaleSocket(t *testing.T) {
	t.Run("a stale socket is replaced", func(t *testing.T) {
		path := filepath.Join(shortTempDir(t), "service.sock")
		old, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		old.SetUnlinkOnClose(false)
		_ = old.Close()
		ln, err := listenSocket(path, true)
		if err != nil {
			t.Fatalf("a stale socket stopped the service: %v", err)
		}
		_ = ln.Close()
	})
	t.Run("a live socket stops the service", func(t *testing.T) {
		path := filepath.Join(shortTempDir(t), "service.sock")
		live, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = live.Close() }()
		if _, err := listenSocket(path, true); err == nil || !strings.Contains(err.Error(), "another Straza background service answers") {
			t.Errorf("err = %v, want the live-socket sentence", err)
		}
	})
	t.Run("a file of another kind stays", func(t *testing.T) {
		path := filepath.Join(shortTempDir(t), "service.sock")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := listenSocket(path, true); err == nil || !strings.Contains(err.Error(), "is not a socket") {
			t.Errorf("err = %v, want the not-a-socket sentence", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("the file was removed: %v", err)
		}
	})
}
