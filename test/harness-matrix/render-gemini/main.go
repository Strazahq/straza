// render-gemini writes a gemini settings.json exactly as straza's installer
// writes it, by calling the same exported writers (InstallHooks /
// InstallManagedHooks), never by imitating their output. Sibling of render/
// (codex) and used for the same reason: the live gates need the installer's
// real file shape with a hook binary THIS lane controls (the sentinel), and
// the full `straza install --managed` path additionally stages a binary copy
// and needs a reachable strazad for snapshot keys, which the file-shape gates
// do not. If the installer's shape changes, this renders the new shape
// automatically, so the gates track the code, never a golden copy.
//
// -managed selects InstallManagedHooks, which for gemini additionally pins
// hooksConfig.enabled = true (install.go's system-scope kill-switch defeat).
// -seed merges a JSON fragment in FIRST, so a case can plant the auth type or
// a user-scope hooksConfig.enabled=false under the same merge path an
// operator's own settings would take.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/strazahq/straza/internal/agentguard"
)

func main() {
	out := flag.String("out", "", "settings.json path to write")
	bin := flag.String("bin", "", "hook binary path the hook commands invoke")
	managed := flag.Bool("managed", false, "write the MANAGED/system shape (adds the hooksConfig.enabled pin)")
	seed := flag.String("seed", "", "JSON object merged into the file before the installer runs (case fixture)")
	flag.Parse()
	if *out == "" || *bin == "" {
		fmt.Fprintln(os.Stderr, "render-gemini: -out and -bin are required")
		os.Exit(2)
	}

	if *seed != "" {
		// The seed is written with the scope's own mode, so installHooks'
		// "an existing file keeps the operator's permissions" branch does not
		// hand a managed file a user-scope 0600 (a 0600 managed file is the
		// silent fleet-wide breakage InstallManagedHooks documents).
		dirMode, fileMode := os.FileMode(0o750), os.FileMode(0o600)
		if *managed {
			dirMode, fileMode = 0o755, 0o644
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(*seed), &doc); err != nil {
			fmt.Fprintf(os.Stderr, "render-gemini: -seed is not a JSON object: %v\n", err)
			os.Exit(2)
		}
		if err := os.MkdirAll(filepath.Dir(*out), dirMode); err != nil { // #nosec G301 -- managed layout is deliberately world-readable
			fmt.Fprintf(os.Stderr, "render-gemini: %v\n", err)
			os.Exit(1)
		}
		raw, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "render-gemini: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out, raw, fileMode); err != nil { // #nosec G306 -- mode is the scope's choice, mirroring installHooks
			fmt.Fprintf(os.Stderr, "render-gemini: %v\n", err)
			os.Exit(1)
		}
	}

	install := agentguard.InstallHooks
	if *managed {
		install = agentguard.InstallManagedHooks
	}
	if err := install("gemini", *out, *bin); err != nil {
		fmt.Fprintf(os.Stderr, "render-gemini: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("rendered %s (managed=%v, bin=%s)\n", *out, *managed, *bin)
}
