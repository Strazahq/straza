package config

// Governance section: the profile-differentiated enforcement knobs and the
// attestation vocabulary they are validated against. Types, validation.
// The env faces stay in config.go's applyEnv.

import (
	"fmt"
	"strconv"
	"time"
)

// Attestation levels a check-in can be gated on, ordered
// none < advisory < managed. Mirrors the identity-plane constants; duplicated
// here because config is a leaf package (store imports config).
const (
	AttestationNone     = "none"
	AttestationAdvisory = "advisory"
	AttestationManaged  = "managed"
)

// AttestationRank orders attestation levels for minimum-level comparisons.
// Unknown levels rank below none so they always fail a gate (fail closed).
func AttestationRank(level string) int {
	switch level {
	case AttestationNone:
		return 1
	case AttestationAdvisory:
		return 2
	case AttestationManaged:
		return 3
	default:
		return 0
	}
}

// Governance holds the profile-differentiated enforcement knobs.
type Governance struct {
	// OfflineGraceTTL bounds how long a client may keep deciding from a valid
	// signed snapshot with the platform unreachable. Zero = fail closed
	// immediately.
	OfflineGraceTTL time.Duration `yaml:"offlineGraceTTL"`
	// LocalToolDefault is the effect when no policy rule matches a tool.pre
	// or permission.request event whose tool is not mcp.call: a local tool,
	// a tool outside the canonical set or no tool. "allow" (standalone) or
	// "deny" (enterprise).
	LocalToolDefault string `yaml:"localToolDefault"`
	// AuditBackpressure is "block" or "drop-with-counter".
	AuditBackpressure string `yaml:"auditBackpressure"`
	// MinAttestation is the minimum attestation level /v1/checkin issues a
	// session token for: "none" (any check-in), "advisory", or "managed".
	// Enterprise default is "managed": tokens only for check-ins whose
	// hashes verify against the expected-hash registry; standalone
	// default is "none".
	MinAttestation string `yaml:"minAttestation"`
	// DeviceTokenTTL is the lifetime of the enroll credential: the
	// long-lived, device-bound token /v1/enroll returns so that enrolling is
	// once per device, not once per ID-token lifetime. Revocable at any time
	// via user disable or device revoke. Zero means the built-in default
	// (720h / 30 days, authn.DefaultDeviceTokenTTL); negative fails boot.
	DeviceTokenTTL time.Duration `yaml:"deviceTokenTTL"`
	// SessionMaxLifetime is the longest a session row stays active from its
	// start, refreshed or not. After it the janitor closes the row and the
	// client starts a fresh session from its device credential without any
	// human action, so it bounds how long a stolen session token stays
	// useful. Zero means the default (12h); negative fails boot.
	SessionMaxLifetime time.Duration `yaml:"sessionMaxLifetime"`
	// CaptureRetention bounds the conversation-turns read model: the janitor
	// purges turns older than this. Deliberately SHORTER than anything
	// governing the audit chain: transcripts are bulkier and more sensitive.
	// Zero means the default (720h / 30 days); negative fails boot.
	CaptureRetention time.Duration `yaml:"captureRetention"`
	// TranscriptBytesWatermark is the transcript-store size in bytes above
	// which the retention janitor logs a loud warning each pass: match-all
	// verbatim capture fills a disk over weeks, and a full disk under Postgres
	// is a fail-closed fleet outage, so the slow path gets loud early. Zero
	// means the default (10 GiB); negative fails boot.
	TranscriptBytesWatermark int64 `yaml:"transcriptBytesWatermark"`
	// AuditIngestBacklogLimit refuses client audit batches (429 +
	// Retry-After) while the outbox holds at least this many unpublished
	// rows: the client spool keeps the records and retries, so
	// nothing is lost; the server just stops absorbing faster than the
	// relay drains. Zero disables the gate (the profile defaults set 50000);
	// negative fails boot.
	AuditIngestBacklogLimit int `yaml:"auditIngestBacklogLimit"`
	// AuditIngestPerSessionRPS bounds one session's audit-batch request
	// rate (per-pod token bucket). Zero or negative disables; the
	// profile defaults set 5, far above the daemon's 30 s drain cadence,
	// low enough to stop a runaway client from monopolizing ingest.
	AuditIngestPerSessionRPS float64 `yaml:"auditIngestPerSessionRPS"`
	// OutboxBulkRetention bounds how long PUBLISHED bulk rows (subjects
	// under straza.audit.>) stay in events_outbox: published
	// audit/capture rows are dead weight (the chain and the read models
	// hold the records) while control rows are kept forever (they ARE the
	// liveSync change feed). Zero means the default (48h); negative fails
	// boot.
	OutboxBulkRetention time.Duration `yaml:"outboxBulkRetention"`
	// Sentinel configures the asynchronous audit sentinel; see sentinel.go.
	Sentinel Sentinel `yaml:"sentinel"`
}

// reacquireSlack bounds how late a client presents its device credential
// after its session's lifetime: the janitor's minute tick, one 300 s session
// token life before the refused refresh, and the verifier's 30 s skew,
// rounded up.
const reacquireSlack = 7 * time.Minute

// lifetimeNotice returns the boot warning for a device credential lifetime
// too short for the session lifetime, or "" when the pair is safe. A client
// presents its device credential only when a session starts and after the
// janitor has closed one, and the credential is renewed at those check-ins
// only when it is past half its life, so the worst case is a credential just
// under half its life old at a session start: the half it has left must
// cover the whole session lifetime plus reacquireSlack.
func (c *Config) lifetimeNotice() string {
	device := c.Governance.DeviceTokenTTL
	if device == 0 {
		device = 30 * 24 * time.Hour
	}
	session := c.EffectiveSessionMaxLifetime()
	least := 2 * (session + reacquireSlack)
	if device >= least {
		return ""
	}
	return fmt.Sprintf("governance.deviceTokenTTL %s is shorter than twice governance.sessionMaxLifetime %s plus %s. "+
		"A client presents its device credential only when a session starts and after the janitor has closed one, "+
		"up to about seven minutes past the lifetime, and renews it only when it is past half its life at that moment, "+
		"so a session that runs its full lifetime can end with an expired credential and a machine locked out until "+
		"someone signs in on it again. Raise deviceTokenTTL to at least %s, or lower sessionMaxLifetime.",
		device, session, reacquireSlack, least)
}

// validateBounds holds the governance numeric-bound checks Validate runs
// before the capture, store-DSN and events checks; validateModes below
// holds the enumeration checks it runs after them. Two methods so the
// first-error order holds.
func (g Governance) validateBounds() error {
	if g.DeviceTokenTTL < 0 {
		return fmt.Errorf("governance.deviceTokenTTL must be a positive duration (e.g. 720h)")
	}
	if g.SessionMaxLifetime < 0 {
		return fmt.Errorf("governance.sessionMaxLifetime must be a positive duration (e.g. 12h)")
	}
	if g.CaptureRetention < 0 {
		return fmt.Errorf("governance.captureRetention must be a positive duration (e.g. 720h)")
	}
	if g.TranscriptBytesWatermark < 0 {
		return fmt.Errorf("governance.transcriptBytesWatermark must be zero (the 10 GiB default) or a positive byte count")
	}
	if g.AuditIngestBacklogLimit < 0 {
		return fmt.Errorf("governance.auditIngestBacklogLimit must be zero (disabled) or a positive row count")
	}
	if g.OutboxBulkRetention < 0 {
		return fmt.Errorf("governance.outboxBulkRetention must be a positive duration (e.g. 48h)")
	}
	return nil
}

// validateModes: see validateBounds for why this is a separate method.
func (g Governance) validateModes() error {
	if g.LocalToolDefault != EffectAllow && g.LocalToolDefault != EffectDeny {
		return fmt.Errorf("unknown governance.localToolDefault %q: expected allow|deny", g.LocalToolDefault)
	}
	if g.AuditBackpressure != BackpressureBlock && g.AuditBackpressure != BackpressureDrop {
		return fmt.Errorf("unknown governance.auditBackpressure %q: expected %q or %q",
			g.AuditBackpressure, BackpressureBlock, BackpressureDrop)
	}
	if g.OfflineGraceTTL < 0 {
		return fmt.Errorf("governance.offlineGraceTTL must be >= 0, got %s", g.OfflineGraceTTL)
	}
	if AttestationRank(g.MinAttestation) == 0 {
		return fmt.Errorf("unknown governance.minAttestation %q: expected none|advisory|managed",
			g.MinAttestation)
	}
	return nil
}

// applyEnvGovernance binds the governance section's env faces; applyEnv
// (config.go) sequences the binders, and each face writes a field no other
// face writes.
func applyEnvGovernance(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	set("STRAZA_MIN_ATTESTATION", func(v string) { cfg.Governance.MinAttestation = v })
	set("STRAZA_DEVICE_TOKEN_TTL", func(v string) {
		// An unparseable value must fail boot loudly (Validate rejects the
		// sentinel), never fall back silently to the previous credential TTL.
		d, err := time.ParseDuration(v)
		if err != nil {
			d = -1
		}
		cfg.Governance.DeviceTokenTTL = d
	})
	set("STRAZA_SESSION_MAX_LIFETIME", func(v string) {
		d, err := time.ParseDuration(v)
		if err != nil {
			d = -1 // Validate rejects the sentinel: fail boot, never fall back silently
		}
		cfg.Governance.SessionMaxLifetime = d
	})
	set("STRAZA_CAPTURE_RETENTION", func(v string) {
		d, err := time.ParseDuration(v)
		if err != nil {
			d = -1 // Validate rejects the sentinel: fail boot, never fall back silently
		}
		cfg.Governance.CaptureRetention = d
	})
	set("STRAZA_TRANSCRIPT_BYTES_WATERMARK", func(v string) {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			n = -1 // Validate rejects the sentinel: fail boot, never fall back silently
		}
		cfg.Governance.TranscriptBytesWatermark = n
	})
	set("STRAZA_AUDIT_INGEST_BACKLOG_LIMIT", func(v string) {
		n, err := strconv.Atoi(v)
		if err != nil {
			n = -1 // Validate rejects the sentinel: fail boot, never fall back silently
		}
		cfg.Governance.AuditIngestBacklogLimit = n
	})
	set("STRAZA_OUTBOX_BULK_RETENTION", func(v string) {
		d, err := time.ParseDuration(v)
		if err != nil {
			d = -1 // Validate rejects the sentinel: fail boot, never fall back silently
		}
		cfg.Governance.OutboxBulkRetention = d
	})
	set("STRAZA_AUDIT_INGEST_PER_SESSION_RPS", func(v string) {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			f = 0 // unparseable = disabled (limiter semantics; backlog gate still guards)
		}
		cfg.Governance.AuditIngestPerSessionRPS = f
	})
}
