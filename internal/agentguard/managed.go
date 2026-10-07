package agentguard

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Managed layout: every artifact the client-side decision depends on
// (binary, config, harness hook wiring) lives in system paths a regular
// user cannot write. Modifying them requires sudo/administrator, and
// any modification changes the measured hashes the server verifies at
// check-in. Per-session state stays under the user home (~/.straza/state):
// enforcement artifacts are root-owned, session state is per-user data the
// hooks must be able to write.

// ManagedRoot returns the managed config root: $STRAZA_SYSTEM
// (test seam) or the per-OS system location.
func ManagedRoot() string {
	if v := os.Getenv("STRAZA_SYSTEM"); v != "" {
		return v
	}
	if osName() == "windows" {
		return filepath.Join(programData(), "straza")
	}
	return "/etc/straza"
}

// ManagedConfigPath returns the managed config.yaml location.
func ManagedConfigPath() string { return filepath.Join(ManagedRoot(), "config.yaml") }

// ManagedInstalled reports whether a managed layout is present and, on unix,
// plausibly tamper-protected (root-owned, not group/world-writable). This is
// only the local *claim*: the server independently verifies the measured
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
	return tamperProtected(fi)
}

// ManagedBinDir returns where the system copy of straza lives:
// $STRAZA_MANAGED_BIN_DIR or the per-OS default.
func ManagedBinDir() string {
	if v := os.Getenv("STRAZA_MANAGED_BIN_DIR"); v != "" {
		return v
	}
	if osName() == "windows" {
		return filepath.Join(programData(), "straza", "bin")
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

// managedHookPath is the ONE resolver for a harness's managed hook file,
// shared by everything that touches it: install (writes it), uninstall,
// attestation (hashes it), and doctor's wiring checks (doctorwiring.go), so
// the consumers cannot diverge by construction. It is ManagedSettingsPath
// EXCEPT codex, whose requirements.toml resolves through
// CodexManagedRequirementsPath (it honors a relocated %ProgramData%; the
// vendor defines the path in terms of that variable). A consumer resolving
// the vendor-default path on a relocated-ProgramData box would read (or
// hash) a file install never wrote.
func managedHookPath(harness string) (string, error) {
	if harness == "codex" {
		return CodexManagedRequirementsPath()
	}
	return ManagedSettingsPath(harness)
}

// selfHash hashes the running executable exactly once per process: the running
// image cannot change, so re-hashing it on every checkin/poll tick (the daemon
// ticks every 30 s in prod) is pure waste, and if the file on disk is swapped
// mid-run (an upgrade), the cached value is the TRUER measurement: attestation
// reports the binary that is executing, not whatever now sits at its path.
var selfHash = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	h, err := fileSHA256(exe)
	if err != nil {
		return ""
	}
	return h
})

// measureAttestation measures every artifact the local decision depends on:
// the running binary always; in managed mode also the managed
// config and this harness's managed hook wiring. A tampered or deleted
// managed artifact therefore changes (or drops) a measurement, which the
// server maps to att=none (spec/attestation v1beta1).
func measureAttestation(harness string) Attestation {
	att := Attestation{
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Hashes:   map[string]string{},
	}
	if h := selfHash(); h != "" {
		att.Hashes["self"] = h
	}
	if !ManagedInstalled() {
		return att
	}
	att.Managed = true
	if h, err := fileSHA256(ManagedConfigPath()); err == nil {
		att.Hashes["config"] = h
	}
	// The harness's MANAGED hook wiring: root-owned, resolved through
	// managedHookPath, the same resolver install writes through, so the hash
	// is over the file the harness actually loads (for codex, the
	// requirements.toml). The USER-scope files are deliberately not measured. They are
	// user-writable by design, so every ordinary edit would read as
	// tampering, and on codex a user-scope hook is trust-gated anyway, so a
	// hash of it proves nothing about enforcement. Attestation measures what
	// the operator cannot change; the honest place for the codex hook lane to
	// be attestation-relevant is the managed one.
	if p, err := managedHookPath(harness); err == nil {
		if h, err := fileSHA256(p); err == nil {
			att.Hashes["hooks."+harness] = h
		}
	}
	return att
}

// ManagedInstallOptions parameterizes InstallManaged.
type ManagedInstallOptions struct {
	Harnesses []string
	ServerURL string // required: the strazad this layout pins for every user
	BinDir    string // "" → ManagedBinDir()
}

// InstallManaged writes the root-owned managed layout: the managed
// config, a system copy of the straza binary, and managed/system hook
// wiring for each harness. The server registers the expected hash of the
// wiring it published at its own boot, so the install registers nothing. It
// must run with privileges that can write the system paths; those paths
// being unwritable by regular users is exactly the tamper evidence the
// managed tier relies on. An empty opts.ServerURL is refused before
// anything is written.
func InstallManaged(ctx context.Context, opts ManagedInstallOptions, w io.Writer) error {
	cfg, err := managedSourceConfig(ctx, opts)
	if err != nil {
		return err
	}

	// 1. Managed config (world-readable, owner-writable only).
	if err := os.MkdirAll(ManagedRoot(), 0o755); err != nil { // #nosec G301 -- every user's harness must read the managed layout; root-writable only
		return fmt.Errorf("create managed root (need sudo/admin?): %w", err)
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(ManagedConfigPath(), raw, 0o644); err != nil { // #nosec G306 -- managed config is deliberately world-readable
		return fmt.Errorf("write managed config (need sudo/admin?): %w", err)
	}
	fmt.Fprintf(w, "managed config  %s\n", ManagedConfigPath())

	// 2. System binary copy: the one hooks will invoke and measure.
	binPath := ManagedBinaryPath(opts.BinDir)
	if err := copySelf(binPath); err != nil {
		return err
	}
	fmt.Fprintf(w, "managed binary  %s\n", binPath)

	// 3. Managed hook wiring per harness. The path comes from managedHookPath
	// (the shared resolver), so the file written here is byte-identically the
	// one measureAttestation hashes and doctor reads.
	for _, harness := range opts.Harnesses {
		settings, err := managedHookPath(harness)
		if err != nil {
			return err
		}
		// Lane decision (managedfetch.go): the SERVER-published artifact
		// written verbatim (default layout, no operator content, artifact
		// verified against the pinned keys), or the local render with a
		// printed reason. A verification failure aborts; it never falls back.
		served, laneNote, err := fetchServedWiring(ctx, cfg, harness, binPath)
		if err != nil {
			return err
		}
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
			h, err := fileSHA256(settings)
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

	return nil
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
		settings, err := managedHookPath(harness)
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
			state, changed, err := UninstallCodexMCPServer(codexPath)
			if err != nil {
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

// copySelf copies the running executable to dst (0755).
func copySelf(dst string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(exe) // #nosec G304 -- copying our own binary into the managed layout
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { // #nosec G301 -- system bin dir must be world-readable
		return fmt.Errorf("create managed bin dir (need sudo/admin?): %w", err)
	}
	if err := os.WriteFile(dst, raw, 0o755); err != nil { // #nosec G306 G703 -- dst is the operator's explicit --bin-dir; the system binary must be executable by all users
		return fmt.Errorf("install managed binary (need sudo/admin?): %w", err)
	}
	return nil
}
