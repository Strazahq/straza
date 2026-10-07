package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func sessionsCmd(client func() *ctl.Client) *cobra.Command {
	sessions := &cobra.Command{Use: "sessions", Short: "Inspect and revoke sessions"}
	var status string
	list := &cobra.Command{
		Use:   "list",
		Short: "List sessions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := client().Sessions(cmd.Context(), status)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tUSER\tHARNESS\tCLIENT\tATTESTATION\tWIRING\tSTATUS\tLAST SEEN")
			for _, s := range list {
				user := s.Username
				if user == "" {
					user = s.UserID
				}
				wiring := s.WiringStatus
				if wiring == "" {
					wiring = "-" // admin harness, or a pre-0.55.0 server
				}
				client := s.ClientVersion
				if client == "" {
					client = "-" // a client older than the stamp
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					s.ID, user, s.Harness, client, s.Attestation, wiring, s.Status, s.LastSeen.Format("2006-01-02 15:04:05"))
			}
			return tw.Flush()
		},
	}
	list.Flags().StringVar(&status, "status", "", "filter: active|revoked|closed")
	sessions.AddCommand(list)

	sessions.AddCommand(&cobra.Command{
		Use:   "transcript <session-id>",
		Short: "Print a session's recorded conversation (recording is policy-opted-in)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tr, err := client().SessionTranscript(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if len(tr.Turns) == 0 {
				fmt.Println("no recorded turns for this session (recording off, or nothing said)")
				return nil
			}
			who := tr.Username
			if who == "" {
				who = "unknown"
			}
			fmt.Printf("session %s: %s, %d turns\n\n", tr.SessionID, who, len(tr.Turns))
			for _, turn := range tr.Turns {
				marker := ""
				if turn.Truncated {
					marker = " [truncated, full hash " + turn.ContentHash + "]"
				}
				fmt.Printf("[%s] %s (%s)%s\n%s\n\n",
					turn.At.Format("15:04:05"), turn.Kind, turn.Mode, marker, turn.Content)
			}
			return nil
		},
	})

	sessions.AddCommand(&cobra.Command{
		Use:   "revoke <session-id>",
		Short: "Revoke a session (kill switch)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().RevokeSession(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("revoked %s\n", args[0])
			return nil
		},
	})
	return sessions
}
