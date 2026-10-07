package agentguard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/trace"
	"github.com/strazahq/straza/internal/policy"
)

// The correlation capture for the decision journal: Client.do
// observes every answer into the trace.Call bound in the request context, and
// the LocalPDP's online lanes carry that Call so the journal names the exact
// server answer a decision depended on, the retried one after a 401 bounce.

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

// TestServerCheckTraceFactsCarryCorrelation pins what the journal learns from
// the online lanes: the branch that ran, the server status and correlation id
// (the retried decide's after a 401 bounce), and fail-closed when the server
// could not be reached.
func TestServerCheckTraceFactsCarryCorrelation(t *testing.T) {
	fetchEvent := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolNetFetch, Paths: []string{"https://x.io"}}
	allow := func(w http.ResponseWriter, id string) {
		w.Header().Set("X-Request-Id", id)
		_ = json.NewEncoder(w).Encode(map[string]any{"effect": "allow", "ruleId": "central-ok"})
	}

	t.Run("server lane", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/decide" {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			allow(w, "corr-decide-1")
		}))
		defer srv.Close()
		p := serverCheckPDP(t, srv.URL)
		if d := p.Decide(Normalized{Event: fetchEvent}); d.Effect != policy.EffectAllow {
			t.Fatalf("decision = %+v", d)
		}
		f := p.traceFacts()
		if f.branch != "server" || f.status != 200 || f.correlation != "corr-decide-1" || f.bounced || f.failClosed || f.classified {
			t.Errorf("facts = %+v", f)
		}
	})

	t.Run("401 bounce: the retried decide wins", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /v1/decide", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer tok-1" {
				w.Header().Set("X-Request-Id", "corr-decide-refused")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"token expired"}`))
				return
			}
			allow(w, "corr-decide-2")
		})
		mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Request-Id", "corr-refresh")
			_, _ = w.Write([]byte(`{"session_id":"s1","session_token":"tok-2","expires_in":300,"roles":["dev"]}`))
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		p := serverCheckPDP(t, srv.URL)
		p.store = daemonStore(t) // the bounce adopts the refreshed session into state
		p.cfg = Config{ServerURL: srv.URL}
		if d := p.Decide(Normalized{Event: fetchEvent}); d.Effect != policy.EffectAllow {
			t.Fatalf("decision after bounce = %+v", d)
		}
		f := p.traceFacts()
		if f.branch != "server" || !f.bounced || f.status != 200 || f.correlation != "corr-decide-2" || f.failClosed {
			t.Errorf("facts = %+v", f)
		}
	})

	t.Run("unreachable server is fail-closed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		srv.Close()
		p := serverCheckPDP(t, srv.URL)
		if d := p.Decide(Normalized{Event: fetchEvent}); d.Effect != policy.EffectDeny {
			t.Fatalf("offline serverCheck allowed: %+v", d)
		}
		f := p.traceFacts()
		if f.branch != "server" || !f.failClosed || f.status != 0 || f.correlation != "" {
			t.Errorf("facts = %+v", f)
		}
	})
}
