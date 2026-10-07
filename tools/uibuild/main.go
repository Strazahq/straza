// Command uibuild compiles web/ui into internal/server/console/dist and
// enforces the app's size contract. It is the one implementation behind
// make ui, the commit gate and CI: it refuses to run without Node 22 and
// npm, installs the locked dependency tree with npm ci so a lockfile that
// disagrees with package.json fails here, runs the Vite build, writes the
// console's third-party notices with tools/notices, and measures every entry
// page against distbudget.Budgets.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/strazahq/straza/internal/server/console/distbudget"
)

const (
	srcDir    = "web/ui"
	distDir   = "internal/server/console/dist"
	nodeMajor = 22
	install   = "  Install Node 22 LTS from https://nodejs.org/en/download (npm ships with it), open a new shell, then run make ui again"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "uibuild:", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := os.Stat(filepath.Join(srcDir, "package.json")); err != nil {
		return fmt.Errorf("run from the repo root: %w", err)
	}
	if err := checkNode(); err != nil {
		return err
	}
	if err := npm("ci", "--no-audit", "--no-fund"); err != nil {
		return err
	}
	if err := npm("run", "build"); err != nil {
		return err
	}
	if err := notices(); err != nil {
		return err
	}
	entries, err := distbudget.Check(os.DirFS(distDir))
	for _, e := range entries {
		fmt.Printf("ui built: %s; %s %d B gzipped of %d B budget (%+d B free) over %d assets\n",
			distDir, e.Page, e.Gzipped, e.Budget, e.Budget-e.Gzipped, len(e.Assets))
	}
	if err != nil {
		return fmt.Errorf("ui asset contract:\n%w", err)
	}
	return nil
}

// checkNode refuses with the install line when node is missing, older than
// nodeMajor, or without npm, because the gate must fail loudly on a commit
// box that cannot rebuild the dist.
func checkNode() error {
	out, err := exec.Command("node", "--version").Output()
	if err != nil {
		return errors.New("node is not installed or not on PATH, and the app build needs Node 22 or newer.\n" + install)
	}
	v := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	major, _ := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	if major < nodeMajor {
		return fmt.Errorf("node %s is installed, and the app build needs Node %d or newer.\n%s", v, nodeMajor, install)
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return errors.New("npm is not on PATH, and the app build needs it.\n" + install)
	}
	return nil
}

// notices writes dist/THIRD_PARTY_NOTICES.txt after the Vite build, which
// empties the dist first. The file is text, so no entry page budget counts it.
func notices() error {
	cmd := exec.Command("go", "run", "./tools/notices", "-console")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go run ./tools/notices -console failed, so the console bundle has no third-party notices: fix what it printed above, then run make ui again: %w", err)
	}
	return nil
}

// npm runs one npm command inside web/ui with its output shown, so a failed
// install or build prints npm's own reason.
func npm(args ...string) error {
	cmd := exec.Command("npm", args...) //nolint:gosec // G204: the arguments are the literal npm verbs run passes
	cmd.Dir = srcDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm %s failed in %s: %w", strings.Join(args, " "), srcDir, err)
	}
	return nil
}
