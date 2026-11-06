package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
)

// bodyLimit is the server.mcpMaxBodyBytes the body limit tests run under,
// small so a body over it is quick to send.
const bodyLimit = 64 << 10

// bearerTransport sends every request with the session token, as the
// straza client's bridge does.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// TestGatewayBodyLimitThroughSDK pins the over-limit answer as a go-sdk
// client meets it, the client the straza client's bridge uses: a
// tools/call over server.mcpMaxBodyBytes fails with the sentence that
// names the setting, and a second call on the same client session works.
// strazad logs one Warn line with the session, and only the second call
// leaves a record, because the first was never read.
func TestGatewayBodyLimitThroughSDK(t *testing.T) {
	t.Parallel()
	log, logs := captureLogger()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }},
		func(c *config.Config) { c.Server.MCPMaxBodyBytes = bodyLimit })
	seedGatewayUser(t, app, "kim", "dev")
	installEchoApp(t, app, startGatewayUpstream(t), "bodyapp", 0)
	catActivate(t, app, "body-policy", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: body-policy}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-echo
      tools: [mcp.call]
      apps: [bodyapp]
      toolNames: {allow: ["echo"]}
      effect: allow
`)
	tok, sid := gatewaySession(t, base, "kim")
	ctx := context.Background()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "body-limit-test", Version: "1.0.0"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{tok}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	_, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "bodyapp__echo", Arguments: map[string]any{"text": strings.Repeat("x", bodyLimit)}})
	if want := fmt.Sprintf(largeBodyMsg, bodyLimit); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("over-limit call error = %v, want %q", err, want)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "bodyapp__echo", Arguments: map[string]any{"text": "after"}})
	if err != nil {
		t.Fatalf("the second call on the same session failed: %v", err)
	}
	if text, _ := res.Content[0].(*mcp.TextContent); text == nil || text.Text != "echo: after" {
		t.Fatalf("second call = %+v, want echo: after", res.Content)
	}
	rec := awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["app"] == "bodyapp" && d["event"] == "tool.pre" })[0]
	if rec["effect"] != "allow" || rec["arguments"] != `{"text":"after"}` {
		t.Errorf("record = %v, want the second call's allow", rec)
	}
	var warns []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "refused an MCP request") {
			warns = append(warns, line)
		}
	}
	want := fmt.Sprintf("strazad refused an MCP request whose body is over server.mcpMaxBodyBytes (%d bytes). Raise the setting, up to 8388608, if agents need larger calls.", bodyLimit)
	if len(warns) != 1 || !strings.Contains(warns[0], want) || !strings.Contains(warns[0], "session="+sid) {
		t.Errorf("warn lines = %q, want one %q with session=%s", warns, want, sid)
	}
}

// postRaw posts body as it stands to the gateway endpoint url and returns
// the status and the decoded answer.
func postRaw(t *testing.T, url, token, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("answer %q is not JSON: %v", raw, err)
	}
	return resp.StatusCode, decoded
}

// TestGatewayBodyLimitAnswers pins the answers around server.mcpMaxBodyBytes
// on /mcp and on a server endpoint: a body of exactly the limit is read, and
// one byte more is refused with -32600 and the sentence, on HTTP 200 with
// the request's id when the id comes before params, and on HTTP 413 with id
// null when it does not or the request has none.
func TestGatewayBodyLimitAnswers(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Server.MCPMaxBodyBytes = bodyLimit })
	seedGatewayUser(t, app, "kim", "dev")
	tok, _ := gatewaySession(t, base, "kim")
	// sized pads the middle of a request so the body is exactly size bytes.
	sized := func(head, tail string, size int) string {
		return head + strings.Repeat("x", size-len(head)-len(tail)) + tail
	}
	const ping = `{"jsonrpc":"2.0","id":3,"method":"ping","params":{"pad":"`
	rows := []struct {
		name   string
		path   string
		body   string
		status int
		id     any
	}{
		{"a body of exactly the limit is read", "/mcp", sized(ping, `"}}`, bodyLimit), http.StatusOK, 3.0},
		{"one byte over with the id first answers 200 with the id", "/mcp", sized(ping, `"}}`, bodyLimit+1), http.StatusOK, 3.0},
		{"a string id is kept", "/mcp", sized(`{"id":"call-9","jsonrpc":"2.0","method":"ping","params":{"pad":"`, `"}}`, bodyLimit+1), http.StatusOK, "call-9"},
		{"the id after params answers 413", "/mcp", sized(`{"jsonrpc":"2.0","method":"ping","params":{"pad":"`, `"},"id":3}`, bodyLimit+1), http.StatusRequestEntityTooLarge, nil},
		{"a notification answers 413", "/mcp", sized(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{"pad":"`, `"}}`, bodyLimit+1), http.StatusRequestEntityTooLarge, nil},
		{"a server endpoint answers the same", "/mcp/nope", sized(ping, `"}}`, bodyLimit+1), http.StatusOK, 3.0},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			status, res := postRaw(t, base+row.path, tok, row.body)
			if status != row.status || res["id"] != row.id {
				t.Fatalf("answer = %d %v, want %d with id %v", status, res, row.status, row.id)
			}
			if len(row.body) == bodyLimit {
				if res["result"] == nil {
					t.Errorf("answer = %v, want the ping's result", res)
				}
				return
			}
			errObj, _ := res["error"].(map[string]any)
			if want := fmt.Sprintf(largeBodyMsg, bodyLimit); errObj == nil || errObj["code"] != -32600.0 || errObj["message"] != want {
				t.Errorf("error = %v, want -32600 %q", errObj, want)
			}
		})
	}
}

// TestBufferedRequestID pins how the id of a request over the limit is read
// from the part of its body that was read: the top-level keys in order up
// to params, a string or a number only, and nothing the limit may have cut.
func TestBufferedRequestID(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name, prefix, want string
	}{
		{"a number before params", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x","arguments":{"text":"xx`, `7`},
		{"a string first", `{"id":"a-1","params":{"x`, `"a-1"`},
		{"a key in another case", `{"ID":-2,"params":{`, `-2`},
		{"the id after params", `{"jsonrpc":"2.0","method":"m","params":{"x":1},"id":7`, ``},
		{"a null id", `{"id":null,"params":{`, ``},
		{"an object id", `{"id":{"x":1},"params":{`, ``},
		{"an id the limit may have cut", `{"jsonrpc":"2.0","id":12`, ``},
		{"a batch", `[{"jsonrpc":"2.0","id":1`, ``},
		{"no id at all", `{"jsonrpc":"2.0","method":"notifications/initialized","params":{"x`, ``},
		{"a key the limit cut", `{"jsonrpc":"2.0","met`, ``},
	}
	for _, row := range rows {
		if got := string(bufferedRequestID(strings.NewReader(row.prefix))); got != row.want {
			t.Errorf("%s: id = %q, want %q", row.name, got, row.want)
		}
	}
}

// stalledBody is a request body that yields prefix and then fails with err,
// as net/http's body fails when its read deadline passes mid-body.
type stalledBody struct {
	prefix *strings.Reader
	err    error
}

func (b *stalledBody) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	return 0, b.err
}

func (b *stalledBody) Close() error { return nil }

// timeoutError is a net.Error that reports a timeout without wrapping
// os.ErrDeadlineExceeded.
type timeoutError struct{}

func (timeoutError) Error() string   { return "stream read deadline" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// TestGatewaySlowBodyAnswers pins the answer to an MCP request whose body
// stopped arriving until its read deadline passed: a JSON-RPC error -32600
// that says the body arrived too slowly and was cut, and what to do next,
// with the request's id and HTTP 200 when the part that arrived names one,
// and id null and HTTP 408 when it does not, and one Warn line with the
// session. A body that is not JSON keeps the parse error and no line.
func TestGatewaySlowBodyAnswers(t *testing.T) {
	t.Parallel()
	const slow = "Straza: this MCP request was not read, because its body arrived too slowly and Straza cut it off. " +
		"Check the network between this machine and Straza, or send less in one call, then try the call once more. If it fails again, tell the user."
	deadline := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	rows := []struct {
		name, prefix string
		err          error
		status       int
		id           any
		code         float64
		msg          string
		logged       bool
	}{
		{"stalled after the id", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x","arguments":{"text":"`, deadline,
			http.StatusOK, float64(7), -32600, slow, true},
		{"stalled before an id", `{"jsonrpc":"2.0",`, deadline, http.StatusRequestTimeout, nil, -32600, slow, true},
		{"a net.Error that times out", `{"jsonrpc":"2.0","id":"a",`, timeoutError{}, http.StatusOK, "a", -32600, slow, true},
		{"a body that is not JSON", `not json`, io.EOF, http.StatusOK, nil, -32700,
			"parse error: single JSON-RPC message expected (batching is not supported)", false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			log, logs := captureLogger()
			app, _ := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }})
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			r.Body = &stalledBody{prefix: strings.NewReader(row.prefix), err: row.err}
			w := httptest.NewRecorder()
			app.handleMCPPost(w, r, authn.Claims{Session: "sess-slow"}, policy.Subject{})
			var answer map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
				t.Fatalf("answer %q: %v", w.Body.String(), err)
			}
			errObj, _ := answer["error"].(map[string]any)
			if w.Code != row.status || answer["id"] != row.id || errObj["code"] != row.code || errObj["message"] != row.msg {
				t.Errorf("answer = %d %s, want %d id %v code %v %q", w.Code, w.Body.String(), row.status, row.id, row.code, row.msg)
			}
			n := strings.Count(logs.String(), "strazad cut an MCP request whose body arrived too slowly")
			if want := map[bool]int{true: 1, false: 0}[row.logged]; n != want || (row.logged && !strings.Contains(logs.String(), "sess-slow")) {
				t.Errorf("%d Warn lines for the slow body, want %d with the session:\n%s", n, want, logs.String())
			}
		})
	}
}
