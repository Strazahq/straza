package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/server/metrics"
	"github.com/strazahq/straza/internal/store"
)

// gaugeValue reads one gauge off the app's private registry (no testutil
// import: keeps go.mod untouched for a test-only readback).
func gaugeValue(t *testing.T, a *App, name string) float64 {
	t.Helper()
	families, err := a.metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("gauge %s not registered", name)
	return 0
}

// TestTranscriptWatermarkPass pins the janitor's disk-fill guard: the pass
// sets the transcript-store gauge from the store's real measurement, stays
// quiet under the watermark, and warns at/above it (both directions
// pinned, so the comparison cannot silently invert). The disk-free gauge
// keeps its -1 unknown sentinel when dataDir is unset, and reports a
// positive number for a real path on unix.
func TestTranscriptWatermarkPass(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()

	if _, err := app.store.Conversations().Insert(ctx, store.ConversationTurn{
		CEID: "ce-wm-1", SessionID: "s-wm", UserID: "u-bob", Kind: "reply",
		Mode: "verbatim", Content: strings.Repeat("bytes ", 512),
		ContentHash: "sha256:ce-wm-1", At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Under the (defaulted 10 GiB) watermark: measured, gauged, quiet.
	bytes, warned := app.transcriptWatermarkPass(ctx)
	if bytes <= 0 {
		t.Fatalf("measured bytes = %d, want > 0", bytes)
	}
	if warned {
		t.Fatal("warned under the 10 GiB default watermark")
	}
	if got := gaugeValue(t, app, "straza_transcript_store_bytes"); got != float64(bytes) {
		t.Fatalf("gauge = %v, want %d", got, bytes)
	}

	// At/above the watermark: loud, with a link to the public docs page. The
	// watermark and the logger are set before the Run goroutine exists,
	// because Run's periodic pass reads both and a later write is a data race.
	log, logs := captureLogger()
	loud, _ := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }},
		func(c *config.Config) { c.Governance.TranscriptBytesWatermark = 1 })
	if _, err := loud.store.Conversations().Insert(ctx, store.ConversationTurn{
		CEID: "ce-wm-2", SessionID: "s-wm", UserID: "u-bob", Kind: "reply",
		Mode: "verbatim", Content: strings.Repeat("bytes ", 512),
		ContentHash: "sha256:ce-wm-2", At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, warned := loud.transcriptWatermarkPass(ctx); !warned {
		t.Fatal("did not warn above a 1-byte watermark")
	}
	if got := logs.String(); !strings.Contains(got, "docs=https://docs.straza.ai/guides/write-policy/capture/#retention-and-where-the-text-lives") || strings.Contains(got, "docs/perf.md") {
		t.Fatalf("the watermark warning must link the public capture guide and name no repository file, got:\n%s", got)
	}

	// Disk-free semantics: a fresh registry starts at the -1 unknown
	// sentinel (set before any pass; the !unix build never leaves it), and
	// a pass over a real dataDir replaces it with a positive measurement
	// on unix.
	fresh := metrics.New(func() float64 { return 0 }, func() float64 { return 0 })
	fams, err := fresh.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather fresh: %v", err)
	}
	found := false
	for _, f := range fams {
		if f.GetName() == "straza_data_disk_free_bytes" {
			found = true
			if v := f.GetMetric()[0].GetGauge().GetValue(); v != -1 {
				t.Fatalf("fresh disk-free gauge = %v, want the -1 unknown sentinel", v)
			}
		}
	}
	if !found {
		t.Fatal("straza_data_disk_free_bytes not registered")
	}
	if _, ok := diskFreeBytes(app.cfg.DataDir); ok {
		if got := gaugeValue(t, app, "straza_data_disk_free_bytes"); got <= 0 {
			t.Fatalf("disk-free gauge after a pass over a real dataDir = %v, want > 0", got)
		}
	}
}
