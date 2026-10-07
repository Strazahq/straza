package main

import (
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

// TestStatusIdentityLine pins the two identity rows of `straza status`: an
// enrolled device names its device id, and a headless enrollment says it is
// headless with no device instead of printing an empty parenthetical.
func TestStatusIdentityLine(t *testing.T) {
	tests := []struct {
		name string
		id   agentguard.Identity
		want string
	}{
		{"enrolled device", agentguard.Identity{Username: "alice", DeviceID: "dev-7f3a"},
			"identity   alice (device dev-7f3a)"},
		{"headless key lane", agentguard.Identity{Username: "ci-bot", Headless: agentguard.HeadlessKey},
			"identity   ci-bot (headless, nhi-key lane, no device)"},
		{"headless client secret lane", agentguard.Identity{Username: "ci-bot", Headless: agentguard.HeadlessClientCreds},
			"identity   ci-bot (headless, client-credentials lane, no device)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := identityLine(tt.id); got != tt.want {
				t.Errorf("identityLine(%+v) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}
