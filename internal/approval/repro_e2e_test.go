package approval

import (
	"context"
	"testing"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestReproE2EPendingList pins the end-to-end sequence of a hook-lane
// request: a bare-approve Request (revision 14: routed to the requester's
// sponsor), then List("pending") must surface the record immediately.
func TestReproE2EPendingList(t *testing.T) {
	h := newHarness(t)
	h.seedUser(t, "grace")
	agent, err := h.st.Users().Create(context.Background(), store.User{
		Username: "e2e-agent", Email: "e2e@x.io", UserType: store.UserTypeAgent, Sponsor: "grace"})
	if err != nil {
		t.Fatalf("Create agent: %v", err)
	}
	rec, err := h.svc.Request(context.Background(), RequestInput{
		SessionID: "sess-1", UserID: agent.ID, Username: "e2e-agent",
		RuleID: "e2e-approve-deploy-prod", SetName: "e2e-guardrails",
		ArgvHash: "sha256:abc", Lane: "hook",
		Summary: "shell.exec: ./deploy-prod.sh --env production",
		Spec:    policy.ApproveSpec{TimeoutSeconds: 120, RetryTTLSeconds: 60},
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	recs, err := h.svc.List(context.Background(), "pending")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != rec.ID {
		t.Fatalf("pending list = %+v, want %s", recs, rec.ID)
	}
}
