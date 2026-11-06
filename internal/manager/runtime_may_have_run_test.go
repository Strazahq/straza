package manager

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// mayRunUpstream is an in-process streamable-HTTP MCP server whose tool
// write counts its runs in ran. With mode "504" a tools/call reaches the
// server, which runs it, and the answer is HTTP 504, as from a proxy that
// gave up waiting. With mode "break" the call runs and its answer breaks
// after the status line of a 200. With a mode that is a 4xx status, the
// call is answered with that status and never runs.
type mayRunUpstream struct {
	*httptest.Server
	mode atomic.Value
	ran  atomic.Int32
}

func newMayRunUpstream(t *testing.T) *mayRunUpstream {
	t.Helper()
	u := &mayRunUpstream{}
	u.mode.Store("")
	srv := mcp.NewServer(&mcp.Implementation{Name: "may-run", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "write", Description: "write"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			u.ran.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if !bytes.Contains(body, []byte(`"tools/call"`)) || !bytes.Contains(body, []byte(`"write"`)) {
			handler.ServeHTTP(w, r)
			return
		}
		switch u.mode.Load() {
		case "504":
			before := u.ran.Load()
			inner := r.Clone(context.Background())
			inner.Body = io.NopCloser(bytes.NewReader(body))
			go handler.ServeHTTP(httptest.NewRecorder(), inner)
			for deadline := time.Now().Add(2 * time.Second); u.ran.Load() == before && time.Now().Before(deadline); {
				time.Sleep(5 * time.Millisecond)
			}
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
		case "break":
			u.ran.Add(1)
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 500\r\n\r\n{\"jsonrpc\":")
			_ = buf.Flush()
			_ = conn.Close()
		case "400", "401", "403", "429":
			code, _ := strconv.Atoi(u.mode.Load().(string))
			http.Error(w, "refused", code)
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// TestRemoteRuntimeCallThatMayHaveRun pins the error of a call whose POST
// left Straza and got no answer from the server: a proxy's 504 after the
// server ran it, or an answer that breaks after a 200. It wraps
// ErrMayHaveRun and quotes neither the transport's words nor the session
// id. A JSON-RPC error the server sent, and a server no connection reaches,
// keep their own errors, since the call ran to an answer or never left. A
// 4xx answer in place of running the call is a RefusedError with its status.
func TestRemoteRuntimeCallThatMayHaveRun(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name     string
		mode     string
		tool     string
		down     bool
		mayHave  bool
		wantRuns int32
		refused  int
	}{
		{"a proxy answers 504 after the server ran the call", "504", "write", false, true, 1, 0},
		{"the answer breaks after a 200", "break", "write", false, true, 1, 0},
		{"the server answers a JSON-RPC error", "", "nope", false, false, 0, 0},
		{"no connection reaches the server", "", "write", true, false, 0, 0},
		{"the server refuses the credential with 401", "401", "write", false, false, 0, 401},
		{"the server refuses the caller with 403", "403", "write", false, false, 0, 403},
		{"the server refuses the request with 400", "400", "write", false, false, 0, 400},
		{"the server answers 429", "429", "write", false, false, 0, 429},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			up := newMayRunUpstream(t)
			r := besideRuntime("mayrun", RemoteSpec{URL: up.URL, Auth: AuthInject}, nil)
			t.Cleanup(r.Stop)
			if err := r.Ping(ctx, nil); err != nil {
				t.Fatalf("warm ping: %v", err)
			}
			r.mu.Lock()
			sid := r.pool[""].ID()
			r.mu.Unlock()
			up.mode.Store(row.mode)
			if row.down {
				// The runtime's idle connection goes too, so the call has to
				// dial, as when the server stopped before it.
				up.Close()
				r.transport.CloseIdleConnections()
			}
			_, err := r.Call(ctx, CallInput{Tool: row.tool})
			if err == nil {
				t.Fatal("the call answered no error")
			}
			if got := errors.Is(err, ErrMayHaveRun); got != row.mayHave {
				var cause error
				if mhr := (*mayHaveRun)(nil); errors.As(err, &mhr) {
					cause = mhr.cause
				}
				t.Errorf("errors.Is(err, ErrMayHaveRun) = %v, want %v: %v (cause %v)", got, row.mayHave, err, cause)
			}
			if row.mayHave && (strings.Contains(err.Error(), sid) || strings.Contains(err.Error(), "session ID") || strings.Contains(err.Error(), "Gateway Timeout")) {
				t.Errorf("the error quotes the transport or the session id: %v", err)
			}
			var refused *RefusedError
			if errors.As(err, &refused) != (row.refused != 0) || (refused != nil && refused.Status != row.refused) {
				t.Errorf("the error = %v, want a RefusedError with status %d", err, row.refused)
			}
			if got := up.ran.Load(); got != row.wantRuns {
				t.Errorf("the server ran the call %d times, want %d", got, row.wantRuns)
			}
		})
	}
}

// TestRemoteRuntimeConnectRefused pins the error of a call whose session
// the server refuses at the connect handshake, as a server that checks its
// bearer token on every request answers a credential it does not hold: a
// 4xx is a RefusedError with its status, and a 5xx keeps the connect error.
func TestRemoteRuntimeConnectRefused(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "refused", status) }))
			t.Cleanup(up.Close)
			r := besideRuntime("refuser", RemoteSpec{URL: up.URL, Auth: AuthInject}, nil)
			t.Cleanup(r.Stop)
			_, err := r.Call(context.Background(), CallInput{Tool: "write"})
			if err == nil {
				t.Fatal("the call answered no error")
			}
			var refused *RefusedError
			if want := status < 500; errors.As(err, &refused) != want || (want && refused.Status != status) {
				t.Errorf("the call = %v, want a RefusedError with status %d: %v", err, status, want)
			}
		})
	}
}

// TestCommandRuntimeCallThatMayHaveRun pins the error of a call to a
// command server whose process exits before it answers: it wraps
// ErrMayHaveRun, since the process read the call. A JSON-RPC error the
// process sent keeps its own error.
func TestCommandRuntimeCallThatMayHaveRun(t *testing.T) {
	r := startCommandRuntime(t, helperSpec(t, EnvVar{Name: "STRAZA_HELPER_CRASH_TOOL", Value: "1"}))
	ctx := context.Background()
	if _, err := r.Call(ctx, CallInput{Tool: "nope"}); err == nil || errors.Is(err, ErrMayHaveRun) {
		t.Errorf("a call of an unknown tool = %v, want the server's own error", err)
	}
	if _, err := r.Call(ctx, CallInput{Tool: "crash"}); !errors.Is(err, ErrMayHaveRun) {
		t.Errorf("a call whose process exits before answering = %v, want one that wraps ErrMayHaveRun", err)
	}
}

// TestMayHaveRunCauseIsLogged pins the log line of a call that may have run:
// the agent reads the may-have-run sentence, and strazad's log keeps the
// runtime's own error, as shown writes it, with the server and the tool, on
// a remote server and on a command server alike. The credential the call
// carried reaches neither.
func TestMayHaveRunCauseIsLogged(t *testing.T) {
	const secret = "sk-cause-6612-NEVER-LEAK"
	up := newMayRunUpstream(t)
	up.mode.Store("504")
	remote, err := Parse([]byte("apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: proxied}\n" +
		"server: {name: straza.test/proxied, version: \"1.0.0\"}\nstraza:\n  runtime:\n    kind: remote\n    remote: {url: " + up.URL + "}\n" +
		"  credential:\n    kind: static\n    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	command := helperManifest(t, "proxied", nil, EnvVar{Name: "STRAZA_HELPER_CRASH_TOOL", Value: "1"})
	rows := []struct {
		name, tool, cause string
		mf                Manifest
		secret            *Secret
	}{
		{"remote", "write", "Gateway Timeout", remote, &Secret{ID: "cred-1", Value: secret}},
		{"command", "crash", "EOF", command, nil},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			logs := &lockedBuffer{}
			secrets := &fakeSecrets{byApp: map[string]*Secret{}}
			mgr := New(Options{Store: testStore(t), Secrets: secrets, HealthInterval: time.Hour, AllowLoopbackUpstreams: true,
				Log: slog.New(slog.NewTextHandler(logs, nil))})
			t.Cleanup(mgr.stopAll)
			ctx := context.Background()
			app, err := mgr.Install(ctx, row.mf, store.AppSourceAPI)
			if err != nil {
				t.Fatal(err)
			}
			if row.secret != nil {
				secrets.set(app.ID, row.secret)
				mgr.SecretUpdated(ctx, app.ID)
			}
			waitStatus(t, mgr, "proxied", StatusRunning)
			_, err = mgr.Call(ctx, "proxied", row.tool, nil, row.secret, nil)
			if !errors.Is(err, ErrMayHaveRun) || strings.Contains(err.Error(), row.cause) {
				t.Fatalf("the call = %v, want the may-have-run sentence without the cause", err)
			}
			out := logs.String()
			if !strings.Contains(out, "manager: a call may have run, and no answer Straza could read came back") ||
				!strings.Contains(out, "app=proxied") || !strings.Contains(out, "tool="+row.tool) || !strings.Contains(out, row.cause) {
				t.Errorf("the log does not keep the cause %q with the server and the tool:\n%s", row.cause, out)
			}
			if strings.Contains(out, secret) {
				t.Errorf("the log holds the credential:\n%s", out)
			}
		})
	}
}

// traceBase is a RoundTripper that reports the write of a request to the
// request's trace as wrote says, then answers status, or err when set.
type traceBase struct {
	wrote  string // "" for no report, "ok" or "failed"
	status int
	err    error
}

func (b traceBase) RoundTrip(req *http.Request) (*http.Response, error) {
	if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.WroteRequest != nil && b.wrote != "" {
		info := httptrace.WroteRequestInfo{}
		if b.wrote == "failed" {
			info.Err = errors.New("broken pipe")
		}
		trace.WroteRequest(info)
	}
	if b.err != nil {
		return nil, b.err
	}
	return &http.Response{StatusCode: b.status, Body: http.NoBody}, nil
}

// TestCallTrace pins what markTrace records of a call's POST and what the
// trace concludes: a POST counts as sent only once it was written whole, a
// 4xx answer but 408 is a refusal in place of running the call, and a
// request written to a connection the server had closed as idle was never
// read. Anything else written may have run.
func TestCallTrace(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name    string
		base    traceBase
		ran     bool
		refused int
	}{
		{"written and answered 200", traceBase{wrote: "ok", status: 200}, true, 0},
		{"written and answered 401", traceBase{wrote: "ok", status: 401}, false, 401},
		{"written and answered 404", traceBase{wrote: "ok", status: 404}, false, 404},
		{"written and answered 408", traceBase{wrote: "ok", status: 408}, true, 0},
		{"written and answered 502", traceBase{wrote: "ok", status: 502}, true, 0},
		{"written, then the connection ended", traceBase{wrote: "ok", err: io.EOF}, true, 0},
		{"written to a connection the server closed as idle", traceBase{wrote: "ok", err: errors.New(serverClosedIdle)}, false, 0},
		{"the write failed", traceBase{wrote: "failed", err: errors.New("broken pipe")}, false, 0},
		{"never written, as after a failed dial", traceBase{err: errors.New("dial tcp: connection refused")}, false, 0},
	}
	for _, row := range rows {
		var tr callTrace
		req := httptest.NewRequest(http.MethodPost, "http://up.test/mcp", nil).WithContext(context.WithValue(context.Background(), traceKey{}, &tr))
		_, _ = markTrace{row.base}.RoundTrip(req)
		if tr.ran() != row.ran || tr.refusedWith() != row.refused {
			t.Errorf("%s: ran %v, refused with %d, want %v and %d", row.name, tr.ran(), tr.refusedWith(), row.ran, row.refused)
		}
	}
}
