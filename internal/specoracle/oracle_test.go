// Package specoracle makes the published JSON Schemas executable oracles. The
// spec/ schemas are the wire contract third parties implement against, so
// loading them keeps parser and schema from drifting apart silently. Each test
// walks a spec example corpus and pins BOTH verdicts to the corpus intent
// encoded in the filename (valid-* must pass parser AND schema, invalid-* must
// fail both), so drift on either side, or a mislabeled example, is a named
// failure.
//
// Two-layer contract (policyset SPEC.md §2.3): an example whose header comment
// marks it "INVALID (semantic)" pins a constraint JSON Schema cannot or
// deliberately does not express. The schema MUST admit it and the platform
// validator MUST reject it. Everything else is two-sided. Attestation is
// one-sided (its checkin request is decoded inside the server handler, with no
// pure exported parser), and hook-profile ships no example corpus at all.
package specoracle

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/events"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
)

func compileSchema(t *testing.T, rel string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("compile %s: %v", rel, err)
	}
	return sch
}

// toJSONValue normalizes a document (YAML or JSON bytes) into the decoded
// form the validator wants: YAML decodes to any, round-trips through
// encoding/json so numbers and keys carry JSON semantics, then
// jsonschema.UnmarshalJSON preserves number fidelity.
func toJSONValue(t *testing.T, raw []byte, isYAML bool) (any, bool) {
	t.Helper()
	b := raw
	if isYAML {
		var v any
		if err := yaml.Unmarshal(raw, &v); err != nil {
			return nil, false // not even well-formed YAML: schema side has no document to judge
		}
		jb, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		b = jb
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, false
	}
	return v, true
}

// runCorpus walks a spec examples dir and asserts filename intent
// (valid-*/invalid-*) against the schema and, when given, the parser.
func runCorpus(t *testing.T, schemaRel, examplesRel, ext string, parse func([]byte) error) {
	t.Helper()
	sch := compileSchema(t, schemaRel)
	dir := filepath.Join("..", "..", examplesRel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", examplesRel, err)
	}
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ext) {
			continue
		}
		wantValid := strings.HasPrefix(name, "valid-") || strings.HasPrefix(name, "checkin-valid")
		if !wantValid && !strings.HasPrefix(name, "invalid-") && !strings.HasPrefix(name, "checkin-invalid") {
			t.Errorf("%s: example %q matches neither valid-* nor invalid-* naming; the corpus convention is the oracle's ground truth", examplesRel, name)
			continue
		}
		seen++
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}

		// The semantic marker is part of the corpus contract: the schema
		// must ADMIT these (their invalidity lives above JSON Schema's
		// expressiveness), the parser must still reject them.
		semanticOnly := strings.Contains(string(raw), "INVALID (semantic)")
		if semanticOnly && wantValid {
			t.Errorf("%s/%s: a valid-* example carries the INVALID (semantic) marker", examplesRel, name)
		}

		val, decodable := toJSONValue(t, raw, ext == ".yaml")
		schemaOK := false
		if decodable {
			schemaOK = sch.Validate(val) == nil
		}
		wantSchema := wantValid || semanticOnly
		if schemaOK != wantSchema {
			t.Errorf("%s/%s: schema verdict %v, corpus says %v (schema drift or mislabeled example)",
				examplesRel, name, schemaOK, wantSchema)
		}

		if parse != nil {
			parserOK := parse(raw) == nil
			if parserOK != wantValid {
				t.Errorf("%s/%s: parser verdict %v, corpus says %v",
					examplesRel, name, parserOK, wantValid)
			}
		}
	}
	if seen == 0 {
		t.Fatalf("%s: zero examples matched *%s; the oracle is running on nothing", examplesRel, ext)
	}
}

func TestPolicysetSchemaParserAgreement(t *testing.T) {
	runCorpus(t, "spec/policyset/policyset.schema.json", "spec/policyset/examples", ".yaml",
		func(raw []byte) error { _, err := policy.Parse(raw); return err })
}

func TestAppManifestSchemaParserAgreement(t *testing.T) {
	runCorpus(t, "spec/app-manifest/app-manifest.schema.json", "spec/app-manifest/examples", ".yaml",
		func(raw []byte) error { _, err := manager.Parse(raw); return err })
}

func TestEventsSchemaValidatorAgreement(t *testing.T) {
	runCorpus(t, "spec/events/events.schema.json", "spec/events/examples", ".json",
		events.Validate)
}

// Attestation is schema-side only: the checkin request is decoded inside
// the server handler (no pure exported parser). The examples still pin the
// schema in both polarities.
func TestAttestationExamplesMatchSchema(t *testing.T) {
	runCorpus(t, "spec/attestation/checkin-request.schema.json", "spec/attestation/examples", ".json", nil)
}

// TestSchemaTitles pins the title of a published schema, the name a code
// generator gives the top-level type it writes from the schema.
func TestSchemaTitles(t *testing.T) {
	for _, tc := range []struct{ schema, title string }{
		{"spec/events/events.schema.json", "StrazaEvent"},
	} {
		if got := compileSchema(t, tc.schema).Title; got != tc.title {
			t.Errorf("%s title = %q, want %q", tc.schema, got, tc.title)
		}
	}
}
