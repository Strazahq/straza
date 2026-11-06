package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// holdGateYAML holds kim's deploy-prod commands for a person of
// sec-approvers to decide.
const holdGateYAML = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: hold-gate }
spec:
  priority: 100
  match: { roles: [dev] }
  rules:
    - id: deploy-held
      tools: [shell.exec]
      command: { allowPatterns: ["deploy-prod*"] }
      effect: allow
      mode: approve
      approve: { roles: [sec-approvers], timeoutSeconds: 120 }
`

// TestHookHoldDenialStands pins that a person's denial of a hold answers a
// retry of the same call on /v1/decide for the rest of the hold's decision
// window: a deny that names the person, the approval and the time the
// denial ends, and says to change the call or tell the user. The retry
// opens no new approval, so nobody is asked again, and its one tool record
// keeps that deny. A changed call is a new call and asks a person again.
func TestHookHoldDenialStands(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	ada := seedApprover(t, app, "ada", "sec-approvers")
	ctx := context.Background()
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{Name: "hold-gate", Status: "active", YAMLSource: holdGateYAML}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	token, session := checkinToken(t, app, base)
	shell := func(command string) map[string]any {
		return map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": command}
	}

	_, first := decide(t, base, token, shell("deploy-prod --all"))
	ref, _ := first["approvalId"].(string)
	if first["effect"] != "deny" || ref == "" {
		t.Fatalf("first call = %v, want a deny with an approval request", first)
	}
	denied, err := app.approval.Decide(ctx, ref, "denied", ada.ID, "console", "not during the freeze", "")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	standing := fmt.Sprintf("Straza: approval denied by ada (ref %s). A person denied this exact call, so it did not run, "+
		"and the same call stays denied until %s. Change the call, or tell the user that it was denied.",
		ref, denied.ExpiresAt.UTC().Format(time.RFC3339))

	cases := []struct {
		name, command string
		wantRef       bool // the answer names the denied approval, not a new one
	}{
		{"the same call again", "deploy-prod --all", true},
		{"a changed call", "deploy-prod --canary", false},
	}
	for _, tc := range cases {
		_, dec := decide(t, base, token, shell(tc.command))
		got, _ := dec["approvalId"].(string)
		reason, _ := dec["reason"].(string)
		switch {
		case dec["effect"] != "deny":
			t.Errorf("%s: effect = %v, want deny", tc.name, dec["effect"])
		case tc.wantRef && (got != ref || reason != standing):
			t.Errorf("%s: answer = %q (ref %q)\nwant %q (ref %q)", tc.name, reason, got, standing, ref)
		case !tc.wantRef && (got == "" || got == ref || reason == standing):
			t.Errorf("%s: answer = %q (ref %q), want a new approval request", tc.name, reason, got)
		}
	}

	rows, err := app.store.Approvals().List(ctx, "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("approvals = %d, %v, want the denied one and the changed call's", len(rows), err)
	}

	// Three decisions, three tool records. The retry's record is the one
	// that keeps the standing denial, with the person, the session, the
	// rule, the command and the deny.
	var records []map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for len(records) < 3 && time.Now().Before(deadline) {
		records = toolRecords(t, app)
		time.Sleep(20 * time.Millisecond)
	}
	var kept []map[string]any
	for _, r := range records {
		if r["reason"] == standing {
			kept = append(kept, r)
		}
	}
	if len(records) != 3 || len(kept) != 1 {
		t.Fatalf("tool records = %d, with the standing denial %d, want 3 and 1: %v", len(records), len(kept), records)
	}
	for field, want := range map[string]any{
		"user": kim.ID, "session": session, "effect": "deny", "ruleId": "deploy-held",
		"setName": "hold-gate", "command": "deploy-prod --all",
	} {
		if kept[0][field] != want {
			t.Errorf("record %s = %v, want %v", field, kept[0][field], want)
		}
	}
}

// toolRecords returns the data of every straza.audit.tool record in the
// outbox, published or not.
func toolRecords(t *testing.T, app *App) []map[string]any {
	t.Helper()
	evs, err := app.store.Outbox().ListRecent(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, e := range evs {
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if e.Subject == "straza.audit.tool" && json.Unmarshal([]byte(e.CE), &ce) == nil {
			out = append(out, ce.Data)
		}
	}
	return out
}

// holdLookupFaultStore is a store whose lookup of a hold's last decision
// fails, standing for a database that stops answering at that step alone.
type holdLookupFaultStore struct{ store.Store }

func (s holdLookupFaultStore) Approvals() store.ApprovalRepo {
	return holdLookupFaultApprovals{s.Store.Approvals()}
}

type holdLookupFaultApprovals struct{ store.ApprovalRepo }

func (holdLookupFaultApprovals) FindLatestHoldByKey(context.Context, string, string, string, string) (store.Approval, error) {
	return store.Approval{}, errors.New("db down")
}

// TestHookHoldDeniedLookupFault pins fail-closed on the hook lane when the
// lookup of a person's standing denial fails: the call is denied with the
// sentence that names the database and bounds the retry, it opens no
// approval, and the failure writes one record and one count.
func TestHookHoldDeniedLookupFault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: "127.0.0.1:0"},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
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
	ctx, cancel := context.WithCancel(context.Background())
	app, err := build(ctx, cfg, logging.New(cfg.Log, io.Discard), holdLookupFaultStore{raw})
	if err != nil {
		cancel()
		t.Fatalf("build: %v", err)
	}
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
	kim := seedIdentity(t, app)
	log, buf := captureLogger()
	rctx := ctxWithCapture(context.Background(), log, "hook-denied")
	d := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "deploy-held", SetName: "hold-gate",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 120, RetryTTLSeconds: 60},
	}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod --all"}

	dec, ref := app.resolveApproveHook(rctx, authn.Claims{Session: "sess-1", Subject: kim.ID}, policy.Subject{User: "kim"}, ev, d)
	if dec.Effect != policy.EffectDeny || dec.Reason != approvalStoreDownReason || ref != "" || dec.SetName != "hold-gate" {
		t.Errorf("decision = %+v (ref %q), want the fail-closed deny %q", dec, ref, approvalStoreDownReason)
	}
	rec := assertOneFailClosedRecord(t, buf, "hook", "hook-denied")
	if !strings.Contains(rec, "rule=deploy-held") || !strings.Contains(rec, "approval denied-hold-find: db down") {
		t.Errorf("fail-closed record lacks the rule or the cause: %s", rec)
	}
	if n := failClosedCounter(t, app, "hook"); n != 1 {
		t.Errorf("straza_failclosed_total{lane=hook} = %v, want 1", n)
	}
	if all, err := app.approval.List(context.Background(), ""); err != nil || len(all) != 0 {
		t.Errorf("approvals = %d, %v, want none", len(all), err)
	}
}
