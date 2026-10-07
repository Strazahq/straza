package trace

import (
	"context"
	"sync"
)

// maxRequestID bounds the correlation id a record carries (the server mints
// UUIDs and accepts sane client ids up to 64 bytes).
const maxRequestID = 64

// Call observes one server round trip for the journal: the HTTP status and
// the X-Request-Id the server answered with, which is the correlation id that
// joins a client journal line to the server's own log record. The decision
// path puts a Call into the context it hands the HTTP client; the transport
// layer calls Observe on every response (the last response wins, so a 401
// bounce followed by the retried call reports the retry).
type Call struct {
	mu        sync.Mutex
	status    int
	requestID string
}

type callKey struct{}

// WithCall binds c into ctx (a nil c hands ctx back unchanged).
func WithCall(ctx context.Context, c *Call) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, callKey{}, c)
}

// CallFrom returns the Call bound by WithCall, or nil.
func CallFrom(ctx context.Context) *Call {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(callKey{}).(*Call)
	return c
}

// Observe records one response; nil-safe so transports never guard.
func (c *Call) Observe(status int, requestID string) {
	if c == nil {
		return
	}
	if len(requestID) > maxRequestID {
		requestID = requestID[:maxRequestID]
	}
	c.mu.Lock()
	c.status, c.requestID = status, requestID
	c.mu.Unlock()
}

// Get returns the last observed status and correlation id (0, "" when none).
func (c *Call) Get() (status int, requestID string) {
	if c == nil {
		return 0, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status, c.requestID
}
