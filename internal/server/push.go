package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ---- Gateway-edge push ----
//
// Daemons hold ONE SSE stream to the strazad/gateway pod they already talk
// HTTPS to, authenticated by their session token; each pod holds ONE core-NATS
// subscription on straza.push.> and fans messages out locally. No client
// ever needs an exposed, unauthenticated broker, and the connection count
// spreads across pods that already scale horizontally. It is the ONLY
// client push lane, because no
// client-reachable broker exists in any profile.
//
// The lane is an ACCELERATOR, never a dependency: subscriber lists are soft
// per-pod state rebuilt on reconnect (exactly like the denylist), a slow
// consumer drops messages rather than blocking the hub, and every daemon
// keeps its poll-refresh loop regardless; a missed push costs latency,
// nothing else.

// The events of the stream. A daemon ends its session on a revocation and
// only renews on the other two, so the hub names every message by the
// subject it arrived on and has no event it falls back to.
const (
	pushEventRevocation = "revocation"
	pushEventPolicy     = "policy"
	pushEventRoles      = "roles"
)

type pushMsg struct {
	event string
	data  []byte
}

// pushConn is one stream: the event each of its subjects goes out as, and
// its buffer.
type pushConn struct {
	events map[string]string
	ch     chan pushMsg
}

type pushHub struct {
	mu    sync.Mutex
	conns map[int64]*pushConn
	// bySubject holds the streams of each subject, so a message costs its
	// own streams and not a walk of every stream of the server.
	bySubject map[string]map[int64]*pushConn
	next      int64
	// closing is closed by close and ends every stream.
	closing   chan struct{}
	closeOnce sync.Once
}

func newPushHub() *pushHub {
	return &pushHub{conns: map[int64]*pushConn{}, bySubject: map[string]map[int64]*pushConn{}, closing: make(chan struct{})}
}

// close ends every open stream and any stream that starts later. The main
// listener calls it when its Shutdown starts: a stream ends only when its
// daemon leaves, so Shutdown would otherwise wait out its whole bound for
// every connected daemon, and the daemon reconnects to a live pod.
func (h *pushHub) close() { h.closeOnce.Do(func() { close(h.closing) }) }

// register adds a subscriber for the subjects that are the keys of events,
// each with the event its messages go out as, refusing over maxConns
// (the per-pod cap; the caller answers 503 and the daemon polls until a
// retry lands). The channel is buffered so a briefly-slow stream loses
// nothing; a persistently-slow one drops (see fanout).
func (h *pushHub) register(events map[string]string, maxConns int) (int64, <-chan pushMsg, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if maxConns > 0 && len(h.conns) >= maxConns {
		return 0, nil, fmt.Errorf("push: pod connection cap reached (%d)", maxConns)
	}
	h.next++
	id := h.next
	c := &pushConn{events: events, ch: make(chan pushMsg, 8)}
	h.conns[id] = c
	for subject := range events {
		if h.bySubject[subject] == nil {
			h.bySubject[subject] = map[int64]*pushConn{}
		}
		h.bySubject[subject][id] = c
	}
	return id, c.ch, nil
}

func (h *pushHub) unregister(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.conns[id]
	if !ok {
		return
	}
	delete(h.conns, id)
	for subject := range c.events {
		delete(h.bySubject[subject], id)
		if len(h.bySubject[subject]) == 0 {
			delete(h.bySubject, subject)
		}
	}
}

// count reports this pod's live subscriptions (the fleet-health surface:
// connected-vs-polling is per pod by construction; subscriber lists are
// soft local state).
func (h *pushHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// fanout delivers one straza.push.> message to every subscriber that
// registered its subject, as the event that subscriber registered for it. A
// subject no stream registered reaches nobody. Non-blocking by doctrine: a
// full buffer drops the message;
// the daemon's poll loop is the correctness backstop, push is only speed.
func (h *pushHub) fanout(subject string, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.bySubject[subject] {
		select {
		case c.ch <- pushMsg{event: c.events[subject], data: data}:
		default:
		}
	}
}

// rolesPushSubject answers the subject of the role nudge of the user
// userID.
func rolesPushSubject(userID string) string { return "straza.push.roles." + userID }

// pushRolesNudge tells the connected sessions of the user userID that the
// roles the user holds changed, for a grant and for a removal, so each
// renews and gets its roles resolved again. The caller publishes after role
// resolution was bumped, because a session renews as soon as it hears. It
// is best-effort core NATS and names no role: a session that misses it
// follows at its next renewal.
func (a *App) pushRolesNudge(userID string) {
	if a.bus == nil || userID == "" {
		return
	}
	payload, err := json.Marshal(map[string]string{"kind": "roles", "id": userID})
	if err != nil {
		return
	}
	_ = a.bus.PublishCore(rolesPushSubject(userID), payload)
}

// rolesBroadcastSubject is the role nudge of every connected session, for
// a change to what a role contains or reaches rather than to who holds it.
const rolesBroadcastSubject = "straza.push.roles"

// pushRolesBroadcast tells every connected session that what a role
// contains or reaches changed, so each renews and gets its roles resolved
// again. The caller publishes after role resolution was bumped, because a
// session renews as soon as it hears. It is best-effort core NATS and names
// no role and no user: a session that misses it follows at its next
// renewal.
func (a *App) pushRolesBroadcast() {
	if a.bus == nil {
		return
	}
	_ = a.bus.PublishCore(rolesBroadcastSubject, []byte(`{"kind":"roles"}`))
}

// handlePushSubscribe serves GET /v1/push: the daemon's edge push stream.
// Auth is the session token, verified in memory + the in-memory denylist:
// no DB touch on this path; the subjects come from the
// token's own claims, so a session can only ever hear about itself, its
// user, its user's role changes, its device, and the public policy nudge and
// role broadcast.
func (a *App) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	raw, scheme := sessionCredential(r)
	claims, err := a.verifySessionToken(w, r, raw, scheme)
	if errors.Is(err, errAnswered) {
		return
	}
	if err != nil || claims.Session == "" {
		apiError(w, http.StatusUnauthorized, "session token rejected")
		return
	}
	if a.denylist.blocked(claims) {
		apiError(w, http.StatusUnauthorized, "session revoked")
		return
	}
	if a.pushHub == nil {
		w.Header().Set("Retry-After", "30")
		a.fail(w, r, http.StatusServiceUnavailable, "push lane unavailable; poll-refresh covers you", nil)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		a.fail(w, r, http.StatusInternalServerError, "streaming unsupported", nil)
		return
	}

	subjects := map[string]string{
		"straza.push.session." + claims.Session: pushEventRevocation,
		"straza.push.user." + claims.Subject:    pushEventRevocation,
		rolesPushSubject(claims.Subject):        pushEventRoles,
		rolesBroadcastSubject:                   pushEventRoles,
		policyPushSubject:                       pushEventPolicy,
	}
	if claims.Device != "" {
		subjects["straza.push.device."+claims.Device] = pushEventRevocation
	}

	id, ch, err := a.pushHub.register(subjects, a.cfg.EffectivePushEdgeMaxConns())
	if err != nil {
		w.Header().Set("Retry-After", "30")
		a.fail(w, r, http.StatusServiceUnavailable, "push lane at capacity; poll-refresh covers you", err)
		return
	}
	defer a.pushHub.unregister(id)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	// retry: the browser/daemon reconnect hint; ready: tells the daemon the
	// lane is live (doctor and logs say "push", not "hoping").
	fmt.Fprintf(w, "retry: 3000\n\nevent: ready\ndata: {\"subjects\":%d}\n\n", len(subjects))
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.pushHub.closing:
			return
		case msg := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.event, msg.data)
			flusher.Flush()
		case <-heartbeat.C:
			// Comment line: keeps NATs/proxies/LBs from idling the stream out.
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
