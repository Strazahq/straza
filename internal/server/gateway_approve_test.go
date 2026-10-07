package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// fakeApprovalGate scripts the approval-service calls the gateway PEP makes and
// records the RequestInput it received, so resolveApprove can be driven without
// standing up the real service.
type fakeApprovalGate struct {
	requestErr   error
	await        approval.Record
	awaitErr     error
	awaitBlock   bool      // block until ctx is done, then return ctx.Err()
	reqExpiresAt time.Time // Request-minted record's window end (zero = past)

	// Ticket lane (class: ticket): ConsumeGrant scripts the DB-durable grant
	// lookup+consume; the gate must NEVER Await for a ticket. grantKey (when
	// set) narrows the hit to one fingerprint, like holdKey.
	grant    approval.Record
	grantHit bool
	grantErr error
	grantKey string

	// Terminal deny-final: DeniedTicketWithinWindow scripts the latest-ticket
	// lookup (a still-in-window denial blocks a fresh call).
	deniedFinal    approval.Record
	deniedFinalHit bool
	deniedFinalErr error

	// Hold lane: ConsumeHold scripts the retry's use of an approval (holdKey,
	// when set, narrows the hit to one fingerprint), and ConsumeHeld the held
	// call's use of the approval it waited for (won unless heldLost or
	// heldErr says otherwise).
	holdHit  bool
	holdKey  string
	holdErr  error
	heldLost bool
	heldErr  error

	reqs       int
	awaits     int // Await calls (a ticket path must never block ⇒ stays 0)
	consumes   int // ConsumeGrant calls
	holdUses   int // ConsumeHold calls
	heldUses   int // ConsumeHeld calls
	denyChecks int // DeniedTicketWithinWindow calls
	lastInput  approval.RequestInput
}

func (f *fakeApprovalGate) ConsumeHold(_ context.Context, _, _, _, argvHash string) (approval.Record, bool, error) {
	f.holdUses++
	if f.holdErr != nil {
		return approval.Record{}, false, f.holdErr
	}
	if f.holdHit && (f.holdKey == "" || f.holdKey == argvHash) {
		return approval.Record{State: approval.StateApproved}, true, nil
	}
	return approval.Record{}, false, nil
}

func (f *fakeApprovalGate) ConsumeHeld(_ context.Context, _ approval.Record) (bool, error) {
	f.heldUses++
	return !f.heldLost && f.heldErr == nil, f.heldErr
}

func (f *fakeApprovalGate) ConsumeGrant(_ context.Context, _, argvHash, _ string) (approval.Record, bool, error) {
	f.consumes++
	if f.grantKey != "" && f.grantKey != argvHash {
		return approval.Record{}, false, f.grantErr
	}
	return f.grant, f.grantHit, f.grantErr
}

func (f *fakeApprovalGate) DeniedTicketWithinWindow(_ context.Context, _, _, _ string) (approval.Record, bool, error) {
	f.denyChecks++
	return f.deniedFinal, f.deniedFinalHit, f.deniedFinalErr
}

func (f *fakeApprovalGate) Request(_ context.Context, in approval.RequestInput) (approval.Record, error) {
	f.reqs++
	f.lastInput = in
	if f.requestErr != nil {
		return approval.Record{}, f.requestErr
	}
	// reqExpiresAt (when set) stamps the minted record's decision-window end;
	// the hold-cap tests script a live window with it; zero keeps the
	// legacy shape (window already over).
	return approval.Record{ID: "rec-1", State: approval.StatePending, ExpiresAt: f.reqExpiresAt}, nil
}

func (f *fakeApprovalGate) Await(ctx context.Context, _ string) (approval.Record, error) {
	f.awaits++
	if f.awaitBlock {
		<-ctx.Done()
		return approval.Record{}, ctx.Err()
	}
	return f.await, f.awaitErr
}

func toolErrText(res *mcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

// TestGatewayResolveApprove pins the gateway approval gate: the
// exemption short-circuit, the Request/Await block, and each terminal verdict's
// verbatim reason. Hermetic: a fake gate stands in for the service.
func TestGatewayResolveApprove(t *testing.T) {
	// Serial: it lowers the package knob gatewayHoldCap for its run.
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}
	claims := authn.Claims{Session: "sess-1", Subject: "user-1"}
	sub := policy.Subject{User: "kim", Roles: []string{"agent"}}
	decision := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "needs-human", SetName: "iga",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
	}
	app := &App{}

	t.Run("a retry that uses an approval proceeds without a request", func(t *testing.T) {
		g := &fakeApprovalGate{holdHit: true}
		res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "why", nil)
		if !ok || res != "" {
			t.Fatalf("a used approval should proceed: ok=%v res=%v", ok, res)
		}
		if g.reqs != 0 {
			t.Errorf("a used approval must not create a request, got %d", g.reqs)
		}
	})

	t.Run("request carries the contract fields", func(t *testing.T) {
		g := &fakeApprovalGate{await: approval.Record{ID: "rec-1", State: approval.StateApproved}}
		_, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "offboard the leaver", nil)
		if !ok {
			t.Fatal("approved should proceed")
		}
		in := g.lastInput
		if in.Lane != "gateway" {
			t.Errorf("lane = %q, want gateway", in.Lane)
		}
		if in.Summary != "mcp.call midpoint:disable_user" {
			t.Errorf("summary = %q", in.Summary)
		}
		if in.Justification != "offboard the leaver" {
			t.Errorf("justification = %q", in.Justification)
		}
		// Mint key is the v2 fingerprint and nothing else rides: the v1
		// candidate is no longer produced. This unit event carries nil Args
		// (unobserved) so the v2 key honestly tool-scopes.
		wantKey, err := approval.EventKeyV2(ev, decision.Approve.Binding)
		if err != nil {
			t.Fatal(err)
		}
		if in.ArgvHash != wantKey {
			t.Errorf("argvHash = %q, want the v2 mint key %q", in.ArgvHash, wantKey)
		}
		if in.SessionID != "sess-1" || in.UserID != "user-1" || in.Username != "kim" {
			t.Errorf("identity = %+v", in)
		}
		if in.RuleID != "needs-human" || in.SetName != "iga" || in.Spec.TimeoutSeconds != 90 {
			t.Errorf("rule/spec = %+v", in)
		}
	})

	t.Run("service error fails closed", func(t *testing.T) {
		g := &fakeApprovalGate{requestErr: fmt.Errorf("db down")}
		// The per-request logger rides the context exactly as requestID binds
		// it: the one fail-closed record lands in the capture.
		log, buf := captureLogger()
		ctx := ctxWithCapture(context.Background(), log, "gw-corr-1")
		res, ok := app.resolveApprove(ctx, g, claims, sub, ev, decision, "", nil)
		if ok {
			t.Fatal("service error must not proceed")
		}
		if got := res; got != "Straza: approval service error. Denied (fail-closed)" {
			t.Errorf("service-error reason = %q", got)
		}
		rec := assertOneFailClosedRecord(t, buf, "gateway", "gw-corr-1")
		if !strings.Contains(rec, "rule=needs-human") || !strings.Contains(rec, `cause="db down"`) {
			t.Errorf("fail-closed record lacks rule/cause: %s", rec)
		}
	})

	t.Run("approved proceeds", func(t *testing.T) {
		g := &fakeApprovalGate{await: approval.Record{ID: "rec-1", State: approval.StateApproved}}
		if res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "", nil); !ok || res != "" {
			t.Fatalf("approved should proceed: ok=%v res=%v", ok, res)
		}
	})

	t.Run("denied returns the denied-by reason", func(t *testing.T) {
		g := &fakeApprovalGate{await: approval.Record{ID: "rec-1", State: approval.StateDenied, DecidedByName: "Kim Decider"}}
		res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "", nil)
		if ok {
			t.Fatal("denied must not proceed")
		}
		if got := res; got != "Straza: approval denied by Kim Decider (ref rec-1)" {
			t.Errorf("denied reason = %q", got)
		}
	})

	t.Run("expired returns the timeout reason", func(t *testing.T) {
		g := &fakeApprovalGate{await: approval.Record{ID: "rec-1", State: approval.StateExpired}}
		res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "", nil)
		if ok {
			t.Fatal("expired must not proceed")
		}
		if got := res; got != "Straza: approval request expired after 90s with no decision (ref rec-1)" {
			t.Errorf("expired reason = %q", got)
		}
	})

	t.Run("await deadline collapses to timeout", func(t *testing.T) {
		g := &fakeApprovalGate{awaitBlock: true}
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the request context is already gone (client disconnect)
		res, ok := app.resolveApprove(ctx, g, claims, sub, ev, decision, "", nil)
		if ok {
			t.Fatal("a blown wait must not proceed")
		}
		if got := res; !strings.Contains(got, "expired after 90s") || !strings.Contains(got, "ref rec-1") {
			t.Errorf("timeout reason = %q", got)
		}
	})

	t.Run("hold cap answers pending before the client deadline", func(t *testing.T) {
		// A live decision window outlasting the hold cap must come back as a
		// STRUCTURED pending reason quickly, never a socket held to the client's
		// transport deadline, an ambiguity a model can turn into fabricated tool
		// output.
		old := gatewayHoldCap
		gatewayHoldCap = 50 * time.Millisecond
		t.Cleanup(func() { gatewayHoldCap = old })
		g := &fakeApprovalGate{awaitBlock: true, reqExpiresAt: time.Now().Add(60 * time.Second)}
		start := time.Now()
		res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "", nil)
		if ok {
			t.Fatal("a pending approval must not proceed")
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("capped hold took %s; the cap did not bound the wait", time.Since(start))
		}
		got := res
		if !strings.Contains(got, "approval pending (ref rec-1)") ||
			!strings.Contains(got, "left in the decision window") ||
			!strings.Contains(got, "a person decides on the self-service page (/self-service/), the console, or an enrolled phone") {
			t.Errorf("pending reason = %q", got)
		}
		// Await-hidden arm: a bare app has no snapshot engine, so the native
		// await tool is invisible and the pending reason must not name it.
		if strings.Contains(got, "straza__approval_await") {
			t.Errorf("await tool named while policy hides it: %q", got)
		}
	})

	t.Run("capped hold names the await tool only when policy exposes it", func(t *testing.T) {
		old := gatewayHoldCap
		gatewayHoldCap = 50 * time.Millisecond
		t.Cleanup(func() { gatewayHoldCap = old })
		vapp, base := testApp(t)
		ctx := context.Background()
		if _, err := vapp.store.Policies().Create(ctx, store.PolicySet{
			Name: "allow-await", Status: "active", YAMLSource: awaitVisiblePolicy,
		}); err != nil {
			t.Fatalf("create policy: %v", err)
		}
		if _, err := vapp.snapshots.Recompile(ctx); err != nil {
			t.Fatalf("recompile: %v", err)
		}
		g := &fakeApprovalGate{awaitBlock: true, reqExpiresAt: time.Now().Add(60 * time.Second)}
		subDev := policy.Subject{User: "kim", Roles: []string{"dev"}}
		res, ok := vapp.resolveApprove(ctx, g, claims, subDev, ev, decision, "", nil)
		if ok {
			t.Fatal("a pending approval must not proceed")
		}
		got := res
		if !strings.Contains(got, "a person decides on the self-service page ("+base+"/self-service/), the console, or an enrolled phone") ||
			!strings.Contains(got, "Wait for the decision with the straza__approval_await tool, then retry the call") {
			t.Errorf("await-visible pending reason = %q", got)
		}
	})

	t.Run("capped hold on an elapsed window stays the expired reason", func(t *testing.T) {
		// Same capped exit, but the decision window itself is over (zero
		// ExpiresAt = past): the final fail-closed timeout wording holds, so
		// a model never sees "pending" for an approval no one can decide.
		old := gatewayHoldCap
		gatewayHoldCap = 50 * time.Millisecond
		t.Cleanup(func() { gatewayHoldCap = old })
		g := &fakeApprovalGate{awaitBlock: true}
		res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "", nil)
		if ok {
			t.Fatal("an expired approval must not proceed")
		}
		if got := res; !strings.Contains(got, "expired after 90s with no decision (ref rec-1)") {
			t.Errorf("expired reason = %q", got)
		}
	})
}

// TestStripJustification pins the justification extract/strip:
// the injected key is removed and returned, other args survive verbatim, and
// non-object or absent inputs pass through with an empty justification.
func TestStripJustification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		wantArgs string // "" = expect the input unchanged
		wantJust string
	}{
		{"extract and strip", `{"text":"hi","_straza_justification":"because"}`, `{"text":"hi"}`, "because"},
		{"only justification", `{"_straza_justification":"why"}`, `{}`, "why"},
		{"absent key passes through", `{"text":"hi"}`, "", ""},
		{"empty args", ``, "", ""},
		{"non-object passes through", `["a","b"]`, "", ""},
		{"non-string justification ignored", `{"text":"hi","_straza_justification":42}`, `{"text":"hi"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs, gotJust := stripJustification(json.RawMessage(tc.in))
			if gotJust != tc.wantJust {
				t.Errorf("justification = %q, want %q", gotJust, tc.wantJust)
			}
			want := tc.wantArgs
			if want == "" {
				want = tc.in
			}
			if !jsonEqual(t, string(gotArgs), want) {
				t.Errorf("args = %s, want %s", gotArgs, want)
			}
			if strings.Contains(string(gotArgs), justificationField) {
				t.Errorf("cleaned args still carry the injected field: %s", gotArgs)
			}
		})
	}
}

// TestInjectJustification pins the schema deep-copy: the field is
// added as a required string, the source schema is never mutated, and a
// nil/non-object schema yields a minimal object schema.
func TestInjectJustification(t *testing.T) {
	t.Parallel()
	t.Run("adds required field, source untouched", func(t *testing.T) {
		src := map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
			"required":   []any{"text"},
		}
		out := injectJustification(src).(map[string]any)

		props := out["properties"].(map[string]any)
		field, ok := props[justificationField].(map[string]any)
		if !ok || field["type"] != "string" || field["description"] != justificationDescription {
			t.Fatalf("injected field = %v", props[justificationField])
		}
		if !containsStr(stringSlice(out["required"]), justificationField) {
			t.Errorf("required missing the field: %v", out["required"])
		}
		if !containsStr(stringSlice(out["required"]), "text") {
			t.Errorf("existing required entry dropped: %v", out["required"])
		}
		// The source must be pristine (deep copy, not an in-place mutation).
		if _, mutated := src["properties"].(map[string]any)[justificationField]; mutated {
			t.Error("source schema properties were mutated")
		}
		if containsStr(stringSlice(src["required"]), justificationField) {
			t.Error("source schema required was mutated")
		}
	})

	t.Run("nil schema yields a minimal object schema", func(t *testing.T) {
		out := injectJustification(nil).(map[string]any)
		if out["type"] != "object" {
			t.Errorf("type = %v, want object", out["type"])
		}
		props := out["properties"].(map[string]any)
		if _, ok := props[justificationField]; !ok {
			t.Errorf("missing injected field: %v", props)
		}
		if !containsStr(stringSlice(out["required"]), justificationField) {
			t.Errorf("required = %v", out["required"])
		}
	})
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		return a == b
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		return false
	}
	ab, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return string(ab) == string(bb)
}

// TestGatewayJustificationInjection is the engine-driven catalog pin: a
// mode:approve tool's catalog schema gains the required
// `_straza_justification` field; a plain-allow tool's schema does not (and the
// catalog build never touches the approval service; buildCatalog only probes
// the engine). Rides the real boot path so the snapshot-aware cache key and the
// per-tool engine probe are exercised end to end.
func TestGatewayJustificationInjection(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()

	seedGatewayUser(t, app, "kim", "dev")
	manifest := fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: echoapp}
server: {name: straza.test/echo, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
`, up.URL)
	mf, err := managerParse(t, manifest)
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
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: row.ID, ToolMatcher: `["*"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}

	// echo is approval-gated; env is a plain allow.
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "mcp-approve", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: mcp-approve}
spec:
  match: {roles: [dev]}
  rules:
    - id: approve-echo
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
    - id: allow-env
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["env"]}
      effect: allow
`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := app.manager.View("echoapp"); ok && v.Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("echoapp not running")
		}
		time.Sleep(20 * time.Millisecond)
	}

	tok := sessionToken(t, base, "kim")
	_, list, _ := mcpCall(t, base, tok, "tools/list", nil)
	schemas := schemasByName(t, list)

	// Gated tool: the required justification field is injected.
	echo := schemas["echoapp__echo"]
	if echo == nil {
		t.Fatalf("echo tool missing from catalog: %v", schemas)
	}
	props, _ := echo["properties"].(map[string]any)
	if _, ok := props[justificationField]; !ok {
		t.Errorf("approval-gated echo schema missing %s: %v", justificationField, echo)
	}
	if !hasRequired(echo, justificationField) {
		t.Errorf("approval-gated echo schema does not require %s: %v", justificationField, echo["required"])
	}

	// Plain tool: byte-clean, no injection.
	env := schemas["echoapp__env"]
	if env == nil {
		t.Fatalf("env tool missing from catalog: %v", schemas)
	}
	if envProps, _ := env["properties"].(map[string]any); envProps != nil {
		if _, ok := envProps[justificationField]; ok {
			t.Errorf("plain env schema was injected with %s: %v", justificationField, env)
		}
	}
	if hasRequired(env, justificationField) {
		t.Errorf("plain env schema requires %s: %v", justificationField, env["required"])
	}
}

func schemasByName(t *testing.T, res map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	result, _ := res["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		name, _ := m["name"].(string)
		schema, _ := m["inputSchema"].(map[string]any)
		out[name] = schema
	}
	return out
}

func hasRequired(schema map[string]any, want string) bool {
	req, _ := schema["required"].([]any)
	for _, r := range req {
		if s, _ := r.(string); s == want {
			return true
		}
	}
	return false
}

// TestGatewayApproveFingerprintV2 pins the fold that fixes the biggest
// approval gap: the gateway fingerprint binds the OBSERVED call arguments
// under binding:call (the default), collapses wire noise, honors the explicit
// binding:tool opt-out, and denies outright when the args are too ambiguous to
// fingerprint.
func TestGatewayApproveFingerprintV2(t *testing.T) {
	t.Parallel()
	claims := authn.Claims{Session: "sess-1", Subject: "user-1"}
	sub := policy.Subject{User: "kim", Roles: []string{"agent"}}
	mkEv := func(args string) policy.Event {
		ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}
		if args != "" {
			ev.Args = json.RawMessage(args)
		}
		return ev
	}
	mkDecision := func(binding string) policy.Decision {
		return policy.Decision{
			Effect: policy.EffectAllow, RuleID: "needs-human", SetName: "iga",
			Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 1, RetryTTLSeconds: 60, Binding: binding},
		}
	}
	app := &App{}
	mint := func(t *testing.T, ev policy.Event, binding string) string {
		t.Helper()
		g := &fakeApprovalGate{await: approval.Record{ID: "rec-1", State: approval.StateApproved}}
		if _, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, mkDecision(binding), "", ev.Args); !ok {
			t.Fatal("approved should proceed")
		}
		return g.lastInput.ArgvHash
	}

	t.Run("different args mint different keys", func(t *testing.T) {
		a := mint(t, mkEv(`{"user":"temp-x"}`), policy.ApproveBindingCall)
		b := mint(t, mkEv(`{"user":"ceo"}`), policy.ApproveBindingCall)
		if a == b {
			t.Fatalf("an approval for temp-x would cover ceo: both minted %q", a)
		}
		if !strings.HasPrefix(a, "v2:call:sha256:") {
			t.Errorf("mint key %q not call-scoped v2", a)
		}
	})

	t.Run("wire noise mints one key", func(t *testing.T) {
		a := mint(t, mkEv(`{"force":true,"user":"bob"}`), policy.ApproveBindingCall)
		b := mint(t, mkEv("{ \"user\" : \"bob\" ,\n\"force\" : true }"), policy.ApproveBindingCall)
		if a != b {
			t.Errorf("key-order/whitespace noise split keys: %q vs %q", a, b)
		}
	})

	t.Run("binding tool collapses args", func(t *testing.T) {
		a := mint(t, mkEv(`{"user":"temp-x"}`), policy.ApproveBindingTool)
		b := mint(t, mkEv(`{"user":"ceo"}`), policy.ApproveBindingTool)
		if a != b {
			t.Errorf("binding:tool split on args: %q vs %q", a, b)
		}
		if !strings.HasPrefix(a, "v2:tool:sha256:") {
			t.Errorf("mint key %q not tool-scoped v2", a)
		}
	})

	t.Run("ambiguous args deny fail-closed", func(t *testing.T) {
		ev := mkEv(`{"user":"bob","user":"ceo"}`)
		g := &fakeApprovalGate{}
		res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, mkDecision(policy.ApproveBindingCall), "", ev.Args)
		if ok {
			t.Fatal("duplicate-key args must not proceed")
		}
		if got := res; !strings.Contains(got, "approval fingerprint unavailable") {
			t.Errorf("reason = %q, want fingerprint-unavailable", got)
		}
		if g.reqs != 0 || g.consumes != 0 {
			t.Errorf("ambiguous args must not touch the gate (reqs=%d consumes=%d)", g.reqs, g.consumes)
		}
	})
}

// TestGatewayApproveV2KeyOnly pins the closed fingerprint drain on the gateway
// lane: only the v2 key resolves a hold's approval or a ticket grant, a record
// keyed by v1 alone opens a fresh request, every lookup runs once, and a
// lookup error still denies fail-closed.
func TestGatewayApproveV2KeyOnly(t *testing.T) {
	t.Parallel()
	claims := authn.Claims{Session: "sess-1", Subject: "user-1"}
	sub := policy.Subject{User: "kim", Roles: []string{"agent"}}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: json.RawMessage(`{"user":"bob"}`)}
	v2, err := approval.EventKeyV2(ev, policy.ApproveBindingCall)
	if err != nil {
		t.Fatal(err)
	}
	v1 := "sha256:stale-v1-key"
	hold := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "needs-human", SetName: "iga",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 1, RetryTTLSeconds: 60},
	}
	ticket := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "needs-human", SetName: "iga",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, Class: policy.ClassTicket, TicketTTLSeconds: 86400, GrantTTLSeconds: 3600},
	}
	app := &App{}
	cases := []struct {
		name           string
		decision       policy.Decision
		gate           *fakeApprovalGate
		wantProceed    bool
		wantReqs       int
		wantConsumes   int
		wantDenyChecks int
		wantReason     string
	}{
		{"a v2 approval resolves the hold", hold,
			&fakeApprovalGate{holdHit: true, holdKey: v2}, true, 0, 0, 0, ""},
		{"a v1 approval no longer resolves the hold", hold,
			&fakeApprovalGate{holdHit: true, holdKey: v1, await: approval.Record{ID: "rec-1", State: approval.StateExpired}},
			false, 1, 0, 0, "Straza: approval request expired after 1s with no decision (ref rec-1)"},
		{"v2 grant resolves the ticket", ticket,
			&fakeApprovalGate{grantHit: true, grantKey: v2, grant: approval.Record{ID: "t-1", State: approval.StateApproved}}, true, 0, 1, 0, ""},
		{"v1 grant no longer resolves the ticket", ticket,
			&fakeApprovalGate{grantHit: true, grantKey: v1, grant: approval.Record{ID: "t-1", State: approval.StateApproved}},
			false, 1, 1, 1, "Straza: approval ticket rec-1 is pending"},
		{"grant lookup error denies fail-closed", ticket,
			&fakeApprovalGate{grantErr: context.DeadlineExceeded}, false, 0, 1, 0, approvalStoreDownReason},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, ok := app.resolveApprove(context.Background(), tc.gate, claims, sub, ev, tc.decision, "", ev.Args)
			if ok != tc.wantProceed {
				t.Fatalf("proceed = %v, want %v (reason %q)", ok, tc.wantProceed, res)
			}
			if tc.gate.reqs != tc.wantReqs || tc.gate.consumes != tc.wantConsumes || tc.gate.denyChecks != tc.wantDenyChecks {
				t.Errorf("requests = %d, grant lookups = %d, deny-final lookups = %d, want %d, %d and %d",
					tc.gate.reqs, tc.gate.consumes, tc.gate.denyChecks, tc.wantReqs, tc.wantConsumes, tc.wantDenyChecks)
			}
			if got := res; !strings.Contains(got, tc.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", got, tc.wantReason)
			}
		})
	}
}

// approval.gatewayHoldSeconds (operator knob): config can lengthen or
// shorten the socket hold; the rule's decision window is always the ceiling.
func TestGatewayHoldConfigurable(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	for _, tc := range []struct {
		name   string
		cfg    int
		window time.Duration
		want   time.Duration
	}{
		{"default caps at 120s", 0, 300 * time.Second, 120 * time.Second},
		{"config raises past the default", 240, 300 * time.Second, 240 * time.Second},
		{"config shortens", 5, 120 * time.Second, 5 * time.Second},
		{"window stays the ceiling", 300, 90 * time.Second, 90 * time.Second},
		{"short window under default", 0, 8 * time.Second, 8 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app.cfg.Approval.GatewayHoldSeconds = tc.cfg
			if got := app.gatewayHold(tc.window); got != tc.want {
				t.Errorf("gatewayHold(%v) with cfg %d = %v, want %v", tc.window, tc.cfg, got, tc.want)
			}
		})
	}
}

// TestGatewayApproveOutcome pins what gateApprove tells a server endpoint
// beyond the sentence: a hold that outlives its wait and an open ticket are
// pending with the approval's ref and window end, a denied hold names its
// ref, and with awaitListed false the pending sentence never names
// straza__approval_await, even when policy exposes it.
func TestGatewayApproveOutcome(t *testing.T) {
	// Serial: it lowers the package knob gatewayHoldCap for its run.
	old := gatewayHoldCap
	gatewayHoldCap = 50 * time.Millisecond
	t.Cleanup(func() { gatewayHoldCap = old })
	vapp, _ := testApp(t)
	ctx := context.Background()
	if _, err := vapp.store.Policies().Create(ctx, store.PolicySet{Name: "allow-await", Status: "active", YAMLSource: awaitVisiblePolicy}); err != nil {
		t.Fatal(err)
	}
	if _, err := vapp.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}
	claims := authn.Claims{Session: "sess-1", Subject: "user-1"}
	sub := policy.Subject{User: "kim", Roles: []string{"dev"}}
	hold := policy.Decision{Effect: policy.EffectAllow, RuleID: "needs-human", SetName: "iga",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}}
	ticket := policy.Decision{Effect: policy.EffectAllow, RuleID: "rotate-ticket", SetName: "change",
		Approve: &policy.ApproveSpec{Class: policy.ClassTicket, TicketTTLSeconds: 86400, GrantTTLSeconds: 3600, Bind: policy.BindFingerprint}}
	window := time.Now().Add(60 * time.Second)

	rows := []struct {
		name        string
		gate        *fakeApprovalGate
		d           policy.Decision
		awaitListed bool
		wantPending bool
		wantRef     string
		wantAwait   bool
	}{
		{"pending hold on /mcp names the await tool", &fakeApprovalGate{awaitBlock: true, reqExpiresAt: window}, hold, true, true, "rec-1", true},
		{"pending hold on a server endpoint does not", &fakeApprovalGate{awaitBlock: true, reqExpiresAt: window}, hold, false, true, "rec-1", false},
		{"open ticket on /mcp names the await tool", &fakeApprovalGate{reqExpiresAt: window}, ticket, true, true, "rec-1", true},
		{"open ticket on a server endpoint does not", &fakeApprovalGate{reqExpiresAt: window}, ticket, false, true, "rec-1", false},
		{"denied hold", &fakeApprovalGate{await: approval.Record{ID: "rec-1", State: approval.StateDenied, DecidedByName: "Ada"}}, hold, false, false, "rec-1", false},
		{"store down", &fakeApprovalGate{holdErr: fmt.Errorf("db down")}, hold, false, false, "", false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			o := vapp.gateApprove(ctx, row.gate, claims, sub, ev, row.d, "", nil, row.awaitListed)
			if o.run || o.reason == "" {
				t.Fatalf("outcome = %+v, want a refusal", o)
			}
			if o.pending != row.wantPending || o.ref != row.wantRef {
				t.Errorf("outcome pending %v ref %q, want %v %q", o.pending, o.ref, row.wantPending, row.wantRef)
			}
			if row.wantPending && !o.expiresAt.Equal(window) {
				t.Errorf("expiresAt = %v, want the window end %v", o.expiresAt, window)
			}
			if named := strings.Contains(o.reason, "straza__approval_await"); named != row.wantAwait {
				t.Errorf("reason names the await tool = %v, want %v: %q", named, row.wantAwait, o.reason)
			}
		})
	}
}
