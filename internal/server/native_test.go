package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// nativeToolsPolicy is one policy set exercising every branch the native
// approval_request tool must distinguish: a ticket rule, a hold rule, a plain
// allow, and (via the mcp.call default) a plain deny.
const nativeToolsPolicy = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: native-tools}
spec:
  match: {roles: [dev]}
  rules:
    - id: deploy-ticket
      tools: [shell.exec]
      command: {allowPatterns: ["deploy-prod*"]}
      effect: allow
      mode: approve
      approve: {class: ticket, roles: [change], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}
    - id: vault-hold
      tools: [mcp.call]
      apps: [vault]
      toolNames: {allow: ["rotate_root_key"]}
      effect: allow
      mode: approve
      approve: {roles: [sec], timeoutSeconds: 90}
    - id: allow-echo
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
    - id: mp-ticket
      tools: [mcp.call]
      apps: [midpoint]
      toolNames: {allow: ["disable_user"]}
      effect: allow
      mode: approve
      approve: {class: ticket, roles: [sec], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}
`

// seedNativeToolsApp boots an app with the native-tools policy compiled and
// returns the requester identity + a dev subject.
func seedNativeToolsApp(t *testing.T) (*App, store.User, policy.Subject) {
	t.Helper()
	app, _ := testApp(t)
	requester := seedIdentity(t, app)
	ctx := context.Background()
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "native-tools", Status: "active", YAMLSource: nativeToolsPolicy,
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatalf("recompile: %v", err)
	}
	return app, requester, policy.Subject{User: "kim", Roles: []string{"dev"}}
}

// nativeStruct asserts a successful native tool result and returns its
// structured content as a map.
func nativeStruct(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res == nil {
		t.Fatal("nil result")
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", toolErrText(res))
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured content: %+v", res)
	}
	return m
}

// TestNativeApprovalRequest is the table-driven spec for the approval_request
// native tool: concrete-call rejection, hold-rule rejection, the ticket happy
// path, no-approval-needed, and denied-by-policy.
func TestNativeApprovalRequest(t *testing.T) {
	t.Parallel()
	app, requester, sub := seedNativeToolsApp(t)
	ctx := context.Background()
	claims := authn.Claims{Session: "sess-req", Subject: requester.ID}

	cases := []struct {
		name    string
		args    map[string]any
		wantErr string // substring in the tool-error text; "" ⇒ expect success
	}{
		{
			name:    "no concrete call is rejected",
			args:    map[string]any{"reason": "let me deploy something later"},
			wantErr: "a concrete call is required",
		},
		{
			name:    "tool without an identity field is rejected",
			args:    map[string]any{"action": map[string]any{"tool": "mcp.call"}, "reason": "x"},
			wantErr: "a concrete call is required",
		},
		{
			name: "a hold rule is rejected (holds are interactive)",
			args: map[string]any{"action": map[string]any{
				"tool": "mcp.call", "app": "vault", "tool_name": "rotate_root_key",
			}, "reason": "rotate"},
			wantErr: "hold",
		},
		{
			name: "a plain allow needs no approval",
			args: map[string]any{"action": map[string]any{
				"tool": "mcp.call", "app": "echoapp", "tool_name": "echo",
			}},
			wantErr: "no approval needed",
		},
		{
			name: "a plain deny is denied by policy",
			args: map[string]any{"action": map[string]any{
				"tool": "mcp.call", "app": "blocked", "tool_name": "wipe",
			}},
			wantErr: "denied by policy",
		},
		{
			name: "a ticket rule opens a ticket",
			args: map[string]any{"action": map[string]any{
				"tool": "shell.exec", "command": "deploy-prod --now",
			}, "reason": "ship the release"},
			wantErr: "",
		},
		{
			// Fingerprint v2: a call-bound rule (the default) cannot mint a
			// consumable grant from a described mcp action WITHOUT its args:
			// reject with the taxonomy error instead of a dead-on-arrival ticket.
			name: "an mcp ticket without args is rejected under binding call",
			args: map[string]any{"action": map[string]any{
				"tool": "mcp.call", "app": "midpoint", "tool_name": "disable_user",
			}, "reason": "offboard temp-x"},
			wantErr: "needs action.args",
		},
		{
			name: "an mcp ticket with exact args opens a ticket",
			args: map[string]any{"action": map[string]any{
				"tool": "mcp.call", "app": "midpoint", "tool_name": "disable_user",
				"args": map[string]any{"user": "temp-x"},
			}, "reason": "offboard temp-x"},
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := app.nativeApprovalRequest(ctx, mustJSON(tc.args), claims, sub)
			if tc.wantErr != "" {
				if !res.IsError || !strings.Contains(toolErrText(res), tc.wantErr) {
					t.Fatalf("got %q, want error containing %q", toolErrText(res), tc.wantErr)
				}
				return
			}
			m := nativeStruct(t, res)
			if m["ref"] == "" || m["ref"] == nil {
				t.Errorf("missing ref: %v", m)
			}
			if m["state"] != string(approval.StatePending) {
				t.Errorf("state = %v, want pending", m["state"])
			}
			if m["expires_at"] == nil {
				t.Errorf("missing expires_at: %v", m)
			}
		})
	}
}

// TestNativeApprovalRequestArgsConverge (fingerprint v2): the key a native
// mcp ticket mints from model-authored args must EQUAL the key the gateway
// PEP computes for the later real call (across key-order and whitespace
// noise, and with the injected justification present), or the grant could
// never be consumed. Also pins the v2:call tag and the args preview riding
// the ticket.
func TestNativeApprovalRequestArgsConverge(t *testing.T) {
	t.Parallel()
	app, requester, sub := seedNativeToolsApp(t)
	app.cfg.Approval.Preview.Enabled = true
	ctx := context.Background()
	claims := authn.Claims{Session: "sess-req", Subject: requester.ID}

	res := nativeStruct(t, app.nativeApprovalRequest(ctx, mustJSON(map[string]any{
		"action": map[string]any{
			"tool": "mcp.call", "app": "midpoint", "tool_name": "disable_user",
			"args": map[string]any{"user": "temp-x", "force": true},
		},
		"reason": "offboard temp-x",
	}), claims, sub))
	ref, _ := res["ref"].(string)
	rec, err := app.approval.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get(%s): %v", ref, err)
	}
	if !strings.HasPrefix(rec.ArgvHash, "v2:call:sha256:") {
		t.Errorf("native mint key = %q, want v2:call", rec.ArgvHash)
	}
	if rec.ArgsPreview == "" || !strings.Contains(rec.ArgsPreview, "temp-x") {
		t.Errorf("args preview missing from native ticket: %q", rec.ArgsPreview)
	}

	// The later REAL gateway call: same logical args, different key order +
	// whitespace + the injected justification the gateway strips/drops.
	gwEv := policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user",
		Args: []byte("{ \"force\" : true, \"_straza_justification\": \"approved ticket\", \"user\" : \"temp-x\" }"),
	}
	key, err := approval.EventKeyV2(gwEv, policy.ApproveBindingCall)
	if err != nil {
		t.Fatal(err)
	}
	if key != rec.ArgvHash {
		t.Errorf("gateway consume key %q != native mint key %q (the grant would be unconsumable)", key, rec.ArgvHash)
	}
}

// TestNativeApprovalRequestDedupe: a repeated request for the same described
// call attaches to the SAME pending ticket (the PEP dedupe path), never a
// second row.
func TestNativeApprovalRequestDedupe(t *testing.T) {
	t.Parallel()
	app, requester, sub := seedNativeToolsApp(t)
	ctx := context.Background()
	claims := authn.Claims{Session: "sess-req", Subject: requester.ID}
	args := mustJSON(map[string]any{"action": map[string]any{
		"tool": "shell.exec", "command": "deploy-prod --now",
	}, "reason": "ship"})

	first := nativeStruct(t, app.nativeApprovalRequest(ctx, args, claims, sub))
	second := nativeStruct(t, app.nativeApprovalRequest(ctx, args, claims, sub))
	if first["ref"] != second["ref"] {
		t.Errorf("dedupe: refs differ %v vs %v", first["ref"], second["ref"])
	}
	pending, _ := app.approval.List(ctx, "pending")
	if len(pending) != 1 || pending[0].Class != "ticket" {
		t.Fatalf("want exactly 1 pending ticket, got %+v", pending)
	}
	if pending[0].Justification != "ship" {
		t.Errorf("justification = %q, want the reason carried through", pending[0].Justification)
	}
}

// TestNativeApprovalStatusScoping: a requester reads its own ticket; another
// user's ref is not-found (no cross-user leak).
func TestNativeApprovalStatusScoping(t *testing.T) {
	t.Parallel()
	app, requester, sub := seedNativeToolsApp(t)
	ctx := context.Background()
	claims := authn.Claims{Session: "sess-req", Subject: requester.ID}
	req := nativeStruct(t, app.nativeApprovalRequest(ctx, mustJSON(map[string]any{
		"action": map[string]any{"tool": "shell.exec", "command": "deploy-prod --now"},
	}), claims, sub))
	ref := req["ref"].(string)

	own := nativeStruct(t, app.nativeApprovalStatus(ctx, mustJSON(map[string]any{"ref": ref}), claims))
	if own["state"] != string(approval.StatePending) || own["class"] != "ticket" {
		t.Errorf("owner status = %v, want pending ticket", own)
	}
	if own["expires_at"] == nil {
		t.Errorf("owner status missing expires_at: %v", own)
	}

	other := authn.Claims{Session: "sess-other", Subject: "u-other"}
	res := app.nativeApprovalStatus(ctx, mustJSON(map[string]any{"ref": ref}), other)
	if !res.IsError || !strings.Contains(toolErrText(res), "no such approval") {
		t.Errorf("cross-user status = %q, want not-found", toolErrText(res))
	}
}

// TestNativeApprovalAwaitImmediate: an already-resolved ticket returns at once
// (no wait), carrying the resolved state.
func TestNativeApprovalAwaitImmediate(t *testing.T) {
	// Serial: its await budget flakes under the load of parallel servers.
	app, requester, _ := seedNativeToolsApp(t)
	ctx := context.Background()
	claims := authn.Claims{Session: "sess-req", Subject: requester.ID}

	grantExp := time.Now().UTC().Add(time.Hour)
	rec, err := app.store.Approvals().Insert(ctx, store.Approval{
		SessionID: "sess-req", UserID: requester.ID, RuleID: "deploy-ticket",
		Class: "ticket", State: "approved", DecidedBy: "u-appr", DecidedByName: "Ada Approver",
		CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(24 * time.Hour),
		GrantExpiresAt: &grantExp,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	start := time.Now()
	res := app.nativeApprovalAwait(ctx, mustJSON(map[string]any{"ref": rec.ID, "max_wait_seconds": 30}), claims)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("await on a resolved ticket blocked %s, want immediate", d)
	}
	m := nativeStruct(t, res)
	if m["state"] != string(approval.StateApproved) {
		t.Errorf("state = %v, want approved", m["state"])
	}
	if m["timed_out"] != false {
		t.Errorf("timed_out = %v, want false", m["timed_out"])
	}
	if m["decided_by"] != "Ada Approver" {
		t.Errorf("decided_by = %v, want the approver name", m["decided_by"])
	}
}

// TestNativeApprovalAwaitBounded: a still-pending ticket returns state=pending
// with timed_out=true once the bounded wait elapses (the wait never runs to the
// day-scale ticketTTL).
func TestNativeApprovalAwaitBounded(t *testing.T) {
	// Serial: its await budget flakes under the load of parallel servers.
	app, requester, _ := seedNativeToolsApp(t)
	ctx := context.Background()
	claims := authn.Claims{Session: "sess-req", Subject: requester.ID}

	rec, err := app.store.Approvals().Insert(ctx, store.Approval{
		SessionID: "sess-req", UserID: requester.ID, RuleID: "deploy-ticket",
		Class: "ticket", State: "pending",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	start := time.Now()
	res := app.nativeApprovalAwait(ctx, mustJSON(map[string]any{"ref": rec.ID, "max_wait_seconds": 1}), claims)
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Errorf("bounded await took %s, want ~1s", elapsed)
	}
	m := nativeStruct(t, res)
	if m["state"] != string(approval.StatePending) {
		t.Errorf("state = %v, want pending", m["state"])
	}
	if m["timed_out"] != true {
		t.Errorf("timed_out = %v, want true", m["timed_out"])
	}
}

// TestClampAwaitSeconds pins the bound: default 30 for non-positive, cap 60.
func TestClampAwaitSeconds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want int }{
		{0, 30}, {-5, 30}, {1, 1}, {30, 30}, {45, 45}, {60, 60}, {61, 60}, {600, 60},
	} {
		if got := clampAwaitSeconds(tc.in); got != tc.want {
			t.Errorf("clampAwaitSeconds(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestNativeStrazaCatalogVisibility proves the virtual app hooks into the
// gateway catalog: the built-in tools are ORIGINATED into every tier-1 catalog
// (no binding), and the tier-2 overlay makes visibility track authorization: a
// role the policy allows to call them sees them; a role it does not sees none
// (default-deny preserved without a binding).
func TestNativeStrazaCatalogVisibility(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "native-vis", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: native-vis}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-native
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: ["*"]}
      effect: allow
`,
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatalf("recompile: %v", err)
	}
	native := []string{"straza__approval_request", "straza__approval_status", "straza__approval_await"}

	// tier-1 originates the native tools for any role (candidates).
	tier1 := app.buildCatalog([]string{"dev"})
	t1 := map[string]bool{}
	for _, tl := range tier1.tools {
		t1[tl.Name] = true
	}
	for _, n := range native {
		if !t1[n] {
			t.Errorf("tier-1 missing originated native tool %q", n)
		}
		if tgt, ok := tier1.targets[n]; !ok || tgt.app != nativeAppName {
			t.Errorf("target for %q = %+v, want app=%s", n, tgt, nativeAppName)
		}
	}

	// dev is authorized ⇒ native tools survive the overlay.
	eng := app.snapshots.Current().Engine
	ovDev := app.buildOverlay("k1", policy.Subject{Roles: []string{"dev"}}, tier1, eng)
	visDev := map[string]bool{}
	for _, tl := range overlayTools(tier1, ovDev) {
		visDev[tl.Name] = true
	}
	for _, n := range native {
		if !visDev[n] {
			t.Errorf("authorized role should see native tool %q", n)
		}
	}

	// an unauthorized role ⇒ every native tool is hidden by the overlay.
	ovOther := app.buildOverlay("k2", policy.Subject{Roles: []string{"other"}}, tier1, eng)
	for _, tl := range overlayTools(tier1, ovOther) {
		if strings.HasPrefix(tl.Name, nativeAppName+"__") {
			t.Errorf("native tool %q leaked to an unauthorized role", tl.Name)
		}
	}
}

// TestGatewayNativeStrazaEndToEnd drives the whole hookup through the real /mcp
// surface: a policy authorizes the built-in `straza` tools for a role (no
// binding; the tools are originated in-process and gated by policy), the
// session lists the three native tools, and a governed tools/call to
// straza__approval_request opens a ticket for a described concrete call.
func TestGatewayNativeStrazaEndToEnd(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	seedGatewayUser(t, app, "kim", "dev")

	// Policy: dev may call the native tools; a shell deploy is ticket-gated.
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "native-e2e", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: native-e2e}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-native
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: ["*"]}
      effect: allow
    - id: deploy-ticket
      tools: [shell.exec]
      command: {allowPatterns: ["deploy-prod*"]}
      effect: allow
      mode: approve
      approve: {class: ticket, roles: [change], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}
`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}

	tok := sessionToken(t, base, "kim")

	names := toolNamesOf(t, second(mcpCall(t, base, tok, "tools/list", nil)))
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	for _, want := range []string{"straza__approval_request", "straza__approval_status", "straza__approval_await"} {
		if !got[want] {
			t.Errorf("tools/list missing native tool %q (have %v)", want, names)
		}
	}

	_, res, _ := mcpCall(t, base, tok, "tools/call", map[string]any{
		"name": "straza__approval_request",
		"arguments": map[string]any{
			"action": map[string]any{"tool": "shell.exec", "command": "deploy-prod --now"},
			"reason": "ship the release",
		},
	})
	result, _ := res["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no result: %v", res)
	}
	sc, _ := result["structuredContent"].(map[string]any)
	if sc == nil {
		t.Fatalf("no structuredContent: %v", result)
	}
	if sc["state"] != "pending" || sc["ref"] == nil || sc["ref"] == "" {
		t.Errorf("approval_request result = %v, want a pending ref", sc)
	}
	pending, _ := app.approval.List(ctx, "pending")
	if len(pending) != 1 || pending[0].Class != "ticket" {
		t.Fatalf("want 1 pending ticket opened via the native tool, got %+v", pending)
	}
}

// TestApprovalPayloadTicketFields: the admin approvals list row carries the
// ticket wire fields for a ticket record and omits them for a hold.
func TestApprovalPayloadTicketFields(t *testing.T) {
	t.Parallel()
	grantExp := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	consumed := time.Date(2026, 7, 24, 10, 5, 0, 0, time.UTC)
	ticket := approval.Record{
		ID: "a1", State: approval.StateApproved, CreatedAt: grantExp, ExpiresAt: grantExp,
		Class: "ticket", GrantExpiresAt: &grantExp, ConsumedAt: &consumed, ConsumedBy: "sess-consumer",
	}
	raw, _ := json.Marshal(toApprovalPayload(ticket))
	for _, want := range []string{`"class":"ticket"`, `"grantExpiresAt":"2026-07-24T10:00:00Z"`,
		`"consumedAt":"2026-07-24T10:05:00Z"`, `"consumedBy":"sess-consumer"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("ticket payload missing %s\n got %s", want, raw)
		}
	}

	hold := approval.Record{ID: "a2", State: approval.StateApproved, CreatedAt: grantExp, ExpiresAt: grantExp}
	rawHold, _ := json.Marshal(toApprovalPayload(hold))
	for _, absent := range []string{"class", "grantExpiresAt", "consumedAt", "consumedBy"} {
		if strings.Contains(string(rawHold), absent) {
			t.Errorf("hold payload should omit %q\n got %s", absent, rawHold)
		}
	}
}

// TestApproverRowTicketFields: the mobile approver row carries the ticket wire
// fields (snake_case) for a ticket record and omits them for a hold.
func TestApproverRowTicketFields(t *testing.T) {
	t.Parallel()
	grantExp := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	consumed := time.Date(2026, 7, 24, 10, 5, 0, 0, time.UTC)

	var row approverRow
	applyApproverTicketFields(&row, approval.Record{
		Class: "ticket", GrantExpiresAt: &grantExp, ConsumedAt: &consumed, ConsumedBy: "sess-consumer",
	})
	raw, _ := json.Marshal(row)
	for _, want := range []string{`"class":"ticket"`, `"grant_expires_at":"2026-07-24T10:00:00Z"`,
		`"consumed_at":"2026-07-24T10:05:00Z"`, `"consumed_by":"sess-consumer"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("ticket approver row missing %s\n got %s", want, raw)
		}
	}

	var hold approverRow
	applyApproverTicketFields(&hold, approval.Record{})
	rawHold, _ := json.Marshal(hold)
	for _, absent := range []string{"class", "grant_expires_at", "consumed_at", "consumed_by"} {
		if strings.Contains(string(rawHold), absent) {
			t.Errorf("hold approver row should omit %q\n got %s", absent, rawHold)
		}
	}
}
