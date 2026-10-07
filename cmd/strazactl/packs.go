package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func packsCmd(client func() *ctl.Client) *cobra.Command {
	packs := &cobra.Command{Use: "packs", Short: "Manage knowledge packs"}
	packs.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List knowledge packs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := client().Packs(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tVERSION\tBYTES\tID")
			for _, p := range list {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", p.Name, p.Version, len(p.Content), p.ID)
			}
			return tw.Flush()
		},
	})
	var file, content, pversion string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a knowledge pack from --content or --file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file != "" {
				raw, err := os.ReadFile(file) // #nosec G304 -- user-supplied --file is the feature
				if err != nil {
					return err
				}
				content = string(raw)
			}
			if content == "" {
				return fmt.Errorf("--content or --file is required")
			}
			p, err := client().CreatePack(cmd.Context(), args[0], pversion, content)
			if err != nil {
				return err
			}
			fmt.Printf("created pack %s (%s)\n", p.Name, p.ID)
			return nil
		},
	}
	create.Flags().StringVar(&file, "file", "", "read content from file")
	create.Flags().StringVar(&content, "content", "", "inline content")
	create.Flags().StringVar(&pversion, "version", "1", "pack version")
	packs.AddCommand(create)

	packs.AddCommand(&cobra.Command{
		Use:   "bind <pack> <role>",
		Short: "Bind a knowledge pack to a role",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().BindPack(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("bound the knowledge pack %s to role %s\n", args[0], args[1])
			return nil
		},
	})
	packs.AddCommand(&cobra.Command{
		Use:   "unbind <pack> <role>",
		Short: "Unbind a knowledge pack from a role",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().UnbindPack(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("unbound the knowledge pack %s from role %s\n", args[0], args[1])
			return nil
		},
	})
	var deleteYes bool
	del := &cobra.Command{
		Use:   "delete <pack>",
		Short: "Delete a knowledge pack that no role is bound to (unbind it first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !deleteYes && !confirm(cmd, fmt.Sprintf("delete knowledge pack %s?", args[0])) {
				return errAborted()
			}
			if err := client().DeletePack(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted knowledge pack %s\n", args[0])
			return nil
		},
	}
	del.Flags().BoolVar(&deleteYes, "yes", false, "delete without the interactive confirm")
	packs.AddCommand(del)
	return packs
}
