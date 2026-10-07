// Command pluginsgate keeps the agent skill bundle under plugins/ true to the
// product. It refuses a CLI command, verb or flag that no generated reference
// page carries, a docs link that maps to no page under website/content, an
// example document the embedded validators reject, a script that no longer
// quotes the CLI output it parses, a SKILL.md without a compatibility line
// naming a product version, and manifests whose versions disagree. A change
// to plugins/ also bumps that version, because Claude Code users receive an
// update only when the string changes.
//
// Findings print one per line as path:LINE: message and the exit code is 1
// when there is at least one. make check runs the same check.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	pluginsDir = "plugins"
	cliDir     = "website/content/reference/cli"
	docsDir    = "website/content"
	staticDir  = "website/static"
)

// finding is one gate violation anchored to a file and a line (0 when the
// whole file is meant).
type finding struct {
	path string
	line int
	msg  string
}

func (f finding) String() string {
	if f.line > 0 {
		return fmt.Sprintf("%s:%d: %s", f.path, f.line, f.msg)
	}
	return fmt.Sprintf("%s: %s", f.path, f.msg)
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	findings, err := run(*root, gitTracked)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pluginsgate: %v\n", err)
		os.Exit(1)
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) > 0 {
		fmt.Fprintf(os.Stderr, "pluginsgate: %d finding(s); the skill under plugins/ drifted from the product\n", len(findings))
		os.Exit(1)
	}
}

// run executes every check against root. list names the tracked files under
// plugins/ relative to root; main passes the git index and tests pass a walk.
func run(root string, list func(root string) ([]string, error)) ([]finding, error) {
	idx, err := loadCLIIndex(root)
	if err != nil {
		return nil, err
	}
	files, err := list(root)
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	var out []finding
	for _, rel := range files {
		ext := strings.ToLower(filepath.Ext(rel))
		if ext != ".md" && ext != ".sh" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // G304: reading tracked files under the repository root is the tool's job
		if err != nil {
			return nil, err
		}
		out = append(out, checkCommands(rel, data, ext == ".sh", idx)...)
		if ext == ".sh" {
			out = append(out, checkParsedOutput(rel, data)...)
		}
		out = append(out, checkLinks(root, rel, data)...)
		if filepath.Base(rel) == "SKILL.md" {
			out = append(out, checkCompatibility(rel, data)...)
		}
	}
	out = append(out, checkExamples(root, files)...)
	out = append(out, checkVersions(root)...)
	return out, nil
}

// gitTracked lists the git-tracked files under plugins/, relative to root,
// so an untracked draft never reaches the gate.
func gitTracked(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--", pluginsDir) //nolint:gosec // G204: fixed argv, only the operator-named root varies
	outb, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files under %s failed: %w", root, err)
	}
	var files []string
	for _, p := range strings.Split(string(outb), "\x00") {
		if p == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			files = append(files, filepath.Clean(p))
		}
	}
	return files, nil
}
