package approval

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// TestRequestRate pins the rate of new approval rows: a session opens five
// at once and a person ten across their sessions, holds and tickets alike.
// A request over either rate answers ErrTooManyRequests and writes no row,
// no request record and no notification. A request that joins a pending
// row spends nothing and is never refused, and another person keeps a rate
// of their own. The steps run in order on one service.
func TestRequestRate(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	ch := attachFakes(h, 1)
	nova := h.seedUser(t, "nova")
	kim := h.seedUser(t, "kim")
	ticket := ticketSpec()
	steps := []struct {
		name, session, userID, argv string
		spec                        policy.ApproveSpec
		want                        string // new, joins or refused
	}{
		{"the first hold", "s-1", nova.ID, "k1", holdSpec(), "new"},
		{"the second hold", "s-1", nova.ID, "k2", holdSpec(), "new"},
		{"the third hold", "s-1", nova.ID, "k3", holdSpec(), "new"},
		{"the fourth hold", "s-1", nova.ID, "k4", holdSpec(), "new"},
		{"the fifth hold", "s-1", nova.ID, "k5", holdSpec(), "new"},
		{"a sixth hold in the session is refused", "s-1", nova.ID, "k6", holdSpec(), "refused"},
		{"a ticket in the session is refused too", "s-1", nova.ID, "k6", ticket, "refused"},
		{"the first call again joins its hold", "s-1", nova.ID, "k1", holdSpec(), "joins"},
		{"a second session opens its own", "s-2", nova.ID, "k7", holdSpec(), "new"},
		{"a second session's ticket", "s-2", nova.ID, "k8", ticket, "new"},
		{"a second session's third", "s-2", nova.ID, "k9", holdSpec(), "new"},
		{"a second session's fourth", "s-2", nova.ID, "k10", holdSpec(), "new"},
		{"the person's tenth", "s-2", nova.ID, "k11", holdSpec(), "new"},
		{"an eleventh in a fresh session is refused for the person", "s-3", nova.ID, "k12", holdSpec(), "refused"},
		{"another person keeps a rate of their own", "s-9", kim.ID, "k12", holdSpec(), "new"},
	}
	refs := map[string]string{}
	for _, st := range steps {
		in := req(st.session, st.userID, "nova", st.spec)
		in.ArgvHash = "sha256:" + st.argv
		rows, records := len(h.listAll(t)), countAuditPhase(t, h.st, "request")
		rec, err := h.svc.Request(ctx, in)
		switch st.want {
		case "new":
			if err != nil || rec.ID == "" || len(h.listAll(t)) != rows+1 {
				t.Fatalf("%s: Request = %q, %v, want a new row", st.name, rec.ID, err)
			}
			waitEvents(t, ch, 1, 2*time.Second)
			refs[st.argv] = rec.ID
		case "joins":
			if err != nil || rec.ID != refs[st.argv] {
				t.Fatalf("%s: Request = %q, %v, want the pending row %s", st.name, rec.ID, err, refs[st.argv])
			}
		case "refused":
			if !errors.Is(err, ErrTooManyRequests) {
				t.Fatalf("%s: Request = %q, %v, want ErrTooManyRequests", st.name, rec.ID, err)
			}
			if n, r := len(h.listAll(t)), countAuditPhase(t, h.st, "request"); n != rows || r != records {
				t.Errorf("%s: rows %d to %d, request records %d to %d, want neither to move", st.name, rows, n, records, r)
			}
		}
	}
	assertNoEvent(t, ch, 300*time.Millisecond)
}

// TestRoutableSpendsNoRate pins that Routable answers what Request would
// for the rate and spends nothing: a session's Routable calls for twelve
// new calls leave its five new rows to Request, and once those are open
// Routable refuses a new call and still passes the call that joins.
func TestRoutableSpendsNoRate(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	nova := h.seedUser(t, "nova")
	call := func(i int) RequestInput {
		in := req("s-1", nova.ID, "nova", holdSpec())
		in.ArgvHash = fmt.Sprintf("sha256:k%d", i)
		return in
	}
	for i := 1; i <= 12; i++ {
		if err := h.svc.Routable(ctx, call(i)); err != nil {
			t.Fatalf("Routable %d = %v, want nil", i, err)
		}
	}
	for i := 1; i <= 5; i++ {
		if _, err := h.svc.Request(ctx, call(i)); err != nil {
			t.Fatalf("Request %d = %v, want a new row", i, err)
		}
	}
	if err := h.svc.Routable(ctx, call(6)); !errors.Is(err, ErrTooManyRequests) {
		t.Errorf("Routable of a new call over the rate = %v, want ErrTooManyRequests", err)
	}
	if err := h.svc.Routable(ctx, call(1)); err != nil {
		t.Errorf("Routable of the pending call = %v, want nil", err)
	}
}

// listAll returns every approval row.
func (h *harness) listAll(t *testing.T) []Record {
	t.Helper()
	all, err := h.svc.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return all
}
