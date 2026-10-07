package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func catalogCmd(client func() *ctl.Client) *cobra.Command {
	catalog := &cobra.Command{Use: "catalog", Short: "Inspect the effective tool catalog"}

	var roles []string
	var user, app string
	preview := &cobra.Command{
		Use:   "preview",
		Short: "Preview the tool catalog a subject would see: named roles, a real user's roles, or both",
		Long: "Prints one row per tool, or one row per server when the outcome is server-wide.\n" +
			"STATUS is visible, approve_gated, hidden_policy, no_binding, matcher_miss or\n" +
			"not_running, and REASON says it in words. At least one --role or --user is required.",
		Example: "  strazactl catalog preview --role scout-role --app scout-tools\n" +
			"  strazactl catalog preview --user nina-data-analyst-agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(roles) == 0 && user == "" {
				return fmt.Errorf("at least one --role or --user is required")
			}
			p, err := client().CatalogPreview(cmd.Context(), roles, user, app)
			if err != nil {
				return err
			}
			return ctl.RenderCatalogPreview(os.Stdout, p)
		},
	}
	preview.Flags().StringArrayVar(&roles, "role", nil, "role to include in the subject (repeatable)")
	preview.Flags().StringVar(&user, "user", "", "resolve the subject from an existing username")
	preview.Flags().StringVar(&app, "app", "", "filter to one server")
	catalog.AddCommand(preview)

	return catalog
}
