// Package distbudget holds the size contract for the embedded app tree
// (internal/server/console/dist) and the single implementation that enforces
// it. Both gates, build time (tools/uibuild) and test time
// (internal/server/console), call [Check], so the two cannot drift.
//
// The contract is a gzipped budget per entry page: the bytes a browser
// downloads for the page's scripts and styles, compressed the way a reverse
// proxy serves them. Vite hashes the asset names, so an entry is found
// through its HTML file rather than a fixed file list. The package carries
// no go:embed directive so tools/uibuild builds when dist is missing.
package distbudget

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

// Budgets is the gzipped byte budget per entry page, keyed by the HTML file
// at the dist root. index.html is the console entry served at /console/ and
// self-service.html the self-service page at /self-service/. The kit chunk
// both pages share, React, the Radix primitives, sonner and the icons, is
// 130 KB gzipped on its own and one file the browser caches once for both
// pages, so the self-service budget is that chunk plus the page's own
// shell, and its tabs load on first use outside the entry.
var Budgets = map[string]int{"index.html": 226 << 10, "self-service.html": 180 << 10}

// Base is the URL prefix the app is served under, which Vite writes in front
// of every asset reference.
const Base = "/console/"

// Entry is the measured size of one entry page.
type Entry struct {
	Page    string   // the HTML file at the dist root
	Assets  []string // the scripts and stylesheets it loads, dist-relative
	Gzipped int      // their gzipped bytes summed
	Budget  int      // the budget from [Budgets]
}

var assetRef = regexp.MustCompile(`(?i)<(?:script[^>]+src|link[^>]+href)="([^"]+)"`)

// Check measures every entry page in fsys against [Budgets]. It returns the
// entries in page order and an error when a page is over budget, loads an
// asset that is not in the tree, or has no budget at all.
func Check(fsys fs.FS) ([]Entry, error) {
	pages, err := fs.Glob(fsys, "*.html")
	if err != nil {
		return nil, err
	}
	sort.Strings(pages)
	var entries []Entry
	var problems []string
	for _, page := range pages {
		budget, known := Budgets[page]
		if !known {
			problems = append(problems, fmt.Sprintf("%s has no budget: add it to distbudget.Budgets with a measured number", page))
			continue
		}
		html, err := fs.ReadFile(fsys, page)
		if err != nil {
			return nil, err
		}
		e := Entry{Page: page, Budget: budget}
		for _, m := range assetRef.FindAllStringSubmatch(string(html), -1) {
			ref := m[1]
			if !strings.HasPrefix(ref, Base) {
				continue
			}
			name := strings.TrimPrefix(ref, Base)
			body, err := fs.ReadFile(fsys, name)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s loads %s, which is not in dist: rebuild with make ui", page, name))
				continue
			}
			e.Assets = append(e.Assets, name)
			e.Gzipped += gzipped(body)
		}
		if e.Gzipped > budget {
			problems = append(problems, fmt.Sprintf("%s is %d B gzipped, over its budget of %d B by %d B: trim the page, or raise its budget and say why in the commit",
				page, e.Gzipped, budget, e.Gzipped-budget))
		}
		entries = append(entries, e)
	}
	if len(pages) == 0 {
		problems = append(problems, "dist holds no HTML page: run make ui")
	}
	if len(problems) > 0 {
		return entries, errors.New("  " + strings.Join(problems, "\n  "))
	}
	return entries, nil
}

// gzipped returns the byte size of body after gzip at the default level, the
// level a reverse proxy uses for the transfer the budget stands for. The
// count depends on the Go toolchain, because Go 1.27 changed the deflate
// encoder and counts the same body about 2.7 percent larger than Go 1.26, so
// a budget is set against the larger count.
func gzipped(body []byte) int {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(body)
	_ = w.Close()
	return buf.Len()
}
