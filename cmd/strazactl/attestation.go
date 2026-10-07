package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func attestationCmd(client func() *ctl.Client) *cobra.Command {
	att := &cobra.Command{Use: "attestation", Short: "Manage the managed-install expected-hash registry"}

	att.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List registered measurements",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, err := client().AttestationHashes(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tARTIFACT\tHARNESS\tPLATFORM\tHASH\tNOTE")
			for _, h := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", h.ID, h.Artifact, h.Harness, h.Platform, h.Hash, h.Note)
			}
			return tw.Flush()
		},
	})

	var artifact, harness, platform, hash, note string
	add := &cobra.Command{
		Use:   "add",
		Short: "Register an expected measurement (allowed-set entry)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			created, err := client().AddAttestationHash(cmd.Context(), ctl.AttestationHash{
				Artifact: artifact, Harness: harness, Platform: platform, Hash: hash, Note: note,
			})
			if err != nil {
				return err
			}
			fmt.Printf("registered %s (%s)\n", created.Artifact, created.ID)
			return nil
		},
	}
	add.Flags().StringVar(&artifact, "artifact", "", "measurement key: self | config | hooks.<harness>")
	add.Flags().StringVar(&harness, "harness", "", "restrict to one harness (default: all)")
	add.Flags().StringVar(&platform, "platform", "", "restrict to one GOOS/GOARCH (default: all)")
	add.Flags().StringVar(&hash, "hash", "", "sha256:<hex> measurement")
	add.Flags().StringVar(&note, "note", "", "free-form note (e.g. straza version)")
	_ = add.MarkFlagRequired("artifact")
	_ = add.MarkFlagRequired("hash")
	att.AddCommand(add)

	att.AddCommand(&cobra.Command{
		Use:   "rm <id>",
		Short: "Remove a measurement from the registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().RemoveAttestationHash(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", args[0])
			return nil
		},
	})
	return att
}
