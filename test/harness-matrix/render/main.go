// render writes codex's managed requirements.toml exactly as `straza install
// --managed codex` writes it, by calling the same exported generator
// (InstallCodexManagedHooks), not by imitating its output. The full managed
// install additionally stages a binary copy and needs a reachable strazad for
// snapshot keys; the harness-matrix gates only need the FILE, so they call the
// file's own writer. If the installer's block shape changes, this renders the
// new shape automatically, so the gate tracks the code, never a golden copy.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/strazahq/straza/internal/agentguard"
)

func main() {
	out := flag.String("out", "", "requirements.toml path to write")
	bin := flag.String("bin", "", "hook binary path the block's commands invoke")
	flag.Parse()
	if *out == "" || *bin == "" {
		fmt.Fprintln(os.Stderr, "render: -out and -bin are required")
		os.Exit(2)
	}
	changed, err := agentguard.InstallCodexManagedHooks(*out, *bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "render: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("rendered %s (changed=%v)\n", *out, changed)
}
