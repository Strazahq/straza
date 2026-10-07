package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
)

// waitViews waits until the manager serves want views for the server name,
// which it reads in the same probe that settles the server running.
func waitViews(t *testing.T, app *App, name string, want int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if v, ok := app.manager.View(name); ok && len(v.Views) == want {
			return
		}
		if time.Now().After(deadline) {
			v, _ := app.manager.View(name)
			t.Fatalf("%s serves %d views, want %d", name, len(v.Views), want)
		}
	}
}

// resourceURIs returns the uris of a resources/list answer.
func resourceURIs(t *testing.T, res map[string]any, raw []byte) []string {
	t.Helper()
	var uris []string
	for _, r := range resultOf(t, res, raw)["resources"].([]any) {
		uris = append(uris, r.(map[string]any)["uri"].(string))
	}
	return uris
}

// TestGatewayServerViews pins the views of a server whose switch is on: the
// endpoint advertises them, lists the tools' links verbatim, lists and
// reads exactly the views a tool the caller can see links, refuses any
// other URI with -32002, and leaves one record per answer. Under
// policyFilter the overlay hides old, so the view only old links is
// neither listed nor readable.
func TestGatewayServerViews(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name         string
		policyFilter bool
		wantViews    []string
	}{
		{"every linking tool visible", false, []string{legacyURI, panelURI}},
		{"the overlay hides old", true, []string{panelURI}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			app, base := testApp(t, func(c *config.Config) { c.Apps.Catalog.PolicyFilter = row.policyFilter })
			up := startViewsUpstream(t)
			kim := seedGatewayUser(t, app, "kim", "dev")
			installViewsApp(t, app, up, "viewapp", true, 0)
			waitViews(t, app, "viewapp", 2)
			catActivate(t, app, "views-policy", viewsPolicy("viewapp"))
			tok, sid := gatewaySession(t, base, "kim")

			_, init, raw := mcpServerCall(t, base, "viewapp", tok, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
			wantCaps := map[string]any{
				"tools": map[string]any{"listChanged": true}, "resources": map[string]any{},
				"extensions": map[string]any{"io.modelcontextprotocol/ui": map[string]any{}},
			}
			if caps := resultOf(t, init, raw)["capabilities"]; !reflect.DeepEqual(caps, wantCaps) {
				t.Errorf("capabilities = %v, want %v", caps, wantCaps)
			}
			_, list, raw := mcpServerCall(t, base, "viewapp", tok, "tools/list", nil)
			for _, tl := range resultOf(t, list, raw)["tools"].([]any) {
				m := tl.(map[string]any)
				if m["name"] == "show" && !reflect.DeepEqual(m["_meta"], map[string]any{"ui": map[string]any{"resourceUri": panelURI}, "x/kept": "yes"}) {
					t.Errorf("show _meta = %v, want the upstream's verbatim", m["_meta"])
				}
			}
			if _, _, raw := mcpCall(t, base, tok, "tools/list", nil); strings.Contains(string(raw), "_meta") {
				t.Errorf("combined list carries _meta with views on: %s", raw)
			}

			_, res, raw := mcpServerCall(t, base, "viewapp", tok, "resources/list", nil)
			if got := resourceURIs(t, res, raw); !reflect.DeepEqual(got, row.wantViews) {
				t.Fatalf("listed views = %v, want %v", got, row.wantViews)
			}
			panel := resultOf(t, res, raw)["resources"].([]any)[len(row.wantViews)-1].(map[string]any)
			if panel["name"] != "panel" || panel["mimeType"] != manager.ViewMIMEType {
				t.Errorf("panel resource = %v", panel)
			}
			_, res, raw = mcpServerCall(t, base, "viewapp", tok, "resources/read", map[string]any{"uri": panelURI})
			contents := resultOf(t, res, raw)["contents"].([]any)
			want := map[string]any{
				"uri": panelURI, "mimeType": manager.ViewMIMEType, "text": viewHTML(panelURI),
				"_meta": map[string]any{"ui": map[string]any{"prefersBorder": true}},
			}
			if len(contents) != 1 || !reflect.DeepEqual(contents[0], want) {
				t.Errorf("read panel = %v, want %v", contents, want)
			}
			refused := []string{"ui://viewapp/nope"}
			if row.policyFilter {
				refused = append(refused, legacyURI)
			}
			for _, uri := range refused {
				res := second(mcpServerCall(t, base, "viewapp", tok, "resources/read", map[string]any{"uri": uri}))
				if code, msg := rpcErrorOf(t, res, nil); code != -32002 || msg != viewUnavailableMsg(uri) {
					t.Errorf("read %s = %d %q, want -32002 %q", uri, code, msg, viewUnavailableMsg(uri))
				}
			}
			if _, res, raw := mcpServerCall(t, base, "viewapp", tok, "resources/templates/list", nil); !reflect.DeepEqual(resultOf(t, res, raw), map[string]any{"resourceTemplates": []any{}}) {
				t.Errorf("templates = %s", raw)
			}
			res = second(mcpServerCall(t, base, "viewapp", tok, "resources/read", nil))
			if code, msg := rpcErrorOf(t, res, nil); code != -32602 || msg != uriRequiredMsg {
				t.Errorf("read without a uri = %d %q", code, msg)
			}

			common := map[string]any{"session": sid, "user": kim.ID, "app": "viewapp", "tool": "", "toolName": "", "ruleId": "", "setName": ""}
			check := func(rec map[string]any, extra map[string]any) {
				t.Helper()
				for _, m := range []map[string]any{common, extra} {
					for k, v := range m {
						if rec[k] != v {
							t.Errorf("record %s = %v, want %v (record %v)", k, rec[k], v, rec)
						}
					}
				}
			}
			check(awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["event"] == eventResourcesList })[0],
				map[string]any{"effect": "allow", "reason": "views served", "count": float64(len(row.wantViews))})
			check(awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["event"] == eventResourcesRead && d["effect"] == "allow" })[0],
				map[string]any{"reason": "view served", "uri": panelURI})
			for _, uri := range refused {
				check(awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["event"] == eventResourcesRead && d["uri"] == uri })[0],
					map[string]any{"effect": "deny", "reason": viewUnavailableMsg(uri)})
			}
		})
	}
}

// slotOf returns the decision slot of a tools/call answer, or nil when it
// carries none.
func slotOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var res struct {
		Result struct {
			IsError bool                      `json:"isError"`
			Meta    map[string]map[string]any `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("answer %s: %v", raw, err)
	}
	return res.Result.Meta[decisionSlotKey]
}

// TestGatewayServerDecisionSlot pins the decision slot on the endpoint of a
// server whose views are on: a policy deny and a throttle carry a denied
// slot, while the same deny on /mcp and on a server whose views are off
// carries none, and an allowed result carries none either. The text the
// model reads is unchanged.
func TestGatewayServerDecisionSlot(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	up := startViewsUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	installViewsApp(t, app, up, "viewapp", true, 0)
	installViewsApp(t, app, up, "offapp", false, 0)
	installViewsApp(t, app, up, "capped", true, 1)
	waitViews(t, app, "viewapp", 2)
	catActivate(t, app, "views-policy", viewsPolicy("viewapp", "offapp", "capped"))
	tok, _ := gatewaySession(t, base, "kim")

	_, _, raw := mcpServerCall(t, base, "viewapp", tok, "tools/call", map[string]any{"name": "old"})
	want := map[string]any{"v": float64(1), "decision": "denied", "audited": true, "reason": "Old views are retired", "source": "Straza"}
	if got := slotOf(t, raw); !reflect.DeepEqual(got, want) {
		t.Errorf("policy deny slot = %v, want %v", got, want)
	}
	if !strings.Contains(string(raw), `"text":"Straza: old views are retired"`) {
		t.Errorf("policy deny text changed: %s", raw)
	}
	for _, c := range []struct {
		what string
		raw  []byte
	}{
		{"/mcp", third(mcpCall(t, base, tok, "tools/call", map[string]any{"name": "viewapp__old"}))},
		{"views off", third(mcpServerCall(t, base, "offapp", tok, "tools/call", map[string]any{"name": "old"}))},
		{"allowed", third(mcpServerCall(t, base, "viewapp", tok, "tools/call", map[string]any{"name": "plain", "arguments": map[string]string{"text": "x"}}))},
	} {
		if strings.Contains(string(c.raw), "_meta") {
			t.Errorf("%s answer carries _meta: %s", c.what, c.raw)
		}
	}

	var throttled []byte
	for i := 0; i < 6 && throttled == nil; i++ {
		if _, _, raw := mcpServerCall(t, base, "capped", tok, "tools/call", map[string]any{"name": "plain"}); strings.Contains(string(raw), "rate limit exceeded") {
			throttled = raw
		}
	}
	if throttled == nil {
		t.Fatal("the rate cap never fired")
	}
	if s := slotOf(t, throttled); s["decision"] != "denied" || s["audited"] != true || !strings.HasPrefix(s["reason"].(string), "Rate limit exceeded for the MCP server") {
		t.Errorf("throttle slot = %v", s)
	}
}

// holdPolicy holds show for a person in sec-approvers and lets the session
// see the native approval tools, so the combined endpoint's pending
// sentence would name straza__approval_await.
const holdPolicy = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: hold-views}
spec:
  match: {roles: [dev]}
  rules:
    - id: hold-show
      tools: [mcp.call]
      apps: [viewapp]
      toolNames: {allow: ["show"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
    - id: allow-straza
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: ["approval_await", "approval_status", "approval_request"]}
      effect: allow
`

// TestGatewayServerHeldSlot pins the slot of a held call on a server
// endpoint whose views are on. A hold that outlives the in-request wait is
// held, with the approval's ref and window end, and its sentence never
// names straza__approval_await, which this endpoint does not list. A hold a
// person denies is denied, with the ref.
func TestGatewayServerHeldSlot(t *testing.T) {
	t.Parallel()
	t.Run("pending", func(t *testing.T) {
		t.Parallel()
		app, base := testApp(t, func(c *config.Config) { c.Approval.GatewayHoldSeconds = 1 })
		up := startViewsUpstream(t)
		seedGatewayUser(t, app, "kim", "dev")
		seedApprover(t, app, "ada", "sec-approvers")
		installViewsApp(t, app, up, "viewapp", true, 0)
		catActivate(t, app, "hold-views", holdPolicy)
		tok, _ := gatewaySession(t, base, "kim")

		_, _, raw := mcpServerCall(t, base, "viewapp", tok, "tools/call", map[string]any{"name": "show", "arguments": map[string]any{"text": "a", justificationField: "why"}})
		pending, _ := app.approval.List(context.Background(), "pending")
		if len(pending) != 1 {
			t.Fatalf("%d pending approvals, want 1: %s", len(pending), raw)
		}
		if !strings.Contains(string(raw), "approval pending (ref "+pending[0].ID+")") || strings.Contains(string(raw), "straza__approval_await") {
			t.Errorf("pending text = %s, want the pending sentence without the await tool", raw)
		}
		s := slotOf(t, raw)
		exp, err := time.Parse(time.RFC3339, s["expiresAt"].(string))
		if err != nil || exp.Sub(pending[0].ExpiresAt).Abs() > time.Second {
			t.Errorf("slot expiresAt = %v, want the approval's %v", s["expiresAt"], pending[0].ExpiresAt)
		}
		delete(s, "expiresAt")
		want := map[string]any{"v": float64(1), "decision": "held", "audited": true, "reason": heldSlotReason, "ref": pending[0].ID, "source": "Straza"}
		if !reflect.DeepEqual(s, want) {
			t.Errorf("held slot = %v, want %v", s, want)
		}
	})
	t.Run("denied by a person", func(t *testing.T) {
		t.Parallel()
		app, base := testApp(t)
		up := startViewsUpstream(t)
		seedGatewayUser(t, app, "kim", "dev")
		approver := seedApprover(t, app, "ada", "sec-approvers")
		installViewsApp(t, app, up, "viewapp", true, 0)
		catActivate(t, app, "hold-views", holdPolicy)
		tok, _ := gatewaySession(t, base, "kim")

		answer := make(chan []byte, 1)
		go func() {
			_, _, raw := mcpServerCall(t, base, "viewapp", tok, "tools/call", map[string]any{"name": "show", "arguments": map[string]any{"text": "b"}})
			answer <- raw
		}()
		var id string
		for deadline := time.Now().Add(10 * time.Second); id == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if pending, _ := app.approval.List(context.Background(), "pending"); len(pending) == 1 {
				id = pending[0].ID
			}
		}
		if id == "" {
			t.Fatal("the held call opened no pending approval")
		}
		if _, err := app.approval.Decide(context.Background(), id, "denied", approver.ID, "console", "", ""); err != nil {
			t.Fatal(err)
		}
		select {
		case raw := <-answer:
			want := map[string]any{"v": float64(1), "decision": "denied", "audited": true, "reason": "Approval denied by ada (ref " + id + ")", "ref": id, "source": "Straza"}
			if got := slotOf(t, raw); !reflect.DeepEqual(got, want) {
				t.Errorf("denied-hold slot = %v, want %v", got, want)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the held call never answered after the decision")
		}
	})
}

// third is the raw bytes of an mcpPost answer.
func third(_ int, _ map[string]any, raw []byte) []byte { return raw }

// TestGatewayServerAuditQueueFull pins a server endpoint under audit
// backpressure block with a full queue: an allowed call is refused with a
// denied slot that says it was not audited, and resources/list and
// resources/read are refused as tools/list is.
func TestGatewayServerAuditQueueFull(t *testing.T) {
	t.Parallel()
	var tok string
	_, base, _, _ := heldAuditApp(t, true, 300*time.Millisecond, func(app *App, base string) {
		seedGatewayUser(t, app, "kim", "dev")
		installViewsApp(t, app, startViewsUpstream(t), "viewapp", true, 0)
		waitViews(t, app, "viewapp", 2)
		catActivate(t, app, "views-policy", viewsPolicy("viewapp"))
		tok, _ = gatewaySession(t, base, "kim")
	})

	_, _, raw := mcpServerCall(t, base, "viewapp", tok, "tools/call", map[string]any{"name": "plain"})
	want := map[string]any{"v": float64(1), "decision": "denied", "audited": false, "reason": personReason(auditQueueFullMsg), "source": "Straza"}
	if got := slotOf(t, raw); !reflect.DeepEqual(got, want) || strings.Contains(string(raw), "plain: ") {
		t.Errorf("call answer %s, want the refusal with slot %v", raw, want)
	}
	for _, c := range []struct {
		method string
		params any
	}{{"resources/list", nil}, {"resources/read", map[string]any{"uri": panelURI}}} {
		res := second(mcpServerCall(t, base, "viewapp", tok, c.method, c.params))
		if code, msg := rpcErrorOf(t, res, nil); code != -32603 || msg != auditQueueFullMsg {
			t.Errorf("%s = %d %q, want -32603 with auditQueueFullMsg", c.method, code, msg)
		}
	}
}

// TestGatewayServerNoDBOnRequestPath extends the no-database rule of the
// request path to a server endpoint: initialize, tools/list, tools/call,
// resources/list, resources/read and the refusal of a server outside the
// catalog read no repository.
func TestGatewayServerNoDBOnRequestPath(t *testing.T) {
	t.Parallel()
	app, base, cs := testAppCounting(t)
	up := startViewsUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	installViewsApp(t, app, up, "viewapp", true, 0)
	waitViews(t, app, "viewapp", 2)
	catActivate(t, app, "views-policy", viewsPolicy("viewapp"))
	tok := sessionToken(t, base, "kim")

	before := cs.snapshot()
	calls := []struct {
		server, method string
		params         any
		want           string
	}{
		{"viewapp", "initialize", nil, "io.modelcontextprotocol/ui"},
		{"viewapp", "tools/list", nil, `"name":"show"`},
		{"viewapp", "tools/call", map[string]any{"name": "plain", "arguments": map[string]string{"text": "nodb"}}, "plain: nodb"},
		{"viewapp", "resources/list", nil, panelURI},
		{"viewapp", "resources/read", map[string]any{"uri": panelURI}, "doctype html"},
		{"viewapp", "resources/read", map[string]any{"uri": "ui://viewapp/nope"}, "-32002"},
		{"nope", "tools/list", nil, "is in your catalog"},
	}
	for _, c := range calls {
		if _, _, raw := mcpServerCall(t, base, c.server, tok, c.method, c.params); !strings.Contains(string(raw), c.want) {
			t.Errorf("%s on %s = %s, want %q in it", c.method, c.server, raw, c.want)
		}
	}
	assertNoRepoAccess(t, cs, "gateway server endpoint", before)
}
