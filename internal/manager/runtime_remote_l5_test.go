package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newListHoldUpstream starts an MCP server over streamable HTTP that answers
// the handshake and every ping at once and holds every tools/list request
// until its client gives up or the test ends.
func newListHoldUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "list-hold", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	release := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var msg struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		if msg.Method == "tools/list" {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	t.Cleanup(func() { close(release) })
	return hs
}

// TestRemoteRuntimeToolsEndsByTheDeadline pins that the tool listing of a
// remote server ends by the runtime's ConnectTimeout, the budget of its
// health ping, or by the caller's own deadline when that is sooner, so a
// server that answers the handshake and the ping and then never answers its
// tool listing cannot hold the health loop or strazad's boot. The listing
// that ran out of time answers a sentence that names the server and the
// wait, and its session is evicted. A server that answers its listing still
// lists its tools.
func TestRemoteRuntimeToolsEndsByTheDeadline(t *testing.T) {
	t.Parallel()
	listing := func(wait string) *regexp.Regexp {
		return regexp.MustCompile(`^the MCP server up did not answer its tool listing within ` + wait + `, so Straza closed its session and lists its tools again ` +
			`at the next health check\. An administrator checks that the server runs and answers at the address in its manifest$`)
	}
	cases := []struct {
		name string
		// upstream starts the row's server and answers its address and the
		// fault to set after the handshake and the ping answered.
		upstream func(t *testing.T) (string, func())
		// caller is the caller's own deadline. At 20 s it lies far beyond the
		// bound, so only the listing's own deadline can answer in time.
		caller  time.Duration
		within  time.Duration
		wantErr *regexp.Regexp // nil means the listing answers the tool echo
	}{
		{"the server answers its tool listing", func(t *testing.T) (string, func()) {
			return newMethodUpstream(t).URL, func() {}
		}, 20 * time.Second, time.Second, nil},
		{"the server answers the handshake and the ping and never its tool listing", func(t *testing.T) (string, func()) {
			return newListHoldUpstream(t).URL, func() {}
		}, 20 * time.Second, shortPing + time.Second, listing("300ms")},
		{"the server stops answering after the ping", func(t *testing.T) (string, func()) {
			up := newMethodUpstream(t)
			return up.URL, func() { up.stall(t) }
		}, 20 * time.Second, shortPing + 5*time.Second + time.Second, listing("300ms")},
		{"the caller's own deadline is sooner than the bound", func(t *testing.T) (string, func()) {
			return newListHoldUpstream(t).URL, func() {}
		}, 150 * time.Millisecond, shortPing + time.Second, listing("1[0-5]0ms")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			url, fault := tc.upstream(t)
			r := besideRuntime("up", RemoteSpec{URL: url}, nil)
			r.ConnectTimeout = shortPing
			t.Cleanup(r.Stop)
			if err := r.Ping(context.Background(), nil); err != nil {
				t.Fatalf("the handshake and the ping: %v", err)
			}
			fault()

			ctx, cancel := context.WithTimeout(context.Background(), tc.caller)
			defer cancel()
			var tools []*mcp.Tool
			var err error
			t.Logf("the listing took %v", within(t, tc.within, "the tool listing", func() { tools, err = r.Tools(ctx, nil) }))
			if tc.wantErr == nil {
				if err != nil || len(tools) == 0 {
					t.Fatalf("tools = %d, error = %v, want the listing", len(tools), err)
				}
				return
			}
			if err == nil || !tc.wantErr.MatchString(err.Error()) {
				t.Fatalf("error = %v, want %s", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), "\n") {
				t.Errorf("the error has a line break: %q", err)
			}
			r.mu.Lock()
			_, kept := r.pool[""]
			r.mu.Unlock()
			if kept {
				t.Error("the session whose listing ran out of time is still pooled, want it evicted")
			}
		})
	}
}
