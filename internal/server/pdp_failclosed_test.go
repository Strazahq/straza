package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
)

// failClosedCounter reads straza_failclosed_total{lane} off the app's private
// registry (0 when the lane has not been counted yet).
func failClosedCounter(t *testing.T, a *App, lane string) float64 {
	t.Helper()
	fams, err := a.metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != "straza_failclosed_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "lane" && l.GetValue() == lane {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// ctxWithCapture binds a per-request logger carrying correlation_id=id into
// ctx exactly the way requestID does, so a direct call into a PEP resolver
// logs into the capture (and only there: a.log stays untouched, so background
// runners cannot pollute an exactly-one assertion).
func ctxWithCapture(ctx context.Context, log *slog.Logger, id string) context.Context {
	return context.WithValue(ctx, reqLogCtxKey{}, log.With(correlationKey, id))
}

// assertOneFailClosedRecord asserts exactly one Error record landed in buf and
// that it carries the fail-closed message, the lane and the correlation id.
func assertOneFailClosedRecord(t *testing.T, buf *syncBuffer, lane, wantID string) string {
	t.Helper()
	recs := errorRecords(buf)
	if len(recs) != 1 {
		t.Fatalf("Error records = %d, want exactly 1:\n%s", len(recs), buf.String())
	}
	for _, want := range []string{`msg="fail-closed: internal failure answered as a deny"`, "lane=" + lane, "cause="} {
		if !strings.Contains(recs[0], want) {
			t.Fatalf("fail-closed record lacks %s: %s", want, recs[0])
		}
	}
	if wantID != "" && !strings.Contains(recs[0], "correlation_id="+wantID) {
		t.Fatalf("fail-closed record lacks correlation_id=%s: %s", wantID, recs[0])
	}
	return recs[0]
}

// TestFailClosedHelper pins the one helper every PEP fail-closed path calls:
// one Error record through the per-request logger (correlation id bound) or
// a.log when the context carries none, the per-lane counter, the unroutable
// exception (a decision, never a failure), and survival on a bare App.
func TestFailClosedHelper(t *testing.T) {
	t.Parallel()
	t.Run("per-request logger carries the correlation id and the lane counts", func(t *testing.T) {
		app, _ := testApp(t)
		log, buf := captureLogger()
		ctx := ctxWithCapture(context.Background(), log, "corr-hook-1")
		app.failClosed(ctx, "hook", "r-approve", errors.New("pg: connection refused"))
		rec := assertOneFailClosedRecord(t, buf, "hook", "corr-hook-1")
		for _, want := range []string{"rule=r-approve", `cause="pg: connection refused"`} {
			if !strings.Contains(rec, want) {
				t.Errorf("record lacks %s: %s", want, rec)
			}
		}
		if got := failClosedCounter(t, app, "hook"); got != 1 {
			t.Errorf("straza_failclosed_total{lane=hook} = %v, want 1", got)
		}
		if got := failClosedCounter(t, app, "gateway"); got != 0 {
			t.Errorf("straza_failclosed_total{lane=gateway} = %v, want 0", got)
		}
		app.failClosed(ctx, "gateway", "", errors.New("db down"))
		if got := failClosedCounter(t, app, "gateway"); got != 1 {
			t.Errorf("straza_failclosed_total{lane=gateway} = %v, want 1", got)
		}
		if recs := errorRecords(buf); len(recs) != 2 {
			t.Errorf("records after two calls = %d, want 2", len(recs))
		}
	})

	t.Run("no per-request logger falls back to a.log", func(t *testing.T) {
		log, buf := captureLogger()
		app, _ := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }})
		buf.Reset()
		app.failClosed(context.Background(), "hook", "r-1", errors.New("boom"))
		rec := assertOneFailClosedRecord(t, buf, "hook", "")
		if strings.Contains(rec, "correlation_id=") {
			t.Errorf("fallback record must not invent a correlation id: %s", rec)
		}
	})

	t.Run("unroutable is a decision: no record, no count", func(t *testing.T) {
		app, _ := testApp(t)
		log, buf := captureLogger()
		ctx := ctxWithCapture(context.Background(), log, "corr-un")
		app.failClosed(ctx, "hook", "r-bare", &approval.UnroutableError{Cause: "no sponsor"})
		if recs := errorRecords(buf); len(recs) != 0 {
			t.Fatalf("unroutable logged %d Error records, want 0:\n%s", len(recs), buf.String())
		}
		if got := failClosedCounter(t, app, "hook"); got != 0 {
			t.Errorf("unroutable counted: straza_failclosed_total{lane=hook} = %v, want 0", got)
		}
	})

	t.Run("bare App survives", func(t *testing.T) {
		app := &App{}
		log, buf := captureLogger()
		app.failClosed(ctxWithCapture(context.Background(), log, "corr-bare"), "gateway", "r", errors.New("x"))
		assertOneFailClosedRecord(t, buf, "gateway", "corr-bare")
		app.failClosed(context.Background(), "gateway", "r", errors.New("y")) // nil a.log, nil metrics: no panic
	})
}

// TestResolveClassifyErrorLogsOnce pins the classifier fallback: a failing
// classifier call still denies with the unchanged reason and now writes one
// fail-closed record on the hook lane; the nil-backend case is configuration
// (no classifier wired), not a failure, and stays silent.
func TestResolveClassifyErrorLogsOnce(t *testing.T) {
	t.Parallel()
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "python3 run.py", Interpreter: "python3"}
	local := policy.Decision{Effect: policy.EffectAllow, RuleID: "classify-interpreters", Classify: true}

	app := &App{classifier: &countingClassifier{err: errors.New("boom")}}
	log, buf := captureLogger()
	d := app.resolveClassify(ctxWithCapture(context.Background(), log, "cls-corr-1"), "hook", ev, local)
	if d.Effect != policy.EffectDeny || d.Reason != "Straza: classifier unavailable for a classify-gated action. Denied" {
		t.Fatalf("decision = %+v", d)
	}
	rec := assertOneFailClosedRecord(t, buf, "hook", "cls-corr-1")
	if !strings.Contains(rec, "rule=classify-interpreters") || !strings.Contains(rec, `cause="classifier: boom"`) {
		t.Errorf("record lacks rule/cause: %s", rec)
	}

	bare := &App{}
	log2, buf2 := captureLogger()
	if d := bare.resolveClassify(ctxWithCapture(context.Background(), log2, "cls-corr-2"), "hook", ev, local); d.Effect != policy.EffectDeny {
		t.Fatalf("nil backend decision = %+v", d)
	}
	if recs := errorRecords(buf2); len(recs) != 0 {
		t.Errorf("nil classifier backend logged %d records, want 0 (configuration, not a failure)", len(recs))
	}
}

// TestHookFailClosedLogsExactlyOnceAcrossLayers drives the REAL approval
// service (not a gate stub) on the fault store with the app logger AND the
// per-request logger captured in the same buffer: exactly one Error record
// must exist for a fail-closed deny, written by the PEP with the correlation
// id and an op-named cause. Without this pin the approval service would log
// the same event again at its own layer.
func TestHookFailClosedLogsExactlyOnceAcrossLayers(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, _, fs := testAppFaultLog(t, log)
	requester := seedIdentity(t, app)
	fs.arm("approvals", errors.New("db down"))
	defer fs.disarm()
	ctx := ctxWithCapture(context.Background(), log, "hook-corr-2")

	claims := authn.Claims{Session: "sess-3", Subject: requester.ID}
	decision := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "r-approve",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
	}
	dec, id := app.resolveApproveHook(ctx, claims, policy.Subject{}, policy.Event{Tool: policy.ToolShellExec, Command: "x"}, decision)
	if dec.Effect != policy.EffectDeny || id != "" {
		t.Fatalf("resolve = %+v id=%q, want deny + empty id", dec, id)
	}
	if dec.Reason != "Straza: approval service error. Denied (fail-closed)" {
		t.Errorf("reason = %q", dec.Reason)
	}
	recs := errorRecords(buf)
	if len(recs) != 1 {
		t.Fatalf("want exactly ONE Error record across the PEP and the approval service, got %d:\n%s", len(recs), strings.Join(recs, "\n"))
	}
	for _, want := range []string{"correlation_id=hook-corr-2", "lane=hook", "rule=r-approve", "approval request-dedupe: db down"} {
		if !strings.Contains(recs[0], want) {
			t.Errorf("record lacks %q: %s", want, recs[0])
		}
	}
}
