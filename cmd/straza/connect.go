package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/connect"
)

func connectCmd() *cobra.Command {
	var expires string
	cmd := &cobra.Command{
		Use:   "connect [server]",
		Short: "Connect your own account to an MCP server; no server lists your connections",
		Long: `Connect your own account to an MCP server that needs each caller's own sign-in or token.
With no server named, the command lists your connections. On a sign-in server it prints
your credentials page, where you sign in and finish. On a token server it asks for the
token with the input hidden.

The token also reads from stdin when stdin is not a terminal, and never from an
argument. straza disconnect takes a connection away. Letting the agents you sponsor
use a connection, and setting an agent's token, happen on the Credentials tab of the
self-service page, where you sign in in your browser. A sign-in is stored only by the
person who signs in.`,
		Example: `  straza connect
  straza connect midpoint
  straza connect github --expires 2026-12-31`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api := agentguard.NewSessionAPI()
			o := connect.Options{
				Tool: "straza", Expires: expires,
				In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
			}
			if len(args) == 1 {
				o.Server = args[0]
			}
			return connect.Run(cmd.Context(), api, o)
		},
	}
	cmd.Flags().StringVar(&expires, "expires", "", "the date the token stops working, as YYYY-MM-DD")
	return cmd
}

func disconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <server>",
		Short: "Remove your own connection to an MCP server",
		Long: `Remove your own connection to an MCP server: your sign-in or your token there.
The credential is forgotten at once, and your next call to that server is refused
until you connect again. Run straza connect to see your connections.`,
		Example: "  straza disconnect midpoint",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api := agentguard.NewSessionAPI()
			o := connect.Options{Tool: "straza", Out: os.Stdout}
			if len(args) == 1 {
				o.Server = args[0]
			}
			return connect.Disconnect(cmd.Context(), api, o)
		},
	}
}
