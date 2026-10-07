package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// targets are the release platforms of .goreleaser.yaml. The binaries are
// built with cgo off on every one of them.
var targets = []struct{ goos, goarch string }{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

// binaries are the main packages a release ships.
var binaries = []string{"./cmd/strazad", "./cmd/straza", "./cmd/strazactl"}

// listFormat prints, for every package the binaries link from a non-main
// module, the module's path, version and folder, the package's folder and
// the files it embeds, relative to that folder, separated by tabs.
const listFormat = `{{with .Module}}{{if not .Main}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{if .Replace}}{{.Replace.Dir}}{{else}}{{.Dir}}{{end}}{{"\t"}}{{$.Dir}}{{range $.EmbedFiles}}{{"\t"}}{{.}}{{end}}{{end}}{{end}}`

// releaseNotices returns the notices file of a release: the sections of the
// committed console notices, the Go standard library and every Go module the
// binaries link on any release platform.
func releaseNotices(root, version string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(root, consoleOut)) //nolint:gosec // G304: a fixed path under the repository root
	if err != nil {
		return nil, fmt.Errorf("the console's notices file %s is missing: run make ui, which writes it, then run tools/notices again", consoleOut)
	}
	if !bytes.HasPrefix(raw, []byte("THIRD-PARTY NOTICES FOR "+consoleScope+"\n")) {
		return nil, fmt.Errorf("%s does not start like the console's notices file: run make ui, which writes it again", consoleOut)
	}
	secs := parseSections(raw)
	if len(secs) == 0 {
		return nil, fmt.Errorf("%s lists no component: run make ui, which writes it again", consoleOut)
	}
	std, err := stdlibComponent(root)
	if err != nil {
		return nil, err
	}
	mods, err := goComponents(root)
	if err != nil {
		return nil, err
	}
	secs = append(secs, std.section())
	secs = append(secs, sectionsOf(mods)...)
	return render("Straza "+version, secs), nil
}

// goComponents returns every module that go list reports for the binaries
// on any release target, once per path and version, with the texts at its
// root and the texts on the way from each linked package's folder, and from
// each file it embeds, up to that root.
func goComponents(root string) ([]component, error) {
	type module struct {
		path, version, dir string
		dirs               []string
	}
	seen := map[string]*module{}
	for _, t := range targets {
		env := []string{"GOOS=" + t.goos, "GOARCH=" + t.goarch, "CGO_ENABLED=0"}
		out, err := goCmd(root, env, append([]string{"list", "-deps", "-f", listFormat}, binaries...)...)
		if err != nil {
			return nil, fmt.Errorf("cannot list the Go modules the binaries link for %s/%s: %w", t.goos, t.goarch, err)
		}
		for _, line := range strings.Split(out, "\n") {
			if line == "" {
				continue
			}
			f := strings.Split(line, "\t")
			if len(f) < 4 {
				return nil, fmt.Errorf("go list printed %q, which is not path, version, module folder and package folder: check the go command on PATH", line)
			}
			m, ok := seen[f[0]+"@"+f[1]]
			if !ok {
				m = &module{path: f[0], version: f[1], dir: f[2]}
				seen[f[0]+"@"+f[1]] = m
			}
			m.dirs = append(m.dirs, f[3])
			for _, embed := range f[4:] {
				m.dirs = append(m.dirs, filepath.Dir(filepath.Join(f[3], embed)))
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	comps := make([]component, 0, len(keys))
	for _, k := range keys {
		m := seen[k]
		if m.dir == "" {
			return nil, fmt.Errorf("the Go module %s %s is not in the module cache: run go mod download, then run tools/notices again", m.path, m.version)
		}
		if _, err := os.Stat(m.dir); err != nil {
			return nil, fmt.Errorf("the Go module %s %s is not in the module cache: run go mod download, then run tools/notices again", m.path, m.version)
		}
		texts, err := licenseTexts(m.dir)
		if err != nil {
			return nil, fmt.Errorf("cannot read the license files of the Go module %s %s: %w", m.path, m.version, err)
		}
		if len(texts) == 0 {
			ex, ok := exceptions[m.path+"@"+m.version]
			if !ok {
				return nil, fmt.Errorf("the Go module %s %s ships no license file, and tools/notices has no exception for it: read the license the module declares, then add a row for this path and version to the exception table in tools/notices/exceptions.go, or replace the module", m.path, m.version)
			}
			texts = []text{{name: "exception table", body: ex}}
		}
		nested, err := nestedTexts(m.dir, m.dirs)
		if err != nil {
			return nil, fmt.Errorf("cannot read the license files in the folders of the Go module %s %s: %w", m.path, m.version, err)
		}
		comps = append(comps, component{kind: "go", name: m.path, version: m.version, license: seeTexts, origin: m.path, texts: append(texts, nested...)})
	}
	return comps, nil
}

// stdlibComponent returns the Go standard library and runtime that every
// binary contains, with the LICENSE and PATENTS files of the Go installation
// that builds the release.
func stdlibComponent(root string) (component, error) {
	out, err := goCmd(root, nil, "env", "GOROOT", "GOVERSION")
	if err != nil {
		return component{}, fmt.Errorf("cannot ask the go command for its installation: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return component{}, fmt.Errorf("go env GOROOT GOVERSION printed %q, not two lines: check the go command on PATH", out)
	}
	version := strings.TrimPrefix(strings.Fields(lines[1])[0], "go")
	c := component{kind: "go-stdlib", name: "std", version: version, license: seeTexts,
		origin: "Go " + version + " standard library and runtime"}
	for _, name := range []string{"LICENSE", "PATENTS"} {
		raw, err := os.ReadFile(filepath.Join(lines[0], name)) //nolint:gosec // G304: a file of the Go installation
		if err != nil {
			return component{}, fmt.Errorf("the Go installation has no readable %s file: install Go again from https://go.dev/dl: %w", name, err)
		}
		c.texts = append(c.texts, text{name: name, body: normalize(raw)})
	}
	return c, nil
}

// goCmd runs the go command in dir with env added to the environment and
// returns its output, or an error that carries what go printed.
func goCmd(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("go", args...) //nolint:gosec // G204: the arguments are the fixed go verbs this program runs
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}
