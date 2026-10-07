package config

import (
	"strings"
	"testing"
)

// TestRemovedTelemetryKeyIgnoredWithNotice pins the telemetry removal: a config
// file that still carries the retired `telemetry:` section loads without
// error (decoding is non-strict) and Load attaches exactly one operator
// notice naming the key; a file without it carries no notice. Notices is
// never a knob (yaml:"-", so the knob table does not list it).
func TestRemovedTelemetryKeyIgnoredWithNotice(t *testing.T) {
	path := writeFile(t, "telemetry:\n  enabled: true\n")
	cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load with leftover telemetry key: %v", err)
	}
	if len(cfg.Notices) != 1 || !strings.Contains(cfg.Notices[0], "telemetry") || !strings.Contains(cfg.Notices[0], "removed") {
		t.Fatalf("Notices = %q, want one telemetry removal notice", cfg.Notices)
	}

	clean := writeFile(t, "log:\n  level: info\n")
	cfg, err = Loader{FilePath: clean, ExplicitFile: true, Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load clean: %v", err)
	}
	if len(cfg.Notices) != 0 {
		t.Fatalf("Notices on a clean file = %q, want none", cfg.Notices)
	}
}

// TestRemovedClientURLKeyIgnoredWithNotice pins the removal of the
// direct NATS client push lane: a config file that still
// sets events.clientUrl, or an environment that still exports
// STRAZA_EVENTS_CLIENT_URL, loads without error and carries exactly one
// notice naming the retired knob and the lane that replaced it (the
// same-origin SSE push lane), so an operator learns why daemons are not
// handed a broker URL any more; a clean config carries none.
func TestRemovedClientURLKeyIgnoredWithNotice(t *testing.T) {
	path := writeFile(t, "events:\n  clientUrl: nats://push.example:4222\n")
	cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load with leftover events.clientUrl: %v", err)
	}
	if len(cfg.Notices) != 1 || !strings.Contains(cfg.Notices[0], "events.clientUrl") || !strings.Contains(cfg.Notices[0], "/v1/push") {
		t.Fatalf("Notices = %q, want one events.clientUrl removal notice naming the SSE lane", cfg.Notices)
	}

	env := envMap(map[string]string{"STRAZA_EVENTS_CLIENT_URL": "nats://push.example:4222"})
	cfg, err = Loader{Getenv: env}.Load()
	if err != nil {
		t.Fatalf("Load with leftover STRAZA_EVENTS_CLIENT_URL: %v", err)
	}
	if len(cfg.Notices) != 1 || !strings.Contains(cfg.Notices[0], "STRAZA_EVENTS_CLIENT_URL") {
		t.Fatalf("Notices = %q, want one STRAZA_EVENTS_CLIENT_URL removal notice", cfg.Notices)
	}

	clean := writeFile(t, "events:\n  embedded: true\n")
	cfg, err = Loader{FilePath: clean, ExplicitFile: true, Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load clean events: %v", err)
	}
	if len(cfg.Notices) != 0 {
		t.Fatalf("Notices on a clean events block = %q, want none", cfg.Notices)
	}
}
