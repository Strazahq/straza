package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// credRig is one state directory, a fake strazad and the service on it,
// which a test can restart.
type credRig struct {
	t      *testing.T
	dir    string
	clock  *fakeClock
	srv    *fakeStrazad
	s      *credStore
	cancel context.CancelFunc
	logs   *rigLog
	sent   *sentLog
	// machine is the file the service reads the machine identity from.
	machine string
}

// sentLog is the transport of the rig's running service to strazad. It
// records the method, path, Authorization header and body of every
// request the service sends.
type sentLog struct {
	base http.RoundTripper
	mu   sync.Mutex
	reqs []string
}

func (l *sentLog) RoundTrip(req *http.Request) (*http.Response, error) {
	line := req.Method + " " + req.URL.Path + " " + req.Header.Get("Authorization")
	if req.Body != nil && req.Body != http.NoBody {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		line += " " + string(body)
	}
	l.mu.Lock()
	l.reqs = append(l.reqs, line)
	l.mu.Unlock()
	return l.base.RoundTrip(req)
}

// carrying returns every request the service sent that carried token.
func (l *sentLog) carrying(token string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range l.reqs {
		if strings.Contains(line, token) {
			out = append(out, line)
		}
	}
	return out
}

// rigLog is what the rig's services logged, as JSON lines, safe for the
// goroutines that write it.
type rigLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *rigLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// messages returns the message of every line logged so far.
func (l *rigLog) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range strings.Split(l.buf.String(), "\n") {
		var rec struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil {
			out = append(out, rec.Msg)
		}
	}
	return out
}

// alice is a person caller under another uid than the test, so her key's
// custody is account.
var alice = Caller{UID: testUID(4242), PID: 1}

// testUID spells the test account n as a caller of this system names it:
// the decimal uid on Linux and a SID on Windows, because the service
// resumes only folders whose names are caller keys (isUID).
func testUID(n int) string {
	if runtime.GOOS == "windows" {
		return "S-1-5-21-1-2-3-" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func newCredRig(t *testing.T) *credRig {
	t.Helper()
	t.Setenv("STRAZA_SYSTEM", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	machine := filepath.Join(t.TempDir(), "machine-id")
	if err := os.WriteFile(machine, []byte("machine-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	machineFrom(t, machine)
	clock := newFakeClock()
	r := &credRig{t: t, dir: filepath.Join(t.TempDir(), "state"), machine: machine, clock: clock, srv: newFakeStrazad(t, clock), logs: &rigLog{}}
	r.open()
	t.Cleanup(func() { r.stop() })
	return r
}

// machineFrom makes the service read the machine identity from the file at
// path until the test ends. A missing or empty file means the system has
// none.
func machineFrom(t *testing.T, path string) {
	old := systemMachine
	systemMachine = func() (string, error) {
		raw, _ := os.ReadFile(path) // #nosec G304 -- a test's own file
		return strings.TrimSpace(string(raw)), nil
	}
	t.Cleanup(func() { systemMachine = old })
}

// open starts the service on the rig's state directory, as Run does: the
// credential half, then the accounts that saved sessions. A new log of
// what it sends to strazad starts with it.
func (r *credRig) open() {
	r.t.Helper()
	r.openAt(r.clock.Now)
}

// openAt is open with the service's clock set to now.
func (r *credRig) openAt(now func() time.Time) {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := newCredentials(ctx, Options{StateDir: r.dir, Log: slog.New(slog.NewJSONHandler(r.logs, nil))})
	if err != nil {
		cancel()
		r.t.Fatalf("newCredentials: %v", err)
	}
	r.sent = &sentLog{base: s.base}
	s.now, s.base = now, r.sent
	s.resumeAccounts()
	r.s, r.cancel = s, cancel
}

// stop ends the running service and releases its lock.
func (r *credRig) stop() {
	if r.s != nil {
		r.cancel()
		r.s.close()
		r.s = nil
	}
}

// restart stops the service and starts it again on the same directory.
func (r *credRig) restart() {
	r.stop()
	r.open()
}

// waitDrained waits until c's spool is empty and its drain wrote the
// last-drain marker, the drain's final write. A test that records waits
// for it, because the drain runs in the background and would otherwise
// still write into the state directory while the test's cleanup removes
// it.
func (r *credRig) waitDrained(t *testing.T, c Caller) {
	t.Helper()
	waitFor(t, "the drain to finish", func() bool {
		st := r.s.Status(c).Spool
		return st.Pending == 0 && !st.LastDrain.IsZero()
	})
}

// enroll enrolls c as alice by device code against the fake strazad.
func (r *credRig) enroll(c Caller) localapi.EnrollResult {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := r.s.Enroll(ctx, c, localapi.EnrollRequest{ServerURL: r.srv.serverURL()}, func(localapi.EnrollEvent) {})
	if err != nil {
		r.t.Fatalf("Enroll: %v", err)
	}
	return res
}

// start starts a session for window id of claude-code.
func (r *credRig) start(c Caller, id string) (Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.s.Session(ctx, c, localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: id}, true)
}
