package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/strazahq/straza/internal/ctl"
)

// secretStdinIsTerminal reports whether stdin is a terminal a person can type
// at, and readSecretHidden reads one line from it without echo. Tests replace
// both.
var (
	secretStdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	readSecretHidden      = func() ([]byte, error) { return term.ReadPassword(int(os.Stdin.Fd())) }
)

// appsSecretCmd is `strazactl apps secret`: the server's own static secret
// by default, a per-role override with --role. The value comes from --value,
// then STRAZA_SECRET_VALUE, then a hidden prompt when stdin is a terminal, so
// a person never has to type the secret into a command line that shell
// history keeps. Reads return a fingerprint.
func appsSecretCmd(client func() *ctl.Client) *cobra.Command {
	secret := &cobra.Command{Use: "secret", Short: "Manage a server's static secret (injected gateway-side, never client-visible)"}

	var setRole, setValue string
	set := &cobra.Command{
		Use:   "set <server>",
		Short: "Store the server's own secret, or with --role the override one role uses instead",
		Long: "Stores a static secret the gateway injects into calls to the server and never shows\n" +
			"a client. Run in a terminal, the command asks for the value at a hidden prompt. In a\n" +
			"script, set STRAZA_SECRET_VALUE from a secret manager instead. --value also works, but\n" +
			"it puts the secret in the process arguments and the shell history.\n\n" +
			"The answer prints the secret's fingerprint, never its value, and setting it again\n" +
			"replaces the stored value. A server whose manifest leaves no use for a static secret,\n" +
			"such as one that declares no credential, refuses it and names the manifest change.",
		Example: "  strazactl apps secret set scout-tools\n" +
			"  strazactl apps secret set scout-tools --role scout-role",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			value := setValue
			if value == "" {
				value = os.Getenv("STRAZA_SECRET_VALUE")
			}
			if value == "" {
				typed, err := promptSecret(cmd, args[0], setRole)
				if err != nil {
					return err
				}
				value = typed
			}
			row, err := client().SetAppSecret(cmd.Context(), args[0], setRole, value)
			if err != nil {
				return err
			}
			fp := ""
			if row.Fingerprint != "" {
				fp = ", fingerprint " + row.Fingerprint
			}
			if setRole == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "secret set for the MCP server %s: the server's own secret%s\n", args[0], fp)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "secret set for the MCP server %s, role %s%s\n", args[0], setRole, fp)
			}
			return nil
		},
	}
	set.Flags().StringVar(&setRole, "role", "", "store the override for this role instead of the server's own secret")
	set.Flags().StringVar(&setValue, "value", "", "secret value; it shows in process arguments and shell history, so leave it out and type the value at the hidden prompt")
	secret.AddCommand(set)

	var removeRole string
	remove := &cobra.Command{
		Use:   "remove <server>",
		Short: "Remove the server's own secret, or with --role the override one role uses",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().RemoveAppSecret(cmd.Context(), args[0], removeRole); err != nil {
				return err
			}
			if removeRole == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "removed the server's own secret for the MCP server %s\n", args[0])
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "removed the secret for the MCP server %s, role %s\n", args[0], removeRole)
			}
			return nil
		},
	}
	remove.Flags().StringVar(&removeRole, "role", "", "remove the override for this role instead of the server's own secret")
	secret.AddCommand(remove)
	return secret
}

// promptSecret asks for the secret at a hidden prompt on stderr that names
// the server, and the role when one is given. It refuses, before anything
// reaches the server, when stdin is not a terminal, when the read fails and
// when nothing was typed.
func promptSecret(cmd *cobra.Command, app, role string) (string, error) {
	if !secretStdinIsTerminal() {
		return "", fmt.Errorf("no secret value given: stdin is not a terminal, so strazactl cannot ask for it at a hidden prompt. Run the command in a terminal and type the value at the prompt, or in a script set STRAZA_SECRET_VALUE from a secret manager")
	}
	target := "the MCP server " + app
	if role != "" {
		target += ", role " + role
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Secret for %s (input hidden): ", target)
	raw, err := readSecretHidden()
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return "", fmt.Errorf("could not read the secret at the prompt (%v), so nothing was stored. Run the command again in a terminal", err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("no secret typed, so nothing was stored. Run the command again and type the value at the prompt")
	}
	return string(raw), nil
}
