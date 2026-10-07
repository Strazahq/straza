//go:build docsgen

package main

import "github.com/strazahq/straza/tools/docsgen/clidoc"

// The hidden gen-docs command writes the CLI reference pages of this binary.
// It exists only in a docsgen-tagged build, so the shipped straza carries
// neither the command nor Cobra's doc package.
func init() {
	devCommands = append(devCommands, clidoc.Command())
}
