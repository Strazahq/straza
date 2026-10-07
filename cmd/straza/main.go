// Command straza is the Straza client-side enforcement kit: the Policy
// Enforcement Point inside coding-agent harnesses. It enrolls the machine,
// wires hooks and the MCP proxy into each Tier-1 harness, and serves the
// hook, daemon, mcp, exec, doctor, status, trace and logs commands.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/version"
)

func main() {
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
		daemonCmd(),
		mcpCmd(),
		execCmd(),
		doctorCmd(),
		logsCmd(),
		traceCmd(),
		drainCmd(),
	)
	root.AddCommand(devCommands...)
	return root
}

// devCommands holds the commands a build-tagged file adds to the root, such
// as gen-docs under the docsgen tag. A plain build leaves it empty.
var devCommands []*cobra.Command

func execCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec -- <command> [args...]",
		Short: "Run a command through Straza policy, for an agent that has no hooks",
		Long: "The execution shim for agents that have no hook surface: the command is\n" +
			"decided as a canonical shell.exec event against the local signed snapshot\n" +
			"(same engine, spool, and fail-closed semantics as the harness hooks), then run\n" +
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

// drainCmd uploads spooled audit records once. Hidden plumbing: hooks fire it
// detached after every governed decision so audit is near-live without a
// daemon; it is also a manual escape hatch. Always exits 0: a failed drain
// keeps the spool for the next trigger and must never look like an error to
// whatever spawned it.
func drainCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "drain",
		Hidden: true,
		Short:  "Upload spooled audit records once (fired automatically after decisions)",
		RunE: func(*cobra.Command, []string) error {
			if n, err := agentguard.DrainOnce(10 * time.Second); err == nil && n > 0 {
				fmt.Printf("drained %d audit record(s)\n", n)
			}
			return nil
		},
	}
}

// doctorCmd runs every diagnostic: enrollment, credentials, server
// reachability + clock skew, session, snapshot verification, hook wiring,
// audit-spool backlog, kill-switch path. Exit 1 when anything fails so
// scripts can gate on it.
func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose enrollment, connectivity, snapshot, hook wiring, and the audit spool",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			checks := agentguard.Doctor(cmd.Context(), store)
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

func daemonCmd() *cobra.Command {
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Keep the session fresh and apply kill-switch pushes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			d := agentguard.NewDaemon(store, os.Stdout)
			if interval > 0 {
				d.PollInterval = interval
			}
			return d.Run(cmd.Context())
		},
	}
	cmd.Flags().DurationVar(&interval, "poll", 0, "poll-refresh interval (fallback when no push; default 30s)")
	return cmd
}

func mcpCmd() *cobra.Command {
	var harness string
	cmd := &cobra.Command{
		Use:   "mcp [server]",
		Short: "Serve the Straza MCP gateway to this harness over stdio (`straza install` registers it)",
		Args:  cobra.MaximumNArgs(1),
		Long: "An MCP stdio server whose backend is the Straza gateway (/mcp): role-computed\n" +
			"tool catalog, server-side credential injection, rate limits, audit. The proxy\n" +
			"keeps the rotating session token on the wire (the one thing a static MCP\n" +
			"config cannot do) and joins the same governed session as the hooks.\n\n" +
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

func enrollCmd() *cobra.Command {
	var server, user string
	var headless bool
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Log in via the OIDC device flow and enroll this device (--headless: an AI agent with no browser)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target := resolveEnrollServer(server, os.Getenv("STRAZA_SERVER"))
			if target.Server == "" {
				return fmt.Errorf("--server is required (or set STRAZA_SERVER)")
			}
			// Before anything is contacted or written: an enrolment aimed by
			// the environment says so, once, on stderr (resolve.go).
			if line := target.Announce(); line != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), line)
			}
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			if headless {
				return agentguard.EnrollHeadless(cmd.Context(), store, target.Server, user, os.Stdout)
			}
			return agentguard.Enroll(cmd.Context(), store, target.Server, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "strazad base URL")
	cmd.Flags().BoolVar(&headless, "headless", false, "enroll an AI agent or a service account with no browser: with its own key at the built-in issuer, or with STRAZA_CLIENT_ID and STRAZA_CLIENT_SECRET at an external identity provider. Its sessions carry no device")
	cmd.Flags().StringVar(&user, "user", "", "the username the local key is registered under (headless, standalone; default: the name straza keygen stored)")
	return cmd
}

func keygenCmd() *cobra.Command {
	var user string
	var force bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate the local Ed25519 key an AI agent enrolls with headless",
		Args:  cobra.NoArgs,
		Long: "Generates the keypair a headless AI agent or service account authenticates with.\n" +
			"The private half never leaves this machine; print-out includes the\n" +
			"`strazactl users nhi-key set` line an administrator runs to register\n" +
			"the public half, after which `straza enroll --headless` works.",
		RunE: func(_ *cobra.Command, _ []string) error {
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			return agentguard.Keygen(store, user, force, os.Stdout)
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
	cmd.Flags().StringVar(&binDir, "bin-dir", "", "managed binary directory (default: /usr/local/bin or %ProgramData%\\straza\\bin)")
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

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show enrollment and session status",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			store, err := agentguard.OpenStore()
			if err != nil {
				return err
			}
			cfg, err := store.LoadConfig()
			if err != nil {
				fmt.Println("not enrolled")
				return nil
			}
			fmt.Printf("server     %s\n", cfg.ServerURL)
			if id, err := store.LoadIdentity(); err == nil {
				fmt.Println(identityLine(id))
			}
			if ses, err := store.LoadSession(); err == nil {
				fmt.Printf("session    %s (roles %v, attestation %s)\n", ses.SessionID, ses.Roles, ses.Attestation)
				fmt.Printf("snapshot   %s\n", ses.SnapshotID)
				fmt.Println("killswitch edge push (SSE on the server origin, /v1/push); poll-refresh is the backstop; straza doctor probes the lane")
			} else {
				fmt.Println("session    none (start a harness session)")
			}
			return nil
		},
	}
}

// identityLine renders the status row for the enrolled identity. A headless
// enrollment has no device, so its row names the credential lane and says so
// instead of printing an empty device id.
func identityLine(id agentguard.Identity) string {
	if id.Headless != "" {
		return fmt.Sprintf("identity   %s (headless, %s lane, no device)", id.Username, id.Headless)
	}
	return fmt.Sprintf("identity   %s (device %s)", id.Username, id.DeviceID)
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
