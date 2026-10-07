// Package authn implements Straza's authentication plane: session tokens
// with rotating ed25519 signing keys and JWKS discovery, the built-in OIDC
// issuer, and the external OIDC client.
package authn

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/store"
)

// DefaultTokenTTL is the session-token lifetime, 300 s. Session tokens
// validate themselves, so a short lifetime plus pushed revocation bounds
// how long a revoked session keeps working.
const DefaultTokenTTL = 300 * time.Second

// Claims is the Straza session-token claim set (spec/session-token).
type Claims struct {
	Subject     string // user id
	Session     string // session id
	Device      string // device id ("" when no device factor)
	Harness     string // "<name>/<version>"
	Attestation string // managed|advisory|none
	RolesHash   string // deterministic hash of the sorted resolved role ids
	Snapshot    string // active policy snapshot id
	JTI         string // set by Mint
	IssuedAt    time.Time
	Expiry      time.Time
}

// RolesHash returns the canonical `rol` claim value: sha256 over the sorted
// role ids, hex-encoded and truncated to 16 bytes worth. Order-insensitive.
func RolesHash(roleIDs []string) string {
	sorted := make([]string, len(roleIDs))
	copy(sorted, roleIDs)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:16])
}

// TokenService mints and verifies session tokens against rotating ed25519
// keys persisted in the store. Verification is purely in-memory, with no I/O
// on the request path. The store is touched only at startup, at the reload
// tick and at rotation (rotation.go).
type TokenService struct {
	repo   store.SigningKeyRepo
	issuer string
	ttl    time.Duration

	mu     sync.RWMutex
	active jwk.Key // private key, signs new tokens
	verify jwk.Set // public keys: staged + active + retiring
}

// NewTokenService loads (or creates) the signing keys and returns a ready
// service. issuer is embedded and enforced in every token.
func NewTokenService(ctx context.Context, repo store.SigningKeyRepo, issuer string, ttl time.Duration) (*TokenService, error) {
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	s := &TokenService{repo: repo, issuer: issuer, ttl: ttl}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Mint issues a signed session token for the given claims. JTI, IssuedAt and
// Expiry are assigned here. The JTI is derived from the session id so
// revocation by session maps 1:1 onto token families.
func (s *TokenService) Mint(c Claims) (string, Claims, error) {
	c.JTI = uuid.NewString()
	c.IssuedAt = time.Now().UTC().Truncate(time.Second)
	c.Expiry = c.IssuedAt.Add(s.ttl)

	tok, err := jwt.NewBuilder().
		Issuer(s.issuer).
		Subject(c.Subject).
		JwtID(c.JTI).
		IssuedAt(c.IssuedAt).
		Expiration(c.Expiry).
		Claim("ses", c.Session).
		Claim("dev", c.Device).
		Claim("hrn", c.Harness).
		Claim("att", c.Attestation).
		Claim("rol", c.RolesHash).
		Claim("snp", c.Snapshot).
		Build()
	if err != nil {
		return "", Claims{}, fmt.Errorf("authn: build token: %w", err)
	}

	s.mu.RLock()
	key := s.active
	s.mu.RUnlock()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
	if err != nil {
		return "", Claims{}, fmt.Errorf("authn: sign token: %w", err)
	}
	return string(signed), c, nil
}

// Verify parses and validates a session token: EdDSA signature against a
// known non-retired key, issuer match, exp/iat with 30 s skew. It does NOT
// consult revocation state; that is the caller's denylist check.
func (s *TokenService) Verify(raw string) (Claims, error) {
	s.mu.RLock()
	set := s.verify
	s.mu.RUnlock()

	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKeySet(set),
		jwt.WithIssuer(s.issuer),
		jwt.WithAcceptableSkew(30*time.Second),
	)
	if err != nil {
		return Claims{}, fmt.Errorf("authn: token rejected: %w", err)
	}

	var c Claims
	sub, _ := tok.Subject()
	c.Subject = sub
	jti, _ := tok.JwtID()
	c.JTI = jti
	if t, ok := tok.IssuedAt(); ok {
		c.IssuedAt = t
	}
	if t, ok := tok.Expiration(); ok {
		c.Expiry = t
	}
	for name, dst := range map[string]*string{
		"ses": &c.Session, "dev": &c.Device, "hrn": &c.Harness,
		"att": &c.Attestation, "rol": &c.RolesHash, "snp": &c.Snapshot,
	} {
		if tok.Has(name) {
			if err := tok.Get(name, dst); err != nil {
				return Claims{}, fmt.Errorf("authn: claim %s: %w", name, err)
			}
		}
	}
	return c, nil
}

// IDClaims is the subset of OpenID claims Straza consumes from ID tokens.
type IDClaims struct {
	Subject  string
	Username string // preferred_username
	Email    string
}

// MintIDToken issues an OIDC ID token for the built-in issuer, signed
// with the same rotating ed25519 keys as session tokens.
func (s *TokenService) MintIDToken(sub, audience string, ttl time.Duration, username, email string) (string, error) {
	iat := time.Now().UTC().Truncate(time.Second)
	tok, err := jwt.NewBuilder().
		Issuer(s.issuer).
		Subject(sub).
		Audience([]string{audience}).
		IssuedAt(iat).
		Expiration(iat.Add(ttl)).
		Claim("preferred_username", username).
		Claim("email", email).
		Build()
	if err != nil {
		return "", fmt.Errorf("authn: build id token: %w", err)
	}
	s.mu.RLock()
	key := s.active
	s.mu.RUnlock()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
	if err != nil {
		return "", fmt.Errorf("authn: sign id token: %w", err)
	}
	return string(signed), nil
}

// VerifyIDToken validates an ID token minted by the built-in issuer
// (signature, issuer, audience, expiry) and extracts the OpenID claims.
// External-IdP tokens are verified by the OIDC client, not here.
func (s *TokenService) VerifyIDToken(raw, audience string) (IDClaims, error) {
	s.mu.RLock()
	set := s.verify
	s.mu.RUnlock()
	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKeySet(set),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(audience),
		jwt.WithAcceptableSkew(30*time.Second),
	)
	if err != nil {
		return IDClaims{}, fmt.Errorf("authn: id token rejected: %w", err)
	}
	var c IDClaims
	c.Subject, _ = tok.Subject()
	if tok.Has("preferred_username") {
		_ = tok.Get("preferred_username", &c.Username)
	}
	if tok.Has("email") {
		_ = tok.Get("email", &c.Email)
	}
	return c, nil
}

// DeviceClaims is what a verified device token proves: this user enrolled
// this device. Everything else (user active? device revoked? denylisted?) is
// re-checked at each use: the token is identity, not authorization.
type DeviceClaims struct {
	Subject string // user id
	Device  string // device id
	JTI     string
	// IssuedAt and Expiry are the credential's own lifetime, read by the
	// check-in renewal to tell a credential past half its life. The verifier
	// has already enforced Expiry.
	IssuedAt time.Time
	Expiry   time.Time
}

// DefaultDeviceTokenTTL is the enroll credential lifetime: long enough
// that enrolling is genuinely once-per-device, short enough that a lost
// laptop's credential dies on its own even if nobody revokes the device.
const DefaultDeviceTokenTTL = 30 * 24 * time.Hour

// MintDeviceToken issues the long-lived enroll credential: a
// purpose-scoped token ("use":"device") binding user + device, signed with
// the same rotating keys. It can open exactly one door (/v1/checkin) where
// denylist and user/device status are enforced; it carries no session claim
// so every data-plane surface refuses it, and no audience so the login paths
// refuse it too.
func (s *TokenService) MintDeviceToken(userID, deviceID string, ttl time.Duration) (string, error) {
	iat := time.Now().UTC().Truncate(time.Second)
	tok, err := jwt.NewBuilder().
		Issuer(s.issuer).
		Subject(userID).
		JwtID(uuid.NewString()).
		IssuedAt(iat).
		Expiration(iat.Add(ttl)).
		Claim("use", "device").
		Claim("dev", deviceID).
		Build()
	if err != nil {
		return "", fmt.Errorf("authn: build device token: %w", err)
	}
	s.mu.RLock()
	key := s.active
	s.mu.RUnlock()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
	if err != nil {
		return "", fmt.Errorf("authn: sign device token: %w", err)
	}
	return string(signed), nil
}

// VerifyDeviceToken validates a device token: signature, issuer, expiry, and
// the "use":"device" purpose claim; a session or ID token must never pass.
func (s *TokenService) VerifyDeviceToken(raw string) (DeviceClaims, error) {
	s.mu.RLock()
	set := s.verify
	s.mu.RUnlock()
	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKeySet(set),
		jwt.WithIssuer(s.issuer),
		jwt.WithAcceptableSkew(30*time.Second),
	)
	if err != nil {
		return DeviceClaims{}, fmt.Errorf("authn: device token rejected: %w", err)
	}
	var use string
	if tok.Has("use") {
		_ = tok.Get("use", &use)
	}
	if use != "device" {
		return DeviceClaims{}, fmt.Errorf("authn: device token rejected: not a device credential")
	}
	var c DeviceClaims
	c.Subject, _ = tok.Subject()
	c.JTI, _ = tok.JwtID()
	c.IssuedAt, _ = tok.IssuedAt()
	c.Expiry, _ = tok.Expiration()
	if tok.Has("dev") {
		if err := tok.Get("dev", &c.Device); err != nil {
			return DeviceClaims{}, fmt.Errorf("authn: device claim: %w", err)
		}
	}
	if c.Subject == "" || c.Device == "" {
		return DeviceClaims{}, fmt.Errorf("authn: device token rejected: incomplete claims")
	}
	return c, nil
}

// ApproverClaims is what a verified approver device token proves: this user
// enrolled this mobile approver device. Authorization (device revoked? user
// disabled? holds an approve role for THIS record?) is re-checked at each use;
// the token is identity, not authorization.
type ApproverClaims struct {
	Subject string // user id the device is bound to
	Device  string // approver device id (apd_...)
	JTI     string
}

// DefaultApproverTokenTTL is the mobile approver device-token lifetime, 30
// days. Rotation is a re-enroll. Revocation is an admin deleting the approver
// device row, and the row-backed verification fails at once.
const DefaultApproverTokenTTL = 30 * 24 * time.Hour

// MintApproverToken issues the mobile approver device credential, as
// MintDeviceToken does for an enrolled machine: a purpose-scoped token
// ("use":"approver") binding user + approver device. It opens exactly the
// /v1/approver/* surface. It carries no session claim so every data-plane and
// admin surface refuses it, and no audience so the login paths refuse it too.
func (s *TokenService) MintApproverToken(userID, deviceID string, ttl time.Duration) (string, error) {
	iat := time.Now().UTC().Truncate(time.Second)
	tok, err := jwt.NewBuilder().
		Issuer(s.issuer).
		Subject(userID).
		JwtID(uuid.NewString()).
		IssuedAt(iat).
		Expiration(iat.Add(ttl)).
		Claim("use", "approver").
		Claim("apd", deviceID).
		Build()
	if err != nil {
		return "", fmt.Errorf("authn: build approver token: %w", err)
	}
	s.mu.RLock()
	key := s.active
	s.mu.RUnlock()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
	if err != nil {
		return "", fmt.Errorf("authn: sign approver token: %w", err)
	}
	return string(signed), nil
}

// VerifyApproverToken validates an approver device token: signature, issuer,
// expiry, and the "use":"approver" purpose claim; a session, device, connect,
// or ID token must never pass.
func (s *TokenService) VerifyApproverToken(raw string) (ApproverClaims, error) {
	s.mu.RLock()
	set := s.verify
	s.mu.RUnlock()
	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKeySet(set),
		jwt.WithIssuer(s.issuer),
		jwt.WithAcceptableSkew(30*time.Second),
	)
	if err != nil {
		return ApproverClaims{}, fmt.Errorf("authn: approver token rejected: %w", err)
	}
	var use string
	if tok.Has("use") {
		_ = tok.Get("use", &use)
	}
	if use != "approver" {
		return ApproverClaims{}, fmt.Errorf("authn: approver token rejected: not an approver credential")
	}
	var c ApproverClaims
	c.Subject, _ = tok.Subject()
	c.JTI, _ = tok.JwtID()
	if tok.Has("apd") {
		if err := tok.Get("apd", &c.Device); err != nil {
			return ApproverClaims{}, fmt.Errorf("authn: approver device claim: %w", err)
		}
	}
	if c.Subject == "" || c.Device == "" {
		return ApproverClaims{}, fmt.Errorf("authn: approver token rejected: incomplete claims")
	}
	return c, nil
}

// ConnectClaims is the verified content of an OAuth connect state.
type ConnectClaims struct {
	Subject string // user id that initiated the connect
	App     string // app id the grant will be bound to
	JTI     string
}

// ConnectStateTTL bounds how long a started connect flow stays redeemable:
// long enough for a human to authorize in a browser, short enough that a
// state lifted from history/logs goes stale fast.
const ConnectStateTTL = 10 * time.Minute

// MintConnectState issues the OAuth `state` parameter for a per-user connect
// flow: a purpose-scoped token ("use":"connect") binding the
// initiating user to one app. Signed with the rotating keys, it verifies on
// any pod; the provider callback needs no server-side pending state
// (stateless control plane across replicas). It opens exactly one door,
// /v1/connect/callback, and carries no session claim or audience.
func (s *TokenService) MintConnectState(userID, appID string) (string, error) {
	return s.mintConnectStateTTL(userID, appID, ConnectStateTTL)
}

func (s *TokenService) mintConnectStateTTL(userID, appID string, ttl time.Duration) (string, error) {
	iat := time.Now().UTC().Truncate(time.Second)
	tok, err := jwt.NewBuilder().
		Issuer(s.issuer).
		Subject(userID).
		JwtID(uuid.NewString()).
		IssuedAt(iat).
		Expiration(iat.Add(ttl)).
		Claim("use", "connect").
		Claim("app", appID).
		Build()
	if err != nil {
		return "", fmt.Errorf("authn: build connect state: %w", err)
	}
	s.mu.RLock()
	key := s.active
	s.mu.RUnlock()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
	if err != nil {
		return "", fmt.Errorf("authn: sign connect state: %w", err)
	}
	return string(signed), nil
}

// VerifyConnectState validates a connect state: signature, issuer, expiry,
// and the "use":"connect" purpose claim; no other credential may pass.
func (s *TokenService) VerifyConnectState(raw string) (ConnectClaims, error) {
	s.mu.RLock()
	set := s.verify
	s.mu.RUnlock()
	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKeySet(set),
		jwt.WithIssuer(s.issuer),
		jwt.WithAcceptableSkew(30*time.Second),
	)
	if err != nil {
		return ConnectClaims{}, fmt.Errorf("authn: connect state rejected: %w", err)
	}
	var use string
	if tok.Has("use") {
		_ = tok.Get("use", &use)
	}
	if use != "connect" {
		return ConnectClaims{}, fmt.Errorf("authn: connect state rejected: not a connect state")
	}
	var c ConnectClaims
	c.Subject, _ = tok.Subject()
	c.JTI, _ = tok.JwtID()
	if tok.Has("app") {
		if err := tok.Get("app", &c.App); err != nil {
			return ConnectClaims{}, fmt.Errorf("authn: connect app claim: %w", err)
		}
	}
	if c.Subject == "" || c.App == "" {
		return ConnectClaims{}, fmt.Errorf("authn: connect state rejected: incomplete claims")
	}
	return c, nil
}

// JWKS returns the public verification keys as a JWKS document for
// /.well-known/straza/jwks.json (gateway pods and straza verify locally).
func (s *TokenService) JWKS() ([]byte, error) {
	s.mu.RLock()
	set := s.verify
	s.mu.RUnlock()
	return json.Marshal(set)
}

func importPrivate(rec store.SigningKey) (jwk.Key, error) {
	priv := ed25519.NewKeyFromSeed(rec.PrivateKey)
	key, err := jwk.Import(priv)
	if err != nil {
		return nil, fmt.Errorf("authn: import private key %s: %w", rec.KID, err)
	}
	if err := annotate(key, rec.KID); err != nil {
		return nil, err
	}
	return key, nil
}

func importPublic(rec store.SigningKey) (jwk.Key, error) {
	key, err := jwk.Import(ed25519.PublicKey(rec.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("authn: import public key %s: %w", rec.KID, err)
	}
	if err := annotate(key, rec.KID); err != nil {
		return nil, err
	}
	return key, nil
}

func annotate(key jwk.Key, kid string) error {
	if err := key.Set(jwk.KeyIDKey, kid); err != nil {
		return fmt.Errorf("authn: set kid: %w", err)
	}
	if err := key.Set(jwk.AlgorithmKey, jwa.EdDSA()); err != nil {
		return fmt.Errorf("authn: set alg: %w", err)
	}
	if err := key.Set(jwk.KeyUsageKey, "sig"); err != nil {
		return fmt.Errorf("authn: set use: %w", err)
	}
	return nil
}
