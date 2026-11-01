//go:build windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
)

func init() {
	devCommands = append(devCommands, installerCmd())
}

// installerCmd is the hidden `straza installer` group: the steps the Straza
// MSI runs as custom actions. The group and each step are hidden, which
// keeps them out of help and out of the CLI reference, whose writer skips
// every command that is not available.
func installerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "installer",
		Short:  "Run a step of the Straza MSI",
		Hidden: true,
	}
	cmd.AddCommand(
		installerStep("lock", "Lock the managed root to administrators, after the MSI placed straza.exe in it", installerLock),
		installerStep("purge", "Delete the state folder of straza-service, with this machine's keys and enrollments",
			func(args []string) error { return installerPurge(localapi.SystemStateDir, args) }),
		installerStep("recovery", "Set straza-service to restart after a failure, after the MSI installed the service",
			func(args []string) error { return installerRecovery(windowsServiceName, args) }),
	)
	return cmd
}

// installerStep is one step of `straza installer`. A refusal reaches stderr
// through main and the Application event log under the source
// straza-service, because msiexec shows only its own error number for a
// custom action that failed.
func installerStep(use, short string, step func(args []string) error) *cobra.Command {
	return &cobra.Command{
		Use:    use,
		Short:  short,
		Hidden: true,
		Args:   cobra.ArbitraryArgs, // the step refuses operands itself, so that refusal reaches the event log too
		RunE: func(_ *cobra.Command, args []string) error {
			err := step(args)
			if err != nil {
				logInstallerRefusal(err)
			}
			return err
		},
	}
}

// installerLock locks the managed root as `straza install --managed` does.
// The MSI runs it after it placed straza.exe under the root and before it
// starts the service, and a refusal makes Windows undo the install.
func installerLock(args []string) error {
	if len(args) > 0 {
		return operandsRefused("lock", args)
	}
	if err := agentguard.LockManagedRoot(); err != nil {
		return fmt.Errorf("The Straza MSI could not lock %s to administrators, so Windows undid the install. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"The check is the one `straza install --managed` runs, and it said: %w", agentguard.ManagedRoot(), err)
	}
	return nil
}

// installerPurge deletes dir, the state folder of straza-service, and
// nothing else. A missing folder is already purged. It refuses a dir that
// is a link or not a folder, so it never deletes what a link points to.
func installerPurge(dir string, args []string) error {
	if len(args) > 0 {
		return operandsRefused("purge", args)
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err == nil && (!fi.IsDir() || isReparsePoint(fi)):
		return fmt.Errorf("`straza installer purge` did not delete %s, because it is a link or not a folder, "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"and purge deletes only the state folder of straza-service. An administrator checks what is there and removes it by hand if nothing needs it.", dir)
	case err == nil:
		err = os.RemoveAll(dir)
	}
	if err != nil {
		return fmt.Errorf("`straza installer purge` could not delete %s, the state folder of straza-service with this machine's keys and enrollments (%w). "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Stop straza-service if it still runs, then run `straza installer purge` again from an administrator PowerShell.", dir, err)
	}
	return nil
}

// recoveryActions restart straza-service 5, 5 and 60 seconds after a
// failure, and recoveryReset is the seconds without a failure after which
// the count starts again. A service that stays down leaves every governed
// tool call refused until someone restarts it.
var recoveryActions = []mgr.RecoveryAction{
	{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
	{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
	{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
}

const recoveryReset = 24 * 60 * 60

// installerRecovery sets the restart-on-failure actions of the service
// named service. The MSI cannot set them from its MsiServiceConfigFailureActions
// table: a restart action needs a service handle with start rights, and
// Windows Installer opens the service without them, so that table failed
// every install with access denied. The MSI runs this step as LocalSystem
// after InstallServices and before StartServices, and a refusal makes
// Windows undo the install.
func installerRecovery(service string, args []string) error {
	if len(args) > 0 {
		return operandsRefused("recovery", args)
	}
	err := setRecovery(service)
	if err != nil {
		return fmt.Errorf("The Straza MSI could not set %s to restart after a failure (%w), so Windows undid the install, "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"because a stopped service leaves every governed tool call refused until someone restarts it. "+
			"Run the install again as LocalSystem or from an administrator PowerShell.", service, err)
	}
	return nil
}

// setRecovery opens service through the service control manager with all
// access, which LocalSystem and an administrator hold, and sets
// recoveryActions on it.
func setRecovery(service string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(service)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	return s.SetRecoveryActions(recoveryActions, recoveryReset)
}

// isReparsePoint reports whether fi, read by os.Lstat, is a Windows reparse
// point such as a symbolic link or a junction.
func isReparsePoint(fi fs.FileInfo) bool {
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return !ok || d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// operandsRefused is the refusal of a `straza installer` step given
// operands, which no step takes.
func operandsRefused(step string, args []string) error {
	return fmt.Errorf("`straza installer %s` takes no operands, and it was given %q, so it did nothing. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
		"Run `straza installer %s` with nothing after it.", step, args, step)
}

// logInstallerRefusal writes err's sentence to the Application event log
// under the source straza-service as an Error event. A log that cannot be
// opened or written is skipped, because the sentence also reaches stderr.
func logInstallerRefusal(err error) {
	elog, oerr := eventlog.Open(windowsServiceName)
	if oerr != nil {
		return
	}
	defer func() { _ = elog.Close() }()
	_ = elog.Error(eventID, err.Error())
}
