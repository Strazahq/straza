package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/connect"
	"github.com/strazahq/straza/internal/ctl"
)

// needsUser is the refusal of connect or disconnect without --user, at the
// server whose self-service page it names: an admin API token is no user, and
// a person connects their own account in a browser.
func needsUser(server string) error {
	return fmt.Errorf("strazactl changes other users' connections, on an admin API token, so name the user with --user. "+
		"Connect your own account with straza connect or on the Credentials tab of the self-service page: %s/self-service/credentials", server)
}

func connectCmd(client func() *ctl.Client) *cobra.Command {
	var (
		user        string
		expires     string
		allowAgents bool
	)
	cmd := &cobra.Command{
		Use:   "connect [server] --user <name>",
		Short: "Connect a user's token to an MCP server; no server lists the user's connections",
		Long: `Connect another user's token to a server that gives each caller their own credential.

strazactl runs on an admin API token, which is no user, so --user names the user
whose connection changes, and the command refuses to run without it. The token
needs apps:read and apps:write, because connect reads the user's connections
before it changes one. A list with no server named needs apps:read alone, and
--allow-agents on a named server sends no read, so it needs apps:write alone.
On a server that takes a token, connect asks for the token at a hidden prompt, or
reads it from stdin when stdin is not a terminal, and never takes it as an argument.
The server tests the token once and keeps only a sealed copy. The answer shows its
fingerprint. A server that uses sign-in needs the user's own browser, so nobody
connects on their behalf. With no server named, the command lists the user's
connections.

A person connects their own account with straza connect, or on the Credentials tab
of the self-service page at /self-service/credentials. strazactl disconnect takes
a connection away.

--expires records the date the token stops working, as the provider shows it.
--allow-agents, on its own, lets the agents the user sponsors run on the user's
connection where the server permits it. --allow-agents=false takes that back, so
an organization can switch it for its users from a script.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client()
			if user == "" {
				return needsUser(c.Base)
			}
			o := connect.Options{
				Tool: "strazactl", User: user, Expires: expires,
				In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
			}
			if len(args) == 1 {
				o.Server = args[0]
			}
			if cmd.Flags().Changed("allow-agents") {
				o.AllowAgents = &allowAgents
			}
			return connect.Run(cmd.Context(), c, o)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "the user whose connections the command reads or changes (required)")
	cmd.Flags().StringVar(&expires, "expires", "", "the date the token stops working, as YYYY-MM-DD")
	cmd.Flags().BoolVar(&allowAgents, "allow-agents", false, "let the agents the user sponsors run on this connection (on its own, changes only that)")
	return cmd
}

func disconnectCmd(client func() *ctl.Client) *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "disconnect <server> --user <name>",
		Short: "Remove a user's connection to a server",
		Long: `Remove another user's connection to a server that gives each caller their own credential.

Straza forgets the credential at once, and the next call by that user to the server
is refused until they connect again. strazactl connect --user lists the connections.

strazactl runs on an admin API token, which is no user, so --user names the user
whose connection goes, and the command refuses to run without it. The token needs
apps:write. A person removes their own connection with straza disconnect, or on
the Credentials tab of the self-service page at /self-service/credentials.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client()
			if user == "" {
				return needsUser(c.Base)
			}
			o := connect.Options{Tool: "strazactl", User: user, Out: os.Stdout}
			if len(args) == 1 {
				o.Server = args[0]
			}
			return connect.Disconnect(cmd.Context(), c, o)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "the user whose connection goes (required)")
	return cmd
}
