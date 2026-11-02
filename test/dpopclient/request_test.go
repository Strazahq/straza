package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/dpop"
)

// proofParts decodes the header and the claims of a proof without verifying
// its signature, which internal/dpop's own tests pin. It returns nil maps for
// a proof that does not decode.
func proofParts(proof string) (header, claims map[string]any) {
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		return nil, nil
	}
	for i, dst := range []*map[string]any{&header, &claims} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil || json.Unmarshal(raw, dst) != nil {
			return nil, nil
		}
	}
	return header, claims
}

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestBuildRequest(t *testing.T) {
	key := testKey(t)
	cases := []struct {
		name      string
		r         request
		wantAuth  string
		wantProof bool
		wantATH   bool
		wantCTR   float64
	}{
		{"call under DPoP", request{method: "POST", path: "/v1/decide?trace=1", body: []byte(`{}`), authToken: "tok", proofATH: "tok"}, "DPoP tok", true, true, 0},
		{"call under Bearer", request{method: "GET", path: "/v1/self", authToken: "tok", bearer: true}, "Bearer tok", false, false, 0},
		{"refresh with the token in the body", request{method: "POST", path: "/v1/checkin", body: []byte(`{}`), proofATH: "tok"}, "", true, true, 0},
		{"enroll", request{method: "POST", path: "/v1/enroll", body: []byte(`{}`)}, "", true, false, 0},
		{"key check-in signs its counter", request{method: "POST", path: "/v1/checkin", body: []byte(`{}`), counter: 42}, "", true, false, 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &state{Server: "http://127.0.0.1:8420/", Nonce: "n1"}
			c := newClient(st, key, &bytes.Buffer{})
			req, proof, err := c.build(tc.r, st.Nonce, "")
			if err != nil {
				t.Fatal(err)
			}
			if got := req.Header.Get("Authorization"); got != tc.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tc.wantAuth)
			}
			if got := req.Header.Get(dpop.HeaderProof); (got != "") != tc.wantProof || got != proof {
				t.Fatalf("DPoP header = %q, returned proof %q, want a proof: %v", got, proof, tc.wantProof)
			}
			if !tc.wantProof {
				return
			}
			header, claims := proofParts(proof)
			if header["typ"] != dpop.ProofType || header["alg"] != "EdDSA" || header["jwk"] == nil {
				t.Errorf("proof header = %v, want typ %s, alg EdDSA and a jwk", header, dpop.ProofType)
			}
			wantHTU := "http://127.0.0.1:8420" + strings.SplitN(tc.r.path, "?", 2)[0]
			if claims["htm"] != tc.r.method || claims["htu"] != wantHTU || claims["nonce"] != "n1" {
				t.Errorf("claims = %v, want htm %s, htu %s and nonce n1", claims, tc.r.method, wantHTU)
			}
			if ath, has := claims["ath"]; has != tc.wantATH || (has && ath != dpop.AccessTokenHash("tok")) {
				t.Errorf("ath = %v (present %v), want present %v and the hash of the token", ath, has, tc.wantATH)
			}
			if ctr, has := claims["ctr"]; has != (tc.wantCTR > 0) || (has && ctr != tc.wantCTR) {
				t.Errorf("ctr = %v (present %v), want %v", ctr, has, tc.wantCTR)
			}
		})
	}
}

// nonceServer answers like strazad's proof check: a proof without the nonce
// it handed out gets the nonce challenge, and every answer carries the next
// nonce.
// It returns the server and a func that lists the proofs it received.
func nonceServer(t *testing.T, challenge string) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu     sync.Mutex
		proofs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proofs = append(proofs, r.Header.Get(dpop.HeaderProof))
		mu.Unlock()
		_, claims := proofParts(r.Header.Get(dpop.HeaderProof))
		if claims["nonce"] != "n1" {
			w.Header().Set(dpop.HeaderNonce, "n1")
			w.Header().Set("WWW-Authenticate", `DPoP error="`+challenge+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Straza needs a fresh proof nonce."}`))
			return
		}
		w.Header().Set(dpop.HeaderNonce, "n2")
		_, _ = w.Write([]byte(`{"session_id":"s1","session_token":"secret-session-token"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), proofs...)
	}
}

func TestDoRetriesOnceOnTheNonceChallenge(t *testing.T) {
	cases := []struct {
		name       string
		challenge  string
		wantStatus int
		wantSent   int
		wantNonce  string
	}{
		{"nonce challenge signs again", "use_dpop_nonce", http.StatusOK, 2, "n2"},
		{"another proof refusal is final", "invalid_dpop_proof", http.StatusUnauthorized, 1, "n1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, received := nonceServer(t, tc.challenge)
			var out bytes.Buffer
			st := &state{Server: srv.URL}
			a, _, err := newClient(st, testKey(t), &out).do(request{method: "POST", path: "/v1/checkin", body: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			if sent := len(received()); a.status != tc.wantStatus || sent != tc.wantSent || st.Nonce != tc.wantNonce {
				t.Errorf("status %d after %d requests with nonce %q, want %d after %d with %q",
					a.status, sent, st.Nonce, tc.wantStatus, tc.wantSent, tc.wantNonce)
			}
			if strings.Contains(out.String(), "secret-session-token") {
				t.Errorf("the output carries the session token:\n%s", out.String())
			}
		})
	}
}

func TestCallReplaySendsTheSameProofTwice(t *testing.T) {
	var (
		mu            sync.Mutex
		proofs, auths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		proofs = append(proofs, r.Header.Get(dpop.HeaderProof))
		auths = append(auths, r.Header.Get("Authorization"))
		w.Header().Set(dpop.HeaderNonce, "n1")
		_, _ = w.Write([]byte(`{"effect":"allow"}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	keyPath, statePath := filepath.Join(dir, "key.pem"), filepath.Join(dir, "state.json")
	if err := writeKey(keyPath, testKey(t)); err != nil {
		t.Fatal(err)
	}
	if err := saveState(statePath, state{Server: srv.URL, DeviceID: "d1", SessionToken: "tok", Nonce: "n1"}); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"call", "-key", keyPath, "-state", statePath, "-method", "POST", "-path", "/v1/decide", "-body", `{}`, "-replay"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(proofs) != 2 || proofs[0] == "" || proofs[0] != proofs[1] {
		t.Fatalf("proofs = %q, want the same proof twice", proofs)
	}
	if auths[0] != "DPoP tok" || auths[1] != "DPoP tok" {
		t.Errorf("Authorization = %q, want DPoP tok twice", auths)
	}
}

func TestKeepEnrollment(t *testing.T) {
	cases := []struct {
		name    string
		a       answer
		wantErr string
	}{
		{"accepted key enrollment", answer{status: 200, body: []byte(`{"user_id":"u1","username":"kim","device_id":"d1","key_thumbprint":"tp","sign_in_expires_in":604800}`)}, ""},
		{"server older than key enrollment", answer{status: 200, body: []byte(`{"user_id":"u1","device_id":"d1","device_token":"dt","device_token_expires_in":2592000}`)}, errOldServer.Error()},
		{"another key bound", answer{status: 200, body: []byte(`{"device_id":"d1","key_thumbprint":"other"}`)}, "the server bound the key other"},
		{"key already enrolled", answer{status: 409, body: []byte(`{"error":"This key is already enrolled for you."}`)}, "refused with 409"},
		{"refused", answer{status: 403, body: []byte(`{"error":"no"}`)}, "refused with 403"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := state{Server: "http://s", Nonce: "n1", Counter: 7, SessionToken: "old"}
			err := keepEnrollment(&st, tc.a, "tp")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if st.DeviceID != "" {
					t.Errorf("a refused enrollment recorded the device %q", st.DeviceID)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := state{Server: "http://s", Nonce: "n1", UserID: "u1", Username: "kim", DeviceID: "d1", KeyThumbprint: "tp"}
			if st != want {
				t.Errorf("state = %+v, want %+v (counter 0 and no session after an enrollment)", st, want)
			}
		})
	}
}

// enrollServer answers a key enrollment the way strazad does when bind is
// set, with the thumbprint of the key in the proof's jwk header, and the way
// a server older than key enrollment does otherwise.
func enrollServer(t *testing.T, bind bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !bind {
			_, _ = w.Write([]byte(`{"user_id":"u1","device_id":"d1","device_token":"dt"}`))
			return
		}
		header, _ := proofParts(r.Header.Get(dpop.HeaderProof))
		jwk, _ := header["jwk"].(map[string]any)
		x, _ := jwk["x"].(string)
		pub, err := base64.RawURLEncoding.DecodeString(x)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"user_id": "u1", "username": "kim", "device_id": "d1",
			"key_thumbprint": dpop.Thumbprint(ed25519.PublicKey(pub)), "sign_in_expires_in": 604800})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnrollWritesANewKey(t *testing.T) {
	cases := []struct {
		name        string
		bind        bool
		wantErr     error
		wantNewKey  bool
		wantCounter int64
	}{
		{"the server binds the new key", true, nil, true, 0},
		{"a server older than key enrollment", false, errOldServer, false, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := enrollServer(t, tc.bind)
			dir := t.TempDir()
			keyPath, statePath := filepath.Join(dir, "key.pem"), filepath.Join(dir, "state.json")
			old := testKey(t)
			if err := writeKey(keyPath, old); err != nil {
				t.Fatal(err)
			}
			if err := saveState(statePath, state{Server: srv.URL, DeviceID: "d0", Counter: 7}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := run([]string{"enroll", "-server", srv.URL, "-id-token", "idt", "-key", keyPath, "-state", statePath,
				"-fingerprint", "fp", "-binding", "b"}, &out)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			key, err := loadKey(keyPath)
			if err != nil {
				t.Fatal(err)
			}
			if replaced := !key.Equal(old); replaced != tc.wantNewKey {
				t.Errorf("key file replaced = %v, want %v", replaced, tc.wantNewKey)
			}
			info, err := os.Stat(keyPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
			}
			st, err := loadState(statePath, false)
			if err != nil {
				t.Fatal(err)
			}
			if st.Counter != tc.wantCounter {
				t.Errorf("counter = %d, want %d", st.Counter, tc.wantCounter)
			}
			if tc.wantNewKey && st.KeyThumbprint != dpop.Thumbprint(key.Public().(ed25519.PublicKey)) {
				t.Errorf("state thumbprint %s is not the key file's", st.KeyThumbprint)
			}
			if strings.Contains(out.String(), "PRIVATE KEY") {
				t.Error("the output carries the key")
			}
		})
	}
}
