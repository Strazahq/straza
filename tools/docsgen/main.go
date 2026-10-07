// Command docsgen writes the generated reference pages of the public docs: the
// configuration page from the knob table in internal/config and the API page
// from the OpenAPI document in pkg/api, so neither page can drift from its
// source.
//
// Usage: go run ./tools/docsgen config OUTFILE writes the configuration page,
// normally website/content/reference/configuration.md, and go run
// ./tools/docsgen api OUTFILE writes the API page, normally
// website/content/reference/api.md, reading pkg/api/openapi.yaml relative to
// the working directory, which is the repository root under make. Both
// outputs are deterministic, and the commit gate refuses a commit where a
// committed page differs from a fresh run. The CLI reference pages come from
// the gen-docs command each binary carries under the docsgen build tag; the
// shared page writer is the clidoc package beside this one, and the skills
// mode rebuilds the served agent skill bundle under website/static.
package main

import (
	"fmt"
	"os"

	"github.com/strazahq/straza/internal/config"
)

const usage = "usage: go run ./tools/docsgen config OUTFILE, go run ./tools/docsgen api OUTFILE, or go run ./tools/docsgen skills OUTDIR (OUTFILE is the page to write, normally website/content/reference/configuration.md or website/content/reference/api.md; the api mode reads pkg/api/openapi.yaml from the working directory; the skills mode rebuilds OUTDIR/skills and OUTDIR/claude-code, normally under website/static/.well-known, from plugins/)"

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var page string
	var err error
	switch os.Args[1] {
	case "skills":
		if err := writeSkillsTree(".", os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "docsgen: %v\n", err)
			os.Exit(1)
		}
		return
	case "config":
		page, err = renderConfigPage(config.Knobs(), config.KnobSections())
	case "api":
		var document []byte
		if document, err = os.ReadFile(openAPIPath); err == nil {
			page, err = renderAPIPage(document)
		}
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "docsgen: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], []byte(page), 0o644); err != nil { //nolint:gosec // G306: a docs page is world-readable by design
		fmt.Fprintf(os.Stderr, "docsgen: %v\n", err)
		os.Exit(1)
	}
}
