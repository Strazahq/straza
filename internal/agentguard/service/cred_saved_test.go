package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// toolPre is a tool call every test policy allows.
var toolPre = policy.Event{Kind: policy.EventToolPre, Tool: "shell.exec", Command: "ls"}

// savedPath is the sessions file of c in the rig's state directory.
func (r *credRig) savedPath(c Caller) string {
	return filepath.Join(r.dir, "u", c.UID, sessionsFile)
}

// checkins returns how many key check-ins strazad accepted and received.
func (r *credRig) checkins() (accepted, received int) {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	return len(r.srv.accepted), len(r.srv.received)
}

// pushOf returns the push state status shows for c's window id.
func (r *credRig) pushOf(c Caller, id string) string {
	for _, ss := range r.s.Status(c).Sessions {
		if ss.Harness.Session == id {
			return ss.Push
		}
	}
	return ""
}

// waitStream waits until c's window id has its push stream up. A test that
// changes the fake after a start waits first, because a stream that
// connects after the change answers it with a renewal of its own, which
// moves the session or the check-in count the test then checks.
func (r *credRig) waitStream(t *testing.T, c Caller, id string) {
	t.Helper()
	waitFor(t, "the push stream of "+id, func() bool { return r.pushOf(c, id) == localapi.PushConnected })
}

// editSaved rewrites c's sessions file through edit, as a lost save or a
// crash would leave it.
func (r *credRig) editSaved(t *testing.T, c Caller, edit func(*savedSessions)) {
	t.Helper()
	raw, err := os.ReadFile(r.savedPath(c))
	var saved savedSessions
	if err == nil {
		err = json.Unmarshal(raw, &saved)
	}
	if err != nil {
		t.Fatal(err)
	}
	edit(&saved)
	if raw, err = json.Marshal(saved); err == nil {
		err = os.WriteFile(r.savedPath(c), raw, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// pulseNewPolicy activates a newer policy at strazad and lets a background
// snapshot check adopt it, and returns ses with the pin it moved to.
func (r *credRig) pulseNewPolicy(t *testing.T, ses Session) Session {
	t.Helper()
	r.srv.activate(t, 901)
	r.clock.Advance(31 * time.Second)
	if _, _, _, err := r.s.Policy(context.Background(), ses); err != nil {
		t.Fatal(err)
	}
	var got Session
	waitFor(t, "the background check to move the pin", func() bool {
		got, _ = r.s.Session(context.Background(), alice, ses.Harness, false)
		return got.SnapshotID == r.srv.snapID()
	})
	return got
}

// TestCredSavedSessionsResume pins that a restarted service resumes a
// window as its last change left it, with no check-in: the session held at
// start, a renewal's new token, a re-acquire's new session, a policy
// refusal with its deadline, a pin a background snapshot check moved, and
// the held snapshot's pin where a lost save left an older one.
// After the restart the window decides on the snapshot kept on disk while
// the snapshot route is down, a start reuses it with its packs, strazad
// accepts its token, its push stream connects again, and a kill switch
// sent on that stream revokes it.
func TestCredSavedSessionsResume(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		before func(t *testing.T, r *credRig, ses Session) Session
	}{
		{"held at start", func(_ *testing.T, _ *credRig, ses Session) Session { return ses }},
		{"renewed", func(t *testing.T, r *credRig, ses Session) Session {
			r.clock.Advance(time.Minute)
			got := r.renewNow(t, ses)
			if !got.ExpiresAt.After(ses.ExpiresAt) {
				t.Fatalf("the renewal kept the expiry %v", got.ExpiresAt)
			}
			return got
		}},
		{"re-acquired", func(t *testing.T, r *credRig, ses Session) Session {
			r.srv.expireSessions()
			if out, err := r.s.Decide(ctx, ses, toolPre, "", "", nil); err != nil || !out.Bounced {
				t.Fatalf("decide after strazad forgot the session: %+v, %v", out, err)
			}
			got, err := r.s.Session(ctx, alice, ses.Harness, false)
			if err != nil || got.ID == ses.ID {
				t.Fatalf("after the re-acquire: %+v, %v", got, err)
			}
			return got
		}},
		{"policy refused", func(t *testing.T, r *credRig, ses Session) Session {
			r.srv.activate(t, 901)
			r.srv.with(func() { r.srv.snapFail, r.srv.snapMsg = http.StatusForbidden, "the snapshot route is closed" })
			got := r.renewNow(t, ses)
			if got.PolicyRefused == "" || !got.PolicyDeadline.Equal(ses.ExpiresAt) {
				t.Fatalf("refusal %q deadline %v", got.PolicyRefused, got.PolicyDeadline)
			}
			return got
		}},
		{"pin moved by a background check", func(t *testing.T, r *credRig, ses Session) Session {
			return r.pulseNewPolicy(t, ses)
		}},
		{"older pin a lost save left", func(t *testing.T, r *credRig, ses Session) Session {
			got := r.pulseNewPolicy(t, ses)
			r.editSaved(t, alice, func(saved *savedSessions) { saved.Sessions[0].SnapshotID = ses.SnapshotID })
			return got
		}},
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
			want := tc.before(t, r, ses)
			accepted, received := r.checkins()
			r.restart()
			r.srv.with(func() { r.srv.snapFail, r.srv.snapMsg = http.StatusServiceUnavailable, "the snapshot store is down" })
			got, err := r.s.Session(ctx, alice, want.Harness, false)
			switch {
			case err != nil:
				t.Fatalf("the window after a restart: %v", err)
			case got.ID != want.ID || got.User != want.User || !slices.Equal(got.Roles, want.Roles) || got.SnapshotID != want.SnapshotID ||
				!got.IssuedAt.Equal(want.IssuedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) || got.ServerURL != want.ServerURL:
				t.Fatalf("resumed %+v, want %+v", got, want)
			case got.PolicyRefused != want.PolicyRefused || !got.PolicyDeadline.Equal(want.PolicyDeadline):
				t.Fatalf("resumed refusal %q at %v, want %q at %v", got.PolicyRefused, got.PolicyDeadline, want.PolicyRefused, want.PolicyDeadline)
			}
			if _, _, from, err := r.s.Policy(ctx, got); err != nil || from != want.SnapshotID {
				t.Fatalf("policy after a restart: from %s, %v, want %s", from, err, want.SnapshotID)
			}
			if out, err := r.s.Decide(ctx, got, toolPre, "", "", nil); err != nil || out.Bounced {
				t.Fatalf("decide after a restart: %+v, %v, want the saved token accepted", out, err)
			}
			again, err := r.s.Session(ctx, alice, want.Harness, true)
			if err != nil || !again.Reused || again.ID != want.ID || len(again.Packs) == 0 || !slices.Equal(again.Packs, want.Packs) {
				t.Fatalf("a start after a restart: %+v, %v, want the resumed session reused with its packs", again, err)
			}
			if again.StartText == nil || *again.StartText != fakeStartText {
				t.Fatalf("a start after a restart: start text %v, want the server's %q", again.StartText, fakeStartText)
			}
			if a, rc := r.checkins(); a != accepted || rc != received {
				t.Fatalf("the restart cost check-ins: accepted %d to %d, received %d to %d", accepted, a, received, rc)
			}
			r.waitStream(t, alice, "w1")
			r.srv.pushEvents <- "revocation"
			var refused *Refused
			waitFor(t, "the kill switch on the resumed stream", func() bool {
				_, err := r.s.Session(ctx, alice, want.Harness, false)
				return errors.As(err, &refused) && refused.Sentence == revokedSentence("kill-switch push from the server")
			})
			r.srv.assertCounters(t)
		})
	}
}

// TestCredSavedRevocationSurvives pins that a window revoked before a
// restart is still refused after it, with the same sentence, holds no push
// stream, and checks in again only at its next start.
func TestCredSavedRevocationSurvives(t *testing.T) {
	const deadline = "Your last sign-in on this machine was more than 30 days ago, and your Straza admin requires a new one. Run straza enroll and sign in with your work account."
	ctx := context.Background()
	cases := []struct {
		name   string
		revoke func(t *testing.T, r *credRig, ses Session) string
	}{
		{"a kill switch on the push stream", func(t *testing.T, r *credRig, ses Session) string {
			waitFor(t, "the stream", func() bool { return r.pushOf(alice, "w1") == localapi.PushConnected })
			r.srv.pushEvents <- "revocation"
			waitFor(t, "the revocation", func() bool { return r.s.Status(alice).Sessions[0].Revoked != "" })
			return "kill-switch push from the server"
		}},
		{"a renewal the server refused for good", func(t *testing.T, r *credRig, ses Session) string {
			r.srv.with(func() { r.srv.refreshRefuse = deadline })
			r.renewNow(t, ses)
			r.srv.with(func() { r.srv.refreshRefuse = "" })
			return deadline
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			ses, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			reason := tc.revoke(t, r, ses)
			accepted, _ := r.checkins()
			r.restart()
			var refused *Refused
			if _, err := r.s.Session(ctx, alice, ses.Harness, false); !errors.As(err, &refused) ||
				refused.Status != http.StatusForbidden || refused.Sentence != revokedSentence(reason) {
				t.Fatalf("the revoked window after a restart: %v, want 403 %q", err, revokedSentence(reason))
			}
			if _, err := r.s.Decide(ctx, ses, toolPre, "", "", nil); !errors.As(err, &refused) || refused.Sentence != revokedSentence(reason) {
				t.Fatalf("decide on the revoked window after a restart: %v", err)
			}
			if st := r.s.Status(alice); len(st.Sessions) != 1 || st.Sessions[0].Revoked != reason || st.Sessions[0].Push != localapi.PushOff {
				t.Fatalf("status after a restart: %+v", st.Sessions)
			}
			if a, _ := r.checkins(); a != accepted {
				t.Fatalf("the revoked window checked in after a restart: %d to %d", accepted, a)
			}
			again, err := r.start(alice, "w1")
			if err != nil || again.ID == ses.ID || again.Reused {
				t.Fatalf("the next start of the revoked window: %+v, %v", again, err)
			}
			r.srv.assertCounters(t)
		})
	}
}

// TestCredSavedForgetRule pins that the forget rule holds across a restart.
// A window forgotten before it, one that lapsed past the rule while the
// service was stopped, one whose key a new enrollment replaced, and one
// saved under an earlier key do not come back, and the load leaves none of
// their tokens on disk. A window that lapsed less than the rule ago comes
// back, and its next call re-acquires it, because strazad refuses its
// expired token. Every load removes a temp file a crash left behind.
func TestCredSavedForgetRule(t *testing.T) {
	ctx := context.Background()
	past := 300*time.Second + forgetAfter + time.Second
	cases := []struct {
		name    string
		restart func(t *testing.T, r *credRig)
		gone    bool
	}{
		{"forgotten before the restart", func(t *testing.T, r *credRig) {
			r.clock.Advance(past)
			r.s.forgetLapsed(r.s.account(alice.UID))
			r.restart()
		}, true},
		{"lapsed past the rule while stopped", func(t *testing.T, r *credRig) {
			r.stop()
			r.clock.Advance(past)
			r.open()
		}, true},
		{"lapsed half an hour ago while stopped", func(t *testing.T, r *credRig) {
			r.stop()
			r.clock.Advance(300*time.Second + 30*time.Minute)
			r.open()
		}, false},
		{"lapsed a day ago while stopped", func(t *testing.T, r *credRig) {
			r.stop()
			r.clock.Advance(300*time.Second + 24*time.Hour)
			r.open()
		}, false},
		{"replaced by a new enrollment", func(t *testing.T, r *credRig) {
			r.enroll(alice)
			r.restart()
		}, true},
		{"saved under an earlier key", func(t *testing.T, r *credRig) {
			old, err := os.ReadFile(r.savedPath(alice))
			if err != nil {
				t.Fatal(err)
			}
			r.enroll(alice)
			r.stop()
			if err := os.WriteFile(r.savedPath(alice), old, 0o600); err != nil {
				t.Fatal(err)
			}
			r.open()
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			ses, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			stale := filepath.Join(filepath.Dir(r.savedPath(alice)), "."+sessionsFile+".tmp-left-by-a-crash")
			if err := os.WriteFile(stale, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			tc.restart(t, r)
			if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the stale temp file after the load: %v, want it removed", err)
			}
			var saved savedSessions
			raw, err := os.ReadFile(r.savedPath(alice))
			if err != nil || json.Unmarshal(raw, &saved) != nil {
				t.Fatalf("the sessions file after the load: %v", err)
			}
			if held := len(r.s.Status(alice).Sessions); held != len(saved.Sessions) || (held == 0) != tc.gone {
				t.Fatalf("held %d and saved %d windows, want the window gone %v", held, len(saved.Sessions), tc.gone)
			}
			accepted, _ := r.checkins()
			got, err := r.s.Session(ctx, alice, ses.Harness, false)
			var refused *Refused
			switch {
			case tc.gone && (!errors.As(err, &refused) || refused.Sentence != noSessionSentence):
				t.Fatalf("got %+v, %v, want the window forgotten", got, err)
			case !tc.gone && (err != nil || got.ID == ses.ID || !got.ExpiresAt.After(r.clock.Now())):
				t.Fatalf("got %+v, %v, want the window back and re-acquired", got, err)
			}
			want := accepted
			if !tc.gone {
				want++
			}
			if a, _ := r.checkins(); a != want {
				t.Fatalf("the call made %d check-ins, want %d", a-accepted, want-accepted)
			}
		})
	}
}

// TestCredSavedKeepsRevokedWindows pins that a revoked window keeps its
// refusal across a restart a day after its expiry, and that it is gone once
// seven days past its expiry have passed.
func TestCredSavedKeepsRevokedWindows(t *testing.T) {
	ctx := context.Background()
	r := newCredRig(t)
	r.enroll(alice)
	ses, err := r.start(alice, "w1")
	if err != nil {
		t.Fatal(err)
	}
	a, hs, err := r.s.held(ses)
	if err != nil {
		t.Fatal(err)
	}
	r.s.revoke(a, hs, ses.ID, "kill-switch push from the server")
	r.stop()
	r.clock.Advance(300*time.Second + 24*time.Hour)
	r.open()
	var refused *Refused
	if _, err := r.s.Session(ctx, alice, ses.Harness, false); !errors.As(err, &refused) || refused.Status != http.StatusForbidden {
		t.Fatalf("a revoked window after a restart a day later: %v, want its refusal", err)
	}
	r.stop()
	r.clock.Advance(forgetAfter)
	r.open()
	if _, err := r.s.Session(ctx, alice, ses.Harness, false); !errors.As(err, &refused) || refused.Sentence != noSessionSentence {
		t.Fatalf("a revoked window past seven days: %v, want it forgotten", err)
	}
}

// TestCredSavedFifteenWindows pins that 15 open windows of one user all
// decide after a service restart, with no new check-in, no snapshot
// download and no key-reuse revocation at strazad, and every one of them
// gets its push stream back.
func TestCredSavedFifteenWindows(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	sessions := startMany(t, r, alice, "w", 15)
	accepted, received := r.checkins()
	_, downloads := r.snapCounts()
	r.restart()
	errs := make([]error, len(sessions))
	var wg sync.WaitGroup
	for i, ses := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			got, err := r.s.Session(ctx, alice, ses.Harness, false)
			if err == nil && got.ID != ses.ID {
				err = fmt.Errorf("resumed session %s, want %s", got.ID, ses.ID)
			}
			if err == nil {
				_, _, _, err = r.s.Policy(ctx, got)
			}
			if err == nil {
				var out DecideOutcome
				if out, err = r.s.Decide(ctx, got, toolPre, "", "", nil); err == nil && out.Bounced {
					err = errors.New("the decide bounced, so the saved token was not used")
				}
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("window %s after a restart: %v", sessions[i].Harness.Session, err)
		}
	}
	if a, rc := r.checkins(); a != accepted || rc != received {
		t.Fatalf("the restart cost check-ins: accepted %d to %d, received %d to %d", accepted, a, received, rc)
	}
	if _, d := r.snapCounts(); d != downloads {
		t.Fatalf("the restart downloaded the snapshot again: %d to %d", downloads, d)
	}
	r.srv.assertCounters(t)
	waitFor(t, "every resumed push stream", func() bool {
		for _, ss := range r.s.Status(alice).Sessions {
			if ss.Push != localapi.PushConnected {
				return false
			}
		}
		return true
	})
}

// TestCredLapsedWindowDropsPacks pins that a window whose token expired
// over refreshRefusedAfter ago keeps no knowledge packs, in memory after the
// sweep or on disk, because only a start that reuses a live window reads
// them, and that its re-acquire brings them back.
func TestCredLapsedWindowDropsPacks(t *testing.T) {
	ctx := context.Background()
	r := newCredRig(t)
	r.enroll(alice)
	ses, err := r.start(alice, "w1")
	if err != nil || len(ses.Packs) == 0 {
		t.Fatalf("start: %+v, %v, want a session with packs", ses, err)
	}
	r.clock.Advance(300*time.Second + time.Hour)
	a := r.s.account(alice.UID)
	r.s.forgetLapsed(a)
	if _, err := r.start(alice, "w2"); err != nil {
		t.Fatal(err)
	}
	var saved savedSessions
	raw, err := os.ReadFile(r.savedPath(alice))
	if err != nil || json.Unmarshal(raw, &saved) != nil {
		t.Fatalf("the sessions file: %v", err)
	}
	for _, v := range saved.Sessions {
		if lapsed := v.Harness.Session == "w1"; lapsed != (len(v.Packs) == 0) {
			t.Fatalf("window %s saved with %d packs", v.Harness.Session, len(v.Packs))
		}
	}
	if hs := a.find(ses.Harness); hs == nil || len(a.copyOf(hs).Packs) != 0 {
		t.Fatal("the lapsed window still holds its packs in memory")
	}
	if _, err := r.s.Session(ctx, alice, ses.Harness, false); err != nil {
		t.Fatal(err)
	}
	again, err := r.start(alice, "w1")
	if err != nil || !again.Reused || len(again.Packs) == 0 {
		t.Fatalf("a start after the re-acquire: %+v, %v, want the live window reused with its packs", again, err)
	}
}
