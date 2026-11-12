package server

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// Per-NHI assertion keys: the public half of
// the Ed25519 keypair a headless agent authenticates with at the built-in
// issuer's client_credentials grant. Same migration-free at-rest model as
// SCIM/admin API tokens: one settings row per NHI, keyed by user id. One key
// per NHI: rotation is an overwrite, revocation a delete; the private half
// never travels (NHI principals never use passwords).
const nhiKeyPrefix = "nhi.key."

type nhiKeyMeta struct {
	PublicKey string    `json:"publicKey"` // base64 (std) raw Ed25519 public key
	Created   time.Time `json:"created"`
}

// handleNHIKeySet registers (or rotates) an NHI's assertion key.
// PUT /v1/admin/users/{id}/nhi-key {"public_key": "<base64 ed25519>"}
// A key that replaces a different one ends every active session of the
// user. A first key, or the same key again, ends none.
func (a *App) handleNHIKeySet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicKey string `json:"public_key"`
	}
	err := decodeAdmin(r.Body, &req)
	if refusedUnknownField(w, err) {
		return
	}
	if err != nil || req.PublicKey == "" {
		apiError(w, http.StatusBadRequest, "public_key (base64 Ed25519) is required")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		apiError(w, http.StatusBadRequest, fmt.Sprintf("public_key must be %d base64-encoded Ed25519 bytes", ed25519.PublicKeySize))
		return
	}
	u, err := a.store.Users().GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such user")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	if userKind(u) != "nhi" {
		apiError(w, http.StatusBadRequest,
			"only an agent identity holds an assertion key. A person signs in interactively (create the user with kind=nhi, classify it userType agent or service, or provision via agentic SCIM)")
		return
	}
	// The key in place is read before the write, so a rotation knows to end
	// the sessions the old key started. A row that does not hold a key is
	// replaced as a different key.
	prev, err := a.registeredNHIKey(r.Context(), u.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, errNHIKeyMalformed) {
		a.fail(w, r, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	replaced := !errors.Is(err, store.ErrNotFound) && !prev.Equal(ed25519.PublicKey(raw))
	meta, err := json.Marshal(nhiKeyMeta{PublicKey: req.PublicKey, Created: time.Now().UTC()})
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "encode failed", err)
		return
	}
	if err := a.store.Settings().Set(r.Context(), nhiKeyPrefix+u.ID, string(meta)); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "store failed", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "nhi-key.set", "user": u.ID,
	})
	a.auditNHIKeySet(r.Context(), u, req.PublicKey)
	if replaced {
		if err := a.endAgentSessions(r, u, nhiKeyReplacedReason); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "The new key for "+u.Username+" is registered"+nhiKeySessionsLeftMsg, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "set", "user_id": u.ID})
}

// handleNHIKeyGet reads the assertion-key POSTURE, so the console can tell
// a usable NHI from a dead one:
// presence, created-at, and a fingerprint. The read never returns the key
// itself; a fingerprint correlates, and whoever registered the key holds it.
func (a *App) handleNHIKeyGet(w http.ResponseWriter, r *http.Request) {
	u, err := a.store.Users().GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such user")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	raw, err := a.store.Settings().Get(r.Context(), nhiKeyPrefix+u.ID)
	if err != nil {
		// Absent row (the settings seam has no distinct not-found): no key.
		writeJSON(w, http.StatusOK, map[string]any{"user_id": u.ID, "registered": false})
		return
	}
	var meta nhiKeyMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "decode failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": u.ID, "registered": true, "created": meta.Created, "fingerprint": nhiKeyFingerprint(meta.PublicKey),
	})
}

// nhiKeyAbsentMsg is the 404 of a key delete for a user who holds no
// assertion key, formatted with the username and the user id.
const nhiKeyAbsentMsg = "the user %s has no assertion key registered, so there is nothing to remove. " +
	"To see what is registered, read GET /v1/admin/users/%s/nhi-key. " +
	"For an AI agent the console also shows the key status on the user's panel."

// handleNHIKeyDelete revokes an NHI's assertion key: the next grant fails,
// every active session of the user ends, and the check-in refuses the ID
// tokens issued for the removed key. A user with no key answers 404 and
// writes no record, because nothing changed.
func (a *App) handleNHIKeyDelete(w http.ResponseWriter, r *http.Request) {
	u, err := a.store.Users().GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such user")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	err = a.store.Settings().Delete(r.Context(), nhiKeyPrefix+u.ID)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, fmt.Sprintf(nhiKeyAbsentMsg, u.Username, u.ID))
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "delete failed", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "nhi-key.removed", "user": u.ID,
	})
	a.auditNHIKeyRemoved(r.Context(), u.ID)
	if err := a.endAgentSessions(r, u, nhiKeyRemovedReason); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The key of "+u.Username+" is removed"+nhiKeySessionsLeftMsg, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "user_id": u.ID})
}

// nhiKeyLookup is the identity-policy closure behind the issuer's
// client_credentials grant (authn.NHIKeyLookup): it alone decides who may
// use the grant: existing, active, kind=nhi, key registered. The issuer
// maps every error here onto one invalid_client (no NHI enumeration).
func (a *App) nhiKeyLookup(ctx context.Context, clientID string) (store.User, ed25519.PublicKey, error) {
	u, err := a.store.Users().GetByUsername(ctx, clientID)
	if err != nil {
		return store.User{}, nil, fmt.Errorf("nhi lookup: %w", err)
	}
	if u.Status != store.UserActive {
		return store.User{}, nil, fmt.Errorf("nhi lookup: user %s is not active", u.ID)
	}
	if userKind(u) != "nhi" {
		return store.User{}, nil, fmt.Errorf("nhi lookup: %s is not an NHI identity", u.ID)
	}
	key, err := a.registeredNHIKey(ctx, u.ID)
	if err != nil {
		return store.User{}, nil, fmt.Errorf("nhi lookup: no usable key registered: %w", err)
	}
	return u, key, nil
}

// errNHIKeyMalformed reports a stored assertion key row that does not hold
// an Ed25519 public key.
var errNHIKeyMalformed = errors.New("the stored assertion key is malformed")

// registeredNHIKey reads the assertion key registered for the user id. It
// answers store.ErrNotFound when none is registered, errNHIKeyMalformed when
// the row does not hold an Ed25519 public key, and the store's error when
// the read fails.
func (a *App) registeredNHIKey(ctx context.Context, userID string) (ed25519.PublicKey, error) {
	raw, err := a.store.Settings().Get(ctx, nhiKeyPrefix+userID)
	if err != nil {
		return nil, err
	}
	var meta nhiKeyMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil, fmt.Errorf("%w: %v", errNHIKeyMalformed, err)
	}
	key, err := base64.StdEncoding.DecodeString(meta.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errNHIKeyMalformed
	}
	return ed25519.PublicKey(key), nil
}

// The session-end reasons of a key change, and the end of the 500 sentence
// of a key change whose session end did not finish.
const (
	nhiKeyReplacedReason  = "the agent's key was replaced"
	nhiKeyRemovedReason   = "the agent's key was removed"
	nhiKeySessionsLeftMsg = ", but Straza could not end all of the agent's running sessions. " +
		"List them with strazactl sessions list and end each one with strazactl sessions revoke <session-id>."
)

// endAgentSessions ends every active session of u after its assertion key
// changed, and writes one session.end record with the outcome revoked-admin
// and reason for each session it ended. It returns an error when the
// sessions could not be listed or one of them could not be ended.
func (a *App) endAgentSessions(r *http.Request, u store.User, reason string) error {
	sessions, err := a.store.Sessions().ListByUser(r.Context(), u.ID)
	if err != nil {
		return fmt.Errorf("list the sessions of %s: %w", u.ID, err)
	}
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if s.Status == store.SessionActive {
			ids = append(ids, s.ID)
		}
	}
	ended, err := a.endSessions(r.Context(), ids, reason)
	for _, id := range ended {
		a.emitAuthnSessionEnd(r.Context(), r, "revoked-admin", authnFields{
			User: u.Username, UserID: u.ID, Session: id, Reason: reason,
		})
	}
	return err
}
