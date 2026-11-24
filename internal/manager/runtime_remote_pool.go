package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callerIdle is how long a caller's upstream session is kept after its last
// call. Nothing tells the runtime that a Straza session ended, so its
// upstream session is closed once it has gone unused this long.
const callerIdle = 600 * time.Second

// maxCallerSessions is how many upstream sessions one Straza session holds
// on one remote server, one for each label of its MCP connections.
const maxCallerSessions = 64

// poolKey identifies a pooled upstream session: the Straza session it
// serves, the label of the caller's MCP connection (CallInput.Conn), the
// credential row and a fingerprint of the credential's value,
// so rotating a credential (refresh worker, reconnect, `apps secret set`)
// can never reuse a session dialed under the previous token. session is
// empty for the app-level session, which serves inventory, health, views
// and the object listing. conn is empty for a client that sent no label,
// and cred and fp are empty for no credential.
type poolKey struct{ session, conn, cred, fp string }

// BusyError refuses a call that needs one more upstream session on the MCP
// server Server for a Straza session that holds maxCallerSessions there,
// each running a call. The call did not run.
type BusyError struct{ Server string }

func (e *BusyError) Error() string {
	return fmt.Sprintf("this session already runs %d calls at once on the MCP server %s, each on its own connection. Wait for one to finish, then call again",
		maxCallerSessions, e.Server)
}

// pooled is one upstream session with what the idle close reads. Both
// counters are atomic, so a call that succeeded never takes the runtime
// lock. A call that failed takes the lock to evict its session.
type pooled struct {
	s     *mcp.ClientSession
	calls atomic.Int32 // tool calls running on s
	used  atomic.Int64 // when s was last handed out or a call on it ended, in Unix nanoseconds
	once  bool         // s was superseded at its dial: it serves one use and is never pooled (entry)
}

// done ends one use of p. A superseded session closes on its own goroutine,
// as evict closes, whether the use succeeded or failed. A pooled one stays.
func (p *pooled) done() {
	if p.once {
		go func() { _ = p.s.Close() }()
	}
}

// newPooled answers the pool entry of the session s, used now.
func newPooled(s *mcp.ClientSession) *pooled {
	p := &pooled{s: s}
	p.used.Store(time.Now().UnixNano())
	return p
}

// call runs one tool call on p and counts it while it runs, so the idle
// close leaves p alone meanwhile and counts its idle time from the call's
// end.
func (p *pooled) call(ctx context.Context, in CallInput) (*mcp.CallToolResult, error) {
	p.calls.Add(1)
	defer func() {
		p.used.Store(time.Now().UnixNano())
		p.calls.Add(-1)
	}()
	return callTool(ctx, p.s, in)
}

// sessionKey answers the pool key of the Straza session and the credential.
func sessionKey(session string, secret *Secret) poolKey {
	if secret == nil {
		return poolKey{session: session}
	}
	sum := sha256.Sum256([]byte(secret.Value))
	return poolKey{session: session, cred: secret.ID, fp: hex.EncodeToString(sum[:8])}
}

// closeIdle drops every caller's session that runs no call and was last
// used idleAfter ago or longer, closes each on its own goroutine as evict
// does, and answers the sessions it kept. The app-level sessions, whose
// Straza session is empty, are never idle.
func (r *RemoteRuntime) closeIdle() map[poolKey]*mcp.ClientSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept, closed := make(map[poolKey]*mcp.ClientSession, len(r.pool)), 0
	for k, p := range r.pool {
		if k.session == "" || p.calls.Load() > 0 || time.Since(time.Unix(0, p.used.Load())) < r.idleAfter {
			kept[k] = p.s
			continue
		}
		delete(r.pool, k)
		go func() { _ = p.s.Close() }()
		closed++
	}
	if closed > 0 {
		r.ring.Append(fmt.Sprintf("closed caller sessions that ran no call for %s: %d", r.idleAfter, closed))
	}
	return kept
}

// entry returns the pool entry of the Straza session, the label of the
// caller's MCP connection and the credential, dialing lazily, and stamps
// its use. session and conn are empty for the app-level session. The dial
// runs off the runtime lock, so it holds up no call on another key. A call
// that finds a dial of its key in flight waits for that dial to end, or for
// ctx, and looks again. A dial for a Straza session that holds
// maxCallerSessions first makes room, or answers makeRoom's BusyError. A
// dial that ends after Stop closes its session and answers notRunning. A
// dial that ends after a dial of another value of its credential row started
// answers a superseded entry, which is never pooled. The caller ends each
// use of the entry with done.
func (r *RemoteRuntime) entry(ctx context.Context, session, conn string, secret *Secret) (*pooled, poolKey, error) {
	if r.inject != nil && secret == nil && !r.PerUser {
		// The manifest requires a credential, and calling upstream without one
		// would leak an uncredentialed request: fail closed (SPEC §3).
		// Per-user apps are exempt here for inventory/health only. Call has
		// its own unconditional guard.
		return nil, poolKey{}, errNoCredential
	}
	key := sessionKey(session, secret)
	key.conn = conn
	r.mu.Lock()
	for {
		if r.stopped {
			r.mu.Unlock()
			return nil, poolKey{}, notRunning(r.name)
		}
		if p, ok := r.pool[key]; ok {
			p.used.Store(time.Now().UnixNano())
			r.mu.Unlock()
			return p, key, nil
		}
		if err := ctx.Err(); err != nil {
			// The caller's time ran out while it waited for the lock or for
			// another dial of its key, so it dials nothing.
			r.mu.Unlock()
			return nil, poolKey{}, err
		}
		inFlight, ok := r.dials[key]
		if !ok {
			break
		}
		r.mu.Unlock()
		select {
		case <-inFlight:
		case <-ctx.Done():
		}
		r.mu.Lock()
	}
	if secret != nil {
		// Rotation and reconnect update the credential row in place, so a key on
		// the row ID alone would keep the superseded token on the wire until
		// an upstream error evicted it. The key fingerprints the value. Any
		// session left under the same credential with a stale value, whichever
		// Straza session it serves, dies here, closed off the lock as evict
		// closes, since Close waits for its calls.
		for k, old := range r.pool {
			if k.cred == key.cred && k.fp != key.fp {
				delete(r.pool, k)
				go func() { _ = old.s.Close() }()
				r.ring.Append("session superseded by rotated credential")
			}
		}
		r.newest[key.cred] = key.fp
	}
	if err := r.makeRoom(session); err != nil {
		r.mu.Unlock()
		return nil, poolKey{}, err
	}
	done := make(chan struct{})
	r.dials[key] = done
	r.mu.Unlock()
	ended := false
	defer func() {
		if !ended {
			// The dial panicked. Its entry goes all the same, so no later
			// call on the key waits for a dial that will never end.
			r.mu.Lock()
			delete(r.dials, key)
			close(done)
			r.mu.Unlock()
		}
	}()

	s, err := r.dial(ctx, secret)
	ended = true
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.dials, key)
	close(done)
	switch {
	case err != nil:
		return nil, poolKey{}, err
	case r.stopped:
		// Stop did not wait for this dial, so the session closes here, off
		// the lock as evict closes.
		go func() { _ = s.Close() }()
		return nil, poolKey{}, notRunning(r.name)
	case secret != nil && r.newest[key.cred] != key.fp:
		// A dial of another value started on the row while this one dialed,
		// and the dial that started last wins, so this session serves only
		// the use that asked for it (pooled.done).
		r.ring.Append("session superseded by rotated credential")
		return &pooled{s: s, once: true}, key, nil
	}
	p := newPooled(s)
	r.pool[key] = p
	return p, key, nil
}

// makeRoom keeps the Straza session within maxCallerSessions before entry
// dials one more upstream session for it, counting its dials in flight with
// its pooled sessions. At the cap it drops the least recently used pooled
// session of that Straza session that runs no call and closes it on its own
// goroutine, as evict does, and never a dial in flight. When nothing idle is
// left it answers a BusyError. It never touches another Straza session's
// sessions, and Straza's own, whose session is empty, have no cap. A
// superseded session serving its one use is neither pooled nor in flight, so
// the Straza session may hold one more than the cap for each meanwhile. The
// caller holds r.mu.
func (r *RemoteRuntime) makeRoom(session string) error {
	if session == "" {
		return nil
	}
	held, lru := 0, poolKey{}
	for k := range r.dials {
		if k.session == session {
			held++
		}
	}
	var oldest *pooled
	for k, p := range r.pool {
		if k.session != session {
			continue
		}
		held++
		if p.calls.Load() == 0 && (oldest == nil || p.used.Load() < oldest.used.Load()) {
			lru, oldest = k, p
		}
	}
	switch {
	case held < maxCallerSessions:
		return nil
	case oldest == nil:
		return &BusyError{Server: r.name}
	}
	delete(r.pool, lru)
	go func() { _ = oldest.s.Close() }()
	r.ring.Append(fmt.Sprintf("closed the least recently used caller session of a Straza session that holds %d", maxCallerSessions))
	return nil
}
