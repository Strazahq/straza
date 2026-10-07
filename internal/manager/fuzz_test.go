package manager

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParseManifest hammers the app-manifest parser with mutated inputs
// seeded from the spec corpus. Manifests arrive from the GitOps apps/
// directory and the admin API, and malformed ones must error, never panic.
func FuzzParseManifest(f *testing.F) {
	dir := filepath.Join("..", "..", "spec", "app-manifest", "examples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		f.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Fuzz(func(_ *testing.T, raw []byte) {
		_, _ = Parse(raw)
	})
}
