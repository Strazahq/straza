package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
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
		Use:   "revoke <name-or-id>",
		Short: "Revoke an admin API token by its name or its id",
		Long: "Revokes one admin API token, and its next request is refused. Pass the name\n" +
			"the console shows under Settings, API tokens, or the id strazactl api-token list\n" +
			"prints. An argument in the form of an id is taken as an id. Revoking by name\n" +
			"reads the token list first, so it needs tokens:read as well as tokens:write.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client()
			if isTokenID(args[0]) {
				if err := c.RevokeAPIToken(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "revoked the admin API token %s, matched by its id.\n", args[0])
				return nil
			}
			id, err := apiTokenIDByName(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			if err := c.RevokeAPIToken(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "revoked the admin API token named %s, matched by its name. Its id was %s.\n", args[0], id)
			return nil
		},
	})
	return cmd
}

// isTokenID reports whether s has the form of an admin API token id, which
// strazad mints as a lowercase UUID. revoke sends such an argument as it is,
// with no list read first, so revoking by id needs tokens:write alone.
func isTokenID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s
}

// apiTokenIDByName finds the id of the one admin API token named name. strazad
// refuses a duplicate name at mint, and a name that tokens share anyway is
// refused here with their ids, so revoke never guesses which one was meant.
func apiTokenIDByName(ctx context.Context, c *ctl.Client, name string) (string, error) {
	tokens, err := c.APITokens(ctx)
	if err != nil {
		// The server's sentence may already end with a period.
		sep := ". "
		if strings.HasSuffix(err.Error(), ".") {
			sep = " "
		}
		return "", fmt.Errorf("strazactl reads the token list to find the token named %q, and reading it failed: %w%s"+
			"If the token in STRAZA_API_TOKEN lacks tokens:read, revoke by the id instead, which needs tokens:write alone, "+
			"or revoke the token in the console under Settings, API tokens", name, err, sep)
	}
	var ids []string
	for _, t := range tokens {
		if t.Name == name {
			ids = append(ids, t.ID)
		}
	}
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		return "", fmt.Errorf("no admin API token is named %q. strazactl api-token list prints each token's name and id, and revoke takes either", name)
	}
	return "", fmt.Errorf("%d admin API tokens are named %q, so the name does not say which one to revoke. Revoke one by its id: %s",
		len(ids), name, strings.Join(ids, ", "))
}
