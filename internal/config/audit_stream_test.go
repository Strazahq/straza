package config

import (
	"testing"
	"time"
)

// TestAuditStreamKnobValidation pins the knob contract: zero means the
// derived default, negatives fail boot loudly (including the env-parse
// sentinel: a typo must never silently fall back).
func TestAuditStreamKnobValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"defaults valid", func(c *Config) {}, false},
		{"explicit bounds valid", func(c *Config) {
			c.Events.AuditStreamMaxAge = 48 * time.Hour
			c.Events.AuditStreamMaxBytes = 1 << 30
		}, false},
		{"negative age fails", func(c *Config) { c.Events.AuditStreamMaxAge = -1 }, true},
		{"negative bytes fails", func(c *Config) { c.Events.AuditStreamMaxBytes = -1 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Loader{Getenv: noEnv}.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tc.mutate(&cfg)
			err = cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestAuditStreamEnvOverrides pins the env faces, including the fail-boot
// sentinel on unparseable values.
func TestAuditStreamEnvOverrides(t *testing.T) {
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}

	cfg, err := Loader{Getenv: env(map[string]string{
		"STRAZA_EVENTS_AUDIT_STREAM_MAX_AGE":   "72h",
		"STRAZA_EVENTS_AUDIT_STREAM_MAX_BYTES": "1073741824",
	})}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Events.AuditStreamMaxAge != 72*time.Hour {
		t.Errorf("AuditStreamMaxAge = %v, want 72h", cfg.Events.AuditStreamMaxAge)
	}
	if cfg.Events.AuditStreamMaxBytes != 1<<30 {
		t.Errorf("AuditStreamMaxBytes = %d, want %d", cfg.Events.AuditStreamMaxBytes, 1<<30)
	}

	for name, vals := range map[string]map[string]string{
		"bad duration": {"STRAZA_EVENTS_AUDIT_STREAM_MAX_AGE": "soon"},
		"bad bytes":    {"STRAZA_EVENTS_AUDIT_STREAM_MAX_BYTES": "2GiB"},
	} {
		if _, err := (Loader{Getenv: env(vals)}).Load(); err == nil {
			t.Errorf("%s: Load succeeded, want fail-boot on unparseable value", name)
		}
	}
}

// TestEffectiveCaptureRetention pins the single shared default.
func TestEffectiveCaptureRetention(t *testing.T) {
	var c Config
	if got := c.EffectiveCaptureRetention(); got != 30*24*time.Hour {
		t.Errorf("zero = %v, want 720h", got)
	}
	c.Governance.CaptureRetention = time.Hour
	if got := c.EffectiveCaptureRetention(); got != time.Hour {
		t.Errorf("set = %v, want 1h", got)
	}
}
