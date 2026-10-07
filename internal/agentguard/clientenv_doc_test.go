package agentguard

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestClientEnvDocumented is the client half of the server's knob truth
// matrix (internal/config/knobs_test.go): every STRAZA_* environment
// variable the client binary reads has to be listed on the client
// environment reference page, so the page stays the whole truth and no
// hidden env can grow back (no silent defaults, no hidden env).
//
// The scan is deliberately broad: every "STRAZA_..." string literal in the
// non-test Go sources of cmd/straza and internal/agentguard counts, whether
// it is read through os.Getenv, os.LookupEnv or an environ map, so a new
// read shape cannot slip past it. The docs side matches each name as
// backtick-wrapped code, which is the only form the page lists them in.
func TestClientEnvDocumented(t *testing.T) {
	roots := []string{
		filepath.Join("..", "..", "cmd", "straza"),
		".",
	}
	lit := regexp.MustCompile(`"(STRAZA_[A-Z0-9_]+)"`)
	names := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path) // #nosec G304 -- test walks its own package tree
			if err != nil {
				return err
			}
			for _, m := range lit.FindAllStringSubmatch(string(src), -1) {
				if _, seen := names[m[1]]; !seen {
					names[m[1]] = path
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(names) == 0 {
		t.Fatal("no STRAZA_* literals found; the scan roots are wrong")
	}

	pagePath := filepath.Join("..", "..", "website", "content", "reference", "client-environment.md")
	page, err := os.ReadFile(pagePath) // #nosec G304 -- fixed docs path
	if err != nil {
		t.Fatalf("read %s: %v", pagePath, err)
	}
	if !strings.Contains(string(page), "`STRAZA_") {
		t.Fatalf("%s lists no STRAZA_ variable in backticks, so it is not the client environment reference; restore the list", pagePath)
	}

	var missing []string
	for name, where := range names {
		if !strings.Contains(string(page), "`"+name+"`") {
			missing = append(missing, name+" (first read in "+where+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("client env read by the binary but not listed on website/content/reference/client-environment.md:\n  %s\nAdd one bullet per name, in backticks, to that page so the reference stays the whole truth.",
			strings.Join(missing, "\n  "))
	}
}
