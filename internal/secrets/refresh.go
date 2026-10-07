package secrets

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// RefresherOpts configures the OAuth refresh worker.
type RefresherOpts struct {
	Store     store.Store
	Broker    *Broker
	Providers map[string]ProviderConfig // provider name → client registration
	HTTP      *http.Client              // default http.DefaultClient
	Log       *slog.Logger
	Interval  time.Duration // pass cadence, default 60 s
	Window    time.Duration // rotate grants expiring within this, default 10 min
}

// Refresher rotates expiring per-user OAuth grants in the background
// (control plane: DB reads and writes are fine here, and the request path
// keeps serving from the broker cache). All worker state derives from
// credential rows, so a strazad restart resumes refreshing with nothing to
// replay.
type Refresher struct {
	opts RefresherOpts
}

// NewRefresher builds a refresher; Run starts the loop.
func NewRefresher(opts RefresherOpts) *Refresher {
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Minute
	}
	if opts.Window <= 0 {
		opts.Window = 10 * time.Minute
	}
	return &Refresher{opts: opts}
}

// Run loops RefreshDue until the context ends. The first pass runs
// immediately so grants that went stale while strazad was down come back
// before their users hit a deny.
func (r *Refresher) Run(ctx context.Context) {
	for {
		if _, err := r.RefreshDue(ctx); err != nil && ctx.Err() == nil {
			r.opts.Log.Warn("oauth refresh pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.opts.Interval):
		}
	}
}

// RefreshDue runs one pass: every user OAuth grant expiring within the
// window (or already expired) that has a refresh token and a configured
// provider is rotated at the provider and re-sealed. Per-grant failures are
// logged and skipped: a provider outage must not wedge the pass, and a
// stale grant keeps failing closed at resolution until a later pass wins.
func (r *Refresher) RefreshDue(ctx context.Context) (int, error) {
	apps, err := r.opts.Store.Apps().List(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(r.opts.Window)
	refreshed := 0
	for _, app := range apps {
		creds, err := r.opts.Store.Credentials().ListByApp(ctx, app.ID)
		if err != nil {
			return refreshed, err
		}
		for _, c := range creds {
			if c.Scope != store.CredScopeUser || c.Kind != store.CredOAuth {
				continue
			}
			if r.refreshOne(ctx, app, c, cutoff) {
				refreshed++
			}
		}
	}
	return refreshed, nil
}

func (r *Refresher) refreshOne(ctx context.Context, app store.App, c store.Credential, cutoff time.Time) bool {
	var meta GrantMeta
	if err := json.Unmarshal([]byte(c.OAuthMeta), &meta); err != nil {
		return false // unreadable meta: resolution already fails closed on it
	}
	if meta.ExpiresAt == nil || meta.ExpiresAt.After(cutoff) {
		return false // non-expiring or not due yet
	}
	plain, err := r.opts.Broker.provider.Open(c.EncPayload)
	if err != nil {
		r.opts.Log.Warn("oauth grant undecryptable, cannot refresh",
			"app", app.Name, "credential", c.ID, "err", err)
		return false
	}
	var grant Grant
	if err := json.Unmarshal(plain, &grant); err != nil || grant.RefreshToken == "" {
		return false // no refresh handle: token dies at its natural expiry
	}
	p, ok := r.opts.Providers[meta.Provider]
	if !ok {
		r.opts.Log.Warn("oauth grant references an unconfigured provider",
			"app", app.Name, "provider", meta.Provider)
		return false
	}
	next, expiry, err := RefreshGrant(ctx, r.opts.HTTP, p, grant.RefreshToken)
	if err != nil {
		r.opts.Log.Warn("oauth refresh failed; grant stays stale (denies until reconnect or retry)",
			"app", app.Name, "provider", meta.Provider, "err", err)
		return false
	}
	meta.ExpiresAt = expiry
	if _, err := r.opts.Broker.SetGrant(ctx, app.ID, c.OwnerID, next, meta); err != nil {
		r.opts.Log.Warn("oauth refresh could not persist", "app", app.Name, "err", err)
		return false
	}
	return true
}
