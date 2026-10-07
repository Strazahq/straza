package server

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---- SSE hub (server→client notifications, per live session) ----
//
// The gateway advertises capabilities.tools.listChanged and keeps one standing
// SSE stream per live client (GET /mcp). broadcast fans a notification out to
// every stream; notifySession targets exactly one session's streams. Both send
// non-blocking and drop on a slow consumer by design: there is no resumability,
// the client re-lists on reconnect and the proxy's periodic resync is the
// backstop for a dropped notification.

type sseHub struct {
	mu    sync.Mutex
	subs  map[int64]chan string
	next  int64
	bySes map[int64]string
	// closing is closed by close and ends every stream.
	closing   chan struct{}
	closeOnce sync.Once
}

func newSSEHub() *sseHub {
	return &sseHub{subs: map[int64]chan string{}, bySes: map[int64]string{}, closing: make(chan struct{})}
}

// close ends every open stream and any stream that starts later, for the
// same reason as pushHub.close: the main listener's Shutdown would otherwise
// wait out its whole bound for every connected MCP client.
func (h *sseHub) close() { h.closeOnce.Do(func() { close(h.closing) }) }

func (h *sseHub) broadcast(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- msg:
		default: // slow stream: drop; the client re-lists on reconnect
		}
	}
}

// notifySession pushes msg only to the live streams belonging to one session
// (bySes[id] == sessionID) with the same non-blocking send as broadcast. Used
// by the checkin role-change nudge so exactly the affected session re-lists
// after its roles change under it: no cross-session leak.
func (h *sseHub) notifySession(sessionID, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.subs {
		if h.bySes[id] != sessionID {
			continue
		}
		select {
		case ch <- msg:
		default: // slow stream: drop; the client re-lists on reconnect
		}
	}
}

// serve keeps one SSE stream open until the client disconnects.
func (h *sseHub) serve(w http.ResponseWriter, r *http.Request, sessionID string) {
	fl, ok := w.(http.Flusher)
	if !ok || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		apiError(w, http.StatusMethodNotAllowed, "GET /mcp requires Accept: text/event-stream")
		return
	}
	ch := make(chan string, 8)
	h.mu.Lock()
	h.next++
	id := h.next
	h.subs[id] = ch
	h.bySes[id] = sessionID
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, id)
		delete(h.bySes, id)
		h.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.closing:
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			fl.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}
