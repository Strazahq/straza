package config

import (
	"strings"
	"testing"
)

// TestEffectiveTranscriptBytesWatermark pins the janitor watermark knob:
// zero means the 10 GiB default (one place, like the retention default),
// an explicit value wins, and a negative fails validation loudly instead
// of silently disabling the only guard on the slow disk-fill path.
func TestEffectiveTranscriptBytesWatermark(t *testing.T) {
	var c Config
	if got := c.EffectiveTranscriptBytesWatermark(); got != 10<<30 {
		t.Fatalf("default = %d, want 10 GiB", got)
	}
	c.Governance.TranscriptBytesWatermark = 1 << 20
	if got := c.EffectiveTranscriptBytesWatermark(); got != 1<<20 {
		t.Fatalf("explicit = %d, want 1 MiB", got)
	}

	bad := defaults(ProfileStandalone)
	bad.Governance.TranscriptBytesWatermark = -1
	err := bad.Validate()
	if err == nil || !strings.Contains(err.Error(), "transcriptBytesWatermark") {
		t.Fatalf("negative watermark: want a naming validation error, got %v", err)
	}
}
