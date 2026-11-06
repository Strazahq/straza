package approval

import (
	"context"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// insertHookStore is a store whose approval insert commits the row and then
// runs after with the insert's context, standing for a client that leaves
// while its approval request is being written.
type insertHookStore struct {
	store.Store
	after func(ctx context.Context) error
}

func (s insertHookStore) Approvals() store.ApprovalRepo {
	return insertHookApprovals{s.Store.Approvals(), s.after}
}

type insertHookApprovals struct {
	store.ApprovalRepo
	after func(ctx context.Context) error
}

func (a insertHookApprovals) Insert(ctx context.Context, row store.Approval) (store.Approval, error) {
	created, err := a.ApprovalRepo.Insert(ctx, row)
	if err != nil {
		return created, err
	}
	return created, a.after(ctx)
}

// TestRequestRecordSurvivesClientLeaving pins that a client that leaves
// once its approval row has committed cannot lose the request record. The
// request answers the pending record, exactly one request record names it
// with the requester, the session and the rule, and the approvers are
// notified once. The second case stands for a driver that reports the
// caller's cancellation although the row committed.
func TestRequestRecordSurvivesClientLeaving(t *testing.T) {
	cases := []struct {
		name  string
		after func(leave context.CancelFunc) func(ctx context.Context) error
	}{
		{"the client leaves after the insert commits", func(leave context.CancelFunc) func(context.Context) error {
			return func(context.Context) error { leave(); return nil }
		}},
		{"the client leaves while the insert answers", func(leave context.CancelFunc) func(context.Context) error {
			return func(ctx context.Context) error { leave(); return ctx.Err() }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ch := attachFakes(h, 1)
			requester := h.seedUser(t, "nova")
			h.seedUser(t, "kim", "sec-approvers")
			ctx, leave := context.WithCancel(context.Background())
			defer leave()
			h.svc.st = insertHookStore{Store: h.st, after: tc.after(leave)}

			rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", holdSpec()))
			if err != nil {
				t.Fatalf("Request = %v, want the pending record although the client left", err)
			}
			if rec.State != StatePending || rec.ID == "" {
				t.Fatalf("Request answered %+v, want a pending record", rec)
			}
			if n := countAuditPhase(t, h.st, "request"); n != 1 {
				t.Fatalf("request records = %d, want exactly 1", n)
			}
			for field, want := range map[string]string{
				"approvalId": rec.ID, "user": requester.ID, "session": "s-1", "rule": "r-1", "state": "pending",
			} {
				assertAuditField(t, h.st, "request", field, want)
			}
			if got := countBy(waitEvents(t, ch, 1, 2*time.Second)); got["a/created"] != 1 {
				t.Errorf("notifications = %v, want one created", got)
			}
		})
	}
}
