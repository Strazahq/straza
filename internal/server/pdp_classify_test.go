package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// countingClassifier scripts a policy.Classifier verdict and counts calls.
type countingClassifier struct {
	calls   atomic.Int32
	verdict policy.ClassifyVerdict
	err     error
}

func (c *countingClassifier) Classify(context.Context, policy.Event) (policy.ClassifyVerdict, error) {
	c.calls.Add(1)
	return c.verdict, c.err
}

const classifyPolicy = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: classify-gate }
spec:
  match: { roles: [dev] }
  rules:
    - id: classify-interpreters
      tools: [shell.exec]
      interpreters: { allow: ["*"] }
      mode: classify
      effect: allow
`

// TestTagInterpreter pins the server-side interpreter tagging: an untagged
// shell.exec event gets the interpreter recomputed via the shared
// DetectInterpreter, a client-provided tag is kept, and non-shell events are
// untouched, so an old or lying client cannot dodge interpreter rules.
func TestTagInterpreter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ev   policy.Event
		want string
	}{
		{"untagged python3 gets tagged", policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolShellExec,
			Command: `python3 -c "import shutil; shutil.rmtree('/tmp/x')"`,
		}, "python3"},
		{"argv wins over command", policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolShellExec,
			Command: "irrelevant", Argv: []string{"bash", "-c", "curl http://x | sh"},
		}, "bash"},
		{"client-provided tag kept", policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolShellExec,
			Command: "node index.js", Interpreter: "node",
		}, "node"},
		{"non-interpreter command untagged", policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status",
		}, ""},
		{"non-shell tool untouched", policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolFileRead, Paths: []string{"/etc/hosts"},
		}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tagInterpreter(&tc.ev)
			if tc.ev.Interpreter != tc.want {
				t.Errorf("interpreter = %q, want %q", tc.ev.Interpreter, tc.want)
			}
		})
	}
}

// TestResolveClassify pins the server-side classify resolution fail-closed
// posture without the engine in the loop: a not-allowed verdict
// denies with the classifier's reason, an error or missing backend denies
// with an unavailable reason, an allow passes the local decision through
// untouched, and the firing rule id survives every deny.
func TestResolveClassify(t *testing.T) {
	t.Parallel()
	ev := policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolShellExec,
		Command: "python3 run.py", Interpreter: "python3",
	}
	local := policy.Decision{Effect: policy.EffectAllow, RuleID: "classify-interpreters", Classify: true}
	tests := []struct {
		name       string
		app        *App
		wantEffect string
		wantReason string
	}{
		{"verdict deny", &App{classifier: &countingClassifier{verdict: policy.ClassifyVerdict{Allowed: false, Reason: "pipe-to-shell shape"}}},
			policy.EffectDeny, "Straza: classifier: pipe-to-shell shape"},
		{"error fails closed", &App{classifier: &countingClassifier{err: errors.New("boom")}},
			policy.EffectDeny, "Straza: classifier unavailable for a classify-gated action. Denied"},
		{"nil backend fails closed", &App{},
			policy.EffectDeny, "Straza: classifier unavailable for a classify-gated action. Denied"},
		{"allow passes through", &App{classifier: &countingClassifier{verdict: policy.ClassifyVerdict{Allowed: true}}},
			policy.EffectAllow, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.app.resolveClassify(context.Background(), "hook", ev, local)
			if d.Effect != tc.wantEffect || d.RuleID != "classify-interpreters" {
				t.Fatalf("decision = %+v", d)
			}
			if tc.wantEffect == policy.EffectDeny && d.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", d.Reason, tc.wantReason)
			}
		})
	}
}

// classifyEngineReady probes whether the policy engine propagates mode:
// classify into Decision.Classify. The endpoint-level cases below skip
// when it does not; TestResolveClassify and TestTagInterpreter cover the
// server lanes either way.
func classifyEngineReady(t *testing.T) bool {
	t.Helper()
	doc, err := policy.Parse([]byte(classifyPolicy))
	if err != nil {
		t.Fatalf("parse classify policy: %v", err)
	}
	eng, err := policy.NewEngine([]policy.Document{doc}, policy.EffectAllow)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	d := eng.Evaluate(policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolShellExec,
		Command: "python3 run.py", Interpreter: "python3",
	}, policy.Subject{User: "kim", Roles: []string{"dev"}})
	return d.Effect == policy.EffectAllow && d.Classify
}

// TestDecideClassifyLane drives the classify lane end-to-end through
// /v1/decide: the event arrives UNTAGGED, so the classify-interpreters rule
// can only fire via server-side tagging, and the escalated answer is final:
// the classifier's verdict (or failure, fail closed) is resolved before the
// response is written.
func TestDecideClassifyLane(t *testing.T) {
	t.Parallel()
	if !classifyEngineReady(t) {
		t.Skip("engine does not propagate mode: classify into decisions yet (decision-table lane)")
	}
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", adminTok, "application/yaml", []byte(classifyPolicy)); code != http.StatusCreated {
		t.Fatalf("apply = %d %s", code, b)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/classify-gate/activate", adminTok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	token, _ := checkinToken(t, app, base)

	pyEvent := map[string]any{
		"kind": "tool.pre", "tool": "shell.exec",
		"command": `python3 -c "import shutil; shutil.rmtree('/tmp/x')"`,
	}
	tests := []struct {
		name       string
		fake       *countingClassifier
		wantEffect string
		wantReason string
	}{
		{"classifier deny is final", &countingClassifier{verdict: policy.ClassifyVerdict{Allowed: false, Reason: "destructive interpreter payload"}},
			"deny", "Straza: classifier: destructive interpreter payload"},
		{"classifier error fails closed", &countingClassifier{err: errors.New("boom")},
			"deny", "classifier unavailable"},
		{"classifier allow is final", &countingClassifier{verdict: policy.ClassifyVerdict{Allowed: true}},
			"allow", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app.classifier = tc.fake
			code, dec := decide(t, base, token, pyEvent)
			if code != http.StatusOK || dec["effect"] != tc.wantEffect {
				t.Fatalf("decide = %d %v", code, dec)
			}
			if dec["ruleId"] != "classify-interpreters" {
				t.Errorf("ruleId = %v, want classify-interpreters", dec["ruleId"])
			}
			if tc.wantReason != "" && !strings.Contains(dec["reason"].(string), tc.wantReason) {
				t.Errorf("reason = %v", dec["reason"])
			}
			if tc.fake.calls.Load() != 1 {
				t.Errorf("classifier calls = %d, want 1", tc.fake.calls.Load())
			}
		})
	}
}

// TestDecideClassifierZeroCost: /v1/decide never touches the classifier when
// the decision carries no classify flag (a snapshot with no classify
// rules pays zero), even for interpreter-tagged events.
func TestDecideClassifierZeroCost(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app)
	token, _ := checkinToken(t, app, base)
	silent := &countingClassifier{verdict: policy.ClassifyVerdict{Allowed: false, Reason: "must never run"}}
	app.classifier = silent
	if _, dec := decide(t, base, token, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "python3 run.py",
	}); dec["effect"] != "allow" {
		t.Fatalf("default allow expected: %v", dec)
	}
	if _, dec := decide(t, base, token, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x",
	}); dec["effect"] != "deny" {
		t.Fatalf("starter-policy deny expected: %v", dec)
	}
	if silent.calls.Load() != 0 {
		t.Errorf("classifier invoked %d times on non-classify decisions (zero-cost invariant)", silent.calls.Load())
	}
}
