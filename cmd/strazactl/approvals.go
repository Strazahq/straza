package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
	"github.com/strazahq/straza/internal/qrterm"
)

func approvalsCmd(client func() *ctl.Client) *cobra.Command {
	approvals := &cobra.Command{Use: "approvals", Short: "List and decide approval requests, and mint approver enroll tokens"}

	var state string
	var wide bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List approval requests (default: pending)",
		Long: "Prints one row per request, a held call and a ticket alike, with the columns ID,\n" +
			"STATE, REQUESTER, SUMMARY, ROLES, EXPIRES and DECIDED BY. ROLES names the roles\n" +
			"whose holders may decide it. --wide prints each request as a block instead.",
		Example: "  strazactl approvals list\n" +
			"  strazactl approvals list --wide",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state == "" {
				state = "pending"
			}
			recs, err := client().Approvals(cmd.Context(), state)
			if err != nil {
				return err
			}
			if wide {
				return printApprovalsWide(os.Stdout, recs)
			}
			tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATE\tREQUESTER\tSUMMARY\tROLES\tEXPIRES\tDECIDED BY")
			for _, r := range recs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					r.ID, r.State, r.Username, r.Summary,
					strings.Join(r.ApproverRoles, ","), expiresIn(r.ExpiresAt), r.DecidedByName)
			}
			return tw.Flush()
		},
	}
	list.Flags().StringVar(&state, "state", "pending", "which requests to list: pending, approved, denied, expired or all")
	list.Flags().BoolVar(&wide, "wide", false, "detail view: add the redacted call preview and the line that says what an approval covers")
	approvals.AddCommand(list)

	var approveReason string
	approve := &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve a pending request",
		Example: "  strazactl approvals list\n" +
			"  strazactl approvals approve 01a0b944-51a6-7a98-959c-40dddb8d86f9 --reason \"Change window CR-2041 is open until 18:00\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := client().ApproveApproval(cmd.Context(), args[0], approveReason)
			if err != nil {
				return err
			}
			fmt.Printf("approved %s (by %s)\n", rec.ID, rec.DecidedByName)
			return nil
		},
	}
	approve.Flags().StringVar(&approveReason, "reason", "", "your own words, recorded on the request (optional, ≤500 bytes)")
	approvals.AddCommand(approve)

	var denyReason string
	deny := &cobra.Command{
		Use:   "deny <id>",
		Short: "Deny a pending request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := client().DenyApproval(cmd.Context(), args[0], denyReason)
			if err != nil {
				return err
			}
			fmt.Printf("denied %s (by %s)\n", rec.ID, rec.DecidedByName)
			return nil
		},
	}
	deny.Flags().StringVar(&denyReason, "reason", "", "your own words, recorded on the request (optional, ≤500 bytes)")
	approvals.AddCommand(deny)

	approvals.AddCommand(&cobra.Command{
		Use:   "enroll-token <username>",
		Short: "Mint a one-time mobile-approver enroll token for a user, and draw its QR",
		Long: "Mints a one-time enroll token for the named user and prints everything the\n" +
			"approver app needs to pair: the token, the project, the server list the app\n" +
			"tries, the TLS pin status, and the raw QR payload.\n\n" +
			"Both streams, every run: the fields go to STDOUT as stable text to pipe or\n" +
			"grep, and the scannable QR is drawn on STDERR, so `2>/dev/null` mutes the\n" +
			"QR without touching what a script reads, and a headless box never needs the\n" +
			"web console to enroll a phone.\n\n" +
			"The token is single-use and expires in minutes (the output says how many):\n" +
			"mint a fresh one per phone, at the phone.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The ctl.Client's exported Do handles admin auth + refresh; the
			// server accepts username or user_id (we send username).
			var out enrollToken
			if err := client().Do(cmd.Context(), http.MethodPost, "/v1/admin/approvers/enroll-token",
				map[string]string{"username": args[0]}, &out); err != nil {
				return err
			}
			printEnrollToken(cmd.OutOrStdout(), cmd.ErrOrStderr(), out)
			return nil
		},
	})

	return approvals
}

// enrollToken is the POST /v1/admin/approvers/enroll-token response, as much of
// it as the CLI renders (server side: internal/server/approver_enroll.go).
type enrollToken struct {
	EnrollToken string   `json:"enroll_token"`
	ExpiresIn   int      `json:"expires_in"`
	Servers     []string `json:"servers"`
	TLSSPKIPin  string   `json:"tls_spki_pin"`
	QRPayload   string   `json:"qr_payload"`
	Project     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
}

// printEnrollToken writes a minted enrolment to two streams: the fields to
// stdout, the scannable QR to stderr. Stdout is the contract, so its bytes
// stay put for anything piping or grepping the payload; drawing the QR on
// stderr costs the pipeline nothing and can be muted with `2>/dev/null`.
//
// Both are unconditional: no --qr flag, no TTY sniffing. What this command
// mints is a single-use token with a minutes-long TTL for ONE person standing
// next to you with ONE phone, so the QR is the whole point of it, and a flag
// would have to be discovered by exactly the operator who would not know to
// type it. Sniffing the TTY is worse: identical invocations would then emit
// different bytes depending on where they ran.
func printEnrollToken(stdout, stderr io.Writer, out enrollToken) {
	fmt.Fprintf(stdout, "Enroll token (one-time, expires in %ds):\n  %s\n\n", out.ExpiresIn, out.EnrollToken)
	if out.Project.ID != "" {
		fmt.Fprintf(stdout, "Project: %s (%s)\n", out.Project.Name, out.Project.ID)
	}
	fmt.Fprintln(stdout, "Servers:")
	for _, s := range out.Servers {
		fmt.Fprintf(stdout, "  %s\n", s)
	}
	if out.TLSSPKIPin != "" {
		fmt.Fprintf(stdout, "TLS SPKI pin: %s\n", out.TLSSPKIPin)
	} else {
		// No pin is the NORMAL shape behind a TLS-terminating ingress: the
		// phone verifies the public-CA certificate through the system trust
		// store. Stated as what happens, not as an assumption; the console's
		// pin-status card uses the same framing.
		fmt.Fprintln(stdout, "TLS SPKI pin: none (public-CA TLS via the system trust store, the expected ingress shape)")
	}
	// qr_payload on its own line, labeled. Scripts parse this line, and it is
	// the fallback whenever the terminal (or the drawing below) cannot produce
	// a picture.
	fmt.Fprintf(stdout, "\nQR payload (encode as a QR for the app):\n  %s\n", out.QRPayload)

	// A failure to draw is a note, never an error: the token is already minted
	// and burning, so the operator gets it plus a way to encode it themselves.
	if out.QRPayload == "" {
		fmt.Fprintln(stderr, "\nno qr_payload in the server response: nothing to draw")
		return
	}
	code, err := qrterm.Render(out.QRPayload)
	if err != nil {
		fmt.Fprintf(stderr, "\nQR not drawn (%v): encode the payload above yourself\n", err)
		return
	}
	// The width is stated because a QR wider than the window wraps, and a
	// wrapped QR is unscannable in a way that looks like a broken token.
	fmt.Fprintf(stderr, "\nScan with the Straza approver app (%d columns wide; widen the window if it wraps):\n",
		code.Columns)
	fmt.Fprint(stderr, code.Text)
}

// printApprovalsWide renders the detail view: the scannable one-liner per record
// plus, when present, the redacted call-parameters preview and the
// server-composed honesty line. The list view does not show them. Params come
// first (the server's own record), then the unverified justification, then
// the honesty line beneath both.
func printApprovalsWide(w io.Writer, recs []ctl.ApprovalInfo) error {
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
	return nil
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
