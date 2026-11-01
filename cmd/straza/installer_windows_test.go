//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// TestInstallerIsHidden pins that the installer group and its three steps are
// registered on Windows and that none of them is an available command, the
// test both help and the CLI reference writer apply.
func TestInstallerIsHidden(t *testing.T) {
	root := rootCmd()
	for _, path := range [][]string{{"installer"}, {"installer", "lock"}, {"installer", "purge"}, {"installer", "recovery"}} {
		c, _, err := root.Find(path)
		if err != nil || c.CommandPath() != "straza "+strings.Join(path, " ") {
			t.Fatalf("%v is not registered: %v", path, err)
		}
		if c.IsAvailableCommand() {
			t.Errorf("%s shows in help and in the CLI reference", c.CommandPath())
		}
	}
}

// TestInstallerPurge pins that purge deletes the state folder it is given
// with everything inside, takes a missing folder as already purged, and
// refuses operands and a path that is not a folder, deleting nothing.
func TestInstallerPurge(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(t *testing.T, dir string)
		args     []string
		wantErr  string
		wantGone bool
	}{
		{name: "a state folder with keys inside is deleted", prepare: func(t *testing.T, dir string) {
			writeTestFile(t, filepath.Join(dir, "u", "S-1-5-21-1", "key"))
		}, wantGone: true},
		{name: "a missing state folder is already purged", prepare: func(*testing.T, string) {}, wantGone: true},
		{name: "a file at the state folder's path is refused", prepare: func(t *testing.T, dir string) {
			writeTestFile(t, dir)
		}, wantErr: "because it is a link or not a folder"},
		{name: "an operand is refused", prepare: func(t *testing.T, dir string) {
			writeTestFile(t, filepath.Join(dir, "key"))
		}, args: []string{`C:\Windows`}, wantErr: "`straza installer purge` takes no operands"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "straza-service")
			tt.prepare(t, dir)
			err := installerPurge(dir, tt.args)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("purge = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("purge = %v, want the refusal %q", err, tt.wantErr)
			}
			if _, serr := os.Lstat(dir); os.IsNotExist(serr) != tt.wantGone {
				t.Errorf("state folder gone = %v, want %v", os.IsNotExist(serr), tt.wantGone)
			}
		})
	}
}

// TestInstallerLockRefusesOperands pins that lock takes no operands and
// locks nothing when it is given one.
func TestInstallerLockRefusesOperands(t *testing.T) {
	root := filepath.Join(t.TempDir(), "straza")
	t.Setenv("STRAZA_SYSTEM", root)
	err := installerLock([]string{"now"})
	if err == nil || !strings.Contains(err.Error(), "`straza installer lock` takes no operands") {
		t.Fatalf("lock with an operand = %v, want the operands refusal", err)
	}
	if _, serr := os.Lstat(root); !os.IsNotExist(serr) {
		t.Errorf("lock with an operand created %s", root)
	}
}

// TestInstallerRecovery pins that recovery sets restart after 5, 5 and 60
// seconds with a one-day reset on a real service, the values the 3A install
// script set, and that it refuses operands and a missing service. It creates
// and deletes a scratch service, so it needs an elevated run.
func TestInstallerRecovery(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("this test creates a service, so it needs an elevated run")
	}
	name := fmt.Sprintf("straza-recovery-test-%d", os.Getpid())
	m, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.CreateService(name, `C:\Windows\System32\cmd.exe`, mgr.Config{StartType: mgr.StartManual, DisplayName: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Delete(); _ = s.Close() })

	if err := installerRecovery(name, []string{"now"}); err == nil || !strings.Contains(err.Error(), "`straza installer recovery` takes no operands") {
		t.Fatalf("recovery with an operand = %v, want the operands refusal", err)
	}
	if got, _ := s.RecoveryActions(); len(got) != 0 {
		t.Fatalf("recovery with an operand set %v", got)
	}
	if err := installerRecovery(name, nil); err != nil {
		t.Fatalf("recovery = %v, want nil", err)
	}
	got, err := s.RecoveryActions()
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(recoveryActions) {
		t.Errorf("recovery actions = %v, want %v", got, recoveryActions)
	}
	if reset, err := s.ResetPeriod(); err != nil || reset != 86400 {
		t.Errorf("reset period = %d, %v, want 86400 seconds", reset, err)
	}
	if err := installerRecovery(name+"-missing", nil); err == nil || !strings.Contains(err.Error(), "so Windows undid the install") {
		t.Errorf("recovery of a missing service = %v, want the refusal sentence", err)
	}
}

// writeTestFile writes an empty file at path, creating its folders.
func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
