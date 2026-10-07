// render-claude writes claude-code's user-scope settings.json exactly as
// `straza install claude-code` writes it, by calling the same exported writer
// (agentguard.InstallHooks), not by imitating its output. The live gates need
// the installer's REAL file shape while pointing the hook command at the
// sentinel recorder instead of straza, and `straza install` always names its
// own executable (os.Executable), so it cannot be asked for that file. Same
// contract as render/ (the codex managed block): if the installer's wiring
// changes, this renders the new shape automatically, so the gate tracks the
// code, never a golden copy.
//
// The static gate deliberately does NOT use this tool: it asserts the file the
// real `straza install claude-code` binary wrote under a redirected
// CLAUDE_CONFIG_DIR, so the path resolution (SettingsPath) is under test too.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/strazahq/straza/internal/agentguard"
)

func main() {
	out := flag.String("out", "", "settings.json path to write")
	bin := flag.String("bin", "", "hook binary path the wiring invokes")
	flag.Parse()
	if *out == "" || *bin == "" {
		fmt.Fprintln(os.Stderr, "render-claude: -out and -bin are required")
		os.Exit(2)
	}
	if err := agentguard.InstallHooks("claude-code", *out, *bin); err != nil {
		fmt.Fprintf(os.Stderr, "render-claude: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("rendered %s\n", *out)
}
