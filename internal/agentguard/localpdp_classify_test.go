package agentguard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/policy"
)

// fakeClassifier scripts a policy.Classifier verdict and counts invocations.
// A non-zero delay sleeps honoring ctx, so a blown deadline surfaces as
// ctx.Err(). A PEP that forgot the deadline gets the verdict late and
// fails the test on it.
type fakeClassifier struct {
	calls   atomic.Int32
	verdict policy.ClassifyVerdict
	err     error
	delay   time.Duration
}

func (f *fakeClassifier) Classify(ctx context.Context, _ policy.Event) (policy.ClassifyVerdict, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return policy.ClassifyVerdict{}, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	return f.verdict, f.err
}

// TestClassifyLane pins the mode:classify escalation: a
// classify-flagged local allow consults the embedded classifier BEFORE
// any serverCheck escalation, a not-allowed verdict or ANY classifier
// failure (error, blown deadline, missing backend) is a deny that fails
// closed, exactly the serverCheck posture. Engine-side flag propagation
// is pinned by the policy decision tables; here the lanes are driven with
// hand-built decisions through the same escalate step Decide runs.
func TestClassifyLane(t *testing.T) {
	pyEvent := policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolShellExec,
		Command: `python3 -c "import shutil; shutil.rmtree('/tmp/x')"`, Interpreter: "python3",
	}
	classified := policy.Decision{Effect: policy.EffectAllow, RuleID: "classify-gate", Classify: true}

	t.Run("classifier deny wins", func(t *testing.T) {
		p := &LocalPDP{classifier: &fakeClassifier{verdict: policy.ClassifyVerdict{Allowed: false, Reason: "pipe-to-shell shape"}}}
		d := p.escalate(pyEvent, classified)
		if d.Effect != policy.EffectDeny || d.RuleID != "classify-gate" {
			t.Fatalf("classifier deny: %+v", d)
		}
		if d.Reason != "Straza: classifier: pipe-to-shell shape" {
			t.Errorf("reason = %q", d.Reason)
		}
	})

	t.Run("classifier allow proceeds", func(t *testing.T) {
		f := &fakeClassifier{verdict: policy.ClassifyVerdict{Allowed: true}}
		p := &LocalPDP{classifier: f}
		d := p.escalate(pyEvent, classified)
		if d.Effect != policy.EffectAllow || d.RuleID != "classify-gate" {
			t.Fatalf("classifier allow: %+v", d)
		}
		if f.calls.Load() != 1 {
			t.Errorf("classifier called %d times, want 1", f.calls.Load())
		}
	})

	t.Run("classifier error denies", func(t *testing.T) {
		p := &LocalPDP{classifier: &fakeClassifier{err: errors.New("boom")}}
		d := p.escalate(pyEvent, classified)
		if d.Effect != policy.EffectDeny || d.RuleID != "classify-gate" {
			t.Fatalf("classifier error: %+v", d)
		}
		if !strings.Contains(d.Reason, "classifier") || !strings.Contains(d.Reason, "doctor") {
			t.Errorf("deny reason not actionable: %q", d.Reason)
		}
	})

	t.Run("classifier timeout denies", func(t *testing.T) {
		// The fake would return an ALLOW after 5 s: only the 1 s deadline the
		// PEP carries turns it into an error. No deadline ⇒ late allow ⇒ fail.
		p := &LocalPDP{classifier: &fakeClassifier{verdict: policy.ClassifyVerdict{Allowed: true}, delay: 5 * time.Second}}
		start := time.Now()
		d := p.escalate(pyEvent, classified)
		if d.Effect != policy.EffectDeny {
			t.Fatalf("blown deadline allowed: %+v", d)
		}
		if elapsed := time.Since(start); elapsed >= 5*time.Second {
			t.Errorf("no deadline enforced (took %v)", elapsed)
		}
	})

	t.Run("nil classifier denies", func(t *testing.T) {
		p := &LocalPDP{}
		if d := p.escalate(pyEvent, classified); d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, "classifier") {
			t.Fatalf("missing backend allowed: %+v", d)
		}
	})

	t.Run("classify allow proceeds to serverCheck", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"effect": "deny", "ruleId": "central-veto", "reason": "Straza: vetoed centrally",
			})
		}))
		defer srv.Close()
		f := &fakeClassifier{verdict: policy.ClassifyVerdict{Allowed: true}}
		p := &LocalPDP{
			classifier: f,
			session:    Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)},
			client:     NewClient(srv.URL),
		}
		both := classified
		both.ServerCheck = true
		d := p.escalate(pyEvent, both)
		if f.calls.Load() != 1 || hits.Load() != 1 {
			t.Fatalf("classifier=%d serverCheck=%d calls, want 1 and 1", f.calls.Load(), hits.Load())
		}
		if d.Effect != policy.EffectDeny || d.RuleID != "central-veto" {
			t.Errorf("server verdict should win after a classify allow: %+v", d)
		}
	})

	t.Run("no classify flag never classifies", func(t *testing.T) {
		// The zero-cost invariant, full Decide path with a real engine:
		// plain allows, serverCheck-only escalations, and observational events
		// must never touch the classifier.
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"effect": "allow"})
		}))
		defer srv.Close()
		f := &fakeClassifier{verdict: policy.ClassifyVerdict{Allowed: false, Reason: "must never run"}}
		p := serverCheckPDP(t, srv.URL)
		p.classifier = f
		if d := p.Decide(Normalized{Event: policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status",
		}}); d.Effect != policy.EffectAllow {
			t.Fatalf("default allow expected: %+v", d)
		}
		if d := p.Decide(Normalized{Event: policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolNetFetch, Paths: []string{"https://x.io"},
		}}); d.Effect != policy.EffectAllow {
			t.Fatalf("serverCheck-only allow expected: %+v", d)
		}
		if d := p.Decide(Normalized{Event: policy.Event{Kind: policy.EventSessionEnd}}); d.Effect != policy.EffectAllow || !d.Default {
			t.Fatalf("observational event: %+v", d)
		}
		if f.calls.Load() != 0 {
			t.Errorf("classifier invoked %d times on non-classify decisions (zero-cost invariant)", f.calls.Load())
		}
		if hits.Load() != 1 {
			t.Errorf("serverCheck lane hit the server %d times, want exactly 1", hits.Load())
		}
	})
}

// TestClassifyDenyRecordCarriesSetName pins the audit record of a classify
// deny: every deny the classify lane builds keeps the firing rule's set name
// beside its rule id, so the spooled record names the deciding set the way a
// pattern deny's record does.
func TestClassifyDenyRecordCarriesSetName(t *testing.T) {
	pyEvent := policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolShellExec,
		Command: `python3 -c "import os"`, Interpreter: "python3",
	}
	local := policy.Decision{Effect: policy.EffectAllow, RuleID: "classify-interpreters", SetName: "dev-guardrails", Classify: true}
	const unavailable = "Straza: classifier unavailable for a classify-gated action. Denied (run `straza doctor`)"
	tests := []struct {
		name       string
		classifier policy.Classifier
		reason     string
	}{
		{"classifier verdict deny", &fakeClassifier{verdict: policy.ClassifyVerdict{Allowed: false, Reason: "pipe-to-shell shape"}}, "Straza: classifier: pipe-to-shell shape"},
		{"classifier error", &fakeClassifier{err: errors.New("boom")}, unavailable},
		{"no classifier", nil, unavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &LocalPDP{classifier: tt.classifier}
			d := p.escalate(pyEvent, local)
			path := filepath.Join(t.TempDir(), "spool.jsonl")
			n := Normalized{HarnessName: "claude-code", HarnessVersion: "2.1", Event: pyEvent}
			if err := spoolAppend(spool.NewSpool(path), n, "s1", "snap-1", d); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path) // #nosec G304 -- test-owned temp path
			if err != nil {
				t.Fatal(err)
			}
			var ce struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(raw, &ce); err != nil {
				t.Fatalf("spool record is not one CloudEvent: %v\n%s", err, raw)
			}
			for field, want := range map[string]string{
				"effect": policy.EffectDeny, "ruleId": "classify-interpreters", "setName": "dev-guardrails", "reason": tt.reason,
			} {
				if got, _ := ce.Data[field].(string); got != want {
					t.Errorf("record %s = %q, want %q", field, got, want)
				}
			}
		})
	}
}
