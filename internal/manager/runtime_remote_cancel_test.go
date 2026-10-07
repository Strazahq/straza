package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// methodUpstream is an in-process streamable-HTTP MCP server that records the
// JSON-RPC method of every POST it receives and DELETE for every session
// termination. Its tool echo answers at once, its tool wait answers when its
// caller cancels it, failCalls makes every tools/call answer HTTP 500, and a
// tools/call of the session named by hang gets no answer at all. failPing
// makes every ping answer HTTP 500. A ping of the session named by hangPing,
// or of any session when it is "*", and every request while stalled, as a
// stopped process, is held until its client gives up or the test ends. slow
// delays the answer to a method, and redirectDelete answers every DELETE
// with a redirect to /held, whose GET is held the same way.
type methodUpstream struct {
	*httptest.Server
	mu             sync.Mutex
	methods        []string
	failCalls      bool
	failPing       bool
	hang           string
	hangPing       string
	stalled        bool
	slow           map[string]time.Duration
	redirectDelete bool
	release        chan struct{}
	unhold         sync.Once
	started        chan struct{}
}

func newMethodUpstream(t *testing.T) *methodUpstream {
	t.Helper()
	u := &methodUpstream{started: make(chan struct{}, 1), release: make(chan struct{}), slow: map[string]time.Duration{}}
	srv := mcp.NewServer(&mcp.Implementation{Name: "method-upstream", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo"}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "wait", Description: "wait"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			u.started <- struct{}{}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "waited"}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var msg struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &msg)
			method = msg.Method
		}
		u.mu.Lock()
		u.methods = append(u.methods, method)
		fail := (u.failCalls && method == "tools/call") || (u.failPing && method == "ping")
		hang := u.hang != "" && method == "tools/call" && r.Header.Get("Mcp-Session-Id") == u.hang
		hold := u.stalled || r.URL.Path == "/held" ||
			(method == "ping" && (u.hangPing == "*" || (u.hangPing != "" && r.Header.Get("Mcp-Session-Id") == u.hangPing)))
		redirect := u.redirectDelete && method == http.MethodDelete
		slow := u.slow[method]
		u.mu.Unlock()
		if redirect {
			http.Redirect(w, r, "/held", http.StatusFound)
			return
		}
		time.Sleep(slow)
		if hold {
			select {
			case <-r.Context().Done():
			case <-u.release:
			}
			return
		}
		if hang {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		if fail {
			http.Error(w, "upstream broke", http.StatusInternalServerError)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *methodUpstream) count(method string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, m := range u.methods {
		if m == method {
			n++
		}
	}
	return n
}

func (u *methodUpstream) failToolCalls() {
	u.mu.Lock()
	u.failCalls = true
	u.mu.Unlock()
}

func (u *methodUpstream) hangSession(id string) {
	u.mu.Lock()
	u.hang = id
	u.mu.Unlock()
}

func (u *methodUpstream) failPings() {
	u.mu.Lock()
	u.failPing = true
	u.mu.Unlock()
}

// hangPings holds every ping of the session id, or of every session when id
// is "*". Held requests end when the test ends, before its other cleanups.
func (u *methodUpstream) hangPings(t *testing.T, id string) {
	t.Cleanup(u.endHolds)
	u.mu.Lock()
	u.hangPing = id
	u.mu.Unlock()
}

// stall holds every request from now on, as a process that was stopped.
// Held requests end when the test ends, before its other cleanups.
func (u *methodUpstream) stall(t *testing.T) {
	t.Cleanup(u.endHolds)
	u.mu.Lock()
	u.stalled = true
	u.mu.Unlock()
}

func (u *methodUpstream) slowDown(method string, d time.Duration) {
	u.mu.Lock()
	u.slow[method] = d
	u.mu.Unlock()
}

// redirectDeletes answers every DELETE with a redirect to /held from now on.
// Held requests end when the test ends, before its other cleanups.
func (u *methodUpstream) redirectDeletes(t *testing.T) {
	t.Cleanup(u.endHolds)
	u.mu.Lock()
	u.redirectDelete = true
	u.mu.Unlock()
}

func (u *methodUpstream) endHolds() {
	u.unhold.Do(func() { close(u.release) })
}

// TestRemoteRuntimeCallerCancelKeepsSession pins that a caller's cancel never
// closes the upstream session every caller of that credential shares: a
// caller that left before its call was sent reaches the upstream with no
// tools/call at all, a caller that left while its call ran cancels only that
// call, and in both cases the pooled session stays, the upstream sees no
// session termination and the next call reuses it. A call that reaches its
// own deadline still evicts the session, so one that stops answering is
// replaced on the next call, and so does an error of the session itself.
func TestRemoteRuntimeCallerCancelKeepsSession(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// call runs one call whose caller leaves at the row's moment and
		// returns its error.
		call      func(r *RemoteRuntime, u *methodUpstream, shared *mcp.ClientSession) error
		wantErr   error
		wantCalls int // tools/call POSTs the row's call reached the upstream with
		wantKept  bool
		// wantNext says the next call works: on the kept session, or on a
		// new one when the row evicts.
		wantNext bool
	}{
		{"the caller left before the call was sent", func(r *RemoteRuntime, _ *methodUpstream, _ *mcp.ClientSession) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := r.Call(ctx, CallInput{Tool: "echo"})
			return err
		}, context.Canceled, 0, true, true},
		{"the caller left while the call ran", func(r *RemoteRuntime, u *methodUpstream, _ *mcp.ClientSession) error {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { <-u.started; cancel() }()
			_, err := r.Call(ctx, CallInput{Tool: "wait"})
			return err
		}, context.Canceled, 1, true, true},
		{"the call's own deadline passed while it ran", func(r *RemoteRuntime, _ *methodUpstream, _ *mcp.ClientSession) error {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_, err := r.Call(ctx, CallInput{Tool: "wait"})
			return err
		}, context.DeadlineExceeded, 1, false, true},
		{"a session that stops answering is replaced on the next call", func(r *RemoteRuntime, u *methodUpstream, shared *mcp.ClientSession) error {
			u.hangSession(shared.ID())
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			_, err := r.Call(ctx, CallInput{Tool: "echo"})
			return err
		}, context.DeadlineExceeded, 1, false, true},
		{"an upstream failure still evicts the session", func(r *RemoteRuntime, u *methodUpstream, _ *mcp.ClientSession) error {
			u.failToolCalls()
			_, err := r.Call(context.Background(), CallInput{Tool: "echo"})
			return err
		}, nil, 1, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := newMethodUpstream(t)
			r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
			t.Cleanup(r.Stop)
			if _, err := r.Call(context.Background(), CallInput{Tool: "echo"}); err != nil {
				t.Fatalf("warm call: %v", err)
			}
			r.mu.Lock()
			shared := r.pool[""]
			r.mu.Unlock()

			err := tc.call(r, up, shared)
			if err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("call error = %v, want %v", err, tc.wantErr)
			}
			if n := up.count("tools/call"); n != 1+tc.wantCalls {
				t.Errorf("tools/call POSTs = %d, want %d", n, 1+tc.wantCalls)
			}
			r.mu.Lock()
			kept := r.pool[""] == shared
			r.mu.Unlock()
			if kept != tc.wantKept {
				t.Fatalf("the shared session kept = %v, want %v", kept, tc.wantKept)
			}
			if tc.wantKept {
				if n := up.count(http.MethodDelete); n != 0 {
					t.Errorf("session terminations the upstream saw = %d, want 0", n)
				}
			}
			if !tc.wantNext {
				return
			}
			if _, err := r.Call(context.Background(), CallInput{Tool: "echo"}); err != nil {
				t.Fatalf("the next call: %v", err)
			}
			wantOpened := 1
			if !tc.wantKept {
				wantOpened = 2
			}
			if n := up.count("initialize"); n != wantOpened {
				t.Errorf("sessions opened = %d, want %d", n, wantOpened)
			}
		})
	}
}
