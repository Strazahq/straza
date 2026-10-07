package e2ematrix

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// upstream is the hermetic MCP server the mcp steps and the app manifests
// point at through ${upstream}. It speaks the streamable HTTP shape the
// gateway's remote runtime dials: one JSON-RPC message per POST answered as
// application/json, 202 for notifications, 405 for the standalone GET
// stream. Three tools: echo returns its text argument, get-sum returns the
// sum of its numeric a and b as a decimal string, and get-env takes no
// argument and returns the fixed text E2E_UPSTREAM=1.
type upstream struct {
	URL  string
	srv  *http.Server
	ln   net.Listener
	done chan struct{}
}

// startUpstream listens on a free loopback port and serves /mcp.
func startUpstream() (*upstream, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	u := &upstream{ln: ln, done: make(chan struct{})}
	u.URL = "http://" + ln.Addr().String() + "/mcp"
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", u.handle)
	u.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		_ = u.srv.Serve(ln)
		close(u.done)
	}()
	return u, nil
}

// stop closes the listener and waits for the server goroutine.
func (u *upstream) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = u.srv.Shutdown(ctx)
	<-u.done
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (u *upstream) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		http.Error(w, "no standalone stream", http.StatusMethodNotAllowed)
		return
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	case http.MethodPost:
	default:
		http.Error(w, "POST, GET or DELETE", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "one JSON-RPC message expected", http.StatusBadRequest)
		return
	}
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var out any
	switch msg.Method {
	case "initialize":
		out = rpcResult(msg.ID, map[string]any{
			"protocolVersion": negotiate(msg.Params),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "e2e-upstream", "version": "1.0.0"},
		})
	case "ping":
		out = rpcResult(msg.ID, map[string]any{})
	case "tools/list":
		out = rpcResult(msg.ID, map[string]any{"tools": upstreamTools()})
	case "tools/call":
		out = u.call(msg)
	default:
		out = rpcErr(msg.ID, -32601, fmt.Sprintf("method %q is not served", msg.Method))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func negotiate(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
		return p.ProtocolVersion
	}
	return "2025-06-18"
}

func upstreamTools() []any {
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "number"}
	return []any{
		map[string]any{"name": "echo", "description": "Return the text argument.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": str}, "required": []string{"text"}}},
		map[string]any{"name": "get-sum", "description": "Return the sum of a and b as a decimal string.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"a": num, "b": num}, "required": []string{"a", "b"}}},
		map[string]any{"name": "get-env", "description": "Return the fixed text E2E_UPSTREAM=1.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
	}
}

func (u *upstream) call(msg rpcMessage) any {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return rpcErr(msg.ID, -32602, "params must carry name and arguments")
	}
	var text string
	switch p.Name {
	case "echo":
		text, _ = p.Arguments["text"].(string)
	case "get-sum":
		a, okA := p.Arguments["a"].(float64)
		b, okB := p.Arguments["b"].(float64)
		if !okA || !okB {
			return rpcErr(msg.ID, -32602, "get-sum needs numeric a and b")
		}
		text = strconv.FormatFloat(a+b, 'f', -1, 64)
	case "get-env":
		text = "E2E_UPSTREAM=1"
	default:
		return rpcErr(msg.ID, -32602, fmt.Sprintf("unknown tool %q", p.Name))
	}
	return rpcResult(msg.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	})
}

func rpcResult(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func rpcErr(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}
