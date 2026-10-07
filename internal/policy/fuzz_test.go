package policy

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParse hammers the PolicySet parser with mutated inputs seeded from the
// spec corpus. Operators paste YAML into `strazactl policy apply`, so
// the parser must reject garbage with an error, never a panic, and anything
// that parses must also compile into an engine without panicking.
func FuzzParse(f *testing.F) {
	dir := filepath.Join("..", "..", "spec", "policyset", "examples")
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
		doc, err := Parse(raw)
		if err != nil {
			return
		}
		_, _ = NewEngine([]Document{doc}, EffectAllow)
	})
}
