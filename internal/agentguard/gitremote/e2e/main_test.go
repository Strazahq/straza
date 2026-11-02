// Package e2e runs real git through git-remote-straza, a user-mode
// `straza service`, a real strazad whose git grant routes a fake answers,
// and a fake resource server in front of `git upload-pack` and
// `git receive-pack --stateless-rpc`.
package e2e

import (
	"os"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/gitremote"
)

// TestMain makes this test binary git's remote helper when git starts it
// through the git-remote-straza symlink the rig puts on PATH, the same
// argv[0] dispatch cmd/straza does.
func TestMain(m *testing.M) {
	if gitremote.Invoked(os.Args[0]) {
		os.Exit(gitremote.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}
