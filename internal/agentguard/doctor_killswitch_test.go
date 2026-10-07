package agentguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveSession is a session token doctor may probe with.
func liveSession() Session {
	return Session{SessionID: "s1", SessionToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}
}

// aliveDaemon is a fresh heartbeat for the tests about the LANE, so their
// assertions stay about the lane; the daemon dimension has its own table in
// TestKillswitchDaemonLiveness.
func aliveDaemon() daemonLiveness {
	return daemonLiveness{state: heartbeatFresh, pid: 4242, interval: 30 * time.Second}
}

// TestKillswitchEdgePushProbe pins that the kill-switch line reports what the
// server answered when doctor dialed /v1/push. A line printed without a dial
// would claim sub-second revocation even against a server that has no
// /v1/push at all.
func TestKillswitchEdgePushProbe(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		stream     bool // hold the response open like the real SSE handler
		wantStatus string
		wantDetail string
	}{
		{"live lane", http.StatusOK, true, checkOK, "verified from here"},
		{"pre-3.1 server has no lane", http.StatusNotFound, false, checkWarn, "NO edge push lane"},
		{"hub down or at capacity", http.StatusServiceUnavailable, false, checkWarn, "unavailable"},
		{"token rejected", http.StatusUnauthorized, false, checkWarn, "rejected"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth, gotAccept string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/push" {
					t.Errorf("probe dialed %s, want /v1/push", r.URL.Path)
				}
				gotAuth, gotAccept = r.Header.Get("Authorization"), r.Header.Get("Accept")
				if !tc.stream {
					http.Error(w, "nope", tc.status)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(tc.status)
				w.(http.Flusher).Flush()
				<-r.Context().Done() // the real handler streams until the client leaves
			}))
			defer srv.Close()

			start := time.Now()
			got := killswitchCheck(context.Background(), srv.URL, liveSession(), true, false, aliveDaemon())
			if elapsed := time.Since(start); elapsed > probeTimeout {
				t.Errorf("probe took %s: a streaming lane must not hold doctor open", elapsed)
			}
			if got.Status != tc.wantStatus || !strings.Contains(got.Detail, tc.wantDetail) {
				t.Fatalf("killswitch = %+v, want %s mentioning %q", got, tc.wantStatus, tc.wantDetail)
			}
			if got.Status != checkOK && got.Hint == "" {
				t.Error("a non-ok check without a next step is noise")
			}
			if gotAuth != "Bearer tok" {
				t.Errorf("Authorization = %q, want the session token", gotAuth)
			}
			if gotAccept != "text/event-stream" {
				t.Errorf("Accept = %q, want text/event-stream", gotAccept)
			}
		})
	}
}

// TestKillswitchNeverGreenWithoutADial pins the rule: an unreachable push
// lane is WARN "NOT verified", never a green claim and never a FAIL. A box
// deliberately off the network is not broken, but it has no verified
// kill-switch either.
func TestKillswitchNeverGreenWithoutADial(t *testing.T) {
	ses := liveSession()

	// Server check already failed: no second wait, and no green.
	got := killswitchCheck(context.Background(), "http://127.0.0.1:9", ses, false, false, aliveDaemon())
	if got.Status != checkWarn || !strings.Contains(got.Detail, "NOT verified") {
		t.Errorf("unreachable-server killswitch = %+v, want warn/NOT verified", got)
	}

	// Server reachable, push endpoint dead (discard port).
	got = killswitchCheck(context.Background(), "http://127.0.0.1:9", ses, true, false, aliveDaemon())
	if got.Status != checkWarn || !strings.Contains(got.Detail, "NOT verified") || got.Hint == "" {
		t.Errorf("dead-endpoint killswitch = %+v, want warn/NOT verified", got)
	}

	// An expired session token cannot verify anything, and must not be spent
	// on a probe that can only 401.
	expired := ses
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("doctor probed the push lane with an expired session token")
	}))
	defer srv.Close()
	got = killswitchCheck(context.Background(), srv.URL, expired, true, false, aliveDaemon())
	if got.Status != checkWarn || !strings.Contains(got.Detail, "NOT verified") {
		t.Errorf("expired-session killswitch = %+v, want warn/NOT verified", got)
	}

	// A dead device credential means no refresh will come: the hint must
	// send the operator to enroll, never to a refresh that cannot happen.
	got = killswitchCheck(context.Background(), srv.URL, expired, true, true, aliveDaemon())
	if got.Status != checkWarn || !strings.Contains(got.Hint, "straza enroll") || strings.Contains(got.Hint, "next hook call") {
		t.Errorf("dead-credential killswitch = %+v, want warn with an enroll hint", got)
	}
}

// TestKillswitchDaemonLiveness pins the daemon dimension: the killswitch probe
// verifies the LANE, but a lane nobody subscribes to delivers nothing. Green
// must mean "revocation will actually arrive sub-second", so the check reads
// the heartbeat the daemon refreshes: green requires a fresh one, and every
// other heartbeat state turns a verified lane into a WARN naming the real
// revocation bound.
func TestKillswitchDaemonLiveness(t *testing.T) {
	// A live edge-push lane, identical for every case: only the heartbeat
	// varies, so any grade change below is the daemon dimension alone.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	// Truncated to the second because the RFC3339 stamps below are, so the ages
	// asserted on stay exact.
	now := time.Now().Truncate(time.Second)
	stamp := func(age time.Duration) string { return now.Add(-age).Format(time.RFC3339) }
	tests := []struct {
		name       string
		file       string // "" = no file at all
		wantStatus string
		wantDetail []string
		wantHint   []string
	}{
		{
			name:       "fresh heartbeat: the full green claim",
			file:       `{"pid":4242,"at":"` + stamp(2*time.Second) + `","intervalSeconds":30}`,
			wantStatus: checkOK,
			wantDetail: []string{"verified from here", "daemon alive (pid 4242)"},
		},
		{
			// 10 minutes against a 30 s interval is far past the 3× staleness
			// bound: the daemon is gone or wedged, and the line must name the
			// age and the interval so the operator sees the arithmetic.
			name:       "stale heartbeat: daemon gone, bound named",
			file:       `{"pid":4242,"at":"` + stamp(10*time.Minute) + `","intervalSeconds":30}`,
			wantStatus: checkWarn,
			wantDetail: []string{"10m0s", "30s"},
			wantHint:   []string{"straza daemon", "token TTL"},
		},
		{
			name:       "absent heartbeat: nothing subscribed",
			file:       "",
			wantStatus: checkWarn,
			wantDetail: []string{"no daemon heartbeat"},
			wantHint:   []string{"straza daemon", "token TTL"},
		},
		{
			// A corrupt file must never read as a live daemon (being wrong in
			// the reassuring direction is the failure this check exists to
			// remove), and must not read as "everything missing" either.
			name:       "corrupt heartbeat: cannot confirm, never alive",
			file:       `{"pid": not json`,
			wantStatus: checkWarn,
			wantDetail: []string{"unreadable"},
			wantHint:   []string{"straza daemon", "token TTL"},
		},
		{
			// Its own tick still respects a slow daemon: within 3× the WRITTEN
			// interval (here 5 minutes on an operator's --poll 120s) is fresh.
			name:       "slow-poll daemon is judged by its own interval",
			file:       `{"pid":77,"at":"` + stamp(5*time.Minute) + `","intervalSeconds":120}`,
			wantStatus: checkOK,
			wantDetail: []string{"daemon alive (pid 77)"},
		},
		{
			// A stamp far in the FUTURE is a dead daemon under a clock that
			// moved backward (VM snapshot restore, clock set back): clamping it
			// to age 0 would read "daemon alive" for as long as the regression
			// lasted, the one reassuring-but-wrong verdict this table had left.
			name:       "future stamp beyond the allowance: cannot confirm, never alive",
			file:       `{"pid":4242,"at":"` + stamp(-10*time.Minute) + `","intervalSeconds":30}`,
			wantStatus: checkWarn,
			wantDetail: []string{"10m0s", "FUTURE"},
			wantHint:   []string{"straza daemon", "token TTL"},
		},
		{
			// A small future skew (an NTP step correction) within one write
			// interval stays fresh: a live daemon re-stamps on its next tick,
			// and a dead one falls to the normal staleness bound at most one
			// interval later, so no flapping on routine clock discipline.
			name:       "future stamp within one interval stays fresh",
			file:       `{"pid":4242,"at":"` + stamp(-10*time.Second) + `","intervalSeconds":30}`,
			wantStatus: checkOK,
			wantDetail: []string{"daemon alive (pid 4242)"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "daemon-heartbeat.json")
			if tc.file != "" {
				if err := os.WriteFile(path, []byte(tc.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := killswitchCheck(context.Background(), srv.URL, liveSession(), true, false,
				readDaemonLiveness(path, now))
			if got.Status != tc.wantStatus {
				t.Fatalf("killswitch = %+v, want %s", got, tc.wantStatus)
			}
			for _, want := range tc.wantDetail {
				if !strings.Contains(got.Detail, want) {
					t.Errorf("detail = %q, want it to mention %q", got.Detail, want)
				}
			}
			if got.Status != checkOK && got.Hint == "" {
				t.Error("a non-ok check without a next step is noise")
			}
			for _, want := range tc.wantHint {
				if !strings.Contains(got.Hint, want) {
					t.Errorf("hint = %q, want it to mention %q", got.Hint, want)
				}
			}
			if tc.wantStatus != checkOK && strings.Contains(got.Detail+got.Hint, "daemon alive") {
				t.Errorf("non-green line claims a live daemon: %+v", got)
			}
		})
	}
}
