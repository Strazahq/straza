// Package config implements layered configuration for strazad and strazactl.
//
// Precedence (lowest to highest): profile defaults ← config file ← environment
// variables ← command-line flags. The profile itself is resolved first (flag >
// env > file > built-in default "standalone") because it determines the
// defaults every other layer overrides. This package encodes the per-profile
// knob table.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Profile names.
const (
	ProfileStandalone = "standalone"
	ProfileEnterprise = "enterprise"
)

// Store drivers.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// Audit backpressure modes. Invariant: overload either blocks the producer or
// drops with a visible counter, never silently.
const (
	BackpressureBlock = "block"
	BackpressureDrop  = "drop-with-counter"
)

// Local-tool default effects.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// Config is the fully resolved strazad configuration.
type Config struct {
	// Profile is "standalone" or "enterprise". It selects defaults only;
	// every knob below remains individually overridable.
	Profile string `yaml:"profile"`
	// DataDir holds local state: sqlite database, embedded NATS store, etc.
	DataDir string `yaml:"dataDir"`
	// Notices are boot-time warnings Load attaches: keys it read and
	// ignored (a removed section still present in the file) and a lifetime
	// pair that can lock an active client out (lifetimeNotice). Not a knob:
	// never read from yaml, never validated; build() logs each at Warn
	// before any subsystem starts so the operator can act on it.
	Notices []string `yaml:"-"`

	Server     Server       `yaml:"server"`
	Log        Log          `yaml:"log"`
	Store      Store        `yaml:"store"`
	Events     Events       `yaml:"events"`
	OIDC       OIDC         `yaml:"oidc"`
	OAuth      OAuthConnect `yaml:"oauth"`
	Governance Governance   `yaml:"governance"`
	Approval   Approval     `yaml:"approval"`
	Apps       Apps         `yaml:"apps"`
	Secrets    Secrets      `yaml:"secrets"`
	SCIM       SCIM         `yaml:"scim"`
	Admin      Admin        `yaml:"admin"`
	Capture    Capture      `yaml:"capture"`
	Sinks      []Sink       `yaml:"sinks"`
}

// defaults returns the built-in defaults for a profile.
func defaults(profile string) Config {
	// Approver auto-mint is a standalone convenience (self-hosters get a
	// working phone surface with zero TLS config); enterprise keeps the
	// explicit bring-your-own contract unless the operator opts in.
	autoMint := profile != ProfileEnterprise
	cfg := Config{
		Profile: profile,
		DataDir: "data",
		Server: Server{Listen: "127.0.0.1:8420", PublicURL: "http://127.0.0.1:8420",
			MaxBodyBytes: 1 << 20, LoginPerIPRPS: 2,
			ApproverTLS: ApproverTLS{AutoMint: &autoMint, PerIPRPS: 10}},
		Log:    Log{Level: "info", Format: "json"},
		Store:  Store{Driver: DriverSQLite},
		Events: Events{Embedded: true},
		OIDC:   OIDC{ClientID: "straza", JITProvision: true},
		Governance: Governance{
			OfflineGraceTTL:          15 * time.Minute,
			LocalToolDefault:         EffectAllow,
			AuditBackpressure:        BackpressureDrop,
			MinAttestation:           AttestationNone,
			DeviceTokenTTL:           30 * 24 * time.Hour,
			SessionMaxLifetime:       12 * time.Hour,
			AuditIngestBacklogLimit:  50_000,
			AuditIngestPerSessionRPS: 5,
			Sentinel:                 defaultSentinel(),
		},
		Approval: defaultApproval(),
		Apps: Apps{
			PollInterval:   time.Second,
			HealthInterval: 20 * time.Second,
			// A loopback upstream is a server on the standalone host itself;
			// enterprise refuses one unless the operator says so.
			AllowLoopbackUpstreams: profile != ProfileEnterprise,
			// Governance-forward catalog defaults, shared by both profiles (the
			// enterprise branch below overrides only Server/Store/OIDC/Governance):
			// hide policy-denied tools, warn past 100 tools per role, page
			// tools/list at 200.
			Catalog: Catalog{PolicyFilter: true, WarnSize: 100, PageSize: 200},
		},
	}
	if profile == ProfileEnterprise {
		cfg.Server.Listen = ":8420"
		cfg.Store.Driver = DriverPostgres
		cfg.OIDC.JITProvision = false
		cfg.Governance = Governance{
			OfflineGraceTTL:          0,
			LocalToolDefault:         EffectDeny,
			AuditBackpressure:        BackpressureBlock,
			MinAttestation:           AttestationManaged,
			DeviceTokenTTL:           30 * 24 * time.Hour,
			SessionMaxLifetime:       12 * time.Hour,
			AuditIngestBacklogLimit:  50_000,
			AuditIngestPerSessionRPS: 5,
			Sentinel:                 defaultSentinel(),
		}
	}
	return cfg
}

// SQLitePath returns the resolved sqlite database path.
func (c Config) SQLitePath() string {
	if c.Store.DSN != "" {
		return c.Store.DSN
	}
	return filepath.Join(c.DataDir, "straza.db")
}

// TLSEnabled reports whether the listener serves native HTTPS.
func (c Config) TLSEnabled() bool {
	return c.Server.TLS.CertFile != "" && c.Server.TLS.KeyFile != ""
}

// AppsDir returns the resolved GitOps directory (default "<dataDir>/apps").
func (c *Config) AppsDir() string {
	if c.Apps.Dir != "" {
		return c.Apps.Dir
	}
	return filepath.Join(c.DataDir, "apps")
}

// KEKFile returns the resolved key-encryption-key path
// (default "<dataDir>/secret.key").
func (c Config) KEKFile() string {
	if c.Secrets.KEKFile != "" {
		return c.Secrets.KEKFile
	}
	return filepath.Join(c.DataDir, "secret.key")
}

// EffectiveOutboxBulkRetention returns governance.outboxBulkRetention with
// the built-in default (48h) applied. Pointer receiver for the same reason
// as EffectiveCaptureRetention below.
func (c *Config) EffectiveOutboxBulkRetention() time.Duration {
	if c.Governance.OutboxBulkRetention == 0 {
		return 48 * time.Hour
	}
	return c.Governance.OutboxBulkRetention
}

// EffectiveSessionMaxLifetime returns governance.sessionMaxLifetime with the
// built-in default (12h) applied. Pointer receiver for the same reason as
// EffectiveCaptureRetention below.
func (c *Config) EffectiveSessionMaxLifetime() time.Duration {
	if c.Governance.SessionMaxLifetime == 0 {
		return 12 * time.Hour
	}
	return c.Governance.SessionMaxLifetime
}

// EffectiveCaptureRetention returns governance.captureRetention with the
// built-in default (720h / 30 days) applied: the ONE place the default
// lives, shared by the transcript janitor and the audit-stream age bound.
// Pointer receiver ON PURPOSE (unlike the other Config helpers): a value
// receiver copies the whole struct, so a goroutine calling it reads EVERY
// config field and races with test-side mutation of unrelated fields; the
// pointer form touches only the one field it needs.
func (c *Config) EffectiveCaptureRetention() time.Duration {
	if c.Governance.CaptureRetention == 0 {
		return 30 * 24 * time.Hour
	}
	return c.Governance.CaptureRetention
}

// EffectiveTranscriptBytesWatermark returns
// governance.transcriptBytesWatermark with the built-in default (10 GiB)
// applied. Pointer receiver for the same reason as
// EffectiveCaptureRetention above.
func (c *Config) EffectiveTranscriptBytesWatermark() int64 {
	if c.Governance.TranscriptBytesWatermark == 0 {
		return 10 << 30
	}
	return c.Governance.TranscriptBytesWatermark
}

// EffectivePushEdgeMaxConns returns events.pushEdgeMaxConns with the
// built-in default (65536) applied. Pointer receiver for the same reason as
// EffectiveCaptureRetention above.
func (c *Config) EffectivePushEdgeMaxConns() int {
	if c.Events.PushEdgeMaxConns == 0 {
		return 65536
	}
	return c.Events.PushEdgeMaxConns
}

// Validate rejects configurations strazad cannot serve, with actionable
// messages (fail closed on unknown state). The per-section validators run in
// a fixed order: first error wins, so the sequence is load-bearing (store
// and governance are split in two on purpose).
func (c Config) Validate() error {
	if c.Profile != ProfileStandalone && c.Profile != ProfileEnterprise {
		return fmt.Errorf("unknown profile %q: expected %q or %q", c.Profile, ProfileStandalone, ProfileEnterprise)
	}
	if err := c.Log.validate(); err != nil {
		return err
	}
	if err := c.Store.validateDriver(); err != nil {
		return err
	}
	if err := c.SCIM.validate(); err != nil {
		return err
	}
	if err := c.Governance.validateBounds(); err != nil {
		return err
	}
	if err := c.Capture.validate(c.Profile); err != nil {
		return err
	}
	if err := c.Store.validateDSN(); err != nil {
		return err
	}
	if err := c.Events.validate(); err != nil {
		return err
	}
	if err := c.Governance.validateModes(); err != nil {
		return err
	}
	if err := c.OIDC.validate(); err != nil {
		return err
	}
	if err := c.Governance.Sentinel.validate(); err != nil {
		return err
	}
	if err := c.Approval.validate(); err != nil {
		return err
	}
	if err := c.Server.validate(); err != nil {
		return err
	}
	if err := c.OAuth.validate(); err != nil {
		return err
	}
	if err := validateSinks(c.Sinks); err != nil {
		return err
	}
	if err := c.Apps.validate(); err != nil {
		return err
	}
	return nil
}

// Overrides carries explicit command-line flag values, the highest-precedence
// layer. Nil pointers mean "flag not set".
type Overrides struct {
	Profile  *string
	DataDir  *string
	Listen   *string
	LogLevel *string
	StoreDSN *string
}

// removedKeyNotices peeks the raw config document for keys whose section or
// knob no longer exists and returns one operator notice per hit (Config
// decoding is non-strict, so a leftover key parses silently; the notice is
// the honest trace). The removed keys are `telemetry`, `events.clientUrl`
// (the retired direct NATS client push lane) and
// `scim.exposeControlPlaneRoles` (retired: all roles are exposed).
func removedKeyNotices(raw []byte) []string {
	var peek struct {
		Telemetry map[string]any `yaml:"telemetry"`
		Events    struct {
			ClientURL *string `yaml:"clientUrl"`
		} `yaml:"events"`
		SCIM struct {
			ExposeControlPlaneRoles *bool `yaml:"exposeControlPlaneRoles"`
		} `yaml:"scim"`
	}
	if yaml.Unmarshal(raw, &peek) != nil {
		return nil
	}
	var out []string
	if peek.Telemetry != nil {
		out = append(out, "telemetry: config key ignored. OpenTelemetry support was removed (it was always a no-op), so the key can be deleted.")
	}
	if peek.Events.ClientURL != nil {
		out = append(out, removedClientURLNotice("events.clientUrl"))
	}
	if peek.SCIM.ExposeControlPlaneRoles != nil {
		out = append(out, "scim.exposeControlPlaneRoles: ignored. The knob was retired and every role now renders on the SCIM wire-group surface (the IdM masters membership everywhere, straza-admin included), so delete the setting.")
	}
	return out
}

// removedClientURLNotice is the one-boot notice for the retired direct NATS
// client push lane: daemons get sub-second kill-switch push over the
// same-origin SSE lane (GET /v1/push, session-token auth), so no
// client-reachable broker is handed out any more.
func removedClientURLNotice(face string) string {
	return face + ": ignored. The direct NATS client push lane was removed. Daemons ride the same-origin SSE push lane (GET /v1/push) and need no broker URL, so delete the setting."
}

// Loader assembles a Config from the four layers.
type Loader struct {
	// FilePath is the config file location; empty means "no file". A non-empty
	// path that does not exist is an error only when ExplicitFile is set.
	FilePath string
	// ExplicitFile marks FilePath as user-supplied (via flag/env) rather than
	// a default probe location.
	ExplicitFile bool
	// Getenv abstracts the environment for tests; defaults to os.Getenv.
	Getenv func(string) string
	// Flags is the highest-precedence layer.
	Flags Overrides
}

// Load resolves the profile, applies all layers in precedence order, and
// validates the result.
func (l Loader) Load() (Config, error) {
	getenv := l.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	raw, err := l.readFile()
	if err != nil {
		return Config{}, err
	}

	profile := l.resolveProfile(raw, getenv)
	cfg := defaults(profile)

	// Layer 2: config file. yaml.Unmarshal into the pre-populated struct
	// overwrites only the keys present in the document.
	if raw != nil {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config file %s: %w", l.FilePath, err)
		}
		cfg.Notices = append(cfg.Notices, removedKeyNotices(raw)...)
	}

	// Layer 3: environment.
	applyEnv(&cfg, getenv)
	if getenv("STRAZA_EVENTS_CLIENT_URL") != "" {
		cfg.Notices = append(cfg.Notices, removedClientURLNotice("STRAZA_EVENTS_CLIENT_URL"))
	}

	// Layer 4: flags.
	applyFlags(&cfg, l.Flags)

	// The profile knob itself must not be silently rewritten by lower layers
	// after defaults were chosen from it.
	cfg.Profile = profile

	normalizeOAuth(&cfg.OAuth)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if n := cfg.lifetimeNotice(); n != "" {
		cfg.Notices = append(cfg.Notices, n)
	}
	return cfg, nil
}

func (l Loader) readFile() ([]byte, error) {
	if l.FilePath == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(l.FilePath)
	if os.IsNotExist(err) && !l.ExplicitFile {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", l.FilePath, err)
	}
	return raw, nil
}

// resolveProfile picks the profile before defaults are applied:
// flag > env > file > standalone.
func (l Loader) resolveProfile(fileRaw []byte, getenv func(string) string) string {
	if l.Flags.Profile != nil && *l.Flags.Profile != "" {
		return *l.Flags.Profile
	}
	if v := getenv("STRAZA_PROFILE"); v != "" {
		return v
	}
	if fileRaw != nil {
		var peek struct {
			Profile string `yaml:"profile"`
		}
		if yaml.Unmarshal(fileRaw, &peek) == nil && peek.Profile != "" {
			return peek.Profile
		}
	}
	return ProfileStandalone
}

// envSet returns the per-face setter the section binders share: apply runs
// only when the variable is set and non-empty, so an absent variable leaves
// the file layer's value in place (precedence file < env < flags is
// sequenced in Load).
func envSet(getenv func(string) string) func(key string, apply func(string)) {
	return func(key string, apply func(string)) {
		if v := getenv(key); v != "" {
			apply(v)
		}
	}
}

// applyEnv maps environment variables to config fields. Each section file
// holds its applyEnvX binder and applyEnv sequences them, so the supported
// surface stays greppable and documented. Every face writes a field no other
// face writes, so the sequence order is not load-bearing; knobs_test.go
// scans the whole package for wired faces.
func applyEnv(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	set("STRAZA_DATA_DIR", func(v string) { cfg.DataDir = v })
	applyEnvServer(cfg, getenv)
	applyEnvLog(cfg, getenv)
	applyEnvStore(cfg, getenv)
	applyEnvOIDC(cfg, getenv)
	applyEnvEvents(cfg, getenv)
	applyEnvSecrets(cfg, getenv)
	applyEnvGovernance(cfg, getenv)
	applyEnvCapture(cfg, getenv)
	applyEnvApproval(cfg, getenv)
	applyEnvApps(cfg, getenv)
	applyEnvOAuth(cfg, getenv)
}

// applyEnvApproval binds the approval section's env faces (Slack channel,
// retention, gateway hold, mobile push, args preview).
func applyEnvApproval(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	// Enabling is the explicit act: only "true"/"1" turns the channel on; any
	// other value stays off (fail-safe) so an env typo can't half-enable Slack.
	// Lets env-only deployments (the eval compose) enable the channel without
	// a config file.
	set("STRAZA_APPROVAL_SLACK_ENABLED", func(v string) {
		cfg.Approval.Channels.Slack.Enabled = v == "true" || v == "1"
	})
	set("STRAZA_APPROVAL_SLACK_BOT_TOKEN", func(v string) { cfg.Approval.Channels.Slack.BotToken = v })
	set("STRAZA_APPROVAL_SLACK_SIGNING_SECRET", func(v string) { cfg.Approval.Channels.Slack.SigningSecret = v })
	// The *_FILE faces exist because the file variant is the PREFERRED form
	// for every secret-bearing knob (an env-inlined secret is readable in
	// /proc/<pid>/environ and `docker inspect`), so an env-only deployment
	// needs a file face too. Same class: bodystore (capture.go) + oauth github
	// below.
	// validate's set-one-not-both rule sees the merged layers, so an env
	// file face colliding with a yaml inline value fails boot loudly.
	set("STRAZA_APPROVAL_SLACK_BOT_TOKEN_FILE", func(v string) { cfg.Approval.Channels.Slack.BotTokenFile = v })
	set("STRAZA_APPROVAL_SLACK_SIGNING_SECRET_FILE", func(v string) { cfg.Approval.Channels.Slack.SigningSecretFile = v })
	set("STRAZA_APPROVAL_SLACK_CHANNEL", func(v string) { cfg.Approval.Channels.Slack.Channel = v })
	set("STRAZA_APPROVAL_RETENTION", func(v string) {
		d, err := time.ParseDuration(v)
		if err != nil {
			d = -1 // validate rejects the sentinel: fail boot, never fall back silently
		}
		cfg.Approval.Retention = d
	})
	set("STRAZA_APPROVAL_GATEWAY_HOLD_SECONDS", func(v string) {
		n, err := strconv.Atoi(v)
		if err != nil {
			n = -1 // validate rejects the sentinel: fail boot, never fall back silently
		}
		cfg.Approval.GatewayHoldSeconds = n
	})
	// Mobile push (the Straza approver app). Enabling FCM is the explicit
	// act: only "true"/"1" turns it on, mirroring the Slack fail-safe so an env
	// typo can't half-enable delivery. AllowedPushHosts is comma-separated.
	set("STRAZA_APPROVAL_PUSH_FCM_ENABLED", func(v string) {
		cfg.Approval.Push.FCM.Enabled = v == "true" || v == "1"
	})
	set("STRAZA_APPROVAL_PUSH_FCM_SERVICE_ACCOUNT_FILE", func(v string) { cfg.Approval.Push.FCM.ServiceAccountFile = v })
	set("STRAZA_APPROVAL_PUSH_FCM_PROJECT_ID", func(v string) { cfg.Approval.Push.FCM.ProjectID = v })
	// The public Firebase app-config trio (BYO-Firebase enroll advertisement).
	// validate's all-or-nothing rule sees the merged result, so a partial env
	// set fails boot exactly like a partial yaml set.
	set("STRAZA_APPROVAL_PUSH_FCM_APP_ID", func(v string) { cfg.Approval.Push.FCM.AppID = v })
	set("STRAZA_APPROVAL_PUSH_FCM_API_KEY", func(v string) { cfg.Approval.Push.FCM.APIKey = v })
	set("STRAZA_APPROVAL_PUSH_FCM_SENDER_ID", func(v string) { cfg.Approval.Push.FCM.SenderID = v })
	// Hosted push relay: enabling is the explicit
	// act (only "true"/"1", the FCM fail-safe idiom); tokenFile custody
	// mirrors vapidKeyFile (minted anonymously at first boot when absent).
	set("STRAZA_APPROVAL_PUSH_RELAY_ENABLED", func(v string) {
		cfg.Approval.Push.Relay.Enabled = v == "true" || v == "1"
	})
	set("STRAZA_APPROVAL_PUSH_RELAY_URL", func(v string) { cfg.Approval.Push.Relay.URL = v })
	set("STRAZA_APPROVAL_PUSH_RELAY_TOKEN_FILE", func(v string) { cfg.Approval.Push.Relay.TokenFile = v })
	// WebPush (RFC 8030/8291/8292): the key-file path is the enable knob;
	// strazad mints the key there on first boot when the file is absent.
	set("STRAZA_APPROVAL_PUSH_WEBPUSH_VAPID_KEY_FILE", func(v string) { cfg.Approval.Push.WebPush.VAPIDKeyFile = v })
	set("STRAZA_APPROVAL_PUSH_WEBPUSH_CONTACT", func(v string) { cfg.Approval.Push.WebPush.Contact = v })
	// APNs (native iOS approver app): the .p8 key-file path is the enable
	// knob, mirroring vapidKeyFile with one asymmetry: the key is Apple-minted
	// and strazad NEVER creates it (an unreadable file fails boot).
	set("STRAZA_APPROVAL_PUSH_APNS_KEY_FILE", func(v string) { cfg.Approval.Push.APNS.KeyFile = v })
	set("STRAZA_APPROVAL_PUSH_APNS_KEY_ID", func(v string) { cfg.Approval.Push.APNS.KeyID = v })
	set("STRAZA_APPROVAL_PUSH_APNS_TEAM_ID", func(v string) { cfg.Approval.Push.APNS.TeamID = v })
	set("STRAZA_APPROVAL_PUSH_APNS_TOPIC", func(v string) { cfg.Approval.Push.APNS.Topic = v })
	set("STRAZA_APPROVAL_PUSH_APNS_ENVIRONMENT", func(v string) { cfg.Approval.Push.APNS.Environment = v })
	set("STRAZA_APPROVAL_PUSH_ALLOWED_HOSTS", func(v string) {
		cfg.Approval.Push.AllowedPushHosts = splitCSV(v)
	})
	set("STRAZA_APPROVAL_PUSH_TICKET_REMINDER_BEFORE", func(v string) {
		// An unparseable value must fail boot loudly (validate rejects the
		// negative sentinel), never fall back silently to the built-in default.
		d, err := time.ParseDuration(v)
		if err != nil {
			d = -1
		}
		cfg.Approval.Push.TicketReminderBefore = d
	})
	// Args preview is ON by default: only an explicit false/0
	// turns it off, so an unrelated env value can never silently disable it.
	set("STRAZA_APPROVAL_PREVIEW_ENABLED", func(v string) {
		cfg.Approval.Preview.Enabled = v != "false" && v != "0"
	})
}

// applyEnvOAuth binds the OAuth connect env faces.
func applyEnvOAuth(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	// GitHub reference provider: the common case gets env-only setup;
	// other providers configure via file.
	setGithub := func(apply func(*OAuthProvider, string)) func(string) {
		return func(v string) {
			if cfg.OAuth.Providers == nil {
				cfg.OAuth.Providers = map[string]OAuthProvider{}
			}
			gh := cfg.OAuth.Providers["github"]
			apply(&gh, v)
			cfg.OAuth.Providers["github"] = gh
		}
	}
	set("STRAZA_OAUTH_GITHUB_CLIENT_ID", setGithub(func(p *OAuthProvider, v string) { p.ClientID = v }))
	set("STRAZA_OAUTH_GITHUB_CLIENT_SECRET", setGithub(func(p *OAuthProvider, v string) { p.ClientSecret = v }))
	// File sibling: see the Slack *_FILE comment above for why this exists.
	set("STRAZA_OAUTH_GITHUB_CLIENT_SECRET_FILE", setGithub(func(p *OAuthProvider, v string) { p.ClientSecretFile = v }))
}

// splitCSV parses a comma-separated env value into a trimmed, non-empty list
// (an empty or all-blank value yields nil, leaving any file-configured list in
// place; set semantics match the other env knobs).
func splitCSV(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func applyFlags(cfg *Config, f Overrides) {
	if f.DataDir != nil {
		cfg.DataDir = *f.DataDir
	}
	if f.Listen != nil {
		cfg.Server.Listen = *f.Listen
	}
	if f.LogLevel != nil {
		cfg.Log.Level = *f.LogLevel
	}
	if f.StoreDSN != nil {
		cfg.Store.DSN = *f.StoreDSN
	}
}
