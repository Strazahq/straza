package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// startMany starts n windows of c at once and returns their sessions.
func startMany(t *testing.T, r *credRig, c Caller, prefix string, n int) []Session {
	t.Helper()
	out := make([]Session, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = r.start(c, fmt.Sprintf("%s-%d", prefix, i))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("start %s-%d: %v", prefix, i, err)
		}
	}
	return out
}

// assertLive checks that every session is held, live and distinct, and
// that a call without start finds each one.
func assertLive(t *testing.T, r *credRig, c Caller, sessions []Session) {
	t.Helper()
	ids := map[string]bool{}
	for _, ses := range sessions {
		if ids[ses.ID] {
			t.Fatalf("two windows share session %s", ses.ID)
		}
		ids[ses.ID] = true
		got, err := r.s.Session(context.Background(), c, ses.Harness, false)
		if err != nil || got.ID != ses.ID || !r.clock.Now().Before(got.ExpiresAt) {
			t.Fatalf("window %s: got %s, %v, want live session %s", ses.Harness.Session, got.ID, err, ses.ID)
		}
	}
}

// TestCredFifteenStartsAtOnce starts 15 windows of one person at once,
// twice. Expected: no revoke, a strictly rising counter, every session
// live.
func TestCredFifteenStartsAtOnce(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	first := startMany(t, r, alice, "a", 15)
	r.clock.Advance(3 * time.Minute)
	second := startMany(t, r, alice, "b", 15)
	r.srv.assertCounters(t)
	r.srv.mu.Lock()
	accepted := len(r.srv.accepted)
	r.srv.mu.Unlock()
	if accepted != 30 {
		t.Fatalf("strazad accepted %d check-ins for 30 windows", accepted)
	}
	assertLive(t, r, alice, slices.Concat(first, second))
}

// TestCredLostAnswers covers the two ways a check-in's outcome stays
// unknown: strazad acted on it and the answer was lost, and a request
// delayed past the client's patience that reaches strazad after a later
// one. Both hold the key's next check-in for two minutes from when the
// lost request's nonce arrived, so the late copy fails at the nonce check.
// Expected: the waiting sentence inside the wait, then no revoke, a
// strictly rising counter, and a live session.
func TestCredLostAnswers(t *testing.T) {
	cases := []struct {
		name     string
		dropped  bool
		readOnly bool // the record cannot be written when the answer is lost
	}{
		{"dropped answer", true, false},
		{"dropped answer while the record cannot be written", true, true},
		{"delayed request overtaken", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.readOnly && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				t.Skip("a read-only mode stops writes into a folder only on Linux and macOS")
			}
			r := newCredRig(t)
			r.enroll(alice)
			release := make(chan struct{})
			var once sync.Once
			free := func() { once.Do(func() { close(release) }) }
			t.Cleanup(free)
			userDir := filepath.Join(r.dir, "u", alice.UID)
			if tc.dropped {
				r.srv.drop = func(c int64) bool {
					if c == 1 && tc.readOnly {
						_ = os.Chmod(userDir, 0o500)
					}
					return c == 1
				}
			} else {
				r.s.checkinTimeout = 300 * time.Millisecond
				r.srv.delay = func(c int64) <-chan struct{} {
					if c == 1 {
						return release
					}
					return nil
				}
			}
			var refused *Refused
			if _, err := r.start(alice, "w1"); !errors.As(err, &refused) || refused.Status != http.StatusBadGateway {
				t.Fatalf("lost check-in: got %v, want the 502 sentence", err)
			}
			if _, err := r.start(alice, "w2"); !errors.As(err, &refused) || refused.Sentence != waitingSentence {
				t.Fatalf("start inside the wait: got %v, want the waiting sentence", err)
			}
			if st := r.s.Status(alice); st.Enrollment == nil || st.Enrollment.WaitUntil.IsZero() {
				t.Fatalf("status shows no wait: %+v", st.Enrollment)
			}
			if err := os.Chmod(userDir, 0o700); err != nil {
				t.Fatal(err)
			}
			r.restart()
			if _, err := r.start(alice, "w2"); !errors.As(err, &refused) || refused.Sentence != waitingSentence {
				t.Fatalf("start inside the wait after a restart: got %v, want the waiting sentence", err)
			}
			r.clock.Advance(lostAnswerWait + time.Second)
			ses, err := r.start(alice, "w2")
			if err != nil {
				t.Fatalf("start after the wait: %v", err)
			}
			free()
			if !tc.dropped {
				<-r.srv.lateDone
			}
			r.srv.mu.Lock()
			reached := slices.Contains(r.srv.seen, 1) && !tc.dropped
			r.srv.mu.Unlock()
			if reached {
				t.Fatal("the late copy of counter 1 reached the counter check")
			}
			r.srv.assertCounters(t)
			assertLive(t, r, alice, []Session{ses})
		})
	}
}

// TestCredRestartBetweenPersistAndSend crashes the service after it
// persisted three counters it never sent. Expected: the restarted service
// resumes at the persisted number plus one, no revoke, a live session.
func TestCredRestartBetweenPersistAndSend(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	a := r.s.account(alice.UID)
	for range 3 {
		if _, err := a.nextCounter(time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	r.restart()
	ses, err := r.start(alice, "w1")
	if err != nil {
		t.Fatalf("start after the restart: %v", err)
	}
	r.srv.mu.Lock()
	received := slices.Clone(r.srv.received)
	accepted := slices.Clone(r.srv.accepted)
	r.srv.mu.Unlock()
	// The restarted service holds no nonce, so its first attempt meets the
	// nonce challenge and the retry takes the next number.
	if !slices.Equal(received, []int64{4, 5}) || !slices.Equal(accepted, []int64{5}) {
		t.Fatalf("strazad received %v and accepted %v, want [4 5] and [5]", received, accepted)
	}
	r.srv.assertCounters(t)
	assertLive(t, r, alice, []Session{ses})
}

// TestCredTwoServicesOneStateDir opens two services on one fresh state
// directory at once. Expected: exactly one runs, and the other stops with
// the second-service sentence.
func TestCredTwoServicesOneStateDir(t *testing.T) {
	newCredRig(t)
	dir := filepath.Join(t.TempDir(), "shared")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stores := make([]*credStore, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stores[i], errs[i] = newCredentials(ctx, Options{StateDir: dir})
		}()
	}
	wg.Wait()
	running := 0
	for i := range 2 {
		switch {
		case errs[i] == nil:
			running++
			defer stores[i].close()
		case errs[i].Error() != secondServiceSentence:
			t.Fatalf("service %d: %v", i, errs[i])
		}
	}
	if running != 1 {
		t.Fatalf("%d services run on one state directory, want 1", running)
	}
}

// TestCredSessionLifecycle pins reuse, refresh near expiry, the D30
// re-acquire through the key lane after a refused refresh, revocation and
// the windowless pick.
func TestCredSessionLifecycle(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	ctx := context.Background()
	h := localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: "w1"}
	first, err := r.s.Session(ctx, alice, h, true)
	if err != nil {
		t.Fatal(err)
	}
	accepted := func() int {
		r.srv.mu.Lock()
		defer r.srv.mu.Unlock()
		return len(r.srv.accepted)
	}
	steps := []struct {
		name     string
		advance  time.Duration
		expire   bool
		start    bool
		wantSame bool
		checkins int
	}{
		{name: "start again reuses", start: true, wantSame: true, checkins: 1},
		{name: "plenty left is not refreshed", advance: 100 * time.Second, wantSame: true, checkins: 1},
		{name: "near expiry refreshes", advance: 90 * time.Second, wantSame: true, checkins: 1},
		{name: "refused refresh re-acquires", advance: 200 * time.Second, expire: true, checkins: 2},
	}
	prev := first
	for _, st := range steps {
		r.clock.Advance(st.advance)
		if st.expire {
			r.srv.expireSessions()
		}
		got, err := r.s.Session(ctx, alice, h, st.start)
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if (got.ID == prev.ID) != st.wantSame || got.Reused != st.start || got.Renewal.Class != RenewNone {
			t.Fatalf("%s: session %s reused %v renewal %+v, previous %s", st.name, got.ID, got.Reused, got.Renewal, prev.ID)
		}
		if !r.clock.Now().Before(got.ExpiresAt.Add(-renewBefore)) {
			t.Fatalf("%s: the session was not renewed: expires %v at %v", st.name, got.ExpiresAt, r.clock.Now())
		}
		if n := accepted(); n != st.checkins {
			t.Fatalf("%s: %d key check-ins, want %d", st.name, n, st.checkins)
		}
		prev = got
	}
	a, hs, err := r.s.held(prev)
	if err != nil {
		t.Fatal(err)
	}
	r.s.revoke(a, hs, prev.ID, "kill-switch push from the server")
	var refused *Refused
	if _, err := r.s.Session(ctx, alice, h, false); !errors.As(err, &refused) || refused.Sentence != revokedSentence("kill-switch push from the server") {
		t.Fatalf("revoked window without start: %v", err)
	}
	if _, err := r.s.Session(ctx, alice, localapi.Harness{Name: "claude-code"}, false); !errors.As(err, &refused) {
		t.Fatalf("windowless pick of only a revoked window: %v", err)
	}
	again, err := r.s.Session(ctx, alice, h, true)
	if err != nil || again.ID == prev.ID {
		t.Fatalf("start of a revoked window: %s, %v", again.ID, err)
	}
	picked, err := r.s.Session(ctx, alice, localapi.Harness{Name: "claude-code"}, false)
	if err != nil || picked.ID != again.ID {
		t.Fatalf("windowless pick: %s, %v, want %s", picked.ID, err, again.ID)
	}
	if _, err := r.s.Session(ctx, alice, localapi.Harness{Name: "codex", Session: "x"}, false); !errors.As(err, &refused) || refused.Sentence != noSessionSentence {
		t.Fatalf("unknown window: %v", err)
	}
	r.srv.assertCounters(t)
}

// TestCredRestartBetweenSendAndAnswer stops the service while a key
// check-in is in flight and its request is held in the network. Expected:
// the next service treats it as a lost answer and waits two minutes from
// when that request's nonce arrived, so the late copy fails at the nonce
// check, no revoke, a strictly rising counter, a live session.
func TestCredRestartBetweenSendAndAnswer(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)
	r.srv.with(func() {
		r.srv.delay = func(c int64) <-chan struct{} {
			if c == 1 {
				return release
			}
			return nil
		}
	})
	old := r.s
	go func() {
		_, _ = old.Session(context.Background(), alice, localapi.Harness{Name: "claude-code", Session: "w1"}, true)
	}()
	waitFor(t, "the check-in to reach strazad", func() bool {
		r.srv.mu.Lock()
		defer r.srv.mu.Unlock()
		return slices.Contains(r.srv.received, 1)
	})
	r.restart()
	var refused *Refused
	if _, err := r.start(alice, "w2"); !errors.As(err, &refused) || refused.Sentence != waitingSentence {
		t.Fatalf("start after the restart: got %v, want the waiting sentence", err)
	}
	r.clock.Advance(lostAnswerWait + time.Second)
	ses, err := r.start(alice, "w2")
	if err != nil {
		t.Fatalf("start after the wait: %v", err)
	}
	free()
	<-r.srv.lateDone
	r.srv.mu.Lock()
	reached := slices.Contains(r.srv.seen, 1)
	r.srv.mu.Unlock()
	if reached {
		t.Fatal("the late copy of counter 1 reached the counter check")
	}
	r.srv.assertCounters(t)
	assertLive(t, r, alice, []Session{ses})
}

// TestCredWindowlessStartsShareOneCheckin starts six windowless sessions of
// one harness while the first start still fetches the snapshot. Expected:
// one key check-in, and every start answers that session.
func TestCredWindowlessStartsShareOneCheckin(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	hold := make(chan struct{})
	r.srv.with(func() { r.srv.snapHold = hold })
	ids := make(chan string, 6)
	for range 6 {
		go func() {
			ses, err := r.s.Session(context.Background(), alice, localapi.Harness{Name: "claude-code"}, true)
			if err != nil {
				t.Error(err)
			}
			ids <- ses.ID
		}()
	}
	waitFor(t, "the first start's snapshot fetch", func() bool {
		reqs, _ := r.snapCounts()
		return reqs > 0
	})
	time.Sleep(200 * time.Millisecond)
	close(hold)
	var got []string
	for range 6 {
		got = append(got, <-ids)
	}
	r.srv.mu.Lock()
	accepted := len(r.srv.accepted)
	r.srv.mu.Unlock()
	slices.Sort(got)
	if accepted != 1 || got[0] != got[5] {
		t.Fatalf("%d check-ins and sessions %v, want one shared session", accepted, got)
	}
}

// TestCredQueuedStartGetsItsContextError pins that a caller whose time runs
// out while it waits for the key lock gets its context's error, not the
// waiting sentence, which names only a lost answer.
func TestCredQueuedStartGetsItsContextError(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	a := r.s.account(alice.UID)
	if err := a.keyLock.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer a.keyLock.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := r.s.Session(ctx, alice, localapi.Harness{Name: "claude-code", Session: "w1"}, true)
	var refused *Refused
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &refused) {
		t.Fatalf("a queued start whose time ran out: %v, want its context's error", err)
	}
}
