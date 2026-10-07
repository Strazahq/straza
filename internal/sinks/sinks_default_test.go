package sinks

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

// TestSinkDefaultExcludesCapture pins the default: a sink with no subjects
// receives everything EXCEPT captured conversation content, and explicit
// subjects (straza.>) still opt in to all of it.
func TestSinkDefaultExcludesCapture(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	runnerFilters := func(subjects []string) []string {
		cfg := config.Config{Sinks: []config.Sink{{
			Name: "t", Type: config.SinkFile, Path: path, Subjects: subjects,
		}}}
		runners, closeAll, err := Build(cfg, nil, log, nil)
		if err != nil {
			t.Fatalf("buildSinks: %v", err)
		}
		t.Cleanup(closeAll)
		var got []string
		for _, r := range runners {
			got = append(got, r.Filters()...)
		}
		return got
	}

	def := strings.Join(runnerFilters(nil), " ")
	if strings.Contains(def, "prompt") || strings.Contains(def, "reply") {
		t.Fatalf("default subjects include capture: %s", def)
	}
	for _, want := range []string{"straza.audit.tool", "straza.revocation.>", "straza.audit.approval"} {
		if !strings.Contains(def, want) {
			t.Fatalf("default subjects missing %s: %s", want, def)
		}
	}

	all := strings.Join(runnerFilters([]string{"straza.>"}), " ")
	if !strings.Contains(all, "straza.audit.>") {
		t.Fatalf("explicit straza.> no longer reaches the audit stream: %s", all)
	}
}
