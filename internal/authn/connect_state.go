package authn

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

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
