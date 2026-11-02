package secrets

import (
	"context"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// ListingSecret answers the listing credential of appID, the one secret the
// call that lists the server's objects carries: exactly the listing row,
// or nil when none is stored or it cannot be opened. It never answers the
// server's own secret or a role's, so a listing without its own credential
// fails rather than running on someone else's. In-memory only.
func (b *Broker) ListingSecret(appID string) *manager.Secret {
	b.mu.RLock()
	c, ok := b.listing[appID]
	b.mu.RUnlock()
	if !ok {
		return nil
	}
	return b.decrypt(c)
}

// SetListing seals and upserts the listing credential of appID (control
// plane), one row owned by the app, then refreshes the cache.
func (b *Broker) SetListing(ctx context.Context, appID, value string) (store.Credential, error) {
	return b.upsertStatic(ctx, appID, store.CredScopeListing, appID, value)
}

// DeleteListing removes the listing credential of appID (control plane),
// then refreshes the cache. It answers store.ErrNotFound when none is
// stored.
func (b *Broker) DeleteListing(ctx context.Context, appID string) error {
	rows, err := b.store.Credentials().ListByApp(ctx, appID)
	if err != nil {
		return err
	}
	for _, c := range rows {
		if c.Scope == store.CredScopeListing && c.Kind == store.CredStatic {
			if err := b.store.Credentials().Delete(ctx, c.ID); err != nil {
				return err
			}
			return b.Refresh(ctx)
		}
	}
	return store.ErrNotFound
}
