package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/strazahq/straza/internal/ctl"
	"github.com/strazahq/straza/internal/manager"
	policyengine "github.com/strazahq/straza/internal/policy"
)

var (
	docsLink   = regexp.MustCompile(`https://docs\.straza\.ai(/[^\s)\]>"'` + "`" + `]*)?`)
	semver     = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	productVer = regexp.MustCompile(`v\d+\.\d+\.\d+`)
)

// manifestFiles carry a version field that must agree across the three
// harness manifests.
var manifestFiles = []string{
	".claude-plugin/plugin.json",
	"gemini-extension.json",
	".codex-plugin/plugin.json",
}

// checkLinks maps every docs.straza.ai link in the file to a page under
// website/content or a served file under website/static. The site root and
// llms.txt need no page.
func checkLinks(root, rel string, data []byte) []finding {
	var out []finding
	for n, line := range strings.Split(string(data), "\n") {
		for _, m := range docsLink.FindAllStringSubmatch(line, -1) {
			p := m[1]
			if k := strings.IndexByte(p, '#'); k >= 0 {
				p = p[:k]
			}
			p = strings.Trim(strings.TrimRight(p, ".,;:"), "/")
			if p == "" || p == "llms.txt" {
				continue
			}
			page := filepath.Join(root, docsDir, p+".md")
			index := filepath.Join(root, docsDir, p, "_index.md")
			asset := filepath.Join(root, staticDir, p)
			if !exists(page) && !exists(index) && !exists(asset) {
				out = append(out, finding{rel, n + 1, fmt.Sprintf("link https://docs.straza.ai/%s/ has no page under %s", p, docsDir)})
			}
		}
	}
	return out
}

// checkExamples runs every document under a skill's references/examples
// through the same validators strazactl uses: app manifests by name prefix,
// PolicySets otherwise.
func checkExamples(root string, files []string) []finding {
	var out []finding
	for _, rel := range files {
		dir, name := filepath.Split(rel)
		if !strings.HasSuffix(filepath.Clean(dir), filepath.Join("references", "examples")) || filepath.Ext(name) != ".yaml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // G304: reading tracked example files is the tool's job
		if err != nil {
			out = append(out, finding{rel, 0, err.Error()})
			continue
		}
		if strings.HasPrefix(name, "app-") {
			_, err = manager.Parse(raw)
		} else {
			_, err = policyengine.ParseAll(raw)
		}
		if err != nil {
			out = append(out, finding{rel, 0, "the validator rejects this example: " + err.Error()})
		}
	}
	return out
}

// parsedOutputs names CLI output that scripts under plugins/ read with grep.
// The strings come from the package that prints them, so a reworded line
// fails here at commit instead of silently in a user's replay.
var parsedOutputs = []struct {
	command string
	phrases []string
}{
	{"strazactl policy simulate", []string{ctl.SimVerdictAgree, ctl.SimVerdictDiffer}},
}

// checkParsedOutput refuses a script that runs a command in parsedOutputs
// without quoting every phrase that command prints.
func checkParsedOutput(rel string, data []byte) []finding {
	var out []finding
	text := string(data)
	for _, p := range parsedOutputs {
		if !strings.Contains(text, p.command) {
			continue
		}
		for _, phrase := range p.phrases {
			if !strings.Contains(text, phrase) {
				out = append(out, finding{rel, 0, fmt.Sprintf("runs %s but does not quote its output line %q; match the text the CLI prints now", p.command, phrase)})
			}
		}
	}
	return out
}

// checkCompatibility requires the SKILL.md front matter to carry a
// compatibility line that names the product version it was verified against.
func checkCompatibility(rel string, data []byte) []finding {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return []finding{{rel, 1, "SKILL.md must open with a front matter block"}}
	}
	for n := 1; n < len(lines); n++ {
		if strings.TrimSpace(lines[n]) == "---" {
			break
		}
		if strings.HasPrefix(lines[n], "compatibility:") {
			if !productVer.MatchString(lines[n]) {
				return []finding{{rel, n + 1, "the compatibility line names no product version such as v1.0.0"}}
			}
			return nil
		}
	}
	return []finding{{rel, 1, "the front matter has no compatibility line naming the product version the skill was verified against"}}
}

// checkVersions requires the three manifests to agree on one semantic version.
func checkVersions(root string) []finding {
	var out []finding
	first := ""
	for _, m := range manifestFiles {
		rel := filepath.Join(pluginsDir, m)
		v, err := readVersion(filepath.Join(root, rel))
		if err != nil {
			out = append(out, finding{rel, 0, err.Error()})
			continue
		}
		if !semver.MatchString(v) {
			out = append(out, finding{rel, 0, fmt.Sprintf("version %q is not MAJOR.MINOR.PATCH", v)})
			continue
		}
		if first == "" {
			first = v
		} else if v != first {
			out = append(out, finding{rel, 0, fmt.Sprintf("version %s disagrees with %s in %s", v, first, filepath.Join(pluginsDir, manifestFiles[0]))})
		}
	}
	return out
}

func readVersion(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: reading the plugin manifests is the tool's job
	if err != nil {
		return "", err
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if m.Version == "" {
		return "", fmt.Errorf("%s carries no version field", path)
	}
	return m.Version, nil
}

func exists(p string) bool {
	_, err := os.Stat(p) //nolint:gosec // G703: the path is built from the repository root and a docs link path
	return err == nil
}
