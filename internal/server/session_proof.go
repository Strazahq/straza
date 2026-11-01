package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// errAnswered reports that a helper already answered the request, so the
// caller returns without writing.
var errAnswered = errors.New("server: request already answered")

// errUnboundUnderDPoP reports a token without cnf that arrived under the
// DPoP scheme, which RFC 9449 keeps for key-bound tokens.
var errUnboundUnderDPoP = errors.New("server: a token that is not bound to a key arrived under the DPoP scheme")

// errLoginNotBearer reports an ID token that arrived under a scheme other
// than Bearer.
var errLoginNotBearer = errors.New("server: an ID token is accepted only under the Bearer scheme")

// schemeBearer is the Authorization scheme of a bearer credential.
const schemeBearer = "Bearer"

// proofCopiedTokenMsg is the 401 of a key-bound session token that arrived
// without a valid proof from its key.
const proofCopiedTokenMsg = "This session token belongs to a key on the machine that checked in, " +
	"and this request carried no valid proof from that key. " +
	"Send it from the Straza client on that machine. A copied token works nowhere else."

// proofNonceMsg is the 401 of a proof whose nonce is missing or stale.
const proofNonceMsg = "Straza needs a fresh proof nonce. Sign the request again with the nonce in the DPoP-Nonce header."

// sessionCredential returns the token of r's Authorization header and its
// scheme, "Bearer" or "DPoP", or two empty strings for any other header.
// The scheme word matches in any case, as RFC 9110 says, and the token
// must not be empty.
func sessionCredential(r *http.Request) (raw, scheme string) {
	word, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || token == "" {
		return "", ""
	}
	switch {
	case strings.EqualFold(word, schemeBearer):
		return token, schemeBearer
	case strings.EqualFold(word, dpop.Scheme):
		return token, dpop.Scheme
	}
	return "", ""
}

// verifySessionToken verifies the session token raw for request r. It checks
// the token locally, and when the claims carry cnf it also checks the scheme
// and the request's DPoP proof: method, URL, nonce, ath over raw and the key
// thumbprint. scheme is the Authorization scheme raw arrived under, or ""
// when raw came in a request body, as on refresh. A token that fails returns
// an error wrapping the authn cause, and the caller answers with its own
// message. A proof failure is answered here and returns errAnswered. It
// reads no store, and the proof failure's record goes to the audit queue.
func (a *App) verifySessionToken(w http.ResponseWriter, r *http.Request, raw, scheme string) (authn.Claims, error) {
	claims, err := a.tokens.Verify(raw)
	if err != nil {
		return authn.Claims{}, fmt.Errorf("session token: %w", err)
	}
	if claims.CNF == "" {
		if scheme == dpop.Scheme {
			return authn.Claims{}, errUnboundUnderDPoP
		}
		return claims, nil
	}
	proofs := r.Header.Values(dpop.HeaderProof)
	var reason string
	switch {
	case scheme == schemeBearer:
		reason = "key-bound token sent under the Bearer scheme"
	case len(proofs) == 0:
		reason = "no DPoP proof"
	case len(proofs) > 1:
		reason = "more than one DPoP proof"
	default:
		_, err := a.proofs.Verify(proofs[0], authn.ProofCheck{
			Method: r.Method, Path: r.URL.Path, Origins: a.proofOrigins(r),
			AccessToken: raw, Thumbprint: claims.CNF,
		}, time.Now())
		if errors.Is(err, authn.ErrStaleNonce) {
			a.answerNonce(w)
			return authn.Claims{}, errAnswered
		}
		if err != nil {
			reason = proofFailureReason(err)
		}
	}
	if reason == "" {
		return claims, nil
	}
	a.refuseProof(w, r, claims, reason)
	return authn.Claims{}, errAnswered
}

// proofFailureReason names the cause of a proof the verifier refused. The
// verifier's reasons are fixed words, so the record never carries request
// bytes, and any other error gets one fixed reason of its own.
func proofFailureReason(err error) string {
	var pe *authn.ProofError
	if errors.As(err, &pe) {
		return pe.Reason
	}
	return "the DPoP proof did not verify"
}

// refuseProof answers 401 to a key-bound token without a valid proof and
// queues one straza.audit.authn proof failure that names the token's user,
// session and harness. The record goes through the audit queue, as a
// decision's does, so the refusal never waits on the store. A record the
// queue refuses is logged, and the request is refused all the same.
func (a *App) refuseProof(w http.ResponseWriter, r *http.Request, claims authn.Claims, reason string) {
	f := authnFields{UserID: claims.Subject, Session: claims.Session, Harness: claims.Harness, Reason: reason}
	ev, err := a.cloudEvent(r.Context(), "straza.audit.authn", authnPayload("proof", "failure", f, r))
	if err == nil {
		err = a.audit.submit(r.Context(), ev)
	}
	if err != nil {
		a.log.Warn("the record of a refused DPoP proof could not be queued, so this refusal has no audit record. "+
			"Check that the database is reachable and takes audit records",
			"user", claims.Subject, "session", claims.Session, "err", err)
	}
	w.Header().Set("WWW-Authenticate", `DPoP error="invalid_dpop_proof"`)
	apiError(w, http.StatusUnauthorized, proofCopiedTokenMsg)
}

// proofOrigins returns the origins a proof's htu may name for r: the origin
// of server.publicUrl, and the origin r arrived on, https when r came over
// TLS, only when no public URL is set or when the public URL and r's Host
// are both loopback hosts with the same scheme and port. The Host header is
// the sender's word, so it never adds an origin otherwise. The loopback
// pair exists for eval, which advertises http://localhost:8420 while its
// clients dial 127.0.0.1:8420.
func (a *App) proofOrigins(r *http.Request) []string {
	arrival := &url.URL{Scheme: "http", Host: r.Host}
	if r.TLS != nil {
		arrival.Scheme = "https"
	}
	pub, err := url.Parse(a.cfg.Server.PublicURL)
	if err != nil || pub.Scheme == "" || pub.Host == "" {
		return []string{arrival.Scheme + "://" + arrival.Host}
	}
	origins := []string{pub.Scheme + "://" + pub.Host}
	if pub.Scheme == arrival.Scheme && isLoopbackHost(pub.Hostname()) && isLoopbackHost(arrival.Hostname()) &&
		originPort(pub) == originPort(arrival) {
		origins = append(origins, arrival.Scheme+"://"+arrival.Host)
	}
	return origins
}

// originPort returns u's port, or the default port of its scheme when u
// names none.
func originPort(u *url.URL) string {
	switch {
	case u.Port() != "":
		return u.Port()
	case u.Scheme == "https":
		return "443"
	}
	return "80"
}

// answerNonce answers 401 to a proof whose nonce is missing or stale, with
// a fresh nonce in the DPoP-Nonce header. It writes no audit record.
func (a *App) answerNonce(w http.ResponseWriter) {
	w.Header().Set(dpop.HeaderNonce, a.proofs.Nonce(time.Now()))
	w.Header().Set("WWW-Authenticate", `DPoP error="use_dpop_nonce"`)
	apiError(w, http.StatusUnauthorized, proofNonceMsg)
}

// withDPoPNonce gives the answer to every request that carries a DPoP header
// a fresh DPoP-Nonce, set before the handler writes, so the client signs its
// next proof with a current nonce. A request without the header is
// untouched.
func (a *App) withDPoPNonce(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values(dpop.HeaderProof)) > 0 {
			w.Header().Set(dpop.HeaderNonce, a.proofs.Nonce(time.Now()))
		}
		next.ServeHTTP(w, r)
	})
}

// verifyBearerLogin verifies raw as an ID token when it arrived under the
// Bearer scheme. Under the DPoP scheme only a key-bound session token is
// accepted, so it returns errLoginNotBearer there.
func (a *App) verifyBearerLogin(r *http.Request, raw, scheme string) (store.User, error) {
	if scheme != schemeBearer {
		return store.User{}, errLoginNotBearer
	}
	return a.verifyLogin(r, raw)
}
