package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/dpop"
)

// seenRequest is what the test server saw of one request.
type seenRequest struct {
	auth   string
	body   string
	claims map[string]any
	thumb  string
}

// readProof verifies a proof's signature under its own jwk and returns its
// claims and key thumbprint.
func readProof(t *testing.T, raw string) (map[string]any, string) {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("proof %q is not a compact JWS", raw)
	}
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var hdr struct {
		Typ string `json:"typ"`
		JWK struct {
			X string `json:"x"`
		} `json:"jwk"`
	}
	var claims map[string]any
	if json.Unmarshal(hb, &hdr) != nil || json.Unmarshal(cb, &claims) != nil || hdr.Typ != dpop.ProofType {
		t.Fatalf("proof %q does not decode", raw)
	}
	pub, _ := base64.RawURLEncoding.DecodeString(hdr.JWK.X)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("proof signature does not verify")
	}
	return claims, dpop.Thumbprint(pub)
}

// TestCredProofTransport pins the DPoP transport: a proof per request with
// htm, htu from the server's origin without the query, the newest nonce,
// ath over a header or body token, ctr on a key check-in, one retry with
// the rewound body after the nonce challenge, and none for a key check-in.
func TestCredProofTransport(t *testing.T) {
	cases := []struct {
		name      string
		method    string
		auth      string // Authorization the caller sets
		bodyToken string // token named in the context
		counter   int64  // a key check-in when above 0
		body      string
		noRewind  bool   // the body has no GetBody
		cached    string // the nonce cached before the request
		wantSends int
		wantAuth  string
		wantATH   string
		wantCTR   float64
		wantCode  int
	}{
		{name: "bearer token becomes DPoP with ath", method: http.MethodGet, auth: "Bearer tok-1", cached: "n-good",
			wantSends: 1, wantAuth: "DPoP tok-1", wantATH: "tok-1", wantCode: http.StatusOK},
		{name: "DPoP token keeps its scheme", method: http.MethodPost, auth: "DPoP tok-2", body: `{"a":1}`, cached: "n-good",
			wantSends: 1, wantAuth: "DPoP tok-2", wantATH: "tok-2", wantCode: http.StatusOK},
		{name: "body token is hashed as ath", method: http.MethodPost, bodyToken: "tok-3", body: `{"session_token":"tok-3"}`, cached: "n-good",
			wantSends: 1, wantATH: "tok-3", wantCode: http.StatusOK},
		{name: "no token, no ath", method: http.MethodPost, body: `{}`, cached: "n-good", wantSends: 1, wantCode: http.StatusOK},
		{name: "nonce challenge retried with rewound body", method: http.MethodPost, body: `{"x":"y"}`,
			wantSends: 2, wantCode: http.StatusOK},
		{name: "unrewindable body replayed after challenge", method: http.MethodPost, body: `{"stream":true}`, noRewind: true,
			wantSends: 2, wantCode: http.StatusOK},
		{name: "key check-in signs ctr", method: http.MethodPost, counter: 7, body: `{"counter":7}`, cached: "n-good",
			wantSends: 1, wantCTR: 7, wantCode: http.StatusOK},
		{name: "key check-in is not retried", method: http.MethodPost, counter: 8, body: `{"counter":8}`,
			wantSends: 1, wantCTR: 8, wantCode: http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []seenRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				claims, thumb := readProof(t, r.Header.Get(dpop.HeaderProof))
				mu.Lock()
				seen = append(seen, seenRequest{auth: r.Header.Get("Authorization"), body: string(body), claims: claims, thumb: thumb})
				mu.Unlock()
				w.Header().Set(dpop.HeaderNonce, "n-good")
				if claims["nonce"] != "n-good" {
					w.Header().Set("WWW-Authenticate", `DPoP error="use_dpop_nonce"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			_, key, _ := ed25519.GenerateKey(rand.Reader)
			nonces := &nonceCache{now: time.Now}
			if tc.cached != "" {
				h := http.Header{}
				h.Set(dpop.HeaderNonce, tc.cached)
				nonces.keep(h)
			}
			pt := &proofTransport{base: http.DefaultTransport, key: key, origin: originOf(srv.URL + "/ignored/base/"), nonces: nonces}
			ctx := context.Background()
			if tc.bodyToken != "" {
				ctx = withBodyToken(ctx, tc.bodyToken)
			}
			att := &keyAttempt{counter: tc.counter, nonce: tc.cached}
			if tc.counter > 0 {
				ctx = withAttempt(ctx, att)
			}
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req, _ := http.NewRequestWithContext(ctx, tc.method, srv.URL+"/v1/thing?q=1", body)
			if tc.noRewind {
				req.GetBody = nil
				req.Body = io.NopCloser(strings.NewReader(tc.body))
			}
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			resp, err := pt.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.wantCode || len(seen) != tc.wantSends {
				t.Fatalf("status %d after %d sends, want %d after %d", resp.StatusCode, len(seen), tc.wantCode, tc.wantSends)
			}
			want := dpop.Thumbprint(key.Public().(ed25519.PublicKey))
			for i, s := range seen {
				if s.thumb != want || s.claims["htm"] != tc.method || s.claims["htu"] != srv.URL+"/v1/thing" || s.body != tc.body {
					t.Fatalf("send %d: thumb %s htm %v htu %v body %q", i, s.thumb, s.claims["htm"], s.claims["htu"], s.body)
				}
				if s.auth != tc.wantAuth {
					t.Fatalf("send %d: Authorization %q, want %q", i, s.auth, tc.wantAuth)
				}
				ath, has := s.claims["ath"]
				if (tc.wantATH == "") == has || (has && ath != dpop.AccessTokenHash(tc.wantATH)) {
					t.Fatalf("send %d: ath %v, want the hash of %q", i, ath, tc.wantATH)
				}
				if ctr, _ := s.claims["ctr"].(float64); ctr != tc.wantCTR {
					t.Fatalf("send %d: ctr %v, want %v", i, ctr, tc.wantCTR)
				}
			}
			if last := seen[len(seen)-1]; tc.wantCode == http.StatusOK && last.claims["nonce"] != "n-good" {
				t.Fatalf("the accepted send carried nonce %v", last.claims["nonce"])
			}
			if got, _ := nonces.get(); got != "n-good" {
				t.Fatalf("the newest nonce was not kept: %q", got)
			}
			if got, _ := seen[0].claims["nonce"].(string); tc.counter > 0 && (!att.sent.Load() || got != tc.cached) {
				t.Fatalf("key attempt: sent %v with nonce %q, want sent with the attempt's nonce %q", att.sent.Load(), got, tc.cached)
			}
		})
	}
}
