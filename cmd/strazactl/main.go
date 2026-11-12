// Command strazactl is the Straza admin CLI on an admin API token: identity
// CRUD, policy lifecycle, drafts, apps/bindings, packs, sessions, audit,
// transcripts, api-tokens, connect, spec conformance, status.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
	"github.com/strazahq/straza/internal/version"
)

func main() {
	cmd, err := rootCmd().ExecuteC()
	if err == nil {
		return
	}
	// policy diff speaks diff(1) exit codes through exitCodeErr, and its code
	// 1 (differs) carries no message. The drafts verbs keep 1 for the
	// server's no in the same way.
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.err != nil {
		fmt.Fprintln(os.Stderr, "strazactl:", err)
	}
	os.Exit(exitCode(cmd, err))
}

// exitCode is the process status of a run of cmd that ended in err: the code
// an exitCodeErr carries, 2 for any other failure of the drafts group or a
// drafts verb, and 1 for any other failure elsewhere. A drafts verb keeps 1
// for the server's no, and cobra's argument and flag errors and the target
// resolution reach main without passing through the verb, so the 2 is set
// here.
func exitCode(cmd *cobra.Command, err error) int {
	var ec exitCodeErr
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ec):
		return ec.code
	case underDrafts(cmd):
		return 2
	}
	return 1
}

// underDrafts reports whether cmd is the drafts group or one of its verbs.
func underDrafts(cmd *cobra.Command) bool {
	for c := cmd; c != nil && c.HasParent(); c = c.Parent() {
		if c.Name() == "drafts" && !c.Parent().HasParent() {
			return true
		}
	}
	return false
}

func rootCmd() *cobra.Command { return newRootCmd(oldLoginPath()) }

// newRootCmd builds the command tree against an explicit path of the old
// login file, which the refusal of a run without a token names when the file
// is there, so tests never look at the operator's ~/.straza.
func newRootCmd(loginPath string) *cobra.Command {
	var serverURL, server, apiToken string

	root := &cobra.Command{
		Use:           "strazactl",
		Short:         "Straza admin CLI",
		Long:          rootLong,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// The version is printed by `strazactl version` only. The flag error for
	// --version names that command so the operator is not left guessing.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if err.Error() == "unknown flag: --version" {
			return fmt.Errorf("unknown flag: --version. Run `strazactl version` to print the version")
		}
		return err
	})
	root.PersistentFlags().StringVar(&serverURL, "server", "",
		"strazad base URL (overrides $STRAZA_SERVER)")

	client := func() *ctl.Client {
		c := ctl.NewClient(server)
		c.APIToken = apiToken
		return c
	}
	status := statusCmd(client)

	// Resolution is lazy: only a command that will build a client needs a
	// token and a server, so local commands run with neither. status sends
	// no credential, so it needs the server alone.
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// A group run with no verb, as drafts alone is, only prints its help.
		if !needsServer(cmd) || cmd.HasSubCommands() && len(args) == 0 {
			return nil
		}
		apiToken = os.Getenv(apiTokenEnv)
		if apiToken == "" && cmd != status {
			return noTokenError(loginPath)
		}
		var err error
		server, err = ctl.ResolveServer(serverURL, os.Getenv("STRAZA_SERVER"))
		return err
	}

	root.AddCommand(
		status, versionCmd(), retiredLogin("login", loginPath), retiredLogin("logout", loginPath),
		usersCmd(client), rolesCmd(client), assignCmd(client), unassignCmd(client),
		packsCmd(client), sessionsCmd(client), signingKeysCmd(client), devicesCmd(client), policyCmd(client),
		auditCmd(client), appsCmd(client), bindingsCmd(client), specCmd(),
		apiTokenCmd(client), attestationCmd(client), connectCmd(client), disconnectCmd(client),
		transcriptsCmd(client), approvalsCmd(client), approversCmd(client), catalogCmd(client),
		sinksCmd(client), draftsCmd(client),
	)
	root.AddCommand(devCommands...)
	refuseUnknownVerbs(root)
	return root
}

// noVerb is the Args of a group, a command that holds verbs: it refuses an
// operand, which is a verb the group does not have, naming the group's
// help, and lets a bare group print that help.
func noVerb(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("%s has no verb %s. Run %s --help for the verbs", cmd.CommandPath(), args[0], cmd.CommandPath())
}

// refuseUnknownVerbs gives every group under cmd that runs nothing of its
// own the Args noVerb and a RunE that prints its help, because cobra
// answers a group that runs nothing with its help and status 0 before it
// looks at the operands, so a script that mistyped a verb read success. A
// group with its own RunE, such as roles implications, keeps it.
func refuseUnknownVerbs(cmd *cobra.Command) {
	for _, c := range cmd.Commands() {
		if !c.Runnable() && c.HasSubCommands() {
			c.Args = noVerb
			c.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
		}
		refuseUnknownVerbs(c)
	}
}

// devCommands holds the commands a build-tagged file adds to the root, such
// as gen-docs under the docsgen tag. A plain build leaves it empty.
var devCommands []*cobra.Command

// rootLong is the root help: where a command goes, which credential it
// sends, and the exit codes.
const rootLong = "A command that talks to strazad goes to --server, else $STRAZA_SERVER. It sends\n" +
	"the admin API token in STRAZA_API_TOKEN, which a person mints in the console\n" +
	"under Settings, API tokens, with the scope the commands need and an expiry.\n" +
	"strazactl has no sign-in of its own. When the identity provider cannot sign\n" +
	"anyone in, the break-glass admin uses Emergency sign-in on the console's sign-in\n" +
	"page and mints a token there. strazactl status sends no credential and needs the\n" +
	"server alone.\n\n" +
	"Every command exits 0 when it did its job and 1 when it did not. An unknown verb\n" +
	"exits 1 too. strazactl drafts and strazactl policy diff name other exit codes in\n" +
	"their help. A retired verb exits 2 and names what replaced it."

func statusCmd(client func() *ctl.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report the resolved target server, then strazad health and version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctl.Status(cmd.Context(), client().Base, cmd.OutOrStdout())
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Print version information",
		Annotations: local,
		Run: func(cmd *cobra.Command, _ []string) {
			v := version.Get()
			fmt.Fprintf(cmd.OutOrStdout(), "strazactl %s (commit %s, %s, %s/%s)\n",
				v.Version, v.Commit, v.Go, v.OS, v.Arch)
		},
	}
}

// retiredLogin is the hidden verb login or logout. strazactl no longer signs
// in, so it takes whatever the old verb took, sends nothing and exits with
// the status of a retired verb. With a token set it says there is nothing to
// sign in or out of, and without one it answers the refusal of a run without
// a token.
func retiredLogin(verb, loginPath string) *cobra.Command {
	return &cobra.Command{
		Use:                verb,
		Hidden:             true,
		DisableFlagParsing: true,
		Annotations:        local,
		RunE: func(*cobra.Command, []string) error {
			if os.Getenv(apiTokenEnv) != "" {
				return exitCodeErr{code: 2, err: errNoSignIn}
			}
			return exitCodeErr{code: 2, err: noTokenError(loginPath)}
		},
	}
}
