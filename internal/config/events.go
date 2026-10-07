package config

// Events section: the event spine. Types, validation. The env faces stay in
// config.go's applyEnv. A leftover `telemetry:` key from the removed
// Telemetry section is ignored with a boot notice.

import (
	"fmt"
	"strconv"
	"time"
)

// Events configures the event spine.
type Events struct {
	// Embedded runs an in-process NATS server with JetStream.
	Embedded bool `yaml:"embedded"`
	// URL points at an external NATS deployment when Embedded is false.
	// Daemons ride the same-origin SSE push lane, GET /v1/push, so no
	// client-reachable broker exists; a leftover events.clientUrl or
	// STRAZA_EVENTS_CLIENT_URL boots with one notice, see config.go.
	URL string `yaml:"url"`
	// AuditStreamMaxAge bounds how long the STRAZA_AUDIT JetStream stream
	// retains messages. The stream is a transport buffer, not the
	// record: the hash chain and events_outbox hold the durable copies, so
	// aged-out messages cost nothing once consumed. Zero means the default
	// (2x the effective governance.captureRetention); negative fails boot.
	AuditStreamMaxAge time.Duration `yaml:"auditStreamMaxAge"`
	// AuditStreamMaxBytes bounds the STRAZA_AUDIT stream's on-disk size.
	// Unbounded, a full disk stops ALL event publication through the
	// shared outbox relay, kill-switch revocations included; bounded, the
	// stream discards its OLDEST messages and publishes keep flowing. Zero
	// means the default (2 GiB); size it well below the NATS volume.
	// Negative fails boot.
	AuditStreamMaxBytes int64 `yaml:"auditStreamMaxBytes"`
	// PushEdgeMaxConns caps concurrent daemon subscriptions to the
	// gateway-edge push lane (GET /v1/push) PER POD. Over the
	// cap the endpoint answers 503 + Retry-After and the daemon rides its
	// poll lane until a retry lands; push is an accelerator, never a
	// dependency. Zero means the default (65536, i.e. bounded by the pod's
	// fd budget before this knob); negative fails boot.
	PushEdgeMaxConns int `yaml:"pushEdgeMaxConns"`
}

// validate holds the events section's checks.
func (e Events) validate() error {
	if !e.Embedded && e.URL == "" {
		return fmt.Errorf("events.embedded=false requires events.url (external NATS URL)")
	}
	if e.AuditStreamMaxAge < 0 {
		return fmt.Errorf("events.auditStreamMaxAge must be a positive duration (e.g. 1440h)")
	}
	if e.AuditStreamMaxBytes < 0 {
		return fmt.Errorf("events.auditStreamMaxBytes must be a positive byte count")
	}
	if e.PushEdgeMaxConns < 0 {
		return fmt.Errorf("events.pushEdgeMaxConns must be a positive connection count")
	}
	return nil
}

// applyEnvEvents binds the events section's env faces; applyEnv
// (config.go) sequences the binders, and each face writes a field no other
// face writes.
func applyEnvEvents(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	set("STRAZA_EVENTS_URL", func(v string) {
		cfg.Events.URL = v
		cfg.Events.Embedded = false
	})
	set("STRAZA_EVENTS_AUDIT_STREAM_MAX_AGE", func(v string) {
		d, err := time.ParseDuration(v)
		if err != nil {
			d = -1 // Validate rejects the sentinel: fail boot, never fall back silently
		}
		cfg.Events.AuditStreamMaxAge = d
	})
	set("STRAZA_EVENTS_AUDIT_STREAM_MAX_BYTES", func(v string) {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			n = -1 // Validate rejects the sentinel: fail boot, never fall back silently
		}
		cfg.Events.AuditStreamMaxBytes = n
	})
	set("STRAZA_EVENTS_PUSH_EDGE_MAX_CONNS", func(v string) {
		n, err := strconv.Atoi(v)
		if err != nil {
			n = -1 // Validate rejects the sentinel: fail boot, never fall back silently
		}
		cfg.Events.PushEdgeMaxConns = n
	})
}
