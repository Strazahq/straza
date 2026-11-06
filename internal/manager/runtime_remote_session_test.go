package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// restartUpstream is an in-process streamable-HTTP MCP server that can
// restart. restart swaps in a fresh handler, which knows none of the
// sessions the old one opened and answers each with HTTP 404, as a server
// that restarted does. runs counts the runs of its tool count, and posts
// the tools/call requests it received. While cut is set, a tools/call runs
// and its answer stream ends after one event and before the result, and the
// GET that resumes the stream answers HTTP 404, as a server that restarted
// while the call ran.
type restartUpstream struct {
	*httptest.Server
	mu      sync.Mutex
	handler http.Handler
	cut     bool
	runs    atomic.Int32
	posts   atomic.Int32
}

func newRestartUpstream(t *testing.T) *restartUpstream {
	t.Helper()
	u := &restartUpstream{}
	u.restart()
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			Method string `json:"method"`
		}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			_ = json.Unmarshal(body, &msg)
		}
		u.mu.Lock()
		handler, cut := u.handler, u.cut
		u.mu.Unlock()
		if msg.Method == "tools/call" {
			u.posts.Add(1)
		}
		switch {
		case cut && msg.Method == "tools/call":
			u.runs.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "id: 1\nretry: 10\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"level\":\"info\",\"data\":\"working\"}}\n\n")
			w.(http.Flusher).Flush()
		case cut && r.Method == http.MethodGet:
			http.Error(w, "session not found", http.StatusNotFound)
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// restart swaps in a fresh server, as a restart of the process does.
func (u *restartUpstream) restart() {
	srv := mcp.NewServer(&mcp.Implementation{Name: "restart-upstream", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "count", Description: "count"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			n := u.runs.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("run %d", n)}}}, nil, nil
		})
	u.mu.Lock()
	u.handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	u.mu.Unlock()
}

func (u *restartUpstream) cutStreams() {
	u.mu.Lock()
	u.cut = true
	u.mu.Unlock()
}

// TestRemoteRuntimeOpensANewSessionAfterARestart pins what a remote runtime
// does when the upstream no longer knows its pooled session: a ping and a
// call each evict the session, open a new one and run once more on it, so
// neither answers the raw "session not found". The call refused with HTTP
// 404 never ran, so the upstream runs it exactly once. A call the upstream
// took before its session ended is never sent again, since it may have run,
// and its error says so in a sentence.
func TestRemoteRuntimeOpensANewSessionAfterARestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rows := []struct {
		name      string
		cut       bool
		do        func(r *RemoteRuntime) (string, error)
		wantErr   string // the start of the error, "" for none
		wantRuns  int32  // runs of count after the restart
		wantPosts int32  // tools/call requests after the restart
	}{
		{"ping", false, func(r *RemoteRuntime) (string, error) { return "", r.Ping(ctx, nil) }, "", 0, 0},
		{"call", false, func(r *RemoteRuntime) (string, error) {
			res, err := r.Call(ctx, CallInput{Tool: "count"})
			if err != nil {
				return "", err
			}
			return textOf(res), nil
		}, "", 1, 2},
		{"call cut after the server took it", true, func(r *RemoteRuntime) (string, error) {
			_, err := r.Call(ctx, CallInput{Tool: "count"})
			return "", err
		}, "the MCP server restart ended its session while the call ran, so the call may have taken effect, and Straza does not send it again. " +
			"Check whether it took effect before you try it once more", 1, 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			up := newRestartUpstream(t)
			r := besideRuntime("restart", RemoteSpec{URL: up.URL, Auth: AuthInject}, nil)
			t.Cleanup(r.Stop)
			if _, err := r.Call(ctx, CallInput{Tool: "count"}); err != nil {
				t.Fatalf("warm call: %v", err)
			}
			up.restart()
			if row.cut {
				up.cutStreams()
			}
			runs, posts := up.runs.Load(), up.posts.Load()
			text, err := row.do(r)
			switch {
			case row.wantErr == "" && err != nil:
				t.Fatalf("after a restart = %v, want no error", err)
			case row.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), row.wantErr)):
				t.Fatalf("after a restart = %v, want %q", err, row.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "session not found") {
				t.Errorf("the error quotes the raw transport error: %v", err)
			}
			if row.cut && !errors.Is(err, ErrMayHaveRun) {
				t.Errorf("the error of a call that may have run = %v, want one that wraps ErrMayHaveRun", err)
			}
			if got := up.runs.Load() - runs; got != row.wantRuns {
				t.Errorf("the upstream ran count %d times after the restart, want %d", got, row.wantRuns)
			}
			if got := up.posts.Load() - posts; got != row.wantPosts {
				t.Errorf("the upstream received %d tools/call requests after the restart, want %d", got, row.wantPosts)
			}
			if row.name == "call" && text != "run 2" {
				t.Errorf("the call after the restart answers %q, want run 2", text)
			}
			if got := evictions(r); got != 1 {
				t.Errorf("%d evictions, want 1", got)
			}
			if !row.cut {
				if _, err := r.Call(ctx, CallInput{Tool: "count"}); err != nil {
					t.Errorf("the next call on the new session = %v", err)
				}
			}
		})
	}
}

// TestRemoteRuntimeConcurrentCallsAfterARestart pins calls that share one
// pooled session when the upstream restarts: no call runs twice and no call
// that failed ran, a call that never left Straza because another call's
// answer already ended the session is sent once on a new session, and no
// error quotes the transport's "session not found" or the dead session's
// id. A call that may have run says so with ErrMayHaveRun.
func TestRemoteRuntimeConcurrentCallsAfterARestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for round := range 10 {
		up := newRestartUpstream(t)
		r := besideRuntime("restart", RemoteSpec{URL: up.URL, Auth: AuthInject}, nil)
		if _, err := r.Call(ctx, CallInput{Tool: "count"}); err != nil {
			t.Fatalf("round %d: warm call: %v", round, err)
		}
		up.restart()
		before := up.runs.Load()
		const n = 16
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { _, errs[i] = r.Call(ctx, CallInput{Tool: "count"}) })
		}
		wg.Wait()
		r.Stop()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case strings.Contains(err.Error(), "session not found") || strings.Contains(err.Error(), "session ID"):
				t.Errorf("round %d: an error quotes the transport: %v", round, err)
			case !errors.Is(err, ErrMayHaveRun):
				t.Errorf("round %d: an error neither ran nor says it may have run: %v", round, err)
			}
		}
		if ran := int(up.runs.Load() - before); ran != ok {
			t.Errorf("round %d: the upstream ran %d calls and %d succeeded, so a call ran twice or a failed call ran", round, ran, ok)
		}
	}
}
