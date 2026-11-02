package main

import (
	"github.com/spf13/cobra"
)

// serviceCmd runs the Straza background service. On Linux and macOS it
// runs in the foreground until SIGTERM or SIGINT. On Windows it runs only
// under the Service Control Manager (service_windows.go).
func serviceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "service",
		Short: "Run the Straza background service that holds this machine's keys and sessions",
		Long: "Runs the Straza background service. It alone holds each enrolled user's key and\n" +
			"every Straza session on this machine, and the hooks, straza mcp and the other\n" +
			"commands reach it over a local socket, a named pipe on Windows, where the system\n" +
			"names the caller.\n\n" +
			"On Linux it runs in the foreground, and a Linux package runs it under its own\n" +
			"account. The Straza package for macOS runs it under its own account as the\n" +
			"launchd daemon ai.straza.service. With STRAZA_HOME set, on Linux or macOS, it\n" +
			"runs as you for that home, with its socket and state inside it.\n\n" +
			"On Windows it runs only as the Windows service straza-service, which the Service\n" +
			"Control Manager starts, and it refuses to start from a console.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runService(cmd.Context())
		},
	}
}
