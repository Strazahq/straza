package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// keyRetireAfter is how long a retiring session signing key keeps verifying
// after it stopped signing: the longest credential lifetime these keys sign,
// the enroll credential or the approver device token, plus one reload
// interval for the replica that signed with the old key last. Session, ID
// and connect tokens live minutes and are covered by that.
func (a *App) keyRetireAfter() time.Duration {
	longest := a.deviceTokenTTL()
	if authn.DefaultApproverTokenTTL > longest {
		longest = authn.DefaultApproverTokenTTL
	}
	return longest + authn.KeyReloadInterval
}

// deviceTokenTTL is the configured lifetime of the device credential that
// strazactl and older Straza clients received at enrollment, the default
// when the knob is unset. strazad issues none any more, and a retiring key
// keeps verifying the ones it issued, so each still gets the sentence that
// says why it stopped working.
func (a *App) deviceTokenTTL() time.Duration {
	if ttl := a.cfg.Governance.DeviceTokenTTL; ttl > 0 {
		return ttl
	}
	return authn.DefaultDeviceTokenTTL
}

// handleSigningKeyRotate serves POST /v1/admin/signing-keys/rotate, phase one
// of a two-phase rotation. It stages a new session signing key, which every
// replica verifies from its next reload, and the rotation janitor
// (runKeyRotation) promotes it two reload intervals later, once every replica
// holds it. The answer says when the new key starts signing and how long the
// previous key keeps verifying. A call while a staged key exists answers with
// that key; every call writes one straza.audit.admin record.
func (a *App) handleSigningKeyRotate(w http.ResponseWriter, r *http.Request) {
	key, err := a.tokens.Stage(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not stage a new signing key", err)
		return
	}
	activeIn := 2 * authn.KeyReloadInterval
	a.emitEvent(r, "straza.audit.admin", map[string]any{
		"action": "signing-keys.rotate", "kid": key.KID,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"kid":                               key.KID,
		"status":                            key.Status,
		"active_in_seconds":                 int(activeIn.Seconds()),
		"previous_key_verifies_for_seconds": int((activeIn + a.keyRetireAfter()).Seconds()),
	})
}

// assertionKeySet is the client assertion key manager with the clock of its
// admin routes, time.Now when nil. Tests set the clock, because the promote
// route refuses a key staged less than two reload intervals ago.
type assertionKeySet struct {
	*authn.ClientAssertionKeys
	clock func() time.Time
}

func (k assertionKeySet) now() time.Time {
	if k.clock != nil {
		return k.clock()
	}
	return time.Now()
}

// clientAssertionJWKSPath is where the client assertion key document lives.
// An identity provider stores this address as the JWKS URL of every agent's
// client (RFC 7591 jwks_uri).
const clientAssertionJWKSPath = "/.well-known/straza/client-assertion-jwks.json"

// assertionKeyRetireAfter is how long a retiring client assertion key stays
// in the key document after it stopped signing: the lifetime of an assertion
// plus one reload interval for the replica that signed with the old key last.
func (a *App) assertionKeyRetireAfter() time.Duration {
	return authn.ClientAssertionTTL + authn.KeyReloadInterval
}

// handleClientAssertionJWKS serves the client assertion key document. The
// route is unauthenticated because the identity provider fetches it, so it
// reads nothing the caller sends and writes bytes that were built at the last
// key reload. A replica that can no longer vouch for its keys answers 503.
func (a *App) handleClientAssertionJWKS(w http.ResponseWriter, r *http.Request) {
	doc, err := a.assertionKeys.JWKS(a.assertionKeys.now())
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		a.fail(w, r, http.StatusServiceUnavailable,
			"the client assertion key document is unavailable, because this replica has not read its key store for over a minute. "+
				"Check the store connection of this replica. The identity provider keeps the keys it already fetched", err)
		return
	}
	// One reload interval, not the session document's minute: every replica
	// serves a staged key within one interval and nothing signs with it
	// before two, so a cache that obeys this header holds the new key before
	// the first assertion it signs.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=30")
	_, _ = w.Write(doc)
}

// handleSigningKeysList serves GET /v1/admin/signing-keys: every signing key
// with its purpose, status and the two timestamps, and for a client
// assertion key that is not retired the end of its certificate's validity.
// It returns no key material of either half.
func (a *App) handleSigningKeysList(w http.ResponseWriter, r *http.Request) {
	type row struct {
		KID                  string  `json:"kid"`
		Purpose              string  `json:"purpose"`
		Status               string  `json:"status"`
		CreatedAt            string  `json:"created_at"`
		RotatedAt            *string `json:"rotated_at,omitempty"`
		CertificateExpiresAt string  `json:"certificate_expires_at,omitempty"`
	}
	out := []row{}
	for _, purpose := range []string{store.KeyPurposeClientAssertion, store.KeyPurposeSession, store.KeyPurposeSnapshot} {
		keys, err := a.store.SigningKeys().ListByPurpose(r.Context(), purpose)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "could not list the signing keys", err)
			return
		}
		for _, k := range keys {
			out = append(out, row{
				KID: k.KID, Purpose: k.Purpose, Status: k.Status,
				CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339), RotatedAt: formatTimePtr(k.RotatedAt),
			})
			if k.Purpose == store.KeyPurposeClientAssertion && k.Status != store.KeyRetired {
				out[len(out)-1].CertificateExpiresAt = authn.CertificateNotAfter(k.CreatedAt).Format(time.RFC3339)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleClientAssertionKeyRotate serves POST
// /v1/admin/signing-keys/client-assertion/rotate behind requireFullAdmin. It
// stages a new client assertion key, which this replica publishes at once and
// every other replica within one reload interval. The key signs nothing until
// an administrator promotes it, which the promote route accepts two reload
// intervals after staging, so active_in_seconds is that wait and
// previous_key_verifies_for_seconds the soonest the previous key can leave
// the document. A call while a staged key exists answers with that key. The
// call that created the key writes one straza.audit.admin record:
// signing-keys.create when no key signs today, else signing-keys.rotate.
func (a *App) handleClientAssertionKeyRotate(w http.ResponseWriter, r *http.Request) {
	key, err := a.assertionKeys.Stage(r.Context(), a.assertionKeys.now())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError,
			"could not stage a new client assertion key. Nothing changed, the current key keeps signing. Check the server log and the store, then run the command again", err)
		return
	}
	if key.Created {
		action := "signing-keys.rotate"
		if key.ActiveKID == "" {
			action = "signing-keys.create"
		}
		a.emitEvent(r, "straza.audit.admin", map[string]any{
			"action": action, "purpose": store.KeyPurposeClientAssertion, "kid": key.KID,
		})
	}
	activeIn := 2 * authn.KeyReloadInterval
	out := map[string]any{
		"kid":                               key.KID,
		"status":                            store.KeyStaged,
		"active_in_seconds":                 int(activeIn.Seconds()),
		"previous_key_verifies_for_seconds": int((activeIn + a.assertionKeyRetireAfter()).Seconds()),
		"jwks_uri":                          a.cfg.Server.PublicURL + clientAssertionJWKSPath,
	}
	if key.ActiveKID != "" {
		out["previous_kid"] = key.ActiveKID
	}
	writeJSON(w, http.StatusOK, out)
}

// handleClientAssertionKeyRetire serves POST
// /v1/admin/signing-keys/client-assertion/{kid}/retire behind
// requireFullAdmin: the lever for a key that may have been copied. The key
// leaves this replica's document at once and every other replica's at its
// next reload. The call that retired the key writes one straza.audit.admin
// record; a key that is retired already answers 200 and writes none.
func (a *App) handleClientAssertionKeyRetire(w http.ResponseWriter, r *http.Request) {
	kid := r.PathValue("kid")
	was, changed, err := a.assertionKeys.Retire(r.Context(), a.assertionKeys.now(), kid)
	var wrong *authn.WrongPurposeError
	switch {
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "no signing key has the id "+kid+". Run `strazactl signing-keys list` to see the keys.")
		return
	case errors.As(err, &wrong):
		apiError(w, http.StatusConflict, "the key "+kid+" is a "+wrong.Purpose+" key, and only a client assertion key can be retired by hand. "+
			"A session key retires by itself after `strazactl signing-keys rotate session`.")
		return
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError,
			"could not retire the client assertion key. Run `strazactl signing-keys list` to see its state, then run the command again", err)
		return
	}
	if changed {
		a.emitEvent(r, "straza.audit.admin", map[string]any{
			"action": "signing-keys.retire", "purpose": store.KeyPurposeClientAssertion, "kid": kid, "was": was,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kid": kid, "status": store.KeyRetired, "was": was,
		"leaves_document_in_seconds": int(authn.KeyReloadInterval.Seconds()),
		"next_key_active_in_seconds": int((2 * authn.KeyReloadInterval).Seconds()),
	})
}

// handleClientAssertionKeyPromote serves POST
// /v1/admin/signing-keys/client-assertion/{kid}/promote behind
// requireFullAdmin. The staged key becomes the signing key, which this
// replica signs with at once and every other replica within one reload
// interval, and the key that signed before retires once the assertions it
// signed have expired. The call that promoted the key writes one
// straza.audit.admin record with the administrator as its actor. A key that
// signs already answers 200 and writes none.
func (a *App) handleClientAssertionKeyPromote(w http.ResponseWriter, r *http.Request) {
	kid := r.PathValue("kid")
	got, err := a.assertionKeys.Promote(r.Context(), a.assertionKeys.now(), kid)
	var wrong *authn.WrongPurposeError
	var status *authn.KeyStatusError
	var early *authn.PromoteTooEarlyError
	switch {
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "no signing key has the id "+kid+". Run `strazactl signing-keys list` to see the keys.")
		return
	case errors.As(err, &wrong):
		apiError(w, http.StatusConflict, "the key "+kid+" is a "+wrong.Purpose+" key, and only a client assertion key is promoted by hand. "+
			"A session key promotes itself after `strazactl signing-keys rotate session`.")
		return
	case errors.As(err, &status):
		apiError(w, http.StatusConflict, "the client assertion key "+kid+" is "+status.Status+" and cannot sign again. "+
			"Run `strazactl signing-keys rotate client_assertion` to stage a new key, then promote that one.")
		return
	case errors.As(err, &early):
		apiError(w, http.StatusConflict, fmt.Sprintf("the client assertion key %s was staged moments ago, and a key can be promoted only once every replica publishes it, %d seconds after staging. "+
			"Run the command again in %d seconds.", kid, int((2*authn.KeyReloadInterval).Seconds()), int(math.Ceil(early.Wait.Seconds()))))
		return
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError,
			"could not promote the client assertion key. Nothing changed, the current key keeps signing. Run `strazactl signing-keys list` to see its state, then run the command again", err)
		return
	}
	if got.Changed {
		a.emitEvent(r, "straza.audit.admin", map[string]any{
			"action": "signing-keys.promote", "purpose": store.KeyPurposeClientAssertion, "kid": kid,
		})
	}
	out := map[string]any{
		"kid": kid, "status": store.KeyActive, "was": got.Was,
		"every_replica_signs_in_seconds": int(authn.KeyReloadInterval.Seconds()),
	}
	if got.PreviousKID != "" {
		out["previous_kid"] = got.PreviousKID
		out["previous_key_leaves_document_in_seconds"] = int((a.assertionKeyRetireAfter() + authn.KeyReloadInterval).Seconds())
	}
	writeJSON(w, http.StatusOK, out)
}

// handleClientAssertionKeyCertificate serves GET
// /v1/admin/signing-keys/client-assertion/{kid}/certificate behind
// requireFullAdmin: the self-signed certificate of one staged, active or
// retiring client assertion key as PEM, which an administrator uploads to an
// Entra agent blueprint. Every replica builds the same bytes, and the
// certificate holds the public key only.
func (a *App) handleClientAssertionKeyCertificate(w http.ResponseWriter, r *http.Request) {
	kid := r.PathValue("kid")
	cert, err := a.assertionKeys.Certificate(r.Context(), a.assertionKeys.now(), kid)
	var wrong *authn.WrongPurposeError
	var status *authn.KeyStatusError
	switch {
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "no signing key has the id "+kid+". Run `strazactl signing-keys list` to see the keys.")
		return
	case errors.As(err, &wrong):
		apiError(w, http.StatusConflict, "the key "+kid+" is a "+wrong.Purpose+" key, and only a client assertion key has a certificate.")
		return
	case errors.As(err, &status):
		apiError(w, http.StatusConflict, "the client assertion key "+kid+" is "+status.Status+", so Straza no longer serves its certificate. "+
			"Remove that certificate from the Entra blueprint if it is still there.")
		return
	case errors.Is(err, authn.ErrAssertionKeysStale):
		a.fail(w, r, http.StatusServiceUnavailable,
			"the certificate is unavailable, because this replica has not read its key store for over a minute. Check the store connection of this replica, then run the command again", err)
		return
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError,
			"could not build the certificate of the client assertion key. Read the reason in the strazad log, then run the command again", err)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	_, _ = w.Write(cert.PEM())
}

// clientAssertionKeyStep is the janitor's pass over the client assertion
// keys: reload, so a key a peer staged, promoted or retired is published,
// signs or is dropped here, then retire each demoted key whose assertions
// have expired. The store lets one replica make each retirement, and that
// replica records it, so it leaves one straza.audit.admin record for the
// whole deployment. The record names no actor, because no authenticated
// principal made the step.
func (a *App) clientAssertionKeyStep(ctx context.Context, now time.Time) {
	if err := a.assertionKeys.Reload(ctx, now); err != nil {
		a.log.Warn("client assertion key reload failed", "err", err)
		return
	}
	step, err := a.assertionKeys.Advance(ctx, now, a.assertionKeyRetireAfter())
	if err != nil {
		a.log.Warn("client assertion key rotation step failed", "err", err)
	}
	for _, kid := range step.Retired {
		a.log.Info("client assertion key retire", "kid", kid)
		a.emitEventCtx(ctx, "straza.audit.admin", map[string]any{
			"action": "signing-keys.retire", "purpose": store.KeyPurposeClientAssertion, "kid": kid,
		})
	}
}
