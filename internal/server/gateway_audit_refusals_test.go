package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// gatewaySession checks a seeded user in and returns the session token and
// the session id the audit records must name.
func gatewaySession(t *testing.T, base, username string) (string, string) {
	t.Helper()
	idToken := loginDeviceFlow(t, base, username, "hunter2!")
	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "x"}},
	})
	if code != http.StatusOK {
		t.Fatalf("checkin = %d: %v", code, body)
	}
	tok, _ := body["session_token"].(string)
	sid, _ := body["session_id"].(string)
	if tok == "" || sid == "" {
		t.Fatalf("checkin answer lacks the token or the session id: %v", body)
	}
	return tok, sid
}

// installEchoApp installs an echo app whose manifest caps calls at rps (0 is
// uncapped), binds every tool to dev and returns the access row so a record
// can be checked against it.
func installEchoApp(t *testing.T, app *App, up *gatewayUpstream, name string, rps int) store.ToolBinding {
	t.Helper()
	ctx := context.Background()
	limits := ""
	if rps > 0 {
		limits = fmt.Sprintf("\n  limits: {rps: %d}", rps)
	}
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: %s}
server: {name: straza.test/%s, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}%s
`, name, name, up.URL, limits))
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	devRole, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: row.ID, ToolMatcher: `["*"]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	catWaitRunning(t, app, name)
	return binding
}

// awaitMCPRecords polls the audit chain until want straza.audit.mcp records
// satisfy sel, then holds a short grace and counts again so a late double is
// caught. It fails unless exactly want match and returns their data objects
// in chain order.
func awaitMCPRecords(t *testing.T, app *App, want int, sel func(map[string]any) bool) []map[string]any {
	t.Helper()
	ctx := context.Background()
	collect := func() ([]map[string]any, []string) {
		recs, err := app.store.Audit().List(ctx, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]any
		var raw []string
		for _, r := range recs {
			if !strings.Contains(r.CE, `"type":"straza.audit.mcp"`) {
				continue
			}
			var ce struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(r.CE), &ce); err != nil {
				t.Fatal(err)
			}
			if sel(ce.Data) {
				out = append(out, ce.Data)
				raw = append(raw, r.CE)
			}
		}
		return out, raw
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got, _ := collect(); len(got) >= want || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	got, raw := collect()
	if len(got) != want {
		t.Fatalf("%d matching straza.audit.mcp records, want exactly %d", len(got), want)
	}
	if len(raw) > 0 {
		t.Logf("mcp audit record: %s", raw[0])
	}
	return got
}

// TestGatewayRefusalsAudited pins that every gateway refusal the PDP never
// sees still leaves one straza.audit.mcp deny record: an unbound or unknown
// name, a tool the session's overlay hides under policyFilter, and a call
// the manifest rate cap throttles. The client answers are unchanged, the
// served calls before the cap keep one allow record each, and fifty probes
// of an unbound name leave fifty records.
func TestGatewayRefusalsAudited(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Apps.Catalog.PolicyFilter = true })
	up := startGatewayUpstream(t)
	user := seedGatewayUser(t, app, "kim", "dev")
	binding := installEchoApp(t, app, up, "refapp", 1)
	catActivate(t, app, "ref-policy", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: ref-policy}
spec:
  match: {roles: [dev]}
  rules:
    - id: deny-env
      tools: [mcp.call]
      apps: [refapp]
      toolNames: {deny: ["env"]}
      effect: deny
      reason: "env reads are blocked for role dev"
    - id: allow-echo
      tools: [mcp.call]
      apps: [refapp]
      toolNames: {allow: ["echo"]}
      effect: allow
`)
	tok, sid := gatewaySession(t, base, "kim")

	rows := []struct {
		name       string
		tool       string
		warm       bool
		wantClient string
		sel        func(map[string]any) bool
		want       map[string]any
	}{
		{"unbound name", "refapp__nothing", false, `unknown tool "refapp__nothing"`,
			func(d map[string]any) bool { return d["toolName"] == "refapp__nothing" },
			map[string]any{
				"effect": "deny", "app": "", "toolName": "refapp__nothing", "ruleId": "", "setName": "",
				"granted": false, "default": false, "session": sid, "user": user.ID,
				"reason": `unknown tool "refapp__nothing": no access row for this session's roles admits it`,
			}},
		{"hidden tool", "refapp__env", false, `unknown tool "refapp__env"`,
			func(d map[string]any) bool { return d["app"] == "refapp" && d["toolName"] == "env" },
			map[string]any{
				"effect": "deny", "app": "refapp", "toolName": "env", "ruleId": "deny-env", "setName": "ref-policy",
				"granted": true, "default": false, "session": sid, "user": user.ID,
				"bindingId": binding.ID, "role": "dev",
				"reason": "env reads are blocked for role dev",
			}},
		{"throttled", "refapp__echo", true, "rate limit exceeded",
			func(d map[string]any) bool {
				return d["app"] == "refapp" && d["toolName"] == "echo" && d["effect"] == "deny"
			},
			map[string]any{
				"effect": "deny", "app": "refapp", "toolName": "echo", "ruleId": "", "setName": "",
				"granted": true, "default": false, "session": sid, "user": user.ID,
				"bindingId": binding.ID, "role": "dev",
				"reason": `Straza: rate limit exceeded for the MCP server "refapp" (1 rps). Retry shortly`,
			}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var answer string
			served := 0
			for i := 0; i < 5; i++ {
				_, call, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
					"name": row.tool, "arguments": map[string]string{"text": "x"},
				})
				// A -32602 refusal is read from the decoded message, since the
				// raw bytes escape the quotes around the tool name.
				answer = string(raw)
				if errObj, _ := call["error"].(map[string]any); errObj != nil {
					answer, _ = errObj["message"].(string)
				}
				if !row.warm || !strings.Contains(answer, "echo: x") {
					break
				}
				served++
			}
			if !strings.Contains(answer, row.wantClient) {
				t.Fatalf("client answer = %s, want %q", answer, row.wantClient)
			}
			if row.warm && served == 0 {
				t.Fatal("the first capped call was not served")
			}
			rec := awaitMCPRecords(t, app, 1, row.sel)[0]
			for k, v := range row.want {
				if rec[k] != v {
					t.Errorf("record %s = %v, want %v", k, rec[k], v)
				}
			}
			if row.warm {
				awaitMCPRecords(t, app, served, func(d map[string]any) bool {
					return d["app"] == "refapp" && d["toolName"] == "echo" && d["effect"] == "allow"
				})
			}
		})
	}
	t.Run("fifty unbound probes leave fifty records", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			_, call, _ := mcpCall(t, base, tok, "tools/call", map[string]any{"name": "refapp__probe"})
			if errObj, _ := call["error"].(map[string]any); errObj == nil {
				t.Fatalf("probe %d = %v, want -32602 unknown tool", i, call)
			}
		}
		awaitMCPRecords(t, app, 50, func(d map[string]any) bool { return d["toolName"] == "refapp__probe" })
	})
}

// TestGatewayToolsListAudited pins that every tools/list answer, whole
// catalog or one page, leaves one straza.audit.mcp record with the event
// tools.list, effect allow, reason "catalog served" and count equal to the
// tools in that answer.
func TestGatewayToolsListAudited(t *testing.T) {
	t.Parallel()
	isList := func(d map[string]any) bool { return d["event"] == "tools.list" }
	rows := []struct {
		name       string
		pageSize   int
		wantCounts []float64
	}{
		{"whole catalog in one answer", 0, []float64{2}},
		{"one page per answer", 1, []float64{1, 1}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			app, base := testApp(t, func(c *config.Config) { c.Apps.Catalog.PageSize = row.pageSize })
			up := startGatewayUpstream(t)
			user := seedGatewayUser(t, app, "kim", "dev")
			installEchoApp(t, app, up, "listapp", 0)
			catRecompile(t, app)
			tok, sid := gatewaySession(t, base, "kim")

			names, pages := walkToolsList(t, base, tok, 2)
			if len(names) != 2 || pages != len(row.wantCounts) {
				t.Fatalf("listed %v over %d pages, want 2 tools over %d", names, pages, len(row.wantCounts))
			}
			recs := awaitMCPRecords(t, app, len(row.wantCounts), isList)
			var counts []float64
			for _, rec := range recs {
				counts = append(counts, rec["count"].(float64))
				want := map[string]any{
					"event": "tools.list", "tool": "", "app": "", "toolName": "", "effect": "allow",
					"ruleId": "", "setName": "", "reason": "catalog served", "granted": false, "default": false,
					"session": sid, "user": user.ID,
				}
				for k, v := range want {
					if rec[k] != v {
						t.Errorf("record %s = %v, want %v", k, rec[k], v)
					}
				}
				if rec["snapshot"] == "" {
					t.Error("record has no snapshot")
				}
			}
			if !reflect.DeepEqual(counts, row.wantCounts) {
				t.Errorf("record counts = %v, want %v", counts, row.wantCounts)
			}
		})
	}
}

// TestGatewayClassifyGate pins that a mode classify allow rule on mcp.call
// runs the classifier on the gateway: the embedded heuristic passes the call
// and the record names the rule, and a missing classifier refuses the call
// with the unavailable sentence and records that deny under the same rule.
func TestGatewayClassifyGate(t *testing.T) {
	t.Parallel()
	const unavailable = "Straza: classifier unavailable for a classify-gated action. Denied"
	rows := []struct {
		name       string
		preRun     func(*App)
		wantServed bool
		wantEffect string
		wantReason string
	}{
		{"heuristic classifier passes the call", nil, true, "allow", ""},
		{"missing classifier fails closed", func(a *App) { a.classifier = nil }, false, "deny", unavailable},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var pre []func(*App)
			if row.preRun != nil {
				pre = append(pre, row.preRun)
			}
			app, base := testAppPreRun(t, pre)
			up := startGatewayUpstream(t)
			seedGatewayUser(t, app, "kim", "dev")
			installEchoApp(t, app, up, "clsapp", 0)
			catActivate(t, app, "cls-policy", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: cls-policy}
spec:
  match: {roles: [dev]}
  rules:
    - id: classify-echo
      tools: [mcp.call]
      apps: [clsapp]
      toolNames: {allow: ["echo"]}
      mode: classify
      effect: allow
`)
			tok, _ := gatewaySession(t, base, "kim")
			_, _, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
				"name": "clsapp__echo", "arguments": map[string]string{"text": "hi"},
			})
			if served := strings.Contains(string(raw), "echo: hi"); served != row.wantServed {
				t.Fatalf("served = %v, want %v: %s", served, row.wantServed, raw)
			}
			if !row.wantServed && !strings.Contains(string(raw), row.wantReason) {
				t.Fatalf("refusal = %s, want %q", raw, row.wantReason)
			}
			rec := awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["app"] == "clsapp" })[0]
			want := map[string]any{"effect": row.wantEffect, "ruleId": "classify-echo", "setName": "cls-policy", "granted": true, "default": false}
			if row.wantReason != "" {
				want["reason"] = row.wantReason
			}
			for k, v := range want {
				if rec[k] != v {
					t.Errorf("record %s = %v, want %v", k, rec[k], v)
				}
			}
		})
	}
}

// TestGatewayLegacyObligationsUnrecorded pins revision 18 for a set stored
// active with an obligations list: it keeps governing (the allow by
// watch-echo stands) and its straza.audit.mcp record carries no
// obligations, because no lane ever ran them.
func TestGatewayLegacyObligationsUnrecorded(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	installEchoApp(t, app, up, "oblapp", 0)
	catActivate(t, app, "obl-policy", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: obl-policy}
spec:
  match: {roles: [dev]}
  rules:
    - id: watch-echo
      tools: [mcp.call]
      apps: [oblapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      obligations: [notify, redact]
`)
	tok, _ := gatewaySession(t, base, "kim")
	_, _, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
		"name": "oblapp__echo", "arguments": map[string]string{"text": "hi"},
	})
	if !strings.Contains(string(raw), "echo: hi") {
		t.Fatalf("call = %s, want the upstream answer", raw)
	}
	rec := awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["app"] == "oblapp" })[0]
	if rec["ruleId"] != "watch-echo" || rec["effect"] != "allow" {
		t.Errorf("record = %v, want an allow by watch-echo", rec)
	}
	if got, ok := rec["obligations"]; ok {
		t.Errorf("record obligations = %v, want none since revision 18", got)
	}
}

// TestGatewayHoldRecordsVerdict pins that a held call's straza.audit.mcp
// record says what the person decided, not the verdict the gate was still
// waiting on: a denied hold is one deny record with the gating rule, its set
// and the denied-by reason the model read, and an approved hold stays one
// allow record with the rule's own reason. Each row is its own call with its
// own arguments, so the fingerprints differ and no exemption crosses rows.
func TestGatewayHoldRecordsVerdict(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()
	kim := seedGatewayUser(t, app, "kim", "dev")
	approver := seedApprover(t, app, "ada", "sec-approvers")
	installEchoApp(t, app, up, "holdapp", 0)
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "hold-policy", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: hold-policy}
spec:
  match: {roles: [dev]}
  rules:
    - id: hold-echo
      tools: [mcp.call]
      apps: [holdapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
`,
	}); err != nil {
		t.Fatal(err)
	}
	catRecompile(t, app)
	tok := sessionToken(t, base, "kim")

	rows := []struct {
		name, verdict, wantEffect, wantAnswer string
	}{
		{"denied", "denied", "deny", "Straza: approval denied by ada (ref "},
		{"approved", "approved", "allow", "echo: approved"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			answer := make(chan []byte, 1)
			go func() {
				_, _, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
					"name": "holdapp__echo", "arguments": map[string]string{"text": row.name},
				})
				answer <- raw
			}()
			var id string
			for deadline := time.Now().Add(10 * time.Second); id == "" && time.Now().Before(deadline); {
				if pending, _ := app.approval.List(ctx, "pending"); len(pending) == 1 {
					id = pending[0].ID
				} else {
					time.Sleep(20 * time.Millisecond)
				}
			}
			if id == "" {
				t.Fatal("the held call opened no pending approval")
			}
			if _, err := app.approval.Decide(ctx, id, row.verdict, approver.ID, "console", "", ""); err != nil {
				t.Fatalf("Decide: %v", err)
			}
			select {
			case raw := <-answer:
				if !strings.Contains(string(raw), row.wantAnswer) {
					t.Fatalf("client answer = %s, want %q", raw, row.wantAnswer)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("the held call never answered after the decision")
			}
			recs := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
				return d["app"] == "holdapp" && strings.Contains(fmt.Sprint(d["arguments"]), row.name)
			})
			want := map[string]any{"effect": row.wantEffect, "ruleId": "hold-echo", "setName": "hold-policy", "user": kim.ID, "reason": ""}
			if row.wantEffect == "deny" {
				want["reason"] = "Straza: approval denied by ada (ref " + id + ")"
			}
			for k, v := range want {
				if recs[0][k] != v {
					t.Errorf("record %s = %v, want %v", k, recs[0][k], v)
				}
			}
		})
	}
}
