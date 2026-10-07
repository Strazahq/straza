package agentguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/agentguard/trace"
	"github.com/strazahq/straza/internal/version"
)

// `straza mcp`: the stdio MCP server a harness registers ("command":
// "straza", "args": ["mcp"]) whose backend is the Straza gateway. It owns
// exactly one job the harness config model cannot: keeping a ROTATING session
// token on the wire. The proxy is convenience and session coherence, not a
// security control: the PEP stays the gateway; bypassing the proxy buys
// nothing without a valid token.

// mcpConnectRetries paces startup against a gateway that is still booting.
const mcpConnectRetries = 3

// RunMCPProxy serves MCP on stdio until ctx ends or stdin closes.
// harnessOverride names the harness this proxy fronts (checkin identity,
// NEVER an admin harness; those tokens are barred from /mcp). Empty =
// STRAZA_HARNESS env, then "claude-code". server names the one upstream
// server to front on its own gateway endpoint (mcpproxy_views.go); empty
// fronts the combined /mcp.
func RunMCPProxy(ctx context.Context, harnessOverride, server string) error {
	return runMCPProxy(ctx, harnessOverride, server, &mcp.StdioTransport{})
}

// runMCPProxy is RunMCPProxy over an injectable harness-side transport: the
// production entrypoint passes stdio; the protocol-purity test drives it over
// in-memory pipes so it can prove the trace never reaches the wire.
func runMCPProxy(ctx context.Context, harnessOverride, server string, transport mcp.Transport) error {
	if server != "" {
		if err := checkServerName(server); err != nil {
			return err
		}
	}
	store, err := OpenStore()
	if err != nil {
		return fmt.Errorf("state unavailable: %w", err)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return fmt.Errorf("not configured (run `straza enroll`): %w", err)
	}
	harness := sessionHarness(harnessOverride)
	client := NewClient(cfg.ServerURL)
	ses, err := ensureSession(ctx, store, client, harness)
	if err != nil {
		return fmt.Errorf("no session and checkin failed: %w", err)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "straza", Version: version.Version}, proxyServerOptions(server))
	p := newMCPProxy(srv)
	p.ctx = ctx // scheduled and periodic resyncs outlive the calls that arm them
	p.harness = harness
	p.server = server
	// The decision journal for this long-lived process: the level is re-read
	// every mcpJournalRecheck so `straza trace on` lands without a restart.
	p.tr = store.Trace()
	p.tr.SetRecheck(mcpJournalRecheck)
	p.debugSession(ses)
	// Boot and revival dial through the same factory so a revived session is
	// wired exactly like this first one (mcpproxy_redial.go).
	endpoint := gatewayEndpoint(cfg.ServerURL, server)
	p.dial = newGatewayDialer(p, store, client, harness, endpoint)
	p.logf = func(event, msg string) {
		store.logClientError(ClientError{Kind: "mcp", Harness: harness, Event: event, Err: msg})
	}

	var cs *mcp.ClientSession
	for attempt := 0; ; attempt++ {
		cs, err = p.dialBounded(ctx)
		if err == nil {
			break
		}
		if server != "" {
			if refused := serverRefusal(server, endpoint, err); refused != nil {
				return refused
			}
		}
		if attempt+1 >= mcpConnectRetries {
			return fmt.Errorf("Straza: gateway unreachable at %s: %w", endpoint, err) //nolint:staticcheck // ST1005: deliberate brand prefix; this text reaches the agent as the tool-failure reason
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	// Close the CURRENT session at exit (revival may have swapped it since
	// boot) and wait out scheduled resync timers so shutdown leaves no
	// stray goroutine behind (their bodies fast-exit on the dead ctx).
	defer func() { _ = p.session().Close() }()
	defer p.stopResyncs()
	p.cs = cs
	// Startup is direct and un-jittered: the harness must see the catalog before
	// it makes its first call. Only notification-driven resyncs get smeared.
	p.resync(ctx)
	go p.runPeriodicResync(ctx)

	return srv.Run(ctx, transport)
}

// Tunable defaults for the resync machinery. They are struct fields (not raw
// consts) so tests can collapse every timer to zero; production takes these.
const (
	// defaultResyncJitterMax bounds the random smear scheduleResync applies to
	// a notification-driven resync, spreading a fleet-wide list_changed herd.
	defaultResyncJitterMax = 5 * time.Second
	// defaultPeriodicEvery / defaultPeriodicJitter set the backstop cadence
	// that catches notifications the SSE hub never delivered.
	defaultPeriodicEvery  = 10 * time.Minute
	defaultPeriodicJitter = 1 * time.Minute
	// defaultResyncRetryPause paces the whole-listing retry after a mid-stream
	// page error; short, because a stale cursor resolves on the next attempt.
	defaultResyncRetryPause = 200 * time.Millisecond
)

// mcpProxy mirrors the gateway's tool catalog onto the stdio server.
type mcpProxy struct {
	srv *mcp.Server

	// dial establishes one fresh gateway client session (newGatewayDialer);
	// written once at wiring, before any concurrent use. Boot and revival
	// share it so every session is wired identically.
	dial func(ctx context.Context) (*mcp.ClientSession, error)
	// logf records one proxy lifecycle breadcrumb (event, message) to the
	// client error log; nil in tests that don't observe it.
	logf func(event, msg string)
	// tr is the decision journal / debug trace (trace package): one "call"
	// record per proxied tool call, lifecycle records in a debug window. nil
	// (tests that don't wire it) writes nothing. harness names the dialect
	// this proxy fronts, carried on every record.
	tr      *trace.Logger
	harness string
	// server names the one upstream server this bridge fronts on the
	// gateway's per-server endpoint; empty for the combined /mcp. Written
	// once at wiring.
	server string

	// ctx is the proxy's run context, captured before the first resync so the
	// notification handler and the periodic backstop, both of which outlive
	// the call that armed them, have a context to list against.
	ctx context.Context

	// Redial tunables (see the default* consts in mcpproxy_redial.go).
	redialBase    time.Duration // backoff base between failed revivals
	redialCap     time.Duration // backoff ceiling
	redialTimeout time.Duration // bound on one dial attempt (black-hole guard)

	// csMu guards the live session and the revival state below. The session
	// is read per use (never captured across a call) so a revival is visible
	// to every subsequent caller.
	csMu        sync.Mutex
	cs          *mcp.ClientSession
	redialFails int       // consecutive failed revival attempts (drives backoff)
	redialNext  time.Time // earliest next revival attempt; zero = no gate

	// Resync tunables (see the default* consts); fields so tests set them ~0.
	retryPause     time.Duration // pause between whole-listing retry attempts
	jitterMax      time.Duration // upper bound on the scheduleResync smear
	periodicEvery  time.Duration // base interval of the backstop resync
	periodicJitter time.Duration // random slack added per backstop tick

	mu sync.Mutex
	// registered maps a mirrored tool name to the digest of the contract the
	// stdio server currently carries for it, so resync can tell a genuine
	// schema change from an unchanged tool (see reconcile).
	registered map[string]string
	// views maps a mirrored view URI to the digest of the resource the stdio
	// server carries for it (see reconcileViews).
	views map[string]string
	// resyncPending folds a burst of list_changed notifications into a single
	// scheduled resync (see scheduleResync).
	resyncPending bool
	// resyncStopped refuses new scheduled resyncs once shutdown waits on
	// timers, because the first Add of a WaitGroup must happen before its Wait.
	resyncStopped bool

	// timers tracks in-flight scheduled resyncs so shutdown (and test
	// teardown) can wait for them instead of leaving a stray goroutine
	// touching the store mid-teardown.
	timers sync.WaitGroup
}

// newMCPProxy wires a proxy with production tunables. Tests that need faster
// timers construct the proxy directly (buildProxy) and override the fields.
func newMCPProxy(srv *mcp.Server) *mcpProxy {
	return &mcpProxy{
		srv:            srv,
		retryPause:     defaultResyncRetryPause,
		jitterMax:      defaultResyncJitterMax,
		periodicEvery:  defaultPeriodicEvery,
		periodicJitter: defaultPeriodicJitter,
		redialBase:     defaultRedialBase,
		redialCap:      defaultRedialCap,
		redialTimeout:  defaultRedialTimeout,
	}
}

// digestUnhashable marks a tool whose mirrored *mcp.Tool failed to marshal.
// It is neither the empty string nor a real 64-hex digest, so the removal path
// still tracks the tool while every resync re-adds it (the fail-safe: never
// skip a possible schema change on our account).
const digestUnhashable = "\x00unhashable"

// toolDigest is the sha256 of the canonical JSON for EXACTLY the fields the
// proxy mirrors onto the stdio server (mirrorTool: name, description,
// inputSchema, _meta). A tool without _meta hashes exactly like its three
// contract fields alone. ok is false only when marshaling fails, which the
// caller treats as "re-add", never "skip".
func toolDigest(t *mcp.Tool) (digest string, ok bool) {
	b, err := json.Marshal(t)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true
}

// mirrorTool is the tool the stdio server carries for a gateway tool: the
// contract fields plus _meta verbatim, which links a tool to its MCP Apps view.
func mirrorTool(t *mcp.Tool) *mcp.Tool {
	return &mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, Meta: t.Meta}
}

// resyncListAttempts bounds how many times listAllTools re-walks the catalog
// from the start when a page fetch fails mid-stream. A mid-listing error is
// almost always a stale cursor: the catalog changed between our page requests,
// so the offset the previous page's cursor encoded no longer lines up and the
// gateway answers -32602. A broken listing can't be resumed coherently, so we
// discard the partial and start over. Three tries is generous: a catalog that
// churns faster than we can walk it will be caught by the next notification or
// the periodic backstop anyway.
const resyncListAttempts = 3

// resync lists the gateway catalog and reconciles the stdio server's tool set.
// Schemas pass through verbatim: the proxy never reinterprets the contract.
func (p *mcpProxy) resync(ctx context.Context) {
	tools, ok := p.listAllTools(ctx)
	if p.tr.DebugOn() {
		p.tr.Debug("mcp.resync", slog.Int("tools", len(tools)), slog.Bool("ok", ok))
	}
	if !ok {
		return // transient; the next list_changed or periodic pass retries
	}
	p.reconcile(tools)
	if p.server != "" {
		p.resyncViews(ctx)
	}
}

// listAllTools walks the gateway catalog to completion, following nextCursor
// across pages via the SDK's auto-paginating iterator. A single ListTools would
// see only the first page once the gateway paginates. On a mid-stream error it
// retries the WHOLE listing (see resyncListAttempts); returning ok=false leaves
// the mirror untouched, because reconciling against a truncated catalog would
// strand every tool past the break.
func (p *mcpProxy) listAllTools(ctx context.Context) ([]*mcp.Tool, bool) {
	for attempt := 0; attempt < resyncListAttempts; attempt++ {
		cs := p.session()
		var tools []*mcp.Tool
		var lastErr error
		for tool, err := range cs.Tools(ctx, nil) {
			if err != nil {
				lastErr = err
				break
			}
			tools = append(tools, tool)
		}
		if lastErr == nil {
			return tools, true
		}
		// On a server endpoint, a catalog refusal of the first page means the
		// server left the caller's catalog: mirror an empty catalog. No cursor
		// is involved yet, so it cannot be a stale one.
		if p.server != "" && len(tools) == 0 && catalogRefused(lastErr) {
			return nil, true
		}
		// A dead session revives HERE too, so the periodic backstop
		// self-heals an idle lane without waiting for the next call (this
		// is what turns "a deploy bricks every idle session" into
		// "self-heal within one backstop interval"). Listing is read-only;
		// a fresh attempt is always safe.
		if sessionDeadErr(lastErr) {
			if _, err := p.revive(ctx, cs); err != nil {
				return nil, false // backoff or gateway down; the next pass retries
			}
			continue
		}
		if attempt+1 >= resyncListAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(p.retryPause):
		}
	}
	return nil, false
}

// reconcile brings the stdio server's tool set in line with the freshly listed
// catalog (add new, drop vanished, re-add on schema drift).
func (p *mcpProxy) reconcile(tools []*mcp.Tool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.registered == nil {
		p.registered = map[string]string{}
	}
	seen := map[string]bool{}
	added := 0 // debug-trace count of tools (re)registered this pass
	for _, t := range tools {
		seen[t.Name] = true
		name := t.Name
		tool := mirrorTool(t)
		digest, ok := toolDigest(tool)
		// Reconcile by CONTENT, not by name. A tool's schema can change under a
		// stable name (the gateway injects _straza_justification when a
		// mode:approve policy activates, or an upstream server revises its
		// schema mid-session), and a name-only skip would strand the harness
		// (and thus the model, and the approver reading the justification) on
		// the stale contract. AddTool REPLACES and self-emits
		// list_changed, so re-adding on a real change is safe; the digest
		// guards against needless churn when nothing changed.
		if ok && p.registered[name] == digest {
			continue
		}
		p.srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// The journal's view of this call: the gateway's answer (status +
			// X-Request-Id) is observed by sessionAuthTransport through this
			// Call, which the SDK threads into the HTTP request via ctx.
			start := time.Now()
			call := &trace.Call{}
			ctx = trace.WithCall(ctx, call)
			revived := false
			cs := p.session()
			params := &mcp.CallToolParams{Name: name, Arguments: req.Params.Arguments}
			out, err := cs.CallTool(ctx, params)
			if err != nil && sessionDeadErr(err) {
				// The session is a corpse (idle eviction, pod restart, SSE
				// exhaustion). Revive so no failure is ever permanent, then
				// split on proof: only a request that provably never left
				// the process may be replayed by plumbing.
				fresh, rerr := p.revive(ctx, cs)
				switch {
				case rerr != nil:
					err = fmt.Errorf("%v; gateway reconnect also failed: %v", err, rerr)
				case neverSentErr(err):
					revived = true
					out, err = fresh.CallTool(ctx, params)
				default:
					// Indeterminate original failure: the lane is healed but
					// the call may have executed and is NOT replayed. The
					// agent decides about a re-call with full context.
					revived = true
					err = fmt.Errorf("%v; the gateway session has been reconnected, retry the call", err)
				}
			}
			p.journalCall(name, out, err, call, start, revived)
			if err != nil {
				// A DENY arrives as an isError result from the gateway and
				// passes through above; this branch is infrastructure:
				// fail closed with an actionable reason.
				return nil, fmt.Errorf("Straza: gateway call failed: %w", err) //nolint:staticcheck // ST1005: deliberate brand prefix; this text reaches the agent as the tool-failure reason
			}
			return out, nil
		})
		added++
		if ok {
			p.registered[name] = digest
		} else {
			p.registered[name] = digestUnhashable
		}
	}
	var gone []string
	for name := range p.registered {
		if !seen[name] {
			gone = append(gone, name)
			delete(p.registered, name)
		}
	}
	if len(gone) > 0 {
		p.srv.RemoveTools(gone...)
	}
	if p.tr.DebugOn() {
		p.tr.Debug("mcp.reconcile", slog.Int("added", added), slog.Int("removed", len(gone)))
	}
}

// sessionAuthTransport injects the CURRENT session token per request and
// does exactly one refresh-and-retry on 401. The token is read from state
// on EVERY request (never cached in the struct):
// hooks rotate the session underneath us and the proxy must follow.
type sessionAuthTransport struct {
	base    http.RoundTripper
	store   *Store
	client  *Client
	harness string

	refreshMu sync.Mutex
}

func (t *sessionAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ses, err := ensureSession(req.Context(), t.store, t.client, t.harness)
	if err != nil {
		return nil, fmt.Errorf("Straza: no governed session: %w", err) //nolint:staticcheck // ST1005: deliberate brand prefix; this text reaches the agent as the tool-failure reason
	}
	resp, err := t.send(req, ses.SessionToken)
	// The decision journal observes the gateway's answer (status + the
	// X-Request-Id it minted) through the Call the tool handler bound into
	// the request context; after a refresh-and-retry the retried answer wins.
	if err == nil {
		trace.CallFrom(req.Context()).Observe(resp.StatusCode, resp.Header.Get("X-Request-Id"))
	}
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	// One refresh-and-retry. Retry needs a rewindable body.
	fresh, rerr := t.refresh(req.Context(), ses)
	if rerr != nil || req.GetBody == nil && req.Body != nil {
		return resp, nil // surface the 401; the refreshed state helps the NEXT call
	}
	_ = resp.Body.Close()
	if req.Body != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		req.Body = body
	}
	retried, err := t.send(req, fresh.SessionToken)
	if err == nil {
		trace.CallFrom(req.Context()).Observe(retried.StatusCode, retried.Header.Get("X-Request-Id"))
	}
	return retried, err
}

func (t *sessionAuthTransport) send(req *http.Request, token string) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(r)
}

// refresh renews the session token, serialized so concurrent 401s spend one
// refresh. Reload-first: another goroutine (or a hook) may already have
// rotated it. Like the hook's refresh, it adopts the policy the renewal
// names and settles a refusal (settlePolicy), so it never extends one.
func (t *sessionAuthTransport) refresh(ctx context.Context, stale Session) (Session, error) {
	t.refreshMu.Lock()
	defer t.refreshMu.Unlock()
	if cur, err := t.store.LoadSession(); err == nil && cur.SessionToken != stale.SessionToken {
		return cur, nil
	}
	resp, err := t.client.Refresh(ctx, stale.SessionToken,
		harnessName(stale.Harness), harnessVersion(stale.Harness), measureAttestation(harnessName(stale.Harness)))
	if err != nil {
		// The session may be individually revoked while the user stays
		// active; a device-token checkin is the recovery lane. A killed
		// user fails here too, with the server's actionable reason.
		return ensureSession(ctx, t.store, t.client, t.harness)
	}
	cur, lerr := t.store.LoadSession()
	if lerr != nil {
		cur = stale
	}
	held := cur.ExpiresAt
	cur.SessionToken = resp.SessionToken
	cur.ExpiresAt = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	if cfg, err := t.store.LoadConfig(); err == nil {
		adopted, ferr := adoptSnapshot(ctx, t.client, resp.SessionToken, t.store, cfg, cur.SnapshotID, resp.SnapshotID)
		cur.SnapshotID = adopted
		cur.settlePolicy(resp.SnapshotID, ferr, held)
	}
	_ = t.store.SaveSession(cur)
	return cur, nil
}

// ensureSession returns a live session, minting one via the enrolled
// identity when none exists (the same headless/device/ID-token lane order a
// hook session start uses, so Tier-3 exec and the proxy serve headless NHIs
// too), under a cross-process lock so a simultaneously-starting hook and
// proxy spend ONE checkin between them. A session
// already on disk is ADOPTED, never re-minted: the hook owns
// conversation-start semantics, the proxy follows.
func ensureSession(ctx context.Context, store *Store, client *Client, harnessName string) (Session, error) {
	const harnessVersion = "mcp" // the proxy checks in as the harness, versioned by its role
	if ses, err := store.LoadSession(); err == nil && time.Until(ses.ExpiresAt) > 30*time.Second {
		return ses, nil
	}
	lock := store.statePath("checkin.lock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- our own state dir
		if err == nil {
			_ = f.Close()
			break
		}
		if fi, serr := os.Stat(lock); serr == nil && time.Since(fi.ModTime()) > 15*time.Second {
			_ = os.Remove(lock) // stale lock from a killed process
			continue
		}
		if time.Now().After(deadline) {
			break // proceed anyway; a double checkin is wasteful, not wrong
		}
		time.Sleep(150 * time.Millisecond)
		// The lock holder may have finished our work for us.
		if ses, err := store.LoadSession(); err == nil && time.Until(ses.ExpiresAt) > 30*time.Second {
			return ses, nil
		}
	}
	defer func() { _ = os.Remove(lock) }()
	// Double-check under the lock.
	prev, prevErr := store.LoadSession()
	if prevErr == nil && time.Until(prev.ExpiresAt) > 30*time.Second {
		return prev, nil
	}
	id, err := store.LoadIdentity()
	if err != nil {
		return Session{}, fmt.Errorf("not enrolled (run `straza enroll`): %w", err)
	}
	if id.Headless == "" && id.DeviceToken == "" && id.IDToken == "" {
		return Session{}, fmt.Errorf("no device token (re-run `straza enroll`)")
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return Session{}, fmt.Errorf("not enrolled (run `straza enroll`): %w", err)
	}
	resp, err := checkinWithIdentity(ctx, client, store, cfg, id, harnessName, harnessVersion)
	if err != nil {
		return Session{}, fmt.Errorf("checkin failed: %w", err)
	}
	ses := Session{
		SessionID: resp.SessionID, SessionToken: resp.SessionToken, SnapshotID: resp.SnapshotID,
		User: resp.User, Roles: resp.Roles,
		Attestation: resp.Attestation, Harness: harnessLabel(harnessName, harnessVersion),
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second),
	}
	// Adopt the policy blob so the pin names a snapshot we actually hold: a hook
	// reading this freshly minted session verifies blob-against-pin and would
	// brick fail-closed on a pin with no blob (proxy mints, hook then reads on a
	// shared session.json). The proxy fronts the gateway PEP and needs no local
	// blob to serve calls, so a snapshot fetch failure must NOT fail the mint;
	// adoptForMint keeps the best pin it can and a later hook/refresh heals it.
	// A mint carries the replaced session's refusal and holds its session
	// time, so minting never extends a refused policy (settlePolicy).
	held := time.Now()
	if prevErr == nil {
		held, ses.PolicyRefused, ses.PolicyDeadline = prev.ExpiresAt, prev.PolicyRefused, prev.PolicyDeadline
	}
	if cfg, cfgErr := store.LoadConfig(); cfgErr == nil {
		pin, ferr := adoptForMint(ctx, client, resp.SessionToken, store, cfg, resp.SnapshotID)
		ses.SnapshotID = pin
		ses.settlePolicy(resp.SnapshotID, ferr, held)
	}
	if err := store.SaveSession(ses); err != nil {
		return Session{}, err
	}
	_ = store.ClearRevocation() // a successful checkin supersedes any previous kill
	return ses, nil
}
