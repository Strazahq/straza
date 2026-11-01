package agentguard

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/harnesscfg"
)

// Managed layout: every artifact the client-side decision depends on
// (binary, config, harness hook wiring) lives in system paths a regular
// user cannot write. Modifying them requires sudo/administrator, and
// any modification changes the measured hashes the server verifies at
// check-in. Sessions and keys live in the Straza background service, and
// the user home (~/.straza/state) keeps only what the hooks write for their
// user: the journal, the error log and the capture watermarks.

// ManagedRoot returns the managed config root: $STRAZA_SYSTEM
// (test seam) or the per-OS system location.
func ManagedRoot() string {
	if v := os.Getenv("STRAZA_SYSTEM"); v != "" {
		return v
	}
	if osName() == "windows" {
		return filepath.Join(managedProgramData(), "straza")
	}
	return "/etc/straza"
}

// ManagedConfigPath returns the managed config.yaml location.
func ManagedConfigPath() string { return filepath.Join(ManagedRoot(), "config.yaml") }

// ManagedInstalled reports whether a managed layout is present and
// tamper-protected: on unix root-owned and not group/world-writable, on
// Windows changeable only by administrators. This is only the local
// *claim*: the server independently verifies the measured
// hashes against the expected-hash registry, and `straza doctor`
// audits ownership in depth. With the $STRAZA_SYSTEM seam set the
// layout is treated as protected: the seam only relocates what straza
// measures, never what the harness actually loads, so it cannot launder a
// tampered enforcement file past the server.
func ManagedInstalled() bool {
	fi, err := os.Stat(ManagedConfigPath())
	if err != nil {
		return false
	}
	if os.Getenv("STRAZA_SYSTEM") != "" {
		return true
	}
	return tamperProtected(ManagedConfigPath(), fi)
}

// ManagedRootUntrusted returns the refusal for a managed config at
// ManagedConfigPath that ManagedInstalled does not trust, and nil when there
// is none or it is trusted. Only Windows answers it, because any user may
// create folders under ProgramData. On Linux and macOS only root can create
// the managed root, so an untrusted file there is no managed layout at all.
func ManagedRootUntrusted() error { return managedRootUntrusted() }

// LockManagedRoot locks the managed root to administrators and resets the
// owner and access rules of what is already inside, as `straza install
// --managed` does before and after it writes there. The Straza MSI runs it
// through `straza installer lock` once straza.exe sits under the root. It
// refuses a root, or anything in it, that is a link or that an account
// other than an administrator created.
func LockManagedRoot() error {
	root := ManagedRoot()
	if err := lockManagedRoot(root); err != nil {
		return err
	}
	return sealManagedRoot(root)
}

// ManagedBinDir returns the directory of the system straza binary the
// managed wiring runs: $STRAZA_MANAGED_BIN_DIR or the per-OS default. On
// Linux that is /usr/bin, where the straza package installs it, because
// distribution packages never write under /usr/local. On macOS it is
// /opt/straza/bin, where the Straza package for macOS installs it, because
// a person may own /usr/local/bin there and swap what the hooks run.
func ManagedBinDir() string {
	if v := os.Getenv("STRAZA_MANAGED_BIN_DIR"); v != "" {
		return v
	}
	switch osName() {
	case "windows":
		return filepath.Join(managedProgramData(), "straza", "bin")
	case "linux":
		return "/usr/bin"
	case "darwin":
		return "/opt/straza/bin"
	}
	return "/usr/local/bin"
}

// ManagedBinaryPath returns the system straza binary path under dir
// (empty dir = ManagedBinDir()).
func ManagedBinaryPath(dir string) string {
	if dir == "" {
		dir = ManagedBinDir()
	}
	name := "straza"
	if osName() == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

func programData() string {
	if v := os.Getenv("ProgramData"); v != "" {
		return v
	}
	return `C:\ProgramData`
}

// ManagedHookPath is the ONE resolver for a harness's managed hook file,
// shared by everything that touches it: install (writes it), uninstall,
// attestation in the background service (hashes it), and doctor's wiring
// checks (doctorwiring.go), so the consumers cannot diverge by
// construction. It is ManagedSettingsPath
// EXCEPT codex, whose requirements.toml resolves through
// CodexManagedRequirementsPath (it honors a relocated %ProgramData%; the
// vendor defines the path in terms of that variable). A consumer resolving
// the vendor-default path on a relocated-ProgramData box would read (or
// hash) a file install never wrote.
func ManagedHookPath(harness string) (string, error) {
	if harness == "codex" {
		return CodexManagedRequirementsPath()
	}
	return ManagedSettingsPath(harness)
}

// ManagedInstallOptions parameterizes InstallManaged.
type ManagedInstallOptions struct {
	Harnesses []string
	ServerURL string // required: the strazad this layout pins for every user
	BinDir    string // "" → ManagedBinDir()
}

// InstallManaged writes the root-owned managed layout: the managed
// config and managed/system hook wiring for each harness, which runs the
// system straza binary. On Linux the straza package installs that binary,
// on Windows the Straza MSI and on macOS the Straza package for macOS, so
// install never writes it and refuses before writing anything when it is
// missing. The server registers the expected hash of the wiring
// it published at its own boot, so the install registers nothing. It must
// run with privileges that can write the system paths; those paths being
// unwritable by regular users is exactly the tamper evidence the managed
// tier relies on. An empty opts.ServerURL is refused before anything is
// written.
func InstallManaged(ctx context.Context, opts ManagedInstallOptions, w io.Writer) error {
	binPath := ManagedBinaryPath(opts.BinDir)
	if err := packagedBinaryRefusal(binPath); err != nil {
		return err
	}
	cfg, err := managedSourceConfig(ctx, opts)
	if err != nil {
		return err
	}
	// Every harness's lane is decided before anything is written, so a
	// refusal leaves the machine as it was. The server lane writes the
	// SERVER-published artifact verbatim (default layout, no operator
	// content, verified against the pinned keys, running binPath), and the
	// local lane writes this build's render with a printed reason. A refusal
	// never falls back (managedfetch.go).
	docs := make([]*harnesscfg.Document, len(opts.Harnesses))
	notes := make([]string, len(opts.Harnesses))
	for i, harness := range opts.Harnesses {
		if docs[i], notes[i], err = fetchServedWiring(ctx, cfg, harness, binPath); err != nil {
			return err
		}
	}

	// 1. Managed config (world-readable, owner-writable only), in a root only
	// administrators can change from here on.
	if err := lockManagedRoot(ManagedRoot()); err != nil {
		return err
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(ManagedConfigPath(), raw, 0o644); err != nil { // #nosec G306 -- managed config is deliberately world-readable
		return fmt.Errorf("write managed config (need sudo/admin?): %w", err)
	}
	fmt.Fprintf(w, "managed config  %s\n", ManagedConfigPath())

	// 2. The system binary hooks will invoke and measure. The package or the
	// MSI put it in place, which was checked above.
	fmt.Fprintf(w, "managed binary  %s\n", binPath)

	// 3. Managed hook wiring per harness. The path comes from ManagedHookPath
	// (the shared resolver), so the file written here is byte-identically the
	// one the background service hashes for attestation and doctor reads.
	for i, harness := range opts.Harnesses {
		settings, err := ManagedHookPath(harness)
		if err != nil {
			return err
		}
		served, laneNote := docs[i], notes[i]
		switch {
		case served != nil:
			if err := writeServedArtifacts(harness, served, binPath); err != nil {
				return err
			}
		case harness == "codex":
			// codex's managed layer is a TOML requirements file, not a
			// managed-settings.json: the JSON writer would produce a file
			// codex cannot parse. Its hooks are the ones codex
			// auto-trusts, so this is the codex lane that actually enforces.
			if _, err := InstallCodexManagedHooks(settings, binPath); err != nil {
				return fmt.Errorf("write managed wiring for codex (need sudo/admin?): %w", err)
			}
		default:
			if err := InstallManagedHooks(harness, settings, binPath); err != nil {
				return fmt.Errorf("write managed wiring for %s (need sudo/admin?): %w", harness, err)
			}
		}
		if served != nil {
			fmt.Fprintf(w, "managed wiring  %s → %s (server-published %s)\n", harness, settings, servedArtifactID(harness, served))
		} else {
			fmt.Fprintf(w, "managed wiring  %s → %s (local render: %s)\n", harness, settings, laneNote)
			// A local render is a hash the server never published, so a
			// managed check-in cannot verify it until an administrator adds
			// it; the default layout never reaches this line.
			h, err := FileSHA256(settings)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "                the server does not hold this wiring's hash; before the attestation floor is managed an administrator registers it:\n")
			fmt.Fprintf(w, "                  strazactl attestation add --artifact hooks.%s --harness %s --platform %s/%s --hash %s\n",
				harness, harness, runtime.GOOS, runtime.GOARCH, h)
		}
		if harness == "codex" {
			// The one place straza can honestly say codex is enforcing:
			// requirements-sourced hooks are auto-trusted and the user hook
			// browser cannot disable them. Everything the operator still gets
			// to decide is printed, not assumed.
			fmt.Fprintf(w, "                codex auto-trusts hooks from this file (no /hooks step) and users cannot disable them; [features].hooks = true is pinned so a user's own `hooks = false` cannot switch them off\n")
			fmt.Fprintf(w, "                to ALSO ignore user/project/session hooks, add `%s` to %s yourself. Straza does not write it (it disables hooks the user installed)\n", CodexManagedLockdownLine, settings)
			fmt.Fprintf(w, "                requires codex >= 0.124 (hooks GA + requirements.toml support); the hook command is written UNQUOTED (the only form codex >= 0.146 spawns; its re-tokenizer rejects a quoted path)\n")
		}

		// Wiring at a vendor-retired path is silently dead (the harness never
		// reads it; governance looks installed but is off), so migrate our
		// own stale file out from under the operator; a foreign file stays.
		if legacy := LegacyManagedSettingsPath(harness); legacy != "" && legacy != settings && fileMentionsStraz(legacy) {
			if err := os.Remove(legacy); err != nil {
				fmt.Fprintf(w, "WARNING: retired wiring at %s could not be removed (%v). Delete it manually; current %s never reads it\n", legacy, err, harness)
			} else {
				fmt.Fprintf(w, "migrated        %s: removed retired wiring at %s (unread by current releases)\n", harness, legacy)
			}
		}

		// 3b. Managed MCP registration (claude-code only today): the straza
		// proxy entry invokes the managed binary, so what the harness runs is
		// the same artifact the server verifies. Deliberately NOT a measured
		// attestation artifact: verifyAttestation fails closed on any
		// registered-but-unreported artifact, so adding one would drop every
		// already-deployed managed fleet to att=none until re-installed;
		// it needs a coordinated spec/attestation + registry rollout first.
		mcpPath, err := ManagedMCPConfigPath(harness)
		if err != nil {
			return err
		}
		if mcpPath != "" {
			// The server lane already wrote the mcp artifact verbatim; the
			// merge-writer runs only when it did not (local lane, or a served
			// document without the artifact).
			if served == nil || !docHasArtifact(served, "mcp."+harness) {
				if _, err := InstallManagedMCPServer(harness, mcpPath, binPath); err != nil {
					return fmt.Errorf("write managed MCP registration for %s (need sudo/admin?): %w", harness, err)
				}
			}
			fmt.Fprintf(w, "managed mcp     %s → %s (exclusive: %s now loads ONLY servers in this file)\n", harness, mcpPath, harness)
		}

		// 3c. codex has NO system-scope MCP config to write (adapters/codex.yaml:
		// the managed layer it clones from claude-code covers hooks only), so its
		// registration necessarily lives in a user's config.toml. Managed mode
		// still writes it (pointed at the managed binary, so the harness runs
		// the artifact the server verifies) for the user who invoked sudo
		// rather than for root, and says exactly which file it wrote. Like the
		// managed MCP file above it is NOT a measured artifact, and here that is
		// structural rather than a rollout concern: config.toml is user-writable,
		// so measuring it would turn every ordinary edit into att=none.
		if harness == "codex" {
			codexPath, err := CodexMCPConfigPathForInvoker()
			if err != nil {
				return err
			}
			if codexPath == "" {
				skipped, _ := codexMCPNoPersonLines()
				fmt.Fprintln(w, skipped)
				continue
			}
			state, changed, err := InstallCodexMCPServer(codexPath, binPath)
			if err != nil {
				return fmt.Errorf("write codex MCP registration: %w", err)
			}
			switch {
			case state == CodexMCPUnmanaged:
				fmt.Fprintf(w, "user mcp        codex → %s KEPT the existing registration (not straza-managed; delete it and re-run to hand it to straza)\n", codexPath)
			case changed:
				fmt.Fprintf(w, "user mcp        codex → %s (straza-managed block → %s; codex has no system-scope MCP config)\n", codexPath, binPath)
			default:
				fmt.Fprintf(w, "user mcp        codex → %s (straza-managed block already current)\n", codexPath)
			}
		}
	}

	// 4. What install wrote under the root gets the root's owner and DACL.
	return sealManagedRoot(ManagedRoot())
}

// The lines install and uninstall print for codex's MCP registration when
// they run as the Windows LocalSystem account, as the Straza MSI and Intune
// run them: codex reads that registration only from a person's own
// config.toml, and SYSTEM's profile is no person's.
const (
	codexMCPSkippedAsSystem = "user mcp        codex: skipped. Codex reads MCP servers only from each person's own config.toml, " +
		"and this command runs as the Windows SYSTEM account, which is no person's account, so it wrote no Straza MCP registration for codex. " +
		"The managed codex hooks above still enforce every codex tool call, so nothing more is needed."
	codexMCPKeptAsSystem = "kept each person's codex config.toml, because this command runs as the Windows SYSTEM account, which is no person's account, " +
		"so it removed no Straza MCP registration for codex. A person who added one deletes the [mcp_servers.straza] table from their own config.toml."
)

// The macOS twins of the lines above, for a run as root that no person
// started through sudo, as Jamf and other device management tools run it:
// root's own config.toml is no person's either.
const (
	codexMCPSkippedAsRoot = "user mcp        codex: skipped. Codex reads MCP servers only from each person's own config.toml, " +
		"and this command runs as root but not through sudo from a person's account, as Jamf and other device management tools run it, " +
		"so it wrote no Straza MCP registration for codex. The managed codex hooks above still enforce every codex tool call, so nothing more is needed."
	codexMCPKeptAsRoot = "kept each person's codex config.toml, because this command runs as root but not through sudo from a person's account, " +
		"so it removed no Straza MCP registration for codex. A person who added one deletes the [mcp_servers.straza] table from their own config.toml."
)

// codexMCPNoPersonLines returns the lines install and uninstall print when
// CodexMCPConfigPathForInvoker resolves no person's config.toml: the root
// run's on macOS and the SYSTEM run's elsewhere.
func codexMCPNoPersonLines() (skipped, kept string) {
	if osName() == "darwin" {
		return codexMCPSkippedAsRoot, codexMCPKeptAsRoot
	}
	return codexMCPSkippedAsSystem, codexMCPKeptAsSystem
}

// packagedBinaryRefusal returns why install --managed cannot run without a
// regular file at binPath, or nil when the binary is there. Only the
// default path is one the straza package, the Straza MSI or the Straza
// package for macOS installs, so a custom path gets its own sentence.
func packagedBinaryRefusal(binPath string) error {
	if fi, err := os.Stat(binPath); err == nil && fi.Mode().IsRegular() {
		return nil
	}
	switch osName() {
	case "windows":
		if binPath == vendorDefaultBinPath("windows") {
			return fmt.Errorf("`straza install --managed` found no %s, which the Straza MSI installs. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
				"Install the Straza MSI for Windows first, then run this command again.", binPath)
		}
		return fmt.Errorf("`straza install --managed` found no straza.exe at %s. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Put straza.exe there, or leave out --bin-dir and STRAZA_MANAGED_BIN_DIR to use %s from the Straza MSI, then run this command again.",
			binPath, vendorDefaultBinPath("windows"))
	case "darwin":
		if binPath == vendorDefaultBinPath("darwin") {
			return fmt.Errorf("`straza install --managed` found no %s, which the Straza package for macOS installs. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
				"Install the Straza package first, then run this command again.", binPath)
		}
		return fmt.Errorf("`straza install --managed` found no straza binary at %s. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Put the straza binary there, or leave out --bin-dir and STRAZA_MANAGED_BIN_DIR to use %s from the Straza package for macOS, then run this command again.",
			binPath, vendorDefaultBinPath("darwin"))
	}
	if binPath == vendorDefaultBinPath("linux") {
		return fmt.Errorf("`straza install --managed` found no %s, which the Straza package installs. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Install the straza package for this distribution first, then run this command again.", binPath)
	}
	return fmt.Errorf("`straza install --managed` found no straza binary at %s. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
		"Put the straza binary there, or leave out --bin-dir and STRAZA_MANAGED_BIN_DIR to use /usr/bin/straza from the straza package, then run this command again.", binPath)
}

// managedSourceConfig fetches the snapshot keys of the server the operator
// named. The layout pins that server for every user of the machine, so it
// never comes from a user's own config, which that user and any agent
// running as them can edit.
func managedSourceConfig(ctx context.Context, opts ManagedInstallOptions) (Config, error) {
	if opts.ServerURL == "" {
		return Config{}, fmt.Errorf("`straza install --managed` needs `--server <server-url>`. "+
			"The managed layout pins the server and its signing keys for every user of this machine, "+
			"so they come from you and never from a user's own settings. "+
			"Run it again with sudo, or as an administrator on Windows: straza install --managed --server https://straza.example.com %s",
			strings.Join(opts.Harnesses, " "))
	}
	keys, err := NewClient(opts.ServerURL).SnapshotKeys(ctx)
	if err != nil {
		return Config{}, fmt.Errorf("fetch snapshot keys from %s: %w", opts.ServerURL, err)
	}
	return Config{ServerURL: opts.ServerURL, SnapshotKeys: keys}, nil
}

// UninstallManaged removes the Straza hook entries from each harness's
// managed settings file and the straza entry from its managed MCP file
// (other entries, and the file itself, stay: an emptied managed-mcp.json
// still means "MCP locked down" to claude-code, which is the operator's
// policy to lift, not ours). The managed config and system binary are left
// in place (remove them manually, or re-run install --managed to re-wire);
// remember to retire the old hashes from the registry.
func UninstallManaged(harnesses []string, binDir string, w io.Writer) error {
	binPath := ManagedBinaryPath(binDir)
	for _, harness := range harnesses {
		settings, err := ManagedHookPath(harness)
		if err != nil {
			return err
		}
		if harness == "codex" {
			// TOML requirements file, not a settings.json; the JSON writer
			// would fail on it (see InstallManaged's codex branch).
			if _, err := UninstallCodexManagedHooks(settings); err != nil {
				return fmt.Errorf("unwire codex: %w", err)
			}
		} else if err := UninstallHooks(harness, settings, binPath); err != nil {
			return fmt.Errorf("unwire %s: %w", harness, err)
		}
		fmt.Fprintf(w, "removed Straza hooks for %s from %s\n", harness, settings)
		mcpPath, err := ManagedMCPConfigPath(harness)
		if err != nil {
			return err
		}
		if mcpPath != "" {
			if err := UninstallMCPServer(mcpPath); err != nil {
				return fmt.Errorf("remove managed MCP registration for %s: %w", harness, err)
			}
			fmt.Fprintf(w, "removed Straza MCP server for %s from %s\n", harness, mcpPath)
		}
		// The user-scope half of the codex story (see InstallManaged step 3c).
		if harness == "codex" {
			codexPath, err := CodexMCPConfigPathForInvoker()
			if err != nil {
				return err
			}
			state, changed := CodexMCPMissing, false
			if codexPath == "" {
				_, kept := codexMCPNoPersonLines()
				fmt.Fprintln(w, kept)
			} else if state, changed, err = UninstallCodexMCPServer(codexPath); err != nil {
				return fmt.Errorf("remove codex MCP registration: %w", err)
			}
			switch {
			case changed:
				fmt.Fprintf(w, "removed Straza MCP server for codex from %s (straza-managed block)\n", codexPath)
			case state == CodexMCPUnmanaged:
				fmt.Fprintf(w, "kept the existing MCP registration for codex in %s (not straza-managed)\n", codexPath)
			}
		}
		if legacy := LegacyManagedSettingsPath(harness); legacy != "" && fileMentionsStraz(legacy) {
			if err := os.Remove(legacy); err == nil {
				fmt.Fprintf(w, "removed retired wiring at %s\n", legacy)
			}
		}
	}
	return nil
}
