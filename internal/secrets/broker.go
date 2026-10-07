package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// Broker resolves the server's own static secret, role-bound static secrets
// and each user's own rows, OAuth grants and pasted tokens, for the gateway
// and manager (manager.SecretSource). It caches credential rows
// (ciphertext) in memory. Refresh runs on the control plane (boot, secret
// set, connect) so ForRoles/ForUser/AppSecret never read the database on a
// request path. Decryption happens per resolution, keeping plaintext
// lifetimes short.
type Broker struct {
	store    store.Store
	provider Provider

	mu     sync.RWMutex
	byApp  map[string][]cached               // app id → role statics (sorted by role name)
	appRow map[string]cached                 // app id → the app-scoped static (the server's own secret)
	grants map[string]map[string]cachedGrant // app id → user id → the user's own row
}

type cached struct {
	id       string
	roleName string
	payload  []byte // ciphertext
}

type cachedGrant struct {
	id          string
	kind        string     // store.CredOAuth (Grant JSON payload) or store.CredToken (raw value)
	payload     []byte     // ciphertext
	expiresAt   *time.Time // nil = non-expiring
	allowAgents bool
}

// NewBroker builds a broker; call Refresh before serving.
func NewBroker(st store.Store, provider Provider) *Broker {
	return &Broker{store: st, provider: provider,
		byApp: map[string][]cached{}, appRow: map[string]cached{}, grants: map[string]map[string]cachedGrant{}}
}

// Refresh reloads every app's credential rows into memory (control plane).
func (b *Broker) Refresh(ctx context.Context) error {
	roles, err := b.store.Roles().List(ctx)
	if err != nil {
		return fmt.Errorf("secrets: refresh roles: %w", err)
	}
	roleName := make(map[string]string, len(roles))
	for _, r := range roles {
		roleName[r.ID] = r.Name
	}

	apps, err := b.store.Apps().List(ctx)
	if err != nil {
		return fmt.Errorf("secrets: refresh apps: %w", err)
	}
	next := map[string][]cached{}
	nextApp := map[string]cached{}
	nextGrants := map[string]map[string]cachedGrant{}
	for _, app := range apps {
		creds, err := b.store.Credentials().ListByApp(ctx, app.ID)
		if err != nil {
			return fmt.Errorf("secrets: refresh credentials for %s: %w", app.Name, err)
		}
		var cs []cached
		for _, c := range creds {
			switch {
			case c.Scope == store.CredScopeApp && c.Kind == store.CredStatic:
				nextApp[app.ID] = cached{id: c.ID, payload: c.EncPayload}
			case c.Scope == store.CredScopeRole && c.Kind == store.CredStatic:
				name, ok := roleName[c.OwnerID]
				if !ok {
					continue // orphaned owner; unreachable is safer than guessing
				}
				cs = append(cs, cached{id: c.ID, roleName: name, payload: c.EncPayload})
			case c.Scope == store.CredScopeUser && isUserKind(c.Kind):
				var meta GrantMeta
				if err := json.Unmarshal([]byte(c.OAuthMeta), &meta); err != nil {
					continue // unreadable meta: treat as absent, deny later (fail closed)
				}
				if nextGrants[app.ID] == nil {
					nextGrants[app.ID] = map[string]cachedGrant{}
				}
				nextGrants[app.ID][c.OwnerID] = cachedGrant{
					id: c.ID, kind: c.Kind, payload: c.EncPayload,
					expiresAt: meta.ExpiresAt, allowAgents: meta.AllowAgents,
				}
			}
		}
		// Deterministic resolution order (documented: lexicographically
		// smallest role name wins ties).
		sort.Slice(cs, func(i, j int) bool { return cs[i].roleName < cs[j].roleName })
		if len(cs) > 0 {
			next[app.ID] = cs
		}
	}

	b.mu.Lock()
	b.byApp = next
	b.appRow = nextApp
	b.grants = nextGrants
	b.mu.Unlock()
	return nil
}

// AppSecret returns the app-level credential used for inventory and health:
// the server's own (app-scoped) secret when one is stored, else the first
// role row in role-name order, else nil. In-memory only (request-path safe).
func (b *Broker) AppSecret(appID string) *manager.Secret {
	b.mu.RLock()
	app, hasApp := b.appRow[appID]
	cs := b.byApp[appID]
	b.mu.RUnlock()
	if hasApp {
		return b.decrypt(app)
	}
	if len(cs) == 0 {
		return nil
	}
	return b.decrypt(cs[0])
}

// ForRoles resolves the credential for a session holding the given role
// names: the row of grantingRole (the role whose access row admitted the
// call) when it has one, else the held role with the lexicographically
// smallest name that has one, else the server's own secret, else nil.
// In-memory only.
func (b *Broker) ForRoles(appID string, roleNames []string, grantingRole string) *manager.Secret {
	held := make(map[string]bool, len(roleNames))
	for _, r := range roleNames {
		held[r] = true
	}
	b.mu.RLock()
	cs := b.byApp[appID]
	app, hasApp := b.appRow[appID]
	b.mu.RUnlock()
	for _, c := range cs {
		if c.roleName == grantingRole {
			return b.decrypt(c)
		}
	}
	for _, c := range cs { // sorted by role name
		if held[c.roleName] {
			return b.decrypt(c)
		}
	}
	if hasApp {
		return b.decrypt(app)
	}
	return nil
}

func (b *Broker) decrypt(c cached) *manager.Secret {
	plain, err := b.provider.Open(c.payload)
	if err != nil {
		// Fail closed: an undecryptable credential resolves to nothing; the
		// caller denies rather than calling upstream uncredentialed.
		return nil
	}
	return &manager.Secret{ID: c.id, Value: string(plain)}
}

// Set seals and upserts a static secret for an app (control plane), then
// refreshes the cache. An empty roleID stores the server's own secret (one
// app-scoped row owned by the app); a role id stores that role's override.
func (b *Broker) Set(ctx context.Context, appID, roleID, value string) (store.Credential, error) {
	sealed, err := b.provider.Seal([]byte(value))
	if err != nil {
		return store.Credential{}, err
	}
	scope, owner := staticScope(appID, roleID)
	existing, err := b.store.Credentials().ListByApp(ctx, appID)
	if err != nil {
		return store.Credential{}, err
	}
	var row store.Credential
	for _, c := range existing {
		if c.Scope == scope && c.OwnerID == owner && c.Kind == store.CredStatic {
			c.EncPayload = sealed
			row, err = b.store.Credentials().Update(ctx, c)
			if err != nil {
				return store.Credential{}, err
			}
			return row, b.Refresh(ctx)
		}
	}
	row, err = b.store.Credentials().Create(ctx, store.Credential{
		AppID: appID, Scope: scope, OwnerID: owner,
		Kind: store.CredStatic, EncPayload: sealed,
	})
	if err != nil {
		return store.Credential{}, err
	}
	return row, b.Refresh(ctx)
}

// staticScope maps the optional role id of a static secret onto the row's
// scope and owner: the app itself when roleID is empty, else the role.
func staticScope(appID, roleID string) (scope, owner string) {
	if roleID == "" {
		return store.CredScopeApp, appID
	}
	return store.CredScopeRole, roleID
}

// isUserKind reports whether a credential kind is a user's own row: an
// OAuth grant or a pasted token.
func isUserKind(kind string) bool {
	return kind == store.CredOAuth || kind == store.CredToken
}

// ForUser resolves one user's own row for an app: the OAuth access token or
// the pasted token, with Present set whenever a row exists. The secret is
// nil when the user has no row, the row expired by its stored date, or the
// payload cannot be opened, and the caller denies in every nil case (fail
// closed). An expired OAuth grant comes back once the refresh worker
// rotates it. In-memory only (request-path safe).
func (b *Broker) ForUser(appID, userID string) manager.UserCredential {
	b.mu.RLock()
	g, ok := b.grants[appID][userID]
	b.mu.RUnlock()
	if !ok {
		return manager.UserCredential{}
	}
	out := manager.UserCredential{Present: true, ExpiresAt: g.expiresAt, AllowAgents: g.allowAgents}
	if g.expiresAt != nil && !time.Now().Before(*g.expiresAt) {
		return out
	}
	plain, err := b.provider.Open(g.payload)
	if err != nil {
		return out
	}
	value := string(plain)
	if g.kind == store.CredOAuth {
		var grant Grant
		if err := json.Unmarshal(plain, &grant); err != nil || grant.AccessToken == "" {
			return out
		}
		value = grant.AccessToken
	}
	out.Secret = &manager.Secret{ID: g.id, Value: value}
	return out
}

// SetToken seals and upserts one user's pasted token for an app (control
// plane: the connect token lane), then refreshes the cache. The meta
// carries the expiry the user typed, who set the row and the agents opt-in.
func (b *Broker) SetToken(ctx context.Context, appID, userID, value string, meta GrantMeta) (store.Credential, error) {
	sealed, err := b.provider.Seal([]byte(value))
	if err != nil {
		return store.Credential{}, err
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return store.Credential{}, err
	}
	row, err := b.upsertUserRow(ctx, appID, userID, store.CredToken, sealed, string(metaJSON), time.Now().UTC())
	if err != nil {
		return store.Credential{}, err
	}
	return row, b.Refresh(ctx)
}

// SetAllowAgents records the owner's opt-in on their own row for an app,
// whichever user kind it is, then refreshes the cache. It answers
// store.ErrNotFound when the user has no row on the app.
func (b *Broker) SetAllowAgents(ctx context.Context, appID, userID string, allow bool) (store.Credential, error) {
	existing, err := b.store.Credentials().ListByApp(ctx, appID)
	if err != nil {
		return store.Credential{}, err
	}
	for _, c := range existing {
		if c.Scope != store.CredScopeUser || c.OwnerID != userID || !isUserKind(c.Kind) {
			continue
		}
		var meta GrantMeta
		if c.OAuthMeta != "" {
			if err := json.Unmarshal([]byte(c.OAuthMeta), &meta); err != nil {
				return store.Credential{}, fmt.Errorf("secrets: read row meta: %w", err)
			}
		}
		meta.AllowAgents = allow
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			return store.Credential{}, err
		}
		c.OAuthMeta = string(metaJSON)
		row, err := b.store.Credentials().Update(ctx, c)
		if err != nil {
			return store.Credential{}, err
		}
		return row, b.Refresh(ctx)
	}
	return store.Credential{}, store.ErrNotFound
}

// UserRow describes one user's own row on an app without its value: the
// row id, its kind, the meta, a fingerprint of a pasted token (empty on an
// OAuth grant) and when it was last set.
type UserRow struct {
	ID          string
	Kind        string
	Meta        GrantMeta
	Fingerprint string
	SetAt       time.Time
}

// UserRowFor lists one user's own row on an app (control plane), or ok
// false when none exists. A payload that cannot be opened lists with an
// empty fingerprint rather than hiding the row.
func (b *Broker) UserRowFor(ctx context.Context, appID, userID string) (UserRow, bool, error) {
	rows, err := b.store.Credentials().ListByApp(ctx, appID)
	if err != nil {
		return UserRow{}, false, err
	}
	for _, c := range rows {
		if c.Scope != store.CredScopeUser || c.OwnerID != userID || !isUserKind(c.Kind) {
			continue
		}
		out := UserRow{ID: c.ID, Kind: c.Kind, SetAt: c.CreatedAt}
		if c.RotatedAt != nil {
			out.SetAt = *c.RotatedAt
		}
		if c.OAuthMeta != "" {
			_ = json.Unmarshal([]byte(c.OAuthMeta), &out.Meta)
		}
		if c.Kind == store.CredToken {
			if plain, err := b.provider.Open(c.EncPayload); err == nil {
				out.Fingerprint = Fingerprint(string(plain))
			}
		}
		return out, true, nil
	}
	return UserRow{}, false, nil
}

// SetGrant seals and upserts one user's OAuth grant for an app (control
// plane: connect callback and refresh worker), then refreshes the cache.
func (b *Broker) SetGrant(ctx context.Context, appID, userID string, g Grant, meta GrantMeta) (store.Credential, error) {
	plain, err := json.Marshal(g) // #nosec G117 -- serialized only to be sealed on the next line
	if err != nil {
		return store.Credential{}, err
	}
	sealed, err := b.provider.Seal(plain)
	if err != nil {
		return store.Credential{}, err
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return store.Credential{}, err
	}
	now := time.Now().UTC()
	row, err := b.upsertUserRow(ctx, appID, userID, store.CredOAuth, sealed, string(metaJSON), now)
	if err != nil {
		return store.Credential{}, err
	}
	return row, b.Refresh(ctx)
}

// upsertUserRow writes one user's own row on an app. A user holds one row
// per app whatever its kind: a row of the same kind rotates in place, and a
// row of the other kind (the app's manifest changed kind) is deleted before
// the new one is created, since the store never rewrites a row's kind.
func (b *Broker) upsertUserRow(ctx context.Context, appID, userID, kind string, sealed []byte, metaJSON string, now time.Time) (store.Credential, error) {
	existing, err := b.store.Credentials().ListByApp(ctx, appID)
	if err != nil {
		return store.Credential{}, err
	}
	for _, c := range existing {
		if c.Scope != store.CredScopeUser || c.OwnerID != userID || !isUserKind(c.Kind) {
			continue
		}
		if c.Kind == kind {
			c.EncPayload = sealed
			c.OAuthMeta = metaJSON
			c.RotatedAt = &now
			return b.store.Credentials().Update(ctx, c)
		}
		if err := b.store.Credentials().Delete(ctx, c.ID); err != nil {
			return store.Credential{}, err
		}
	}
	return b.store.Credentials().Create(ctx, store.Credential{
		AppID: appID, Scope: store.CredScopeUser, OwnerID: userID,
		Kind: kind, EncPayload: sealed, OAuthMeta: metaJSON,
	})
}

// DeleteGrant removes one user's own row for an app (disconnect), OAuth
// grant or pasted token, then refreshes the cache. Deleting an absent row
// is a no-op.
func (b *Broker) DeleteGrant(ctx context.Context, appID, userID string) error {
	existing, err := b.store.Credentials().ListByApp(ctx, appID)
	if err != nil {
		return err
	}
	for _, c := range existing {
		if c.Scope == store.CredScopeUser && c.OwnerID == userID && isUserKind(c.Kind) {
			if err := b.store.Credentials().Delete(ctx, c.ID); err != nil {
				return err
			}
			return b.Refresh(ctx)
		}
	}
	return nil
}
