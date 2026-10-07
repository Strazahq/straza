package server

import (
	"context"
	"errors"
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
	doc, err := a.assertionKeys.JWKS(time.Now())
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
// with its purpose, status and the two timestamps. It returns no key
// material of either half.
func (a *App) handleSigningKeysList(w http.ResponseWriter, r *http.Request) {
	type row struct {
		KID       string  `json:"kid"`
		Purpose   string  `json:"purpose"`
		Status    string  `json:"status"`
		CreatedAt string  `json:"created_at"`
		RotatedAt *string `json:"rotated_at,omitempty"`
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
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleClientAssertionKeyRotate serves POST
// /v1/admin/signing-keys/client-assertion/rotate behind requireFullAdmin. It
// stages a new client assertion key, which this replica publishes at once and
// the janitor promotes two reload intervals later. A call while a staged key
// exists answers with that key. The call that created the key writes one
// straza.audit.admin record: signing-keys.create when no key signs today,
// else signing-keys.rotate.
func (a *App) handleClientAssertionKeyRotate(w http.ResponseWriter, r *http.Request) {
	key, err := a.assertionKeys.Stage(r.Context(), time.Now())
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
	was, changed, err := a.assertionKeys.Retire(r.Context(), time.Now(), kid)
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

// clientAssertionKeyStep is the janitor's pass over the client assertion
// keys: reload, so a key a peer staged or retired is published or dropped
// here, then advance the rotation. The store lets one replica make each
// transition, and that replica records it, so a promotion or a retirement
// leaves one straza.audit.admin record for the whole deployment. The record
// names no actor, because no authenticated principal made the step.
func (a *App) clientAssertionKeyStep(ctx context.Context, now time.Time) {
	if err := a.assertionKeys.Reload(ctx, now); err != nil {
		a.log.Warn("client assertion key reload failed", "err", err)
		return
	}
	step, err := a.assertionKeys.Advance(ctx, now, a.assertionKeyRetireAfter())
	if err != nil {
		a.log.Warn("client assertion key rotation step failed", "err", err)
	}
	record := func(action, kid string) {
		a.log.Info("client assertion key "+action, "kid", kid)
		a.emitEventCtx(ctx, "straza.audit.admin", map[string]any{
			"action": "signing-keys." + action, "purpose": store.KeyPurposeClientAssertion, "kid": kid,
		})
	}
	if step.Promoted != "" {
		record("promote", step.Promoted)
	}
	for _, kid := range step.Retired {
		record("retire", kid)
	}
}
