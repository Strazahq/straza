package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// apiClassifier scripts a classifier verdict and counts its calls. A delay
// waits on the context, so a blown bound surfaces as the context's error.
type apiClassifier struct {
	calls   atomic.Int32
	verdict policy.ClassifyVerdict
	err     error
	delay   time.Duration
}

func (f *apiClassifier) Classify(ctx context.Context, _ policy.Event) (policy.ClassifyVerdict, error) {
	f.calls.Add(1)
	if !hold(ctx, f.delay) {
		return policy.ClassifyVerdict{}, ctx.Err()
	}
	return f.verdict, f.err
}

// verdictOf is a double's /v1/decide that answers v.
func verdictOf(v agentguard.DecideResponse) func(context.Context, policy.Event) (DecideOutcome, error) {
	return func(context.Context, policy.Event) (DecideOutcome, error) {
		return DecideOutcome{Answer: v, Status: 200, Correlation: "req-1"}, nil
	}
}

// refusedBy is a double's /v1/decide that strazad refused with status and
// msg, after the bounce when bounced is set.
func refusedBy(status int, msg string) func(context.Context, policy.Event) (DecideOutcome, error) {
	return func(context.Context, policy.Event) (DecideOutcome, error) {
		return DecideOutcome{Status: status, Correlation: "req-2", Bounced: true},
			fmt.Errorf("/v1/decide: %w", &agentguard.StatusError{Status: status, Msg: msg})
	}
}

// escalateWith runs the escalation lanes on d as the decide route would.
func escalateWith(a *api, ev policy.Event, d policy.Decision) (policy.Decision, bool, localapi.Journal) {
	var j localapi.Journal
	req := localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code"}, Event: ev}
	out, recorded := a.escalate(context.Background(), liveSession(), req, d, &j)
	return out, recorded, j
}

// TestDecideServerCheckLane pins the online gate: a serverCheck allow asks
// /v1/decide, strazad's verdict wins as it is, any failure to reach it
// denies, and a plain allow or an observational event never asks.
func TestDecideServerCheckLane(t *testing.T) {
	doc := `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: online-gate}
spec:
  match: {roles: [dev]}
  rules:
    - id: gated-fetch
      tools: [net.fetch]
      effect: allow
      mode: serverCheck
      reason: "online gate"
`
	fetch := localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code"}, AgentType: "researcher", AgentID: "a-1",
		Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolNetFetch, Paths: []string{"https://x.io"}}}
	double := func() *apiDouble {
		f := gateDouble(t, 900, time.Hour, Renewal{})
		f.engine = apiEngine(t, doc, 900)
		return f
	}
	for _, v := range []agentguard.DecideResponse{
		{Effect: policy.EffectDeny, RuleID: "central-veto", Reason: "Straza: vetoed centrally"},
		{Effect: policy.EffectAllow, RuleID: "central-ok"},
	} {
		f := double()
		f.decide = verdictOf(v)
		a, _ := testAPI(f, false)
		ans := decideVia(t, a, fetch)
		if d := ans.Decision; d.Effect != v.Effect || d.RuleID != v.RuleID || d.Reason != v.Reason {
			t.Errorf("verdict %s: got %+v", v.Effect, d)
		}
		if ans.Journal.Escalation != "server" || ans.Journal.Status != 200 || ans.Journal.Correlation != "req-1" {
			t.Errorf("journal = %+v, want the server lane with strazad's status and correlation", ans.Journal)
		}
		if c := f.decideCalls; len(c) != 1 || c[0].agentType != "researcher" || c[0].agentID != "a-1" {
			t.Errorf("decide calls = %+v, want one with the delegate tag", c)
		}
	}
	t.Run("unreachable server denies", func(t *testing.T) {
		a, _ := testAPI(double(), false)
		ans := decideVia(t, a, fetch)
		if d := ans.Decision; d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, "unreachable") || !strings.Contains(d.Reason, "doctor") {
			t.Fatalf("offline serverCheck = %+v, want the actionable unreachable deny", d)
		}
		if j := ans.Journal; j.Escalation != "server" || !j.FailClosed || j.Status != 0 || j.Correlation != "" || !j.Spooled {
			t.Errorf("journal = %+v, want the server lane, fail-closed, no answer and the deny recorded", j)
		}
	})
	t.Run("after a 401 bounce the retried answer is journaled", func(t *testing.T) {
		f := double()
		f.decide = func(context.Context, policy.Event) (DecideOutcome, error) {
			return DecideOutcome{Answer: agentguard.DecideResponse{Effect: policy.EffectAllow, RuleID: "central-ok"},
				Status: 200, Correlation: "corr-decide-2", Bounced: true}, nil
		}
		a, _ := testAPI(f, false)
		ans := decideVia(t, a, fetch)
		if j := ans.Journal; ans.Decision.Effect != policy.EffectAllow || j.Status != 200 || j.Correlation != "corr-decide-2" || j.FailClosed || j.Spooled {
			t.Errorf("answer = %+v, want the retried allow with its correlation and no record of the service's own", ans)
		}
	})
	t.Run("server error denies", func(t *testing.T) {
		f := double()
		f.decide = refusedBy(500, "HTTP 500")
		a, _ := testAPI(f, false)
		if d := decideVia(t, a, fetch).Decision; d.Effect != policy.EffectDeny {
			t.Fatalf("500 from /v1/decide allowed: %+v", d)
		}
	})
	t.Run("plain allow and observational events never ask", func(t *testing.T) {
		f := double()
		a, _ := testAPI(f, false)
		if d := decideVia(t, a, shellReq("git status")).Decision; d.Effect != policy.EffectAllow {
			t.Fatalf("default allow expected: %+v", d)
		}
		end := shellReq("")
		end.Event = policy.Event{Kind: policy.EventSessionEnd}
		if d := decideVia(t, a, end).Decision; d.Effect != policy.EffectAllow || !d.Default {
			t.Fatalf("observational event: %+v", d)
		}
		if len(f.decideCalls) != 0 {
			t.Errorf("the fast path asked strazad %d times", len(f.decideCalls))
		}
	})
}

// TestDecideByHarnessToolName pins the hop of a classified tool's own name:
// the service decides by it locally and forwards it to strazad with the
// event, and a request without it decides by the lane alone. A decision the
// service makes itself leaves exactly one record, and an unclassified
// tool's record names the tool.
func TestDecideByHarnessToolName(t *testing.T) {
	doc := `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: by-name}
spec:
  rules:
    - id: deny-monitor
      tools: [other]
      toolNames: {deny: [Monitor]}
      reason: "Monitor is off"
    - id: gated-handback
      tools: [other]
      toolNames: {allow: [SubagentHandback]}
      mode: serverCheck
`
	spawn := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolTaskSpawn}
	rows := []struct {
		name, native, wantRule string
		event                  policy.Event
		wantEffect             string
		wantForward            bool
		wantToolName           string
	}{
		{"named deny decides locally", "Monitor", "deny-monitor", spawn, policy.EffectDeny, false, ""},
		{"named gate forwards the name", "SubagentHandback", "central-ok", spawn, policy.EffectAllow, true, ""},
		{"no name keeps the lane", "", "", spawn, policy.EffectAllow, false, ""},
		{"an unclassified tool is recorded by name", "", "",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "FutureTool"}, policy.EffectAllow, false, "FutureTool"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := gateDouble(t, 900, time.Hour, Renewal{})
			f.engine = apiEngine(t, doc, 900)
			f.decide = verdictOf(agentguard.DecideResponse{Effect: policy.EffectAllow, RuleID: "central-ok"})
			a, _ := testAPI(f, false)
			d := decideVia(t, a, localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code"}, NativeTool: row.native, Event: row.event}).Decision
			if d.Effect != row.wantEffect || d.RuleID != row.wantRule {
				t.Fatalf("decision = %+v, want %s by %q", d, row.wantEffect, row.wantRule)
			}
			switch c := f.decideCalls; {
			case row.wantForward && (len(c) != 1 || c[0].event.NativeTool != row.native):
				t.Errorf("decide calls = %+v, want one carrying %q", c, row.native)
			case !row.wantForward && len(c) != 0:
				t.Errorf("decide calls = %+v, want none", c)
			}
			if row.wantForward {
				return // strazad records the verdict it gave
			}
			recs := f.recorded()
			if len(recs) != 1 {
				t.Fatalf("%d records, want exactly one", len(recs))
			}
			if r := recs[0].data; recs[0].ceType != "straza.audit.tool" || r["session"] != "s-1" || r["tool"] != row.event.Tool ||
				r["effect"] != d.Effect || r["ruleId"] != d.RuleID || r["reason"] != d.Reason || r["toolName"] != nonEmpty(row.wantToolName) {
				t.Errorf("record = %s %+v, want the decision with toolName %q", recs[0].ceType, r, row.wantToolName)
			}
		})
	}
}

// TestDecideApproveLane pins mode approve: the service never blocks,
// strazad's verdict passes through as it is, an unreachable server denies
// with the long-pinned sentence, approve wins over serverCheck, classify
// runs first, and a decision without the marker never asks.
func TestDecideApproveLane(t *testing.T) {
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}
	approved := policy.Decision{Effect: policy.EffectAllow, RuleID: "needs-human", Approve: &policy.ApproveSpec{TimeoutSeconds: 90, RetryTTLSeconds: 60}}
	t.Run("server verdict passes through verbatim", func(t *testing.T) {
		for _, v := range []agentguard.DecideResponse{
			{Effect: policy.EffectDeny, RuleID: "needs-human", Reason: "Straza: approval requested (notified roles [sec-approvers]); retry after approval (ref abc123)", ApprovalID: "abc123"},
			{Effect: policy.EffectAllow, RuleID: "needs-human", Reason: "Straza: approved by kim (ref abc123)", ApprovalID: "abc123"},
		} {
			a, _ := testAPI(&apiDouble{decide: verdictOf(v)}, false)
			d, recorded, j := escalateWith(a, ev, approved)
			if d.Effect != v.Effect || d.RuleID != v.RuleID || d.Reason != v.Reason || !recorded || j.Escalation != "approve" {
				t.Errorf("verdict %s: got %+v recorded %v journal %+v", v.Effect, d, recorded, j)
			}
		}
	})
	t.Run("unreachable server denies fail-closed", func(t *testing.T) {
		a, _ := testAPI(&apiDouble{}, false)
		d, recorded, j := escalateWith(a, ev, approved)
		if d.Effect != policy.EffectDeny || d.RuleID != "needs-human" || recorded || !j.FailClosed {
			t.Fatalf("offline approve = %+v recorded %v, want a fail-closed deny the service records", d, recorded)
		}
		if d.Reason != "Straza: security layer unreachable for an approval-gated action. Denied (fail-closed)" {
			t.Errorf("deny reason not the verbatim unreachable string: %q", d.Reason)
		}
	})
	t.Run("approve short-circuits serverCheck", func(t *testing.T) {
		a, _ := testAPI(&apiDouble{}, false)
		both := approved
		both.ServerCheck = true
		if d, _, _ := escalateWith(a, ev, both); d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, "approval-gated") {
			t.Errorf("approve branch did not win over serverCheck: %+v", d)
		}
	})
	t.Run("classifier deny short-circuits approve", func(t *testing.T) {
		f := &apiDouble{decide: verdictOf(agentguard.DecideResponse{Effect: policy.EffectAllow})}
		a, _ := testAPI(f, false)
		a.classifier = &apiClassifier{verdict: policy.ClassifyVerdict{Allowed: false, Reason: "shape"}}
		both := approved
		both.Classify = true
		if d, _, _ := escalateWith(a, ev, both); d.Effect != policy.EffectDeny || d.Reason != "Straza: classifier: shape" || len(f.decideCalls) != 0 {
			t.Fatalf("classifier deny should win before approve: %+v, %d decide calls", d, len(f.decideCalls))
		}
	})
	t.Run("classifier allow proceeds to approve", func(t *testing.T) {
		f := &apiDouble{decide: verdictOf(agentguard.DecideResponse{Effect: policy.EffectDeny, RuleID: "needs-human",
			Reason: "Straza: approval requested (notified roles [sec-approvers]); retry after approval (ref r1)"})}
		a, _ := testAPI(f, false)
		cls := &apiClassifier{verdict: policy.ClassifyVerdict{Allowed: true}}
		a.classifier = cls
		both := approved
		both.Classify = true
		d, _, _ := escalateWith(a, ev, both)
		if cls.calls.Load() != 1 || len(f.decideCalls) != 1 || !strings.Contains(d.Reason, "retry after approval") {
			t.Fatalf("classifier=%d approve=%d calls, decision %+v; want 1, 1 and the server verdict", cls.calls.Load(), len(f.decideCalls), d)
		}
	})
	t.Run("no approve marker never dials", func(t *testing.T) {
		f := &apiDouble{}
		a, _ := testAPI(f, false)
		a.classifier = &apiClassifier{verdict: policy.ClassifyVerdict{Allowed: true}}
		for _, d := range []policy.Decision{
			{Effect: policy.EffectAllow, RuleID: "plain"},
			{Effect: policy.EffectAllow, RuleID: "cls", Classify: true},
			{Effect: policy.EffectDeny, RuleID: "denied", Approve: &policy.ApproveSpec{}},
		} {
			if out, _, _ := escalateWith(a, ev, d); out.Effect != d.Effect {
				t.Errorf("%s: got %+v", d.RuleID, out)
			}
		}
		if len(f.decideCalls) != 0 {
			t.Errorf("the approve lane dialed %d times for decisions without the marker", len(f.decideCalls))
		}
	})
}

// TestDecideClassifyLane pins mode classify: the classifier runs before any
// online lane under its one second bound, and a not-allowed verdict or any
// classifier failure denies with the firing rule and set kept.
func TestDecideClassifyLane(t *testing.T) {
	py := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec,
		Command: `python3 -c "import shutil; shutil.rmtree('/tmp/x')"`, Interpreter: "python3"}
	local := policy.Decision{Effect: policy.EffectAllow, RuleID: "classify-gate", SetName: "dev-guardrails", Classify: true}
	const unavailable = "Straza: classifier unavailable for a classify-gated action. Denied (run `straza doctor`)"
	rows := []struct {
		name       string
		classifier policy.Classifier
		effect     string
		reason     string
		failClosed bool
	}{
		{"classifier deny wins", &apiClassifier{verdict: policy.ClassifyVerdict{Reason: "pipe-to-shell shape"}}, policy.EffectDeny, "Straza: classifier: pipe-to-shell shape", false},
		{"classifier allow proceeds", &apiClassifier{verdict: policy.ClassifyVerdict{Allowed: true}}, policy.EffectAllow, "", false},
		{"classifier error denies", &apiClassifier{err: errors.New("boom")}, policy.EffectDeny, unavailable, true},
		{"classifier timeout denies", &apiClassifier{verdict: policy.ClassifyVerdict{Allowed: true}, delay: 5 * time.Second}, policy.EffectDeny, unavailable, true},
		{"nil classifier denies", nil, policy.EffectDeny, unavailable, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			a, _ := testAPI(&apiDouble{}, false)
			a.classifier = row.classifier
			start := time.Now()
			d, _, j := escalateWith(a, py, local)
			if time.Since(start) >= 2*classifyBudget {
				t.Errorf("classify took %v, so its bound was not kept", time.Since(start))
			}
			if d.Effect != row.effect || d.Reason != row.reason || d.RuleID != "classify-gate" || d.SetName != "dev-guardrails" || j.FailClosed != row.failClosed {
				t.Errorf("decision = %+v journal %+v, want %s %q on the firing rule and set", d, j, row.effect, row.reason)
			}
			if j.Escalation != "classify" {
				t.Errorf("escalation = %q, want classify", j.Escalation)
			}
		})
	}
	t.Run("classify allow proceeds to serverCheck", func(t *testing.T) {
		f := &apiDouble{decide: verdictOf(agentguard.DecideResponse{Effect: policy.EffectDeny, RuleID: "central-veto", Reason: "Straza: vetoed centrally"})}
		a, _ := testAPI(f, false)
		cls := &apiClassifier{verdict: policy.ClassifyVerdict{Allowed: true}}
		a.classifier = cls
		both := local
		both.ServerCheck = true
		d, _, j := escalateWith(a, py, both)
		if cls.calls.Load() != 1 || len(f.decideCalls) != 1 || d.RuleID != "central-veto" || j.Escalation != "server" {
			t.Errorf("classifier=%d serverCheck=%d, decision %+v, journal %+v; want 1, 1 and the server's verdict", cls.calls.Load(), len(f.decideCalls), d, j)
		}
	})
}

// TestDecideFailClosedKeepsSetName pins that an unreachable online lane
// keeps the firing rule's set name beside its rule id, so the record names
// the deciding set.
func TestDecideFailClosedKeepsSetName(t *testing.T) {
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}
	for _, row := range []struct {
		name  string
		local policy.Decision
		want  string
	}{
		{"serverCheck unreachable", policy.Decision{Effect: policy.EffectAllow, RuleID: "server-gated", SetName: "dev-guardrails", ServerCheck: true}, "a server-checked action"},
		{"approveCheck unreachable", policy.Decision{Effect: policy.EffectAllow, RuleID: "needs-human", SetName: "dev-guardrails", Approve: &policy.ApproveSpec{}}, "an approval-gated action"},
	} {
		t.Run(row.name, func(t *testing.T) {
			a, _ := testAPI(&apiDouble{}, false)
			d, _, _ := escalateWith(a, ev, row.local)
			if d.Effect != policy.EffectDeny || d.RuleID != row.local.RuleID || d.SetName != "dev-guardrails" || !strings.Contains(d.Reason, row.want) {
				t.Errorf("fail-closed decision = %+v, want a deny naming rule %q in set dev-guardrails and %q", d, row.local.RuleID, row.want)
			}
		})
	}
}

// TestDecideOnlineDenyWording pins the honest deny of a dead online lane:
// when strazad answered and refused, before or after the bounce, the deny
// quotes it and never claims the server is unreachable, and the journal
// keeps the status and correlation id of the last answer.
func TestDecideOnlineDenyWording(t *testing.T) {
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}
	approve := policy.Decision{Effect: policy.EffectAllow, RuleID: "needs-human", Approve: &policy.ApproveSpec{}}
	f := &apiDouble{decide: refusedBy(401, "Straza: session state expired. Check in again")}
	a, _ := testAPI(f, false)
	d, recorded, j := escalateWith(a, ev, approve)
	if d.Effect != policy.EffectDeny || d.RuleID != "needs-human" || recorded {
		t.Fatalf("unrecovered bounce = %+v, want a fail-closed deny on the local rule", d)
	}
	if !strings.Contains(d.Reason, "refused") || !strings.Contains(d.Reason, "session state expired") ||
		!strings.Contains(d.Reason, "doctor") || strings.Contains(d.Reason, "unreachable") || strings.Contains(d.Reason, "Straza: session state") {
		t.Errorf("reason %q should quote the refusal once and name a remedy, and never claim unreachability", d.Reason)
	}
	if j.Status != 401 || j.Correlation != "req-2" || !j.FailClosed {
		t.Errorf("journal = %+v, want the refusal's status and correlation", j)
	}
}

// TestDecideRecordsOnce pins that every decision leaves exactly one
// straza.audit.tool record: the service records a decision it made itself,
// in the caller's spool, and records nothing for a verdict strazad gave,
// which strazad records. The delegate tag rides on both.
func TestDecideRecordsOnce(t *testing.T) {
	doc := `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: once}
spec:
  match: {roles: [dev]}
  rules:
    - {id: no-rm, tools: [shell.exec], command: {denyPatterns: ["rm -rf *"]}, effect: deny, reason: "blocked"}
    - {id: classify-scripts, tools: [shell.exec], command: {allowPatterns: ["python *"]}, effect: allow, mode: classify}
    - {id: gated-fetch, tools: [net.fetch], effect: allow, mode: serverCheck}
    - {id: gated-deploy, tools: [shell.exec], command: {allowPatterns: ["deploy-prod*"]}, effect: allow, mode: approve}
`
	fetch := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolNetFetch, Paths: []string{"https://x.io"}}
	shell := func(c string) policy.Event {
		return policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: c}
	}
	rows := []struct {
		name    string
		event   policy.Event
		verdict *agentguard.DecideResponse
		effect  string
		records int
		calls   int
	}{
		{"local allow", shell("ls -la"), nil, policy.EffectAllow, 1, 0},
		{"local deny", shell("rm -rf /x"), nil, policy.EffectDeny, 1, 0},
		{"classify", shell("python build.py"), nil, policy.EffectAllow, 1, 0},
		{"serverCheck online", fetch, &agentguard.DecideResponse{Effect: "allow", RuleID: "gated-fetch"}, policy.EffectAllow, 0, 1},
		{"serverCheck offline", fetch, nil, policy.EffectDeny, 1, 1},
		{"approve online allowed", shell("deploy-prod v1"), &agentguard.DecideResponse{Effect: "allow", RuleID: "gated-deploy", Reason: "Straza: approved by kim (ref abc123)"}, policy.EffectAllow, 0, 1},
		{"approve online denied", shell("deploy-prod v1"), &agentguard.DecideResponse{Effect: "deny", RuleID: "gated-deploy", Reason: "Straza: approval requested; retry after approval (ref abc123)"}, policy.EffectDeny, 0, 1},
		{"revoked", fetch, &agentguard.DecideResponse{Effect: "deny", RuleID: "revoked", Reason: "Straza: your session has been revoked. Re-enroll"}, policy.EffectDeny, 0, 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := gateDouble(t, 900, time.Hour, Renewal{})
			f.engine = apiEngine(t, doc, 900)
			if row.verdict != nil {
				f.decide = verdictOf(*row.verdict)
			}
			a, _ := testAPI(f, false)
			req := shellReq("")
			req.Event, req.AgentType, req.AgentID = row.event, "researcher", "a-1"
			ans := decideVia(t, a, req)
			if ans.Decision.Effect != row.effect || len(f.decideCalls) != row.calls {
				t.Fatalf("decision = %+v after %d decide calls, want %s after %d", ans.Decision, len(f.decideCalls), row.effect, row.calls)
			}
			recs := f.recorded()
			if len(recs) != row.records || ans.Journal.Spooled != (row.records == 1) {
				t.Fatalf("%d records with journal %+v, want %d", len(recs), ans.Journal, row.records)
			}
			for _, rec := range recs {
				d := rec.data
				if rec.ceType != "straza.audit.tool" || rec.caller.UID != "1000" || d["session"] != "s-1" || d["snapshot"] != "snap-1" ||
					d["harness"] != "claude-code/2.1.0" || d["effect"] != ans.Decision.Effect || d["reason"] != ans.Decision.Reason ||
					d["ruleId"] != ans.Decision.RuleID || d["agentType"] != "researcher" || d["agentId"] != "a-1" {
					t.Errorf("record = %s %+v for uid %s, want the decision's full record in uid 1000's spool", rec.ceType, d, rec.caller.UID)
				}
			}
			for _, c := range f.decideCalls {
				if c.agentType != "researcher" || c.agentID != "a-1" {
					t.Errorf("decide call %+v lacks the delegate tag", c)
				}
			}
		})
	}
}

// TestDecideRecordCarriesEvidence pins the record's shape: the full event,
// the deciding set beside the rule id, an empty set name present on a
// default decision, and no delegate keys on a root-lane record.
func TestDecideRecordCarriesEvidence(t *testing.T) {
	req := localapi.DecideRequest{Harness: localapi.Harness{Name: "codex"},
		Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user",
			Paths: []string{"/w/a"}, Workspace: "/w"}}
	ruled := toolRecord(liveSession(), "snap-1", req, policy.Decision{Effect: policy.EffectDeny, RuleID: "dev-shell-rm", SetName: "dev-guardrails"})
	raw, _ := json.Marshal(toolRecord(liveSession(), "snap-1", req, policy.Decision{Effect: policy.EffectAllow}))
	if ruled["setName"] != "dev-guardrails" || !strings.Contains(string(raw), `"setName":""`) || !strings.Contains(string(raw), `"ruleId":""`) {
		t.Errorf("records %v and %s, want the set named and an empty set name present", ruled, raw)
	}
	if ruled["toolName"] != "disable_user" || ruled["workspace"] != "/w" || ruled["harness"] != "codex" || ruled["app"] != "midpoint" {
		t.Errorf("record %v lacks the event's evidence", ruled)
	}
	if _, ok := ruled["agentType"]; ok {
		t.Errorf("root-lane record carries a delegate key: %v", ruled)
	}
}

// TestDecideSpoolFailureNeverChangesDecisions pins that a spool the service
// cannot write changes no decision: the allow, the rule deny and the
// fail-closed deny stand, and the journal names only the error's class,
// never its text, which can name a path.
func TestDecideSpoolFailureNeverChangesDecisions(t *testing.T) {
	f := gateDouble(t, 900, time.Hour, Renewal{})
	f.recordErr = &fs.PathError{Op: "open", Path: "/var/lib/straza-service/u/1000/audit-spool.jsonl", Err: errors.New("no space left on device")}
	a, _ := testAPI(f, false)
	for command, effect := range map[string]string{"git status": policy.EffectAllow, "rm -rf /x": policy.EffectDeny} {
		ans := decideVia(t, a, shellReq(command))
		if ans.Decision.Effect != effect || !ans.Journal.Spooled || ans.Journal.SpoolError != "open" {
			t.Errorf("%q = %+v, want %s with the spool error class open", command, ans, effect)
		}
	}
	f.session.ExpiresAt = time.Now().Add(-time.Hour)
	f.maxAge, f.engine = 0, apiEngine(t, apiOfflineDoc, 0)
	if ans := decideVia(t, a, shellReq("git status")); ans.Decision.Effect != policy.EffectDeny || ans.Journal.SpoolError != "open" {
		t.Errorf("expired session = %+v, want the offline deny with the spool error class", ans)
	}
	for err, want := range map[error]string{
		nil: "",
		fmt.Errorf("append: %w", context.DeadlineExceeded):                            "timeout",
		&fs.PathError{Op: "write", Path: "/x", Err: errors.New("input/output error")}: "write",
		fmt.Errorf("drain: %w", &agentguard.StatusError{Status: 429}):                 "http",
		errors.New("spool record of 70 MiB is larger than the line bound"):            "other",
	} {
		if got := spoolClass(err); got != want {
			t.Errorf("spoolClass(%v) = %q, want %q", err, got, want)
		}
	}
}

// TestDecideNamesTheDecidingSnapshot pins that the record, the journal and
// the session answer name the snapshot the engine was built from, even when
// a renewal moved the pin after the credential half handed out the
// session's copy.
func TestDecideNamesTheDecidingSnapshot(t *testing.T) {
	f := gateDouble(t, 900, time.Hour, Renewal{})
	f.snapshot = "snap-2"
	a, _ := testAPI(f, false)
	ans := decideVia(t, a, shellReq("git status"))
	if recs := f.recorded(); len(recs) != 1 || recs[0].data["snapshot"] != "snap-2" || ans.Journal.Snapshot != "snap-2" {
		t.Errorf("record %+v and journal %+v, want both to name snap-2", recs, ans.Journal)
	}
	w := serveAs(a, "1000", http.MethodPost, localapi.RouteSessions, localapi.SessionRequest{Harness: localapi.Harness{Name: "claude-code"}})
	var sa localapi.SessionAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &sa); err != nil || sa.SnapshotID != "snap-2" {
		t.Errorf("session answer = %d %q, want snap-2", w.Code, w.Body.String())
	}
}

// nonEmpty answers s as a record value: nil for "", because a record leaves
// an empty field out.
func nonEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
