package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestCredCounterPersistedBeforeReturn pins the counter function: each call
// returns the next number only after the record on disk holds it, and a
// restart resumes after the last number handed out.
func TestCredCounterPersistedBeforeReturn(t *testing.T) {
	cases := []struct {
		name    string
		calls   int
		restart bool
	}{
		{"one call", 1, false},
		{"three calls", 3, false},
		{"three calls then a restart", 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			path := filepath.Join(r.dir, "u", alice.UID, recordFile)
			a := r.s.account(alice.UID)
			for want := int64(1); want <= int64(tc.calls); want++ {
				got, err := a.nextCounter(time.Time{})
				if err != nil {
					t.Fatalf("nextCounter: %v", err)
				}
				rec, err := readRecord(path)
				if err != nil || rec == nil {
					t.Fatalf("read the record: %v", err)
				}
				if got != want || rec.Counter != want {
					t.Fatalf("call %d: returned %d with %d on disk, want %d for both", want, got, rec.Counter, want)
				}
			}
			if !tc.restart {
				return
			}
			r.restart()
			got, err := r.s.account(alice.UID).nextCounter(time.Time{})
			if err != nil || got != int64(tc.calls)+1 {
				t.Fatalf("after a restart: got %d, %v, want %d", got, err, tc.calls+1)
			}
		})
	}
}

// TestCredCounterEveryAttemptTakesANumber pins that the retry after a nonce
// challenge takes a new number, so no number reaches the server twice, and
// that the number of the challenged attempt is never sent again.
func TestCredCounterEveryAttemptTakesANumber(t *testing.T) {
	cases := []struct {
		name     string
		stale    bool  // the cached nonce is past its two windows
		accepted int64 // the counter strazad accepts
		onDisk   int64
	}{
		{"fresh nonce", false, 1, 1},
		{"stale nonce", true, 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			if tc.stale {
				r.clock.Advance(3 * time.Minute)
			}
			if _, err := r.start(alice, "w1"); err != nil {
				t.Fatalf("start: %v", err)
			}
			r.srv.mu.Lock()
			accepted := append([]int64(nil), r.srv.accepted...)
			r.srv.mu.Unlock()
			if len(accepted) != 1 || accepted[0] != tc.accepted {
				t.Fatalf("strazad accepted %v, want [%d]", accepted, tc.accepted)
			}
			rec, _ := readRecord(filepath.Join(r.dir, "u", alice.UID, recordFile))
			if rec.Counter != tc.onDisk || !rec.WaitUntil.IsZero() {
				t.Fatalf("record counter %d wait %v, want %d and no wait", rec.Counter, rec.WaitUntil, tc.onDisk)
			}
			r.srv.assertCounters(t)
		})
	}
}

// TestCredCounterUnderKeyLock pins that the key lock serializes the
// counter: concurrent check-ins each get their own number.
func TestCredCounterUnderKeyLock(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	a := r.s.account(alice.UID)
	got := make(chan int64, 20)
	for range 20 {
		go func() {
			_ = a.keyLock.lock(context.Background())
			defer a.keyLock.unlock()
			n, err := a.nextCounter(time.Time{})
			if err != nil {
				t.Error(err)
			}
			got <- n
		}()
	}
	seen := map[int64]bool{}
	for range 20 {
		n := <-got
		if seen[n] {
			t.Fatalf("counter %d handed out twice", n)
		}
		seen[n] = true
	}
	for n := int64(1); n <= 20; n++ {
		if !seen[n] {
			t.Fatalf("counter %d skipped: %v", n, seen)
		}
	}
}
