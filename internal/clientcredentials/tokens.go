// Package clientcredentials gets an agent's own upstream token. It runs the
// OAuth client credentials grant (RFC 6749 section 4.4) at the customer's
// identity provider, authenticated by a client assertion that Straza signs
// (RFC 7523 section 2.2), and keeps each token in memory until it expires.
// Nothing here is stored, and no token or assertion is logged or put into an
// error.
package clientcredentials

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	// fetchLimit bounds one token request. The refusal sentence names it.
	fetchLimit = 3 * time.Second
	// renewWindow is how long before its expiry a served token starts its
	// renewal, so steady calls never wait on the provider.
	renewWindow = 60 * time.Second
	// maxSessions bounds the sessions an entry remembers for DropSession.
	maxSessions = 64
	// sweepEvery paces the removal of dead entries.
	sweepEvery = time.Minute
	// pauseFirst and pauseMax bound the pause after a refused or failed
	// request, which doubles from the first to the second.
	pauseFirst = time.Second
	pauseMax   = 30 * time.Second
)

// Signer signs the client assertion of RFC 7523 section 2.2 for clientID. The
// production signer is *authn.ClientAssertionKeys.
type Signer interface {
	SignAssertion(now time.Time, clientID, audience string) (string, error)
}

// Provider is one identity provider whose operator settings say that it
// trusts Straza's client assertion keys. Every field comes from strazad's
// config and none from a manifest.
type Provider struct {
	TokenURL string
	Audience string
	Scopes   []string
}

// Options configures Tokens.
type Options struct {
	// Providers holds the providers with a clientCredentials block, by the
	// name a manifest uses.
	Providers map[string]Provider
	Signer    Signer
	// KeysURL is the address of the client assertion key document, which the
	// refusal for an unknown client hands to the identity team.
	KeysURL string
	Log     *slog.Logger
	// Transport replaces the default transport, for a test that must trust a
	// test certificate. The client around it is always built here.
	Transport http.RoundTripper
	// Now replaces time.Now in tests.
	Now func() time.Time
}

// Request names whose token is wanted. ClientID must be the username of the
// verified session subject, because it becomes the client the provider
// authenticates. Every field is required.
type Request struct {
	Provider string // the provider name of the server's manifest
	Server   string // the server's name, for the sentences
	ServerID string
	UserID   string
	ClientID string
	Session  string
}

// key identifies one cached token. It holds the client id beside the user id
// so that a served token was always fetched for the client the request names.
type key struct{ provider, server, user, client string }

// entry is the cache state of one key. Tokens.mu guards it.
type entry struct {
	token    string
	expiry   time.Time
	sessions map[string]struct{} // sessions served from this entry
	overflow bool                // more sessions than maxSessions used it
	flight   *flight             // the one fetch in progress, or nil
	failures int                 // consecutive refused or failed requests
	retryAt  time.Time           // no request before this time
	lastErr  error               // what the pause answers
}

// flight is one fetch and its result, which is complete once done is closed.
type flight struct {
	done  chan struct{}
	token string
	err   error
}

// Tokens fetches and caches agents' upstream tokens. Each replica holds its
// own cache. It is safe for concurrent use.
type Tokens struct {
	providers map[string]Provider
	signer    Signer
	keysURL   string
	log       *slog.Logger
	http      *http.Client
	now       func() time.Time
	limit     time.Duration

	mu        sync.Mutex
	entries   map[key]*entry
	lastSweep time.Time
}

// New builds Tokens. The HTTP client follows no redirect, verifies TLS with
// the machine's trust store and carries no cookie jar.
func New(opts Options) *Tokens {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Transport == nil {
		opts.Transport = http.DefaultTransport
	}
	return &Tokens{
		providers: opts.Providers, signer: opts.Signer, keysURL: opts.KeysURL, log: opts.Log, now: opts.Now,
		limit: fetchLimit,
		http: &http.Client{
			Transport:     opts.Transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		entries: map[key]*entry{},
	}
}

// Token returns the agent's token for the server. A token with more than
// renewWindow left is served from memory. Inside the window it is served and
// one renewal starts in the background. With no live token the call waits for
// the one fetch of its key, which other callers share and which survives a
// caller that hangs up. Every failure is a refusal sentence and no token.
func (t *Tokens) Token(ctx context.Context, r Request) (string, error) {
	if r.Provider == "" || r.Server == "" || r.ServerID == "" || r.UserID == "" || r.ClientID == "" || r.Session == "" {
		return "", refuse(r, "Straza could not tell which agent, server or session the call belongs to, so the call is refused. Check in again, then call again.")
	}
	p, ok := t.providers[r.Provider]
	if !ok {
		return "", noSettings(r)
	}
	k := key{provider: r.Provider, server: r.ServerID, user: r.UserID, client: r.ClientID}

	t.mu.Lock()
	now := t.now()
	t.sweep(now)
	e := t.entries[k]
	if e == nil {
		e = &entry{sessions: map[string]struct{}{}}
		t.entries[k] = e
	}
	e.note(r.Session)
	if e.token != "" && now.Before(e.expiry) {
		if e.expiry.Sub(now) <= renewWindow && e.flight == nil && !now.Before(e.retryAt) {
			t.start(k, e, p, r)
		}
		token := e.token
		t.mu.Unlock()
		return token, nil
	}
	e.token = ""
	if e.flight == nil {
		if now.Before(e.retryAt) {
			err := paused(e.lastErr, r.Provider, e.retryAt.Sub(now))
			t.mu.Unlock()
			return "", err
		}
		t.start(k, e, p, r)
	}
	f := e.flight
	t.mu.Unlock()

	select {
	case <-f.done:
		return f.token, f.err
	case <-ctx.Done():
		return "", refuse(r, "The call ended before %s answered. Call again.", r.Provider)
	}
}

// start begins the one fetch of e and returns at once. The caller holds t.mu.
// The fetch runs on its own context, so it outlives the caller that began it
// and its result serves every waiter. A result for an entry that was dropped
// meanwhile is neither kept nor handed out.
func (t *Tokens) start(k key, e *entry, p Provider, r Request) {
	f := &flight{done: make(chan struct{})}
	e.flight = f
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), t.limit)
		defer cancel()
		// The lifetime counts from before the request, so the entry never
		// outlives the token by the time the request took.
		sent := t.now()
		token, lifetime, err := t.fetch(ctx, p, r)

		t.mu.Lock()
		e.flight = nil
		var rf *refusal
		switch {
		case t.entries[k] != e:
			f.err = refuse(r, "The agent was disabled or its session was revoked while Straza asked %s, so the call is refused. An administrator checks the agent's status, then the agent calls again.", r.Provider)
		case err == nil:
			e.token, e.expiry = token, sent.Add(lifetime)
			e.failures, e.retryAt, e.lastErr = 0, time.Time{}, nil
			f.token = token
		case asRefusal(err, &rf) && rf.asked:
			e.failures++
			e.retryAt, e.lastErr = t.now().Add(pause(e.failures)), err
			f.err = err
		default:
			f.err = err
		}
		t.mu.Unlock()
		close(f.done)
	}()
}

// pause is how long a key stays quiet after its n-th consecutive failure.
func pause(n int) time.Duration {
	d := pauseFirst
	for ; n > 1 && d < pauseMax; n-- {
		d *= 2
	}
	return min(d, pauseMax)
}

// note remembers that session used the entry, up to maxSessions.
func (e *entry) note(session string) {
	if _, ok := e.sessions[session]; ok || e.overflow {
		return
	}
	if len(e.sessions) >= maxSessions {
		e.overflow = true
		return
	}
	e.sessions[session] = struct{}{}
}

// sweep removes entries that serve nothing any more: no live token, no fetch
// in flight and no pause left to remember. The caller holds t.mu.
func (t *Tokens) sweep(now time.Time) {
	if now.Sub(t.lastSweep) < sweepEvery {
		return
	}
	t.lastSweep = now
	for k, e := range t.entries {
		live := e.token != "" && now.Before(e.expiry)
		if !live && e.flight == nil && !now.Before(e.retryAt.Add(pauseMax)) {
			delete(t.entries, k)
		}
	}
}

// DropUser forgets every token of the user, for a disabled or locked agent.
func (t *Tokens) DropUser(userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.entries {
		if k.user == userID {
			delete(t.entries, k)
		}
	}
}

// DropSession forgets every token the session used. An entry that more than
// maxSessions sessions used no longer knows them all and is dropped too.
func (t *Tokens) DropSession(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.entries {
		if _, used := e.sessions[sessionID]; used || e.overflow {
			delete(t.entries, k)
		}
	}
}
