package agentguard

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// liveNoSnapshotPhrase opens the deny of a live session whose policy
// snapshot is missing and could not be fetched.
const liveNoSnapshotPhrase = "Straza: this session is live, but the policy snapshot is missing on this machine"

// adoptLane is one way a client takes a live session from disk and decides
// on it. decide runs one governed shell command through the lane and returns
// whether it was denied and the reason a person reads.
type adoptLane struct {
	name   string
	decide func(t *testing.T, store *Store, serverURL, command string) (denied bool, reason string)
}

// hookDecide is one tool.pre decision of the hook lane.
func hookDecide(_ *testing.T, store *Store, _, command string) (bool, string) {
	d := liveDecider{store: store}.Decide(Normalized{HarnessName: "claude-code", HarnessVersion: "2.1.0", Event: shellEvent(command)})
	return d.Effect == policy.EffectDeny, d.Reason
}

var adoptLanes = []adoptLane{
	{name: "hook", decide: hookDecide},
	{name: "exec wrapper", decide: func(_ *testing.T, _ *Store, _, command string) (bool, string) {
		var errOut bytes.Buffer
		code := Exec(context.Background(), strings.Fields(command), &errOut)
		return code == execDenyExit, strings.TrimSpace(errOut.String())
	}},
	{name: "MCP proxy, then hook", decide: func(t *testing.T, store *Store, serverURL, command string) (bool, string) {
		t.Helper()
		// The proxy adopts the session and serves the gateway without a blob,
		// and the hook of the next tool call decides on the same disk state.
		ses, err := ensureSession(context.Background(), store, NewClient(serverURL), "claude-code")
		if err != nil {
			t.Fatalf("the proxy failed to adopt a live session: %v", err)
		}
		if on, _ := store.LoadSession(); ses.SessionID != on.SessionID {
			t.Fatalf("the proxy minted session %q, want the one on disk, %q", ses.SessionID, on.SessionID)
		}
		return hookDecide(t, store, serverURL, command)
	}},
}

// countedServer serves g and counts every request for the policy snapshot,
// whatever it answers.
func countedServer(t *testing.T, g *tokenGatedServer) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	serve := g.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/snapshot" {
			n.Add(1)
		}
		serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// markPulse writes the snapshot-checked marker the way the drain pulse does
// after it asked the server.
func markPulse(t *testing.T, store *Store) {
	t.Helper()
	if err := os.WriteFile(store.statePath("snapshot-checked"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLiveSessionFetchesMissingSnapshot pins the heal: a client that finds a
// live session on disk but no verifiable snapshot fetches one at once with
// that session's token, so the first decision is the policy's and not a
// fail-closed deny. A torn blob and a blob the pinned keys do not verify
// count as missing. A drain pulse that asked the server a moment ago never
// holds the first fetch back.
func TestLiveSessionFetchesMissingSnapshot(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	_, idA := testSignedPolicy(t, priv, pinPolicyA)
	blobB, idB := testSignedPolicy(t, priv, pinPolicyB)
	otherPriv, _ := testSnapshotKey(t)
	foreign, _ := testSignedPolicy(t, otherPriv, pinPolicyB)
	disks := []struct {
		name string
		blob []byte
	}{
		{"no blob", nil},
		{"torn blob", blobB[:len(blobB)/2]},
		{"blob signed by another key", foreign},
	}
	for _, lane := range adoptLanes {
		for _, disk := range disks {
			t.Run(lane.name+", "+disk.name, func(t *testing.T) {
				g := &tokenGatedServer{blob: blobB, id: idB, live: "tok-seed"}
				srv, fetches := countedServer(t, g)
				store := pinStore(t, srv.URL, keys)
				if disk.blob != nil {
					if err := store.SaveSnapshot(disk.blob); err != nil {
						t.Fatal(err)
					}
				}
				expires := time.Now().Add(time.Hour).Truncate(time.Second)
				seedSession(t, store, idA, expires)
				markPulse(t, store)

				denied, reason := lane.decide(t, store, srv.URL, "b-cmd /x")
				if !denied || strings.TrimPrefix(reason, "Straza: ") != "b" {
					t.Fatalf("b-cmd = denied %v with %q, want the deny of the fetched policy, b", denied, reason)
				}
				if n := fetches.Load(); n != 1 {
					t.Errorf("%d snapshot fetches, want 1", n)
				}
				cfg, _ := store.LoadConfig()
				if diskID, ok := diskSnapshotID(store, cfg); !ok || diskID != idB {
					t.Errorf("disk blob = %q (verified %v), want %q", diskID, ok, idB)
				}
				ses, err := store.LoadSession()
				if err != nil {
					t.Fatal(err)
				}
				if ses.SessionID != "s-seed" || ses.SessionToken != "tok-seed" || ses.SnapshotID != idB || !ses.ExpiresAt.Equal(expires) {
					t.Errorf("session %q token %q pins %q until %v; want s-seed, tok-seed, %q, %v", ses.SessionID, ses.SessionToken, ses.SnapshotID, ses.ExpiresAt, idB, expires)
				}
				if denied, reason := lane.decide(t, store, srv.URL, "b-cmd /y"); !denied || strings.TrimPrefix(reason, "Straza: ") != "b" {
					t.Errorf("second b-cmd = denied %v with %q, want b", denied, reason)
				}
				if n := fetches.Load(); n != 1 {
					t.Errorf("%d snapshot fetches after the second decision, want still 1", n)
				}
			})
		}
	}
}

// TestLiveSessionWithSnapshotFetchesNothing is the positive control: a live
// session with a verifiable blob on disk decides on it and asks the server
// for nothing, on every lane.
func TestLiveSessionWithSnapshotFetchesNothing(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testSignedPolicy(t, priv, pinPolicyA)
	blobB, idB := testSignedPolicy(t, priv, pinPolicyB)
	for _, lane := range adoptLanes {
		t.Run(lane.name, func(t *testing.T) {
			g := &tokenGatedServer{blob: blobB, id: idB, live: "tok-seed"}
			srv, fetches := countedServer(t, g)
			store := pinStore(t, srv.URL, keys)
			if err := store.SaveSnapshot(blobA); err != nil {
				t.Fatal(err)
			}
			seedSession(t, store, idA, time.Now().Add(time.Hour))
			if denied, reason := lane.decide(t, store, srv.URL, "a-cmd /x"); !denied || strings.TrimPrefix(reason, "Straza: ") != "a" {
				t.Errorf("a-cmd = denied %v with %q, want the cached policy's deny, a", denied, reason)
			}
			if n := fetches.Load(); n != 0 {
				t.Errorf("%d snapshot fetches, want none", n)
			}
		})
	}
}

// Advice phrases that follow the answer in the missing-snapshot deny, one
// per kind of failed fetch.
const (
	retryAdvice      = "Wait 30s and run the tool call again, which fetches it again."
	newSessionAdvice = "Retrying will not change this answer while this session lasts. Start a new session of the AI agent so straza checks in again. " +
		"For straza exec, restarting the agent starts no new session, because straza exec checks in again by itself only when this session is about to end."
	ownAdvice   = "Retrying will not change this answer."
	localAdvice = "Fix that error on this machine, then wait"
)

// TestMissingSnapshotFetchFailureDenies pins the fail-closed side of the
// heal. A fetch that fails denies with a sentence that says the session is
// live, the snapshot is missing, what came back and what to do next, and it
// offers a retry only where a retry can land. It changes nothing on the
// session. A second decision inside the snapshot lag asks the server
// nothing, a newly adopted session asks at once, and the same session asks
// again once the lag has passed.
func TestMissingSnapshotFetchFailureDenies(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	_, idA := testSignedPolicy(t, priv, pinPolicyA)
	blobB, idB := testSignedPolicy(t, priv, pinPolicyB)
	otherPriv, _ := testSnapshotKey(t)
	foreign, foreignID := testSignedPolicy(t, otherPriv, pinPolicyB)
	answers := []struct {
		name   string
		server func() *tokenGatedServer
		// setup breaks the machine's side of the fetch when set.
		setup   func(t *testing.T, store *Store)
		want    string   // the answer
		advice  string   // what the person is told to do
		notWant []string // advice that is untrue for this answer
	}{
		{name: "the server refuses the token",
			server: func() *tokenGatedServer { return &tokenGatedServer{blob: blobB, id: idB, refuseAll: true} },
			want:   "The server refused it: " + gatedRefusal, advice: newSessionAdvice, notWant: []string{"fetches it again"}},
		{name: "the server sends a policy the pinned keys do not verify",
			server: func() *tokenGatedServer { return &tokenGatedServer{blob: foreign, id: foreignID, live: "tok-seed"} },
			want:   "does not verify against the snapshot keys this machine pinned", advice: ownAdvice,
			notWant: []string{"fetches it again", "Start a new session of the AI agent"}},
		{name: "the server redirects to another origin",
			server: func() *tokenGatedServer {
				return &tokenGatedServer{blob: blobB, id: idB, redirectTo: "http://127.0.0.1:1/v1/snapshot"}
			},
			want: "The fetch was redirected: ", advice: ownAdvice, notWant: []string{"fetches it again", "Start a new session of the AI agent"}},
		{name: "the server fails",
			server: func() *tokenGatedServer {
				return &tokenGatedServer{blob: blobB, id: idB, failStatus: http.StatusServiceUnavailable}
			},
			want: "The server answered HTTP 503: snapshot store unavailable.", advice: retryAdvice, notWant: []string{"Retrying will not"}},
		{name: "a proxy in front of strazad rate-limits",
			server: func() *tokenGatedServer {
				return &tokenGatedServer{blob: blobB, id: idB, failStatus: http.StatusTooManyRequests}
			},
			want: "The server refused it with HTTP 429 and gave no reason.", advice: retryAdvice, notWant: []string{"Retrying will not"}},
		{name: "a proxy in front of strazad times the request out",
			server: func() *tokenGatedServer {
				return &tokenGatedServer{blob: blobB, id: idB, failStatus: http.StatusRequestTimeout}
			},
			want: "The server refused it with HTTP 408 and gave no reason.", advice: retryAdvice, notWant: []string{"Retrying will not"}},
		{name: "no answer comes back",
			server: func() *tokenGatedServer { return &tokenGatedServer{blob: blobB, id: idB, dropSnapshot: true} },
			want:   "No answer came back: ", advice: retryAdvice, notWant: []string{"Retrying will not"}},
		{name: "the server answers but the snapshot cannot be stored",
			server: func() *tokenGatedServer { return &tokenGatedServer{blob: blobB, id: idB, live: "tok-seed"} },
			setup: func(t *testing.T, store *Store) {
				t.Helper()
				// A folder where the blob belongs makes the save after the 200 fail.
				if err := os.MkdirAll(filepath.Join(store.statePath("snapshot.cbor"), "in-the-way"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want:    "The server answered HTTP 200 with the policy, but straza could not check or store the snapshot on this machine: ",
			advice:  localAdvice,
			notWant: []string{"No answer came back", "is the address strazad serves on", "Retrying will not"}},
	}
	for _, lane := range adoptLanes {
		for _, answer := range answers {
			t.Run(lane.name+", "+answer.name, func(t *testing.T) {
				g := answer.server()
				srv, fetches := countedServer(t, g)
				store := pinStore(t, srv.URL, keys)
				if answer.setup != nil {
					answer.setup(t, store)
				}
				expires := time.Now().Add(time.Hour).Truncate(time.Second)
				seedSession(t, store, idA, expires)
				before, _ := store.LoadSession()

				check := func(step string, wantFetches int64) {
					t.Helper()
					denied, reason := lane.decide(t, store, srv.URL, "b-cmd /x")
					if !denied || !strings.HasPrefix(reason, liveNoSnapshotPhrase) || !strings.Contains(reason, answer.want) ||
						!strings.Contains(reason, srv.URL) {
						t.Errorf("%s: b-cmd = denied %v with %q; want the missing-snapshot deny naming %s and %q", step, denied, reason, srv.URL, answer.want)
					}
					// Inside the lag the wait counts down, so only the first
					// decision of a fetch is held to the full 30s.
					if advice := answer.advice; !strings.Contains(reason, advice) &&
						(advice != retryAdvice || !strings.Contains(reason, "and run the tool call again, which fetches it again.")) {
						t.Errorf("%s: reason %q lacks the advice %q", step, reason, advice)
					}
					for _, untrue := range answer.notWant {
						if strings.Contains(reason, untrue) {
							t.Errorf("%s: reason %q says %q, which is untrue after this answer", step, reason, untrue)
						}
					}
					if n := fetches.Load(); n != wantFetches {
						t.Errorf("%s: %d snapshot fetches, want %d", step, n, wantFetches)
					}
				}
				check("first decision", 1)
				ses, _ := store.LoadSession()
				if ses.SnapshotID != before.SnapshotID || !ses.ExpiresAt.Equal(expires) || ses.PolicyRefused != "" || !ses.PolicyDeadline.IsZero() {
					t.Errorf("session after the failed fetch pins %q until %v with refusal %q until %v; want it unchanged", ses.SnapshotID, ses.ExpiresAt, ses.PolicyRefused, ses.PolicyDeadline)
				}
				check("inside the snapshot lag", 1)

				next := before
				next.SessionID, next.SessionToken = "s-next", "tok-next"
				if err := store.SaveSession(next); err != nil {
					t.Fatal(err)
				}
				g.mu.Lock()
				if g.live != "" {
					g.live = next.SessionToken // the server answers the new session as it answered the old one
				}
				g.mu.Unlock()
				check("a newly adopted session", 2)

				past := time.Now().Add(-time.Hour)
				if err := os.Chtimes(store.statePath("snapshot-checked"), past, past); err != nil {
					t.Fatal(err)
				}
				check("after the snapshot lag", 3)
			})
		}
	}
}

// TestFetchedSnapshotEndsRefusal pins that a fetch that lands ends an
// outstanding policy refusal, as the drain pulse's fetch does. The refusal's
// deadline has passed, so without that the first decision would be the
// refused-policy deny instead of the fetched policy's own.
func TestFetchedSnapshotEndsRefusal(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	_, idA := testSignedPolicy(t, priv, pinPolicyA)
	blobB, idB := testSignedPolicy(t, priv, pinPolicyB)
	for _, lane := range adoptLanes {
		t.Run(lane.name, func(t *testing.T) {
			g := &tokenGatedServer{blob: blobB, id: idB, live: "tok-seed"}
			srv, fetches := countedServer(t, g)
			store := pinStore(t, srv.URL, keys)
			seedSession(t, store, idA, time.Now().Add(time.Hour))
			ses, _ := store.LoadSession()
			ses.PolicyRefused, ses.PolicyDeadline = "The server refused it: "+gatedRefusal, time.Now().Add(-2*time.Hour)
			if err := store.SaveSession(ses); err != nil {
				t.Fatal(err)
			}
			if denied, reason := lane.decide(t, store, srv.URL, "b-cmd /x"); !denied || strings.TrimPrefix(reason, "Straza: ") != "b" {
				t.Errorf("b-cmd = denied %v with %q, want the deny of the fetched policy, b", denied, reason)
			}
			if ses, _ := store.LoadSession(); ses.PolicyRefused != "" || !ses.PolicyDeadline.IsZero() || ses.SnapshotID != idB {
				t.Errorf("session pins %q with refusal %q until %v; want %q and no refusal", ses.SnapshotID, ses.PolicyRefused, ses.PolicyDeadline, idB)
			}
			if n := fetches.Load(); n != 1 {
				t.Errorf("%d snapshot fetches, want 1", n)
			}
		})
	}
}

// TestExpiredSessionFetchesNoSnapshot pins that the heal is for a live
// session only: a hook whose session has expired and could not renew, here
// because strazad answered the renewal with a 503, asks the server for no
// snapshot with a token that is past its time, and it still fails closed.
// The renewals that land or are refused are pinned by
// TestExpiredSessionRenewsBeforeFetch (snapshot_u3_test.go).
func TestExpiredSessionFetchesNoSnapshot(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobB, idB := testSignedPolicy(t, priv, pinPolicyB)
	g := &tokenGatedServer{blob: blobB, id: idB, live: "tok-seed", refreshStatus: http.StatusServiceUnavailable}
	srv, fetches := countedServer(t, g)
	store := pinStore(t, srv.URL, keys)
	seedSession(t, store, idB, time.Now().Add(-time.Minute))
	if denied, reason := hookDecide(t, store, srv.URL, "b-cmd /x"); !denied || strings.HasPrefix(reason, liveNoSnapshotPhrase) {
		t.Errorf("b-cmd = denied %v with %q, want a fail-closed deny that does not call the session live", denied, reason)
	}
	if n := fetches.Load(); n != 0 {
		t.Errorf("%d snapshot fetches, want none", n)
	}
}
