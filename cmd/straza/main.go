// Command straza is the Straza client-side enforcement kit: the Policy
// Enforcement Point inside coding-agent harnesses. It enrolls the machine,
// wires hooks and the MCP proxy into each Tier-1 harness, runs the Straza
// background service, and serves the hook, mcp, exec, doctor, status, trace
// and logs commands.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/gitremote"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/oidcflow"
	"github.com/strazahq/straza/internal/version"
)

func main() {
	// Run as git-remote-straza, the binary is git's remote helper and
	// nothing else, so no command, hook record or flag parsing sees the run.
	if gitremote.Invoked(os.Args[0]) {
		os.Exit(gitremote.Main(os.Args[1:]))
	}
	if err := rootCmd().Execute(); err != nil {
		// A hook deny carries the harness block exit code (e.g. 2); the
		// response body was already written to stdout.
		if coder, ok := err.(interface{ Code() int }); ok {
			os.Exit(coder.Code())
		}
		recordHookBoundary(os.Args, err)
		fmt.Fprintln(os.Stderr, "straza:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "straza",
		Short:         "Straza client: enroll this machine, wire the hooks and decide each tool call",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// The version is printed by `straza version` only. The flag error for
	// --version names that command so the operator is not left guessing.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if err.Error() == "unknown flag: --version" {
			return fmt.Errorf("unknown flag: --version. Run `straza version` to print the version")
		}
		return err
	})
	root.AddCommand(
		hookCmd(),
		versionCmd(),
		enrollCmd(),
		keygenCmd(),
		installCmd(),
		uninstallCmd(),
		statusCmd(),
		connectCmd(),
		disconnectCmd(),
		serviceCmd(),
		mcpCmd(),
		execCmd(),
		doctorCmd(),
		logsCmd(),
		traceCmd(),
	)
	root.AddCommand(devCommands...)
	return root
}

// devCommands holds the hidden commands a build-tagged file adds to the
// root: gen-docs under the docsgen tag, and on Windows and macOS the installer
// group that the Straza MSI or the Straza package for macOS needs. A plain
// build for Linux leaves it empty.
var devCommands []*cobra.Command

func execCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec -- <command> [args...]",
		Short: "Run a command through Straza policy, for an agent that has no hooks",
		Long: "The execution shim for agents that have no hook surface: the command is\n" +
			"decided as a canonical shell.exec event by the Straza background service\n" +
			"(same policy, audit, and fail-closed semantics as the harness hooks), then run\n" +
			"with inherited stdio and exit-code passthrough. Denials print the rule reason\n" +
			"and exit 2. Advisory on an open machine; boundary-grade inside a sandbox\n" +
			"profile where this shim is the only exec surface.",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := agentguard.Exec(cmd.Context(), args, os.Stderr)
			if code != 0 {
				os.Exit(code) // exit-code passthrough is the contract; cobra must not reword it
			}
			return nil
		},
	}
	cmd.Flags().SetInterspersed(false) // everything after the command is the child's, not ours
	return cmd
}

// doctorCmd runs every diagnostic: the background service, enrollment, server
// reachability + clock skew, sessions, hook wiring, audit-spool backlog,
// kill-switch path. Exit 1 when anything fails so scripts can gate on it.
func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the background service, enrollment, connectivity, hook wiring, and the audit spool",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			checks := agentguard.Doctor(cmd.Context(), store, agentguard.NewServiceClient())
			failed := false
			for _, c := range checks {
				status := map[string]string{"ok": " ok ", "warn": "WARN", "fail": "FAIL"}[c.Status]
				fmt.Printf("[%s] %-11s %s\n", status, c.Name, c.Detail)
				if c.Hint != "" {
					fmt.Printf("       %-11s → %s\n", "", c.Hint)
				}
				if c.Status == "fail" {
					failed = true
				}
			}
			if failed {
				return fmt.Errorf("doctor found failures (see hints above)")
			}
			return nil
		},
	}
}

func mcpCmd() *cobra.Command {
	var harness string
	cmd := &cobra.Command{
		Use:   "mcp [server]",
		Short: "Serve the Straza MCP gateway to this harness over stdio (`straza install` registers it)",
		Args:  cobra.MaximumNArgs(1),
		Long: "An MCP stdio server whose backend is the Straza gateway (/mcp): role-computed\n" +
			"tool catalog, server-side credential injection, rate limits, audit. The Straza\n" +
			"background service keeps the rotating session token and its proof on the wire\n" +
			"(the one thing a static MCP config cannot do), so the proxy holds no credential\n" +
			"and joins the harness's newest governed session.\n\n" +
			"With a server name, it serves that one server from its own gateway endpoint,\n" +
			"with the server's own tool names and its views, for chat apps that show MCP\n" +
			"Apps views.",
		RunE: func(cmd *cobra.Command, args []string) error {
			server := ""
			if len(args) == 1 {
				if server = args[0]; server == "" {
					return fmt.Errorf("the server name after mcp is empty. Name one server, or leave the argument out to serve all your servers")
				}
			}
			return agentguard.RunMCPProxy(cmd.Context(), harness, server)
		},
	}
	cmd.Flags().StringVar(&harness, "harness", "", "harness this proxy fronts (default: STRAZA_HARNESS env, then claude-code)")
	return cmd
}

// noDisplay is the refusal of a browser sign-in on a machine where no
// browser can be opened.
const noDisplay = "This machine has no display, so Straza cannot open a browser here. " +
	"Run `straza enroll --use-device-code` to sign in with a code on another device, if your Straza admin allows it."

// headlessDeviceCode is the refusal of the two flags together.
const headlessDeviceCode = "--use-device-code is for a person's sign-in, and --headless enrolls an agent with its key. " +
	"Run `straza enroll --headless` for an agent, or `straza enroll --use-device-code` for a person."

func enrollCmd() *cobra.Command {
	var server, user string
	var headless, deviceCode bool
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Sign in through your browser and enroll this machine with the background service (--use-device-code: sign in with a code, --headless: an AI agent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if headless && deviceCode {
				return errors.New(headlessDeviceCode) //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			}
			target := resolveEnrollServer(server, os.Getenv("STRAZA_SERVER"))
			// Before anything is contacted or written: an enrolment aimed by
			// the environment says so, once, on stderr (resolve.go).
			if line := target.Announce(); line != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), line)
			}
			req := localapi.EnrollRequest{ServerURL: target.Server, Headless: headless, Username: user}
			switch {
			case headless:
				// An agent signs in with its key or client secret, on no lane.
			case deviceCode:
				req.Lane = localapi.LaneDeviceCode
			case !oidcflow.CanOpenBrowser(runtime.GOOS, os.Getenv):
				// Never a silent switch to the code lane: the person picks it.
				return errors.New(noDisplay) //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			default:
				req.Lane = localapi.LaneBrowser
			}
			return agentguard.Enroll(cmd.Context(), agentguard.NewServiceClient(), req, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "strazad base URL")
	cmd.Flags().BoolVar(&deviceCode, "use-device-code", false, "sign in with a code that you confirm in a browser on another device, for a machine with no display. Your Straza admin can turn this off")
	cmd.Flags().BoolVar(&headless, "headless", false, "enroll an AI agent or a service account with no browser: with the key straza keygen made, or with the client secret at your identity provider that an administrator gave the Straza background service. Its sessions carry no device")
	cmd.Flags().StringVar(&user, "user", "", "the username the agent's key is registered under (headless; default: the name straza keygen stored)")
	return cmd
}

func keygenCmd() *cobra.Command {
	var user string
	var force bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Have the background service generate the Ed25519 key an AI agent enrolls with headless",
		Args:  cobra.NoArgs,
		Long: "Asks the Straza background service to generate the keypair a headless AI agent\n" +
			"or service account authenticates with. The service keeps the private half;\n" +
			"print-out includes the `strazactl users nhi-key set` line an administrator\n" +
			"runs to register the public half, after which `straza enroll --headless` works.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return agentguard.Keygen(cmd.Context(), agentguard.NewServiceClient(), user, force, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "the username this key will be registered under, printed in the registration line and used by enroll when --user is left out")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing key (the registered public key stops working)")
	return cmd
}

func installCmd() *cobra.Command {
	var managed bool
	var server, binDir string
	cmd := &cobra.Command{
		Use:   "install <harness>...",
		Short: "Write hook wiring for Tier-1 harnesses (user mode, or --managed system layout)",
		Long: "Wires the hooks and the MCP entry into each named harness: claude-code, codex or\n" +
			"gemini. Without --managed it writes your own settings, and with --managed the\n" +
			"root-owned layout that a user cannot edit.",
		Example: "  straza install claude-code\n" +
			"  sudo straza install --managed --server https://straza.example.com claude-code",
		Args: cobra.ArbitraryArgs, // harnessArgs validates; cobra's message would hide the valid names
		RunE: func(cmd *cobra.Command, args []string) error {
			harnesses, err := harnessArgs(args)
			if err != nil {
				return err
			}
			if managed {
				return agentguard.InstallManaged(cmd.Context(), agentguard.ManagedInstallOptions{
					Harnesses: harnesses, ServerURL: server, BinDir: binDir,
				}, os.Stdout)
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			for _, h := range harnesses {
				if err := installHarnessHooks(h, self); err != nil {
					return err
				}
				if err := registerMCP(h, self); err != nil {
					return err
				}
				if h == "codex" {
					// Last, and after the MCP line: an operator who stops
					// reading at the first "installed" has an ungoverned codex.
					fmt.Print(codexTrustNotice)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&managed, "managed", false, "install the root-owned, managed layout that a user cannot edit (run with sudo, or as an administrator on Windows)")
	cmd.Flags().StringVar(&server, "server", "", "strazad base URL the managed layout pins for every user of this machine (required with --managed)")
	cmd.Flags().StringVar(&binDir, "bin-dir", "", "managed binary directory (default: /usr/bin on Linux, /opt/straza/bin on macOS, %ProgramData%\\straza\\bin on Windows). The straza package, the Straza package for macOS or the Straza MSI installs the binary there, and this command never copies it")
	return cmd
}

// harnessArgs resolves the harness operands for install/uninstall. There is
// one canonical form: harnesses are POSITIONAL operands (`straza install
// codex`, like `apt install`), with no --harness flag and no silent default,
// because a default would silently wire the wrong harness. A missing operand
// errors naming the valid values. Every name is validated up front, so a bad
// one fails the whole run before any file is touched (no partial wiring).
func harnessArgs(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("specify at least one harness: claude-code, codex, gemini")
	}
	for _, h := range args {
		if _, err := agentguard.SettingsPath(h); err != nil {
			return nil, err
		}
	}
	return args, nil
}

// installHarnessHooks writes one harness's user-scope hook wiring. Every
// harness but codex keeps its hooks in a settings.json the shared writer owns;
// codex reads a dedicated hooks.json and gates non-managed hooks behind an
// operator trust step, so it gets its own branch (internal/agentguard/
// installcodex.go) and its own output, including the part where the install
// is not yet enforcing anything.
func installHarnessHooks(harness, self string) error {
	if harness == "codex" {
		return installCodexHooks(self)
	}
	settings, err := agentguard.SettingsPath(harness)
	if err != nil {
		return err
	}
	if err := agentguard.InstallHooks(harness, settings, self); err != nil {
		return err
	}
	fmt.Printf("installed Straza hooks for %s into %s\n", harness, settings)
	return nil
}

// installCodexHooks writes codex's hooks.json and migrates the settings.json
// older straza versions wrote, which no codex release reads. The trust notice
// that makes this install honest is printed by the caller, after the MCP
// registration, so it is the last thing on screen.
func installCodexHooks(self string) error {
	hooks, err := agentguard.CodexHooksPath()
	if err != nil {
		return err
	}
	changed, err := agentguard.InstallCodexHooks(hooks, self)
	if err != nil {
		return err
	}
	if changed {
		fmt.Printf("installed Straza hooks for codex into %s\n", hooks)
	} else {
		fmt.Printf("Straza hooks for codex already current in %s\n", hooks)
	}
	return migrateCodexStaleHooks()
}

// codexTrustNotice is printed by every codex install, last and blunt: writing
// hooks.json does NOT turn enforcement on, and an operator who assumes it did
// has an ungoverned codex with no way to know it.
const codexTrustNotice = "\nACTION REQUIRED (codex will NOT run these hooks until you trust them):\n" +
	"  open codex and run /hooks to review + trust the Straza hooks\n" +
	"Codex skips hooks from a non-managed source until an operator trusts the exact\n" +
	"definition (trust is keyed to its hash, so a later re-install re-gates it).\n" +
	"Until you do that, the codex hook lane is NOT enforcing; codex governance is\n" +
	"the MCP gateway lane only. Nothing outside codex can verify the trust state;\n" +
	"once a governed codex session starts, it shows up in `straza status`.\n"

// migrateCodexStaleHooks cleans up the $CODEX_HOME/settings.json that older
// straza versions wrote. It is silent when there is nothing of ours there.
func migrateCodexStaleHooks() error {
	stale, err := agentguard.CodexStaleHooksPath()
	if err != nil {
		return err
	}
	action, err := agentguard.MigrateCodexStaleHooks(stale)
	if err != nil {
		return err
	}
	switch action {
	case agentguard.CodexStaleRemoved:
		fmt.Printf("migrated codex: deleted the dead wiring at %s (no codex release reads settings.json; it held nothing else)\n", stale)
	case agentguard.CodexStaleCleaned:
		fmt.Printf("migrated codex: removed the dead Straza wiring from %s (no codex release reads settings.json; your other content is untouched)\n", stale)
	}
	return nil
}

// registerMCP writes the harness's MCP server registration for `straza
// mcp` next to the hook wiring. Every Tier-1 harness is written by code; an
// installer that prints a snippet for the operator to paste has not finished
// installing.
func registerMCP(harness, self string) error {
	if harness == "codex" {
		return registerCodexMCP(self)
	}
	mcpPath, err := agentguard.MCPConfigPath(harness)
	if err != nil {
		return err
	}
	if mcpPath == "" {
		return fmt.Errorf("no MCP registration target for harness %q", harness)
	}
	changed, err := agentguard.InstallMCPServer(harness, mcpPath, self)
	if err != nil {
		return err
	}
	if changed {
		fmt.Printf("registered Straza MCP server for %s in %s (restart the harness to load it)\n", harness, mcpPath)
	} else {
		fmt.Printf("Straza MCP server already registered for %s in %s\n", harness, mcpPath)
	}
	return nil
}

// registerCodexMCP writes codex's registration into its config.toml, which is
// TOML rather than the JSON every other harness uses: straza owns a
// marker-delimited block in it and nothing else (installmcptoml.go). A
// registration the user wrote themselves is kept, and said so.
func registerCodexMCP(self string) error {
	path, err := agentguard.CodexMCPConfigPath()
	if err != nil {
		return err
	}
	state, changed, err := agentguard.InstallCodexMCPServer(path, self)
	if err != nil {
		return err
	}
	switch {
	case state == agentguard.CodexMCPUnmanaged:
		fmt.Printf("kept the existing MCP registration for codex in %s (not straza-managed: straza left it alone; delete it and re-run install to have straza own it)\n", path)
	case changed:
		fmt.Printf("registered Straza MCP server for codex in %s (straza-managed block; restart the harness to load it)\n", path)
	default:
		fmt.Printf("Straza MCP server already registered for codex in %s (straza-managed block)\n", path)
	}
	return nil
}

// unregisterMCP removes the harness's MCP server registration, mirroring
// registerMCP: only what straza wrote, never what the user did.
func unregisterMCP(harness string) error {
	if harness == "codex" {
		path, err := agentguard.CodexMCPConfigPath()
		if err != nil {
			return err
		}
		state, changed, err := agentguard.UninstallCodexMCPServer(path)
		if err != nil {
			return err
		}
		switch {
		case changed:
			fmt.Printf("removed Straza MCP server for codex from %s (straza-managed block)\n", path)
		case state == agentguard.CodexMCPUnmanaged:
			fmt.Printf("kept the existing MCP registration for codex in %s (not straza-managed)\n", path)
		}
		return nil
	}
	mcpPath, err := agentguard.MCPConfigPath(harness)
	if err != nil || mcpPath == "" {
		return err
	}
	if err := agentguard.UninstallMCPServer(mcpPath); err != nil {
		return err
	}
	fmt.Printf("removed Straza MCP server for %s from %s\n", harness, mcpPath)
	return nil
}

func uninstallCmd() *cobra.Command {
	var managed bool
	var binDir string
	cmd := &cobra.Command{
		Use:   "uninstall <harness>...",
		Short: "Remove Straza hook wiring from harness settings",
		Args:  cobra.ArbitraryArgs, // same positional operands install takes
		RunE: func(cmd *cobra.Command, args []string) error {
			harnesses, err := harnessArgs(args)
			if err != nil {
				return err
			}
			if managed {
				return agentguard.UninstallManaged(harnesses, binDir, os.Stdout)
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			for _, h := range harnesses {
				if err := uninstallHarnessHooks(h, self); err != nil {
					return err
				}
				if err := unregisterMCP(h); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&managed, "managed", false, "remove the managed/system hook wiring (run with sudo/admin)")
	cmd.Flags().StringVar(&binDir, "bin-dir", "", "managed binary directory used at install time")
	return cmd
}

// uninstallHarnessHooks is installHarnessHooks in reverse. codex again gets
// its own path: its hooks live in hooks.json, and the stale settings.json an
// older straza wrote has to go here too; an uninstall that leaves our debris
// behind is how the operator ends up believing codex is unwired when a dead
// file still says otherwise.
func uninstallHarnessHooks(harness, self string) error {
	if harness != "codex" {
		settings, err := agentguard.SettingsPath(harness)
		if err != nil {
			return err
		}
		if err := agentguard.UninstallHooks(harness, settings, self); err != nil {
			return err
		}
		fmt.Printf("removed Straza hooks for %s from %s\n", harness, settings)
		return nil
	}
	hooks, err := agentguard.CodexHooksPath()
	if err != nil {
		return err
	}
	changed, err := agentguard.UninstallCodexHooks(hooks)
	if err != nil {
		return err
	}
	if changed {
		fmt.Printf("removed Straza hooks for codex from %s\n", hooks)
	} else {
		fmt.Printf("no Straza hooks for codex in %s\n", hooks)
	}
	return migrateCodexStaleHooks()
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			v := version.Get()
			fmt.Printf("straza %s (commit %s, %s, %s/%s)\n", v.Version, v.Commit, v.Go, v.OS, v.Arch)
		},
	}
}

func hookCmd() *cobra.Command {
	var harness, conformancePolicy string
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Hook entrypoint: normalize a harness payload and decide (reads stdin)",
		Long: "Reads one harness event as JSON on stdin, decides it against the signed policy of\n" +
			"this machine's session, spools the audit record and answers in the harness's own\n" +
			"format. straza install wires it into each harness, so you rarely run it by hand.\n\n" +
			"An allow exits 0. Codex then gets empty stdout, and Claude Code and the Python SDK\n" +
			"get the decision on stdout for a tool call. A deny prints the reason on stderr and\n" +
			"exits 2, and Claude Code also reads the decision on stdout. Gemini instead reads\n" +
			"every answer, a deny included, as JSON on stdout and ignores the exit code. When\n" +
			"the hook cannot decide, it denies.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ranHook = true // parse layer passed; in-command logging owns errors now
			hio := agentguard.HookIO{
				Harness: harness,
				Stdin:   cmd.InOrStdin(),
				Stdout:  cmd.OutOrStdout(),
				Stderr:  cmd.ErrOrStderr(),
				Environ: os.Environ,
			}
			if conformancePolicy != "" {
				return agentguard.RunHookConformance(hio, conformancePolicy)
			}
			err := agentguard.RunHook(hio)
			// The exit-1 class (bare errors that never pass the fail-closed
			// choke point, the live codex Stop shape); deny-coded
			// errors are skipped inside, they were recorded at the choke point.
			agentguard.RecordHookFailure(harness, err)
			return err
		},
	}
	cmd.Flags().StringVar(&harness, "harness", "", "the harness dialect: claude-code, codex, gemini or python-sdk (default: the STRAZA_HARNESS variable, else detected from the harness's environment and the payload)")
	cmd.Flags().StringVar(&conformancePolicy, "conformance-policy", "",
		"decide against this PolicySet file instead of the session snapshot, for conformance runs: no check-in with the server and no audit record")
	return cmd
}
