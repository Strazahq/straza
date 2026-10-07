package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/tokenscopes"
)

// Admin API tokens: the long-lived counterpart of session tokens for
// non-interactive /v1/admin callers (IGA pull connectors, automation).
// Same at-rest model as SCIM tokens: the plaintext is minted exactly once,
// only its SHA-256 lands in settings, verification is one O(1) lookup and a
// leaked database never yields usable tokens. Scope is per-area grants
// (internal/tokenscopes) or `full`, every admin route including minting further
// tokens, so treat full-scope tokens as root credentials.
const (
	apiTokenKeyPrefix = "admin.token." // #nosec G101 -- settings key prefix, not a credential
	apiTokenPrefix    = "wat_"
)

type apiTokenMeta struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Scope   string    `json:"scope"`
	Created time.Time `json:"created"`
	// Lifecycle fields. Expires nil = never (so older stored rows parse
	// unchanged); an expired credential fails closed at
	// verification. LastUsed is stamped at most once per hour so
	// verification stays a single read on the hot path.
	Expires  *time.Time `json:"expires,omitempty"`
	LastUsed *time.Time `json:"lastUsed,omitempty"`
	// CreatedBy names the principal that minted this token: the username of
	// a console or CLI session, or the name of the API token that minted it,
	// the same handle the audit record carries as actor. Empty when no
	// authenticated principal was on the request, and empty on rows stored
	// before the field existed, which list unchanged.
	CreatedBy string `json:"created_by,omitempty"`
}

// apiTokenAuthenticate verifies an admin API token against the stored hash.
func (a *App) apiTokenAuthenticate(ctx context.Context, bearer string) (apiTokenMeta, bool) {
	var meta apiTokenMeta
	sum := sha256.Sum256([]byte(bearer))
	key := apiTokenKeyPrefix + hex.EncodeToString(sum[:])
	raw, err := a.store.Settings().Get(ctx, key)
	if err != nil || json.Unmarshal([]byte(raw), &meta) != nil {
		return apiTokenMeta{}, false
	}
	if meta.Expires != nil && time.Now().After(*meta.Expires) {
		// Fail closed: an expired credential is no credential. The row stays
		// listable (with its deadline) until revoked, so the operator can
		// see WHY a connector died.
		return apiTokenMeta{}, false
	}
	if meta.LastUsed == nil || time.Since(*meta.LastUsed) > time.Hour {
		a.stampAPITokenUse(ctx, key, meta)
	}
	return meta, true
}

// stampAPITokenUse records the use on the token's row, best effort. It
// rewrites the row only while it exists: the copy in hand was read before
// this write, and a revoke that landed in between must stay a revoke.
func (a *App) stampAPITokenUse(ctx context.Context, key string, meta apiTokenMeta) {
	now := time.Now().UTC()
	meta.LastUsed = &now
	if b, err := json.Marshal(meta); err == nil {
		_ = a.store.Settings().Update(ctx, key, string(b))
	}
}

func (a *App) handleAPITokenCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Scope     string `json:"scope"`
		ExpiresIn int64  `json:"expires_in"` // seconds; 0 = never
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		apiError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.ExpiresIn < 0 {
		apiError(w, http.StatusBadRequest, "expires_in must be positive seconds (omit for a non-expiring token)")
		return
	}
	scope, err := tokenscopes.Parse(req.Scope)
	if err != nil {
		// Fail closed at the source: no default scope exists, so the caller
		// says exactly what the credential may touch, or gets nothing.
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Scope = scope.String() // canonical: sorted, deduplicated
	// Names are the operator handle in lists and audit events: a duplicate
	// would make two credentials indistinguishable.
	if existing, err := a.apiTokens(r.Context()); err == nil {
		for _, m := range existing {
			if m.Name == req.Name {
				apiError(w, http.StatusConflict, "an API token named "+req.Name+" already exists. Revoke it first or pick another name")
				return
			}
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "entropy unavailable", err)
		return
	}
	token := apiTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	m := apiTokenMeta{ID: uuid.NewString(), Name: req.Name, Scope: req.Scope, Created: time.Now().UTC()}
	// Attribution comes off the one actor seam requireAdmin fills and every
	// audit record already reads, so a token row and its create record name
	// the same principal. An unknown minter stays empty, never invented.
	if act, ok := actorFrom(r.Context()); ok {
		m.CreatedBy = act.Name
	}
	if req.ExpiresIn > 0 {
		exp := m.Created.Add(time.Duration(req.ExpiresIn) * time.Second)
		m.Expires = &exp
	}
	meta, err := json.Marshal(m)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "encode failed", err)
		return
	}
	if err := a.store.Settings().Set(r.Context(), apiTokenKeyPrefix+hex.EncodeToString(sum[:]), string(meta)); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "store failed", err)
		return
	}
	a.emitEvent(r, "straza.audit.admin", map[string]any{"action": "api-token.create", "name": m.Name, "id": m.ID, "scope": m.Scope})
	// The plaintext appears exactly once, here.
	out := map[string]any{"id": m.ID, "name": m.Name, "scope": m.Scope, "token": token}
	if m.Expires != nil {
		out["expires"] = m.Expires.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusCreated, out)
}

func (a *App) apiTokens(ctx context.Context) (map[string]apiTokenMeta, error) {
	all, err := a.store.Settings().List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]apiTokenMeta{} // settings key → meta
	for k, v := range all {
		if !strings.HasPrefix(k, apiTokenKeyPrefix) {
			continue
		}
		var m apiTokenMeta
		if json.Unmarshal([]byte(v), &m) == nil {
			out[k] = m
		}
	}
	return out, nil
}

func (a *App) handleAPITokenList(w http.ResponseWriter, r *http.Request) {
	tokens, err := a.apiTokens(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list failed", err)
		return
	}
	out := make([]apiTokenMeta, 0, len(tokens))
	for _, m := range tokens {
		out = append(out, m)
	}
	// Newest first, name tie-break: the source is a map, and an unsorted
	// list reshuffles on every read.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].Name < out[j].Name
		}
		return out[i].Created.After(out[j].Created)
	})
	if p, ok := parsePageParams(w, r); p.paged || !ok {
		if !ok {
			return
		}
		page, next, valid := pageAfter(out, func(m apiTokenMeta) string { return m.ID }, p.before, p.limit)
		if !valid {
			apiError(w, http.StatusBadRequest, "invalid or stale cursor: re-fetch from the start")
			return
		}
		writePage(w, page, next)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleAPITokenRevoke(w http.ResponseWriter, r *http.Request) {
	tokens, err := a.apiTokens(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list failed", err)
		return
	}
	id := r.PathValue("id")
	for key, m := range tokens {
		if m.ID == id {
			if err := a.store.Settings().Delete(r.Context(), key); err != nil {
				a.fail(w, r, http.StatusInternalServerError, "revoke failed", err)
				return
			}
			a.emitEvent(r, "straza.audit.admin", map[string]any{"action": "api-token.revoke", "id": id})
			writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "revoked"})
			return
		}
	}
	apiError(w, http.StatusNotFound, "no admin API token with that id (strazactl api-token list)")
}
