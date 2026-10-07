package agentguard_test

import (
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

// TestHintForRevokedRefusals pins that every revoked refusal the server
// sends maps to the hint for its kind: a session or token refusal to
// starting a new session, a user or device refusal to the administrator.
// The first row is the sentence older servers still send.
func TestHintForRevokedRefusals(t *testing.T) {
	for _, tc := range []struct {
		reason, wantIn string
	}{
		{"Straza: your session has been revoked. Re-enroll", "start a new one"},
		{"Straza: this session has been revoked. Start a new session: its check-in uses this device's existing enrollment, so re-enrolling is not needed", "start a new one"},
		{"Straza: this session has been revoked, so its audit events are refused and stay in the spool. Start a new session: its check-in uses this device's existing enrollment, and the spool uploads under it", "start a new one"},
		{"Straza: this device or user has been revoked. Contact your administrator", "Contact your administrator"},
		{"Straza: this device or user has been revoked, so its audit events are refused. Contact your administrator", "Contact your administrator"},
		{"Straza: session renewal was refused: this device or user has been revoked. Contact your administrator", "Contact your administrator"},
	} {
		if got := agentguard.HintFor(tc.reason); !strings.Contains(got, tc.wantIn) {
			t.Errorf("HintFor(%q) = %q, want a hint containing %q", tc.reason, got, tc.wantIn)
		}
	}
}
