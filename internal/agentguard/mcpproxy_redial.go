package agentguard

// Gateway-session revival for the MCP stdio proxy. The go-sdk streamable
// client latches itself permanently closed once its standalone SSE stream
// exhausts reconnection (idle eviction, a
// strazad pod restart emptying the subject cache, a network cut), and a proxy
// holding that corpse turns one transient into a session-long outage of the
// whole governed tool lane. The gateway is deliberately stateless (it never
// mints an MCP session id; there is nothing to resume), so the correct
// recovery is a fresh dial: cheap (initialize + tools/list), valid against
// any pod behind the balancer, and safe because revival by itself replays
// nothing.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/strazahq/straza/internal/sameorigin"
	"github.com/strazahq/straza/internal/version"
)

// Revival tunables (struct fields on mcpProxy so tests collapse them).
const (
	// defaultRedialBase / defaultRedialCap bound the full-jitter backoff
	// between FAILED revival attempts, so a down gateway costs each call one
	// cheap refusal instead of a fleet-wide dial storm.
	defaultRedialBase = 500 * time.Millisecond
	defaultRedialCap  = 30 * time.Second
	// defaultRedialTimeout bounds one dial attempt end to end. A black-holed
	// gateway (dead VPN, wedged LB) accepts the TCP connect and then answers
	// nothing; without this bound the handshake would hang forever holding
	// the revival lock, and every future call would queue behind it.
	defaultRedialTimeout = 15 * time.Second
)

// newGatewayDialer builds the gateway transport and returns the dial factory
// that BOTH the boot path and revival use: one factory, so a revived session
// is wired exactly like the boot one (same auth transport, same list_changed
// handler). The transport carries no http.Client.Timeout, deliberately: a
// client-level deadline also guillotines the standalone SSE stream (forcing a
// silent stream cycle every period) and races the gateway's approval hold,
// which may legally take the full decision window before answering. Connect
// and TLS are bounded by DefaultTransport; per-call ceilings ride the
// caller's context; dialBounded guards the handshake itself. endpoint is the
// full gateway URL to dial (gatewayEndpoint).
func newGatewayDialer(p *mcpProxy, store *Store, client *Client, harness, endpoint string) func(context.Context) (*mcp.ClientSession, error) {
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: gatewayHTTPClient(store, client, harness),
	}
	return func(ctx context.Context) (*mcp.ClientSession, error) {
		return mcp.NewClient(&mcp.Implementation{Name: "straza-mcp", Version: version.Version},
			&mcp.ClientOptions{
				ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
					// Coalesced + jittered; see scheduleResync.
					p.scheduleResync()
				},
			}).Connect(ctx, transport, nil)
	}
}

// gatewayHTTPClient is the gateway lane's HTTP client. sessionAuthTransport
// puts the session token on every request it sends, so every redirect
// answers to sameorigin.Check.
func gatewayHTTPClient(store *Store, client *Client, harness string) *http.Client {
	return &http.Client{CheckRedirect: sameorigin.Check, Transport: &sessionAuthTransport{
		base: http.DefaultTransport, store: store, client: client, harness: harness,
	}}
}

// session returns the live gateway client session.
func (p *mcpProxy) session() *mcp.ClientSession {
	p.csMu.Lock()
	defer p.csMu.Unlock()
	return p.cs
}

type dialResult struct {
	cs  *mcp.ClientSession
	err error
}

// dialBounded runs one dial attempt bounded by redialTimeout WITHOUT tying
// the session's lifetime to the bound. A context deadline cannot express
// this: the session must live on the proxy's run context, and a
// deadline-wrapped ctx would kill the very session it dialed once the
// deadline lapsed. An attempt that outruns the bound is abandoned and reaped
// if it ever completes.
func (p *mcpProxy) dialBounded(ctx context.Context) (*mcp.ClientSession, error) {
	// The session must outlive the triggering call: dial on the proxy's run
	// context, never the caller's, because a caller whose ctx ends right after
	// revival would otherwise kill the session it just paid to establish.
	// The caller's ctx only bounds the WAIT below.
	dialCtx := p.ctx
	if dialCtx == nil {
		dialCtx = ctx
	}
	ch := make(chan dialResult, 1)
	go func() {
		cs, err := p.dial(dialCtx)
		ch <- dialResult{cs, err}
	}()
	limit := p.redialTimeout
	if limit <= 0 {
		limit = defaultRedialTimeout
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.cs, r.err
	case <-ctx.Done():
		go reapDial(ch)
		return nil, ctx.Err()
	case <-timer.C:
		go reapDial(ch)
		return nil, fmt.Errorf("gateway handshake exceeded %s", limit)
	}
}

// reapDial drains an abandoned dial attempt and closes its session if the
// attempt won its race after we stopped waiting, since nobody owns it.
func reapDial(ch chan dialResult) {
	if r := <-ch; r.cs != nil {
		_ = r.cs.Close()
	}
}

// revive replaces a dead gateway session with a freshly dialed one,
// single-flight: the caller passes the session it OBSERVED failing, the first
// caller through the lock dials, and every concurrent observer of the same
// corpse waits on the lock and leaves with the winner's fresh session.
// Failed attempts arm a full-jitter backoff so a down gateway is refused
// fast, not hammered; the refusal is an error the caller surfaces, and a
// LATER call earns a fresh attempt: the whole point is that no failure is
// ever permanent.
func (p *mcpProxy) revive(ctx context.Context, dead *mcp.ClientSession) (*mcp.ClientSession, error) {
	p.csMu.Lock()
	defer p.csMu.Unlock()
	if p.cs != dead {
		return p.cs, nil // a concurrent caller already revived this corpse
	}
	if !p.redialNext.IsZero() && time.Now().Before(p.redialNext) {
		return nil, fmt.Errorf("gateway reconnect backing off after %d failed attempts", p.redialFails)
	}
	fresh, err := p.dialBounded(ctx)
	if err != nil {
		p.redialFails++
		p.redialNext = time.Now().Add(p.redialDelay())
		p.log("redial", fmt.Sprintf("gateway reconnect attempt %d failed: %v", p.redialFails, err))
		if p.tr.DebugOn() {
			p.tr.Debug("mcp.redial", slog.Int("attempt", p.redialFails), slog.Bool("ok", false),
				slog.String("err_class", errClass(err)))
		}
		return nil, err
	}
	p.cs = fresh
	failsBefore := p.redialFails
	p.redialFails, p.redialNext = 0, time.Time{}
	// Close the corpse detached: a latched client's Close can block on its
	// dead SSE stream, and revival must never wait on a corpse.
	go func() { _ = dead.Close() }()
	// The catalog may have drifted while the lane was dead. The mirror still
	// carries the pre-death tool set, so calls work immediately; the
	// coalesced, jittered resync trues it up. Boot stays the only
	// un-jittered sync, because there the harness has no catalog at all yet.
	p.scheduleResync()
	p.log("redial", fmt.Sprintf("gateway session revived (after %d failed attempts)", failsBefore))
	if p.tr.DebugOn() {
		p.tr.Debug("mcp.redial", slog.Int("attempt", failsBefore+1), slog.Bool("ok", true))
	}
	return fresh, nil
}

// redialDelay computes the full-jitter backoff for the CURRENT failure count
// (call with csMu held, after incrementing redialFails).
func (p *mcpProxy) redialDelay() time.Duration {
	base, ceil := p.redialBase, p.redialCap
	if base <= 0 {
		base = defaultRedialBase
	}
	if ceil <= 0 {
		ceil = defaultRedialCap
	}
	d := base
	for i := 1; i < p.redialFails && d < ceil; i++ {
		d *= 2
	}
	if d > ceil {
		d = ceil
	}
	// Full jitter spreads a fleet whose sessions died together (one pod
	// restart kills them all); the 1ms floor keeps the gate armed even with
	// test-collapsed tunables.
	return time.Millisecond + rand.N(d) // #nosec G404 -- herd-spreading jitter, not a security boundary
}

// log records one proxy lifecycle breadcrumb to the client error log
// (`straza logs`); nil-safe because tests wire no logger.
func (p *mcpProxy) log(event, msg string) {
	if p.logf != nil {
		p.logf(event, msg)
	}
}

// sessionDeadErr reports whether err marks the gateway client session itself
// as a corpse: the go-sdk streamable client latches closed once its
// standalone SSE stream exhausts reconnection, and from then on every send is
// refused locally. This class triggers REVIVAL, which is always safe: a
// fresh dial replays nothing by itself.
func sessionDeadErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mcp.ErrConnectionClosed) {
		return true
	}
	// The sdk does not wrap the sentinel on every latched path (its own
	// tests hedge on the string), so match the latched spelling too. Pinned
	// against the real latched client in TestMCPProxyRevivesLatchedSession.
	return strings.Contains(err.Error(), "client is closing")
}

// neverSentErr reports whether err PROVES the failed request never left the
// process, the pre-send refusal of a latched client. ONLY this class may be
// replayed by the proxy: any error after a request hit the wire is
// indeterminate (the gateway may have executed the call), and plumbing must
// never replay a possibly-executed governed call. The indeterminate-but-dead
// case still revives the lane; the agent is told the session recovered and
// decides about a re-call itself, with full context.
func neverSentErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "client is closing")
}
