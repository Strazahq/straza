package server

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// proofRig is one running strazad that the verify-site table fires at, and
// the key its bound tokens name.
type proofRig struct {
	app   *App
	base  string
	key   ed25519.PrivateKey
	other ed25519.PrivateKey // a key no token is bound to
}

// nonce returns the rig's current DPoP nonce.
func (g *proofRig) nonce() string { return g.app.proofs.Nonce(time.Now()) }

// proof signs a DPoP proof with key for method and path on the rig's
// origin, with nonce and ath over token.
func (g *proofRig) proof(t *testing.T, key ed25519.PrivateKey, method, path, nonce, token string) string {
	t.Helper()
	p, err := dpop.NewProof(key, method, g.base+path, nonce, token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// proofSite is one verify site of a session token as a client reaches it.
type proofSite struct {
	name         string
	rig          *proofRig
	claims       authn.Claims // the session a working request names
	method, path string
	body         string
	stream       bool // the answer is an event stream: read the head and close
	ok           int  // the status of a request that passes the token check
	// host is the Host header to send, "" for the host the client dials.
	host string
	// bodyToken says the token rides in the check-in body, as on refresh,
	// so the rows about the Authorization scheme do not apply.
	bodyToken bool
	// refusal is the site's own answer to a token without cnf under the
	// DPoP scheme.
	refusal string
}

// proofAnswer is what a verify site answered.
type proofAnswer struct {
	status int
	header http.Header
	err    string
}

// call sends one request to s with token under scheme and the given DPoP
// headers.
func (s proofSite) call(t *testing.T, scheme, token string, proofs ...string) proofAnswer {
	t.Helper()
	body := s.body
	if s.bodyToken {
		body = fmt.Sprintf(`{"session_token":%q,"attestation":{"managed":false,"hashes":{}}}`, token)
	}
	var rd io.Reader = http.NoBody
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(s.method, s.rig.base+s.path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if s.host != "" {
		req.Host = s.host
	}
	if !s.bodyToken {
		req.Header.Set("Authorization", scheme+" "+token)
	}
	for _, p := range proofs {
		req.Header.Add(dpop.HeaderProof, p)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", s.method, s.path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	ans := proofAnswer{status: resp.StatusCode, header: resp.Header}
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		ans.err = e.Error
	}
	return ans
}

// proofRow is one way a client presents a session token.
type proofRow struct {
	name  string
	bound bool // the token carries cnf
	// scheme is the Authorization scheme.
	scheme string
	// proofs builds the DPoP headers for token; nil sends none.
	proofs func(t *testing.T, s proofSite, token string) []string
	// replay sends the same request once before the checked one.
	replay bool
	// header marks a row about the Authorization scheme.
	header bool
	// host is the Host header the row sends, "" for the dialed one.
	host   string
	want   string // ok, copied, nonce or site
	reason string // the proof record's reason, "" for no record
}

// proofRows are the presentations every verify site is fired with.
func proofRows() []proofRow {
	valid := func(t *testing.T, s proofSite, tok string) []string {
		return []string{s.rig.proof(t, s.rig.key, s.method, s.path, s.rig.nonce(), tok)}
	}
	return []proofRow{
		{name: "a token without cnf under Bearer works as today", scheme: schemeBearer, want: "ok"},
		{name: "a cnf token with a valid proof under DPoP works", bound: true, scheme: dpop.Scheme, proofs: valid, want: "ok"},
		{name: "a cnf token under Bearer is refused", bound: true, scheme: schemeBearer, proofs: valid, header: true,
			want: "copied", reason: "key-bound token sent under the Bearer scheme"},
		{name: "a cnf token with no proof is refused", bound: true, scheme: dpop.Scheme,
			want: "copied", reason: "no DPoP proof"},
		{name: "a cnf token with two proofs is refused", bound: true, scheme: dpop.Scheme,
			proofs: func(t *testing.T, s proofSite, tok string) []string {
				return append(valid(t, s, tok), valid(t, s, tok)...)
			}, want: "copied", reason: "more than one DPoP proof"},
		{name: "a proof for another path is refused", bound: true, scheme: dpop.Scheme,
			proofs: func(t *testing.T, s proofSite, tok string) []string {
				return []string{s.rig.proof(t, s.rig.key, s.method, "/v1/elsewhere", s.rig.nonce(), tok)}
			}, want: "copied", reason: "htu path does not match the request"},
		{name: "a proof for another method is refused", bound: true, scheme: dpop.Scheme,
			proofs: func(t *testing.T, s proofSite, tok string) []string {
				other := http.MethodPut
				if s.method == other {
					other = http.MethodPatch
				}
				return []string{s.rig.proof(t, s.rig.key, other, s.path, s.rig.nonce(), tok)}
			}, want: "copied", reason: "htm does not match the request method"},
		{name: "a proof for a forged Host's origin is refused", bound: true, scheme: dpop.Scheme, host: "evil.example",
			proofs: func(t *testing.T, s proofSite, tok string) []string {
				p, err := dpop.NewProof(s.rig.key, s.method, "http://evil.example"+s.path, s.rig.nonce(), tok, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				return []string{p}
			}, want: "copied", reason: "htu origin is not this server"},
		{name: "a proof from another key is refused", bound: true, scheme: dpop.Scheme,
			proofs: func(t *testing.T, s proofSite, tok string) []string {
				return []string{s.rig.proof(t, s.rig.other, s.method, s.path, s.rig.nonce(), tok)}
			}, want: "copied", reason: "the proof key is not the key this credential is bound to"},
		{name: "a proof with a wrong ath is refused", bound: true, scheme: dpop.Scheme,
			proofs: func(t *testing.T, s proofSite, _ string) []string {
				return []string{s.rig.proof(t, s.rig.key, s.method, s.path, s.rig.nonce(), "another-token")}
			}, want: "copied", reason: "ath does not match the access token"},
		{name: "a replayed proof is refused", bound: true, scheme: dpop.Scheme, proofs: valid, replay: true,
			want: "copied", reason: proofReplayReason},
		{name: "a stale nonce gets the nonce answer and no record", bound: true, scheme: dpop.Scheme,
			proofs: func(t *testing.T, s proofSite, tok string) []string {
				return []string{s.rig.proof(t, s.rig.key, s.method, s.path, "", tok)}
			}, want: "nonce"},
		{name: "a token without cnf under DPoP gets the site's own refusal", scheme: dpop.Scheme, proofs: valid,
			header: true, want: "site"},
	}
}

// proofReplayReason is the verifier's reason for a proof id it has seen.
const proofReplayReason = "jti was already used"

// proofExpect is a proof record a row wants, or wants absent.
type proofExpect struct {
	rig    *proofRig
	tag    string
	claims authn.Claims
	reason string
}

// TestSessionProofVerifySites fires every verify site of a session token
// with every way of presenting one: a bearer token as today, a bound token
// with a valid proof, and each way a proof can be missing or wrong. A
// refusal answers 401 with the copied-token sentence and writes exactly one
// proof record that names the token's user, session and harness and a
// fixed reason. A stale nonce answers the nonce challenge, which the client
// then signs with, and writes no record. Every answer to a request that
// carried a DPoP header brings a DPoP-Nonce, and no other answer does.
func TestSessionProofVerifySites(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	std := &proofRig{app: app, base: base, key: newProofKey(t), other: newProofKey(t)}
	ent, entBase := testApp(t, enterpriseProfile)
	entRig := &proofRig{app: ent, base: entBase, key: std.key, other: std.other}

	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	session := func() authn.Claims {
		code, out := postJSON(t, base+"/v1/checkin", map[string]any{
			"id_token":    idToken,
			"harness":     map[string]string{"name": "console", "version": "2.1.0"},
			"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
		})
		tok, _ := out["session_token"].(string)
		c, err := app.tokens.Verify(tok)
		if code != http.StatusOK || err != nil {
			t.Fatalf("checkin = %d %v (%v)", code, out, err)
		}
		return c
	}
	const tokenRejected = "session token rejected"
	sites := []proofSite{
		{name: "decide", rig: std, claims: session(), method: http.MethodPost, path: "/v1/decide",
			body: `{"event":{"kind":"tool.pre","tool":"shell.exec","command":"ls"}}`, ok: http.StatusOK, refusal: tokenRejected},
		{name: "gateway", rig: std, claims: session(), method: http.MethodPost, path: "/mcp",
			body: `{"jsonrpc":"2.0","id":1,"method":"ping"}`, ok: http.StatusOK, refusal: tokenRejected},
		{name: "gateway stream", rig: std, claims: session(), method: http.MethodGet, path: "/mcp", stream: true,
			ok: http.StatusOK, refusal: tokenRejected},
		{name: "push", rig: std, claims: session(), method: http.MethodGet, path: "/v1/push", stream: true,
			ok: http.StatusOK, refusal: tokenRejected},
		{name: "audit batch", rig: std, claims: session(), method: http.MethodPost, path: "/v1/audit/batch",
			body: `{"events":[]}`, ok: http.StatusOK, refusal: tokenRejected},
		{name: "snapshot", rig: entRig, claims: authn.Claims{Subject: "user-1", Session: "ses-proof", Device: "dev-1"},
			method: http.MethodGet, path: "/v1/snapshot", ok: http.StatusOK, refusal: snapshotBadSessionMsg},
		{name: "connect", rig: std, claims: session(), method: http.MethodGet, path: "/v1/connect",
			ok: http.StatusOK, refusal: tokenRejected},
		{name: "approvals", rig: std, claims: session(), method: http.MethodGet, path: "/v1/self", ok: http.StatusOK,
			refusal: "token rejected: this route needs a person's session or ID token; an admin API token carries no user and cannot decide"},
		{name: "self revoke", rig: std, claims: session(), method: http.MethodPost, path: "/v1/session/revoke",
			ok: http.StatusOK, refusal: notSessionTokenMsg},
		{name: "admin", rig: std, claims: session(), method: http.MethodGet, path: "/v1/admin/policies", ok: http.StatusOK,
			refusal: "token rejected: /v1/admin takes an admin session or ID token, or an admin API token (strazactl api-token create)"},
		{name: "refresh", rig: std, claims: keyedSession(t, app, kim.ID, std.key), method: http.MethodPost,
			path: "/v1/checkin", ok: http.StatusOK, bodyToken: true},
	}

	var expects []proofExpect
	for i, s := range sites {
		for j, row := range proofRows() {
			if row.header && s.bodyToken {
				continue
			}
			// Each row gets its own copy of the site, so its Host stays with it.
			s := s
			s.host = row.host
			t.Run(s.name+"/"+row.name, func(t *testing.T) {
				tag := fmt.Sprintf("proof-%d-%d/1", i, j)
				c := s.claims
				c.Harness = tag
				tok := bindToken(t, s.rig.app, c, s.rig.key)
				if !row.bound {
					var err error
					if tok, _, err = s.rig.app.tokens.Mint(c); err != nil {
						t.Fatal(err)
					}
				}
				var proofs []string
				if row.proofs != nil {
					proofs = row.proofs(t, s, tok)
				}
				if row.replay {
					if first := s.call(t, row.scheme, tok, proofs...); first.status != s.ok {
						t.Fatalf("the first use of the proof = %d %q, want %d", first.status, first.err, s.ok)
					}
				}
				ans := s.call(t, row.scheme, tok, proofs...)
				checkProofAnswer(t, s, row, ans, len(proofs) > 0)
				if row.want == "nonce" {
					retry := []string{s.rig.proof(t, s.rig.key, s.method, s.path, ans.header.Get(dpop.HeaderNonce), tok)}
					if again := s.call(t, row.scheme, tok, retry...); again.status != s.ok {
						t.Errorf("a proof signed with the answered nonce = %d %q, want %d", again.status, again.err, s.ok)
					}
				}
				expects = append(expects, proofExpect{rig: s.rig, tag: tag, claims: c, reason: row.reason})
			})
		}
	}
	checkProofRecords(t, sites, expects)
}

// checkProofAnswer checks one answer of s to row.
func checkProofAnswer(t *testing.T, s proofSite, row proofRow, ans proofAnswer, sentProof bool) {
	t.Helper()
	challenge := ans.header.Get("WWW-Authenticate")
	switch row.want {
	case "ok":
		if ans.status != s.ok {
			t.Errorf("answer = %d %q, want %d", ans.status, ans.err, s.ok)
		}
	case "copied":
		if ans.status != http.StatusUnauthorized || ans.err != proofCopiedTokenMsg || challenge != `DPoP error="invalid_dpop_proof"` {
			t.Errorf("answer = %d %q %q, want the copied-token 401", ans.status, ans.err, challenge)
		}
	case "nonce":
		if ans.status != http.StatusUnauthorized || ans.err != proofNonceMsg || challenge != `DPoP error="use_dpop_nonce"` {
			t.Errorf("answer = %d %q %q, want the nonce 401", ans.status, ans.err, challenge)
		}
	case "site":
		if ans.status != http.StatusUnauthorized || ans.err != s.refusal {
			t.Errorf("answer = %d %q, want the site's own 401 %q", ans.status, ans.err, s.refusal)
		}
	}
	if got := ans.header.Get(dpop.HeaderNonce); (got != "") != sentProof {
		t.Errorf("DPoP-Nonce = %q on a request that sent a proof: %v", got, sentProof)
	}
}

// checkProofRecords waits for a fence record on each rig, so every record
// the rows wrote has reached the chain, and then wants exactly one proof
// record for each refused row and none for the others.
func checkProofRecords(t *testing.T, sites []proofSite, expects []proofExpect) {
	t.Helper()
	fenced := map[*proofRig]bool{}
	for _, s := range sites {
		if fenced[s.rig] || s.bodyToken {
			continue
		}
		fenced[s.rig] = true
		c := s.claims
		c.Harness = "proof-fence/1"
		if ans := s.call(t, schemeBearer, bindToken(t, s.rig.app, c, s.rig.key)); ans.err != proofCopiedTokenMsg {
			t.Fatalf("fence on %s = %d %q", s.name, ans.status, ans.err)
		}
		waitAuthn(t, s.rig.app, "the fence proof record", func(d map[string]any) bool {
			return d["action"] == "proof" && d["harness"] == "proof-fence/1"
		})
	}
	for _, e := range expects {
		var got []map[string]any
		for _, d := range authnData(t, e.rig.app) {
			if d["action"] == "proof" && d["harness"] == e.tag {
				got = append(got, d)
			}
		}
		want := 0
		if e.reason != "" {
			want = 1
		}
		if len(got) != want {
			t.Errorf("%s: %d proof records, want %d: %v", e.tag, len(got), want, got)
			continue
		}
		if want == 0 {
			continue
		}
		d := got[0]
		if d["outcome"] != "failure" || d["userId"] != e.claims.Subject || d["session"] != e.claims.Session || d["reason"] != e.reason {
			t.Errorf("%s: record %v, want outcome failure, userId %s, session %s, reason %q",
				e.tag, d, e.claims.Subject, e.claims.Session, e.reason)
		}
	}
}

// keyedSession seeds a device enrolled with key and an active session on
// it for userID, as the key lane's check-in leaves them, and returns the
// session's claims.
func keyedSession(t *testing.T, app *App, userID string, key ed25519.PrivateKey) authn.Claims {
	t.Helper()
	ctx := context.Background()
	pub := key.Public().(ed25519.PublicKey)
	dev, err := app.store.Devices().Create(ctx, store.Device{
		UserID: userID, Name: "kim-key", Platform: "linux", Fingerprint: "sha256:proof-key",
		Status: "active", ClientKind: store.DeviceClientKit,
		KeyJWK:        fmt.Sprintf(`{"crv":"Ed25519","kty":"OKP","x":%q}`, base64.RawURLEncoding.EncodeToString(pub)),
		KeyThumbprint: dpop.Thumbprint(pub), Custody: `{"kind":"user","platform":"linux","version":"1"}`,
		MachineBinding: "proof-machine", KeyCounter: 1, SignedInAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := app.store.Sessions().Create(ctx, store.Session{
		UserID: userID, DeviceID: dev.ID, HarnessName: "straza", HarnessVersion: "1",
		AttestationLevel: "none", AttestationHashes: "{}", Status: store.SessionActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	return authn.Claims{Subject: userID, Session: ses.ID, Device: dev.ID, Attestation: "none"}
}
