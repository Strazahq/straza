package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func apiTokenCmd(client func() *ctl.Client) *cobra.Command {
	cmd := &cobra.Command{Use: "api-token", Short: "Manage long-lived admin API tokens, the one credential for automation, connectors and an identity manager's SCIM push"}

	var name string
	var scopes []string
	var ttl time.Duration
	create := &cobra.Command{
		Use:   "create",
		Short: "Mint a long-lived admin API token (shown exactly once)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return fmt.Errorf("--name is required (e.g. --name midpoint-pull)")
			}
			if len(scopes) == 0 {
				return fmt.Errorf(`--scope is required: repeatable area:verb scopes (e.g. --scope identity:read --scope changes:read) or --scope full (root credential; it can mint further tokens)`)
			}
			tok, err := client().CreateAPIToken(cmd.Context(), name, strings.Join(scopes, ","), ttl)
			if err != nil {
				return err
			}
			expires := "never"
			if tok.Expires != nil {
				expires = tok.Expires.Format("2006-01-02 15:04 MST")
			}
			fmt.Printf("id:      %s\nname:    %s\nscope:   %s\nexpires: %s\ntoken:   %s\n\nStore this token now. It is not retrievable again.\n", tok.ID, tok.Name, tok.Scope, expires, tok.Token)
			return nil
		},
	}
	create.Flags().StringVar(&name, "name", "", "token label (which system uses it)")
	create.Flags().DurationVar(&ttl, "ttl", 0, "token lifetime (e.g. 720h); 0 = never expires")
	create.Flags().StringSliceVar(&scopes, "scope", nil,
		`area:verb scope, repeatable or comma-joined (areas: identity, sessions, policy, apps, audit, transcripts, approvals, config, tokens, changes, scim, drafts), or "full" (root credential)`)
	cmd.AddCommand(create)

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List admin API token metadata",
		RunE: func(cmd *cobra.Command, _ []string) error {
			tokens, err := client().APITokens(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSCOPE\tCREATED\tEXPIRES\tLAST USED")
			for _, t := range tokens {
				expires, lastUsed := "never", "never"
				if t.Expires != nil {
					expires = t.Expires.Format("2006-01-02 15:04")
				}
				if t.LastUsed != nil {
					lastUsed = t.LastUsed.Format("2006-01-02 15:04")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Scope, t.Created.Format("2006-01-02 15:04"), expires, lastUsed)
			}
			return tw.Flush()
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke an admin API token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().RevokeAPIToken(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("revoked %s\n", args[0])
			return nil
		},
	})
	return cmd
}
