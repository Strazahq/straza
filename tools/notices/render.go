package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// component is one piece of third-party software with the texts its license
// asks a redistributor to carry.
type component struct {
	kind    string // vendored, npm, go-stdlib or go
	name    string
	version string
	license string
	origin  string
	texts   []text
}

// text is one license, notice or copyright file of a component.
type text struct {
	name string
	body string
}

// section is one component as it appears in the notices file, kept whole so
// the release mode can copy the console's sections unchanged.
type section struct {
	kind    string
	name    string
	version string
	body    string
}

// kindOrder is the order of the kinds in the notices file.
var kindOrder = map[string]int{"vendored": 0, "npm": 1, "go-stdlib": 2, "go": 3}

const rule = "================================================================================"

// seeTexts is the license field of a component whose license is named only
// by its texts.
const seeTexts = "see the texts below"

func (c component) section() section {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s %s %s\nLicense: %s\nOrigin: %s\n%s\n", rule, c.kind, c.name, c.version, c.license, c.origin, rule)
	for _, t := range c.texts {
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", t.name, t.body)
	}
	return section{kind: c.kind, name: c.name, version: c.version, body: b.String()}
}

// render writes the notices file for scope: the header, then every section
// ordered by kind, name and version in byte order.
func render(scope string, secs []section) []byte {
	sort.Slice(secs, func(i, j int) bool {
		a, b := secs[i], secs[j]
		if a.kind != b.kind {
			return kindOrder[a.kind] < kindOrder[b.kind]
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.version < b.version
	})
	var b strings.Builder
	fmt.Fprintf(&b, "THIRD-PARTY NOTICES FOR %s\n\n", scope)
	fmt.Fprintf(&b, "This file lists the third-party software that %s includes, with the copyright\n", scope)
	b.WriteString("notices and license texts that its licenses require. tools/notices generates it. Do\n")
	b.WriteString("not edit it by hand.\n\n")
	fmt.Fprintf(&b, "Components: %d\n", len(secs))
	for _, s := range secs {
		b.WriteString("\n")
		b.WriteString(s.body)
	}
	return []byte(b.String())
}

var sectionHead = regexp.MustCompile(`(?m)^` + rule + `\n(vendored|npm|go-stdlib|go) (.+) (\S+)\nLicense: .*\nOrigin: .*\n` + rule + `\n`)

// parseSections splits a notices file into its sections. The blank line
// that render puts before each section is not part of it.
func parseSections(data []byte) []section {
	heads := sectionHead.FindAllSubmatchIndex(data, -1)
	secs := make([]section, 0, len(heads))
	for i, h := range heads {
		end := len(data)
		if i+1 < len(heads) {
			end = heads[i+1][0] - 1
		}
		secs = append(secs, section{
			kind:    string(data[h[2]:h[3]]),
			name:    string(data[h[4]:h[5]]),
			version: string(data[h[6]:h[7]]),
			body:    string(data[h[0]:end]),
		})
	}
	return secs
}

var (
	licenseName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|copyright)([._-].*)?$`)
	sourceName  = regexp.MustCompile(`(?i)\.(js|mjs|cjs|jsx|ts|mts|cts|tsx|go|json|html|css|map)$`)
)

// licenseTexts returns the license, notice and copyright files at the top of
// dir, not in its subfolders, in byte order of their names. A source file
// such as license.go or an icon module such as copyright.mjs is not a
// license text, and an empty file is skipped.
func licenseTexts(dir string) ([]text, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []text
	for _, e := range entries {
		name := e.Name()
		if !licenseName.MatchString(name) || sourceName.MatchString(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(path) //nolint:gosec // G304: a license file of an installed dependency
		if err != nil {
			return nil, err
		}
		if body := normalize(raw); body != "" {
			out = append(out, text{name: name, body: body})
		}
	}
	return out, nil
}

// nestedTexts returns the license files of every folder on the way from each
// of dirs up to root, root excluded, named by their path relative to root,
// once each and in byte order of those names. A package that vendors code in
// a subfolder, such as an SDK with its own NOTICE, carries its texts there,
// and root's own files are the caller's to read.
func nestedTexts(root string, dirs []string) ([]text, error) {
	root = filepath.Clean(root)
	seen := map[string]bool{}
	var out []text
	for _, d := range dirs {
		for d = filepath.Clean(d); d != root && strings.HasPrefix(d, root+string(filepath.Separator)) && !seen[d]; d = filepath.Dir(d) {
			seen[d] = true
			texts, err := licenseTexts(d)
			if err != nil {
				return nil, err
			}
			rel, err := filepath.Rel(root, d)
			if err != nil {
				return nil, err
			}
			for _, t := range texts {
				out = append(out, text{name: filepath.ToSlash(filepath.Join(rel, t.name)), body: t.body})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// normalize makes a license text safe to concatenate: valid UTF-8 without a
// byte order mark, LF line ends, and no trailing blank lines.
func normalize(raw []byte) string {
	s := strings.ToValidUTF8(string(raw), "\uFFFD")
	s = strings.TrimPrefix(s, "\uFEFF")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, " \t\n")
}

// sectionsOf renders every component into its section.
func sectionsOf(comps []component) []section {
	secs := make([]section, 0, len(comps))
	for _, c := range comps {
		secs = append(secs, c.section())
	}
	return secs
}
