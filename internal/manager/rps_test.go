package manager

import (
	"fmt"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestRPS pins the rate the gateway applies to each server, per session: a
// remote server whose manifest sets no limits.rps, or sets 0, gets 20 a
// second, a manifest value above 0 wins whether it is lower or higher, and a
// server on any other runtime, or one that is not installed, stays
// unlimited. Parse keeps the manifest as written, so the default never lands
// in the stored document.
func TestRPS(t *testing.T) {
	t.Parallel()
	const (
		remote  = "kind: remote\n    remote: {url: \"https://mcp.example.com/mcp\"}"
		command = "kind: command\n    command: {exec: npx, args: [\"-y\", \"files-mcp\"]}"
		oci     = "kind: oci\n    oci: {image: \"ghcr.io/example/mcp:1\"}"
	)
	cases := []struct {
		name    string
		runtime string
		limits  string
		want    float64
	}{
		{"remote with none", remote, "", 20},
		{"remote with 0", remote, "\n  limits: {rps: 0}", 20},
		{"remote with only a timeout", remote, "\n  limits: {timeoutSeconds: 60}", 20},
		{"remote with 5", remote, "\n  limits: {rps: 5}", 5},
		{"remote with 50", remote, "\n  limits: {rps: 50}", 50},
		{"remote with 0.5", remote, "\n  limits: {rps: 0.5}", 0.5},
		{"command with none", command, "", 0},
		{"command with 0", command, "\n  limits: {rps: 0}", 0},
		{"command with 5", command, "\n  limits: {rps: 5}", 5},
		{"oci with none", oci, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mf, err := Parse(fmt.Appendf(nil, `apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: rated}
server: {name: straza.test/rated, version: "1.0.0"}
straza:
  runtime:
    %s%s
`, tc.runtime, tc.limits))
			if err != nil {
				t.Fatalf("Parse refused the manifest: %v", err)
			}
			if l := mf.Straza.Limits; l != nil && tc.want == 20 && l.RPS != 0 {
				t.Fatalf("Parse wrote limits.rps = %v into the manifest, want it kept as written", l.RPS)
			}
			mgr := &Manager{byName: map[string]*instance{
				"rated": {app: store.App{ID: "app-1", Name: "rated"}, manifest: mf},
			}}
			if got := mgr.RPS("rated"); got != tc.want {
				t.Errorf("RPS = %v, want %v", got, tc.want)
			}
		})
	}

	empty := &Manager{byName: map[string]*instance{}}
	if got := empty.RPS("absent"); got != 0 {
		t.Errorf("RPS of a server that is not installed = %v, want 0", got)
	}
}
