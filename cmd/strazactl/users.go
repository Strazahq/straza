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
			fmt.Fprintf(cmd.OutOrStdout(), "enabled %s. The status is active and the user's revocations are lifted. Sessions revoked earlier stay ended. A device the user enrolled starts a new session at its next check-in with no new enrollment, unless it was revoked on its own or its device credential expired meanwhile: such a device needs straza enroll again\n", args[0])
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
		Short: "Set a user's built-in-issuer password (rotation, or a credential for SCIM-born users)",
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
			"that acts for a person, and service is a technical account with no agency. Neither can sign in to strazactl or the console. " +
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
		Short: "Register (or rotate) the agent identity's Ed25519 public key",
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
		Short: "Revoke the agent identity's key (the next token request fails; running sessions die via the kill switch)",
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
	return users
}
