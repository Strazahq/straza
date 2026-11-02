package authn

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// proofNow sits 30 s into its nonce window, so the previous window is
// proofNow minus 60 s and the window before that is stale.
var proofNow = time.Date(2026, 10, 20, 12, 0, 30, 0, time.UTC)

func testNonceKey(fill byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = fill + byte(i)
	}
	return k
}

func testProofKey(fill byte) ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = fill
	}
	return ed25519.NewKeyFromSeed(seed)
}

// signRaw signs header and claims as a compact JWS with priv, so a test can
// craft any header or claim the client package would never produce.
func signRaw(t *testing.T, priv ed25519.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(input)))
}

func publicJWKMap(pub ed25519.PublicKey) map[string]any {
	return map[string]any{"kty": "OKP", "crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(pub)}
}

// TestProofVerifierVerify walks every rule of the proof in the order Verify
// checks them. Each case starts from a proof that passes and changes one
// thing.
func TestProofVerifierVerify(t *testing.T) {
	key := testProofKey(7)
	pub := key.Public().(ed25519.PublicKey)
	other := testProofKey(9)
	const token = "session-token-under-test"
	v0 := NewProofVerifier(testNonceKey(1))
	current := v0.Nonce(proofNow)
	previous := v0.Nonce(proofNow.Add(-60 * time.Second))
	stale := v0.Nonce(proofNow.Add(-120 * time.Second))
	future := v0.Nonce(proofNow.Add(60 * time.Second))
	foreign := NewProofVerifier(testNonceKey(2)).Nonce(proofNow)

	baseCheck := func() ProofCheck {
		return ProofCheck{
			Method:      "POST",
			Path:        "/v1/decide",
			Origins:     []string{"https://straza.example", "http://127.0.0.1:8420"},
			AccessToken: token,
			Thumbprint:  dpop.Thumbprint(pub),
		}
	}
	baseHeader := func() map[string]any {
		return map[string]any{"typ": "dpop+jwt", "alg": "EdDSA", "jwk": publicJWKMap(pub)}
	}
	baseClaims := func() map[string]any {
		return map[string]any{
			"htm": "POST", "htu": "https://straza.example/v1/decide", "iat": proofNow.Unix(),
			"jti": "jti-under-test", "nonce": current, "ath": dpop.AccessTokenHash(token),
		}
	}

	cases := []struct {
		name       string
		header     func(h map[string]any)
		claims     func(c map[string]any)
		check      func(c *ProofCheck)
		signer     ed25519.PrivateKey
		raw        func(good string) string
		wantStale  bool
		wantReason string // empty means the proof passes
	}{
		{name: "a proof that follows every rule passes"},
		{name: "a nonce from the previous window passes", claims: func(c map[string]any) { c["nonce"] = previous }},
		{name: "htu origin is compared lower-cased with the default port dropped",
			claims: func(c map[string]any) { c["htu"] = "HTTPS://Straza.Example:443/v1/decide" }},
		{name: "htu query and fragment are ignored",
			claims: func(c map[string]any) { c["htu"] = "https://straza.example/v1/decide?a=1#frag" }},
		{name: "the origin the request arrived on is allowed",
			claims: func(c map[string]any) { c["htu"] = "http://127.0.0.1:8420/v1/decide" }},
		{name: "allowed origins are normalized too",
			claims: func(c map[string]any) { c["htu"] = "http://localhost/v1/decide" },
			check:  func(c *ProofCheck) { c.Origins = []string{"HTTP://LOCALHOST:80"} }},
		{name: "no access token and no ath passes",
			claims: func(c map[string]any) { delete(c, "ath") },
			check:  func(c *ProofCheck) { c.AccessToken = "" }},
		{name: "no wanted thumbprint accepts any key", signer: other,
			header: func(h map[string]any) { h["jwk"] = publicJWKMap(other.Public().(ed25519.PublicKey)) },
			check:  func(c *ProofCheck) { c.Thumbprint = "" }},

		{name: "a missing nonce is stale", claims: func(c map[string]any) { delete(c, "nonce") }, wantStale: true},
		{name: "an empty nonce is stale", claims: func(c map[string]any) { c["nonce"] = "" }, wantStale: true},
		{name: "a nonce two windows old is stale", claims: func(c map[string]any) { c["nonce"] = stale }, wantStale: true},
		{name: "a nonce from the next window is stale", claims: func(c map[string]any) { c["nonce"] = future }, wantStale: true},
		{name: "a nonce under another key is stale", claims: func(c map[string]any) { c["nonce"] = foreign }, wantStale: true},
		{name: "a nonce that is not base64url is stale", claims: func(c map[string]any) { c["nonce"] = "not a nonce!" }, wantStale: true},
		{name: "a nonce with one character changed is stale",
			claims: func(c map[string]any) { c["nonce"] = current[:len(current)-1] + flipChar(current[len(current)-1]) }, wantStale: true},
		{name: "a stale nonce is reported before a bad signature", signer: other,
			claims: func(c map[string]any) { c["nonce"] = stale }, wantStale: true},
		{name: "a stale nonce is reported before a wrong typ",
			header: func(h map[string]any) { h["typ"] = "JWT" },
			claims: func(c map[string]any) { c["nonce"] = stale }, wantStale: true},

		{name: "two segments are not a compact JWS", raw: func(good string) string { return good[:strings.LastIndex(good, ".")] },
			wantReason: "the proof is not a compact JWS"},
		{name: "a header that is not base64url", raw: func(good string) string { return "!!!" + good[strings.Index(good, "."):] },
			wantReason: "the proof header is malformed"},
		{name: "claims that are not JSON", raw: func(good string) string {
			parts := strings.Split(good, ".")
			return parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte("[1,2]")) + "." + parts[2]
		}, wantReason: "the proof claims are malformed"},
		{name: "typ must be dpop+jwt", header: func(h map[string]any) { h["typ"] = "JWT" }, wantReason: "typ is not dpop+jwt"},
		{name: "a missing typ is refused", header: func(h map[string]any) { delete(h, "typ") }, wantReason: "typ is not dpop+jwt"},
		{name: "alg must be EdDSA", header: func(h map[string]any) { h["alg"] = "ES256" }, wantReason: "alg is not EdDSA"},
		{name: "alg none is refused", header: func(h map[string]any) { h["alg"] = "none" }, wantReason: "alg is not EdDSA"},
		{name: "critical extensions are refused", header: func(h map[string]any) { h["crit"] = []string{"b64"}; h["b64"] = false },
			wantReason: "the proof header lists critical extensions"},
		{name: "a missing jwk is refused", header: func(h map[string]any) { delete(h, "jwk") }, wantReason: "the proof header carries no jwk"},
		{name: "an EC jwk is refused", header: func(h map[string]any) { h["jwk"].(map[string]any)["kty"] = "EC" },
			wantReason: "jwk is not an Ed25519 public key"},
		{name: "an X25519 jwk is refused", header: func(h map[string]any) { h["jwk"].(map[string]any)["crv"] = "X25519" },
			wantReason: "jwk is not an Ed25519 public key"},
		{name: "a short x is refused", header: func(h map[string]any) { h["jwk"].(map[string]any)["x"] = "AAAA" },
			wantReason: "jwk is not an Ed25519 public key"},
		{name: "a jwk that carries the private key is refused",
			header: func(h map[string]any) {
				h["jwk"].(map[string]any)["d"] = base64.RawURLEncoding.EncodeToString(key.Seed())
			}, wantReason: "jwk carries a private key"},
		{name: "a proof signed by another key is refused", signer: other, wantReason: "the signature does not verify"},
		{name: "a tampered signature is refused", raw: func(good string) string {
			return good[:len(good)-2] + flipChar(good[len(good)-2]) + good[len(good)-1:]
		}, wantReason: "the signature does not verify"},
		{name: "htm must equal the request method", claims: func(c map[string]any) { c["htm"] = "GET" },
			wantReason: "htm does not match the request method"},
		{name: "htm is case sensitive", claims: func(c map[string]any) { c["htm"] = "post" },
			wantReason: "htm does not match the request method"},
		{name: "a relative htu is refused", claims: func(c map[string]any) { c["htu"] = "/v1/decide" },
			wantReason: "htu is not an absolute URL"},
		{name: "an htu that does not parse is refused", claims: func(c map[string]any) { c["htu"] = "https://straza.example/%zz" },
			wantReason: "htu is not an absolute URL"},
		{name: "htu path must match exactly", claims: func(c map[string]any) { c["htu"] = "https://straza.example/v1/decide/" },
			wantReason: "htu path does not match the request"},
		{name: "htu path of another endpoint is refused", claims: func(c map[string]any) { c["htu"] = "https://straza.example/v1/checkin" },
			wantReason: "htu path does not match the request"},
		{name: "htu origin must be allowed", claims: func(c map[string]any) { c["htu"] = "https://evil.example/v1/decide" },
			wantReason: "htu origin is not this server"},
		{name: "htu scheme must match the origin", claims: func(c map[string]any) { c["htu"] = "http://straza.example/v1/decide" },
			wantReason: "htu origin is not this server"},
		{name: "htu port must match the origin", claims: func(c map[string]any) { c["htu"] = "https://straza.example:8443/v1/decide" },
			wantReason: "htu origin is not this server"},
		{name: "iat is required", claims: func(c map[string]any) { delete(c, "iat") }, wantReason: "iat is missing"},
		{name: "iat must be a number", claims: func(c map[string]any) { c["iat"] = "yesterday" }, wantReason: "iat is missing"},
		{name: "jti is required", claims: func(c map[string]any) { delete(c, "jti") }, wantReason: "jti is missing"},
		{name: "jti is bounded", claims: func(c map[string]any) { c["jti"] = strings.Repeat("j", 129) },
			wantReason: "jti is longer than 128 bytes"},
		{name: "ath is required with an access token", claims: func(c map[string]any) { delete(c, "ath") },
			wantReason: "ath is missing"},
		{name: "ath must hash the access token", claims: func(c map[string]any) { c["ath"] = dpop.AccessTokenHash("another-token") },
			wantReason: "ath does not match the access token"},
		{name: "ath without an access token is refused", check: func(c *ProofCheck) { c.AccessToken = "" },
			wantReason: "ath is present but the request carries no access token"},
		{name: "the proof key must have the wanted thumbprint", signer: other,
			header:     func(h map[string]any) { h["jwk"] = publicJWKMap(other.Public().(ed25519.PublicKey)) },
			wantReason: "the proof key is not the key this credential is bound to"},

		{name: "a check-in proof that signs the counter passes",
			claims: func(c map[string]any) { delete(c, "ath"); c["ctr"] = 5 },
			check:  func(c *ProofCheck) { c.AccessToken = ""; c.Counter = 5 }},
		{name: "ctr is required on a key check-in",
			claims:     func(c map[string]any) { delete(c, "ath") },
			check:      func(c *ProofCheck) { c.AccessToken = ""; c.Counter = 5 },
			wantReason: "ctr is missing"},
		{name: "ctr must equal the check-in counter",
			claims:     func(c map[string]any) { delete(c, "ath"); c["ctr"] = 4 },
			check:      func(c *ProofCheck) { c.AccessToken = ""; c.Counter = 5 },
			wantReason: "ctr does not match the check-in counter"},
		{name: "ctr as a string does not match",
			claims:     func(c map[string]any) { delete(c, "ath"); c["ctr"] = "5" },
			check:      func(c *ProofCheck) { c.AccessToken = ""; c.Counter = 5 },
			wantReason: "ctr does not match the check-in counter"},
		{name: "ctr as a fraction does not match",
			claims:     func(c map[string]any) { delete(c, "ath"); c["ctr"] = 5.5 },
			check:      func(c *ProofCheck) { c.AccessToken = ""; c.Counter = 5 },
			wantReason: "ctr does not match the check-in counter"},
		{name: "ctr on a request that is not a key check-in is refused",
			claims:     func(c map[string]any) { c["ctr"] = 5 },
			wantReason: "ctr is present but the request is not a key check-in"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, c, check := baseHeader(), baseClaims(), baseCheck()
			if tc.header != nil {
				tc.header(h)
			}
			if tc.claims != nil {
				tc.claims(c)
			}
			if tc.check != nil {
				tc.check(&check)
			}
			signer := key
			if tc.signer != nil {
				signer = tc.signer
			}
			raw := signRaw(t, signer, h, c)
			if tc.raw != nil {
				raw = tc.raw(signRaw(t, key, baseHeader(), baseClaims()))
			}

			v := NewProofVerifier(testNonceKey(1))
			got, err := v.Verify(raw, check, proofNow)
			switch {
			case tc.wantStale:
				if !errors.Is(err, ErrStaleNonce) {
					t.Fatalf("err = %v, want ErrStaleNonce", err)
				}
			case tc.wantReason != "":
				var pe *ProofError
				if !errors.As(err, &pe) || !strings.Contains(pe.Reason, tc.wantReason) {
					t.Fatalf("err = %v, want a ProofError with reason %q", err, tc.wantReason)
				}
				if errors.Is(err, ErrStaleNonce) {
					t.Fatal("a proof failure is reported as a stale nonce")
				}
				if !strings.HasPrefix(err.Error(), "authn: dpop proof rejected: ") {
					t.Errorf("error %q does not say a dpop proof was rejected", err)
				}
			default:
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				signerPub := signer.Public().(ed25519.PublicKey)
				if got.Thumbprint != dpop.Thumbprint(signerPub) || !got.PublicKey.Equal(signerPub) ||
					got.JWK != dpop.PublicJWK(signerPub) || got.JTI != c["jti"] {
					t.Errorf("proof = %+v, want the signer's key, its thumbprint and canonical JWK, and jti %v", got, c["jti"])
				}
			}
		})
	}
}

// flipChar returns a different base64url character than b.
func flipChar(b byte) string {
	if b == 'A' {
		return "B"
	}
	return "A"
}

// TestProofVerifierAcceptsClientProofs checks that what the client package
// signs is what the verifier accepts.
func TestProofVerifierAcceptsClientProofs(t *testing.T) {
	key := testProofKey(3)
	v := NewProofVerifier(testNonceKey(1))
	nonce := v.Nonce(proofNow)
	cases := []struct {
		name, method, htu, token string
		check                    ProofCheck
	}{
		{name: "check-in with no token", method: "POST", htu: "https://straza.example/v1/checkin",
			check: ProofCheck{Method: "POST", Path: "/v1/checkin", Origins: []string{"https://straza.example"}}},
		{name: "session request with a token", method: "GET", htu: "http://127.0.0.1:8420/v1/snapshot", token: "tok",
			check: ProofCheck{Method: "GET", Path: "/v1/snapshot", Origins: []string{"http://127.0.0.1:8420"}, AccessToken: "tok",
				Thumbprint: dpop.Thumbprint(key.Public().(ed25519.PublicKey))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := dpop.NewProof(key, tc.method, tc.htu, nonce, tc.token, proofNow)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := v.Verify(raw, tc.check, proofNow); err != nil {
				t.Fatalf("Verify of a client proof: %v", err)
			}
		})
	}
}

// TestProofVerifierSpendsJTI pins replay protection: a proof that passed is
// refused the second time, a proof that failed spends nothing, and a spent
// jti is forgotten after three minutes.
func TestProofVerifierSpendsJTI(t *testing.T) {
	key := testProofKey(4)
	pub := key.Public().(ed25519.PublicKey)
	check := ProofCheck{Method: "POST", Path: "/v1/checkin", Origins: []string{"https://straza.example"}}
	proof := func(v *ProofVerifier, htm, jti string, at time.Time) string {
		return signRaw(t, key, map[string]any{"typ": "dpop+jwt", "alg": "EdDSA", "jwk": publicJWKMap(pub)},
			map[string]any{"htm": htm, "htu": "https://straza.example/v1/checkin", "iat": at.Unix(), "jti": jti, "nonce": v.Nonce(at)})
	}

	steps := []struct {
		name       string
		htm, jti   string
		after      time.Duration
		wantReason string
	}{
		{name: "a failed proof spends nothing", htm: "GET", jti: "a", wantReason: "htm does not match"},
		{name: "the same jti then passes", htm: "POST", jti: "a"},
		{name: "a replay is refused", htm: "POST", jti: "a", wantReason: "jti was already used"},
		{name: "another jti passes", htm: "POST", jti: "b"},
		{name: "a replay just inside three minutes is refused", htm: "POST", jti: "a", after: 179 * time.Second,
			wantReason: "jti was already used"},
		{name: "the jti is free again after three minutes", htm: "POST", jti: "a", after: 180 * time.Second},
		{name: "and spent again", htm: "POST", jti: "a", after: 181 * time.Second, wantReason: "jti was already used"},
	}
	v := NewProofVerifier(testNonceKey(1))
	for _, s := range steps {
		at := proofNow.Add(s.after)
		_, err := v.Verify(proof(v, s.htm, s.jti, at), check, at)
		if s.wantReason == "" && err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if s.wantReason != "" && (err == nil || !strings.Contains(err.Error(), s.wantReason)) {
			t.Fatalf("%s: err = %v, want %q", s.name, err, s.wantReason)
		}
	}
}

// TestProofVerifierCallerSpends pins the key lanes: with CallerSpends set a
// passing proof is not spent in memory, because the caller spends it in the
// store once it knows who is calling, and nothing a caller does not spend
// grows the map. A data-plane check of the same proof still spends it.
func TestProofVerifierCallerSpends(t *testing.T) {
	key := testProofKey(6)
	v := NewProofVerifier(testNonceKey(1))
	raw, err := dpop.NewCheckinProof(key, "https://straza.example/v1/checkin", v.Nonce(proofNow), 3, proofNow)
	if err != nil {
		t.Fatal(err)
	}
	check := ProofCheck{Method: "POST", Path: "/v1/checkin", Origins: []string{"https://straza.example"}, Counter: 3, CallerSpends: true}

	steps := []struct {
		name         string
		callerSpends bool
		wantReason   string
		wantSpent    int
	}{
		{name: "the key lane passes the proof unspent", callerSpends: true},
		{name: "and again, because the caller spends it", callerSpends: true},
		{name: "a check that spends in memory passes once", wantSpent: 1},
		{name: "then refuses the replay", wantReason: "jti was already used", wantSpent: 1},
	}
	var jti string
	for _, s := range steps {
		check.CallerSpends = s.callerSpends
		p, err := v.Verify(raw, check, proofNow)
		if s.wantReason == "" && err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if s.wantReason != "" && (err == nil || !strings.Contains(err.Error(), s.wantReason)) {
			t.Fatalf("%s: err = %v, want %q", s.name, err, s.wantReason)
		}
		if err == nil && jti == "" {
			jti = p.JTI
		}
		if err == nil && p.JTI != jti {
			t.Fatalf("%s: jti %q, want %q", s.name, p.JTI, jti)
		}
		v.mu.Lock()
		spent := len(v.spent)
		v.mu.Unlock()
		if spent != s.wantSpent {
			t.Fatalf("%s: %d spent ids in memory, want %d", s.name, spent, s.wantSpent)
		}
	}
	if jti == "" {
		t.Fatal("the key lane got no jti to spend in the store")
	}
}

// TestProofVerifierSpendsJTIOnce races one proof on many goroutines: exactly
// one verification passes.
func TestProofVerifierSpendsJTIOnce(t *testing.T) {
	key := testProofKey(5)
	v := NewProofVerifier(testNonceKey(1))
	raw, err := dpop.NewProof(key, "POST", "https://straza.example/v1/checkin", v.Nonce(proofNow), "", proofNow)
	if err != nil {
		t.Fatal(err)
	}
	check := ProofCheck{Method: "POST", Path: "/v1/checkin", Origins: []string{"https://straza.example"}}
	var mu sync.Mutex
	passed := 0
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if _, err := v.Verify(raw, check, proofNow); err == nil {
				mu.Lock()
				passed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if passed != 1 {
		t.Fatalf("%d verifications of one proof passed, want 1", passed)
	}
}

func TestProofVerifierNonce(t *testing.T) {
	v := NewProofVerifier(testNonceKey(1))
	same := NewProofVerifier(testNonceKey(1))
	other := NewProofVerifier(testNonceKey(2))
	windowStart := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		a, b  string
		equal bool
	}{
		{"stable inside one window", v.Nonce(windowStart), v.Nonce(windowStart.Add(59 * time.Second)), true},
		{"changes at the next window", v.Nonce(windowStart), v.Nonce(windowStart.Add(60 * time.Second)), false},
		{"replicas with one key agree", v.Nonce(windowStart), same.Nonce(windowStart), true},
		{"another key gives another nonce", v.Nonce(windowStart), other.Nonce(windowStart), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if (tc.a == tc.b) != tc.equal {
				t.Errorf("nonces %q and %q: equal = %v, want %v", tc.a, tc.b, tc.a == tc.b, tc.equal)
			}
			if _, err := base64.RawURLEncoding.DecodeString(tc.a); err != nil {
				t.Errorf("nonce %q is not unpadded base64url: %v", tc.a, err)
			}
		})
	}
}

func TestNewProofVerifierRefusesShortKey(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewProofVerifier accepted a 16-byte nonce key")
		}
	}()
	NewProofVerifier(make([]byte, 16))
}

// openSettings opens one sqlite store at dsn, migrated, as one replica would.
func openSettings(t *testing.T, dsn string) store.SettingsRepo {
	t.Helper()
	s, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: dsn}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s.Settings()
}

// TestLoadOrCreateNonceKeySharedByReplicas opens two stores on one database,
// as two replicas do, and checks that both load the key the first one made.
func TestLoadOrCreateNonceKeySharedByReplicas(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "t.db")
	storetest.SeedSQLite(t, dsn)
	first, second := openSettings(t, dsn), openSettings(t, dsn)

	k1, err := LoadOrCreateNonceKey(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateNonceKey(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreateNonceKey(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != 32 || string(k1) != string(k2) || string(k1) != string(again) {
		t.Fatalf("keys differ or are not 32 bytes: %x, %x, %x", k1, k2, again)
	}
	stored, err := first.Get(ctx, NonceKeySetting)
	if err != nil || stored != base64.StdEncoding.EncodeToString(k1) {
		t.Fatalf("settings row = %q, %v, want the base64 key", stored, err)
	}
}

func TestLoadOrCreateNonceKeyFailsClosed(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		row     string
		wantErr string
	}{
		{"a row that is not base64", "not base64!", "does not hold a base64 32-byte key"},
		{"a row with a 16-byte key", base64.StdEncoding.EncodeToString(make([]byte, 16)), "does not hold a base64 32-byte key"},
		{"an empty row", "", "does not hold a base64 32-byte key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "t.db")
			storetest.SeedSQLite(t, dsn)
			settings := openSettings(t, dsn)
			if err := settings.Set(ctx, NonceKeySetting, tc.row); err != nil {
				t.Fatal(err)
			}
			key, err := LoadOrCreateNonceKey(ctx, settings)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("key %x, err %v; want an error containing %q", key, err, tc.wantErr)
			}
			if got, _ := settings.Get(ctx, NonceKeySetting); got != tc.row {
				t.Errorf("the bad row was rewritten to %q", got)
			}
		})
	}

	t.Run("an existing good row is kept", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "t.db")
		storetest.SeedSQLite(t, dsn)
		settings := openSettings(t, dsn)
		want := testNonceKey(40)
		if err := settings.Set(ctx, NonceKeySetting, base64.StdEncoding.EncodeToString(want)); err != nil {
			t.Fatal(err)
		}
		got, err := LoadOrCreateNonceKey(ctx, settings)
		if err != nil || string(got) != string(want) {
			t.Fatalf("key %x, err %v; want the stored key", got, err)
		}
	})

	for _, tc := range []struct {
		name string
		repo store.SettingsRepo
	}{
		{"a store that cannot write", failingSettings{failSet: true}},
		{"a store that cannot read", failingSettings{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if key, err := LoadOrCreateNonceKey(ctx, tc.repo); err == nil {
				t.Fatalf("key %x loaded from a failing store", key)
			}
		})
	}
}

// failingSettings fails SetIfAbsent when failSet is set and fails every Get.
type failingSettings struct {
	store.SettingsRepo
	failSet bool
}

func (f failingSettings) SetIfAbsent(context.Context, string, string) error {
	if f.failSet {
		return errors.New("disk full")
	}
	return nil
}

func (f failingSettings) Get(context.Context, string) (string, error) {
	return "", errors.New("connection refused")
}
