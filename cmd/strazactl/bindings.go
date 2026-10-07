package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// errBindingsApplyGone answers every invocation of the removed bindings
// apply, so an older skill, page or script learns where access rows change
// now instead of meeting a flag error.
var errBindingsApplyGone = errors.New("strazactl bindings apply is gone: access rows now change through a draft that a person publishes. " +
	"Put the Role documents in a file, run strazactl drafts check -f <file>, then strazactl drafts create -f <file>")

func bindingsCmd(client func() *ctl.Client) *cobra.Command {
	bindings := &cobra.Command{
		Use:   "bindings",
		Short: "List the access rows of roles on servers",
		Long: "Lists the access rows of roles on servers. A role's access row as code lives in its\n" +
			"Role document: strazactl roles export prints it, and strazactl drafts create -f\n" +
			"proposes a changed one, which a person then publishes.",
	}

	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List the access rows",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listJSON {
				raw, err := client().BindingsJSON(cmd.Context())
				if err != nil {
					return err
				}
				return printServerJSON(cmd.OutOrStdout(), raw)
			}
			bs, err := client().Bindings(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSERVER\tROLE\tTOOLS")
			for _, b := range bs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.ID, b.App, b.Role, strings.Join(b.Tools, ","))
			}
			return tw.Flush()
		},
	}
	list.Flags().BoolVar(&listJSON, "json", false, jsonFlagUsage)
	bindings.AddCommand(list)
	// The hidden apply takes whatever the old verb took and sends nothing.
	// Hidden keeps it off the help, the generated pages and the skill.
	bindings.AddCommand(&cobra.Command{
		Use:                "apply",
		Hidden:             true,
		DisableFlagParsing: true,
		Annotations:        local,
		RunE: func(*cobra.Command, []string) error {
			return exitCodeErr{code: 2, err: errBindingsApplyGone}
		},
	})
	return bindings
}
