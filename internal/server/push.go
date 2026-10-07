package server

import (
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

type pushMsg struct {
	subject string
	data    []byte
}

type pushConn struct {
	subjects map[string]bool
	ch       chan pushMsg
}

type pushHub struct {
	mu    sync.Mutex
	conns map[int64]*pushConn
	next  int64
	// closing is closed by close and ends every stream.
	closing   chan struct{}
	closeOnce sync.Once
}

func newPushHub() *pushHub {
	return &pushHub{conns: map[int64]*pushConn{}, closing: make(chan struct{})}
}

// close ends every open stream and any stream that starts later. The main
// listener calls it when its Shutdown starts: a stream ends only when its
// daemon leaves, so Shutdown would otherwise wait out its whole bound for
// every connected daemon, and the daemon reconnects to a live pod.
func (h *pushHub) close() { h.closeOnce.Do(func() { close(h.closing) }) }

// register adds a subscriber for the given subjects, refusing over maxConns
// (the per-pod cap; the caller answers 503 and the daemon polls until a
// retry lands). The channel is buffered so a briefly-slow stream loses
// nothing; a persistently-slow one drops (see fanout).
func (h *pushHub) register(subjects []string, maxConns int) (int64, <-chan pushMsg, error) {
	set := make(map[string]bool, len(subjects))
	for _, s := range subjects {
		set[s] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if maxConns > 0 && len(h.conns) >= maxConns {
		return 0, nil, fmt.Errorf("push: pod connection cap reached (%d)", maxConns)
	}
	h.next++
	id := h.next
	c := &pushConn{subjects: set, ch: make(chan pushMsg, 8)}
	h.conns[id] = c
	return id, c.ch, nil
}

func (h *pushHub) unregister(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, id)
}

// count reports this pod's live subscriptions (the fleet-health surface:
// connected-vs-polling is per pod by construction; subscriber lists are
// soft local state).
func (h *pushHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// fanout delivers one straza.push.> message to every subscriber whose subject
// set names it. Non-blocking by doctrine: a full buffer drops the message;
// the daemon's poll loop is the correctness backstop, push is only speed.
func (h *pushHub) fanout(subject string, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.conns {
		if !c.subjects[subject] {
			continue
		}
		select {
		case c.ch <- pushMsg{subject: subject, data: data}:
		default:
		}
	}
}

// handlePushSubscribe serves GET /v1/push: the daemon's edge push stream.
// Auth is the session token, verified in memory + the in-memory denylist:
// no DB touch on this path; the subjects come from the
// token's own claims, so a session can only ever hear about itself, its
// user, its device, and the public policy nudge.
func (a *App) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	claims, err := a.tokens.Verify(bearerToken(r))
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

	subjects := []string{
		"straza.push.session." + claims.Session,
		"straza.push.user." + claims.Subject,
	}
	if claims.Device != "" {
		subjects = append(subjects, "straza.push.device."+claims.Device)
	}
	subjects = append(subjects, policyPushSubject)

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
			event := "revocation"
			if msg.subject == policyPushSubject {
				event = "policy"
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, msg.data)
			flusher.Flush()
		case <-heartbeat.C:
			// Comment line: keeps NATs/proxies/LBs from idling the stream out.
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
