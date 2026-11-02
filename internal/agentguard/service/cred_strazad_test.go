package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/policy"
)

// fakeClock is a settable clock shared by the service and the fake strazad.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 21, 12, 0, 30, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeStrazad mirrors the key lane of internal/server/identity_key.go: the
// nonce is checked before anything else and never touches the counter
// (checkProof, :70-95, authn.ErrStaleNonce), the proof signs the body
// counter as ctr, the proof id is spent before the counter is read
// (:271-281), a counter above the stored one advances it (:299-305), and
// one at or below it revokes the device as key reuse (:320-325,
// revokeReusedKey). Nonces live for the current and the previous 60-second
// window, as authn.ProofVerifier's do. Refresh needs a proof whose ath
// hashes the body token, and an agent's id-token check-in a proof with
// neither ath nor ctr (pinned server rule 5).
type fakeStrazad struct {
	t        *testing.T
	srv      *httptest.Server
	clock    *fakeClock
	snap     []byte
	snapK    ed25519.PublicKey
	snapPriv ed25519.PrivateKey

	mu        sync.Mutex
	devices   map[string]*fakeDevice
	spent     map[string]bool
	sessions  map[string]fakeSession // token to session
	accepted  []int64                // counters in the order they were accepted
	revoked   int
	reuse     []string
	seen      []int64 // every counter that reached the counter check
	received  []int64 // every key check-in counter, in arrival order
	tokens    int     // client_credentials grants
	snaps     int     // snapshot downloads answered 200
	refreshes int     // refresh requests
	batches   int     // audit records accepted
	checkins  []http.Header

	// drop, when it returns true for a counter, cuts the connection after
	// the server acted on that check-in, so its answer is lost.
	drop func(counter int64) bool
	// delay, when it returns a channel for a counter, holds that check-in
	// after its body was read and before the nonce check until the channel
	// closes, as a slow network or a busy server does.
	delay func(counter int64) <-chan struct{}
	// reportID, when set, is the snapshot id check-ins and refreshes name
	// in place of the one served, as activation skew leaves it.
	reportID string
	// snapFail, when set, is the status every snapshot fetch gets, with
	// snapMsg as its sentence.
	snapFail int
	snapMsg  string
	snapReqs int // snapshot requests, answered or not
	// snapHold, when set, holds every snapshot request until it closes.
	snapHold chan struct{}
	// roles and userType are the identity check-ins and refreshes
	// resolve, and noRoles drops the roles field, as an older server does.
	roles    []string
	userType string
	noRoles  bool
	// auditFail, when set, is the status every audit batch gets.
	auditFail int
	// pushRefuse, when set, is the status every push connect gets, and
	// refreshRefuse the 403 sentence every refresh gets.
	pushRefuse    int
	refreshRefuse string
	// echoStatus, when set, is the status /v1/echo answers a bound request,
	// and echoes counts its requests, with the last body in echoBody.
	echoStatus int
	echoes     int
	echoBody   string
	// checkinRefuse, when set, is the 403 sentence every key check-in gets
	// after its proof, before the counter, as the sign-in deadline is.
	checkinRefuse string
	// pushEvents hands one event to one open push stream, and "close" ends
	// that stream.
	pushEvents chan string
	// enrollRefuse, when set, is the 403 sentence every enrollment gets.
	enrollRefuse string
	// lateDone closes when the one check-in delay held has finished.
	lateDone chan struct{}
	// idp, when set, is the idp.json strazad answers, which is a 404
	// otherwise, as from a server older than idp.json.
	idp map[string]any
	// The browser lane: codes maps an authorization code to its request,
	// authorized is the last authorization request, idTokens maps each ID
	// token the code grant minted to its nonce, enrolledNonce is the nonce
	// of the ID token the last enrollment carried, and deviceStarts counts
	// device authorization requests.
	codes         map[string]url.Values
	authorized    url.Values
	idTokens      map[string]string
	enrolledNonce string
	deviceStarts  int
	// git is the git grant side: the repos it lists, the grants it minted
	// and the git_upstreams of every check-in answer.
	git fakeGit
}

type fakeDevice struct {
	id, thumb, user string
	counter         int64
	revoked         bool
}

// fakeSession is one session token strazad minted: valid for tokenLife
// from its minting, and still valid after a refresh minted a newer one, as
// strazad's in-memory token check keeps it.
type fakeSession struct {
	id, user, cnf string
	exp           time.Time
}

// tokenLife is the life of a session token the fake mints, its expires_in.
const tokenLife = 300 * time.Second

func newFakeStrazad(t *testing.T, clock *fakeClock) *fakeStrazad {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	snap, err := policy.Compile(policy.CompileInput{LocalDefault: policy.EffectAllow, MaxAge: 900, CreatedUnix: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	signed, _, err := snap.Sign("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeStrazad{t: t, clock: clock, snap: signed, snapK: pub, snapPriv: priv,
		devices: map[string]*fakeDevice{}, spent: map[string]bool{}, sessions: map[string]fakeSession{},
		lateDone: make(chan struct{}), pushEvents: make(chan string),
		codes: map[string]url.Values{}, idTokens: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeStrazad) nonce(at time.Time) string { return fmt.Sprintf("n%d", at.Unix()/60) }

func (f *fakeStrazad) nonceFresh(n string) bool {
	now := f.clock.Now()
	return n != "" && (n == f.nonce(now) || n == f.nonce(now.Add(-60*time.Second)))
}

func (f *fakeStrazad) fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (f *fakeStrazad) ok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeStrazad) challenge(w http.ResponseWriter) {
	w.Header().Set(dpop.HeaderNonce, f.nonce(f.clock.Now()))
	w.Header().Set("WWW-Authenticate", `DPoP error="use_dpop_nonce"`)
	f.fail(w, http.StatusUnauthorized, "Straza needs a fresh proof nonce.")
}

// proof verifies r's DPoP proof the way authn.ProofVerifier does and
// returns its claims and thumbprint. stale reports a missing or old nonce,
// which the caller answers with the challenge before any other check.
func (f *fakeStrazad) proof(r *http.Request, ath string, ctr int64) (claims map[string]any, thumb string, stale bool, reason string) {
	raw := r.Header.Get(dpop.HeaderProof)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, "", false, "no proof"
	}
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var hdr struct {
		Typ string `json:"typ"`
		Alg string `json:"alg"`
		JWK struct {
			X string `json:"x"`
		} `json:"jwk"`
	}
	if json.Unmarshal(hb, &hdr) != nil || json.Unmarshal(cb, &claims) != nil {
		return nil, "", false, "malformed"
	}
	if n, _ := claims["nonce"].(string); !f.nonceFresh(n) {
		return nil, "", true, ""
	}
	pub, _ := base64.RawURLEncoding.DecodeString(hdr.JWK.X)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	switch {
	case hdr.Typ != dpop.ProofType || hdr.Alg != "EdDSA" || len(pub) != ed25519.PublicKeySize:
		return nil, "", false, "header"
	case !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig):
		return nil, "", false, "signature"
	case claims["htm"] != r.Method:
		return nil, "", false, "htm"
	case claims["htu"] != f.srv.URL+r.URL.Path:
		return nil, "", false, fmt.Sprintf("htu %v", claims["htu"])
	}
	gotATH, hasATH := claims["ath"].(string)
	switch {
	case ath == "" && hasATH:
		return nil, "", false, "ath present"
	case ath != "" && gotATH != dpop.AccessTokenHash(ath):
		return nil, "", false, "ath"
	}
	gotCTR, hasCTR := claims["ctr"].(float64)
	switch {
	case ctr == 0 && hasCTR:
		return nil, "", false, "ctr present"
	case ctr > 0 && int64(gotCTR) != ctr:
		return nil, "", false, "ctr"
	}
	return claims, dpop.Thumbprint(pub), false, ""
}

// spend records a proof id and reports whether it was fresh.
func (f *fakeStrazad) spend(claims map[string]any) bool {
	jti, _ := claims["jti"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	if jti == "" || f.spent[jti] {
		return false
	}
	f.spent[jti] = true
	return true
}

func (f *fakeStrazad) mint(user, cnf string) map[string]any {
	id, tok := randHex(8), "tok-"+randHex(16)
	f.mu.Lock()
	f.sessions[tok] = fakeSession{id: id, user: user, cnf: cnf, exp: f.clock.Now().Add(tokenLife)}
	f.mu.Unlock()
	return f.answer(id, tok, user)
}

// answer is a check-in or refresh answer for session id with token tok.
func (f *fakeStrazad) answer(id, tok, user string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	reported := f.reportID
	if reported == "" {
		reported = f.snapIDLocked()
	}
	out := map[string]any{"session_id": id, "session_token": tok, "expires_in": int(tokenLife / time.Second), "user": user,
		"attestation": "none", "snapshot_id": reported, "user_type": f.userType,
		"knowledge_packs": []map[string]string{{"name": "golang-style", "version": "1", "content": "always run gofmt", "checksum": "c1"}}}
	if !f.noRoles {
		roles := f.roles
		if roles == nil {
			roles = []string{"dev"}
		}
		out["roles"] = roles
	}
	if f.git.upstreams != nil {
		out["git_upstreams"] = f.git.upstreams
	}
	return out
}

func (f *fakeStrazad) snapID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapIDLocked()
}

func (f *fakeStrazad) snapIDLocked() string {
	sum := sha256.Sum256(f.snap)
	return hex.EncodeToString(sum[:])
}

// activate makes a new snapshot, with maxAge as its offline grace, the one
// strazad serves.
func (f *fakeStrazad) activate(t *testing.T, maxAge int64) {
	t.Helper()
	f.activateAt(t, maxAge, 1700000000)
}

// activateAt is activate for a snapshot compiled at created.
func (f *fakeStrazad) activateAt(t *testing.T, maxAge, created int64) {
	t.Helper()
	snap, err := policy.Compile(policy.CompileInput{LocalDefault: policy.EffectAllow, MaxAge: maxAge, CreatedUnix: created})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snap, _, err = snap.Sign("k1", f.snapPriv); err != nil {
		t.Fatal(err)
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// bound returns the session of r's DPoP token after its proof with ath. An
// unknown or expired token is refused with 401.
func (f *fakeStrazad) bound(w http.ResponseWriter, r *http.Request) (fakeSession, bool) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "DPoP ")
	f.mu.Lock()
	ses, ok := f.sessions[tok]
	f.mu.Unlock()
	if !ok || !f.clock.Now().Before(ses.exp) {
		f.fail(w, http.StatusUnauthorized, "session token rejected")
		return ses, false
	}
	claims, thumb, stale, reason := f.proof(r, tok, 0)
	switch {
	case stale:
		f.challenge(w)
		return ses, false
	case reason != "" || thumb != ses.cnf || !f.spend(claims):
		f.fail(w, http.StatusUnauthorized, "This session token belongs to a key on the machine that checked in.")
		return ses, false
	}
	return ses, true
}

func (f *fakeStrazad) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(dpop.HeaderNonce, f.nonce(f.clock.Now()))
	switch r.URL.Path {
	case "/.well-known/straza/idp.json":
		f.mu.Lock()
		idp := f.idp
		f.mu.Unlock()
		if idp == nil {
			http.NotFound(w, r)
			return
		}
		f.ok(w, idp)
	case "/.well-known/openid-configuration":
		f.ok(w, map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/oidc/authorize",
			"device_authorization_endpoint": f.srv.URL + "/oidc/device_authorization", "token_endpoint": f.srv.URL + "/oidc/token"})
	case "/oidc/authorize":
		f.authorize(w, r)
	case "/oidc/device_authorization":
		f.mu.Lock()
		f.deviceStarts++
		f.mu.Unlock()
		f.ok(w, map[string]any{"device_code": "dc", "user_code": "ABCD-EFGH", "verification_uri_complete": f.srv.URL + "/verify", "expires_in": 60, "interval": 1})
	case "/oidc/token":
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") == "authorization_code" {
			f.codeGrant(w, r)
			return
		}
		if r.PostForm.Get("grant_type") == "client_credentials" {
			f.mu.Lock()
			f.tokens++
			f.mu.Unlock()
			exp := f.clock.Now().Add(time.Hour).Unix()
			payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"%s","exp":%d}`, r.PostForm.Get("client_id"), exp)))
			f.ok(w, map[string]string{"id_token": "e30." + payload + ".sig"})
			return
		}
		f.ok(w, map[string]string{"id_token": "idtok-alice"})
	case "/.well-known/straza/snapshot-keys.json":
		f.ok(w, map[string]any{"keys": []map[string]string{{"kid": "k1", "key": base64.StdEncoding.EncodeToString(f.snapK)}}})
	case "/v1/enroll":
		f.enroll(w, r)
	case "/v1/checkin":
		f.checkin(w, r)
	case "/v1/snapshot":
		f.mu.Lock()
		f.snapReqs++
		fail, msg, hold := f.snapFail, f.snapMsg, f.snapHold
		f.mu.Unlock()
		if hold != nil {
			<-hold
		}
		if _, ok := f.bound(w, r); !ok {
			return
		}
		if fail != 0 {
			f.fail(w, fail, msg)
			return
		}
		id := f.snapID()
		if r.Header.Get("If-None-Match") == `"`+id+`"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		f.mu.Lock()
		f.snaps++
		body := f.snap
		f.mu.Unlock()
		w.Header().Set("X-Straza-Snapshot-Id", id)
		_, _ = w.Write(body)
	case "/v1/echo":
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.echoes++
		f.echoBody = string(body)
		status := f.echoStatus
		f.mu.Unlock()
		if _, ok := f.bound(w, r); !ok {
			return
		}
		if status != 0 {
			f.fail(w, status, "refused by the test")
			return
		}
		f.ok(w, map[string]string{"echo": string(body)})
	case "/v1/grants/resolve", "/v1/grants":
		f.grants(w, r)
	case "/v1/decide":
		if _, ok := f.bound(w, r); ok {
			f.ok(w, map[string]any{"effect": "allow", "ruleId": "r1"})
		}
	case "/v1/audit/batch":
		f.mu.Lock()
		fail := f.auditFail
		f.mu.Unlock()
		if fail != 0 {
			f.fail(w, fail, "the audit store is down")
			return
		}
		if _, ok := f.bound(w, r); ok {
			var body struct {
				Events []json.RawMessage `json:"events"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.batches += len(body.Events)
			f.mu.Unlock()
			f.ok(w, map[string]int{"accepted": len(body.Events)})
		}
	case "/v1/push":
		f.mu.Lock()
		refuse := f.pushRefuse
		f.mu.Unlock()
		if refuse != 0 {
			f.fail(w, refuse, "session revoked")
			return
		}
		if _, ok := f.bound(w, r); ok {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: ready\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			select {
			case ev := <-f.pushEvents:
				if ev == "close" {
					return
				}
				_, _ = io.WriteString(w, "event: "+ev+"\ndata: {}\n\n")
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
			<-r.Context().Done()
		}
	default:
		http.NotFound(w, r)
	}
}

// expireSessions makes strazad forget every session token, as a restart
// with a session revoke does, so the next refresh answers 401.
func (f *fakeStrazad) expireSessions() {
	f.mu.Lock()
	f.sessions = map[string]fakeSession{}
	f.mu.Unlock()
}

// assertCounters checks that strazad accepted a strictly rising counter
// and revoked nothing.
func (f *fakeStrazad) assertCounters(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revoked != 0 {
		t.Fatalf("strazad revoked the device for key reuse: %v", f.reuse)
	}
	for i := 1; i < len(f.accepted); i++ {
		if f.accepted[i] <= f.accepted[i-1] {
			t.Fatalf("accepted counters do not rise strictly: %v", f.accepted)
		}
	}
}

// with runs fn under the fake's lock, so a test changes its behavior
// while handlers run.
func (f *fakeStrazad) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// serverURL returns the fake's base URL.
func (f *fakeStrazad) serverURL() string {
	u, _ := url.Parse(f.srv.URL)
	return u.String()
}
