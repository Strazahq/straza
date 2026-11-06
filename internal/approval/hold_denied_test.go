package approval

import (
	"context"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestDeniedHoldWithinWindow pins the hold's deny-final lookup: it reports
// the latest hold of the exact user, session, rule and call when a person
// denied it and its decision window is still open, and nothing otherwise.
// Each row is a hold of nova's on r-1, and with two rows the second is
// newer. A store that cannot answer returns an error, so the PEP fails
// closed.
func TestDeniedHoldWithinWindow(t *testing.T) {
	ctx := context.Background()
	type row struct {
		state, session string
		expiresIn      time.Duration
	}
	denied := row{"denied", "s-1", time.Minute}
	cases := []struct {
		name string
		rows []row
		want bool // the last row is the one reported
	}{
		{"no hold for the call", nil, false},
		{"a person denied it inside its window", []row{denied}, true},
		{"the denial's window has passed", []row{{"denied", "s-1", -time.Second}}, false},
		{"the hold is pending", []row{{"pending", "s-1", time.Minute}}, false},
		{"the hold is approved", []row{{"approved", "s-1", time.Minute}}, false},
		{"the hold expired undecided", []row{{"expired", "s-1", -time.Second}}, false},
		{"a newer approval follows the denial", []row{denied, {"approved", "s-1", time.Minute}}, false},
		{"a newer denial follows an approval", []row{{"approved", "s-1", time.Minute}, denied}, true},
		{"another session's denial", []row{{"denied", "s-other", time.Minute}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			nova := h.seedUser(t, "nova")
			now := time.Now().UTC()
			var last store.Approval
			for _, r := range tc.rows {
				in, err := h.st.Approvals().Insert(ctx, store.Approval{
					SessionID: r.session, UserID: nova.ID, Username: "nova", RuleID: "r-1", ArgvHash: "sha256:k1",
					Lane: "hook", Class: "hold", State: r.state, DecidedBy: "u-kim", DecidedByName: "kim",
					CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(r.expiresIn),
				})
				if err != nil {
					t.Fatalf("Insert: %v", err)
				}
				last = in
			}
			rec, ok, err := h.svc.DeniedHoldWithinWindow(ctx, nova.ID, "s-1", "r-1", "sha256:k1")
			switch {
			case err != nil:
				t.Fatalf("DeniedHoldWithinWindow: %v", err)
			case ok != tc.want:
				t.Fatalf("DeniedHoldWithinWindow = %v, want %v", ok, tc.want)
			case ok && (rec.ID != last.ID || rec.State != StateDenied || rec.DecidedByName != "kim"):
				t.Errorf("record = %s/%s by %q, want the denial %s by kim", rec.ID, rec.State, rec.DecidedByName, last.ID)
			}
		})
	}

	t.Run("a store that cannot answer", func(t *testing.T) {
		h := newHarness(t)
		_ = h.st.Close()
		if _, ok, err := h.svc.DeniedHoldWithinWindow(ctx, "u-nova", "s-1", "r-1", "sha256:k1"); err == nil || ok {
			t.Fatalf("DeniedHoldWithinWindow on a closed store = %v, %v, want an error", ok, err)
		}
	})
}
