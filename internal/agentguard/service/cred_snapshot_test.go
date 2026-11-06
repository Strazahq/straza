package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
)

// renewNow refreshes ses's window at once and returns what the API half
// then sees.
func (r *credRig) renewNow(t *testing.T, ses Session) Session {
	t.Helper()
	a, hs, err := r.s.held(ses)
	if err != nil {
		t.Fatal(err)
	}
	return r.s.renew(context.Background(), a, hs, true)
}

// snapCounts returns how many snapshot requests strazad saw and how many
// it answered with a download.
func (r *credRig) snapCounts() (reqs, downloads int) {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	return r.srv.snapReqs, r.srv.snaps
}

// TestCredSnapshotAdoption pins blob-first adoption: the pin only ever names
// a snapshot the account verified and holds, a failed fetch keeps the held
// one, a refusal starts a deadline that a renewal never extends and a
// landed fetch ends, and a new snapshot reaches every window.
func TestCredSnapshotAdoption(t *testing.T) {
	t.Run("the pin follows the saved blob, not the check-in id", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		r.srv.with(func() { r.srv.reportID = "an-id-the-snapshot-route-does-not-serve-yet" })
		ses, err := r.start(alice, "w1")
		if err != nil || ses.SnapshotID != r.srv.snapID() || ses.PolicyRefused != "" {
			t.Fatalf("start: pin %s refused %q, %v, want %s", ses.SnapshotID, ses.PolicyRefused, err, r.srv.snapID())
		}
	})
	t.Run("a kept blob that does not verify is fetched again", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		if _, err := r.start(alice, "w1"); err != nil {
			t.Fatal(err)
		}
		r.stop()
		blob := filepath.Join(r.dir, "u", alice.UID, snapshotFile)
		if err := os.WriteFile(blob, []byte("torn"), 0o600); err != nil {
			t.Fatal(err)
		}
		r.open()
		ses, err := r.start(alice, "w2")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := r.s.Policy(context.Background(), ses); err != nil {
			t.Fatalf("policy after the torn blob: %v", err)
		}
		if _, downloads := r.snapCounts(); downloads != 2 {
			t.Fatalf("%d downloads, want the torn blob fetched again", downloads)
		}
	})
	t.Run("a managed config that does not parse verifies no snapshot", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		if err := os.WriteFile(agentguard.ManagedConfigPath(), []byte("serverUrl: [unclosed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r.restart()
		ses, err := r.start(alice, "w1")
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, err = r.s.Policy(context.Background(), ses)
		if err == nil || !strings.Contains(err.Error(), agentguard.ManagedConfigPath()) || !strings.Contains(err.Error(), "does not parse") {
			t.Fatalf("policy with a broken managed config: %v, want the deny naming %s", err, agentguard.ManagedConfigPath())
		}
	})
	t.Run("a managed config that breaks after the policy was verified stops it at the held time", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		w1, err := r.start(alice, "w1")
		if err != nil {
			t.Fatal(err)
		}
		w2, err := r.start(alice, "w2")
		if err != nil {
			t.Fatal(err)
		}
		r.srv.activate(t, 901)
		if err := os.WriteFile(agentguard.ManagedConfigPath(), []byte("serverUrl: [unclosed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := r.renewNow(t, w1)
		other, err := r.s.Session(context.Background(), alice, w2.Harness, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			ses  Session
			held time.Time
		}{{got, w1.ExpiresAt}, {other, w2.ExpiresAt}} {
			if !strings.Contains(c.ses.PolicyRefused, agentguard.ManagedConfigPath()) || !c.ses.PolicyDeadline.Equal(c.held) || c.ses.SnapshotID != w1.SnapshotID {
				t.Fatalf("%s: refusal %q deadline %v pin %s, want a refusal naming %s at %v on the held pin",
					c.ses.Harness.Session, c.ses.PolicyRefused, c.ses.PolicyDeadline, c.ses.SnapshotID, agentguard.ManagedConfigPath(), c.held)
			}
		}
	})
	t.Run("a failed fetch keeps the held snapshot and starts no refusal", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		ses, err := r.start(alice, "w1")
		if err != nil {
			t.Fatal(err)
		}
		held := ses.SnapshotID
		r.srv.activate(t, 901)
		r.srv.with(func() { r.srv.snapFail = http.StatusServiceUnavailable })
		got := r.renewNow(t, ses)
		_, maxAge, _, err := r.s.Policy(context.Background(), got)
		if got.SnapshotID != held || got.PolicyRefused != "" || err != nil || maxAge != 900 {
			t.Fatalf("after a failed fetch: pin %s refused %q maxAge %d %v", got.SnapshotID, got.PolicyRefused, maxAge, err)
		}
	})
	t.Run("a refusal keeps its deadline until a fetch lands", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		ses, err := r.start(alice, "w1")
		if err != nil {
			t.Fatal(err)
		}
		r.srv.activate(t, 901)
		r.srv.with(func() { r.srv.snapFail, r.srv.snapMsg = http.StatusForbidden, "the snapshot route is closed" })
		first := r.renewNow(t, ses)
		if first.PolicyRefused != "The server refused it: the snapshot route is closed." || !first.PolicyDeadline.Equal(ses.ExpiresAt) {
			t.Fatalf("refusal %q deadline %v, want the held time %v", first.PolicyRefused, first.PolicyDeadline, ses.ExpiresAt)
		}
		r.clock.Advance(time.Minute)
		if again := r.renewNow(t, first); !again.PolicyDeadline.Equal(ses.ExpiresAt) {
			t.Fatalf("a renewal moved the deadline to %v", again.PolicyDeadline)
		}
		r.srv.with(func() { r.srv.snapFail = 0 })
		landed := r.renewNow(t, first)
		_, maxAge, _, err := r.s.Policy(context.Background(), landed)
		if landed.PolicyRefused != "" || !landed.PolicyDeadline.IsZero() || landed.SnapshotID != r.srv.snapID() || err != nil || maxAge != 901 {
			t.Fatalf("after the fetch landed: %+v maxAge %d %v", landed, maxAge, err)
		}
	})
	t.Run("a policy change reaching many windows is downloaded once", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		sessions := startMany(t, r, alice, "w", 5)
		r.srv.activate(t, 901)
		var wg sync.WaitGroup
		for _, ses := range sessions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.renewNow(t, ses)
			}()
		}
		wg.Wait()
		if _, downloads := r.snapCounts(); downloads != 2 {
			t.Fatalf("%d downloads, want the first snapshot and the new one once each", downloads)
		}
		for _, ses := range sessions {
			if got, _ := r.s.Session(context.Background(), alice, ses.Harness, false); got.SnapshotID != r.srv.snapID() {
				t.Fatalf("%s pins %s, want %s", ses.Harness.Session, got.SnapshotID, r.srv.snapID())
			}
		}
	})
	t.Run("a snapshot compiled before the held one is never adopted", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		r.srv.activateAt(t, 901, 1700000100)
		ses, err := r.start(alice, "w1")
		if err != nil {
			t.Fatal(err)
		}
		held := ses.SnapshotID
		r.srv.activateAt(t, 902, 1700000000)
		got := r.renewNow(t, ses)
		_, maxAge, from, err := r.s.Policy(context.Background(), got)
		if got.SnapshotID != held || from != held || maxAge != 901 || err != nil {
			t.Fatalf("after an older snapshot: pin %s from %s maxAge %d %v, want the held %s", got.SnapshotID, from, maxAge, err, held)
		}
	})
	t.Run("a new snapshot reaches every window", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		w1, err := r.start(alice, "w1")
		if err != nil {
			t.Fatal(err)
		}
		w2, err := r.start(alice, "w2")
		if err != nil {
			t.Fatal(err)
		}
		r.srv.activate(t, 901)
		r.renewNow(t, w1)
		got, err := r.s.Session(context.Background(), alice, w2.Harness, false)
		if err != nil || got.SnapshotID != r.srv.snapID() {
			t.Fatalf("w2 pins %s, %v, want the new %s", got.SnapshotID, err, r.srv.snapID())
		}
		if _, maxAge, from, err := r.s.Policy(context.Background(), got); err != nil || maxAge != 901 || from != r.srv.snapID() {
			t.Fatalf("w2's policy: %d from %s, %v", maxAge, from, err)
		}
		if _, downloads := r.snapCounts(); downloads != 2 {
			t.Fatalf("%d downloads, want 2", downloads)
		}
	})
}

// TestCredMissingSnapshot pins the fetch of a live session's missing
// snapshot: it heals with the session's own token, a failure denies with
// what came back and what to do, the same answer holds for the snapshot lag
// without asking the server, and a session past its time fetches nothing.
func TestCredMissingSnapshot(t *testing.T) {
	ctx := context.Background()
	t.Run("a live session fetches its missing snapshot", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		r.srv.with(func() { r.srv.snapFail = http.StatusServiceUnavailable })
		ses, err := r.start(alice, "w1")
		if err != nil || ses.SnapshotID != r.srv.snapID() {
			t.Fatalf("start with the snapshot route down: pin %s, %v", ses.SnapshotID, err)
		}
		r.srv.with(func() { r.srv.snapFail = 0 })
		if _, _, _, err := r.s.Policy(ctx, ses); err != nil {
			t.Fatalf("policy: %v", err)
		}
	})
	t.Run("a renewal that names the pinned id fetches the missing snapshot", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		r.srv.with(func() { r.srv.snapFail = http.StatusServiceUnavailable })
		ses, err := r.start(alice, "w1")
		if err != nil {
			t.Fatal(err)
		}
		r.srv.with(func() { r.srv.snapFail = 0 })
		r.renewNow(t, ses)
		reqs, _ := r.snapCounts()
		if _, _, _, err := r.s.Policy(ctx, ses); err != nil {
			t.Fatalf("policy after the renewal: %v", err)
		}
		if after, downloads := r.snapCounts(); after != reqs || downloads != 1 {
			t.Fatalf("the renewal did not fetch the snapshot: %d requests then %d, %d downloads", reqs, after, downloads)
		}
	})
	t.Run("a failed fetch denies and is answered again for the lag", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		r.srv.with(func() { r.srv.snapFail, r.srv.snapMsg = http.StatusServiceUnavailable, "the store is down" })
		ses, err := r.start(alice, "w1")
		if err != nil {
			t.Fatal(err)
		}
		reqs, _ := r.snapCounts()
		steps := []struct {
			advance time.Duration
			want    []string
			fetches int
		}{
			{0, []string{"the policy snapshot is missing on this machine", "just now", "The server answered HTTP 503: the store is down.", "Wait 30s"}, 1},
			{10 * time.Second, []string{"10s ago", "Wait 20s"}, 0},
			{21 * time.Second, []string{"just now", "Wait 30s"}, 1},
		}
		for _, st := range steps {
			r.clock.Advance(st.advance)
			_, _, _, err := r.s.Policy(ctx, ses)
			after, _ := r.snapCounts()
			for _, w := range st.want {
				if err == nil || !strings.Contains(err.Error(), w) {
					t.Fatalf("after %v: %v, want it to say %q", st.advance, err, w)
				}
			}
			if after-reqs != st.fetches {
				t.Fatalf("after %v: %d fetches, want %d", st.advance, after-reqs, st.fetches)
			}
			reqs = after
		}
	})
	t.Run("a session past its time fetches nothing", func(t *testing.T) {
		r := newCredRig(t)
		r.enroll(alice)
		r.srv.with(func() { r.srv.snapFail = http.StatusServiceUnavailable })
		ses, err := r.start(alice, "w1")
		if err != nil {
			t.Fatal(err)
		}
		r.srv.with(func() { r.srv.snapFail = 0 })
		r.clock.Advance(301 * time.Second)
		reqs, _ := r.snapCounts()
		if _, _, _, err := r.s.Policy(ctx, ses); err == nil {
			t.Fatal("an expired session got a policy without a snapshot")
		}
		if after, _ := r.snapCounts(); after != reqs {
			t.Fatal("an expired session fetched the snapshot with its old token")
		}
	})
}

// TestCredFetchAdvice pins the advice after a failed snapshot fetch, the
// refusal that names its own remedy included.
func TestCredFetchAdvice(t *testing.T) {
	status := func(code int, msg string) error {
		return &agentguard.StatusError{Status: code, Msg: msg}
	}
	cases := []struct {
		name   string
		err    error
		advice fetchAdvice
		answer string
	}{
		{"refusal naming a new session and doctor", status(401, "Start a new session of the AI agent, then run `straza doctor`."), adviceExec, "The server refused it: Start a new session"},
		{"other refusal", status(403, "policy fetch refused"), adviceNewSession, "The server refused it: policy fetch refused."},
		{"proxy timeout", status(408, "slow"), adviceRetry, "The server refused it: slow."},
		{"rate limit", status(429, "slow down"), adviceRetry, "The server refused it: slow down."},
		{"server failure", status(503, "down"), adviceRetry, "The server answered HTTP 503: down."},
		{"bare server failure", status(500, "HTTP 500"), adviceRetry, "The server answered HTTP 500 and gave no reason."},
		{"unverified snapshot", errSnapshotUnverified, adviceOwn, "does not verify against the snapshot keys"},
		{"snapshot not kept", errSnapshotNotKept, adviceFixLocal, "The server answered HTTP 200 with the policy, but"},
		{"no answer", errors.New("dial tcp: connection refused"), adviceRetry, "No answer came back: dial tcp: connection refused."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fetchOutcome(tc.err)
			if f.Advice != tc.advice || !strings.Contains(f.Answer, tc.answer) {
				t.Fatalf("got %q with %q, want %q containing %q", f.Advice, f.Answer, tc.advice, tc.answer)
			}
		})
	}
}

// TestCredRenewalClasses pins how a renewal's outcome is classed for the
// offline gate, with the server's sentence as the detail, unchanged.
func TestCredRenewalClasses(t *testing.T) {
	status := func(code int, msg string) error {
		return &agentguard.StatusError{Status: code, Msg: msg}
	}
	deadline := "Your last sign-in on this machine was more than 30 days ago, and your Straza admin requires a new one. Run straza enroll and sign in with your work account."
	cases := []struct {
		name       string
		reacquired bool
		err        error
		class      RenewalClass
		detail     string
	}{
		{"landed", false, nil, RenewNone, ""},
		{"no answer", false, errors.New("dial tcp: connection refused"), RenewTransport, "dial tcp: connection refused"},
		{"server failure", false, status(503, "down"), RenewTransport, "down"},
		{"refresh refused", false, status(403, deadline), RenewTerminal, deadline},
		{"bare refusal", false, status(403, "HTTP 403"), RenewTerminal, "the server answered HTTP 403 and gave no reason."},
		{"rate limit", false, status(429, "slow down"), RenewUnjudged, "the server answered HTTP 429: slow down."},
		{"bare bad request", false, status(400, "HTTP 400"), RenewUnjudged, "the server answered HTTP 400 and gave no reason."},
		{"re-acquire refused", true, status(403, deadline), RenewTerminal, deadline},
		{"re-acquire got no answer", true, errors.New("dial tcp: connection refused"), RenewRejected, "dial tcp: connection refused"},
		{"re-acquire waits after a lost answer", true, &Refused{Status: http.StatusServiceUnavailable, Sentence: waitingSentence}, RenewRejected, waitingSentence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, detail := renewalClass(tc.reacquired, tc.err)
			if class != tc.class || !strings.HasPrefix(detail, tc.detail) {
				t.Fatalf("got %d %q, want %d %q", class, detail, tc.class, tc.detail)
			}
		})
	}
}

// TestCredRefreshAdoptsIdentity pins that a start stores the identity the
// server resolved, its type and its packs, that a refresh adopts roles the
// server resolved again, and that a reply with no roles at all changes
// nothing, because clearing roles would widen access.
func TestCredRefreshAdoptsIdentity(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	r.srv.with(func() { r.srv.userType = "person" })
	ses, err := r.start(alice, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ses.Roles, []string{"dev"}) || ses.UserType != "person" || len(ses.Packs) != 1 || ses.Packs[0].Name != "golang-style" {
		t.Fatalf("start: roles %v type %q packs %+v", ses.Roles, ses.UserType, ses.Packs)
	}
	if again, err := r.start(alice, "w1"); err != nil || !again.Reused || len(again.Packs) != 1 {
		t.Fatalf("a resumed window lost its packs: %+v, %v", again, err)
	}
	r.srv.with(func() { r.srv.roles, r.srv.userType = []string{"dev", "ops"}, "agent" })
	got := r.renewNow(t, ses)
	if !slices.Equal(got.Roles, []string{"dev", "ops"}) || got.UserType != "agent" {
		t.Fatalf("refresh: roles %v type %q", got.Roles, got.UserType)
	}
	r.srv.with(func() { r.srv.noRoles = true })
	if kept := r.renewNow(t, ses); !slices.Equal(kept.Roles, []string{"dev", "ops"}) || kept.UserType != "agent" {
		t.Fatalf("an identity-less refresh changed the identity: roles %v type %q", kept.Roles, kept.UserType)
	}
}

// TestCredRefreshAdoptsStartText pins that a refresh adopts the start text
// the server wrote at that check-in, so a window that starts later reads
// the text of the policy as it is now, and that a refresh from a server
// that sends none keeps the text the session holds.
func TestCredRefreshAdoptsStartText(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	ses, err := r.start(alice, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if ses.StartText == nil || *ses.StartText != fakeStartText {
		t.Fatalf("start: start text %v, want %q", ses.StartText, fakeStartText)
	}
	const newer = "Straza governs this test session, and whoami is now off."
	r.srv.with(func() { r.srv.startText = newer })
	r.renewNow(t, ses)
	if again, err := r.start(alice, "w1"); err != nil || !again.Reused || again.StartText == nil || *again.StartText != newer {
		t.Fatalf("a window after the refresh: %+v, %v, want the newer start text", again, err)
	}
	r.srv.with(func() { r.srv.noStartText = true })
	if kept := r.renewNow(t, ses); kept.StartText == nil || *kept.StartText != newer {
		t.Fatalf("a refresh with no start text changed it to %v", kept.StartText)
	}
}

// TestCredAttestation pins what the service measures: its own binary
// always, and with the managed layout the managed config and the managed
// hook wiring of the harness named, which for codex is requirements.toml.
// A changed managed file changes its measurement.
func TestCredAttestation(t *testing.T) {
	cases := []struct {
		name    string
		managed bool
		harness string
		keys    []string
	}{
		{"user mode", false, "claude-code", []string{"self"}},
		{"managed claude-code", true, "claude-code", []string{"config", "hooks.claude-code", "self"}},
		{"managed codex", true, "codex", []string{"config", "hooks.codex", "self"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sys := t.TempDir()
			t.Setenv("STRAZA_SYSTEM", filepath.Join(sys, "absent"))
			t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sys, "harness"))
			var wiring string
			if tc.managed {
				t.Setenv("STRAZA_SYSTEM", sys)
				if err := os.WriteFile(agentguard.ManagedConfigPath(), []byte("serverUrl: https://managed.test\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				var err error
				if wiring, err = agentguard.ManagedSettingsPath(tc.harness); tc.harness == "codex" {
					wiring, err = agentguard.CodexManagedRequirementsPath()
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(wiring), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(wiring, []byte("hooks = true\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			att := measureAttestation(tc.harness)
			var keys []string
			for k := range att.Hashes {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if att.Managed != tc.managed || !slices.Equal(keys, tc.keys) || !strings.HasPrefix(att.Hashes["self"], "sha256:") {
				t.Fatalf("measured managed %v %v, want %v %v", att.Managed, att.Hashes, tc.managed, tc.keys)
			}
			if !tc.managed {
				return
			}
			want, _ := agentguard.FileSHA256(wiring)
			if att.Hashes["hooks."+tc.harness] != want {
				t.Fatalf("hooks.%s = %s, want the hash of %s", tc.harness, att.Hashes["hooks."+tc.harness], wiring)
			}
			before := att.Hashes["config"]
			if err := os.WriteFile(agentguard.ManagedConfigPath(), []byte("serverUrl: https://elsewhere.test\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if after := measureAttestation(tc.harness).Hashes["config"]; after == before {
				t.Fatal("a tampered managed config kept its measurement")
			}
		})
	}
}

// TestCredSpoolZeroLossAcrossOutage pins the service's use of the spool:
// records kept while strazad refuses them, all of them uploaded once it
// takes them again, and the drain's error and time in status.
func TestCredSpoolZeroLossAcrossOutage(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	if _, err := r.start(alice, "w1"); err != nil {
		t.Fatal(err)
	}
	r.srv.with(func() { r.srv.auditFail = http.StatusInternalServerError })
	for range 5 {
		if err := r.s.Record(alice, "straza.audit.tool", map[string]any{"session": "s"}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the failed drain", func() bool { return r.s.Status(alice).Spool.LastError != "" })
	if st := r.s.Status(alice).Spool; st.Pending != 5 || !st.LastDrain.IsZero() {
		t.Fatalf("during the outage: %+v, want 5 pending and no drain", st)
	}
	r.srv.with(func() { r.srv.auditFail = 0 })
	if err := r.s.Record(alice, "straza.audit.tool", map[string]any{"session": "s"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "all six records", func() bool {
		r.srv.mu.Lock()
		defer r.srv.mu.Unlock()
		return r.srv.batches == 6
	})
	waitFor(t, "a clean drain in status", func() bool {
		st := r.s.Status(alice).Spool
		return st.Pending == 0 && st.LastError == "" && !st.LastDrain.IsZero()
	})
}

// TestCredSnapshotPulse pins the background check of the active snapshot:
// with no push and no renewal, a newer snapshot is adopted once the lag has
// passed since the last check, the decide that starts the check never
// waits for it, and concurrent decides start one check.
func TestCredSnapshotPulse(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	ses, err := r.start(alice, "w1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	policyOf := func() (int64, string) {
		_, maxAge, from, err := r.s.Policy(ctx, ses)
		if err != nil {
			t.Fatalf("policy: %v", err)
		}
		return maxAge, from
	}
	first := r.srv.snapID()
	r.srv.activate(t, 901)
	reqs, _ := r.snapCounts()
	if maxAge, from := policyOf(); maxAge != 900 || from != first {
		t.Fatalf("inside the lag: %d from %s, want the held snapshot", maxAge, from)
	}
	if after, _ := r.snapCounts(); after != reqs {
		t.Fatal("a decide inside the lag checked the snapshot")
	}
	r.clock.Advance(31 * time.Second)
	policyOf()
	waitFor(t, "the newer snapshot", func() bool {
		maxAge, from := policyOf()
		return maxAge == 901 && from == r.srv.snapID()
	})
	if got, _ := r.s.Session(ctx, alice, ses.Harness, false); got.ID != ses.ID || got.SnapshotID != r.srv.snapID() {
		t.Fatalf("the window was renewed or not re-pinned: %+v", got)
	}

	hold := make(chan struct{})
	r.srv.with(func() { r.srv.snapHold = hold })
	r.clock.Advance(31 * time.Second)
	reqs, _ = r.snapCounts()
	done := make(chan struct{})
	for range 2 {
		go func() {
			policyOf()
			done <- struct{}{}
		}()
	}
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a decide waited for the background check")
		}
	}
	waitFor(t, "the background check to reach strazad", func() bool {
		after, _ := r.snapCounts()
		return after > reqs
	})
	policyOf()
	after, _ := r.snapCounts()
	close(hold)
	if after != reqs+1 {
		t.Fatalf("%d background checks for three decides inside one check, want 1", after-reqs)
	}
}
