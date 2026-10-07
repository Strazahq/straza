// Package clientassertion is the ONE contract for the RFC 7523-style client
// assertion the headless NHI lane uses: the straza client mints it with the
// local NHI key, and the strazad issuer verifies it. It is deliberately a
// leaf, importable by the client PEP without dragging in the server-side
// authn package, whose store dependency would embed database drivers into
// every endpoint agent binary.
package clientassertion

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// MaxLifetime bounds an assertion's validity window; the verify side rejects
// anything longer (replay-window discipline). Client and server read this
// one constant, so the contract cannot drift.
const MaxLifetime = 5 * time.Minute

// Mint builds the assertion the straza headless enroll/checkin lane signs
// with its local NHI key: iss == sub == clientID, aud == the issuer URL, a
// fresh jti for the server's replay cache. An out-of-range ttl falls back to
// 2 minutes; never unsigned, never unbounded.
func Mint(key ed25519.PrivateKey, clientID, issuerURL string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > MaxLifetime {
		ttl = 2 * time.Minute
	}
	iat := time.Now().UTC().Truncate(time.Second)
	tok, err := jwt.NewBuilder().
		Issuer(clientID).Subject(clientID).Audience([]string{issuerURL}).
		IssuedAt(iat).Expiration(iat.Add(ttl)).JwtID(newJTI()).Build()
	if err != nil {
		return "", fmt.Errorf("clientassertion: build: %w", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
	if err != nil {
		return "", fmt.Errorf("clientassertion: sign: %w", err)
	}
	return string(signed), nil
}

// newJTI returns 16 bytes of hex entropy, the replay-cache key.
func newJTI() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("clientassertion: entropy unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}
