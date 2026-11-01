// Package objects keeps each MCP server's object list: the objects its
// listing tool returns, listed in the background with the server's own
// listing credential, stored with a version, audited, and held in memory
// on every replica so a decision never reads the database for it.
package objects

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// Caller is the manager surface a listing uses: the server's live tools,
// to check the listing tool before each listing, and the listing call.
type Caller interface {
	LiveInventory(appName string) ([]*mcp.Tool, error)
	CallListing(ctx context.Context, appName, tool string, args json.RawMessage, secret *manager.Secret) (*mcp.CallToolResult, error)
}

// Secrets answers a server's listing credential, nil when none is stored.
type Secrets interface {
	ListingSecret(appID string) *manager.Secret
}

// Options are what New needs. Owner names this replica in the claim of a
// listing. Record writes one actorless straza.audit.admin record. OnRenamed,
// when set, hears the ids a listing gave a new value, once per listing, on
// the replica that ran it. Off stops every listing, as
// apps.objects.enabled false asks.
type Options struct {
	Store     store.Store
	Caller    Caller
	Secrets   Secrets
	Owner     string
	Record    func(ctx context.Context, data map[string]any)
	OnRenamed func(ctx context.Context, appID string, ids []string)
	Log       *slog.Logger
	Off       bool
}

// ErrTooSoon refuses a refresh of a server that was listed within the
// last minute.
var ErrTooSoon = errors.New("objects: listed within the last minute")

// ErrOff refuses a refresh or a mark while the lists are off, because no
// listing runs then.
var ErrOff = errors.New("objects: listing is off")

// Lists holds every server's object list in memory and runs the loop that
// keeps it current. Get, Missed, Refresh and MarkDue are safe for
// concurrent use.
type Lists struct {
	opts Options

	// every is the loop's tick, interval the time between two listings of
	// one server, and claimFor how long a claim holds. pageTimeout and
	// listTimeout bound one page and one listing. Tests shorten them.
	every, interval, claimFor time.Duration
	pageTimeout, listTimeout  time.Duration
	now                       func() time.Time

	mu    sync.RWMutex
	lists map[string]*List

	// loadMu orders every write of lists with the store read it comes
	// from, so an older read never replaces a newer one.
	loadMu sync.Mutex

	missMu   sync.Mutex
	missAt   map[string]time.Time
	missed   map[string]bool
	inflight map[string]bool
	wake     chan struct{}
	wg       sync.WaitGroup
}

// New builds the object lists of one replica; Run keeps them current.
func New(opts Options) *Lists {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &Lists{opts: opts,
		every: 15 * time.Second, interval: 10 * time.Minute, claimFor: 6 * time.Minute,
		pageTimeout: 30 * time.Second, listTimeout: 5 * time.Minute,
		now:   func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
		lists: map[string]*List{}, missAt: map[string]time.Time{}, missed: map[string]bool{},
		inflight: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// SetPace replaces the loop's tick and its clock. It must be called before
// Run. Tests use it to stretch the tick, so the loop lists only when a mark
// or a miss wakes it, and to move the clock past the minute that limits
// listings.
func (l *Lists) SetPace(every time.Duration, now func() time.Time) {
	l.every, l.now = every, now
}

// Get answers the object list of appID as this replica holds it, nil when
// it holds none. The List is never changed after Get answers it. In-memory
// only.
func (l *Lists) Get(appID string) *List {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.lists[appID]
}

// Missed says a call named an object of appID the list does not hold. At
// most one a minute per server reaches the loop, which lists the server
// again unless it was listed within the last minute. It never blocks.
func (l *Lists) Missed(appID string) {
	now := l.now()
	l.missMu.Lock()
	if last, ok := l.missAt[appID]; ok && now.Sub(last) < time.Minute {
		l.missMu.Unlock()
		return
	}
	l.missAt[appID] = now
	l.missed[appID] = true
	l.missMu.Unlock()
	l.poke()
}

// Refresh asks for a listing of appID now. It answers when the listing is
// due, or ErrTooSoon with how long ago the last listing ran when that was
// within the last minute. A server whose list the loop has not made yet is
// due at once. While the lists are off it answers ErrOff and writes
// nothing.
func (l *Lists) Refresh(ctx context.Context, appID string) (due time.Time, since time.Duration, err error) {
	if l.opts.Off {
		return time.Time{}, 0, ErrOff
	}
	now := l.now()
	ok, err := l.opts.Store.ObjectLists().MarkDueIfIdle(ctx, appID, now, now.Add(-time.Minute))
	switch {
	case errors.Is(err, store.ErrNotFound):
		l.poke()
		return now, 0, nil
	case err != nil:
		return time.Time{}, 0, err
	case ok:
		l.poke()
		return now, 0, nil
	}
	row, err := l.opts.Store.ObjectLists().Get(ctx, appID)
	if err != nil {
		return time.Time{}, 0, err
	}
	if row.AttemptedAt != nil {
		since = now.Sub(*row.AttemptedAt)
	}
	return time.Time{}, since, ErrTooSoon
}

// MarkDue makes the listing of appID due at once, as a change of its
// listing credential asks. A server without a list yet needs no mark.
// While the lists are off it answers ErrOff and writes nothing.
func (l *Lists) MarkDue(ctx context.Context, appID string) error {
	if l.opts.Off {
		return ErrOff
	}
	err := l.opts.Store.ObjectLists().MarkDue(ctx, appID, l.now())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	l.poke()
	return nil
}

// poke wakes the loop without blocking.
func (l *Lists) poke() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}
