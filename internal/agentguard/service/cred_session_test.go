package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// waitFor polls cond for up to five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestCredRefusals pins the sentences a caller gets before any check-in:
// not enrolled, and the server's own enrollment refusal passed through
// unchanged.
func TestCredRefusals(t *testing.T) {
	r := newCredRig(t)
	var refused *Refused
	if _, err := r.start(alice, "w1"); !errors.As(err, &refused) || refused.Sentence != notEnrolledSentence || refused.Status != http.StatusForbidden {
		t.Fatalf("start before enrolling: %v", err)
	}
	if st := r.s.Status(alice); st.Enrollment != nil {
		t.Fatalf("status before enrolling: %+v", st)
	}
	const custody = "Straza refused this machine because its key is kept in your own account instead of by the Straza background service, " +
		"and your Straza admin requires the service. Install Straza from your software portal, then run `straza enroll`."
	r.srv.with(func() { r.srv.enrollRefuse = custody })
	_, err := r.s.Enroll(context.Background(), alice, localapi.EnrollRequest{ServerURL: r.srv.serverURL()}, func(localapi.EnrollEvent) {})
	if !errors.As(err, &refused) || refused.Sentence != custody || refused.Status != http.StatusForbidden {
		t.Fatalf("refused enrollment: %v", err)
	}
	if _, err := r.start(alice, "w1"); !errors.As(err, &refused) || refused.Sentence != notEnrolledSentence {
		t.Fatalf("a refused enrollment left something enrolled: %v", err)
	}
}

// TestCredNotEnrolledNamesTheAgentLane pins the next step a session start
// names before any enrollment: both lanes for an OS account that holds
// nothing, and the headless enrollment for one that holds an agent's key
// from keygen, also when keygen --force replaced an enrolled agent's key.
// Each enroll command carries --server unless the managed configuration
// names the server, which the service then enrolls at.
func TestCredNotEnrolledNamesTheAgentLane(t *testing.T) {
	sam := Caller{UID: testUID(65532)}
	keygen := func(force bool) func(*testing.T, *credRig) {
		return func(t *testing.T, r *credRig) {
			if _, err := r.s.Keygen(context.Background(), sam, localapi.KeygenRequest{Username: "sam", Force: force}); err != nil {
				t.Fatalf("keygen: %v", err)
			}
		}
	}
	const (
		nothing = "This machine isn't enrolled for you yet. A person runs `straza enroll --server <strazad url>` and signs in with their work account. " +
			"An AI agent runs `straza enroll --server <strazad url> --headless` with its registered key or the client secret IT installed for it."
		nothingManaged = "This machine isn't enrolled for you yet. A person runs `straza enroll` and signs in with their work account. " +
			"An AI agent runs `straza enroll --headless` with its registered key or the client secret IT installed for it."
		key = "This machine isn't enrolled for you yet. This OS account holds an AI agent's key from `straza keygen`. " +
			"Once an admin has registered its public key, run `straza enroll --server <strazad url> --headless`."
		keyManaged = "This machine isn't enrolled for you yet. This OS account holds an AI agent's key from `straza keygen`. " +
			"Once an admin has registered its public key, run `straza enroll --headless`."
	)
	cases := []struct {
		name    string
		managed bool
		setup   func(*testing.T, *credRig)
		want    string
	}{
		{"nothing held", false, func(*testing.T, *credRig) {}, nothing},
		{"nothing held, managed", true, func(*testing.T, *credRig) {}, nothingManaged},
		{"an agent key from keygen", false, keygen(false), key},
		{"an agent key from keygen, managed", true, keygen(false), keyManaged},
		{"keygen --force over an enrolled agent", false, func(t *testing.T, r *credRig) {
			keygen(false)(t, r)
			if _, err := r.s.Enroll(context.Background(), sam, localapi.EnrollRequest{ServerURL: r.srv.serverURL(), Headless: true}, func(localapi.EnrollEvent) {}); err != nil {
				t.Fatalf("headless enroll: %v", err)
			}
			keygen(true)(t, r)
		}, key},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			tc.setup(t, r)
			if tc.managed {
				manageServer(t, "https://straza.example")
			}
			var refused *Refused
			if _, err := r.start(sam, "w1"); !errors.As(err, &refused) || refused.Sentence != tc.want || refused.Status != http.StatusForbidden {
				t.Fatalf("start = %v, want 403 %q", err, tc.want)
			}
			if st := r.s.Status(sam); st.Enrollment != nil {
				t.Fatalf("status reports an enrollment: %+v", st.Enrollment)
			}
		})
	}
}

// manageServer writes a managed configuration that names server into the
// managed root the rig set.
func manageServer(t *testing.T, server string) {
	t.Helper()
	path := filepath.Join(os.Getenv("STRAZA_SYSTEM"), "config.yaml")
	if err := os.WriteFile(path, []byte("serverUrl: "+server+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// personChangedFor is a person's machine-changed refusal on a machine whose
// managed configuration names no server, after enrolling at server.
func personChangedFor(server string) string {
	return "This machine's identity changed since it was enrolled, so Straza stopped using its key. Run `straza enroll --server " + server + "` to enroll it again."
}

// TestCredMachineChangedNamesTheLane pins the refusal after the machine
// identity changed: a person enrolls again, an agent on the key
// lane makes a new key, and an agent on the client-secret lane, or with a
// record from before lanes, enrolls headless again. The enroll command names
// the enrolled server unless the managed configuration names one.
func TestCredMachineChangedNamesTheLane(t *testing.T) {
	const head = "This machine's identity changed since it was enrolled, so Straza stopped using its key. "
	cases := []struct {
		name    string
		managed bool
		rec     enrollment
		want    string
	}{
		{"a person", false, enrollment{ServerURL: "https://s.example", Kind: localapi.KindPerson, Username: "alice"}, personChangedFor("https://s.example")},
		{"a person, managed", true, enrollment{ServerURL: "https://s.example", Kind: localapi.KindPerson, Username: "alice"},
			head + "Run `straza enroll` to enroll it again."},
		{"an agent on the key lane", false, enrollment{ServerURL: "https://s.example", Kind: localapi.KindAgent, Lane: laneKey, Username: "sam"},
			head + "Run `straza keygen --force --user sam`, have an admin register the new public key it prints, and then run `straza enroll --server https://s.example --headless`."},
		{"an agent on the key lane, managed", true, enrollment{ServerURL: "https://s.example", Kind: localapi.KindAgent, Lane: laneKey, Username: "sam"},
			head + "Run `straza keygen --force --user sam`, have an admin register the new public key it prints, and then run `straza enroll --headless`."},
		{"an agent on the client-secret lane", false, enrollment{ServerURL: "https://s.example", Kind: localapi.KindAgent, Lane: laneSecret, Username: "sam"},
			head + "Run `straza enroll --server https://s.example --headless --user sam` to enroll the agent again."},
		{"an agent on the client-secret lane, managed", true, enrollment{ServerURL: "https://s.example", Kind: localapi.KindAgent, Lane: laneSecret, Username: "sam"},
			head + "Run `straza enroll --headless --user sam` to enroll the agent again."},
		{"an agent record with no lane", false, enrollment{ServerURL: "https://s.example", Kind: localapi.KindAgent, Username: "sam"},
			head + "Run `straza enroll --server https://s.example --headless --user sam` to enroll the agent again."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STRAZA_SYSTEM", t.TempDir())
			if tc.managed {
				manageServer(t, "https://s.example")
			}
			if got := changedSentence(tc.rec); got != tc.want {
				t.Errorf("changedSentence =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}

	// The reviewer's probe end to end: an agent that enrolled on the key
	// lane, then saw its machine identity change.
	r := newCredRig(t)
	sam := Caller{UID: testUID(65532)}
	if _, err := r.s.Keygen(context.Background(), sam, localapi.KeygenRequest{Username: "sam"}); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if _, err := r.s.Enroll(context.Background(), sam, localapi.EnrollRequest{ServerURL: r.srv.serverURL(), Headless: true}, func(localapi.EnrollEvent) {}); err != nil {
		t.Fatalf("headless enroll: %v", err)
	}
	if err := os.WriteFile(r.machine, []byte("machine-two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.restart()
	want := head + "Run `straza keygen --force --user sam`, have an admin register the new public key it prints, and then run `straza enroll --server " +
		r.srv.serverURL() + " --headless`."
	var refused *Refused
	if _, err := r.start(sam, "w1"); !errors.As(err, &refused) || refused.Sentence != want || refused.Status != http.StatusForbidden {
		t.Fatalf("start after the machine changed = %v, want 403 %q", err, want)
	}
}

// TestCredEnrollPerson pins what a person's enrollment streams and keeps.
func TestCredEnrollPerson(t *testing.T) {
	r := newCredRig(t)
	var events []localapi.EnrollEvent
	res, err := r.s.Enroll(context.Background(), alice, localapi.EnrollRequest{ServerURL: r.srv.serverURL()},
		func(ev localapi.EnrollEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Event != localapi.EnrollCode || events[0].UserCode != "ABCD-EFGH" {
		t.Fatalf("streamed events %+v, want the code", events)
	}
	if res.Kind != localapi.KindPerson || res.Username != "alice" || res.Custody != localapi.CustodyAccount || res.KeyThumbprint == "" {
		t.Fatalf("enroll result %+v", res)
	}
	st := r.s.Status(alice)
	if st.Enrollment == nil || st.Enrollment.DeviceID != res.DeviceID || st.Enrollment.KeyThumbprint != res.KeyThumbprint {
		t.Fatalf("status after enrolling: %+v", st.Enrollment)
	}
	again := r.enroll(alice)
	if again.KeyThumbprint == res.KeyThumbprint {
		t.Fatal("a second enrollment reused the key")
	}
}

// TestCredDecideBounce pins Decide: the status and correlation of the
// answer, and one renewal through the key lane after a 401.
func TestCredDecideBounce(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	ses, err := r.start(alice, "w1")
	if err != nil {
		t.Fatal(err)
	}
	r.waitStream(t, alice, "w1")
	ev := policy.Event{Kind: policy.EventToolPre, Tool: "shell.exec", Command: "ls"}
	out, err := r.s.Decide(context.Background(), ses, ev, "", "")
	if err != nil || out.Answer.Effect != policy.EffectAllow || out.Status != http.StatusOK || out.Bounced {
		t.Fatalf("decide: %+v, %v", out, err)
	}
	r.srv.expireSessions()
	out, err = r.s.Decide(context.Background(), ses, ev, "", "")
	if err != nil || out.Answer.Effect != policy.EffectAllow || out.Status != http.StatusOK || !out.Bounced {
		t.Fatalf("decide after the server forgot the session: %+v, %v", out, err)
	}
	if now, _ := r.s.Session(context.Background(), alice, ses.Harness, false); now.ID == ses.ID {
		t.Fatal("the bounce did not re-acquire the session")
	}
	r.srv.assertCounters(t)
}

// TestCredPolicyVerifiedOnce pins that the snapshot is fetched once,
// verified once, and kept on disk, so a restart needs no fetch.
func TestCredPolicyVerifiedOnce(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	for _, id := range []string{"w1", "w2"} {
		ses, err := r.start(alice, id)
		if err != nil {
			t.Fatal(err)
		}
		eng, maxAge, from, err := r.s.Policy(context.Background(), ses)
		if err != nil || eng == nil || maxAge != 900 || ses.SnapshotID != r.srv.snapID() || from != ses.SnapshotID {
			t.Fatalf("policy of %s: %v, %d, from %s, %v, pin %s", id, eng, maxAge, from, err, ses.SnapshotID)
		}
	}
	r.restart()
	ses, err := r.start(alice, "w3")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := r.s.Policy(context.Background(), ses); err != nil {
		t.Fatalf("policy after a restart: %v", err)
	}
	r.srv.mu.Lock()
	snaps := r.srv.snaps
	r.srv.mu.Unlock()
	if snaps != 1 {
		t.Fatalf("the snapshot was downloaded %d times, want once", snaps)
	}
}

// TestCredRecordDrains pins that a record reaches strazad through a live
// session of its uid.
func TestCredRecordDrains(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	if _, err := r.start(alice, "w1"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := r.s.Record(alice, "straza.audit.tool", map[string]any{"session": "s", "effect": "allow"}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the drain", func() bool {
		r.srv.mu.Lock()
		defer r.srv.mu.Unlock()
		return r.srv.batches == 3
	})
	r.waitDrained(t, alice)
}

// TestCredAgentLane pins an agent: keygen prints a public key once, the
// headless enrollment proves the key, the ID token is fetched once for
// many sessions, and every session is bound to the key.
func TestCredAgentLane(t *testing.T) {
	r := newCredRig(t)
	sam := Caller{UID: testUID(65532)}
	ctx := context.Background()
	key, err := r.s.Keygen(ctx, sam, localapi.KeygenRequest{Username: "sam"})
	if raw, derr := base64.StdEncoding.DecodeString(key.PublicKey); err != nil || derr != nil || len(raw) != 32 {
		t.Fatalf("keygen: %+v, %v", key, err)
	}
	var refused *Refused
	if _, err := r.s.Keygen(ctx, sam, localapi.KeygenRequest{Username: "sam"}); !errors.As(err, &refused) || refused.Sentence != keyExistsSentence {
		t.Fatalf("second keygen without force: %v", err)
	}
	res, err := r.s.Enroll(ctx, sam, localapi.EnrollRequest{ServerURL: r.srv.serverURL(), Headless: true}, func(localapi.EnrollEvent) {})
	if err != nil || res.Kind != localapi.KindAgent || res.Username != "sam" {
		t.Fatalf("headless enroll: %+v, %v", res, err)
	}
	sessions := startMany(t, r, sam, "agent", 5)
	for _, ses := range sessions {
		if ses.User != "sam" {
			t.Fatalf("agent session user %q", ses.User)
		}
		if out, err := r.s.Decide(ctx, ses, policy.Event{Kind: policy.EventToolPre, Tool: "shell.exec"}, "", ""); err != nil || out.Bounced {
			t.Fatalf("a bound agent session's decide: %+v, %v", out, err)
		}
	}
	r.srv.mu.Lock()
	tokens := r.srv.tokens
	r.srv.mu.Unlock()
	if tokens != 1 {
		t.Fatalf("the agent's ID token was fetched %d times, want once", tokens)
	}
}

// TestCredPushStream pins the push stream of a window: connected once it is
// up, named by the harness's own session id in status, closed once its
// session lapses, and a revocation marks only that window revoked while
// the service keeps running, so a later start checks in again.
func TestCredPushStream(t *testing.T) {
	r := newCredRig(t)
	r.s.pushCheck = 20 * time.Millisecond
	r.enroll(alice)
	window := func(id string) localapi.SessionStatus {
		for _, ss := range r.s.Status(alice).Sessions {
			if ss.Harness.Session == id {
				return ss
			}
		}
		return localapi.SessionStatus{}
	}
	first, err := r.start(alice, "w1")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "w1's stream", func() bool { return window("w1").Push == localapi.PushConnected })
	r.srv.pushEvents <- "revocation"
	waitFor(t, "w1's revocation", func() bool {
		ss := window("w1")
		return ss.Revoked != "" && ss.Push == localapi.PushOff
	})
	var refused *Refused
	if _, err := r.s.Session(context.Background(), alice, first.Harness, false); !errors.As(err, &refused) || refused.Status != http.StatusForbidden {
		t.Fatalf("revoked window without start: %v", err)
	}
	if _, err := r.start(alice, "w2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "w2's stream", func() bool { return window("w2").Push == localapi.PushConnected })
	r.clock.Advance(301 * time.Second)
	waitFor(t, "w2's stream to close after its session lapsed", func() bool { return window("w2").Push == localapi.PushOff })
	if ss := window("w2"); ss.Revoked != "" {
		t.Fatalf("a lapsed session counts as revoked: %+v", ss)
	}
	again, err := r.start(alice, "w1")
	if err != nil || again.ID == first.ID {
		t.Fatalf("start of the revoked window: %s, %v", again.ID, err)
	}
	waitFor(t, "w1's new stream", func() bool { return window("w1").Push == localapi.PushConnected })
}

// TestCredForgetsLapsedSessions pins the sweep of held sessions: one whose
// expiry passed more than seven days ago is forgotten, revoked or not, and
// its window then answers the no-session refusal until a start checks in
// again. A younger one stays: its next call re-acquires it however long it
// sat unused, and a revoked one keeps its refusal. The drain ticker runs
// the sweep.
func TestCredForgetsLapsedSessions(t *testing.T) {
	cases := []struct {
		name    string
		revoke  bool
		advance time.Duration // past the session's issue time; it expires at 300 s
		ticker  bool          // sweep by the drain ticker instead of directly
		gone    bool
	}{
		{name: "live", advance: 0},
		{name: "lapsed half an hour ago", advance: 300*time.Second + 30*time.Minute},
		{name: "lapsed a day ago", advance: 300*time.Second + 24*time.Hour},
		{name: "lapsed just under seven days ago", advance: 300*time.Second + forgetAfter - time.Second},
		{name: "lapsed over seven days ago", advance: 300*time.Second + forgetAfter + time.Second, gone: true},
		{name: "revoked and lapsed a day ago", revoke: true, advance: 300*time.Second + 24*time.Hour},
		{name: "revoked and lapsed over seven days ago", revoke: true, advance: 300*time.Second + forgetAfter + time.Second, gone: true},
		{name: "revoked a moment ago", revoke: true, advance: time.Second},
		{name: "swept by the drain ticker", advance: 300*time.Second + forgetAfter + time.Second, ticker: true, gone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			if tc.ticker {
				r.s.drainEvery = 20 * time.Millisecond
			}
			r.enroll(alice)
			ses, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			a, hs, err := r.s.held(ses)
			if err != nil {
				t.Fatal(err)
			}
			if tc.revoke {
				r.s.revoke(a, hs, ses.ID, "kill-switch push from the server")
			}
			r.clock.Advance(tc.advance)
			held := func() bool { return len(r.s.Status(alice).Sessions) == 1 }
			if tc.ticker {
				waitFor(t, "the ticker's sweep", func() bool { return !held() })
			} else {
				r.s.forgetLapsed(a)
			}
			if held() == tc.gone {
				t.Fatalf("held %v after the sweep, want forgotten %v", held(), tc.gone)
			}
			if !tc.gone {
				got, err := r.s.Session(context.Background(), alice, ses.Harness, false)
				var refused *Refused
				switch {
				case tc.revoke && (!errors.As(err, &refused) || refused.Status != http.StatusForbidden):
					t.Fatalf("a kept revoked window without start: %v, want its refusal", err)
				case !tc.revoke && (err != nil || !got.ExpiresAt.After(r.clock.Now())):
					t.Fatalf("a kept window's next call: %+v, %v, want it live again", got, err)
				}
				return
			}
			if hs.pushState() != localapi.PushOff {
				t.Fatalf("a forgotten session's stream is %s", hs.pushState())
			}
			var refused *Refused
			if _, err := r.s.Session(context.Background(), alice, ses.Harness, false); !errors.As(err, &refused) || refused.Sentence != noSessionSentence {
				t.Fatalf("a forgotten window without start: %v", err)
			}
			again, err := r.start(alice, "w1")
			if err != nil || again.ID == ses.ID || again.Reused {
				t.Fatalf("start of a forgotten window: %+v, %v", again, err)
			}
		})
	}
}

// TestCredAgentCannotReplacePerson pins that an agent running as a person
// cannot unenroll the person's machine: keygen, with or without --force,
// and a headless enrollment on a uid that holds a person's enrollment are
// refused, and the person's key keeps working. The headless refusal comes
// before the lane pick, so it covers the key lane and the client-secret
// lane alike.
func TestCredAgentCannotReplacePerson(t *testing.T) {
	cases := []struct {
		name string
		do   func(r *credRig) error
	}{
		{"keygen", func(r *credRig) error {
			_, err := r.s.Keygen(context.Background(), alice, localapi.KeygenRequest{Username: "sam"})
			return err
		}},
		{"keygen --force", func(r *credRig) error {
			_, err := r.s.Keygen(context.Background(), alice, localapi.KeygenRequest{Username: "sam", Force: true})
			return err
		}},
		{"headless enroll", func(r *credRig) error {
			_, err := r.s.Enroll(context.Background(), alice, localapi.EnrollRequest{ServerURL: r.srv.serverURL(), Headless: true, Username: "sam"},
				func(localapi.EnrollEvent) {})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			enrolled := r.enroll(alice)
			var refused *Refused
			if err := tc.do(r); !errors.As(err, &refused) || refused.Sentence != personKeptSentence || refused.Status != http.StatusConflict {
				t.Fatalf("got %v, want the 409 person-kept sentence", err)
			}
			st := r.s.Status(alice)
			if st.Enrollment == nil || st.Enrollment.Kind != localapi.KindPerson || st.Enrollment.KeyThumbprint != enrolled.KeyThumbprint {
				t.Fatalf("the person's enrollment changed: %+v", st.Enrollment)
			}
			if _, err := r.start(alice, "w1"); err != nil {
				t.Fatalf("the person's key stopped working: %v", err)
			}
		})
	}
}

// TestCredPushRefusedAtConnect pins that a push connect strazad refuses,
// as it does for a revoked session, user or device, renews the session at
// once, and the renewal's own rules decide: a refused token re-acquires
// the window, and a refusal of the enrollment is recorded as terminal with
// the server's sentence.
func TestCredPushRefusedAtConnect(t *testing.T) {
	const deadline = "Your last sign-in on this machine was more than 30 days ago, and your Straza admin requires a new one. Run straza enroll and sign in with your work account."
	cases := []struct {
		name       string
		refuse     func(f *fakeStrazad)
		reacquires bool
	}{
		{"401 at connect re-acquires", func(f *fakeStrazad) { f.sessions = map[string]fakeSession{} }, true},
		{"403 at connect records the refusal", func(f *fakeStrazad) { f.pushRefuse, f.refreshRefuse = http.StatusForbidden, deadline }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			first, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			current := func() Session {
				ses, err := r.s.Session(context.Background(), alice, first.Harness, false)
				if err != nil {
					t.Fatal(err)
				}
				return ses
			}
			waitFor(t, "the stream", func() bool { return r.s.Status(alice).Sessions[0].Push == localapi.PushConnected })
			r.srv.with(func() { tc.refuse(r.srv) })
			r.srv.pushEvents <- "close"
			ev := policy.Event{Kind: policy.EventToolPre, Tool: "shell.exec", Command: "ls"}
			if tc.reacquires {
				waitFor(t, "the re-acquire", func() bool { return current().ID != first.ID })
				waitFor(t, "the new stream", func() bool { return r.s.Status(alice).Sessions[0].Push == localapi.PushConnected })
				if _, err := r.s.Decide(context.Background(), current(), ev, "", ""); err != nil {
					t.Fatalf("decide after the re-acquire: %v", err)
				}
				return
			}
			var refused *Refused
			waitFor(t, "the terminal renewal to revoke the window", func() bool {
				_, err := r.s.Session(context.Background(), alice, first.Harness, false)
				return errors.As(err, &refused) && refused.Sentence == revokedSentence(deadline)
			})
			if _, err := r.s.Decide(context.Background(), first, ev, "", ""); !errors.As(err, &refused) || refused.Status != http.StatusForbidden {
				t.Fatalf("decide after the terminal renewal: %v, want the revoked refusal", err)
			}
			again, err := r.start(alice, first.Harness.Session)
			if err != nil || again.ID == first.ID {
				t.Fatalf("start after the terminal renewal: %+v, %v", again, err)
			}
		})
	}
}

// TestCredTerminalRenewalRevokes pins that a renewal the server refuses
// for good, on the refresh or on the re-acquire, marks the window revoked
// with the server's sentence: the next call without start is refused, a
// decide is refused, and a start checks in again.
func TestCredTerminalRenewalRevokes(t *testing.T) {
	const deadline = "Your last sign-in on this machine was more than 30 days ago, and your Straza admin requires a new one. Run straza enroll and sign in with your work account."
	cases := []struct {
		name   string
		refuse func(f *fakeStrazad)
		clear  func(f *fakeStrazad)
	}{
		{"refresh refused", func(f *fakeStrazad) { f.refreshRefuse = deadline }, func(f *fakeStrazad) { f.refreshRefuse = "" }},
		{"re-acquire refused", func(f *fakeStrazad) { f.sessions, f.checkinRefuse = map[string]fakeSession{}, deadline },
			func(f *fakeStrazad) { f.checkinRefuse = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			first, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			r.srv.with(func() { tc.refuse(r.srv) })
			r.clock.Advance(190 * time.Second)
			var refused *Refused
			ctx := context.Background()
			if _, err := r.s.Session(ctx, alice, first.Harness, false); !errors.As(err, &refused) || refused.Sentence != revokedSentence(deadline) {
				t.Fatalf("the renewing call: %v, want the revoked sentence with the server's", err)
			}
			if _, err := r.s.Session(ctx, alice, first.Harness, false); !errors.As(err, &refused) || refused.Status != http.StatusForbidden {
				t.Fatalf("the next call: %v, want the revoked refusal", err)
			}
			if _, err := r.s.Decide(ctx, first, policy.Event{Kind: policy.EventToolPre, Tool: "shell.exec"}, "", ""); !errors.As(err, &refused) {
				t.Fatalf("decide: %v, want the revoked refusal", err)
			}
			if st := r.s.Status(alice); st.Sessions[0].Revoked != deadline {
				t.Fatalf("status revoked %q", st.Sessions[0].Revoked)
			}
			r.srv.with(func() { tc.clear(r.srv) })
			again, err := r.start(alice, "w1")
			if err != nil || again.ID == first.ID {
				t.Fatalf("start after the refusal: %+v, %v", again, err)
			}
			r.srv.assertCounters(t)
		})
	}
}

// TestCredPolicyAfterReenrollDuringFetch pins that a decide whose snapshot
// fetch lands after a re-enrollment dropped the account's snapshot gets
// the missing-snapshot error, not a panic.
func TestCredPolicyAfterReenrollDuringFetch(t *testing.T) {
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
	a.mu.Lock()
	hs.s.SnapshotID = "a-pin-the-account-does-not-hold"
	a.mu.Unlock()
	hold := make(chan struct{})
	r.srv.with(func() { r.srv.snapHold = hold })
	before, _ := r.snapCounts()
	done := make(chan error, 1)
	go func() {
		_, _, _, err := r.s.Policy(context.Background(), ses)
		done <- err
	}()
	waitFor(t, "the fetch to reach strazad", func() bool {
		reqs, _ := r.snapCounts()
		return reqs > before
	})
	a.mu.Lock()
	a.snap = nil
	a.mu.Unlock()
	close(hold)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "the enrollment changed while it was fetched") {
		t.Fatalf("policy after the snapshot was dropped: %v", err)
	}
}

// TestCredSessionStartFindsDroppedSession pins that a session start which
// would reuse a live window refreshes it first, once its token is
// startRefreshAfter old: a session strazad dropped is re-acquired, one it
// refuses for good is revoked and checked in again, and a live one is
// reused.
func TestCredSessionStartFindsDroppedSession(t *testing.T) {
	const deadline = "Your last sign-in on this machine was more than 30 days ago, and your Straza admin requires a new one. Run straza enroll and sign in with your work account."
	cases := []struct {
		name     string
		server   func(f *fakeStrazad)
		reused   bool
		checkins int
	}{
		{"a live session is reused after a refresh", func(*fakeStrazad) {}, true, 1},
		{"a dropped session is re-acquired", func(f *fakeStrazad) { f.sessions = map[string]fakeSession{} }, false, 2},
		{"a refused session is revoked and checked in again", func(f *fakeStrazad) { f.refreshRefuse = deadline }, false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			first, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			r.waitStream(t, alice, "w1")
			r.srv.with(func() { tc.server(r.srv) })
			r.srv.with(func() { r.srv.refreshes = 0 })
			r.clock.Advance(startRefreshAfter)
			again, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			r.srv.mu.Lock()
			accepted, refreshes := len(r.srv.accepted), r.srv.refreshes
			r.srv.mu.Unlock()
			if again.Reused != tc.reused || (again.ID == first.ID) != tc.reused || accepted != tc.checkins || refreshes != 1 {
				t.Fatalf("second start: reused %v same id %v, %d check-ins, %d refreshes", again.Reused, again.ID == first.ID, accepted, refreshes)
			}
			r.srv.with(func() { r.srv.refreshRefuse = "" })
			if _, err := r.s.Decide(context.Background(), again, policy.Event{Kind: policy.EventToolPre, Tool: "shell.exec"}, "", ""); err != nil {
				t.Fatalf("decide on the started session: %v", err)
			}
			r.srv.assertCounters(t)
		})
	}
}

// TestCredTransportBounce pins the proxy transport: a 401 renews the session
// once and the request is sent again with the new token and its body, a
// renewal the server refuses for good revokes the window, and a 403 is
// never retried.
func TestCredTransportBounce(t *testing.T) {
	const deadline = "Your last sign-in on this machine was more than 30 days ago, and your Straza admin requires a new one. Run straza enroll and sign in with your work account."
	cases := []struct {
		name   string
		server func(f *fakeStrazad)
		status int // 0 means the call fails with the revoked refusal
		echoes int
	}{
		{"a 401 renews and retries", func(f *fakeStrazad) { f.sessions = map[string]fakeSession{} }, http.StatusOK, 2},
		{"a refused renewal revokes", func(f *fakeStrazad) { f.sessions, f.refreshRefuse = map[string]fakeSession{}, deadline }, 0, 1},
		{"a 403 is not retried", func(f *fakeStrazad) { f.echoStatus = http.StatusForbidden }, http.StatusForbidden, 1},
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
			r.srv.with(func() { tc.server(r.srv) })
			req, _ := http.NewRequest(http.MethodPost, r.srv.serverURL()+"/v1/echo", strings.NewReader(`{"jsonrpc":"2.0"}`))
			req.GetBody = nil
			resp, err := r.s.Transport(ses).RoundTrip(req)
			var refused *Refused
			switch {
			case tc.status == 0 && (!errors.As(err, &refused) || refused.Sentence != revokedSentence(deadline)):
				t.Fatalf("got %v, want the revoked refusal", err)
			case tc.status != 0 && (err != nil || resp.StatusCode != tc.status):
				t.Fatalf("got %v, %v, want %d", resp, err, tc.status)
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
			r.srv.mu.Lock()
			echoes, body := r.srv.echoes, r.srv.echoBody
			r.srv.mu.Unlock()
			if echoes != tc.echoes || body != `{"jsonrpc":"2.0"}` {
				t.Fatalf("%d requests, last body %q, want %d with the body", echoes, body, tc.echoes)
			}
		})
	}
}

// TestCredReacquiresLongExpiredWindow pins that a window whose token
// expired over refreshRefusedAfter ago re-acquires on its next call without
// sending that token, which strazad would refuse for sure, so the call
// spends its renewal budget on the check-in alone. A token that expired a
// moment ago is still sent in a refresh first.
func TestCredReacquiresLongExpiredWindow(t *testing.T) {
	cases := []struct {
		name string
		past time.Duration // past the session's issue time; it expires at 300 s
		sent bool
	}{
		{"expired a moment ago", 300*time.Second + 10*time.Second, true},
		{"expired a day ago", 300*time.Second + 24*time.Hour, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			refresh := `"session_token":"` + a.tokenOf(hs) + `"`
			r.clock.Advance(tc.past)
			before := len(r.sent.carrying(refresh))
			got, err := r.s.Session(context.Background(), alice, ses.Harness, false)
			if err != nil || !got.ExpiresAt.After(r.clock.Now()) {
				t.Fatalf("the next call: %+v, %v, want the window live again", got, err)
			}
			if sent := len(r.sent.carrying(refresh)) > before; sent != tc.sent {
				t.Fatalf("a refresh with the expired token was sent %v, want %v", sent, tc.sent)
			}
		})
	}
}

// TestCredLapsedWindowOfflineIsTransport pins that a window whose token
// expired over refreshRefusedAfter ago, and whose re-acquire finds strazad
// unreachable, keeps a transport renewal, as a refresh that got no answer
// does: no refresh was sent, so the server refused nothing, and the
// offline gate words its deny by the grace period, not by a refusal.
func TestCredLapsedWindowOfflineIsTransport(t *testing.T) {
	cases := []struct {
		name string
		past time.Duration // past the session's issue time; it expires at 300 s
	}{
		{"expired 90 seconds ago", 300*time.Second + 90*time.Second},
		{"expired a day ago", 300*time.Second + 24*time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			ses, err := r.start(alice, "w1")
			if err != nil {
				t.Fatal(err)
			}
			r.clock.Advance(tc.past)
			r.srv.srv.Close()
			got := r.renewNow(t, ses)
			if got.Renewal.Class != RenewTransport || !strings.Contains(got.Renewal.Detail, "connection refused") {
				t.Fatalf("renewal %+v, want a transport failure naming the refused connection", got.Renewal)
			}
		})
	}
}

// TestCredLapsedReacquireFailingHereIsNoOutage pins that a lapsed window
// whose re-acquire fails on this machine while strazad is up keeps the
// renewal-failed class, so the offline gate names the failure and never
// claims the server was unreachable.
func TestCredLapsedReacquireFailingHereIsNoOutage(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	ses, err := r.start(alice, "w1")
	if err != nil {
		t.Fatal(err)
	}
	userDir := filepath.Join(r.dir, "u", alice.UID)
	r.clock.Advance(300*time.Second + 90*time.Second)
	if err := os.Chmod(userDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(userDir, 0o700) })
	got := r.renewNow(t, ses)
	_ = os.Chmod(userDir, 0o700)
	if got.Renewal.Class != RenewRejected {
		t.Fatalf("renewal %+v, want the renewal-failed class", got.Renewal)
	}
	if deny := expiredReason(got, 15*time.Minute); strings.Contains(deny, "unreachable") || !strings.Contains(deny, "automatic renewal failed") {
		t.Fatalf("grace-end deny %q claims an outage or hides the failure", deny)
	}
}

// TestCredLapsedBounceKeepsTheRefusal pins that a lapsed window bounced by
// strazad's 401, whose re-acquire then waits after a lost answer, denies
// with the server's refusal and the renewal-failed class, not as an outage.
func TestCredLapsedBounceKeepsTheRefusal(t *testing.T) {
	r := newCredRig(t)
	// The server reads drop with no lock, so it is set before any request
	// and armed only around the re-acquire whose answer is lost.
	var armed atomic.Bool
	r.srv.drop = func(int64) bool { return armed.Load() }
	r.enroll(alice)
	ses, err := r.start(alice, "w1")
	if err != nil {
		t.Fatal(err)
	}
	r.clock.Advance(300*time.Second + 90*time.Second)
	armed.Store(true)
	r.renewNow(t, ses)
	armed.Store(false)
	_, err = r.s.Decide(context.Background(), ses, policy.Event{Kind: policy.EventToolPre, Tool: "shell.exec"}, "", "")
	if deny := onlineDenyReason(err, "an approval-gated action", "Denied (fail-closed)"); !strings.Contains(deny, "refused this session") {
		t.Fatalf("deny %q, want the server's refusal of the session", deny)
	}
	a, hs, err := r.s.held(ses)
	if err != nil {
		t.Fatal(err)
	}
	if class := a.copyOf(hs).Renewal.Class; class != RenewRejected {
		t.Fatalf("class after the bounce %d, want the renewal-failed class", class)
	}
}

// TestUnanswered pins which renewal failures count as getting no answer:
// a transport failure, a timeout, a server error, an answer cut short and
// the wait after a lost answer, as the refresh path classes them. A
// refusal below 500 and a failure on this machine got an answer.
func TestUnanswered(t *testing.T) {
	transport := fmt.Errorf("POST /v1/checkin: %w", &url.Error{Op: "Post", URL: "http://127.0.0.1:1/v1/checkin", Err: errors.New("connect: connection refused")})
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"connection refused", transport, true},
		{"timeout", fmt.Errorf("check-in: %w", context.DeadlineExceeded), true},
		{"canceled", fmt.Errorf("check-in: %w", context.Canceled), true},
		{"a proxy's 502", fmt.Errorf("/v1/checkin: %w", &agentguard.StatusError{Status: http.StatusBadGateway, Msg: "HTTP 502"}), true},
		{"an answer cut short", fmt.Errorf("read the answer to POST /v1/checkin: %w", io.ErrUnexpectedEOF), true},
		{"the wait after a lost answer", &Refused{Status: http.StatusServiceUnavailable, Sentence: waitingSentence}, true},
		{"a refusal", &agentguard.StatusError{Status: http.StatusForbidden, Msg: "revoked"}, false},
		{"a rate limit", &agentguard.StatusError{Status: http.StatusTooManyRequests, Msg: "slow down"}, false},
		{"a failure on this machine", errors.New("Straza could not save this machine's check-in counter"), false},
	} {
		if got := unanswered(tc.err); got != tc.want {
			t.Errorf("%s: unanswered %v, want %v", tc.name, got, tc.want)
		}
	}
}
