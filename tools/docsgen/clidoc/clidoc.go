// Package clidoc writes the public CLI reference pages for a Cobra command
// tree: one Markdown page per available command, in the front matter shape
// the docs site expects, with site-absolute links between the pages.
//
// Each binary carries a hidden gen-docs command built only under the
// docsgen build tag (cmd/*/gendocs.go), and that command calls Write on the
// binary's own root. The shipped binaries never link this package or Cobra's
// doc package.
package clidoc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
	"gopkg.in/yaml.v3"
)

// contentRoot is the Hugo content directory; the part of DIR after it is the
// site path of the generated section.
const contentRoot = "website/content/"

// Command returns the hidden gen-docs DIR command. It carries its own no-op
// PersistentPreRunE so a root that resolves a server or credentials before
// every command does not do so for a documentation run.
func Command() *cobra.Command {
	return &cobra.Command{
		Use:               "gen-docs DIR",
		Short:             "Write the Markdown reference pages for every command into DIR (removed and recreated)",
		Hidden:            true,
		Args:              cobra.ExactArgs(1),
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			return Write(cmd.Root(), args[0])
		},
	}
}

// Write removes and recreates dir, then writes one page per available
// command under root, the root as _index.md and every other command as
// Cobra's underscore-joined name. Hidden and deprecated commands are
// skipped. The output carries no dates and no absolute paths, and the pages
// come out in Cobra's sorted command order, so two runs are byte-identical.
func Write(root *cobra.Command, dir string) error {
	root.DisableAutoGenTag = true
	// Cobra adds its completion command to the root on Execute, which is
	// how gen-docs runs; its boilerplate is not part of the reference, so it
	// is hidden here before the walk and before the See also lists render.
	for _, c := range root.Commands() {
		if c.Name() == "completion" {
			c.Hidden = true
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: docs pages are world-readable by design
		return err
	}
	base := sitePath(dir)
	weight := rootWeightBase[root.Name()]
	var walk func(c *cobra.Command) error
	walk = func(c *cobra.Command) error {
		weight++
		page, err := Render(c, base, weight)
		if err != nil {
			return err
		}
		name := "_index.md"
		if c.HasParent() {
			name = strings.ReplaceAll(c.CommandPath(), " ", "_") + ".md"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(page), 0o644); err != nil { //nolint:gosec // G306: docs pages are world-readable by design
			return err
		}
		for _, child := range c.Commands() {
			if !child.IsAvailableCommand() || child.IsAdditionalHelpTopicCommand() {
				continue
			}
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root)
}

// rootWeightBase is the weight before a binary's root page, so that the
// three sections sort on the CLI reference index in the order its prose
// names them: strazad, strazactl, straza. Another root starts at weight 1.
var rootWeightBase = map[string]int{"strazad": 0, "strazactl": 1, "straza": 2}

// sitePath turns the output directory into the site-absolute path of the
// section, the part after website/content with a slash on both ends.
func sitePath(dir string) string {
	rel := filepath.ToSlash(filepath.Clean(dir))
	if i := strings.Index(rel, contentRoot); i >= 0 {
		rel = rel[i+len(contentRoot):]
	}
	return "/" + strings.Trim(rel, "/") + "/"
}

// frontMatter is the page contract for a generated reference page.
type frontMatter struct {
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Pagetype    string   `yaml:"pagetype"`
	Weight      int      `yaml:"weight"`
	Draft       bool     `yaml:"draft"`
	Sources     []string `yaml:"sources"`
	Keywords    string   `yaml:"keywords"`
}

// Render returns the page for one command: the front matter, a comment
// naming the generator, then Cobra's Markdown with the title heading and
// the Short dropped (the layout renders both from the front matter), the
// section headings raised one level so the body starts at ##, the help
// flag's line dropped, and "SEE ALSO" renamed. base is the site path of the
// section, used for the links between pages.
func Render(c *cobra.Command, base string, weight int) (string, error) {
	rootName := c.Root().Name()
	link := func(name string) string {
		stem := strings.TrimSuffix(name, ".md")
		if stem == rootName {
			return base
		}
		return base + stem + "/"
	}
	var body bytes.Buffer
	if err := doc.GenMarkdownCustom(c, &body, link); err != nil {
		return "", err
	}
	fm := frontMatter{
		Title:       c.CommandPath(),
		Description: c.Short,
		Pagetype:    "reference",
		Weight:      weight,
		Draft:       false,
		Sources:     []string{"cmd/" + rootName},
		Keywords:    strings.Join(strings.Fields(c.CommandPath()), ", "),
	}
	var head bytes.Buffer
	enc := yaml.NewEncoder(&head)
	enc.SetIndent(2)
	if err := enc.Encode(fm); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("---\n")
	out.Write(head.Bytes())
	out.WriteString("---\n")
	fmt.Fprintf(&out, "<!-- Generated by the gen-docs command of %s from the command tree in cmd/%s.\n     Do not edit this file; change the help strings and run make docs-gen. -->\n\n", rootName, rootName)
	out.WriteString(postProcess(body.String(), c.CommandPath(), c.Short, !c.HasParent()))
	return out.String(), nil
}

// sectionHeadings are the headings Cobra writes at level 3; the page raises
// them to level 2 because the command heading above them is dropped.
var sectionHeadings = map[string]string{
	"### Synopsis": "## Synopsis",
	"### Examples": "## Examples",
	"### Options":  "## Options",
	"### Options inherited from parent commands": "## Options inherited from parent commands",
	"### SEE ALSO": "## See also",
}

// helpFlagLine is the line Cobra writes for the help flag it adds to every
// command, which says nothing a reader of a reference page needs.
var helpFlagLine = regexp.MustCompile(`^\s*(-h, )?--help\s+help for \S+$`)

// postProcess reshapes Cobra's Markdown for the site layout. It drops the
// help flag's line, and an Options block that held only that line. A Long
// that opens with the Short loses that sentence, because the lede already
// prints it and the terminal help still needs it. On a root page the See
// also list is the binary's commands, so it is headed Commands.
func postProcess(md, commandPath, short string, root bool) string {
	md = strings.TrimPrefix(md, "## "+commandPath+"\n\n")
	md = strings.TrimPrefix(md, short+"\n\n")
	md = strings.Replace(md, "### Synopsis\n\n"+short+".\n\n", "### Synopsis\n\n", 1)
	lines := strings.Split(md, "\n")
	out := lines[:0]
	for _, line := range lines {
		if helpFlagLine.MatchString(line) {
			continue
		}
		if h, ok := sectionHeadings[line]; ok {
			if root && line == "### SEE ALSO" {
				h = "## Commands"
			}
			line = h
		}
		if strings.HasPrefix(line, "* [") {
			line = strings.Replace(line, "\t - ", ": ", 1)
		}
		out = append(out, line)
	}
	return strings.Replace(strings.Join(out, "\n"), "## Options\n\n```\n```\n\n", "", 1)
}
