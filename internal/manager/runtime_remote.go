package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// errNoCredential refuses a call or a dial for an app whose manifest takes a
// credential that did not resolve. An operator reads it as the app's health
// reason, printed straight after "Health: degraded," and in the REASON column
// of apps list, so it names neither this package nor the probe that hit it.
var errNoCredential = errors.New("requires a credential and none is stored")

// RemoteRuntime proxies to a remote streamable-HTTP MCP server. It has
// no process lifecycle, and health is an MCP ping. Credentials are
// injected as an HTTP header per upstream request and never appear in any
// client-visible payload. Sessions are pooled per credential
// so distinct role-bound secrets never share an upstream identity.
type RemoteRuntime struct {
	// ConnectTimeout bounds each connect handshake, each health ping, each
	// tool listing and the DELETE that ends a session (default 15 s).
	ConnectTimeout time.Duration
	// OnState, when set, is called with readiness transitions (remote
	// runtimes report state through health pings, driven by the manager).
	OnState func(up bool, detail string)
	// PerUser marks oauth-kind apps: no app-level credential exists by
	// design, so inventory/health may run uncredentialed (upstreams that
	// refuse anonymous initialize surface as degraded). Tool calls still
	// always require the caller's grant; see Call.
	PerUser bool
	// AllowLoopback lets the runtime dial a loopback address, which reaches
	// strazad's own host: apps.allowLoopbackUpstreams, true under the
	// standalone profile and false under enterprise. The unspecified,
	// link-local and cloud metadata classes stay refused whatever it says.
	AllowLoopback bool
	// Views makes the upstream initialize advertise the MCP Apps extension
	// (straza.exposure.views). Set it before the first call.
	Views bool

	name      string
	spec      RemoteSpec
	inject    *InjectSpec // nil when credential.kind is none or auth passthrough
	ring      *Ring
	transport *http.Transport

	mu      sync.Mutex
	pool    map[string]*mcp.ClientSession // credential id → session
	stopped bool
}

// NewRemoteRuntime builds a remote runtime. inject is the manifest credential
// injection spec (header mode), or nil for uncredentialed apps.
func NewRemoteRuntime(name string, spec RemoteSpec, inject *InjectSpec, ring *Ring) *RemoteRuntime {
	if ring == nil {
		ring = NewRing(256)
	}
	if spec.Auth == AuthPassthrough {
		// Reserved for EMA/ID-JAG (v1.5); v1beta1 treats it as no injection
		// (spec/app-manifest SPEC.md §2).
		inject = nil
	}
	r := &RemoteRuntime{
		ConnectTimeout: 15 * time.Second,
		name:           name,
		spec:           spec,
		inject:         inject,
		ring:           ring,
		pool:           map[string]*mcp.ClientSession{},
	}
	r.transport = r.newTransport()
	return r
}

// Start is a no-op: remotes have no lifecycle to supervise.
func (r *RemoteRuntime) Start(context.Context) error { return nil }

// Stop closes every pooled session.
func (r *RemoteRuntime) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	for k, s := range r.pool {
		_ = s.Close()
		delete(r.pool, k)
	}
	r.transport.CloseIdleConnections()
}

// Ready is true until stopped: reachability is asserted per call and by the
// manager's health pings, not by a supervised connection.
func (r *RemoteRuntime) Ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.stopped
}

// Tools lists the upstream inventory using the app-level credential. The
// listing ends by ConnectTimeout, or by the caller's own deadline when that
// is sooner, so a server that answers its handshake and its ping and then
// stops answering cannot hold the health loop or strazad's boot. A listing
// that ran out of time answers a sentence that names the wait. A caller that
// left gets its context's error, which the manager words.
func (r *RemoteRuntime) Tools(ctx context.Context, secret *Secret) ([]*mcp.Tool, error) {
	s, key, err := r.session(ctx, secret)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		// The caller's time ran out while it waited for the lock, which says
		// nothing about the pooled session.
		return nil, err
	}
	bound := r.ConnectTimeout
	if dl, ok := ctx.Deadline(); ok {
		bound = min(bound, time.Until(dl))
	}
	lctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	tools, err := listTools(lctx, s)
	if err == nil {
		return tools, nil
	}
	r.evict(key, s)
	if errors.Is(lctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("the MCP server %s did not answer its tool listing within %s, so Straza closed its session and lists its tools again at the next health check. "+
			"An administrator checks that the server runs and answers at the address in its manifest", r.name, bound.Round(10*time.Millisecond))
	}
	return nil, err
}

// Resources lists the upstream's resources with the app-level credential,
// bounded as onSession bounds it.
func (r *RemoteRuntime) Resources(ctx context.Context, secret *Secret) ([]*mcp.Resource, error) {
	var out []*mcp.Resource
	err := r.onSession(ctx, secret, "its resource listing", func(ctx context.Context, s *mcp.ClientSession) (err error) {
		out, err = listResources(ctx, s)
		return err
	})
	return out, err
}

// ReadResource reads one upstream resource with the app-level credential,
// bounded as onSession bounds it.
func (r *RemoteRuntime) ReadResource(ctx context.Context, secret *Secret, uri string) (*mcp.ReadResourceResult, error) {
	var out *mcp.ReadResourceResult
	err := r.onSession(ctx, secret, "the read of "+uri, func(ctx context.Context, s *mcp.ClientSession) (err error) {
		out, err = s.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		return err
	})
	return out, err
}

// onSession runs f on the pooled session of secret within ConnectTimeout, or
// the caller's own deadline when that is sooner. A session that ran out of
// time is evicted, so the next use dials afresh, and the answer is a
// sentence that names what, the request that went unanswered. Any other
// error comes back as f answered it and keeps the session, because the
// server answered.
func (r *RemoteRuntime) onSession(ctx context.Context, secret *Secret, what string, f func(context.Context, *mcp.ClientSession) error) error {
	s, key, err := r.session(ctx, secret)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		// The caller's time ran out while it waited for the lock, which says
		// nothing about the pooled session.
		return err
	}
	bound := r.ConnectTimeout
	if dl, ok := ctx.Deadline(); ok {
		bound = min(bound, time.Until(dl))
	}
	fctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	err = f(fctx, s)
	if err != nil && errors.Is(fctx.Err(), context.DeadlineExceeded) {
		r.evict(key, s)
		return fmt.Errorf("the MCP server %s did not answer %s within %s, so Straza closed its session. "+
			"An administrator checks that the server runs and answers at the address in its manifest", r.name, what, bound.Round(10*time.Millisecond))
	}
	return err
}

// Call invokes one upstream tool with the caller's resolved credential.
func (r *RemoteRuntime) Call(ctx context.Context, in CallInput) (*mcp.CallToolResult, error) {
	if r.inject != nil && in.Secret == nil {
		// Unlike inventory/health, a tool call may NEVER go upstream
		// uncredentialed, including on per-user apps, where the manager
		// resolves the caller's grant before reaching this runtime.
		return nil, errNoCredential
	}
	s, key, err := r.session(ctx, in.Secret)
	if err != nil {
		return nil, err
	}
	res, err := callTool(ctx, s, in.Tool, in.Args)
	if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		// Every caller of this credential shares s. A caller that left says
		// nothing about s, and closing s would end its upstream session for
		// every other caller. A call that reached its own deadline still
		// evicts s, so a session that stops answering is replaced on the
		// next call.
		r.evict(key, s)
	}
	return res, err
}

// Ping checks upstream liveness with the app-level credential and answers
// for that session, whose answer decides the app's health. It pings every
// other pooled session, one per caller credential, at the same time, and
// evicts each that fails, so the next call on its credential dials afresh.
// The wait for the runtime lock and the dial of the app-level session share
// one ConnectTimeout, and every ping has its own.
func (r *RemoteRuntime) Ping(ctx context.Context, secret *Secret) error {
	pass, cancel := context.WithTimeout(ctx, r.ConnectTimeout)
	defer cancel()
	appKey := sessionKey(secret)
	r.mu.Lock()
	others := maps.Clone(r.pool)
	r.mu.Unlock()
	delete(others, appKey)
	var wg sync.WaitGroup
	defer wg.Wait()
	for key, s := range others {
		wg.Go(func() { _ = r.ping(pass, key, s) })
	}
	s, key, err := r.session(pass, secret)
	if errors.Is(err, context.DeadlineExceeded) && errors.Is(pass.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		// Only the check before a dial answers the bare error, since a
		// timed-out dial answers its own sentence: the pass spent its time
		// waiting for the lock behind another dial.
		return fmt.Errorf("the MCP server %s was not checked within %s, because Straza was still waiting on another connection attempt to it, "+
			"and it checks the server again at the next health check. An administrator checks that the server runs and answers at the address in its manifest",
			r.name, r.ConnectTimeout)
	}
	if err != nil {
		return err
	}
	return r.ping(ctx, key, s)
}

// ping pings one pooled session under a deadline of ConnectTimeout and
// evicts it when the ping failed, unless only because ctx was cancelled: a
// caller that left says nothing about the session, the rule Call follows. A
// ping that ran out of time answers a sentence an administrator acts on.
func (r *RemoteRuntime) ping(ctx context.Context, key string, s *mcp.ClientSession) error {
	pctx, cancel := context.WithTimeout(ctx, r.ConnectTimeout)
	defer cancel()
	err := s.Ping(pctx, nil)
	if err == nil || errors.Is(ctx.Err(), context.Canceled) {
		return err
	}
	r.evict(key, s)
	if errors.Is(pctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("the MCP server %s did not answer a ping within %s, so Straza closed its session and opens a new one at the next health check. "+
			"An administrator checks that the server runs and answers at the address in its manifest", r.name, r.ConnectTimeout)
	}
	return err
}

// Logs returns recent connection log lines.
func (r *RemoteRuntime) Logs(n int) []string { return r.ring.Last(n) }

// session returns the pooled session for the credential, dialing lazily.
func (r *RemoteRuntime) session(ctx context.Context, secret *Secret) (*mcp.ClientSession, string, error) {
	if r.inject != nil && secret == nil && !r.PerUser {
		// The manifest requires a credential, and calling upstream without one
		// would leak an uncredentialed request: fail closed (SPEC §3).
		// Per-user apps are exempt here for inventory/health only. Call has
		// its own unconditional guard.
		return nil, "", errNoCredential
	}
	key := sessionKey(secret)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return nil, "", notRunning(r.name)
	}
	if s, ok := r.pool[key]; ok {
		return s, key, nil
	}
	if err := ctx.Err(); err != nil {
		// The caller's time ran out while it waited for the lock. A dial now
		// would still hold the lock for the SDK's cancel notice and a DELETE.
		return nil, "", err
	}
	if secret != nil {
		// Rotation and reconnect update the credential row in place, so a key on
		// the row ID alone would keep the superseded token on the wire until
		// an upstream error evicted it. The key fingerprints the value. Any
		// session left under the same credential with a stale value dies here,
		// closed off the lock as evict closes, since Close waits for its calls.
		for k, old := range r.pool {
			if k != key && strings.HasPrefix(k, secret.ID+"|") {
				delete(r.pool, k)
				go func() { _ = old.Close() }()
				r.ring.Append("session superseded by rotated credential")
			}
		}
	}

	bound := r.ConnectTimeout
	if dl, ok := ctx.Deadline(); ok {
		// A call's own timeout, or what is left of a health pass, may be
		// sooner, and the sentence below names the bound that applied.
		bound = min(bound, time.Until(dl))
	}
	cctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	s, err := r.connect(cctx, secret)
	if err != nil {
		r.ring.Append(fmt.Sprintf("connect %s failed: %v", r.shownURL(), err))
		if refusal := refusalOf(err); refusal != nil {
			return nil, "", refusal
		}
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return nil, "", fmt.Errorf("the MCP server %s did not answer the connect handshake within %s, so Straza dials it again on the next call or health check. "+
				"An administrator checks that the server runs and answers at the address in its manifest", r.name, bound.Round(10*time.Millisecond))
		}
		return nil, "", fmt.Errorf("the connection to MCP server %s failed: %w. An administrator checks that the server runs and answers at the address in its manifest", r.name, err)
	}
	r.ring.Append("connected " + r.shownURL())
	r.pool[key] = s
	return s, key, nil
}

// connect opens one upstream session for secret. The host of the manifest's
// address is classed before the dial, and the dial hook classes every
// resolved address, so a refusal from either place comes back as its own
// sentence, the one the health loop reports.
func (r *RemoteRuntime) connect(ctx context.Context, secret *Secret) (*mcp.ClientSession, error) {
	if err := r.refuseHost(); err != nil {
		return nil, err
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:             r.spec.URL,
		HTTPClient:           r.httpClient(secret),
		DisableStandaloneSSE: true, // request/response only: upstream notifications are unused
	}
	return newClient(r.Views).Connect(ctx, transport, nil)
}

// Probe dials the upstream once with the given credential, pings it and
// closes the session without pooling it: the paste-time check of a
// caller's token, whose row does not exist yet, so nothing may be kept
// under a key that never serves a call.
func (r *RemoteRuntime) Probe(ctx context.Context, secret *Secret) error {
	r.mu.Lock()
	stopped := r.stopped
	r.mu.Unlock()
	if stopped {
		return notRunning(r.name)
	}
	cctx, cancel := context.WithTimeout(ctx, r.ConnectTimeout)
	defer cancel()
	s, err := r.connect(cctx, secret)
	if err != nil {
		if refusal := refusalOf(err); refusal != nil {
			return refusal
		}
		return fmt.Errorf("connect %s: %w", r.shownURL(), err)
	}
	defer func() { _ = s.Close() }()
	return s.Ping(cctx, nil)
}

// sessionKey identifies a pooled upstream session: credential row + a
// fingerprint of the value, so rotating a credential (refresh worker,
// reconnect, `apps secret set`) can never reuse a session dialed under the
// previous token. "" = uncredentialed.
func sessionKey(secret *Secret) string {
	if secret == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(secret.Value))
	return secret.ID + "|" + hex.EncodeToString(sum[:8])
}

// evict drops a broken session so the next call re-dials, and writes one
// ring line. Only the eviction that removes the pool entry closes the
// session, and it closes it on its own goroutine: the SDK's Close first
// waits for every call still running on the session, each of which ends by
// its own deadline, and the DELETE that follows ends by ConnectTimeout.
func (r *RemoteRuntime) evict(key string, s *mcp.ClientSession) {
	r.mu.Lock()
	removed := r.pool[key] == s
	if removed {
		delete(r.pool, key)
	}
	r.mu.Unlock()
	if !removed {
		return
	}
	go func() { _ = s.Close() }()
	r.ring.Append("session evicted after error")
}

// httpClient wraps the pooled transport with header injection when the
// manifest declares one, and bounds the DELETE that ends the session. Both
// clients follow a redirect only on the manifest's host, since a hop also
// carries the call's arguments.
func (r *RemoteRuntime) httpClient(secret *Secret) *http.Client {
	if r.inject == nil || secret == nil {
		return &http.Client{Transport: &deleteDeadline{base: r.transport, timeout: r.ConnectTimeout}, CheckRedirect: followRedirect}
	}
	value := strings.ReplaceAll(r.inject.Template, SecretPlaceholder, secret.Value)
	return &http.Client{CheckRedirect: followRedirect, Transport: &deleteDeadline{timeout: r.ConnectTimeout, base: &headerInjector{
		base:  r.transport,
		name:  r.inject.Name,
		value: value,
	}}}
}

// deleteDeadline gives the DELETE that ends an upstream session a deadline.
// The SDK sends it from Close on the session's own context, which never
// ends, so a server that stopped answering would hold Close, and the
// runtime lock while a failed dial closes its half-made session, until the
// server answered again.
type deleteDeadline struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (d *deleteDeadline) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodDelete {
		return d.base.RoundTrip(req)
	}
	// Close reads no body of the answer, so the deadline may end with the
	// round trip.
	ctx, cancel := context.WithTimeout(req.Context(), d.timeout)
	defer cancel()
	return d.base.RoundTrip(req.WithContext(ctx))
}

// maxRedirects is how many redirects the upstream client follows, the limit
// net/http applies to a client with no redirect policy of its own.
const maxRedirects = 10

// followRedirect is the upstream client's redirect policy. The header
// injector adds the credential to every request the client sends, the hops
// of a redirect included, so a hop may stay only on the host and port of the
// manifest's address, and may never go from https to http. A redirect on
// that host, such as /mcp to /mcp/, is followed, because the credential then
// never leaves the host the operator named. Each refusal says what the server
// did and what an administrator changes. The DELETE that ends a session is
// never followed, because net/http would send its hop as a GET that
// deleteDeadline does not bound, and Close reads no answer anyway.
func followRedirect(req *http.Request, via []*http.Request) error {
	switch {
	case via[0].Method == http.MethodDelete:
		return http.ErrUseLastResponse
	case !strings.EqualFold(req.URL.Host, via[0].URL.Host):
		return fmt.Errorf("the MCP server redirected to %s, and Straza follows a redirect only on the host and port of the address in the manifest, "+
			"so that no call and no credential reaches another host. If %s is the right address, an administrator puts it in the manifest", req.URL.Host, req.URL.Host)
	case via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http":
		return errors.New("the MCP server redirected from https to http, and Straza does not follow that, so that no call and no credential crosses the network unencrypted. " +
			"An administrator puts the https address the server answers at in the manifest")
	case len(via) >= maxRedirects:
		return fmt.Errorf("the MCP server redirected %d times, and Straza stops there. An administrator puts the address the server answers at in the manifest", maxRedirects)
	}
	return nil
}

// headerInjector adds the rendered credential header to every upstream
// request, the hops of a redirect included, which is why the client refuses
// a hop that leaves the manifest's host. It also strips any client-originated
// value for the same header: injection is authoritative (SPEC §2: client
// Authorization never forwards).
type headerInjector struct {
	base        http.RoundTripper
	name, value string
}

func (h *headerInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set(h.name, h.value)
	return h.base.RoundTrip(clone)
}

func orEmptyArgs(args []byte) []byte {
	if len(args) == 0 {
		return []byte(`{}`)
	}
	return args
}
