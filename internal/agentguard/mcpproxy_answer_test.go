package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// callAnswer stands in for strazad's answer to one tools/call. It gets the
// call's id and tool name and reports false to let the go-sdk server answer.
type callAnswer func(w http.ResponseWriter, id json.RawMessage, name string) bool

// newCallGateway is a real sdk-served gateway whose catalog holds
// everything__echo, with every tools/call offered to answer first. It
// returns the gateway and the tool names of every tools/call it received.
func newCallGateway(t *testing.T, answer callAnswer) (*fakeGateway, func() []string) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "gateway", Version: "1.0.0"}, nil)
	type echoArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "everything__echo", Description: "echo through the gateway"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "gateway: " + a.Text}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	var mu sync.Mutex
	var names []string
	g := &fakeGateway{token: "ses-token-1", mcpSrv: srv}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", g.handleCheckin)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		want := "Bearer " + g.token
		g.mu.Unlock()
		if r.Header.Get("Authorization") != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			if json.Unmarshal(body, &req) == nil && req.Method == "tools/call" {
				mu.Lock()
				names = append(names, req.Params.Name)
				mu.Unlock()
				if answer(w, req.ID, req.Params.Name) {
					return
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		handler.ServeHTTP(w, r)
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), names...)
	}
}

// TestMCPProxyCallAnswers pins what the agent reads for each answer to a
// tools/call. A name outside the bridge's mirror goes to the gateway like
// any other call, so strazad records it and words the answer, and the
// bridge never echoes the caller's name itself. A sentence the gateway
// answered reaches the agent as the gateway wrote it, once. A failure the
// gateway did not answer gets the bridge's own sentence, which
// TestMCPProxyAnswerLost pins.
func TestMCPProxyCallAnswers(t *testing.T) {
	long := "ghost__" + strings.Repeat("x", 64*1024-len("ghost__"))
	// cut is the gateway's answer to a name it does not know: the name cut
	// to 256 bytes, then its full length.
	cut := fmt.Sprintf("unknown tool %q", long[:256]+fmt.Sprintf("... (%d bytes)", len(long)))
	const (
		auditBlocked = "Straza: this action did not run, because its audit record could not be written while Straza cannot reach its database. " +
			"Try the call once more in a minute. If it is denied again, tell the user."
		upstreamDown = "upstream call failed: dial tcp 10.0.0.9:443: connect: connection refused"
	)
	rpcError := func(code int, message string) callAnswer {
		return func(w http.ResponseWriter, id json.RawMessage, _ string) bool {
			writeRPC(w, id, map[string]any{"error": map[string]any{"code": code, "message": message}})
			return true
		}
	}
	cases := []struct {
		name   string
		tool   string
		answer callAnswer
		// wantText is the result's text when the call succeeds. Otherwise
		// wantCode and wantMsg are the JSON-RPC error the agent reads, and
		// wantPrefix with wantEnd bound a message that carries the sdk's
		// own words in between.
		wantText            string
		wantCode            int64
		wantMsg             string
		wantPrefix, wantEnd string
	}{
		{name: "a name outside the mirror reaches the gateway, which answers it",
			tool: long, answer: rpcError(-32602, cut),
			wantCode: -32602, wantMsg: cut},
		{name: "a name the gateway runs before the bridge mirrors it",
			tool: "everything__late",
			answer: func(w http.ResponseWriter, id json.RawMessage, _ string) bool {
				writeRPC(w, id, map[string]any{"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "late ran"}}}})
				return true
			},
			wantText: "late ran"},
		{name: "a gateway sentence that starts with Straza reaches the agent once",
			tool: "everything__echo", answer: rpcError(-32603, auditBlocked),
			wantCode: -32603, wantMsg: auditBlocked},
		{name: "a gateway sentence without the prefix reaches the agent as written",
			tool: "everything__echo", answer: rpcError(-32000, upstreamDown),
			wantCode: -32000, wantMsg: upstreamDown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, received := newCallGateway(t, tc.answer)
			store := proxyTestStore(t, g)
			ctx, _, harness := buildProxy(t, g, store)

			out, err := harness.CallTool(ctx, &mcp.CallToolParams{Name: tc.tool, Arguments: map[string]any{"text": "hi"}})
			if got := received(); len(got) != 1 || got[0] != tc.tool {
				t.Errorf("the gateway received %d calls, want exactly one carrying the whole name of %d bytes", len(got), len(tc.tool))
			}
			if tc.wantText != "" {
				if err != nil {
					t.Fatalf("call must succeed, got: %v", err)
				}
				if txt := out.Content[0].(*mcp.TextContent).Text; txt != tc.wantText {
					t.Errorf("result = %q, want %q", txt, tc.wantText)
				}
				return
			}
			var rpcErr *jsonrpc.Error
			if !errors.As(err, &rpcErr) {
				t.Fatalf("the bridge must answer with a JSON-RPC error, got: %v", err)
			}
			msg := rpcErr.Message
			if len(msg) > 1024 {
				t.Errorf("the agent reads %d bytes, want the gateway's cut answer, never the whole name", len(msg))
			}
			if tc.wantMsg != "" && (rpcErr.Code != tc.wantCode || msg != tc.wantMsg) {
				t.Errorf("error code %d, text:\n%s\nwant code %d, text:\n%s", rpcErr.Code, shortText(msg), tc.wantCode, shortText(tc.wantMsg))
			}
			if tc.wantPrefix != "" && (!strings.HasPrefix(msg, tc.wantPrefix) || !strings.HasSuffix(msg, tc.wantEnd)) {
				t.Errorf("error text:\n%s\nwant it to start %q and end %q", shortText(msg), tc.wantPrefix, tc.wantEnd)
			}
		})
	}
}

// shortText keeps a failure message readable when the text under test
// carries a name of many kilobytes.
func shortText(s string) string {
	if len(s) <= 400 {
		return s
	}
	return fmt.Sprintf("%s... (%d bytes)", s[:400], len(s))
}

// TestMCPProxyAnswerLost pins the next step the bridge gives for a call the
// gateway did not answer. It asks for one more try only when the call
// provably did not run: a 429, a 503 the service marks as its own refusal
// before the send, and a service it could not reach at all. A 503 without
// the mark, a 502, a 504 and a dropped connection can follow a call
// strazad already ran, behind an ingress or the service's proxy, so the
// agent reads that the outcome is unknown and is never invited to send the
// call again blind. A call replayed on a new session after one that never
// left the bridge gets the same rule.
func TestMCPProxyAnswerLost(t *testing.T) {
	const (
		retry   = ". Try the call once more in a minute. If it fails again, tell the user to run `straza doctor`."
		unknown = ". The gateway's answer to this call did not arrive. The call may or may not have run. " +
			"Check whether its effect happened, or ask the user, before you send the call again"
	)
	// refuseBeforeSend is the service refusing every tools/call before it
	// sends it, marked as its own refusal.
	refuseBeforeSend := func(_ http.Handler, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(localapi.HeaderNotSent, "1")
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	// dropAfterRun is the service sending the call on and dropping the
	// connection before the answer comes back.
	dropAfterRun := func(inner http.Handler, w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(httptest.NewRecorder(), r)
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}
	cases := []struct {
		name string
		// status is the status the gateway's side answers after it ran the
		// call, 0 to let the call run and answer normally.
		status int
		// service handles every tools/call in the service in place of the
		// pass-through, nil to pass it through.
		service func(inner http.Handler, w http.ResponseWriter, r *http.Request)
		// unreachable removes the service's socket before the call.
		unreachable bool
		// replay makes the first send never leave the bridge, so the call
		// is replayed on a new session.
		replay  bool
		wantRan int64
		want    string
	}{
		{name: "429", status: http.StatusTooManyRequests, wantRan: 1, want: retry},
		{name: "503 from an ingress after the call ran", status: http.StatusServiceUnavailable, wantRan: 1, want: unknown},
		{name: "503 the service refused before the send", service: refuseBeforeSend, wantRan: 0, want: retry},
		{name: "502 after the call ran", status: http.StatusBadGateway, wantRan: 1, want: unknown},
		{name: "504 after the call ran", status: http.StatusGatewayTimeout, wantRan: 1, want: unknown},
		{name: "a dropped connection after the call ran", service: dropAfterRun, wantRan: 1, want: unknown},
		{name: "a service the bridge cannot reach", unreachable: true, wantRan: 0, want: retry},
		{name: "a replay the service refused before the send", service: refuseBeforeSend, replay: true, wantRan: 0, want: retry},
		{name: "a replay answered 502", status: http.StatusBadGateway, replay: true, wantRan: 1, want: unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran atomic.Int64
			g, _ := newCallGateway(t, func(w http.ResponseWriter, _ json.RawMessage, _ string) bool {
				ran.Add(1) // the call's effect happened upstream
				if tc.status == 0 {
					return false
				}
				w.WriteHeader(tc.status)
				return true
			})
			svc := proxyService(t, g)
			if tc.service != nil {
				inner := svc.mcp
				svc.mcp = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					r.Body = io.NopCloser(bytes.NewReader(body))
					if !strings.Contains(string(body), `"tools/call"`) {
						inner.ServeHTTP(w, r)
						return
					}
					tc.service(inner, w, r)
				})
			}
			ctx, p, harness := buildProxy(t, g, svc.store(t))
			p.jitterMax = 0 // a revive arms a resync, and this fires it at once
			if tc.replay {
				_ = p.session().Close()
			}
			if tc.unreachable {
				socket, _, _ := localapi.Paths(svc.home)
				if err := os.Remove(socket); err != nil {
					t.Fatal(err)
				}
			}
			_, err := harness.CallTool(ctx, &mcp.CallToolParams{Name: "everything__echo", Arguments: map[string]any{"text": "pay"}})
			if n := ran.Load(); n != tc.wantRan {
				t.Fatalf("the gateway ran the call %d times, want %d", n, tc.wantRan)
			}
			var rpcErr *jsonrpc.Error
			if !errors.As(err, &rpcErr) {
				t.Fatalf("the bridge must answer with a JSON-RPC error, got: %v", err)
			}
			if msg := rpcErr.Message; !strings.HasPrefix(msg, "Straza: gateway call failed: ") || !strings.HasSuffix(msg, tc.want) {
				t.Errorf("the agent reads:\n%s\nwant it to end:\n%s", msg, tc.want)
			}
		})
	}
}
