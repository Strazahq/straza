package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// StaticSecret describes one stored static secret without its value: the
// row id, its scope (app or role), the owning role id (empty on the app
// row), a fingerprint of the plaintext and when it was last set.
type StaticSecret struct {
	ID          string
	Scope       string
	RoleID      string
	Fingerprint string
	SetAt       time.Time
}

// Fingerprint returns the first four hex characters of the SHA-256 of a
// secret value: enough for an operator to recognise which value is stored,
// far too little to recover it.
func Fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:4]
}

// Statics lists an app's static secrets (control plane), app row first and
// then role rows in creation order. Each fingerprint is computed in memory
// from the opened payload; a payload that cannot be opened lists with an
// empty fingerprint rather than hiding the row.
func (b *Broker) Statics(ctx context.Context, appID string) ([]StaticSecret, error) {
	rows, err := b.store.Credentials().ListByApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	var out []StaticSecret
	for _, c := range rows {
		if c.Kind != store.CredStatic || (c.Scope != store.CredScopeApp && c.Scope != store.CredScopeRole) {
			continue
		}
		s := StaticSecret{ID: c.ID, Scope: c.Scope, SetAt: c.CreatedAt}
		if c.Scope == store.CredScopeRole {
			s.RoleID = c.OwnerID
		}
		if c.RotatedAt != nil {
			s.SetAt = *c.RotatedAt
		}
		if plain, err := b.provider.Open(c.EncPayload); err == nil {
			s.Fingerprint = Fingerprint(string(plain))
		}
		if c.Scope == store.CredScopeApp {
			out = append([]StaticSecret{s}, out...)
		} else {
			out = append(out, s)
		}
	}
	return out, nil
}

// Remove deletes one static secret (control plane), the server's own row
// when roleID is empty or the role's override otherwise, then refreshes the
// cache. It answers store.ErrNotFound when no such row exists.
func (b *Broker) Remove(ctx context.Context, appID, roleID string) (store.Credential, error) {
	scope, owner := staticScope(appID, roleID)
	rows, err := b.store.Credentials().ListByApp(ctx, appID)
	if err != nil {
		return store.Credential{}, err
	}
	for _, c := range rows {
		if c.Scope == scope && c.OwnerID == owner && c.Kind == store.CredStatic {
			if err := b.store.Credentials().Delete(ctx, c.ID); err != nil {
				return store.Credential{}, err
			}
			return c, b.Refresh(ctx)
		}
	}
	return store.Credential{}, store.ErrNotFound
}

// RemoveAll deletes every static secret of an app (app remove), then
// refreshes the cache. OAuth grants ride the app row's cascade.
func (b *Broker) RemoveAll(ctx context.Context, appID string) error {
	rows, err := b.store.Credentials().ListByApp(ctx, appID)
	if err != nil {
		return err
	}
	for _, c := range rows {
		if c.Kind != store.CredStatic {
			continue
		}
		if err := b.store.Credentials().Delete(ctx, c.ID); err != nil {
			return err
		}
	}
	return b.Refresh(ctx)
}
