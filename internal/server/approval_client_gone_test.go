package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// useHookStore is a store whose approval use write first runs the armed
// hook, which stands in for a client that leaves while the write is in
// flight: the lookup has already answered and the write then commits.
type useHookStore struct {
	store.Store
	mu   sync.Mutex
	hook func()
}

func (s *useHookStore) arm(hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = hook
}

func (s *useHookStore) Approvals() store.ApprovalRepo {
	return useHookApprovals{s.Store.Approvals(), s}
}

type useHookApprovals struct {
	store.ApprovalRepo
	s *useHookStore
}

func (a useHookApprovals) MarkConsumed(ctx context.Context, id, consumedBy string, now, asOf time.Time) (bool, error) {
	a.s.mu.Lock()
	hook := a.s.hook
	a.s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return a.ApprovalRepo.MarkConsumed(ctx, id, consumedBy, now, asOf)
}

// testAppUseHook boots a full standalone strazad over a useHookStore, with
// loopback upstreams allowed so an in-process MCP server can be installed.
func testAppUseHook(t *testing.T) (*App, *useHookStore) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: "127.0.0.1:0"},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Apps:    config.Apps{HealthInterval: time.Hour, AllowLoopbackUpstreams: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
	}
	seedStoreTemplate(t, cfg)
	raw, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hs := &useHookStore{Store: raw}
	ctx, cancel := context.WithCancel(context.Background())
	app, err := build(ctx, cfg, logging.New(cfg.Log, io.Discard), hs)
	if err != nil {
		cancel()
		t.Fatalf("build: %v", err)
	}
	app.gateway.notify.SetDelay(0)
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Log("app did not stop in time")
		}
	})
	return app, hs
}

// echoRunsUpstream is an MCP server whose echo tool counts its runs.
func echoRunsUpstream(t *testing.T) (*gatewayUpstream, *atomic.Int32) {
	t.Helper()
	runs := &atomic.Int32{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "echo-runs", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct {
			Text string `json:"text"`
		}) (*mcp.CallToolResult, any, error) {
			runs.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo"}}}, nil, nil
		})
	up := &gatewayUpstream{Server: httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))}
	t.Cleanup(up.Close)
	return up, runs
}

// TestApprovalUseClientGone pins the case where the
// client of a gated call leaves while the use write of its approval is in
// flight, and the write commits. On both lanes and for every way a call uses
// an approval, the approval stays spent with its one consumed record, the
// call is refused with the sentence that names the approval, one Warn line
// says so, and on the gateway the tool never runs and the call's one record
// is a deny, not an allow.
func TestApprovalUseClientGone(t *testing.T) {
	t.Parallel()
	hold := "approve: {roles: [sec-approvers], timeoutSeconds: 60}"
	ticket := "approve: {class: ticket, roles: [sec-approvers]}"
	cases := []struct {
		name, lane, approve string
		held                bool // the call waits for a decision instead of using a seeded one
	}{
		{"gateway retry of an approved hold", "gateway", hold, false},
		{"gateway held call on the approval it waited for", "gateway", hold, true},
		{"gateway call on a ticket's grant", "gateway", ticket, false},
		{"hook retry of an approved hold", "hook", hold, false},
		{"hook call on a ticket's grant", "hook", ticket, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, hs := testAppUseHook(t)
			kim := seedGatewayUser(t, app, "kim", "dev")
			approver := seedApprover(t, app, "ada", "sec-approvers")
			log, buf := captureLogger()
			ctx, leave := context.WithCancel(ctxWithCapture(context.Background(), log, "use-gone"))
			defer leave()
			hs.arm(leave)
			claims := authn.Claims{Session: "sess-1", Subject: kim.ID}
			sub := policy.Subject{User: "kim", Roles: []string{"dev"}}

			var reason string
			var runs *atomic.Int32
			if tc.lane == "hook" {
				ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}
				d := policy.Decision{
					Effect: policy.EffectAllow, RuleID: "gated", SetName: "gone",
					Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 60, RetryTTLSeconds: 60},
				}
				if tc.approve == ticket {
					d.Approve = ticketDecision().Approve
					seedTicketGrant(t, app, kim.ID, "gated", ev)
				} else {
					seedHoldApproval(t, app, kim.ID, "gated", ev)
				}
				dec, _ := app.resolveApproveHook(ctx, claims, sub, ev, d)
				if dec.Effect != policy.EffectDeny {
					t.Errorf("hook effect = %s, want deny", dec.Effect)
				}
				reason = dec.Reason
			} else {
				reason, runs = callGatedEcho(t, app, ctx, claims, sub, tc.approve, tc.held, approver.ID)
			}

			spent, err := app.store.Approvals().List(context.Background(), "approved")
			if err != nil || len(spent) != 1 || spent[0].ConsumedAt == nil {
				t.Fatalf("approved rows = %+v, %v, want the one approval, spent", spent, err)
			}
			id := spent[0].ID
			want := fmt.Sprintf("Straza: approval %s was used for this call, but the AI agent's connection to Straza closed before the call could run, so the call did not run. The approval is spent, because one approval runs one call. Send the call again to ask for a new approval.", id)
			if reason != want {
				t.Errorf("reason = %q\nwant     %q", reason, want)
			}
			if uses := approvalUses(t, app, id); len(uses) != 1 || uses[0]["consumedBy"] != "sess-1" {
				t.Errorf("consumed records = %v, want one used by sess-1", uses)
			}
			var warns []string
			for _, line := range strings.Split(buf.String(), "\n") {
				if strings.Contains(line, "level=WARN") {
					warns = append(warns, line)
				}
			}
			if len(warns) != 1 || !strings.Contains(warns[0], "approval="+id) || !strings.Contains(warns[0], "lane="+tc.lane) ||
				!strings.Contains(warns[0], "correlation_id=use-gone") || len(errorRecords(buf)) != 0 {
				t.Errorf("want one Warn line naming the approval and the lane and no Error line:\n%s", buf.String())
			}
			if runs == nil {
				return
			}
			if n := runs.Load(); n != 0 {
				t.Errorf("the upstream tool ran %d times, want 0", n)
			}
			recs := awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["toolName"] == "echo" })
			if recs[0]["effect"] != "deny" || recs[0]["ruleId"] != "gated" || recs[0]["reason"] != want {
				t.Errorf("the call's record = %v, want a deny by gated with the sentence", recs[0])
			}
		})
	}
}

// callGatedEcho sends one echoapp__echo call under a rule with the given
// approve block straight into the gateway handler on ctx, and returns the
// text the client would have read and the upstream's run counter. A held
// call is approved by approverID while it waits; otherwise the approval is
// seeded before the call.
func callGatedEcho(t *testing.T, app *App, ctx context.Context, claims authn.Claims, sub policy.Subject, approve string, held bool, approverID string) (string, *atomic.Int32) {
	t.Helper()
	up, runs := echoRunsUpstream(t)
	installEchoApp(t, app, up, "echoapp", 0)
	if _, err := app.store.Policies().Create(context.Background(), store.PolicySet{
		Name: "gone", Status: "active", YAMLSource: fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: gone}
spec:
  match: {roles: [dev]}
  rules:
    - id: gated
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      %s
`, approve),
	}); err != nil {
		t.Fatal(err)
	}
	catRecompile(t, app)
	args := json.RawMessage(`{"text":"hi"}`)
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "echoapp", ToolName: "echo", Args: args}
	switch {
	case held:
	case strings.Contains(approve, "ticket"):
		seedTicketGrant(t, app, claims.Subject, "gated", ev)
	default:
		seedHoldApproval(t, app, claims.Subject, "gated", ev)
	}

	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echoapp__echo","arguments":{"text":"hi"}}}`
	var req rpcRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.handleToolCall(w, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)).WithContext(ctx), req, claims, sub)
	}()
	if held {
		id := onePending(t, app)
		if _, err := app.approval.Decide(context.Background(), id, "approved", approverID, "console", "", ""); err != nil {
			t.Fatalf("Decide: %v", err)
		}
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the gateway handler did not return")
	}
	var answer struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil || len(answer.Result.Content) != 1 {
		t.Fatalf("gateway answer = %s, want one tool error text", w.Body.String())
	}
	return answer.Result.Content[0].Text, runs
}
