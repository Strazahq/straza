package approval

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// TestRequestAndDecideLogAtDebug pins the approval state-transition lines: a
// new record logs "approval requested", a dedupe hit logs "approval request
// attached to pending", a decision logs "approval decided", all at Debug,
// ids only, and the happy path writes nothing at Info or Warn.
func TestRequestAndDecideLogAtDebug(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var buf bytes.Buffer
	h.svc.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	approver := h.seedUser(t, "ann", "sec-approvers")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	rec, err := h.svc.Request(ctx, req("s-1", "u-1", "tox", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	got := buf.String()
	for _, want := range []string{`level=DEBUG msg="approval requested"`, "component=approval", "id=" + rec.ID, "lane=hook", "rule=r-1", "user=tox"} {
		if !strings.Contains(got, want) {
			t.Fatalf("request record lacks %s:\n%s", want, got)
		}
	}

	buf.Reset()
	again, err := h.svc.Request(ctx, req("s-1", "u-1", "tox", spec))
	if err != nil || again.ID != rec.ID {
		t.Fatalf("dedupe: err=%v id=%s want %s", err, again.ID, rec.ID)
	}
	got = buf.String()
	if !strings.Contains(got, `level=DEBUG msg="approval request attached to pending"`) || !strings.Contains(got, "id="+rec.ID) {
		t.Fatalf("dedupe record missing:\n%s", got)
	}
	if strings.Contains(got, `msg="approval requested"`) {
		t.Fatalf("a dedupe hit must not log a new request:\n%s", got)
	}

	buf.Reset()
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	got = buf.String()
	for _, want := range []string{`level=DEBUG msg="approval decided"`, "component=approval", "id=" + rec.ID, "verdict=approved", "channel=console", "decider=" + approver.ID} {
		if !strings.Contains(got, want) {
			t.Fatalf("decide record lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "level=INFO") || strings.Contains(got, "level=WARN") {
		t.Fatalf("happy path wrote Info/Warn:\n%s", got)
	}
}
