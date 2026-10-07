package events

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const specExamples = "../../spec/events/examples"

// TestEnvelopeAgreesWithSpecExamples pins that the Go envelope validator and
// events.schema.json agree on every example in the corpus.
func TestEnvelopeAgreesWithSpecExamples(t *testing.T) {
	entries, err := os.ReadDir(specExamples)
	if err != nil {
		t.Fatalf("read spec examples: %v", err)
	}
	var valid, invalid int
	for _, e := range entries {
		name := e.Name()
		raw, err := os.ReadFile(filepath.Join(specExamples, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		switch {
		case strings.HasPrefix(name, "valid-"):
			valid++
			if err := Validate(raw); err != nil {
				t.Errorf("%s: expected valid, got: %v", name, err)
			}
		case strings.HasPrefix(name, "invalid-"):
			invalid++
			if err := Validate(raw); err == nil {
				t.Errorf("%s: expected invalid, validated cleanly", name)
			}
		default:
			t.Errorf("unclassified example %s (name must start valid-/invalid-)", name)
		}
	}
	if valid < 3 || invalid < 3 {
		t.Errorf("spec corpus needs >=3 valid and >=3 invalid examples, got %d/%d", valid, invalid)
	}
}

func TestValidType(t *testing.T) {
	for _, good := range []string{
		"straza.audit.tool", "straza.audit.mcp", "straza.audit.identity",
		"straza.audit.prompt", "straza.audit.reply", "straza.audit.sentinel",
		"straza.audit.approval",
		"straza.policy.updated",
		"straza.revocation.session", "straza.revocation.lift", "straza.apps.drift",
		"straza.identity.created",
	} {
		if !ValidType(good) {
			t.Errorf("ValidType(%q) = false", good)
		}
	}
	for _, bad := range []string{"straza.audit", "straza.audit.sentinelx", "straza.apps.exploded", "other.audit.tool", ""} {
		if ValidType(bad) {
			t.Errorf("ValidType(%q) = true", bad)
		}
	}
}
