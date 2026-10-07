package harnesscfg_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/harnesscfg"
)

// The spec/harness-config example key: a throwaway pair regenerated from
// this fixed seed (documented in examples/README.md) so the fixtures are
// reproducible bytes, never secrets. External test package deliberately:
// agentguard imports harnesscfg, so an internal test importing agentguard
// would cycle.
const exampleKID = "spec-example-key"

func exampleKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x53}, ed25519.SeedSize))
}

func exampleLookup() harnesscfg.KeyLookup {
	pub := exampleKey().Public().(ed25519.PublicKey)
	return func(kid string) (ed25519.PublicKey, bool) {
		if kid == exampleKID {
			return pub, true
		}
		return nil, false
	}
}

var validExamples = []struct{ file, harness, goos string }{
	{"valid-claude-code-linux.json", "claude-code", "linux"},
	{"valid-codex-windows.json", "codex", "windows"},
	{"valid-gemini-darwin.json", "gemini", "darwin"},
}

// invalidExamples derive from the claude-code document, one verification
// failure each; want is the error substring the replay asserts.
var invalidExamples = []struct {
	file   string
	mutate func(*harnesscfg.Document)
	want   string
}{
	{"invalid-kind.json", func(d *harnesscfg.Document) { d.Kind = "policy-snapshot" }, "kind"},
	{"invalid-content-hash.json", func(d *harnesscfg.Document) {
		d.Artifacts[0].ContentHash = "sha256:" + strings.Repeat("0", 64)
	}, "content hash"},
	{"invalid-signature.json", func(d *harnesscfg.Document) {
		d.Artifacts[0].Sig = append([]byte{}, d.Artifacts[0].Sig...)
		d.Artifacts[0].Sig[0] ^= 0xff
	}, "signature"},
}

func exampleDir() string {
	return filepath.Join("..", "..", "spec", "harness-config", "examples")
}

func buildExample(t *testing.T, harness, goos string) harnesscfg.Document {
	t.Helper()
	// Pin the one env the default render reads, so fixtures are identical on
	// any build host (a relocated %ProgramData% would leak into the bytes).
	t.Setenv("ProgramData", "")
	arts, err := agentguard.RenderManagedArtifacts(harness, goos, "")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(arts))
	for name := range arts {
		names = append(names, name)
	}
	sort.Strings(names)
	doc := harnesscfg.Document{Format: harnesscfg.Format, Kind: harnesscfg.Kind, Harness: harness, Platform: goos}
	for _, name := range names {
		art, err := harnesscfg.SignArtifact(exampleKID, exampleKey(), harness, goos, name, arts[name])
		if err != nil {
			t.Fatal(err)
		}
		doc.Artifacts = append(doc.Artifacts, art)
	}
	return doc
}

func marshalExample(t *testing.T, doc harnesscfg.Document) []byte {
	t.Helper()
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// TestSpecExamplesUpToDate regenerates every spec/harness-config example
// from the renderer + the fixed example key and compares bytes: the fixtures
// are pinned to what the product actually renders (the adapter-drift
// pattern). Regenerate after an intentional render change with
// STRAZA_UPDATE_SPEC_EXAMPLES=1 and commit the diff with the CHANGELOG line
// explaining it.
func TestSpecExamplesUpToDate(t *testing.T) {
	update := os.Getenv("STRAZA_UPDATE_SPEC_EXAMPLES") == "1"
	write := func(file string, body []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(exampleDir(), file), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check := func(file string, want []byte) {
		t.Helper()
		if update {
			write(file, want)
			return
		}
		got, err := os.ReadFile(filepath.Join(exampleDir(), file))
		if err != nil {
			t.Fatalf("%s missing (regenerate: STRAZA_UPDATE_SPEC_EXAMPLES=1 go test ./internal/harnesscfg/): %v", file, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s drifted from the current render; regenerate deliberately and explain in CHANGELOG", file)
		}
	}

	for _, ex := range validExamples {
		check(ex.file, marshalExample(t, buildExample(t, ex.harness, ex.goos)))
	}
	for _, ex := range invalidExamples {
		doc := buildExample(t, "claude-code", "linux")
		ex.mutate(&doc)
		check(ex.file, marshalExample(t, doc))
	}
}

// TestSpecExamplesReplay verifies the committed fixtures the way a consumer
// would: every valid example passes Verify against the example key, every
// invalid one fails naming the planted defect.
func TestSpecExamplesReplay(t *testing.T) {
	lookup := exampleLookup()
	load := func(file string) harnesscfg.Document {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(exampleDir(), file))
		if err != nil {
			t.Fatal(err)
		}
		var doc harnesscfg.Document
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	for _, ex := range validExamples {
		if err := harnesscfg.Verify(load(ex.file), lookup); err != nil {
			t.Errorf("%s: %v", ex.file, err)
		}
	}
	for _, ex := range invalidExamples {
		err := harnesscfg.Verify(load(ex.file), lookup)
		if err == nil || !strings.Contains(err.Error(), ex.want) {
			t.Errorf("%s: err %v, want mention of %q", ex.file, err, ex.want)
		}
	}
}
