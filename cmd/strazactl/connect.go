package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/connect"
	"github.com/strazahq/straza/internal/ctl"
)

func connectCmd(client func() *ctl.Client) *cobra.Command {
	var (
		user        string
		expires     string
		allowAgents bool
	)
	cmd := &cobra.Command{
		Use:   "connect [server]",
		Short: "Connect your own account or token to an MCP server; no server lists your connections",
		Long: `Connect your own account or token to a server that gives each caller their own credential.

On a server that uses sign-in, connect prints the address of your credentials page
and waits. You sign in to Straza there and press the server's Sign in button, so the
sign-in is stored only for the person who is signed in.
On a server that takes a token, connect asks for the token at a hidden prompt, or
reads it from stdin when stdin is not a terminal, and never takes it as an argument.
The server tests the token once and keeps only a sealed copy. The answer shows its
fingerprint. With no server named, the command lists your connections.

Administrators and scripts use this command. A person connects with
straza connect, or on the Credentials tab of the self-service page at
/self-service/credentials. strazactl disconnect takes a connection away.

--user names an agent you sponsor, or any user when you are an administrator.
--expires records the date the token stops working, as the provider shows it.
--allow-agents, on its own, lets the agents you sponsor run on your connection
where the server permits it. --allow-agents=false takes that back.

connect acts as a person with your strazactl login, so it refuses to run while
STRAZA_API_TOKEN is set.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			return connect.Run(cmd.Context(), client(), o)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "act for this user: an agent you sponsor, or anyone when you are an administrator")
	cmd.Flags().StringVar(&expires, "expires", "", "the date the token stops working, as YYYY-MM-DD")
	cmd.Flags().BoolVar(&allowAgents, "allow-agents", false, "let the agents you sponsor run on this connection (on its own, changes only that)")
	return cmd
}

func disconnectCmd(client func() *ctl.Client) *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "disconnect <server>",
		Short: "Remove a connection to a server: your own, or another user's with --user",
		Long: `Remove a connection to a server that gives each caller their own credential.

Straza forgets the credential at once, and the next call by that user to the server
is refused until they connect again. strazactl connect lists the connections.

--user names an agent you sponsor, or any user when you are an administrator.

disconnect acts as a person with your strazactl login, so it refuses to run while
STRAZA_API_TOKEN is set.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o := connect.Options{Tool: "strazactl", User: user, Out: os.Stdout}
			if len(args) == 1 {
				o.Server = args[0]
			}
			return connect.Disconnect(cmd.Context(), client(), o)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "act for this user: an agent you sponsor, or anyone when you are an administrator")
	return cmd
}
