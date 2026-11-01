package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func approvalsCmd(client func() *ctl.Client) *cobra.Command {
	approvals := &cobra.Command{Use: "approvals", Short: "List approval requests"}

	var state string
	var wide bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List approval requests (default: pending)",
		Long: "Prints one row per request, a held call and a ticket alike, with the columns ID,\n" +
			"STATE, REQUESTER, SUMMARY, ROLES, EXPIRES and DECIDED BY. ROLES names the roles\n" +
			"whose holders may decide it. --wide prints each request as a block instead.\n" +
			"One line on stderr then says where a person decides: in the console, on the\n" +
			"self-service page or on the phone.",
		Example: "  strazactl approvals list\n" +
			"  strazactl approvals list --wide",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state == "" {
				state = "pending"
			}
			c := client()
			recs, err := c.Approvals(cmd.Context(), state)
			if err != nil {
				return err
			}
			if wide {
				printApprovalsWide(os.Stdout, recs)
			} else {
				tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tSTATE\tREQUESTER\tSUMMARY\tROLES\tEXPIRES\tDECIDED BY")
				for _, r := range recs {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						r.ID, r.State, r.Username, r.Summary,
						strings.Join(r.ApproverRoles, ","), expiresIn(r.ExpiresAt), r.DecidedByName)
				}
				if err := tw.Flush(); err != nil {
					return err
				}
			}
			// Stdout is the table a script reads, so the way to decide goes to
			// stderr, after it.
			fmt.Fprintln(cmd.ErrOrStderr(), decideWhere(c.Base))
			return nil
		},
	}
	list.Flags().StringVar(&state, "state", "pending", "which requests to list: pending, approved, denied, expired or all")
	list.Flags().BoolVar(&wide, "wide", false, "detail view: add the redacted call preview and the line that says what an approval covers")
	approvals.AddCommand(list)

	// The hidden approve and deny take whatever the old verbs took and send
	// nothing. Hidden keeps them off the help, the generated pages and the skill.
	for _, verb := range []string{"approve", "deny"} {
		approvals.AddCommand(&cobra.Command{
			Use:                verb,
			Hidden:             true,
			DisableFlagParsing: true,
			Annotations:        local,
			RunE: func(*cobra.Command, []string) error {
				return exitCodeErr{code: 2, err: errNoDecisions}
			},
		})
	}

	// The hidden enroll-token takes whatever the old verb took and sends
	// nothing, like approve and deny.
	approvals.AddCommand(&cobra.Command{
		Use:                "enroll-token",
		Hidden:             true,
		DisableFlagParsing: true,
		Annotations:        local,
		RunE: func(*cobra.Command, []string) error {
			return exitCodeErr{code: 2, err: errNoEnroll}
		},
	})

	return approvals
}

// errNoDecisions answers the retired approve and deny.
var errNoDecisions = errors.New("strazactl no longer decides requests, because it runs on an admin API token, which is not a person, " +
	"and a decision must name the person who made it. Decide in the console under Approvals, on the self-service page, or on your phone")

// errNoEnroll answers the retired enroll-token.
var errNoEnroll = errors.New("strazactl no longer enrolls approver phones, because a phone or browser that decides as a person " +
	"is enrolled only by whoever is signed in as that person on the self-service page. Give the person the role " +
	"straza-enroll-mobile in your identity manager, and they choose Add a phone on the self-service page")

// decideWhere is the line approvals list ends with: where a person decides a
// request at server, in the console, on the self-service page or on the phone.
func decideWhere(server string) string {
	return "A person decides these requests in the console under Approvals at " + server + "/console/approvals, " +
		"on the Requests tab of the self-service page at " + server + "/self-service/requests, or on their phone."
}

// printApprovalsWide renders the detail view: the scannable one-liner per record
// plus, when present, the redacted call-parameters preview and the
// server-composed honesty line. The list view does not show them. Params come
// first (the server's own record), then the unverified justification, then
// the honesty line beneath both.
func printApprovalsWide(w io.Writer, recs []ctl.ApprovalInfo) {
	for i, r := range recs {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s  [%s]  %s  %s\n", r.ID, r.State, r.Username, r.Summary)
		fmt.Fprintf(w, "  roles: %s   expires: %s\n",
			strings.Join(r.ApproverRoles, ","), expiresIn(r.ExpiresAt))
		if r.ArgsPreview != "" {
			fmt.Fprintln(w, "  parameters (preview):")
			for _, line := range strings.Split(r.ArgsPreview, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
			if r.ArgsTruncated {
				fmt.Fprintf(w, "    (preview truncated; %d bytes)\n", r.ArgsBytes)
			}
		}
		if r.Justification != "" {
			fmt.Fprintf(w, "  stated reason (unverified): %s\n", r.Justification)
		}
		if r.ArgsPreview != "" {
			fmt.Fprintf(w, "  %s\n", honestyLine(r.BindingScope, r.ArgvHashPrefix))
		}
	}
}

// honestyLine composes the "preview only" caption locally (kept in step with
// approval.HonestyLine; reproduced here so strazactl does not pull the
// server-side approval package into its binary).
func honestyLine(scope, prefix string) string {
	binds := "the exact call"
	if scope == "tool_identity" {
		binds = "tool identity"
	}
	return "preview only; this approval covers " + binds + " (sha256:" + prefix + ")"
}

// expiresIn renders an RFC 3339 expiry as a relative "in 42s"; "-" when past
// or unparseable.
func expiresIn(ts string) string {
	if ts == "" {
		return "-"
	}
	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "-"
	}
	d := time.Until(at).Round(time.Second)
	if d <= 0 {
		return "expired"
	}
	return "in " + d.String()
}
