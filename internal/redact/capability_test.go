package redact

import "testing"

// TestCapability pins what reads as a generated secret in an address path:
// a long random run, 32 hex digits or more, and not an identifier, a UUID,
// a short hex id or a word.
func TestCapability(t *testing.T) {
	cases := []struct {
		name, path string
		want       bool
	}{
		{"a random run", "/services/T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3", true},
		{"a long hex run", "/hook/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b", true},
		{"a short hex id", "/v1/9f86d081884c7d65", false},
		{"a UUID", "/v1/0b9e8f4a-1c2d-4e5f-8a9b-0c1d2e3f4a5b/mcp", false},
		{"an identifier with clumped digits", "/mcp-server-2025-11-25/stream", false},
		{"plain words", "/services/github/mcp", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Capability(tc.path); got != tc.want {
				t.Errorf("Capability(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
