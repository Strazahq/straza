package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// strazadBadSessionSentence is strazad's own 401 sentence for a snapshot
// request whose bearer is not a live session token of that server
// (snapshotBadSessionMsg in internal/server/api_policy.go), copied verbatim.
const strazadBadSessionSentence = "The token on this request is not a valid session token from this server, " +
	"because it is expired, is another kind of credential, or was issued elsewhere. " +
	"The enterprise profile serves the policy snapshot only to a checked-in session. " +
	"Start a new session so straza checks in again, and run straza doctor if it keeps failing."

// strazadNoSessionSentence is strazad's own 401 sentence for a snapshot
// request that carries no bearer (snapshotNoSessionMsg in
// internal/server/api_policy.go), copied verbatim. It names a new session
// but not straza doctor.
const strazadNoSessionSentence = "The enterprise profile serves the policy snapshot only to a checked-in session, " +
	"and this request carried no session token. Update straza on this machine, " +
	"then start a new session so it checks in and sends its token. " +
	"If straza is already current, a proxy in front of strazad is dropping the Authorization header."

// execAdvice is the clause the client adds after a refusal whose own
// sentence names a new session and straza doctor.
const execAdvice = "For straza exec, restarting the agent starts no new session, " +
	"because straza exec checks in again by itself only when this session is about to end."

// disabledSentence is strazad's refusal of a check-in whose user is disabled.
const disabledSentence = "user is disabled. Contact your administrator"

// issuerRefusal is the error_description strazad's issuer gives with an
// invalid_client answer to the client_credentials grant.
const issuerRefusal = "client authentication failed"

// tokenRejectedSentence is strazad's 401 to a renewal whose session token no
// longer verifies.
const tokenRejectedSentence = "session token rejected: re-enroll or restart the session"

// cannedAnswer is an HTTP status and the sentence strazad words it with.
type cannedAnswer struct {
	status int
	msg    string
}

func (a cannedAnswer) write(w http.ResponseWriter) {
	raw, _ := json.Marshal(map[string]string{"error": a.msg})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.status)
	_, _ = w.Write(raw)
}

// u3Server serves g, except that refresh answers every session-token
// renewal, device every device-token check-in, snapshot every snapshot
// request and grant every token request of the key lane, whose msg is the
// OAuth error code, each only when its status is set. It records the bearer
// of every snapshot request and counts the check-ins.
type u3Server struct {
	g                                *tokenGatedServer
	refresh, device, snapshot, grant cannedAnswer

	mu       sync.Mutex
	bearers  []string
	checkins int
}

func (s *u3Server) start(t *testing.T) *httptest.Server {
	t.Helper()
	serve := s.g.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/snapshot":
			s.mu.Lock()
			s.bearers = append(s.bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			s.mu.Unlock()
			if s.snapshot.status != 0 {
				s.snapshot.write(w)
				return
			}
		case "/v1/checkin":
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				SessionToken string `json:"session_token"`
				DeviceToken  string `json:"device_token"`
			}
			_ = json.Unmarshal(raw, &body)
			s.mu.Lock()
			s.checkins++
			s.mu.Unlock()
			switch {
			case body.SessionToken != "" && s.refresh.status != 0:
				s.refresh.write(w)
				return
			case body.DeviceToken != "" && s.device.status != 0:
				s.device.write(w)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		case "/oidc/token":
			if s.grant.status != 0 {
				raw, _ := json.Marshal(map[string]string{"error": s.grant.msg, "error_description": issuerRefusal})
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(s.grant.status)
				_, _ = w.Write(raw)
				return
			}
		}
		serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seen returns the bearers of the snapshot requests and the check-ins so far.
func (s *u3Server) seen() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bearers...), s.checkins
}

// TestRenewalOfPinnedIDFetchesMissingSnapshot pins that a renewal that
// reports the snapshot id the session already pins fetches the snapshot when
// no verifiable blob is on disk, on the daemon tick, the daemon's re-acquire
// and the MCP proxy's token refresh. A torn blob counts as none. A
// verifiable blob is kept and nothing is fetched.
func TestRenewalOfPinnedIDFetchesMissingSnapshot(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobB, idB := testSignedPolicy(t, priv, pinPolicyB)
	lanes := []struct {
		name          string
		refreshStatus int
		run           func(ctx context.Context, t *testing.T, store *Store, serverURL string, out io.Writer) error
	}{
		{name: "daemon tick", run: daemonTick},
		{name: "daemon re-acquire", refreshStatus: http.StatusUnauthorized, run: daemonTick},
		{name: "MCP proxy token refresh", run: proxyRefresh},
	}
	disks := []struct {
		name        string
		blob        []byte
		wantFetches int64
	}{
		{"no blob", nil, 1},
		{"torn blob", blobB[:len(blobB)/2], 1},
		{"verifiable blob", blobB, 0},
	}
	for _, lane := range lanes {
		for _, disk := range disks {
			t.Run(lane.name+", "+disk.name, func(t *testing.T) {
				g := &tokenGatedServer{blob: blobB, id: idB, live: "tok-seed", refreshStatus: lane.refreshStatus}
				srv, fetches := countedServer(t, g)
				store := pinStore(t, srv.URL, keys)
				if disk.blob != nil {
					if err := store.SaveSnapshot(disk.blob); err != nil {
						t.Fatal(err)
					}
				}
				seedSession(t, store, idB, time.Now().Add(time.Hour))
				if err := lane.run(context.Background(), t, store, srv.URL, io.Discard); err != nil {
					t.Fatalf("the renewal failed: %v", err)
				}
				if n := fetches.Load(); n != disk.wantFetches {
					t.Errorf("%d snapshot fetches, want %d", n, disk.wantFetches)
				}
				cfg, _ := store.LoadConfig()
				if diskID, ok := diskSnapshotID(store, cfg); !ok || diskID != idB {
					t.Errorf("disk blob = %q (verified %v), want %q", diskID, ok, idB)
				}
				if ses, err := store.LoadSession(); err != nil || ses.SnapshotID != idB || ses.PolicyRefused != "" {
					t.Errorf("session pins %q with refusal %q (%v); want %q and no refusal", ses.SnapshotID, ses.PolicyRefused, err, idB)
				}
			})
		}
	}
}

// TestExpiredSessionRenewsBeforeFetch pins that a hook whose session has
// run out and whose policy snapshot is missing, or whose verified blob is not
// the one it pins, renews the session first, through the steps
// RefreshIfStale takes, and then decides by the policy. A renewal the server
// refuses denies with the server's own sentence, and a renewal that fails
// otherwise denies with what failed. Neither asks for a snapshot, and no
// snapshot request carries the expired token. The last row is the positive
// control: a live session is not renewed and fetches as
// TestLiveSessionFetchesMissingSnapshot pins.
func TestExpiredSessionRenewsBeforeFetch(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	_, idA := testSignedPolicy(t, priv, pinPolicyA)
	blobB, idB := testSignedPolicy(t, priv, pinPolicyB)
	refused := "session renewal was refused: " + disabledSentence
	failed := "). Restart the session, or run `straza doctor`."
	rows := []struct {
		name            string
		expires         time.Duration
		pin             string // the seeded pin, idB when empty
		blob            []byte // a blob on disk before the decision
		noIdentity      bool
		refresh, device cannedAnswer
		want            string   // the reason after the marker, exactly
		wantParts       []string // phrases of the reason, when want is empty
		wantFetches     int
		wantCheckins    int
		wantSession     string // session id on disk afterwards
		wantToken       string // session token on disk afterwards
	}{
		{name: "the server renews the session", expires: -10 * time.Minute,
			want: "b", wantFetches: 1, wantCheckins: 1, wantSession: "s-seed", wantToken: "tok-1"},
		{name: "the token is refused and the device credential checks in", expires: -10 * time.Minute,
			refresh: cannedAnswer{http.StatusUnauthorized, tokenRejectedSentence},
			want:    "b", wantFetches: 1, wantCheckins: 2, wantSession: "s-tok-1", wantToken: "tok-1"},
		{name: "a verified blob the pin does not name, as a crash between the two writes of a renewal leaves",
			expires: -10 * time.Minute, pin: idA, blob: blobB,
			want: "b", wantCheckins: 1, wantSession: "s-seed", wantToken: "tok-1"},
		{name: "the server refuses the renewal because the user is disabled", expires: -10 * time.Minute,
			refresh: cannedAnswer{http.StatusForbidden, disabledSentence},
			want:    refused, wantCheckins: 1, wantSession: "s-seed", wantToken: "tok-seed"},
		{name: "the token is refused and so is the device credential's check-in", expires: -10 * time.Minute,
			refresh: cannedAnswer{http.StatusUnauthorized, tokenRejectedSentence},
			device:  cannedAnswer{http.StatusForbidden, disabledSentence},
			want:    refused, wantCheckins: 2, wantSession: "s-seed", wantToken: "tok-seed"},
		{name: "the token is refused and no identity can check in", expires: -10 * time.Minute, noIdentity: true,
			refresh:      cannedAnswer{http.StatusUnauthorized, tokenRejectedSentence},
			wantParts:    []string{"session token expired ", " ago and automatic renewal failed (no enrolled identity: ", failed},
			wantCheckins: 1, wantSession: "s-seed", wantToken: "tok-seed"},
		{name: "strazad answers the renewal with a 503", expires: -10 * time.Minute,
			refresh:      cannedAnswer{http.StatusServiceUnavailable, "store unavailable"},
			wantParts:    []string{"session token expired ", " ago and automatic renewal failed (", "store unavailable", failed},
			wantCheckins: 1, wantSession: "s-seed", wantToken: "tok-seed"},
		{name: "a proxy answers the renewal with a 429 and no reason", expires: -10 * time.Minute,
			refresh: cannedAnswer{http.StatusTooManyRequests, ""},
			wantParts: []string{"session token expired ", " ago, and the renewal got an answer that does not say whether this session is still accepted: " +
				"the server answered HTTP 429 and gave no reason. The next tool call tries the renewal again."},
			wantCheckins: 1, wantSession: "s-seed", wantToken: "tok-seed"},
		{name: "a live session is not renewed", expires: time.Hour,
			want: "b", wantFetches: 1, wantSession: "s-seed", wantToken: "tok-seed"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			s := &u3Server{g: &tokenGatedServer{blob: blobB, id: idB, live: "tok-seed"}, refresh: row.refresh, device: row.device}
			srv := s.start(t)
			store := pinStore(t, srv.URL, keys)
			if row.noIdentity {
				if err := os.Remove(store.statePath("identity.json")); err != nil {
					t.Fatal(err)
				}
			}
			if row.blob != nil {
				if err := store.SaveSnapshot(row.blob); err != nil {
					t.Fatal(err)
				}
			}
			pin := idB
			if row.pin != "" {
				pin = row.pin
			}
			seedSession(t, store, pin, time.Now().Add(row.expires).Truncate(time.Second))

			denied, reason := hookDecide(t, store, srv.URL, "b-cmd /x")
			reason = strings.TrimPrefix(reason, "Straza: ")
			switch {
			case !denied:
				t.Fatalf("b-cmd was allowed, want a deny")
			case row.want != "" && reason != row.want:
				t.Fatalf("b-cmd denied with %q, want %q", reason, row.want)
			}
			for _, part := range row.wantParts {
				if !strings.Contains(reason, part) {
					t.Errorf("b-cmd denied with %q, which lacks %q", reason, part)
				}
			}
			bearers, checkins := s.seen()
			if len(bearers) != row.wantFetches || checkins != row.wantCheckins {
				t.Errorf("%d snapshot requests and %d check-ins, want %d and %d", len(bearers), checkins, row.wantFetches, row.wantCheckins)
			}
			for _, b := range bearers {
				if row.expires < 0 && b == "tok-seed" {
					t.Errorf("a snapshot request carried the expired session's token")
				}
			}
			ses, err := store.LoadSession()
			if err != nil {
				t.Fatal(err)
			}
			if ses.SessionID != row.wantSession || ses.SessionToken != row.wantToken {
				t.Errorf("session %q holds token %q, want %q and %q", ses.SessionID, ses.SessionToken, row.wantSession, row.wantToken)
			}
			cfg, _ := store.LoadConfig()
			diskID, onDisk := diskSnapshotID(store, cfg)
			if wantBlob := row.wantFetches > 0 || row.blob != nil; onDisk != wantBlob || (wantBlob && (diskID != idB || ses.SnapshotID != idB)) {
				t.Errorf("disk blob %q (verified %v) and pin %q; want a blob %v with id %q", diskID, onDisk, ses.SnapshotID, wantBlob, idB)
			}
			if row.wantToken == "tok-1" && !ses.ExpiresAt.After(time.Now()) {
				t.Errorf("the renewed session ends at %v, which has passed", ses.ExpiresAt)
			}
		})
	}
}

// TestRefusalThatNamesItsRemedyKeepsItsOwnAdvice pins that when strazad's
// refusal of the snapshot fetch already tells the person to start a new
// session and run straza doctor, the deny says each once and adds only what
// that means for straza exec, which the server's sentence does not say. It
// never says that retrying will not help, because a later renewal can heal.
// A refusal that names a new session but not straza doctor, as strazad's 401
// for a request with no token does, and one that names neither keep the
// client's advice (positive controls).
func TestRefusalThatNamesItsRemedyKeepsItsOwnAdvice(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	_, idA := testSignedPolicy(t, priv, pinPolicyA)
	blobB, idB := testSignedPolicy(t, priv, pinPolicyB)
	rows := []struct {
		name     string
		snapshot cannedAnswer
		advice   string
		notWant  []string
		once     bool // the deny tells the person to start a new session and run straza doctor once each
	}{
		{"strazad's 401 for a token that is not a live session", cannedAnswer{http.StatusUnauthorized, strazadBadSessionSentence},
			execAdvice, []string{"Retrying will not"}, true},
		{"strazad's 401 for a request with no token", cannedAnswer{http.StatusUnauthorized, strazadNoSessionSentence},
			newSessionAdvice, nil, false},
		{"a 403 that names no remedy", cannedAnswer{http.StatusForbidden, disabledSentence}, newSessionAdvice, nil, false},
	}
	for _, lane := range adoptLanes {
		for _, row := range rows {
			t.Run(lane.name+", "+row.name, func(t *testing.T) {
				s := &u3Server{g: &tokenGatedServer{blob: blobB, id: idB, live: "tok-seed"}, snapshot: row.snapshot}
				srv := s.start(t)
				store := pinStore(t, srv.URL, keys)
				seedSession(t, store, idA, time.Now().Add(time.Hour))

				denied, reason := lane.decide(t, store, srv.URL, "b-cmd /x")
				if !denied || !strings.HasPrefix(reason, liveNoSnapshotPhrase) || !strings.Contains(reason, "The server refused it: "+row.snapshot.msg) {
					t.Fatalf("b-cmd = denied %v with %q; want the missing-snapshot deny quoting %q", denied, reason, row.snapshot.msg)
				}
				if !strings.Contains(reason, row.advice) {
					t.Errorf("reason %q lacks the advice %q", reason, row.advice)
				}
				for _, untrue := range row.notWant {
					if strings.Contains(reason, untrue) {
						t.Errorf("reason %q says %q, which is untrue after this answer", reason, untrue)
					}
				}
				if !row.once {
					return
				}
				if n := strings.Count(strings.ToLower(reason), "start a new session"); n != 1 {
					t.Errorf("reason says to start a new session %d times, want once: %q", n, reason)
				}
				if n := strings.Count(reason, "straza doctor"); n != 1 {
					t.Errorf("reason names straza doctor %d times, want once: %q", n, reason)
				}
			})
		}
	}
}

// TestRefusedRenewalEndsOfflineGrace pins that a renewal the server
// refused ends the offline grace at once, because a refusal is an answer and
// not being offline, on the device lane and on a headless AI agent's key
// lane alike, and so does a 401 or a 403 with no reason, which a proxy that
// replaced strazad's answer leaves. A transport failure, a 401 whose
// re-acquire could not complete, a 408 or a 429 from something in front of
// strazad, and any other status with no reason keep the grace, because the
// server's judgment is unknown. The enterprise profile, whose grace is zero,
// denies either way.
func TestRefusedRenewalEndsOfflineGrace(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	standalone, standaloneID := testSignedPolicy(t, priv, pinPolicyB)
	enterprise, enterpriseID := testEnterprisePolicy(t, priv, pinPolicyB)
	refused := "Straza: session renewal was refused: " + disabledSentence
	rows := []struct {
		name                   string
		enterprise             bool
		unreachable            bool
		noIdentity             bool
		headless               bool // a headless AI agent enrolled on the key lane
		refresh, device, grant cannedAnswer
		want                   string // a phrase of the deny, or "" for an allow
	}{
		{name: "standalone, the renewal is refused",
			refresh: cannedAnswer{http.StatusForbidden, disabledSentence}, want: refused},
		{name: "standalone, the token and then the device credential's check-in are refused",
			refresh: cannedAnswer{http.StatusUnauthorized, tokenRejectedSentence},
			device:  cannedAnswer{http.StatusForbidden, disabledSentence}, want: refused},
		{name: "standalone, strazad cannot be reached", unreachable: true},
		{name: "standalone, the token is refused and no identity can re-acquire", noIdentity: true,
			refresh: cannedAnswer{http.StatusUnauthorized, tokenRejectedSentence}},
		{name: "standalone, the token is refused and the issuer refuses the headless AI agent", headless: true,
			refresh: cannedAnswer{http.StatusUnauthorized, tokenRejectedSentence},
			grant:   cannedAnswer{http.StatusBadRequest, "invalid_client"},
			want:    "Straza: session renewal was refused: headless login refused (invalid_client): " + issuerRefusal + "."},
		{name: "standalone, the token is refused and the headless AI agent's issuer fails", headless: true,
			refresh: cannedAnswer{http.StatusUnauthorized, tokenRejectedSentence},
			grant:   cannedAnswer{http.StatusServiceUnavailable, ""}},
		{name: "standalone, a proxy answers the renewal with a 429 and a reason",
			refresh: cannedAnswer{http.StatusTooManyRequests, "rate limited. Retry shortly"}},
		{name: "standalone, a proxy answers the renewal with a 408 and no reason",
			refresh: cannedAnswer{http.StatusRequestTimeout, ""}},
		{name: "standalone, the renewal gets a 404 with no reason",
			refresh: cannedAnswer{http.StatusNotFound, ""}},
		{name: "standalone, the token is refused and the device check-in gets a 403 with no reason",
			refresh: cannedAnswer{http.StatusUnauthorized, tokenRejectedSentence},
			device:  cannedAnswer{http.StatusForbidden, ""},
			want:    "Straza: session renewal was refused: the server answered HTTP 403 and gave no reason."},
		{name: "enterprise, a proxy answers the renewal with a 429 and no reason", enterprise: true,
			refresh: cannedAnswer{http.StatusTooManyRequests, ""},
			want:    "the server answered HTTP 429 and gave no reason. The next tool call tries the renewal again."},
		{name: "enterprise, the renewal is refused", enterprise: true,
			refresh: cannedAnswer{http.StatusForbidden, disabledSentence}, want: refused},
		{name: "enterprise, strazad cannot be reached", enterprise: true, unreachable: true,
			want: "the offline grace period (0s) is exhausted"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			blob, id := standalone, standaloneID
			if row.enterprise {
				blob, id = enterprise, enterpriseID
			}
			s := &u3Server{g: &tokenGatedServer{blob: blob, id: id, live: "tok-seed"}, refresh: row.refresh, device: row.device, grant: row.grant}
			url := s.start(t).URL
			if row.unreachable {
				url = "http://127.0.0.1:1"
			}
			store := pinStore(t, url, keys)
			if row.noIdentity {
				if err := os.Remove(store.statePath("identity.json")); err != nil {
					t.Fatal(err)
				}
			}
			if row.headless {
				if err := Keygen(store, "u3-bot", false, io.Discard); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveIdentity(Identity{Username: "u3-bot", Headless: HeadlessKey}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SaveSnapshot(blob); err != nil {
				t.Fatal(err)
			}
			seedSession(t, store, id, time.Now().Add(-time.Minute))

			denied, reason := hookDecide(t, store, url, "echo hi")
			switch {
			case row.want == "" && denied:
				t.Errorf("echo = denied with %q, want the allow of the cached policy inside the grace", reason)
			case row.want != "" && (!denied || !strings.Contains(reason, row.want)):
				t.Errorf("echo = denied %v with %q, want a deny with %q", denied, reason, row.want)
			}
		})
	}
}

// Hints of the two failing snapshot lines of straza doctor.
const (
	keysUnreadableHint = "run `straza enroll` again to pin the server's snapshot keys, or on a machine set up with " +
		"`straza install --managed` ask an administrator to run that install again with `--server <server-url>`, " +
		"because straza enroll does not change the keys a managed install pinned"
	snapshotUnverifiedHint = "hooks are denying (fail closed). Start a new session of the AI agent, which fetches a fresh snapshot. " +
		"If this line still fails after that, the server's snapshot keys changed: run `straza enroll` again, or on a machine set up with " +
		"`straza install --managed` ask an administrator to run that install again with `--server <server-url>`"
)

// TestDoctorSnapshotHintsNameManagedInstall pins that both failing
// snapshot lines of straza doctor give the managed-install remedy beside
// straza enroll, because on a managed install the managed config wins and
// straza enroll does not change the keys it pinned.
func TestDoctorSnapshotHintsNameManagedInstall(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blob, id := testSignedPolicy(t, priv, pinPolicyA)
	rows := []struct {
		name       string
		keys       map[string]string
		blob       []byte
		wantDetail string
		wantHint   string
	}{
		{"the pinned keys cannot be read", map[string]string{"k1": "not base64!"}, blob, "bad pinned key k1", keysUnreadableHint},
		{"the cached snapshot fails verification", keys, []byte("random bytes, not a signed snapshot"), "cached snapshot fails verification", snapshotUnverifiedHint},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Setenv("STRAZA_HOME", t.TempDir())
			store, err := OpenStore()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveSnapshot(row.blob); err != nil {
				t.Fatal(err)
			}
			c := snapshotCheck(store, Config{SnapshotKeys: row.keys}, Session{SnapshotID: id}, true)
			if c.Status != checkFail || !strings.Contains(c.Detail, row.wantDetail) || c.Hint != row.wantHint {
				t.Errorf("snapshot line = %s %q -> %q; want fail with %q -> %q", c.Status, c.Detail, c.Hint, row.wantDetail, row.wantHint)
			}
		})
	}
}
