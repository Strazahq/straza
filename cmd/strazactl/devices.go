package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// devicesCmd is the operator surface for device enrollments: list a user's
// enrolled machines, and revoke one. Revoking one device is the lever for a
// lost laptop or a leaver's workstation without disabling the whole user.
// Operands are the username (as everywhere else in the CLI) plus, for revoke,
// the device id `devices list` printed.
func devicesCmd(client func() *ctl.Client) *cobra.Command {
	devices := &cobra.Command{Use: "devices", Short: "Inspect and revoke a user's enrolled devices"}

	devices.AddCommand(&cobra.Command{
		Use:   "list <username>",
		Short: "List a user's enrolled devices",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := client().UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			devs, err := client().UserDevices(cmd.Context(), u.ID)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tPLATFORM\tSTATUS\tENROLLED")
			for _, d := range devs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					d.ID, d.Name, d.Platform, d.Status, d.EnrolledAt.Format("2006-01-02 15:04:05"))
			}
			return tw.Flush()
		},
	})

	devices.AddCommand(&cobra.Command{
		Use:   "revoke <username> <device-id>",
		Short: "Revoke a device enrollment (its device credential stops minting sessions)",
		Long: "Removes the device enrollment: the long-lived device credential minted at\n" +
			"enroll can no longer start sessions, sessions bound to the device stop\n" +
			"refreshing, and a live enforcement kit on it is stood down by push. The user\n" +
			"is untouched: they re-enroll the machine (or a replacement) with a fresh\n" +
			"interactive login. For a person-level cut, use `users disable` or `users lock`.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := client().UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := client().RevokeDevice(cmd.Context(), u.ID, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"revoked device %s of %s: its device credential is dead; re-enrolling needs a fresh login\n",
				args[1], args[0])
			return nil
		},
	})

	return devices
}
