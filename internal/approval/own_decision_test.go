package approval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// TestDecideErrorTextOwnDecision pins that Slack tells the requester where
// their own request can be decided instead of answering "internal error".
func TestDecideErrorTextOwnDecision(t *testing.T) {
	got := decideErrorText(ErrUnsignedOwnDecision)
	for _, want := range []string{"your own request", "enrolled phone", "This browser"} {
		if !strings.Contains(got, want) {
			t.Errorf("decideErrorText = %q, want it to name %q", got, want)
		}
	}
}

// TestOwnDecisionNeedsSignedDevice pins who may decide a request they raised
// themselves: a decision that carries an enrolled device's id is accepted, an
// unsigned one is refused unless the config switch is on, and the rule never
// touches a decision made by somebody else.
func TestOwnDecisionNeedsSignedDevice(t *testing.T) {
	confirmSpec := policy.ApproveSpec{TimeoutSeconds: 90, RetryTTLSeconds: 60}
	selfSpec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, SelfApproval: true, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	fourEyesSpec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}

	tests := []struct {
		name      string
		confirm   bool
		spec      policy.ApproveSpec
		switchOn  bool
		byOther   bool
		deviceID  string
		wantError error
	}{
		{name: "confirm, unsigned, switch off", confirm: true, spec: confirmSpec, wantError: ErrUnsignedOwnDecision},
		{name: "confirm, signed", confirm: true, spec: confirmSpec, deviceID: "dev-1"},
		{name: "confirm, unsigned, switch on", confirm: true, spec: confirmSpec, switchOn: true},
		{name: "selfApproval, unsigned, switch off", spec: selfSpec, wantError: ErrUnsignedOwnDecision},
		{name: "selfApproval, signed", spec: selfSpec, deviceID: "dev-1"},
		{name: "selfApproval, unsigned, switch on", spec: selfSpec, switchOn: true},
		{name: "four eyes keeps its own refusal", spec: fourEyesSpec, wantError: ErrSelfApproval},
		{name: "four eyes keeps its own refusal when signed", spec: fourEyesSpec, deviceID: "dev-1", wantError: ErrSelfApproval},
		{name: "another person decides unsigned", spec: fourEyesSpec, byOther: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.svc.cfg.UnsignedOwnDecisions = tc.switchOn
			ctx := context.Background()
			requester := h.seedUser(t, "nova", "sec-approvers")
			other := h.seedUser(t, "kim", "sec-approvers")

			in := req("s-1", requester.ID, "nova", tc.spec)
			in.Confirm = tc.confirm
			rec, err := h.svc.Request(ctx, in)
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			decider, channel := requester.ID, "console"
			if tc.byOther {
				decider = other.ID
			}
			if tc.deviceID != "" {
				channel = "phone"
			}
			out, err := h.svc.Decide(ctx, rec.ID, "approved", decider, channel, "", tc.deviceID)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("Decide = %v, want %v", err, tc.wantError)
			}
			if tc.wantError == nil && out.State != StateApproved {
				t.Errorf("state = %q, want approved", out.State)
			}
			if tc.wantError != nil {
				if cur, _ := h.svc.Get(ctx, rec.ID); cur.State != StatePending {
					t.Errorf("a refused decision left state %q, want pending", cur.State)
				}
			}
		})
	}
}
