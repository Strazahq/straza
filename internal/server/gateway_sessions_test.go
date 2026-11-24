package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sessionUpstream is startSessionUpstream's server and the number of tool
// calls it answered.
type sessionUpstream struct {
	*gatewayUpstream
	calls atomic.Int32
}

// startSessionUpstream serves one tool, session, that answers the id of the
// MCP session the call arrived on, so a test sees which calls shared one.
func startSessionUpstream(t *testing.T) *sessionUpstream {
	t.Helper()
	u := &sessionUpstream{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "session-upstream", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "session", Description: "session"},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			u.calls.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: req.Session.ID()}}}, nil, nil
		})
	u.gatewayUpstream = &gatewayUpstream{Server: httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))}
	t.Cleanup(u.Close)
	return u
}

// TestGatewayGivesEachSessionItsOwnUpstreamSession drives the gateway with
// three Straza sessions on a server with no credential: each session's
// calls arrive on one MCP session of its own, whether the other session is
// another person's or a second one of the same person, so the session id of
// the verified token is what keys the upstream session.
func TestGatewayGivesEachSessionItsOwnUpstreamSession(t *testing.T) {
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	seedGatewayUser(t, app, "lee", "dev")
	installEchoApp(t, app, startSessionUpstream(t).gatewayUpstream, "sesapp", 0)
	upstreamOf := func(tok string) string {
		t.Helper()
		_, body, raw := mcpCall(t, base, tok, "tools/call", map[string]any{"name": "sesapp__session", "arguments": map[string]any{}})
		res, _ := body["result"].(map[string]any)
		content, _ := res["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("the call answered %s, want a tool result", raw)
		}
		first, _ := content[0].(map[string]any)
		id, _ := first["text"].(string)
		if id == "" {
			t.Fatalf("the call answered %s, want the id of its MCP session", raw)
		}
		return id
	}
	kim1, _ := gatewaySession(t, base, "kim")
	kim2, _ := gatewaySession(t, base, "kim")
	lee, _ := gatewaySession(t, base, "lee")

	first := upstreamOf(kim1)
	if again := upstreamOf(kim1); again != first {
		t.Errorf("two calls of one session arrived on the MCP sessions %s and %s, want one", first, again)
	}
	if other := upstreamOf(kim2); other == first {
		t.Errorf("a second session of the same person shares the MCP session %s of the first", first)
	}
	if other := upstreamOf(lee); other == first {
		t.Errorf("another person's session shares the MCP session %s", first)
	}
}

// mcpConnRequest sends one request with the HTTP method verb to the gateway
// endpoint url, with the label conn in its Mcp-Session-Id header, or no such
// header when conn is empty. A POST carries the JSON-RPC message of method
// and params. It answers the status, the answer's header and its body.
func mcpConnRequest(t *testing.T, verb, url, token, conn, method string, params any) (int, http.Header, []byte) {
	t.Helper()
	var body io.Reader
	if verb == http.MethodPost {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(verb, url, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if conn != "" {
		req.Header.Set(mcpSessionHeader, conn)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

// TestGatewayInitializeGivesAConnectionLabel pins that initialize, and only
// initialize, answers a fresh label in the Mcp-Session-Id header, which the
// gateway accepts back, so an MCP client holds one label per connection.
func TestGatewayInitializeGivesAConnectionLabel(t *testing.T) {
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	tok, _ := gatewaySession(t, base, "kim")
	wellFormed := regexp.MustCompile(`^[A-Za-z0-9_-]{22,64}$`)
	init := map[string]any{"protocolVersion": "2025-06-18"}
	seen := map[string]bool{}
	for _, tc := range []struct {
		method    string
		params    any
		wantLabel bool
	}{
		{method: "initialize", params: init, wantLabel: true},
		{method: "initialize", params: init, wantLabel: true},
		{method: "ping"},
		{method: "tools/list"},
	} {
		status, header, raw := mcpConnRequest(t, http.MethodPost, base+"/mcp", tok, "", tc.method, tc.params)
		label := header.Get(mcpSessionHeader)
		switch {
		case status != http.StatusOK:
			t.Fatalf("%s = %d %s, want 200", tc.method, status, raw)
		case !tc.wantLabel && label != "":
			t.Errorf("%s answered the label %q, want none, since a client takes a second label as a broken session", tc.method, label)
		case tc.wantLabel && !wellFormed.MatchString(label):
			t.Errorf("%s answered the label %q, want 22 to 64 letters, digits, '-' or '_'", tc.method, label)
		case tc.wantLabel && seen[label]:
			t.Errorf("two initializes answered the same label %q", label)
		}
		if !tc.wantLabel {
			continue
		}
		seen[label] = true
		if status, _, raw := mcpConnRequest(t, http.MethodPost, base+"/mcp", tok, label, "ping", nil); status != http.StatusOK {
			t.Errorf("a ping with the label %q = %d %s, want 200", label, status, raw)
		}
	}
}

// TestGatewayGivesEachConnectionItsOwnUpstreamSession is the case of two
// agent windows of one person: two MCP clients of the bridge's kind under
// one session token each hold the label their initialize got, and each
// reaches an upstream session of its own. Requests with no label share
// one more upstream session of the Straza session.
func TestGatewayGivesEachConnectionItsOwnUpstreamSession(t *testing.T) {
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	installEchoApp(t, app, startSessionUpstream(t).gatewayUpstream, "sesapp", 0)
	tok, _ := gatewaySession(t, base, "kim")
	ctx := context.Background()
	connect := func() *mcp.ClientSession {
		t.Helper()
		s, err := mcp.NewClient(&mcp.Implementation{Name: "conn-test", Version: "1.0.0"}, nil).Connect(ctx,
			&mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{tok}}, MaxRetries: -1}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	viaClient := func(s *mcp.ClientSession) func() string {
		return func() string {
			t.Helper()
			res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "sesapp__session", Arguments: map[string]any{}})
			if err != nil || len(res.Content) == 0 {
				t.Fatalf("the call on the connection %s = %v, %v, want a tool result", s.ID(), res, err)
			}
			return res.Content[0].(*mcp.TextContent).Text
		}
	}
	noLabel := func() string {
		t.Helper()
		_, body, raw := mcpCall(t, base, tok, "tools/call", map[string]any{"name": "sesapp__session", "arguments": map[string]any{}})
		res, _ := body["result"].(map[string]any)
		content, _ := res["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("the call with no label answered %s, want a tool result", raw)
		}
		id, _ := content[0].(map[string]any)["text"].(string)
		return id
	}
	one, two := viaClient(connect()), viaClient(connect())
	for _, tc := range []struct {
		name     string
		a, b     func() string
		wantSame bool
	}{
		{name: "two calls of one connection", a: one, b: one, wantSame: true},
		{name: "two connections of one session", a: one, b: two},
		{name: "a connection and a call with no label", a: two, b: noLabel},
		{name: "two calls with no label", a: noLabel, b: noLabel, wantSame: true},
	} {
		if a, b := tc.a(), tc.b(); (a == b) != tc.wantSame {
			t.Errorf("%s: the calls arrived on the MCP sessions %s and %s, want the same one: %v", tc.name, a, b, tc.wantSame)
		}
	}
}

// TestGatewayRefusesAMalformedConnectionLabel pins that a request whose
// Mcp-Session-Id is not 1 to 64 letters, digits, '-' or '_' gets HTTP 400
// and the sentence on every method, and that no call of it reaches the
// server. A label of 64 such characters is taken, whoever gave it out.
func TestGatewayRefusesAMalformedConnectionLabel(t *testing.T) {
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	up := startSessionUpstream(t)
	installEchoApp(t, app, up.gatewayUpstream, "sesapp", 0)
	tok, _ := gatewaySession(t, base, "kim")
	call := map[string]any{"name": "sesapp__session", "arguments": map[string]any{}}
	for _, tc := range []struct {
		name, verb, method, conn string
		wantOK                   bool
	}{
		{name: "a label of 65 characters", verb: http.MethodPost, method: "tools/call", conn: strings.Repeat("a", 65)},
		{name: "a label with a dot", verb: http.MethodPost, method: "tools/call", conn: "conn.1"},
		{name: "a label with a space", verb: http.MethodPost, method: "tools/call", conn: "conn 1"},
		{name: "a label on initialize", verb: http.MethodPost, method: "initialize", conn: "conn+1"},
		{name: "a label on the stream", verb: http.MethodGet, conn: "conn/1"},
		{name: "a label on the session's end", verb: http.MethodDelete, conn: "conn:1"},
		{name: "a label of 64 characters", verb: http.MethodPost, method: "tools/call", conn: strings.Repeat("a-_9", 16), wantOK: true},
	} {
		before := up.calls.Load()
		status, _, raw := mcpConnRequest(t, tc.verb, base+"/mcp", tok, tc.conn, tc.method, call)
		if tc.wantOK {
			if status != http.StatusOK || up.calls.Load() != before+1 {
				t.Errorf("%s = %d %s, want the call answered", tc.name, status, raw)
			}
			continue
		}
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &body)
		if status != http.StatusBadRequest || body.Error != badConnLabelMsg {
			t.Errorf("%s = %d %s, want 400 and %q", tc.name, status, raw, badConnLabelMsg)
		}
		if n := up.calls.Load() - before; n != 0 {
			t.Errorf("%s reached the server with %d calls, want none", tc.name, n)
		}
	}
}
