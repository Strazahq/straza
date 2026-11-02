// Package dpop is the client side of RFC 9449 proof of possession: the
// thumbprint of an Ed25519 key and the proof JWT a key holder signs for each
// request. It is a leaf like internal/clientassertion, so a client binary
// can import it without the server-side authn package and its store.
package dpop

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
)

// Header names and the Authorization scheme of RFC 9449.
const (
	HeaderProof = "DPoP"
	HeaderNonce = "DPoP-Nonce"
	Scheme      = "DPoP"
	ProofType   = "dpop+jwt"
)

// PublicJWK returns pub as canonical JWK JSON, the RFC 7638 input: the
// required members crv, kty and x in lexicographic order with no whitespace.
func PublicJWK(pub ed25519.PublicKey) string {
	return `{"crv":"Ed25519","kty":"OKP","x":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}`
}

// Thumbprint returns the RFC 7638 JWK thumbprint of pub: the unpadded
// base64url SHA-256 of {"crv":"Ed25519","kty":"OKP","x":"..."}.
func Thumbprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256([]byte(PublicJWK(pub)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AccessTokenHash returns the ath claim value for token: the unpadded
// base64url SHA-256 of its ASCII bytes.
func AccessTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// proofClaims is the claim set of a proof, in the order RFC 9449 lists it,
// plus ctr, the Straza key check-in counter.
type proofClaims struct {
	HTM   string `json:"htm"`
	HTU   string `json:"htu"`
	IAT   int64  `json:"iat"`
	JTI   string `json:"jti"`
	Nonce string `json:"nonce,omitempty"`
	ATH   string `json:"ath,omitempty"`
	CTR   int64  `json:"ctr,omitempty"`
}

// NewProof signs a proof for one request: header typ dpop+jwt, alg EdDSA and
// the public jwk; claims htm, htu, iat, a random jti, nonce when not empty,
// and ath when accessToken is not empty.
func NewProof(priv ed25519.PrivateKey, method, htu, nonce, accessToken string, now time.Time) (string, error) {
	c := proofClaims{HTM: method, HTU: htu, IAT: now.Unix(), Nonce: nonce}
	if accessToken != "" {
		c.ATH = AccessTokenHash(accessToken)
	}
	return sign(priv, c)
}

// NewCheckinProof signs the proof of a key check-in: htm POST, htu, iat, a
// random jti, nonce when not empty, and the check-in counter as ctr, so the
// counter in the body cannot be changed without the key. counter must be at
// least 1.
func NewCheckinProof(priv ed25519.PrivateKey, htu, nonce string, counter int64, now time.Time) (string, error) {
	if counter < 1 {
		return "", errors.New("dpop: a check-in counter starts at 1")
	}
	return sign(priv, proofClaims{HTM: "POST", HTU: htu, IAT: now.Unix(), Nonce: nonce, CTR: counter})
}

// sign adds a random jti to c and signs it with priv under the proof header.
func sign(priv ed25519.PrivateKey, c proofClaims) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", errors.New("dpop: the proof key is not an Ed25519 private key")
	}
	pub, err := jwk.Import(priv.Public())
	if err != nil {
		return "", fmt.Errorf("dpop: import public key: %w", err)
	}
	hdr := jws.NewHeaders()
	if err := hdr.Set(jws.TypeKey, ProofType); err != nil {
		return "", fmt.Errorf("dpop: set typ: %w", err)
	}
	if err := hdr.Set(jws.JWKKey, pub); err != nil {
		return "", fmt.Errorf("dpop: set jwk: %w", err)
	}

	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("dpop: entropy unavailable: %w", err)
	}
	c.JTI = hex.EncodeToString(jti)
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("dpop: encode claims: %w", err)
	}
	signed, err := jws.Sign(payload, jws.WithKey(jwa.EdDSA(), priv, jws.WithProtectedHeaders(hdr)))
	if err != nil {
		return "", fmt.Errorf("dpop: sign: %w", err)
	}
	return string(signed), nil
}
