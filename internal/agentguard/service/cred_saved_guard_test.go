package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestCredSavedSessionsOnAnotherMachine pins that saved sessions never get
// around the machine binding. On a machine cloned after a person enrolled,
// the restarted service deletes the person's sessions file and any temp
// file a crash left beside it, answers a
// decide, an MCP call and a connect call of the held window with the
// machine-changed sentence, and sends strazad nothing with its token.
func TestCredSavedSessionsOnAnotherMachine(t *testing.T) {
	window := func(r *http.Request) *http.Request {
		r.Header.Set(localapi.HeaderHarnessSession, "w1")
		return r
	}
	decide, err := json.Marshal(localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: "w1"}, Event: toolPre})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		req  func() *http.Request
	}{
		{"decide", func() *http.Request {
			return httptest.NewRequest(http.MethodPost, localapi.BaseURL+localapi.RouteDecide, bytes.NewReader(decide))
		}},
		{"mcp", func() *http.Request {
			return window(proxyReq(http.MethodPost, localapi.RouteMCP, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		}},
		{"connect", func() *http.Request {
			return window(proxyReq(http.MethodGet, strings.TrimSuffix(localapi.RouteConnect, "/")+"?user=kim", ""))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			if _, err := r.start(alice, "w1"); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(r.savedPath(alice))
			var saved savedSessions
			if err != nil || json.Unmarshal(raw, &saved) != nil || len(saved.Sessions) != 1 || saved.Sessions[0].Token == "" {
				t.Fatalf("the saved sessions before the clone: %v", err)
			}
			r.stop()
			if err := os.WriteFile(r.machine, []byte("machine-two\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			stale := filepath.Join(filepath.Dir(r.savedPath(alice)), "."+sessionsFile+".tmp-left-by-a-crash")
			if err := os.WriteFile(stale, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			r.srv.with(func() { r.srv.refreshes = 0 })
			r.open()
			for _, path := range []string{r.savedPath(alice), stale} {
				if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("%s on the cloned machine: %v, want it deleted", path, err)
				}
			}
			w := httptest.NewRecorder()
			newAPI(r.s, Options{Log: slog.New(slog.DiscardHandler)}).routes().ServeHTTP(w, asCaller(tc.req(), alice.UID))
			var refusal localapi.Refusal
			if w.Code != http.StatusForbidden || json.Unmarshal(w.Body.Bytes(), &refusal) != nil || refusal.Error != personChangedFor(r.srv.serverURL()) {
				t.Fatalf("%s on the cloned machine: %d %s, want 403 with the machine-changed sentence", tc.name, w.Code, w.Body)
			}
			if sent := r.sent.carrying(saved.Sessions[0].Token); len(sent) != 0 {
				t.Fatalf("the cloned machine sent the saved token: %q", sent)
			}
			r.srv.mu.Lock()
			refreshes := r.srv.refreshes
			r.srv.mu.Unlock()
			if refreshes != 0 || len(r.s.Status(alice).Sessions) != 0 {
				t.Fatalf("the cloned machine renewed %d times or holds a session", refreshes)
			}
		})
	}
}

// TestCredSavedSessionsUnreadable pins that a sessions file that cannot be
// read or parsed logs the S3 sentence once the service starts, starts its
// user with no sessions and a working start, and leaves every other user's
// windows resumed. A file that cannot be written either logs the save
// failure with its next step, and the start still works.
func TestCredSavedSessionsUnreadable(t *testing.T) {
	ctx := context.Background()
	bob := Caller{UID: testUID(4343), PID: 2}
	cases := []struct {
		name      string
		spoil     func(path string) error
		saveFails bool
	}{
		{"not JSON", func(path string) error { return os.WriteFile(path, []byte("{torn"), 0o600) }, false},
		{"a directory in its place", func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Mkdir(path, 0o700)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			r.enroll(bob)
			w1, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			b1, err := r.start(bob, "b1")
			if err != nil {
				t.Fatal(err)
			}
			r.stop()
			if err := tc.spoil(r.savedPath(alice)); err != nil {
				t.Fatal(err)
			}
			r.open()
			head, tail, _ := strings.Cut(savedUnreadable, "%v")
			head = fmt.Sprintf(head, r.savedPath(alice))
			if !slices.ContainsFunc(r.logs.messages(), func(m string) bool { return strings.HasPrefix(m, head) && strings.HasSuffix(m, tail) }) {
				t.Fatalf("no S3 line for %s in %q", r.savedPath(alice), r.logs.messages())
			}
			accepted, _ := r.checkins()
			got, err := r.s.Session(ctx, bob, b1.Harness, false)
			if err != nil || got.ID != b1.ID {
				t.Fatalf("bob's window: %+v, %v, want it resumed", got, err)
			}
			if a, _ := r.checkins(); a != accepted {
				t.Fatalf("bob's window checked in: %d to %d", accepted, a)
			}
			var refused *Refused
			if _, err := r.s.Session(ctx, alice, w1.Harness, false); !errors.As(err, &refused) || refused.Sentence != noSessionSentence {
				t.Fatalf("alice's window: %v, want the no-session refusal", err)
			}
			if _, err := r.start(alice, "w2"); err != nil {
				t.Fatalf("alice's next start: %v", err)
			}
			if logged := slices.Contains(r.logs.messages(), savedNotWritten); logged != tc.saveFails {
				t.Fatalf("the save failure logged %v, want %v", logged, tc.saveFails)
			}
			// Two devices count from 1 each, so only the revocations are
			// checked, not one rising order.
			r.srv.mu.Lock()
			reuse := r.srv.reuse
			r.srv.mu.Unlock()
			if len(reuse) != 0 {
				t.Fatalf("strazad revoked a device for key reuse: %v", reuse)
			}
		})
	}
}

// TestCredSavedWhileStopping pins what a service that has begun to stop
// still writes. A revocation is written, because the mark only narrows
// access. Any other change is not, because the next service may already
// own the state directory.
func TestCredSavedWhileStopping(t *testing.T) {
	cases := []struct {
		name    string
		change  func(r *credRig, s *credStore, a *account, hs *heldSession, ses Session)
		written bool
	}{
		{"a revocation", func(_ *credRig, s *credStore, a *account, hs *heldSession, ses Session) {
			s.revoke(a, hs, ses.ID, "kill-switch push from the server")
		}, true},
		{"the forget sweep", func(r *credRig, s *credStore, a *account, _ *heldSession, _ Session) {
			r.clock.Advance(300*time.Second + forgetAfter + time.Second)
			s.forgetLapsed(a)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			ses, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			old := r.s
			a, hs, err := old.held(ses)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(r.savedPath(alice))
			if err != nil {
				t.Fatal(err)
			}
			r.stop()
			tc.change(r, old, a, hs, ses)
			after, err := os.ReadFile(r.savedPath(alice))
			if err != nil || (string(after) != string(before)) != tc.written {
				t.Fatalf("the stopped service wrote %v, want %v: %v", string(after) != string(before), tc.written, err)
			}
			if !tc.written {
				return
			}
			r.open()
			var refused *Refused
			if _, err := r.s.Session(context.Background(), alice, ses.Harness, false); !errors.As(err, &refused) ||
				refused.Sentence != revokedSentence("kill-switch push from the server") {
				t.Fatalf("the window revoked while stopping: %v, want it refused", err)
			}
		})
	}
}

// TestCredSavedWindowCap pins the cap on the live windows one user holds.
// At the cap a start for a new window forgets the lapsed window that
// expired first, one that is not revoked before a revoked one, and is
// refused, checking nothing in, only when every held window is unexpired. A start that reuses a held window,
// live or revoked, is never refused.
func TestCredSavedWindowCap(t *testing.T) {
	cases := []struct {
		name    string
		held    int    // windows held before the start, w1 among them
		others  string // "live", "lapsed", "lapsed revoked" or "lapsed, oldest revoked": the held windows but w1
		window  string
		revoke  bool // w1 is revoked before the start
		refused bool
	}{
		{"a new window below the cap", maxWindows - 1, "live", "new", false, false},
		{"a new window at the cap of live windows", maxWindows, "live", "new", false, true},
		{"a new window at the cap of lapsed windows", maxWindows, "lapsed", "new", false, false},
		{"a new window at the cap of lapsed revoked windows", maxWindows, "lapsed revoked", "new", false, false},
		{"a new window at the cap when the oldest lapsed one is revoked", maxWindows, "lapsed, oldest revoked", "new", false, false},
		{"a live held window at the cap", maxWindows, "live", "w1", false, false},
		{"a revoked held window at the cap", maxWindows, "live", "w1", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			ses, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			r.waitStream(t, alice, "w1")
			a, hs, err := r.s.held(ses)
			if err != nil {
				t.Fatal(err)
			}
			a.mu.Lock()
			for i := 1; i < tc.held; i++ {
				h := localapi.Harness{Name: "claude-code", Session: fmt.Sprintf("held-%d", i)}
				other := &heldSession{lock: newCtxLock(), token: "tok-of-" + h.Session,
					s: Session{UID: alice.UID, Harness: h, ID: h.Session, IssuedAt: r.clock.Now(), ExpiresAt: ses.ExpiresAt}}
				if tc.others != "live" {
					other.s.ExpiresAt = r.clock.Now().Add(-time.Duration(tc.held-i) * time.Second)
				}
				if tc.others == "lapsed revoked" || (tc.others == "lapsed, oldest revoked" && i == 1) {
					other.token, other.revoked = "", "an old window"
				}
				a.sessions[sessKey{h.Name, h.Session}] = other
			}
			a.mu.Unlock()
			if tc.revoke {
				r.s.revoke(a, hs, ses.ID, "kill-switch push from the server")
			}
			accepted, _ := r.checkins()
			got, err := r.start(alice, tc.window)
			var refused *Refused
			held := map[string]bool{}
			for _, ss := range r.s.Status(alice).Sessions {
				held[ss.Harness.Session] = true
			}
			switch n, _ := r.checkins(); {
			case tc.refused && (!errors.As(err, &refused) || refused.Status != http.StatusTooManyRequests ||
				refused.Sentence != fmt.Sprintf(tooManyWindows, maxWindows) || n != accepted || len(held) != maxWindows):
				t.Fatalf("got %v with %d check-ins and %d held, want the 429 cap sentence and nothing checked in", err, n-accepted, len(held))
			case !tc.refused && (err != nil || len(held) != max(tc.held, maxWindows)):
				t.Fatalf("got %+v, %v with %d held, want the start and %d held", got, err, len(held), maxWindows)
			case !tc.refused && tc.window == "w1" && got.Reused == tc.revoke:
				t.Fatalf("reused %v, want a revoked window checked in again and a live one reused", got.Reused)
			case tc.others == "lapsed, oldest revoked" && (!held["held-1"] || held["held-2"] || !held["held-3"]):
				t.Fatalf("held-1 %v, held-2 %v, held-3 %v, want the revoked window kept and the oldest other one forgotten", held["held-1"], held["held-2"], held["held-3"])
			case tc.others != "live" && tc.others != "lapsed, oldest revoked" && (held["held-1"] || !held["held-2"]):
				t.Fatalf("the oldest lapsed window held %v and the next one %v, want only the oldest forgotten", held["held-1"], held["held-2"])
			}
		})
	}
}

// TestCredStartRefreshThrottle pins that starts on a live window refresh it
// at most once per startRefreshAfter: 100 starts right after its check-in
// refresh nothing and save nothing, and 100 starts once its token is that
// old refresh it once, saving the window once.
func TestCredStartRefreshThrottle(t *testing.T) {
	cases := []struct {
		name      string
		advance   time.Duration
		refreshes int
	}{
		{"right after the check-in", 0, 0},
		{"once the token is old enough", startRefreshAfter, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			ses, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			r.waitStream(t, alice, "w1")
			// Contents, not os.SameFile: on Windows SameFile reads both files'
			// ids from the path when it compares, so it never sees a rename.
			before, err := os.ReadFile(r.savedPath(alice))
			if err != nil {
				t.Fatal(err)
			}
			r.clock.Advance(tc.advance)
			r.srv.with(func() { r.srv.refreshes = 0 })
			for range 100 {
				if got, err := r.start(alice, "w1"); err != nil || !got.Reused || got.ID != ses.ID {
					t.Fatalf("a start on the live window: %+v, %v, want it reused", got, err)
				}
			}
			r.srv.mu.Lock()
			refreshes := r.srv.refreshes
			r.srv.mu.Unlock()
			after, err := os.ReadFile(r.savedPath(alice))
			if err != nil || refreshes != tc.refreshes || bytes.Equal(before, after) != (tc.refreshes == 0) {
				t.Fatalf("%d refreshes, file rewritten %v, %v, want %d refreshes and a save only with one", refreshes, !bytes.Equal(before, after), err, tc.refreshes)
			}
		})
	}
}

// TestCredSavedAgentReenrollment pins that saved sessions belong to one
// enrollment, not only to one key. An agent that enrolls again keeps its
// key and gets a new binding, so sessions a crash left on disk from its
// earlier enrollment are not resumed.
func TestCredSavedAgentReenrollment(t *testing.T) {
	r := newCredRig(t)
	sam := Caller{UID: testUID(65532)}
	ctx := context.Background()
	if _, err := r.s.Keygen(ctx, sam, localapi.KeygenRequest{Username: "sam"}); err != nil {
		t.Fatal(err)
	}
	enroll := func() savedSessions {
		t.Helper()
		if _, err := r.s.Enroll(ctx, sam, localapi.EnrollRequest{ServerURL: r.srv.serverURL(), Headless: true}, func(localapi.EnrollEvent) {}); err != nil {
			t.Fatal(err)
		}
		var saved savedSessions
		raw, err := os.ReadFile(r.savedPath(sam))
		if err != nil || json.Unmarshal(raw, &saved) != nil {
			t.Fatalf("the sessions file after the enrollment: %v", err)
		}
		return saved
	}
	first := enroll()
	h := localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: "a1"}
	if _, err := r.s.Session(ctx, sam, h, true); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(r.savedPath(sam))
	if err != nil {
		t.Fatal(err)
	}
	second := enroll()
	if second.Key != first.Key || second.Binding == first.Binding || len(second.Sessions) != 0 {
		t.Fatalf("the second enrollment: key kept %v, binding kept %v, %d sessions", second.Key == first.Key, second.Binding == first.Binding, len(second.Sessions))
	}
	r.stop()
	if err := os.WriteFile(r.savedPath(sam), old, 0o600); err != nil {
		t.Fatal(err)
	}
	r.open()
	var refused *Refused
	if _, err := r.s.Session(ctx, sam, h, false); !errors.As(err, &refused) || refused.Sentence != noSessionSentence {
		t.Fatalf("the window of the earlier enrollment: %v, want the no-session refusal", err)
	}
}
