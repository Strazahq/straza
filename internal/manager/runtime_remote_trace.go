package manager

import (
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
)

// traceKey is the context key under which a tool call carries the
// callTrace that markTrace writes.
type traceKey struct{}

// callTrace records what became of the POST that carries one tool call:
// sent once net/http wrote it whole, and status, the HTTP status of the
// answer, 0 while none came.
type callTrace struct {
	sent   atomic.Bool
	status atomic.Int32
}

// refusedWith answers the status the upstream answered the call's POST with
// in place of running it, a 4xx status other than 408, and 0 for any other
// answer or none. A 404 to the call's session is among them, which the MCP
// streamable HTTP transport gives for a session the server does not know.
func (t *callTrace) refusedWith() int {
	if s := int(t.status.Load()); s >= 400 && s < 500 && s != http.StatusRequestTimeout {
		return s
	}
	return 0
}

// ran reports whether the upstream may have run the call: its POST was
// written whole, and the upstream did not answer it with a status it gives
// in place of running a call.
func (t *callTrace) ran() bool { return t.sent.Load() && t.refusedWith() == 0 }

// markTrace writes the callTrace a POST's context carries under traceKey.
// While the call's context lasts, the POST that carries the call is the
// only POST under it, so the trace speaks of that very call. A POST counts
// as sent only once net/http wrote it whole, so a failed dial or TLS
// handshake, a dial Straza's address check refused and a connection the
// server closed as idle before it read the POST leave it unsent.
type markTrace struct{ base http.RoundTripper }

func (m markTrace) RoundTrip(req *http.Request) (*http.Response, error) {
	tr, _ := req.Context().Value(traceKey{}).(*callTrace)
	if tr == nil || req.Method != http.MethodPost {
		return m.base.RoundTrip(req)
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				tr.sent.Store(true)
			}
		},
	}))
	resp, err := m.base.RoundTrip(req)
	switch {
	case err == nil:
		if c := resp.StatusCode; c >= 100 && c <= 999 {
			tr.status.Store(int32(c))
		}
	case strings.Contains(err.Error(), serverClosedIdle):
		// The server had closed the idle connection while the POST was
		// written to it, so the server never read it.
		tr.sent.Store(false)
	}
	return resp, err
}

// serverClosedIdle is the text of net/http's unexported error for a request
// written to a keep-alive connection the server had already closed as idle.
const serverClosedIdle = "http: server closed idle connection"
