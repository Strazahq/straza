package approval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// errResolver answers every role resolution with err.
type errResolver struct{ err error }

func (r errResolver) ResolveRoles(context.Context, string, time.Time) ([]store.Role, error) {
	return nil, r.err
}

// TestDecidableTouchesOnlyADecider pins the device lane's read for a user
// who decides nothing: an AI agent and a user whose row cannot be read get
// no rows, no challenge and no touch of the device's last-seen time. A
// failed role resolution is an error on both reads of the queue, with no
// challenge and no touch. A person who may decide gets a row with a
// challenge and a touched device, the instruments' positive control.
func TestDecidableTouchesOnlyADecider(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	person := h.seedUser(t, "secops", "sec-approvers")
	nhi := h.seedNHI(t, "robot", "sec-approvers")
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	if _, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec)); err != nil {
		t.Fatal(err)
	}
	far := time.Now().Add(24 * time.Hour)
	resolveErr := errors.New("the directory is down")

	cases := []struct {
		name    string
		user    store.User
		swap    func()
		wantErr error
		// wantRows is the rows Decidable answers, each with a challenge.
		wantRows int
	}{
		{name: "an AI agent", user: nhi},
		{name: "a user whose row cannot be read", user: person,
			swap: func() { h.svc.st = &flakyStore{Store: h.st, userErr: pgDown} }},
		{name: "a failed role resolution", user: person, wantErr: resolveErr,
			swap: func() { h.svc.resolver = errResolver{resolveErr} }},
		{name: "a person who may decide", user: person, wantRows: 1},
	}
	for _, tc := range cases {
		_, spki := genP256(t)
		deviceID := h.enroll(t, tc.user.ID, spki)
		if _, err := h.st.Approvers().PurgeChallengesBefore(ctx, far); err != nil {
			t.Fatal(err)
		}
		if tc.swap != nil {
			tc.swap()
		}
		rows, err := h.svc.Decidable(ctx, tc.user.ID, deviceID)
		_, rowsErr := h.svc.DecidableRows(ctx, tc.user.ID)
		h.svc.st, h.svc.resolver = h.st, h.resolver

		if !errors.Is(err, tc.wantErr) || !errors.Is(rowsErr, tc.wantErr) {
			t.Errorf("%s: Decidable err %v, DecidableRows err %v, want %v from both", tc.name, err, rowsErr, tc.wantErr)
		}
		if len(rows) != tc.wantRows {
			t.Errorf("%s: Decidable = %d rows, want %d", tc.name, len(rows), tc.wantRows)
		}
		for _, r := range rows {
			if r.Challenge == "" {
				t.Errorf("%s: a decidable row has no challenge", tc.name)
			}
		}
		challenges, err := h.st.Approvers().PurgeChallengesBefore(ctx, far)
		if err != nil {
			t.Fatal(err)
		}
		dev, err := h.st.Approvers().GetDevice(ctx, deviceID)
		if err != nil {
			t.Fatal(err)
		}
		if decider := tc.wantRows > 0; int(challenges) != tc.wantRows || (dev.LastSeenAt != nil) != decider {
			t.Errorf("%s: %d challenges and last_seen %v, want %d challenges and a touch only for a decider", tc.name, challenges, dev.LastSeenAt, tc.wantRows)
		}
	}
}
