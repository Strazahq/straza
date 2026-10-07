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
func (a *App) handleNHIKeySet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PublicKey == "" {
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

// handleNHIKeyDelete revokes an NHI's assertion key: the next grant fails.
// Running sessions die through the normal session/user kill-switch lanes.
// A user with no key answers 404 and writes no record, because nothing
// changed.
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
	raw, err := a.store.Settings().Get(ctx, nhiKeyPrefix+u.ID)
	if err != nil {
		return store.User{}, nil, fmt.Errorf("nhi lookup: no key registered: %w", err)
	}
	var meta nhiKeyMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return store.User{}, nil, fmt.Errorf("nhi lookup: stored key unreadable: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(meta.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return store.User{}, nil, fmt.Errorf("nhi lookup: stored key malformed")
	}
	return u, ed25519.PublicKey(key), nil
}
