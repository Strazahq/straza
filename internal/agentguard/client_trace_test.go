package agentguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/trace"
)

// The correlation capture for the decision journal: Client.do
// observes every answer into the trace.Call bound in the request context, so
// the journal names the exact server answer a decision depended on.

func TestClientDoObservesCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-"+r.URL.Path[1:])
		if r.URL.Path == "/boom" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"down"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()
	var out map[string]any

	call := &trace.Call{}
	if err := c.getJSON(trace.WithCall(ctx, call), "/ok", &out); err != nil {
		t.Fatal(err)
	}
	if st, id := call.Get(); st != 200 || id != "req-ok" {
		t.Errorf("observed (%d, %q), want (200, req-ok)", st, id)
	}
	if err := c.getJSON(trace.WithCall(ctx, call), "/boom", &out); err == nil {
		t.Fatal("503 must surface as an error")
	}
	if st, id := call.Get(); st != 503 || id != "req-boom" {
		t.Errorf("observed (%d, %q) after a refusal, want (503, req-boom)", st, id)
	}
	// No Call bound: nothing to observe, nothing to trip over.
	if err := c.getJSON(ctx, "/ok", &out); err != nil {
		t.Fatal(err)
	}
}
