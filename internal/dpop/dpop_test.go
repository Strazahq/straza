package dpop

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

// rfc8037Seed and rfc8037X are the Ed25519 key of RFC 8037 appendix A.1.
const (
	rfc8037Seed = "nWGxne_9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A"
	rfc8037X    = "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"
)

func rfc8037Key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	seed, err := base64.RawURLEncoding.DecodeString(rfc8037Seed)
	if err != nil {
		t.Fatal(err)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestThumbprintAndJWK(t *testing.T) {
	priv := rfc8037Key(t)
	pub := priv.Public().(ed25519.PublicKey)
	if got := base64.RawURLEncoding.EncodeToString(pub); got != rfc8037X {
		t.Fatalf("RFC 8037 seed gives x %q, want %q", got, rfc8037X)
	}

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"RFC 8037 A.3 thumbprint", Thumbprint(pub), "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"},
		{"canonical JWK members in lexicographic order", PublicJWK(pub), `{"crv":"Ed25519","kty":"OKP","x":"` + rfc8037X + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestAccessTokenHash(t *testing.T) {
	cases := []struct {
		name, token, want string
	}{
		// RFC 9449 section 7.1 pairs this access token with this ath.
		{"RFC 9449 example", "Kz~8mXK1EalYznwH-LC-1fBAo.4Ljp~zsPE_NeO.gxU", "fUHyO2r2Z3DZ53EsNrWBb0xWXoaNy59IiKCAqksmQEo"},
		{"empty token still hashes", "", "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AccessTokenHash(tc.token); got != tc.want {
				t.Errorf("AccessTokenHash(%q) = %q, want %q", tc.token, got, tc.want)
			}
		})
	}
}

// decodeProof splits a compact proof into its header and claims and checks
// the signature under the header's jwk.
func decodeProof(t *testing.T, raw string) (header, claims map[string]any) {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("proof has %d parts, want 3", len(parts))
	}
	for i, dst := range []*map[string]any{&header, &claims} {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatalf("part %d is not base64url: %v", i, err)
		}
		if err := json.Unmarshal(b, dst); err != nil {
			t.Fatalf("part %d is not JSON: %v", i, err)
		}
	}
	jwk, _ := header["jwk"].(map[string]any)
	x, _ := jwk["x"].(string)
	pub, err := base64.RawURLEncoding.DecodeString(x)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("jwk x %q is not an Ed25519 public key", x)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("signature does not verify under the header jwk")
	}
	return header, claims
}

func TestNewProof(t *testing.T) {
	priv := rfc8037Key(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	jtiShape := regexp.MustCompile(`^[0-9a-f]{32}$`)

	cases := []struct {
		name        string
		method, htu string
		nonce       string
		token       string
	}{
		{name: "check-in with a nonce and no token", method: "POST", htu: "https://straza.example/v1/checkin", nonce: "n-1"},
		{name: "session request with a nonce and a token", method: "POST", htu: "https://straza.example/v1/decide", nonce: "n-2", token: "tok"},
		{name: "first request with neither", method: "GET", htu: "http://127.0.0.1:8420/v1/snapshot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := NewProof(priv, tc.method, tc.htu, tc.nonce, tc.token, now)
			if err != nil {
				t.Fatal(err)
			}
			header, claims := decodeProof(t, raw)

			if len(header) != 3 || header["typ"] != ProofType || header["alg"] != "EdDSA" {
				t.Errorf("header = %v, want exactly typ %s, alg EdDSA and jwk", header, ProofType)
			}
			jwk, _ := header["jwk"].(map[string]any)
			if len(jwk) != 3 || jwk["kty"] != "OKP" || jwk["crv"] != "Ed25519" || jwk["x"] != rfc8037X {
				t.Errorf("jwk = %v, want exactly kty OKP, crv Ed25519 and x", jwk)
			}

			if claims["htm"] != tc.method || claims["htu"] != tc.htu {
				t.Errorf("htm, htu = %v, %v", claims["htm"], claims["htu"])
			}
			if iat, _ := claims["iat"].(float64); int64(iat) != now.Unix() {
				t.Errorf("iat = %v, want %d", claims["iat"], now.Unix())
			}
			if jti, _ := claims["jti"].(string); !jtiShape.MatchString(jti) {
				t.Errorf("jti = %q, want 16 random bytes in hex", jti)
			}
			nonce, hasNonce := claims["nonce"]
			if hasNonce != (tc.nonce != "") || (hasNonce && nonce != tc.nonce) {
				t.Errorf("nonce = %v (present %v), want %q", nonce, hasNonce, tc.nonce)
			}
			ath, hasATH := claims["ath"]
			if hasATH != (tc.token != "") || (hasATH && ath != AccessTokenHash(tc.token)) {
				t.Errorf("ath = %v (present %v), want the hash of %q", ath, hasATH, tc.token)
			}
			wantClaims := 4
			if hasNonce {
				wantClaims++
			}
			if hasATH {
				wantClaims++
			}
			if len(claims) != wantClaims {
				t.Errorf("claims = %v, want only htm, htu, iat, jti, nonce and ath", claims)
			}
		})
	}
}

func TestNewProofFreshJTI(t *testing.T) {
	priv := rfc8037Key(t)
	now := time.Now()
	seen := map[string]bool{}
	for range 20 {
		raw, err := NewProof(priv, "POST", "https://straza.example/v1/checkin", "n", "", now)
		if err != nil {
			t.Fatal(err)
		}
		_, claims := decodeProof(t, raw)
		jti, _ := claims["jti"].(string)
		if seen[jti] {
			t.Fatalf("jti %q repeated", jti)
		}
		seen[jti] = true
	}
}

// TestNewCheckinProof pins the key check-in proof: htm POST, the counter
// signed as ctr, no ath, and a counter below 1 refused.
func TestNewCheckinProof(t *testing.T) {
	priv := rfc8037Key(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		counter int64
		wantErr bool
	}{
		{name: "the first check-in", counter: 1},
		{name: "a later check-in", counter: 9007199254740991},
		{name: "counter 0 is the enrollment, not a check-in", counter: 0, wantErr: true},
		{name: "a negative counter", counter: -3, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := NewCheckinProof(priv, "https://straza.example/v1/checkin", "n-1", tc.counter, now)
			if tc.wantErr {
				if err == nil {
					t.Fatal("NewCheckinProof signed a counter below 1")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, claims := decodeProof(t, raw)
			if claims["htm"] != "POST" || claims["htu"] != "https://straza.example/v1/checkin" || claims["nonce"] != "n-1" {
				t.Errorf("claims = %v, want htm POST and the given htu and nonce", claims)
			}
			if ctr, _ := claims["ctr"].(float64); int64(ctr) != tc.counter {
				t.Errorf("ctr = %v, want %d", claims["ctr"], tc.counter)
			}
			if _, ok := claims["ath"]; ok || len(claims) != 6 {
				t.Errorf("claims = %v, want only htm, htu, iat, jti, nonce and ctr", claims)
			}
		})
	}
}

func TestNewProofRejectsBadKey(t *testing.T) {
	if _, err := NewProof(ed25519.PrivateKey{1, 2, 3}, "POST", "https://straza.example/v1/checkin", "", "", time.Now()); err == nil {
		t.Fatal("NewProof signed with a key that is not an Ed25519 private key")
	}
}
