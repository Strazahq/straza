package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// newProofKey returns a fresh Ed25519 key for a test to bind tokens to.
func newProofKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// bindToken mints a session token with the claims of c, bound to key.
func bindToken(t *testing.T, app *App, c authn.Claims, key ed25519.PrivateKey) string {
	t.Helper()
	c.CNF = dpop.Thumbprint(key.Public().(ed25519.PublicKey))
	tok, _, err := app.tokens.Mint(c)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestSessionCredential pins how the Authorization header is read: the
// Bearer and DPoP schemes in any case, a token that is not empty, and
// bearerToken taking the Bearer scheme only.
func TestSessionCredential(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, header               string
		wantRaw, wantScheme, wantB string
	}{
		{"bearer", "Bearer tok", "tok", "Bearer", "tok"},
		{"bearer in any case", "bEARER tok", "tok", "Bearer", "tok"},
		{"dpop", "DPoP tok", "tok", "DPoP", ""},
		{"dpop in any case", "dpop tok", "tok", "DPoP", ""},
		{"no header", "", "", "", ""},
		{"a scheme with an empty token", "Bearer ", "", "", ""},
		{"a scheme alone", "DPoP", "", "", ""},
		{"another scheme", "Basic a2ltOmh1bnRlcjIh", "", "", ""},
		{"a word that only starts like a scheme", "Bearerx tok", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if raw, scheme := sessionCredential(r); raw != tc.wantRaw || scheme != tc.wantScheme {
				t.Errorf("sessionCredential = %q, %q; want %q, %q", raw, scheme, tc.wantRaw, tc.wantScheme)
			}
			if got := bearerToken(r); got != tc.wantB {
				t.Errorf("bearerToken = %q, want %q", got, tc.wantB)
			}
		})
	}
}

// TestProofOrigins pins the origins a proof's htu may name. The origin of
// server.publicUrl always counts, without its path. The origin the request
// arrived on counts only when no public URL is set, or when the public URL
// and the request both name a loopback host with the same scheme and port,
// as eval advertises localhost and its clients dial 127.0.0.1. A Host header
// is the sender's word, so it never adds an origin otherwise.
func TestProofOrigins(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, publicURL, target string
		tls                     bool
		want                    []string
	}{
		{"the eval pair: localhost advertised, 127.0.0.1 dialed", "http://localhost:8420", "http://127.0.0.1:8420", false,
			[]string{"http://localhost:8420", "http://127.0.0.1:8420"}},
		{"an IPv6 loopback dial of a localhost public URL", "http://localhost:8420/", "http://[::1]:8420", false,
			[]string{"http://localhost:8420", "http://[::1]:8420"}},
		{"loopback on the default port on both sides", "http://localhost", "http://127.0.0.2", false,
			[]string{"http://localhost", "http://127.0.0.2"}},
		{"a forged Host against a public URL that is not loopback", "https://straza.example/", "http://evil.example", false,
			[]string{"https://straza.example"}},
		{"a loopback Host against a public URL that is not loopback", "https://straza.example", "http://127.0.0.1:8420", false,
			[]string{"https://straza.example"}},
		{"a public loopback host against a Host that is not loopback", "http://localhost:8420", "http://evil.example:8420", false,
			[]string{"http://localhost:8420"}},
		{"loopback on another port", "http://localhost:8420", "http://127.0.0.1:9000", false,
			[]string{"http://localhost:8420"}},
		{"loopback under another scheme", "https://localhost:8420", "http://127.0.0.1:8420", false,
			[]string{"https://localhost:8420"}},
		{"tls and a public URL with a port and a path", "https://straza.example:8443/base", "https://127.0.0.1:8443", true,
			[]string{"https://straza.example:8443"}},
		{"no public URL takes the arrival origin", "", "http://127.0.0.1:8420", false, []string{"http://127.0.0.1:8420"}},
		{"no public URL under tls", "", "https://straza.example", true, []string{"https://straza.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &App{cfg: config.Config{Server: config.Server{PublicURL: tc.publicURL}}}
			r := httptest.NewRequest(http.MethodPost, tc.target+"/v1/decide", nil)
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if got := a.proofOrigins(r); !slices.Equal(got, tc.want) {
				t.Errorf("proofOrigins = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSessionProofReadsNoStore pins that the proof check reads no store on
// the data plane: a valid proof, a refused one with its record, and a stale
// nonce at decide, the gateway, push and the audit batch leave every
// request-path repo untouched.
func TestSessionProofReadsNoStore(t *testing.T) {
	t.Parallel()
	app, base, cs := testAppCounting(t)
	seedIdentity(t, app)
	tok, _ := checkinToken(t, app, base)
	claims, err := app.tokens.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	g := &proofRig{app: app, base: base, key: newProofKey(t)}
	bound := bindToken(t, app, claims, g.key)
	decideSite := proofSite{name: "decide", rig: g, method: http.MethodPost, path: "/v1/decide", ok: http.StatusOK,
		body: `{"event":{"kind":"tool.pre","tool":"shell.exec","command":"ls"}}`}
	sites := []proofSite{
		decideSite,
		{name: "gateway", rig: g, method: http.MethodPost, path: "/mcp", ok: http.StatusOK,
			body: `{"jsonrpc":"2.0","id":1,"method":"ping"}`},
		{name: "push", rig: g, method: http.MethodGet, path: "/v1/push", ok: http.StatusOK, stream: true},
	}

	before := cs.snapshot()
	for _, s := range sites {
		if ans := s.call(t, dpop.Scheme, bound, g.proof(t, g.key, s.method, s.path, g.nonce(), bound)); ans.status != s.ok {
			t.Errorf("%s with a valid proof = %d %q, want %d", s.name, ans.status, ans.err, s.ok)
		}
		if ans := s.call(t, schemeBearer, bound); ans.status != http.StatusUnauthorized || ans.err != proofCopiedTokenMsg {
			t.Errorf("%s under Bearer = %d %q, want the copied-token 401", s.name, ans.status, ans.err)
		}
	}
	audit := proofSite{name: "audit batch", rig: g, method: http.MethodPost, path: "/v1/audit/batch", body: `{"events":[]}`}
	if ans := audit.call(t, dpop.Scheme, bound); ans.status != http.StatusUnauthorized || ans.err != proofCopiedTokenMsg {
		t.Errorf("audit batch with no proof = %d %q, want the copied-token 401", ans.status, ans.err)
	}
	if ans := decideSite.call(t, dpop.Scheme, bound, g.proof(t, g.key, http.MethodPost, "/v1/decide", "", bound)); ans.err != proofNonceMsg {
		t.Errorf("decide with a stale nonce = %d %q, want the nonce 401", ans.status, ans.err)
	}
	assertNoRepoAccess(t, cs, "DPoP proof check on the data plane", before)
}

// TestSessionProofRecordQueued pins that a proof failure hands its record
// to the audit queue, as a decision's record goes, and writes nothing to the
// store while the request runs, so a slow store never holds a data-plane
// refusal. The record names the token's user, session and harness, the
// outcome and the reason, and carries no via or user.
func TestSessionProofRecordQueued(t *testing.T) {
	t.Parallel()
	queued := make(chan store.OutboxEvent, 256)
	var cs *countingStore
	app, base := testAppPreRun(t, []func(*App){func(a *App) {
		cs = &countingStore{inner: a.store}
		a.store = cs
		a.audit = newAuditSpool(false, a.log, func(_ context.Context, e store.OutboxEvent) error {
			select {
			case queued <- e:
			default:
			}
			return nil
		})
	}})
	seedIdentity(t, app)
	tok, _ := checkinToken(t, app, base)
	claims, err := app.tokens.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	claims.Harness = "proof-queue/1"
	g := &proofRig{app: app, base: base, key: newProofKey(t)}
	bound := bindToken(t, app, claims, g.key)
	decideSite := proofSite{name: "decide", rig: g, method: http.MethodPost, path: "/v1/decide",
		body: `{"event":{"kind":"tool.pre","tool":"shell.exec","command":"ls"}}`}

	before := cs.snapshot()
	outbox := cs.count("outbox")
	if ans := decideSite.call(t, schemeBearer, bound); ans.status != http.StatusUnauthorized || ans.err != proofCopiedTokenMsg {
		t.Fatalf("decide under Bearer = %d %q, want the copied-token 401", ans.status, ans.err)
	}
	assertNoRepoAccess(t, cs, "proof refusal", before)
	if got := cs.count("outbox"); got != outbox {
		t.Errorf("the proof refusal wrote the outbox on the request path (%d -> %d); its record belongs to the audit queue", outbox, got)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-queued:
			var ce struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(e.CE), &ce); err != nil || ce.Type != "straza.audit.authn" {
				continue
			}
			want := map[string]any{"action": "proof", "outcome": "failure", "userId": claims.Subject,
				"session": claims.Session, "harness": "proof-queue/1", "reason": "key-bound token sent under the Bearer scheme"}
			for k, v := range want {
				if ce.Data[k] != v {
					t.Errorf("record %s = %v, want %v (record %v)", k, ce.Data[k], v, ce.Data)
				}
			}
			for _, k := range []string{"via", "user"} {
				if _, ok := ce.Data[k]; ok {
					t.Errorf("record carries %s: %v", k, ce.Data)
				}
			}
			if ip, _ := ce.Data["sourceIp"].(string); ip == "" {
				t.Errorf("record carries no sourceIp: %v", ce.Data)
			}
			return
		case <-deadline:
			t.Fatal("the proof failure never reached the audit queue")
		}
	}
}
