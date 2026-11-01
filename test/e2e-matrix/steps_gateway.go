package e2ematrix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

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

// mcpStep sends one tools/list or tools/call through the identity's service
// to the gateway, in the harness window the identity's session.start steps
// opened, so the call rides that window's session.
func (s *scenarioRun) mcpStep(ctx context.Context, st *step, args, want map[string]any) (attempt, error) {
	as := st.str("as")
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
	code, body, err := s.clients[as].mcp(ctx, s.harness, s.window(as), raw)
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
// Object is the object member of a straza.audit.mcp record, events
// revision 44, nil on a record without one. SignIn is the lane of a
// device.enrolled record, events revision 47, empty on any other record.
type auditEvent struct {
	Type string `json:"type"`
	Data struct {
		User   string         `json:"user"`
		Event  string         `json:"event"`
		Action string         `json:"action"`
		Effect string         `json:"effect"`
		Reason string         `json:"reason"`
		Object map[string]any `json:"object"`
		SignIn string         `json:"signIn"`
	} `json:"data"`
}

// auditStep counts the chain rows since the scenario started that match
// subject, kind, action, decision, reasonContains, object and signIn, and
// compares with count (at least one row when count is absent).
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
	object, _ := args["object"].(map[string]any)
	signIn, _ := args["signIn"].(string)
	matched := 0
	var seen []string
	for _, row := range rows {
		var ce auditEvent
		if json.Unmarshal([]byte(row.CE), &ce) != nil {
			continue
		}
		words := objectWords(ce.Data.Object)
		if ce.Data.SignIn != "" {
			words += " signIn=" + ce.Data.SignIn
		}
		seen = append(seen, fmt.Sprintf("%s %s %s %s %q%s", strings.TrimPrefix(ce.Type, "straza.audit."), ce.Data.Event, ce.Data.Action, ce.Data.Effect, trim(ce.Data.Reason), words))
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
		if !objectMatches(object, ce.Data.Object) {
			continue
		}
		if signIn != "" && ce.Data.SignIn != signIn {
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
	return attempt{detail: fmt.Sprintf("audit rows matching subject=%q kind=%q action=%q decision=%q reason~%q object=%v signIn=%q: %d, want %s; the %d rows since the run started: %s",
		subject, kind, action, decision, reason, object, signIn, matched, wantText, len(rows), strings.Join(seen, "; "))}, nil
}

// objectMatches reports whether a record's object member carries every
// field the step's object matcher names with the same value. A step
// without the matcher matches every record, and a record without an object
// matches no matcher, so a matcher that names a wrong object finds nothing.
func objectMatches(want, got map[string]any) bool {
	if len(want) > 0 && got == nil {
		return false
	}
	for k, v := range want {
		g, ok := got[k]
		if !ok || fmt.Sprint(g) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// objectWords renders a record's object member for a failure line: the
// type, reach, value and id it carries, or nothing.
func objectWords(o map[string]any) string {
	if o == nil {
		return ""
	}
	out := " object"
	for _, k := range auditObjectKeys {
		if v, ok := o[k]; ok {
			out += fmt.Sprintf(" %s=%v", k, v)
		}
	}
	return out
}

// auditKeys are the fields an audit step may carry.
var auditKeys = []string{"subject", "kind", "action", "decision", "reasonContains", "object", "signIn", "count"}

// auditObjectKeys are the fields of a straza.audit.mcp record's object
// member, events revision 44, that an audit step's object matcher may name.
var auditObjectKeys = []string{"type", "reach", "value", "id"}

// validateAudit checks an audit step: at least one of auditKeys and no
// other key, because a misspelled matcher would be ignored, and the
// object matcher a map of at least one of auditObjectKeys, each a non-empty
// string, because the record carries every one of them as a string, ids
// included.
func validateAudit(args map[string]any) error {
	if len(args) == 0 {
		return fmt.Errorf("audit needs at least one of %s", strings.Join(auditKeys, ", "))
	}
	for k := range args {
		if !contains(auditKeys, k) {
			return fmt.Errorf("audit.%s is not one of %s. Check its spelling, because the step would ignore it and count rows it never meant", k, strings.Join(auditKeys, ", "))
		}
	}
	raw, present := args["object"]
	if !present {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok || len(m) == 0 {
		return fmt.Errorf("audit object must be a map of at least one of %s, such as object: {type: repo, value: acme/payments}", strings.Join(auditObjectKeys, ", "))
	}
	for k, v := range m {
		if !contains(auditObjectKeys, k) {
			return fmt.Errorf("audit object.%s is not one of %s", k, strings.Join(auditObjectKeys, ", "))
		}
		if s, ok := v.(string); !ok || s == "" {
			return fmt.Errorf("audit object.%s must be a non-empty string, because the record carries it as one: quote a number, such as id: \"1001\"", k)
		}
	}
	return nil
}
