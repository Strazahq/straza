package localapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// testPipePath returns a pipe name no other test or service uses.
func testPipePath(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return `\\.\pipe\straza-test-` + hex.EncodeToString(b[:])
}

// ownSID is the SID of the account the test runs as.
func ownSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

// fakePipeService answers status on a new pipe the test's account owns,
// with the service's access rules, and counts the requests it answered.
func fakePipeService(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	own, path := ownSID(t), testPipePath(t)
	sddl := fmt.Sprintf("O:%sD:P(A;;FA;;;SY)(A;;FA;;;%s)(A;;0x%x;;;AU)", own, own, PipeAccess)
	ln, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: sddl})
	if err != nil {
		t.Fatal(err)
	}
	answered := &atomic.Int32{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		answered.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"test"}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return path, answered
}

// pipeClient returns the thin client for the pipe at path, expecting owner
// to own it.
func pipeClient(t *testing.T, path, owner string) *Client {
	t.Helper()
	old := pipeOwner
	pipeOwner = owner
	t.Cleanup(func() { pipeOwner = old })
	c := NewClient("")
	c.socket = path
	return c
}

// TestDialChecksThePipeOwner pins the thin client's check on Windows: it
// talks to a pipe the expected account owns, and closes any other pipe
// before it writes, failing as unreachable with the wrong-owner sentence.
func TestDialChecksThePipeOwner(t *testing.T) {
	own := ownSID(t)
	for _, row := range []struct {
		name, owner string
		ok          bool
	}{
		{"the expected owner", own, true},
		{"the service's account expected", ServiceSID, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			path, answered := fakePipeService(t)
			st, err := pipeClient(t, path, row.owner).Status(context.Background())
			if row.ok {
				if err != nil || st.Version != "test" || answered.Load() != 1 {
					t.Fatalf("status = %+v, %v after %d answers; want the fake service's answer", st, err, answered.Load())
				}
				return
			}
			if !errors.Is(err, ErrUnreachable) || !strings.Contains(fmt.Sprint(err), "is not the Straza background service, so Straza sent it nothing") ||
				!strings.Contains(fmt.Sprint(err), own) {
				t.Errorf("status error = %v, want unreachable with the wrong-owner sentence naming %s", err, own)
			}
			if n := answered.Load(); n != 0 {
				t.Errorf("the pipe of another owner answered %d request(s), want none", n)
			}
		})
	}
}

// TestPipeAccessCreatesNoInstance pins the access a thin client asks for
// and the pipe grants: read, write and read-control, never
// FILE_APPEND_DATA, which on a pipe creates a server instance.
func TestPipeAccessCreatesNoInstance(t *testing.T) {
	need := uint32(windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.READ_CONTROL | windows.SYNCHRONIZE)
	if got := uint32(PipeAccess); got != 0x12019b || got&windows.FILE_APPEND_DATA != 0 || got&need != need {
		t.Errorf("PipeAccess = %#x, want 0x12019b without FILE_APPEND_DATA", got)
	}
}

// TestPathsAreTheSystemServicesWhateverHome pins that STRAZA_HOME selects
// nothing on Windows: the pipe and the state folder are the system
// service's, in ProgramData as Windows names it.
func TestPathsAreTheSystemServicesWhateverHome(t *testing.T) {
	pd, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{"", `C:\Users\kim\.straza-ci`} {
		socket, state, user := Paths(home)
		if socket != `\\.\pipe\straza-service` || state != filepath.Join(pd, "straza-service") || user {
			t.Errorf("Paths(%q) = %q, %q, %v; want the system pipe and %s, not user mode", home, socket, state, user,
				filepath.Join(pd, "straza-service"))
		}
	}
}

// TestStateDirIgnoresTheEnvironment pins that the environment a process
// starts with cannot move SystemStateDir: a child with ProgramData,
// ALLUSERSPROFILE and SystemDrive pointed elsewhere resolves the same
// folder. The ProgramData known folder failed this on Windows 10.
func TestStateDirIgnoresTheEnvironment(t *testing.T) {
	if os.Getenv("STRAZA_TEST_PRINT_STATE_DIR") == "1" {
		fmt.Println(SystemStateDir)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStateDirIgnoresTheEnvironment$") // #nosec G204 -- the test binary itself
	cmd.Env = append(os.Environ(), "STRAZA_TEST_PRINT_STATE_DIR=1",
		`ProgramData=`+t.TempDir(), `SystemDrive=Q:`, `ALLUSERSPROFILE=`+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := strings.Cut(string(out), "\n"); got != SystemStateDir || SystemStateDir == "" {
		t.Errorf("a child with a moved environment resolves %q, this process %q; want the same, non-empty", got, SystemStateDir)
	}
}
