package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The skill bundle is served from the docs domain under .well-known: the
// Agent Skills index that the Skills CLI reads, the skill files beside it, and
// a Claude Code marketplace file whose plugin source is the plugins directory
// of the public repository, fetched by sparse clone.
const (
	skillsSource     = "plugins/skills"
	pluginManifest   = "plugins/.claude-plugin/plugin.json"
	publicRepoGitURL = "https://github.com/strazahq/straza.git"
	publicPluginPath = "plugins"
	ownerName        = "Straza"
)

type skillEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
}

type skillsIndex struct {
	Skills []skillEntry `json:"skills"`
}

type marketplaceSource struct {
	Source string `json:"source"`
	URL    string `json:"url"`
	Path   string `json:"path"`
}

type marketplacePlugin struct {
	Name        string            `json:"name"`
	Source      marketplaceSource `json:"source"`
	Description string            `json:"description"`
}

type marketplace struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Owner       map[string]string   `json:"owner"`
	Plugins     []marketplacePlugin `json:"plugins"`
}

// writeSkillsTree rebuilds outDir/skills and outDir/claude-code from the
// skill sources: every skill directory copied whole, an index.json naming each
// skill with its description and files, and the marketplace file. The output
// is deterministic, so docs-drift can compare a committed tree with a fresh run.
func writeSkillsTree(root, outDir string) error {
	skillsOut := filepath.Join(outDir, "skills")
	claudeOut := filepath.Join(outDir, "claude-code")
	for _, d := range []string{skillsOut, claudeOut} {
		if err := os.RemoveAll(d); err != nil { //nolint:gosec // G703: d is one of two fixed subdirectories of the operator-named output
			return err
		}
		if err := os.MkdirAll(d, 0o755); err != nil { //nolint:gosec // G301: served docs directories are world-readable by design
			return err
		}
	}

	dirs, err := os.ReadDir(filepath.Join(root, skillsSource))
	if err != nil {
		return fmt.Errorf("reading %s: %w", skillsSource, err)
	}
	index := skillsIndex{Skills: []skillEntry{}}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		src := filepath.Join(root, skillsSource, d.Name())
		entry, err := copySkill(src, filepath.Join(skillsOut, d.Name()))
		if err != nil {
			return err
		}
		index.Skills = append(index.Skills, entry)
	}
	sort.Slice(index.Skills, func(i, j int) bool { return index.Skills[i].Name < index.Skills[j].Name })
	if err := writeJSON(filepath.Join(skillsOut, "index.json"), index); err != nil {
		return err
	}

	name, description, err := readPluginManifest(filepath.Join(root, pluginManifest))
	if err != nil {
		return err
	}
	mp := marketplace{
		Name:        name,
		Description: description,
		Owner:       map[string]string{"name": ownerName},
		Plugins: []marketplacePlugin{{
			Name:        name,
			Source:      marketplaceSource{Source: "git-subdir", URL: publicRepoGitURL, Path: publicPluginPath},
			Description: description,
		}},
	}
	return writeJSON(filepath.Join(claudeOut, "marketplace.json"), mp)
}

// copySkill copies one skill directory and returns its index entry, with the
// name and description read from the SKILL.md front matter.
func copySkill(src, dst string) (skillEntry, error) {
	var entry skillEntry
	var files []string
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p) //nolint:gosec // G304: copying the skill sources is the tool's job
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil { //nolint:gosec // G301: served docs directories are world-readable by design
			return err
		}
		if err := os.WriteFile(out, data, 0o644); err != nil { //nolint:gosec // G306: served docs files are world-readable by design
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		if rel == "SKILL.md" {
			entry.Name, entry.Description = frontMatterNameDescription(string(data))
		}
		return nil
	})
	if err != nil {
		return entry, fmt.Errorf("copying %s: %w", src, err)
	}
	if entry.Name == "" {
		return entry, fmt.Errorf("%s: SKILL.md carries no name in its front matter", src)
	}
	sort.Strings(files)
	entry.Files = files
	return entry, nil
}

// frontMatterNameDescription reads the name and description lines of a
// SKILL.md front matter block.
func frontMatterNameDescription(doc string) (name, description string) {
	lines := strings.Split(doc, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "---" {
			break
		}
		if v, ok := strings.CutPrefix(l, "name:"); ok {
			name = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(l, "description:"); ok {
			description = strings.TrimSpace(v)
		}
	}
	return name, description
}

func readPluginManifest(path string) (name, description string, err error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: reading the plugin manifest is the tool's job
	if err != nil {
		return "", "", err
	}
	var m struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", "", fmt.Errorf("%s: %w", path, err)
	}
	if m.Name == "" || m.Description == "" {
		return "", "", fmt.Errorf("%s: name and description are required", path)
	}
	return m.Name, m.Description, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644) //nolint:gosec // G306: served docs files are world-readable by design
}
