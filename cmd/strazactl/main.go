// Command strazactl is the Straza admin CLI: login, identity
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

func rootCmd() *cobra.Command { return newRootCmd(ctl.DefaultCredsPath()) }

// newRootCmd builds the command tree against an explicit credentials file so
// tests can drive the real CLI without touching the operator's ~/.straza.
func newRootCmd(credsPath string) *cobra.Command {
	var serverURL string
	var target ctl.Target
	var apiToken, agentMarker string

	root := &cobra.Command{
		Use:           "strazactl",
		Short:         "Straza admin CLI",
		Long:          rootLong,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&serverURL, "server", "",
		"strazad base URL (overrides $STRAZA_SERVER and the server you logged into)")

	// Resolution is lazy: only a command that will build a client needs a
	// target, so local commands run with no login and no flags.
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// A group run with no verb, as drafts alone is, only prints its help.
		if !needsServer(cmd) || cmd.HasSubCommands() && len(args) == 0 {
			return nil
		}
		apiToken = os.Getenv(apiTokenEnv)
		agentMarker = ctl.CodingAgentMarker(os.Getenv)
		if apiToken != "" && cmd.Name() == "login" {
			return errLoginWithToken
		}
		t, err := ctl.ResolveTarget(serverURL, os.Getenv("STRAZA_SERVER"), credsPath)
		// Nothing to resolve means nothing to log out of: logout says that in
		// its own words (naming the credentials file) instead of the generic
		// error, whose advice (run `login`) is the opposite of the ask.
		if errors.Is(err, ctl.ErrNoServer) && cmd.Name() == "logout" {
			return nil
		}
		if errors.Is(err, ctl.ErrNoServer) && apiToken != "" {
			return errTokenWithoutServer
		}
		if err != nil {
			return err
		}
		// The override warning speaks of the login's credentials and advises
		// a login, which a token replaces and refuses, so a token run names
		// its target in the token notice instead. Logout never sends the
		// token and says so in its own words.
		switch {
		case apiToken == "":
			if notice := overrideNotice(t, cmd.Name()); notice != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), notice)
			}
		case cmd.Name() != "logout":
			fmt.Fprintln(cmd.ErrOrStderr(), tokenNotice(t.Server))
		}
		target = t
		return nil
	}

	client := func() *ctl.Client {
		c := ctl.NewClient(target.Server)
		c.CredsPath = credsPath
		c.APIToken, c.AgentMarker = apiToken, agentMarker
		return c
	}

	root.AddCommand(
		statusCmd(client), versionCmd(), loginCmd(client, &target), logoutCmd(client, &target),
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
// sends, and what changes inside a coding agent.
const rootLong = "A command that talks to strazad goes to --server, else $STRAZA_SERVER, else the\n" +
	"server of your strazactl login. It authenticates with that login, or with an\n" +
	"admin API token when STRAZA_API_TOKEN holds one. With a token, every admin call\n" +
	"sends it in place of the login, and one line on stderr says so. strazactl login,\n" +
	"connect and disconnect act as a person and refuse to run while the token is set,\n" +
	"and strazactl logout still ends the stored login.\n\n" +
	"Inside a coding agent, a command that changes Straza refuses to run on your\n" +
	"login, because the agent would act as you. Reads and checks such as policy\n" +
	"simulate still work. For automation, use an admin API token in\n" +
	"STRAZA_API_TOKEN. An agent proposes config changes through the built-in straza\n" +
	"MCP server's drafting tools. strazactl knows it runs inside a coding agent from\n" +
	"the environment variables that Claude Code, the Gemini CLI and the npm build of\n" +
	"Codex set for the commands they run.\n\n" +
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

func loginCmd(client func() *ctl.Client, target *ctl.Target) *cobra.Command {
	var breakGlass bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in via the OIDC device flow and start a strazactl session",
		Long: "Logs in against --server, else $STRAZA_SERVER, else the server this machine is\n" +
			"already logged into (a plain `strazactl login` renews where you are). With none\n" +
			"of the three there is nothing to log in to, and the command says so.\n" +
			"--break-glass signs in at the server's own emergency page instead of your\n" +
			"identity provider. That page accepts the break-glass admin only; use it when\n" +
			"the identity provider cannot sign you in.\n\n" +
			"While STRAZA_API_TOKEN is set, login refuses to run. Every admin call would\n" +
			"send that token, so the login would never be used. After a successful login, a\n" +
			"line on stderr asks you to keep the login away from coding agents.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := client()
			if target.Source == ctl.SourceCredentials {
				// Renewal against the stored server: name it, so a renewal
				// never quietly refreshes a deployment you forgot you were on.
				fmt.Fprintf(cmd.OutOrStdout(), "Logging in again at %s\n", c.Base)
			}
			// Login prints its own logged-in line, so don't repeat it here.
			if err := c.Login(cmd.Context(), cmd.OutOrStdout(), breakGlass); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), loginNote)
			return nil
		},
	}
	cmd.Flags().BoolVar(&breakGlass, "break-glass", false, "sign in as the break-glass admin at the server's own emergency page")
	return cmd
}
