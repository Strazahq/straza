//go:build darwin

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
)

func init() {
	devCommands = append(devCommands, installerCmd())
}

// pkgIdentifier is the identifier of the Straza package for macOS, and
// launchdLabel is the label of the background service it installs.
const (
	pkgIdentifier = "ai.straza.straza"
	launchdLabel  = "ai.straza.service"
)

// The system tools the uninstall runs, by full path, so a run as root never
// takes them from a PATH that someone else set.
const (
	launchctlTool = "/bin/launchctl"
	dsclTool      = "/usr/bin/dscl"
	pkgutilTool   = "/usr/sbin/pkgutil"
)

// macLayout holds the paths of what the Straza package for macOS installs
// or makes, the paths its files in deploy/macos name.
type macLayout struct {
	plist     string   // the LaunchDaemon
	binary    string   // the one universal straza binary
	links     []string // the PATH links to binary
	runDir    string   // the service's socket and git host file
	newsyslog string   // the rotation rule of the service's log
	stateDir  string   // the service's key, sessions and counter
	docDir    string   // the license files
	folders   []string // the package's folders, innermost first, removed once empty
	logDir    string   // the folder of the log launchd writes for the service
}

// pkgLayout is the layout on a Mac. Tests use one in a temporary folder.
var pkgLayout = macLayout{
	plist:     "/Library/LaunchDaemons/" + launchdLabel + ".plist",
	binary:    "/opt/straza/bin/straza",
	links:     []string{"/usr/local/bin/straza", "/usr/local/bin/git-remote-straza"},
	runDir:    filepath.Dir(localapi.SystemSocket),
	newsyslog: "/etc/newsyslog.d/" + launchdLabel + ".conf",
	stateDir:  localapi.SystemStateDir,
	docDir:    "/opt/straza/share/doc",
	folders:   []string{"/opt/straza/bin", "/opt/straza/share", "/opt/straza"},
	logDir:    "/var/log/straza-service",
}

// macSystem is what the uninstall asks of the Mac besides its files: the
// effective user id, the system tools and the harness unwire. Tests swap
// it to record the order of the steps without root, launchctl or dscl.
type macSystem struct {
	euid   func() int
	run    func(tool string, args ...string) error
	unwire func(w io.Writer) error
}

// thisMac is the macSystem of the Mac the command runs on.
var thisMac = macSystem{
	euid: os.Geteuid,
	run:  runTool,
	unwire: func(w io.Writer) error {
		return agentguard.UninstallManaged([]string{"claude-code", "codex", "gemini"}, "", w)
	},
}

// installerCmd is the hidden `straza installer` group on macOS. Its one
// step, uninstall, removes what the Straza package for macOS installed,
// because a macOS package carries no uninstall of its own. The group and
// the step are hidden, which keeps them out of help and out of the CLI
// reference, whose writer skips every command that is not available.
func installerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "installer",
		Short:  "Run a step of the Straza package for macOS",
		Hidden: true,
	}
	var purge bool
	uninstall := &cobra.Command{
		Use:    "uninstall",
		Short:  "Remove the Straza background service, its harness wiring and its files from this Mac",
		Hidden: true,
		Args:   cobra.ArbitraryArgs, // the step refuses operands itself, with a sentence
		RunE: func(_ *cobra.Command, args []string) error {
			return installerUninstall(thisMac, pkgLayout, purge, args, os.Stdout)
		},
	}
	uninstall.Flags().BoolVar(&purge, "purge", false, "also delete "+pkgLayout.stateDir+
		" with this Mac's key and sessions, the service's log, and the account and group "+localapi.ServiceAccount)
	cmd.AddCommand(uninstall)
	return cmd
}

// installerUninstall removes the Straza package for macOS from the Mac
// that sys and l describe, and says what it removed on w. It stops the
// service before it unwires Claude Code, Codex and Gemini, so a failure
// never leaves a running service beside unwired harnesses. Each step skips
// what is already gone, and only the package's empty folders come after the
// binary, so a rerun after a failure finishes the job. Without purge the
// state folder and the account stay, so a new install finds its enrollment.
func installerUninstall(sys macSystem, l macLayout, purge bool, args []string, w io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("`straza installer uninstall` takes no operands, and it was given %q, so it changed nothing. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Run `sudo straza installer uninstall`, with --purge only to delete this Mac's key, sessions and service account too.", args)
	}
	if sys.euid() != 0 {
		return errors.New("`straza installer uninstall` changes system folders and must run as root. Run it again with sudo.") //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
	}
	rerun := "sudo " + l.binary + " installer uninstall"
	if purge {
		rerun += " --purge"
	}
	fail := func(what string, err error) error {
		return fmt.Errorf("`straza installer uninstall` could not %s (%v), so it stopped before the steps after it. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Fix that, then run `%s` again, which skips what is already gone.", what, err, rerun)
	}

	service := "system/" + launchdLabel
	if sys.run(launchctlTool, "print", service) == nil {
		if err := sys.run(launchctlTool, "bootout", service); err != nil && sys.run(launchctlTool, "print", service) == nil {
			return fail("stop the Straza background service "+service, err)
		}
		fmt.Fprintf(w, "stopped the Straza background service %s\n", service)
	}
	if err := removePath(l.plist, w); err != nil {
		return fail("remove "+l.plist, err)
	}
	if err := sys.unwire(w); err != nil {
		return fail("unwire Claude Code, Codex and Gemini", err)
	}
	for _, p := range []string{l.runDir, l.newsyslog} {
		if err := removePath(p, w); err != nil {
			return fail("remove "+p, err)
		}
	}
	if purge {
		// Both folders belong to the account, so they go before it does.
		if err := purgeFolder(l.stateDir, "the state folder of the Straza background service, with this Mac's key and sessions", rerun, w); err != nil {
			return err
		}
		if err := purgeFolder(l.logDir, "the log folder of the Straza background service", rerun, w); err != nil {
			return err
		}
		for _, rec := range []struct{ path, what string }{
			{"/Users/" + localapi.ServiceAccount, "the account " + localapi.ServiceAccount},
			{"/Groups/" + localapi.ServiceAccount, "the group " + localapi.ServiceAccount},
		} {
			if sys.run(dsclTool, ".", "-read", rec.path, "RecordName") != nil {
				continue
			}
			if err := sys.run(dsclTool, ".", "-delete", rec.path); err != nil {
				return fail("delete "+rec.what, err)
			}
			fmt.Fprintf(w, "deleted %s\n", rec.what)
		}
	}
	if sys.run(pkgutilTool, "--pkg-info", pkgIdentifier) == nil {
		if err := sys.run(pkgutilTool, "--forget", pkgIdentifier); err != nil {
			return fail("forget the package receipt "+pkgIdentifier, err)
		}
		fmt.Fprintf(w, "forgot the package receipt %s\n", pkgIdentifier)
	}
	for _, link := range l.links {
		target, err := os.Readlink(link)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil || target != l.binary:
			fmt.Fprintf(w, "kept %s, because it is not the Straza package's link to %s\n", link, l.binary)
		default:
			if err := removePath(link, w); err != nil {
				return fail("remove "+link, err)
			}
		}
	}
	if err := removePath(l.docDir, w); err != nil {
		return fail("remove "+l.docDir, err)
	}
	if err := removePath(l.binary, w); err != nil {
		return fail("remove "+l.binary, err)
	}
	// A folder that still holds a file the package did not install stays.
	// The binary is gone by now, so a folder that will not go is reported
	// for an administrator instead of a rerun, and the uninstall succeeds.
	for _, dir := range l.folders {
		err := os.Remove(dir)
		switch {
		case err == nil:
			fmt.Fprintf(w, "removed %s\n", dir)
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTEMPTY):
		default:
			fmt.Fprintf(w, "left %s in place, because removing it failed (%v). An administrator may remove it by hand.\n", dir, err)
		}
	}
	if _, err := os.Lstat(l.stateDir); !purge && err == nil {
		fmt.Fprintf(w, "kept %s, with this Mac's key and sessions, and the account %s, so a new install of the Straza package finds its enrollment. "+
			"To delete them too, install the package again and run `sudo straza installer uninstall --purge`.\n", l.stateDir, localapi.ServiceAccount)
	}
	return nil
}

// purgeFolder deletes dir, which what describes, with everything inside,
// and says so on w. A missing folder is already purged. It refuses a dir
// that is a link or not a folder, so it never deletes what a link points at.
func purgeFolder(dir, what, rerun string, w io.Writer) error {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err == nil && !fi.IsDir():
		return fmt.Errorf("`straza installer uninstall --purge` did not delete %s, because it is a link or not a folder, "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"and --purge deletes it only as %s, never what a link points at. "+
			"An administrator checks what is there and removes it by hand if nothing needs it, then runs `%s` again.", dir, what, rerun)
	case err == nil:
		err = os.RemoveAll(dir)
	}
	if err != nil {
		return fmt.Errorf("`straza installer uninstall --purge` could not delete %s, %s (%v), "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"so it stopped before the steps after it. Fix that, then run `%s` again, which skips what is already gone.", dir, what, err, rerun)
	}
	fmt.Fprintf(w, "removed %s\n", dir)
	return nil
}

// removePath removes path, with everything inside when it is a folder, and
// says so on w. A path that is already gone is fine. A link is removed
// itself, never what it points at.
func removePath(path string, w io.Writer) error {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	fmt.Fprintf(w, "removed %s\n", path)
	return nil
}

// runTool runs a system tool and, when it fails, returns an error that
// carries the tool's own output, because launchctl, dscl and pkgutil say
// why only there.
func runTool(tool string, args ...string) error {
	out, err := exec.Command(tool, args...).CombinedOutput() // #nosec G204 -- a fixed system tool with arguments this file builds
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", tool, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
