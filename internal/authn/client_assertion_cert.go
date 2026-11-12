package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// clientAssertionCertValidity is how long the certificate of a client
// assertion key is valid, counted from the key's creation.
const clientAssertionCertValidity = 730 * 24 * time.Hour

// KeyCertificate is the self-signed X.509 certificate of one client assertion
// key, which an administrator uploads to an Entra agent blueprint so that
// Entra accepts the key's certificate assertions. Every replica builds the
// same bytes from the key, its id and its creation time, so nothing is
// stored.
type KeyCertificate struct {
	KID      string
	DER      []byte
	NotAfter time.Time

	thumbprint string // the x5t#S256 header value: base64url SHA-256 of DER
}

// PEM returns the certificate as one PEM CERTIFICATE block.
func (c KeyCertificate) PEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.DER})
}

// CertificateNotAfter is the end of validity of the certificate of a client
// assertion key created at created.
func CertificateNotAfter(created time.Time) time.Time {
	return created.UTC().Truncate(time.Second).Add(clientAssertionCertValidity)
}

// CertificateExpiredError says the certificate of the signing key KID expired
// at NotAfter, so an identity provider that matches the certificate would
// refuse the assertion, and nothing was signed.
type CertificateExpiredError struct {
	KID      string
	NotAfter time.Time
}

func (e *CertificateExpiredError) Error() string {
	return fmt.Sprintf("authn: the certificate of client assertion key %s expired at %s", e.KID, e.NotAfter.UTC().Format(time.RFC3339))
}

// buildCertificate makes the certificate of priv. Every field is set here
// and none is left to a default of crypto/x509, and RSA PKCS#1 v1.5
// signatures are deterministic, so the bytes depend only on the key, the kid
// and the creation time. The subject names the kid, the serial is the first
// 16 bytes of the kid's SHA-256 made positive, and the certificate is not a
// CA, which keeps x509 from adding key identifiers of its own choosing.
func buildCertificate(priv *rsa.PrivateKey, kid string, created time.Time) (KeyCertificate, error) {
	sum := sha256.Sum256([]byte(kid))
	serial := sum[:16]
	serial[0] &= 0x7f
	notAfter := CertificateNotAfter(created)
	tmpl := &x509.Certificate{
		SerialNumber:          new(big.Int).SetBytes(serial),
		Subject:               pkix.Name{CommonName: "Straza client assertion " + kid},
		NotBefore:             notAfter.Add(-clientAssertionCertValidity),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		SignatureAlgorithm:    x509.SHA256WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return KeyCertificate{}, fmt.Errorf("authn: build the certificate of client assertion key %s: %w", kid, err)
	}
	thumb := sha256.Sum256(der)
	return KeyCertificate{KID: kid, DER: der, NotAfter: notAfter, thumbprint: base64.RawURLEncoding.EncodeToString(thumb[:])}, nil
}

// Certificate returns the certificate of the client assertion key kid from
// the last good load, and reads a key that a peer staged after that load
// with one Reload. It answers ErrAssertionKeysStale on a replica that lost
// the store, a KeyStatusError for a retired key, store.ErrNotFound for an
// unknown kid and a WrongPurposeError for a key of another purpose.
func (s *ClientAssertionKeys) Certificate(ctx context.Context, now time.Time, kid string) (KeyCertificate, error) {
	st, err := s.fresh(now)
	if err != nil {
		return KeyCertificate{}, err
	}
	if c, ok := st.certs[kid]; ok {
		return c, nil
	}
	if err := s.Reload(ctx, now); err != nil {
		return KeyCertificate{}, err
	}
	if st, err = s.fresh(now); err != nil {
		return KeyCertificate{}, err
	}
	if c, ok := st.certs[kid]; ok {
		return c, nil
	}
	rec, _, err := s.find(ctx, kid)
	if err != nil {
		return KeyCertificate{}, err
	}
	return KeyCertificate{}, &KeyStatusError{KID: kid, Status: rec.Status}
}

// SignCertificateAssertion signs the client assertion of a Microsoft
// identity platform certificate credential for clientID, an Entra agent
// blueprint: iss and sub are the client id, aud is the token endpoint, nbf
// equals iat, and the header names the signing key's certificate by its
// x5t#S256 thumbprint and carries no kid, the shape the lab tenant accepted.
// It also returns the id of the key that signed, so a refusal names the
// certificate the blueprint must hold. The caller vouches for clientID. It
// refuses like SignAssertion, and with a CertificateExpiredError once the
// signing key's certificate has expired, before anything is signed.
func (s *ClientAssertionKeys) SignCertificateAssertion(now time.Time, clientID, audience string) (string, string, error) {
	if clientID == "" || audience == "" {
		return "", "", errors.New("authn: a certificate assertion needs a client id and an audience")
	}
	st, err := s.fresh(now)
	if err != nil {
		return "", "", err
	}
	if err := st.signable(); err != nil {
		return "", "", err
	}
	cert := st.certs[st.activeKID]
	if now.After(cert.NotAfter) {
		return "", "", &CertificateExpiredError{KID: st.activeKID, NotAfter: cert.NotAfter}
	}
	iat := now.UTC().Truncate(time.Second)
	tok, err := jwt.NewBuilder().
		Issuer(clientID).
		Subject(clientID).
		Audience([]string{audience}).
		JwtID(uuid.NewString()).
		IssuedAt(iat).
		NotBefore(iat).
		Expiration(iat.Add(ClientAssertionTTL)).
		Build()
	if err != nil {
		return "", "", fmt.Errorf("authn: build certificate assertion: %w", err)
	}
	hdr := jws.NewHeaders()
	if err := hdr.Set(jws.X509CertThumbprintS256Key, cert.thumbprint); err != nil {
		return "", "", fmt.Errorf("authn: set the certificate thumbprint: %w", err)
	}
	// The raw key, not the annotated JWK, so jwx adds no kid to the header.
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256(), st.activePriv, jws.WithProtectedHeaders(hdr)))
	if err != nil {
		return "", "", fmt.Errorf("authn: sign certificate assertion: %w", err)
	}
	return string(signed), st.activeKID, nil
}
