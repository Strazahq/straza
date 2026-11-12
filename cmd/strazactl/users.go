package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

func usersCmd(client func() *ctl.Client) *cobra.Command {
	users := &cobra.Command{Use: "users", Short: "Manage users"}

	users.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List users",
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := client().Users(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "USERNAME\tSTATUS\tORIGIN\tEMAIL\tID")
			for _, u := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Username, u.Status, u.Origin, u.Email, u.ID)
			}
			return tw.Flush()
		},
	})

	var email, display, password string
	create := &cobra.Command{
		Use:   "create <username>",
		Short: "Create a local user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := client().CreateUser(cmd.Context(), args[0], email, display, password)
			if err != nil {
				return err
			}
			fmt.Printf("created user %s (%s)\n", u.Username, u.ID)
			return nil
		},
	}
	create.Flags().StringVar(&email, "email", "", "email address")
	create.Flags().StringVar(&display, "display", "", "display name")
	create.Flags().StringVar(&password, "password", "", "initial password (for the built-in issuer)")
	users.AddCommand(create)

	users.AddCommand(&cobra.Command{
		Use:   "enable <username>",
		Short: "Enable a disabled user: the status goes back to active, the user's revocations are lifted, and a device still enrolled needs no new enrollment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := client().UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			// An active user gets no status write: the server would record
			// an identity update that changed nothing, and a lock is not
			// lifted by the status at all.
			if u.Status == "active" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is already active. A lock is lifted with `strazactl users unlock %s`\n", args[0], args[0])
				return nil
			}
			if _, err := client().SetUserStatus(cmd.Context(), u.ID, "active"); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "enabled %s. The status is active and the user's revocations are lifted. Sessions revoked earlier stay ended. A device the user enrolled starts a new session at its next check-in with no new enrollment, unless it was revoked on its own or the sign-in it enrolled with grew older than governance.signInMaxAge meanwhile. Such a device needs straza enroll again\n", args[0])
			return nil
		},
	})

	users.AddCommand(&cobra.Command{
		Use:   "disable <username>",
		Short: "Disable a user (status disabled, every session revoked, lifted by `strazactl users enable`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := client().UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if _, err := client().SetUserStatus(cmd.Context(), u.ID, "disabled"); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "disabled %s. Sessions revoked, lift with `strazactl users enable %s`\n", args[0], args[0])
			return nil
		},
	})

	var newPassword string
	setPassword := &cobra.Command{
		Use:   "set-password <username>",
		Short: "Set a person's built-in-issuer password (rotation, or a credential for SCIM-born people)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if newPassword == "" {
				return fmt.Errorf("--password is required")
			}
			u, err := client().UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if _, err := client().SetUserPassword(cmd.Context(), u.ID, newPassword); err != nil {
				return err
			}
			fmt.Printf("password updated for %s\n", args[0])
			return nil
		},
	}
	setPassword.Flags().StringVar(&newPassword, "password", "", "the new password")
	users.AddCommand(setPassword)

	var lockReason, lockOrigin string
	lock := &cobra.Command{
		Use:   "lock <username>",
		Short: "Lock a user: a revocation that an identity manager cannot lift by enabling the user; the status stays as it is",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if lockReason == "" {
				return fmt.Errorf("--reason is required (it lands on the audit chain and in the SCIM lock block)")
			}
			u, err := client().UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := client().LockUser(cmd.Context(), u.ID, lockReason, lockOrigin); err != nil {
				return err
			}
			fmt.Printf("locked %s. Sessions revoked; the identity manager sees it read-only, lift with `strazactl users unlock %s`\n", args[0], args[0])
			return nil
		},
	}
	lock.Flags().StringVar(&lockReason, "reason", "", "why (required)")
	lock.Flags().StringVar(&lockOrigin, "origin", "", "admin (default) or external (SOAR/SIEM automation)")
	users.AddCommand(lock)
	users.AddCommand(&cobra.Command{
		Use:   "unlock <username>",
		Short: "Unlock a user: lift the user's revocations and leave the status as it is, so a disabled user still needs `strazactl users enable`",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := client().UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := client().UnlockUser(cmd.Context(), u.ID); err != nil {
				return err
			}
			fmt.Printf("unlocked %s\n", args[0])
			return nil
		},
	})

	var nhiDisplay, nhiType string
	createNHI := &cobra.Command{
		Use:   "create-nhi <username> --type agent|service",
		Short: "Provision an AI agent or a service account (headless, no password, key-based)",
		Long: "Provision an identity with no password and no interactive login. --type is required: agent is an AI agent " +
			"that acts for a person, and service is a technical account with no agency. Neither can sign in to the console. " +
			"Register its key next with strazactl users nhi-key set, after straza keygen prints it on the agent's machine.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			const hint = "Use --type agent for an AI agent that acts for a person, or --type service for a technical account with no agency"
			word := map[string]string{"agent": "AI agent", "service": "service account"}[nhiType]
			switch {
			case nhiType == "":
				return fmt.Errorf("create-nhi needs --type, because Straza must know whether %s is an AI agent or a service account. %s", args[0], hint)
			case word == "":
				return fmt.Errorf("--type %q is neither agent nor service. %s", nhiType, hint)
			}
			u, err := client().CreateNHI(cmd.Context(), args[0], nhiDisplay, nhiType)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created the %s %s (%s). Register its key: strazactl users nhi-key set %s <publicKey>\n", word, u.Username, u.ID, u.Username)
			return nil
		},
	}
	createNHI.Flags().StringVar(&nhiType, "type", "", "agent or service (required)")
	createNHI.Flags().StringVar(&nhiDisplay, "display", "", "the name people see for this identity")
	users.AddCommand(createNHI)

	nhiKey := &cobra.Command{Use: "nhi-key", Short: "Manage an agent identity's headless assertion key (straza keygen prints it)"}
	nhiKey.AddCommand(&cobra.Command{
		Use:   "set <username> <publicKeyBase64>",
		Short: "Register the agent identity's Ed25519 public key, or rotate it and end the agent's running sessions",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := client().UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := client().SetNHIKey(cmd.Context(), u.ID, args[1]); err != nil {
				return err
			}
			fmt.Printf("key registered for %s. The agent can now `straza enroll --headless --user %s`\n", args[0], args[0])
			return nil
		},
	})
	nhiKey.AddCommand(&cobra.Command{
		Use:   "unset <username>",
		Short: "Revoke the agent identity's key, which ends its running sessions and refuses its next token request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := client().UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := client().UnsetNHIKey(cmd.Context(), u.ID); err != nil {
				return err
			}
			fmt.Printf("key revoked for %s\n", args[0])
			return nil
		},
	})
	users.AddCommand(nhiKey)
	users.AddCommand(entraAgentIDCmd(client))
	return users
}

// entraAgentIDCmd is strazactl users entra-agent-id: set, unset and show
// the Entra agent identity an AI agent acts as at Entra.
func entraAgentIDCmd(client func() *ctl.Client) *cobra.Command {
	entra := &cobra.Command{
		Use:   "entra-agent-id",
		Short: "Manage the Entra agent identity an AI agent acts as at Entra",
		Long: "An AI agent that calls a server behind Microsoft Entra acts there as an Entra agent identity. " +
			"Its client id is a GUID that Entra assigns, so Straza keeps it on the agent's record. " +
			"An identity manager can write it over SCIM as entraAgentId instead.",
	}
	set := func(cmd *cobra.Command, username, clientID string) (ctl.User, error) {
		u, err := client().UserByUsername(cmd.Context(), username)
		if err != nil {
			return ctl.User{}, err
		}
		return client().SetEntraAgentID(cmd.Context(), u.ID, clientID)
	}
	entra.AddCommand(&cobra.Command{
		Use:   "set <username> <client id>",
		Short: "Set the client id of the Entra agent identity the agent acts as",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := set(cmd, args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s now acts at Entra as the agent identity %s.\n", u.Username, u.EntraAgentID)
			return nil
		},
	})
	entra.AddCommand(&cobra.Command{
		Use:   "unset <username>",
		Short: "Clear the agent's Entra agent identity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := set(cmd, args[0], "")
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s no longer has an Entra agent identity.\n", u.Username)
			return nil
		},
	})
	entra.AddCommand(&cobra.Command{
		Use:   "show <username>",
		Short: "Show the Entra agent identity the agent acts as",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := client().UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if u.EntraAgentID == "" && u.UserType != "agent" {
				fmt.Fprintf(cmd.OutOrStdout(), entraNotAgentShown, u.Username, userTypePhrase(u.UserType))
				return nil
			}
			if u.EntraAgentID == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s has no Entra agent identity. Set one with strazactl users entra-agent-id set %s <client id>.\n", u.Username, u.Username)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s acts at Entra as the agent identity %s.\n", u.Username, u.EntraAgentID)
			return nil
		},
	})
	return entra
}

// entraNotAgentShown is what show prints for a user that holds no Entra
// agent identity and is not an agent, so strazad would refuse a set.
const entraNotAgentShown = "%[1]s has no Entra agent identity and cannot hold one, because %[1]s is %[2]s and Straza gets Entra tokens for agents only. " +
	"If %[1]s is an agent, set its type to agent first, from the identity manager with userType agent or over the admin API with user_type agent, " +
	"then run strazactl users entra-agent-id set %[1]s <client id>.\n"

// userTypePhrase names the user_type of a user that is not an agent in a
// sentence about the user.
func userTypePhrase(userType string) string {
	switch userType {
	case "human":
		return "a person"
	case "service":
		return "a service account"
	}
	return "unclassified, with no user type,"
}
