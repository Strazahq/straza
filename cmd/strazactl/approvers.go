package main

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// approversCmd is the operator surface for enrolled mobile approver devices
// (the phones that receive approval requests, distinct from `approvals`, the
// requests they decide). The enroll response goes to the phone, not the
// admin, so `list` is how an admin learns the apd_ id that revoke takes. Its
// push-route column attributes the channel-status "enrolled devices vs
// push routes" diagnostic to a specific phone.
func approversCmd(client func() *ctl.Client) *cobra.Command {
	approvers := &cobra.Command{Use: "approvers", Short: "Inspect and revoke enrolled approver phones and browsers"}

	approvers.AddCommand(&cobra.Command{
		Use:   "list [username]",
		Short: "List enrolled approver phones and browsers, with owner, key posture and push routes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			user := ""
			if len(args) == 1 {
				user = args[0]
			}
			devs, err := client().ApproverDevices(cmd.Context(), user)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tUSER\tNAME\tPLATFORM\tKEY\tATTESTATION\tPUSH\tENROLLED\tLAST SEEN")
			for _, d := range devs {
				user := d.Username
				if user == "" {
					user = d.UserID
				}
				// A phone without a push route can decide but is never notified,
				// the exact silent-approver condition the channel status can only
				// count, not attribute. Say it on the row.
				push := fmt.Sprintf("%d", d.PushRoutes)
				if d.PushRoutes == 0 {
					push = "none (never notified)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					d.ID, user, d.Name, d.Platform, d.KeySecurityLevel, d.Attestation,
					push, approverTime(d.EnrolledAt), approverTime(d.LastSeen))
			}
			return tw.Flush()
		},
	})

	approvers.AddCommand(&cobra.Command{
		Use:   "revoke <device-id>",
		Short: "Revoke an enrolled approver device (its approver credential fails on the next call)",
		Long: "Deletes the approver-device enrollment (the apd_ id from `approvers list`).\n" +
			"Row-backed and immediate: the phone's use=approver token fails on its very\n" +
			"next call, and its push registrations stop routing. The user is untouched:\n" +
			"re-enrolling takes a fresh one-time QR (`approvals enroll-token <username>`).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().RevokeApproverDevice(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"revoked approver device %s: its credential is dead; re-enrolling needs a fresh QR\n", args[0])
			return nil
		},
	})

	return approvers
}

// approverTime renders an RFC 3339 server timestamp for the table; "-" when
// absent or unparseable (last_seen is nullable, a phone that never called).
func approverTime(ts string) string {
	if ts == "" {
		return "-"
	}
	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "-"
	}
	return at.Format("2006-01-02 15:04:05")
}
