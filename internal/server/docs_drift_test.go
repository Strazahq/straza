package server

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestArchitectureDocVersionClaims is the doc-drift tripwire: it pins the
// greppable VERSION claims of the architecture reference document to the
// tree. Prose stays free, but a version bump that forgets the reference doc
// fails the build in the same commit. Same class as
// TestOpenAPIMatchesInfraRoutes: claims vs the single source of truth, both
// directions loud.
func TestArchitectureDocVersionClaims(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}
	firstMatch := func(rel, pattern string) string {
		t.Helper()
		m := regexp.MustCompile(pattern).FindStringSubmatch(read(rel))
		if m == nil {
			t.Fatalf("%s: no match for %q (tripwire anchor moved?)", rel, pattern)
		}
		return m[1]
	}

	openapiVersion := firstMatch("pkg/api/openapi.yaml", `(?m)^  version: (\S+)`)
	chartVersion := firstMatch("deploy/helm/straza/Chart.yaml", `(?m)^version: (\S+)`)
	eventsRev := firstMatch("spec/events/SPEC.md", `\(revision (\d+)\)`)
	scimRev := firstMatch("spec/scim-profile/SPEC.md", `\(revision (\d+)\)`)
	policysetRev := firstMatch("spec/policyset/SPEC.md", `\(revision (\d+)\)`)

	migHead := func(driver string) string {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(root, "internal", "store", "migrations", driver))
		if err != nil {
			t.Fatalf("read %s migrations: %v", driver, err)
		}
		var nums []string
		for _, e := range entries {
			if n := regexp.MustCompile(`^(\d{6})_`).FindStringSubmatch(e.Name()); n != nil {
				nums = append(nums, n[1])
			}
		}
		if len(nums) == 0 {
			t.Fatalf("no migrations found for %s", driver)
		}
		sort.Strings(nums)
		return nums[len(nums)-1]
	}
	pgHead, liteHead := migHead("postgres"), migHead("sqlite")
	if pgHead != liteHead {
		t.Fatalf("migration heads diverge: postgres %s vs sqlite %s (the doc claims 'both drivers')", pgHead, liteHead)
	}

	// docs/ARCHITECTURE.md is a maintainer document that the public release
	// tree does not ship. Makefile.internal never ships either, so its absence
	// marks the public tree, which skips the doc claims. The private tree
	// still fails when the document goes missing.
	if _, err := os.Stat(filepath.Join(root, "Makefile.internal")); errors.Is(err, fs.ErrNotExist) {
		t.Skip("docs/ARCHITECTURE.md is a maintainer document that the public tree does not ship, so its version claims are checked only in the private tree")
	}
	arch := read(filepath.Join("docs", "ARCHITECTURE.md"))
	claims := []struct {
		name    string
		pattern string // capture group 1 = the claimed value
		want    string
	}{
		{"openapi version", "`pkg/api/openapi\\.yaml` \\*\\*([0-9.]+)\\*\\*", openapiVersion},
		{"events revision", `spec/events v1beta1 \*\*rev (\d+)\*\*`, eventsRev},
		{"scim-profile revision", `spec/scim-profile v1beta1 \*\*rev (\d+)\*\*`, scimRev},
		{"policyset revision", `spec/policyset v1beta1 \*\*rev (\d+)\*\*`, policysetRev},
		{"chart version", "deploy/helm/straza` v([0-9.]+)", chartVersion},
		{"migration head", `head (\d{6})(?: on)? both drivers`, pgHead},
	}
	for _, c := range claims {
		ms := regexp.MustCompile(c.pattern).FindAllStringSubmatch(arch, -1)
		if len(ms) == 0 {
			t.Errorf("ARCHITECTURE.md no longer states the %s (pattern %q): the tripwire needs the claim present, not deleted", c.name, c.pattern)
			continue
		}
		for _, m := range ms {
			if m[1] != c.want {
				t.Errorf("ARCHITECTURE.md claims %s %q, tree says %q: update the doc in this commit (claim text: %q)",
					c.name, m[1], c.want, strings.TrimSpace(m[0]))
			}
		}
	}
}
