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
		Short: "Revoke a device enrollment (it stops starting sessions)",
		Long: "Removes the device enrollment: the machine's key can no longer start sessions,\n" +
			"sessions bound to the device stop refreshing, and a live enforcement kit on it is\n" +
			"stood down by push. The user is untouched: they enroll the machine, or a\n" +
			"replacement, again with `straza enroll` and a fresh sign-in. For a person-level\n" +
			"cut, use `users disable` or `users lock`.",
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
				"revoked device %s of %s. Its key starts no more sessions. To use the machine again, enroll it again with straza enroll.\n",
				args[1], args[0])
			return nil
		},
	})

	return devices
}
