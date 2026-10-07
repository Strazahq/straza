package clientassertion

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// TestMint pins the RFC 7523-style assertion shape the straza headless lane
// signs and the strazad issuer verifies: iss == sub == clientID, aud ==
// issuer URL, a non-empty jti, EdDSA signature by the NHI key, and a
// lifetime clamped to the shared contract bound (out-of-range ttl falls back
// to 2 minutes; never unsigned, never unbounded).
func TestMint(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		ttl     time.Duration
		wantTTL time.Duration
	}{
		{name: "explicit ttl inside the bound", ttl: time.Minute, wantTTL: time.Minute},
		{name: "zero ttl falls back to 2m", ttl: 0, wantTTL: 2 * time.Minute},
		{name: "negative ttl falls back to 2m", ttl: -time.Hour, wantTTL: 2 * time.Minute},
		{name: "over the contract bound falls back to 2m", ttl: MaxLifetime + time.Minute, wantTTL: 2 * time.Minute},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Mint(priv, "ci-bot", "https://strazad.example", tc.ttl)
			if err != nil {
				t.Fatal(err)
			}
			tok, err := jwt.Parse([]byte(raw),
				jwt.WithKey(jwa.EdDSA(), pub), jwt.WithValidate(true), jwt.WithAudience("https://strazad.example"))
			if err != nil {
				t.Fatalf("assertion does not verify with the NHI public key: %v", err)
			}
			iss, _ := tok.Issuer()
			sub, _ := tok.Subject()
			if iss != "ci-bot" || sub != "ci-bot" {
				t.Errorf("iss/sub = %q/%q, want the clientID in both", iss, sub)
			}
			jti, _ := tok.JwtID()
			if jti == "" {
				t.Error("assertion has no jti (replay cache would be blind)")
			}
			iat, ok1 := tok.IssuedAt()
			exp, ok2 := tok.Expiration()
			if !ok1 || !ok2 {
				t.Fatal("assertion missing iat/exp")
			}
			if got := exp.Sub(iat); got != tc.wantTTL {
				t.Errorf("lifetime = %s, want %s", got, tc.wantTTL)
			}
		})
	}

	t.Run("wrong key does not verify", func(t *testing.T) {
		otherPub, _, _ := ed25519.GenerateKey(nil)
		raw, err := Mint(priv, "ci-bot", "https://strazad.example", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := jwt.Parse([]byte(raw), jwt.WithKey(jwa.EdDSA(), otherPub), jwt.WithValidate(true)); err == nil {
			t.Error("assertion verified with the wrong key")
		}
	})

	t.Run("distinct jti per mint", func(t *testing.T) {
		a, _ := Mint(priv, "ci-bot", "https://strazad.example", time.Minute)
		b, _ := Mint(priv, "ci-bot", "https://strazad.example", time.Minute)
		ta, _ := jwt.Parse([]byte(a), jwt.WithKey(jwa.EdDSA(), pub), jwt.WithValidate(true), jwt.WithAudience("https://strazad.example"))
		tb, _ := jwt.Parse([]byte(b), jwt.WithKey(jwa.EdDSA(), pub), jwt.WithValidate(true), jwt.WithAudience("https://strazad.example"))
		ja, _ := ta.JwtID()
		jb, _ := tb.JwtID()
		if ja == jb {
			t.Error("two mints share a jti")
		}
	})
}
