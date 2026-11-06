package authn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/clientassertion"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// The built-in issuer's OAuth2 client_credentials grant for NHI principals:
// a headless agent authenticates with an RFC 7523-style client assertion
// (a short-lived EdDSA JWT signed with the per-NHI Ed25519 key an admin
// registered) and receives the same short ID token the interactive device
// flow mints. No browser, no password, no long-lived bearer credential: the
// private key never travels, and each assertion is single-use (jti replay
// cache) within a ≤5-minute window.

// NHIKeyLookup resolves a client_credentials client_id (the NHI username)
// to the local user and its registered assertion key. The callback owns ALL
// identity policy: it must fail unless the user exists, is active, is
// kind=nhi, and has a registered key; the issuer treats every error as
// failed client authentication (one error code, no NHI enumeration).
type NHIKeyLookup func(ctx context.Context, clientID string) (store.User, ed25519.PublicKey, error)

const (
	clientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	// maxAssertionLifetime bounds exp−now: an assertion is a per-request
	// proof, not a credential; a lifted one must go stale fast even before
	// the replay cache bites. The bound is the shared mint/verify contract
	// (internal/clientassertion, a leaf the client links without this
	// package's store dependency).
	maxAssertionLifetime = clientassertion.MaxLifetime
	// nhiTokenAudience is the audience minted into headless ID tokens; it is
	// the enforcement kit's client id, already accepted at /v1/checkin.
	nhiTokenAudience = "straza"
)

// errAssertionKey reports an EdDSA client assertion whose signature does not
// verify with the key registered for its client.
var errAssertionKey = errors.New("authn: assertion signature does not verify with the registered key")

// assertionKeyRefused is the error_description for errAssertionKey. It names
// the client the caller sent, how to register the key it signs with, and
// the enroll command for server. The server is this issuer's URL, which
// idp.json names as nhi_issuer, so the agent's key lane already reached it.
func assertionKeyRefused(username, server string) string {
	return "the key that signed the request is not the key registered for " + username + ". " +
		"On the agent's machine, run `straza keygen --force --user " + username + "` to make a new key. " +
		"Have an admin register the public key it prints, with `strazactl users nhi-key set` or in the console. " +
		"Then run `straza enroll --server " + server + " --headless`"
}

// nhiGrantOutcome fires the matching observer hook once per judged grant.
func (i *Issuer) nhiGrantOutcome(userID, username, reason string, r *http.Request) {
	if reason == "" {
		if i.OnNHIGrant != nil {
			i.OnNHIGrant(userID, username, r)
		}
		return
	}
	if i.OnNHIGrantFailed != nil {
		i.OnNHIGrantFailed(userID, username, reason, r)
	}
}

// handleClientCredentials serves grant_type=client_credentials on the token
// endpoint. Called from handleToken with the form already parsed.
func (i *Issuer) handleClientCredentials(w http.ResponseWriter, r *http.Request) {
	if i.NHIKeys == nil {
		oauthError(w, "unsupported_grant_type",
			"the client_credentials grant is not enabled on this issuer")
		return
	}
	clientID := r.PostForm.Get("client_id")
	assertion := r.PostForm.Get("client_assertion")
	if clientID == "" || assertion == "" || r.PostForm.Get("client_assertion_type") != clientAssertionType {
		oauthError(w, "invalid_request",
			"client_id, client_assertion_type ("+clientAssertionType+") and client_assertion are required")
		return
	}

	u, pub, err := i.NHIKeys(r.Context(), clientID)
	if err != nil || u.Status != store.UserActive || len(pub) != ed25519.PublicKeySize {
		i.nhiGrantOutcome("", "", "unknown or ineligible client", r)
		oauthError(w, "invalid_client", "client authentication failed")
		return
	}
	jti, exp, err := verifyClientAssertion(assertion, pub, clientID, i.baseURL)
	if err != nil {
		i.nhiGrantOutcome(u.ID, u.Username, "client assertion rejected", r)
		desc := "client assertion rejected: mint a fresh EdDSA assertion (iss=sub=client_id, aud=issuer, exp ≤ 5 min, unique jti)"
		if errors.Is(err, errAssertionKey) {
			desc = assertionKeyRefused(u.Username, i.baseURL)
		}
		oauthError(w, "invalid_client", desc)
		return
	}

	i.mu.Lock()
	i.gcJTILocked()
	if _, seen := i.seenJTI[jti]; seen {
		i.mu.Unlock()
		i.nhiGrantOutcome(u.ID, u.Username, "client assertion replayed", r)
		oauthError(w, "invalid_grant", "client assertion replayed: sign a fresh assertion per request")
		return
	}
	i.seenJTI[jti] = exp
	i.mu.Unlock()

	// The token names the key that verified the assertion, so the server
	// refuses it once an admin replaces or removes that key.
	idTok, err := i.tokens.MintIDTokenFor(u.ID, nhiTokenAudience, i.idTTL, u.Username, u.Email,
		LoginFacts{KeyThumbprint: dpop.Thumbprint(pub)})
	if err != nil {
		oauthError(w, "server_error", "could not sign id token")
		return
	}
	i.nhiGrantOutcome(u.ID, u.Username, "", r)
	writeJSONIssuer(w, http.StatusOK, map[string]any{
		"access_token": idTok, // doubles as the Straza API bearer, same as the device grant
		"id_token":     idTok,
		"token_type":   "Bearer",
		"expires_in":   int(i.idTTL.Seconds()),
	})
}

// verifyClientAssertion checks signature (EdDSA against the registered key),
// exp/iat (30 s skew, same as every Straza token), iss==sub==client_id,
// audience (the issuer URL or its token endpoint), the lifetime bound, and
// that a jti is present. Returns the jti and expiry for the replay cache.
// A refused assertion whose EdDSA signature does not verify with pub fails
// with errAssertionKey.
func verifyClientAssertion(raw string, pub ed25519.PublicKey, clientID, issuerURL string) (jti string, exp time.Time, err error) {
	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKey(jwa.EdDSA(), pub),
		jwt.WithAcceptableSkew(30*time.Second),
	)
	if err != nil {
		if signedByAnotherKey(raw, pub) {
			return "", time.Time{}, fmt.Errorf("%w: %w", errAssertionKey, err)
		}
		return "", time.Time{}, fmt.Errorf("authn: assertion rejected: %w", err)
	}
	iss, _ := tok.Issuer()
	sub, _ := tok.Subject()
	if iss != clientID || sub != clientID {
		return "", time.Time{}, fmt.Errorf("authn: assertion iss/sub must equal client_id")
	}
	okAud := false
	aud, _ := tok.Audience()
	for _, a := range aud {
		if a == issuerURL || a == issuerURL+"/oidc/token" {
			okAud = true
			break
		}
	}
	if !okAud {
		return "", time.Time{}, fmt.Errorf("authn: assertion audience is not this issuer")
	}
	expT, ok := tok.Expiration()
	if !ok || time.Until(expT) > maxAssertionLifetime+30*time.Second {
		return "", time.Time{}, fmt.Errorf("authn: assertion lifetime exceeds %s", maxAssertionLifetime)
	}
	jti, _ = tok.JwtID()
	if jti == "" {
		return "", time.Time{}, fmt.Errorf("authn: assertion has no jti")
	}
	return jti, expT, nil
}

// signedByAnotherKey reports whether raw is a compact JWS with alg EdDSA
// whose signature does not verify with pub. It only names the reason for a
// refusal that jwt.Parse already made. It verifies the bytes as received
// with the call jwt.Parse makes for one key, so an assertion signed by pub
// and refused for its claims, its encoding or a crit header reports false.
func signedByAnotherKey(raw string, pub ed25519.PublicKey) bool {
	b := bytes.TrimSpace([]byte(raw))
	msg, err := jws.Parse(b, jws.WithCompact())
	if err != nil || len(msg.Signatures()) != 1 {
		return false
	}
	if alg, ok := msg.Signatures()[0].ProtectedHeaders().Algorithm(); !ok || alg != jwa.EdDSA() {
		return false
	}
	_, err = jws.VerifyCompactFast(pub, b, jwa.EdDSA())
	return errors.Is(err, jws.VerificationError())
}

// gcJTILocked drops replay-cache entries whose assertions have expired (they
// can no longer verify, so remembering them buys nothing). Called with i.mu
// held.
func (i *Issuer) gcJTILocked() {
	cutoff := time.Now().Add(-30 * time.Second) // skew: keep until truly dead
	for jti, exp := range i.seenJTI {
		if exp.Before(cutoff) {
			delete(i.seenJTI, jti)
		}
	}
}

// The mint half of this contract lives in internal/clientassertion (a leaf
// package) so the straza client signs assertions without linking this
// package's store dependency; the shared MaxLifetime keeps mint and verify
// agreeing on one bound.
