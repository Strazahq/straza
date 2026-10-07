package e2ematrix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// daemonStep starts or stops the identity's straza daemon.
func (s *scenarioRun) daemonStep(st *step) (attempt, error) {
	c := s.clients[st.str("as")]
	if st.str("action") == "start" {
		return attempt{ok: true}, c.startDaemon()
	}
	return attempt{ok: true}, c.stopDaemon()
}

// serverStep stops or restarts strazad on the same port and data directory.
// After a restart the admin signs in again so the steps keep a live token.
func (s *scenarioRun) serverStep(ctx context.Context, st *step) (attempt, error) {
	if st.str("action") == "stop" {
		s.stack.server.stop()
		return attempt{ok: true}, nil
	}
	if err := s.stack.server.start(ctx); err != nil {
		return attempt{}, err
	}
	s.r.timer("cold-start", "-", s.stack.server.coldStart)
	return attempt{ok: true}, s.stack.server.login(ctx)
}

// mcpStep sends one tools/list or tools/call through the gateway with the
// session the identity's last session.start opened.
func (s *scenarioRun) mcpStep(ctx context.Context, st *step, args, want map[string]any) (attempt, error) {
	token, err := s.clients[st.str("as")].sessionToken()
	if err != nil {
		return attempt{}, err
	}
	msg := map[string]any{"jsonrpc": "2.0", "id": 1}
	if _, list := args["list"]; list {
		msg["method"] = "tools/list"
	} else {
		params := map[string]any{"name": args["tool"]}
		if a, ok := args["args"].(map[string]any); ok {
			params["arguments"] = a
		} else {
			params["arguments"] = map[string]any{}
		}
		msg["method"], msg["params"] = "tools/call", params
	}
	raw, _ := json.Marshal(msg)
	code, body, err := httpCall(ctx, http.MethodPost, s.stack.server.URL+"/mcp", token, "application/json", raw)
	if err != nil {
		return attempt{}, err
	}
	var decoded any
	_ = json.Unmarshal(body, &decoded)
	g := gatewayAnswer(code, body, decoded)
	a := attempt{ok: true, body: decoded}
	fail := func(format string, v ...any) attempt {
		return attempt{body: decoded, detail: fmt.Sprintf(format, v...)}
	}
	if refused, _ := want["refused"].(bool); refused {
		if !g.refused {
			return fail("want a refused call, the gateway answered %q", trim(g.text)), nil
		}
	} else if g.refused {
		return fail("gateway refused the call: %s", trim(g.text)), nil
	}
	if w, ok := want["reasonContains"].(string); ok && !strings.Contains(strings.ToLower(g.text), strings.ToLower(w)) {
		return fail("gateway answer %q does not mention %q", trim(g.text), w), nil
	}
	if w, ok := want["result"].(string); ok && !strings.Contains(g.text, w) {
		return fail("tool result %q does not contain %q", trim(g.text), w), nil
	}
	for _, name := range wantList(want, "tools") {
		if !contains(g.tools, name) {
			return fail("tools/list lacks %q (has %s)", name, strings.Join(g.tools, ", ")), nil
		}
	}
	for _, name := range wantList(want, "notTools") {
		if contains(g.tools, name) {
			return fail("tools/list still carries %q", name), nil
		}
	}
	return a, nil
}

// gatewayReply is what the runner reads out of one gateway answer: whether
// the call was refused (an HTTP error, a JSON-RPC error or a tool error), the
// text a model would read, and the tool names of a list.
type gatewayReply struct {
	refused bool
	text    string
	tools   []string
}

func gatewayAnswer(code int, body []byte, decoded any) gatewayReply {
	g := gatewayReply{}
	if code != http.StatusOK {
		g.refused = true
		g.text = fmt.Sprintf("HTTP %d: %s", code, redact(string(body)))
		return g
	}
	m, _ := decoded.(map[string]any)
	if e, ok := m["error"].(map[string]any); ok {
		g.refused = true
		g.text, _ = e["message"].(string)
		return g
	}
	result, _ := m["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		g.refused = true
	}
	var parts []string
	if content, ok := result["content"].([]any); ok {
		for _, c := range content {
			if cm, ok := c.(map[string]any); ok {
				if t, ok := cm["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
	}
	g.text = strings.Join(parts, "\n")
	if tools, ok := result["tools"].([]any); ok {
		for _, t := range tools {
			if tm, ok := t.(map[string]any); ok {
				if name, ok := tm["name"].(string); ok {
					g.tools = append(g.tools, name)
				}
			}
		}
	}
	return g
}

// wantList reads a list of strings from want.
func wantList(want map[string]any, key string) []string {
	var out []string
	if list, ok := want[key].([]any); ok {
		for _, v := range list {
			out = append(out, fmt.Sprint(v))
		}
	}
	return out
}

// auditEvent is the part of an audit CloudEvent the audit step matches on.
type auditEvent struct {
	Type string `json:"type"`
	Data struct {
		User   string `json:"user"`
		Event  string `json:"event"`
		Action string `json:"action"`
		Effect string `json:"effect"`
		Reason string `json:"reason"`
	} `json:"data"`
}

// auditStep counts the chain rows since the scenario started that match
// subject, kind, action, decision and reasonContains, and compares with
// count (at least one row when count is absent).
func (s *scenarioRun) auditStep(ctx context.Context, args map[string]any) (attempt, error) {
	rows, err := s.stack.server.auditSince(ctx, s.auditSeq)
	if err != nil {
		return attempt{}, err
	}
	subject, _ := args["subject"].(string)
	kind, _ := args["kind"].(string)
	action, _ := args["action"].(string)
	decision, _ := args["decision"].(string)
	reason, _ := args["reasonContains"].(string)
	matched := 0
	var seen []string
	for _, row := range rows {
		var ce auditEvent
		if json.Unmarshal([]byte(row.CE), &ce) != nil {
			continue
		}
		seen = append(seen, fmt.Sprintf("%s %s %s %s %q", strings.TrimPrefix(ce.Type, "straza.audit."), ce.Data.Event, ce.Data.Action, ce.Data.Effect, trim(ce.Data.Reason)))
		if subject != "" && row.Username != subject && ce.Data.User != subject {
			continue
		}
		if kind != "" && ce.Data.Event != kind && ce.Type != kind {
			continue
		}
		if action != "" && ce.Data.Action != action {
			continue
		}
		if decision != "" && ce.Data.Effect != decision {
			continue
		}
		if reason != "" && !strings.Contains(strings.ToLower(ce.Data.Reason), strings.ToLower(reason)) {
			continue
		}
		matched++
	}
	want, exact := wantInt(args, "count")
	if (exact && matched == want) || (!exact && matched > 0) {
		return attempt{ok: true}, nil
	}
	wantText := "at least 1"
	if exact {
		wantText = fmt.Sprint(want)
	}
	return attempt{detail: fmt.Sprintf("audit rows matching subject=%q kind=%q action=%q decision=%q reason~%q: %d, want %s; the %d rows since the run started: %s",
		subject, kind, action, decision, reason, matched, wantText, len(rows), strings.Join(seen, "; "))}, nil
}
