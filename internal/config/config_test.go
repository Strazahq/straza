package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func noEnv(string) string { return "" }

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "straza.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsStandalone(t *testing.T) {
	cfg, err := Loader{Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profile != ProfileStandalone {
		t.Errorf("profile = %q, want standalone", cfg.Profile)
	}
	if cfg.Store.Driver != DriverSQLite {
		t.Errorf("store driver = %q, want sqlite", cfg.Store.Driver)
	}
	if !cfg.Events.Embedded {
		t.Error("events.embedded = false, want true")
	}
	if cfg.Governance.OfflineGraceTTL != 15*time.Minute {
		t.Errorf("grace TTL = %s, want 15m", cfg.Governance.OfflineGraceTTL)
	}
	if cfg.Governance.LocalToolDefault != EffectAllow {
		t.Errorf("localToolDefault = %q, want allow", cfg.Governance.LocalToolDefault)
	}
	if !cfg.Apps.AllowLoopbackUpstreams {
		t.Error("standalone: apps.allowLoopbackUpstreams = false, want true")
	}
	if cfg.Governance.AuditBackpressure != BackpressureDrop {
		t.Errorf("auditBackpressure = %q, want drop-with-counter", cfg.Governance.AuditBackpressure)
	}
	if cfg.Governance.MinAttestation != AttestationNone {
		t.Errorf("minAttestation = %q, want none", cfg.Governance.MinAttestation)
	}
	if got := cfg.SQLitePath(); got != filepath.Join("data", "straza.db") {
		t.Errorf("SQLitePath = %q", got)
	}
}

// TestSCIMDefaultsExplicitOnly pins the revision-8 posture: nothing implicit
// by default. The group-name prefix convention
// ships disabled (empty prefix) and the IdM may never mint roles; a config
// file opts back into either, verbatim.
func TestSCIMDefaultsExplicitOnly(t *testing.T) {
	// exposeControlPlaneRoles is retired (all roles exposed, the
	// IdM masters membership everywhere): a leftover key, either value,
	// boots with one ignored-with-notice line and changes nothing.
	path := writeFile(t, "scim:\n  exposeControlPlaneRoles: false\n")
	cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load with retired key: %v", err)
	}
	var noticed bool
	for _, n := range cfg.Notices {
		if strings.Contains(n, "scim.exposeControlPlaneRoles") && strings.Contains(n, "ignored") {
			noticed = true
		}
	}
	if !noticed {
		t.Errorf("retired exposeControlPlaneRoles key must boot with an ignored notice, notices = %v", cfg.Notices)
	}

	// Unified role model tombstones: the retired keys fail validation
	// loudly (fail closed on upgrade, never silent-ignore).
	for _, body := range []string{
		"scim:\n  groupRolePrefix: \"straza-\"\n",
		"scim:\n  groupRoleMap: {\"straza-dev\": \"dev\"}\n",
		"scim:\n  autoCreateRoles: false\n",
	} {
		path := writeFile(t, body)
		if _, err := (Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil {
			t.Errorf("retired key must fail validation, config: %q", body)
		} else if !strings.Contains(err.Error(), "unified role model") {
			t.Errorf("retired-key error must teach (mention the unified role model), got: %v", err)
		}
	}
}
func TestDefaultsEnterprise(t *testing.T) {
	profile := ProfileEnterprise
	dsn := "postgres://straza@localhost/straza"
	cfg, err := Loader{Getenv: noEnv, Flags: Overrides{Profile: &profile, StoreDSN: &dsn}}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Store.Driver != DriverPostgres {
		t.Errorf("store driver = %q, want postgres", cfg.Store.Driver)
	}
	if cfg.Apps.AllowLoopbackUpstreams {
		t.Error("enterprise: apps.allowLoopbackUpstreams = true, want false")
	}
	if cfg.Governance.OfflineGraceTTL != 0 {
		t.Errorf("grace TTL = %s, want 0 (D11)", cfg.Governance.OfflineGraceTTL)
	}
	if cfg.Governance.LocalToolDefault != EffectDeny {
		t.Errorf("localToolDefault = %q, want deny", cfg.Governance.LocalToolDefault)
	}
	if cfg.Governance.AuditBackpressure != BackpressureBlock {
		t.Errorf("auditBackpressure = %q, want block", cfg.Governance.AuditBackpressure)
	}
	if cfg.Governance.MinAttestation != AttestationManaged {
		t.Errorf("minAttestation = %q, want managed (P5.1 require-managed)", cfg.Governance.MinAttestation)
	}
}

func TestEnterpriseRequiresDSN(t *testing.T) {
	profile := ProfileEnterprise
	_, err := Loader{Getenv: noEnv, Flags: Overrides{Profile: &profile}}.Load()
	if err == nil || !strings.Contains(err.Error(), "store.dsn") {
		t.Fatalf("want store.dsn error, got %v", err)
	}
}

func TestFileOverridesDefaults(t *testing.T) {
	path := writeFile(t, "server:\n  listen: 0.0.0.0:9999\nlog:\n  level: debug\n")
	cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != "0.0.0.0:9999" {
		t.Errorf("listen = %q", cfg.Server.Listen)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("level = %q", cfg.Log.Level)
	}
	// Keys absent from the file keep their defaults.
	if cfg.Log.Format != "json" {
		t.Errorf("format = %q, want default json", cfg.Log.Format)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	path := writeFile(t, "log:\n  level: debug\n")
	env := envMap(map[string]string{"STRAZA_LOG_LEVEL": "warn"})
	cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: env}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("level = %q, want warn (env beats file)", cfg.Log.Level)
	}
}

func TestFlagsOverrideEnv(t *testing.T) {
	env := envMap(map[string]string{"STRAZA_LOG_LEVEL": "warn", "STRAZA_LISTEN": "1.2.3.4:1"})
	level := "error"
	cfg, err := Loader{Getenv: env, Flags: Overrides{LogLevel: &level}}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Log.Level != "error" {
		t.Errorf("level = %q, want error (flag beats env)", cfg.Log.Level)
	}
	if cfg.Server.Listen != "1.2.3.4:1" {
		t.Errorf("listen = %q, want env value to survive", cfg.Server.Listen)
	}
}

func TestProfileFromFileSetsDefaults(t *testing.T) {
	path := writeFile(t, "profile: enterprise\nstore:\n  dsn: postgres://x\n")
	cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Governance.LocalToolDefault != EffectDeny {
		t.Error("enterprise profile from file must select enterprise defaults")
	}
}

func TestProfileFlagBeatsFile(t *testing.T) {
	path := writeFile(t, "profile: enterprise\n")
	profile := ProfileStandalone
	cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv, Flags: Overrides{Profile: &profile}}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profile != ProfileStandalone {
		t.Errorf("profile = %q, want standalone (flag beats file)", cfg.Profile)
	}
	if cfg.Store.Driver != DriverSQLite {
		t.Errorf("driver = %q, want sqlite defaults from flag profile", cfg.Store.Driver)
	}
}

func TestExternalEventsURLViaEnv(t *testing.T) {
	env := envMap(map[string]string{"STRAZA_EVENTS_URL": "nats://mq:4222"})
	cfg, err := Loader{Getenv: env}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Events.Embedded || cfg.Events.URL != "nats://mq:4222" {
		t.Errorf("events = %+v, want external nats://mq:4222", cfg.Events)
	}
}

// TestContainerEnvKnobs covers the env vars container deployments (Helm,
// compose) rely on: the mounted-KEK path and the TLS material paths.
func TestContainerEnvKnobs(t *testing.T) {
	env := envMap(map[string]string{
		"STRAZA_SECRETS_KEK_FILE": "/etc/straza/secret.key",
		"STRAZA_TLS_CERT_FILE":    "/etc/straza/tls/tls.crt",
		"STRAZA_TLS_KEY_FILE":     "/etc/straza/tls/tls.key",
	})
	cfg, err := Loader{Getenv: env}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KEKFile() != "/etc/straza/secret.key" {
		t.Errorf("kekFile = %q, want /etc/straza/secret.key", cfg.KEKFile())
	}
	if !cfg.TLSEnabled() || cfg.Server.TLS.CertFile != "/etc/straza/tls/tls.crt" {
		t.Errorf("tls = %+v, want enabled via env", cfg.Server.TLS)
	}
}

// TestApprovalDefaults pins the always-on posture: the service is constructed
// in both profiles (no enable knob), Slack is opt-in, retention defaults to
// 30 days.
func TestApprovalDefaults(t *testing.T) {
	cfg, err := Loader{Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Approval.Retention != 30*24*time.Hour {
		t.Errorf("retention = %s, want 720h", cfg.Approval.Retention)
	}
	if cfg.Approval.Channels.Slack.Enabled {
		t.Error("slack must default disabled")
	}
	if cfg.Approval.Channels.Slack.IncludeJustification {
		t.Error("includeJustification must default false (privacy rule)")
	}
}

// TestApprovalSlackEnvKnobs covers the four container env overrides (an
// env-only deployment like the eval compose needs no config file at all) and
// that only an explicit "true"/"1" enables the channel.
func TestApprovalSlackEnvKnobs(t *testing.T) {
	env := envMap(map[string]string{
		"STRAZA_APPROVAL_SLACK_ENABLED":        "true",
		"STRAZA_APPROVAL_SLACK_BOT_TOKEN":      "xoxb-1",
		"STRAZA_APPROVAL_SLACK_SIGNING_SECRET": "shh",
		"STRAZA_APPROVAL_SLACK_CHANNEL":        "C123",
	})
	cfg, err := Loader{Getenv: env}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.Approval.Channels.Slack
	if !s.Enabled || s.BotToken != "xoxb-1" || s.SigningSecret != "shh" || s.Channel != "C123" {
		t.Errorf("slack config = %+v, want env-provided credentials", s)
	}

	// A typo'd ENABLED value stays OFF (fail-safe), and validation stays quiet
	// because the channel is simply disabled.
	env = envMap(map[string]string{"STRAZA_APPROVAL_SLACK_ENABLED": "yes-please"})
	cfg, err = Loader{Getenv: env}.Load()
	if err != nil {
		t.Fatalf("Load with bad ENABLED: %v", err)
	}
	if cfg.Approval.Channels.Slack.Enabled {
		t.Error("non-true ENABLED value must not enable the channel")
	}
}

// TestApprovalSlackValidation rejects an enabled channel missing its channel
// id and the both-inline-and-file token conflict.
func TestApprovalSlackValidation(t *testing.T) {
	missing := writeFile(t, "approval:\n  channels:\n    slack:\n      enabled: true\n      botToken: x\n      signingSecret: y\n")
	if _, err := (Loader{FilePath: missing, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil ||
		!strings.Contains(err.Error(), "channel is required") {
		t.Errorf("enabled slack without channel: want channel-required error, got %v", err)
	}
	both := writeFile(t, "approval:\n  channels:\n    slack:\n      botToken: x\n      botTokenFile: /f\n")
	if _, err := (Loader{FilePath: both, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil ||
		!strings.Contains(err.Error(), "not both") {
		t.Errorf("botToken + botTokenFile: want mutual-exclusion error, got %v", err)
	}
}

// TestOAuthProviders covers the OAuth connect config: env-only GitHub setup
// gets endpoint defaults; non-github providers must spell endpoints out;
// half-configured providers fail boot loudly.
func TestOAuthProviders(t *testing.T) {
	env := envMap(map[string]string{
		"STRAZA_OAUTH_GITHUB_CLIENT_ID":     "Iv1.abc",
		"STRAZA_OAUTH_GITHUB_CLIENT_SECRET": "cs-1",
	})
	cfg, err := Loader{Getenv: env}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gh := cfg.OAuth.Providers["github"]
	if gh.ClientID != "Iv1.abc" || gh.ClientSecret != "cs-1" {
		t.Errorf("github provider = %+v", gh)
	}
	if gh.AuthURL != "https://github.com/login/oauth/authorize" ||
		gh.TokenURL != "https://github.com/login/oauth/access_token" {
		t.Errorf("github endpoints not defaulted: %+v", gh)
	}

	// File-configured custom provider needs explicit endpoints.
	path := writeFile(t, `
oauth:
  providers:
    gitlab:
      clientId: glc
      clientSecret: gls
`)
	if _, err := (Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil {
		t.Error("provider without endpoints accepted")
	}

	path = writeFile(t, `
oauth:
  providers:
    github:
      clientSecret: cs-only
`)
	if _, err := (Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}).Load(); err == nil {
		t.Error("provider without clientId accepted")
	}
}

// TestMinAttestationEnvAndRank covers the attestation gate knob: env override plus
// the level ordering the checkin gate relies on (unknown ranks below none).
func TestMinAttestationEnvAndRank(t *testing.T) {
	env := envMap(map[string]string{"STRAZA_MIN_ATTESTATION": "advisory"})
	cfg, err := Loader{Getenv: env}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Governance.MinAttestation != AttestationAdvisory {
		t.Errorf("minAttestation = %q, want advisory (env)", cfg.Governance.MinAttestation)
	}
	if AttestationRank(AttestationNone) >= AttestationRank(AttestationAdvisory) ||
		AttestationRank(AttestationAdvisory) >= AttestationRank(AttestationManaged) {
		t.Error("attestation ranks must order none < advisory < managed")
	}
	if AttestationRank("tampered") != 0 {
		t.Error("unknown level must rank below none (fail closed)")
	}
}

// TestCatalogDefaults pins the gateway catalog knobs: policyFilter defaults ON
// in BOTH profiles (hide policy-denied tools), warnSize defaults to 100, and
// pageSize defaults to 200.
func TestCatalogDefaults(t *testing.T) {
	for _, profile := range []string{ProfileStandalone, ProfileEnterprise} {
		cfg := defaults(profile)
		if !cfg.Apps.Catalog.PolicyFilter {
			t.Errorf("%s: catalog.policyFilter = false, want true (hide policy-denied tools)", profile)
		}
		if cfg.Apps.Catalog.WarnSize != 100 {
			t.Errorf("%s: catalog.warnSize = %d, want 100", profile, cfg.Apps.Catalog.WarnSize)
		}
		if cfg.Apps.Catalog.PageSize != 200 {
			t.Errorf("%s: catalog.pageSize = %d, want 200", profile, cfg.Apps.Catalog.PageSize)
		}
	}
}

// TestCatalogConfigFromFile: the knobs are individually overridable (a false
// policyFilter, a -1 warnSize, and a 0 pageSize survive the merge into the
// true/100/200 defaults).
func TestCatalogConfigFromFile(t *testing.T) {
	path := writeFile(t, "apps:\n  catalog:\n    policyFilter: false\n    warnSize: -1\n    pageSize: 0\n")
	cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Apps.Catalog.PolicyFilter {
		t.Error("policyFilter: file value false must win over the default true")
	}
	if cfg.Apps.Catalog.WarnSize != -1 {
		t.Errorf("warnSize = %d, want -1 (disabled)", cfg.Apps.Catalog.WarnSize)
	}
	if cfg.Apps.Catalog.PageSize != 0 {
		t.Errorf("pageSize = %d, want 0 (file value must win over the default 200)", cfg.Apps.Catalog.PageSize)
	}
}

func TestExplicitMissingFileFails(t *testing.T) {
	_, err := Loader{FilePath: filepath.Join(t.TempDir(), "nope.yaml"), ExplicitFile: true, Getenv: noEnv}.Load()
	if err == nil {
		t.Fatal("want error for explicitly named missing config file")
	}
}

func TestMissingProbeFileIsFine(t *testing.T) {
	_, err := Loader{FilePath: filepath.Join(t.TempDir(), "nope.yaml"), Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("probe file absence must not error: %v", err)
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"bad profile", func(c *Config) { c.Profile = "cloud" }, "unknown profile"},
		{"bad level", func(c *Config) { c.Log.Level = "loud" }, "log level"},
		{"bad format", func(c *Config) { c.Log.Format = "xml" }, "log format"},
		{"bad driver", func(c *Config) { c.Store.Driver = "oracle" }, "store driver"},
		{"external events without url", func(c *Config) { c.Events.Embedded = false }, "events.url"},
		{"bad effect", func(c *Config) { c.Governance.LocalToolDefault = "maybe" }, "localToolDefault"},
		{"bad backpressure", func(c *Config) { c.Governance.AuditBackpressure = "spill" }, "auditBackpressure"},
		{"negative grace", func(c *Config) { c.Governance.OfflineGraceTTL = -time.Second }, "offlineGraceTTL"},
		{"negative session lifetime", func(c *Config) { c.Governance.SessionMaxLifetime = -time.Second }, "sessionMaxLifetime"},
		{"bad min attestation", func(c *Config) { c.Governance.MinAttestation = "verified" }, "minAttestation"},
		{"tls cert without key", func(c *Config) { c.Server.TLS.CertFile = "tls.crt" }, "server.tls"},
		{"tls key without cert", func(c *Config) { c.Server.TLS.KeyFile = "tls.key" }, "server.tls"},
		{"sink without name", func(c *Config) { c.Sinks = []Sink{{Type: SinkFile, Path: "x"}} }, "name is required"},
		{"sink duplicate name", func(c *Config) {
			c.Sinks = []Sink{{Name: "a", Type: SinkFile, Path: "x"}, {Name: "a", Type: SinkFile, Path: "y"}}
		}, "duplicate name"},
		{"sink unknown type", func(c *Config) { c.Sinks = []Sink{{Name: "a", Type: "kafka"}} }, "unknown type"},
		{"webhook sink without url", func(c *Config) { c.Sinks = []Sink{{Name: "a", Type: SinkWebhook}} }, "requires url"},
		{"webhook sink with both secrets", func(c *Config) {
			c.Sinks = []Sink{{Name: "a", Type: SinkWebhook, URL: "https://x", Secret: "s", SecretFile: "f"}}
		}, "not both"},
		{"file sink without path", func(c *Config) { c.Sinks = []Sink{{Name: "a", Type: SinkFile}} }, "requires path"},
		{"sentinel zero warn threshold", func(c *Config) { c.Governance.Sentinel.DenyBurstWarn = 0 }, "denyBurstWarn"},
		{"sentinel critical below warn", func(c *Config) { c.Governance.Sentinel.DenyBurstCritical = 3 }, "denyBurstCritical"},
		{"sentinel negative window", func(c *Config) { c.Governance.Sentinel.VariantWindow = -time.Second }, "windows"},
		{"sentinel zero baseline floor", func(c *Config) { c.Governance.Sentinel.BaselineMinEvents = 0 }, "baselineMinEvents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaults(ProfileStandalone)
			tc.mut(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// TestSessionMaxLifetime pins the absolute session lifetime: 12h in both
// profiles, the same default when the file leaves the key unset, the set
// value otherwise, and an unparseable env value fails boot instead of
// falling back silently.
func TestSessionMaxLifetime(t *testing.T) {
	for _, profile := range []string{ProfileStandalone, ProfileEnterprise} {
		if cfg := defaults(profile); cfg.Governance.SessionMaxLifetime != 12*time.Hour {
			t.Errorf("%s: sessionMaxLifetime default = %s, want 12h", profile, cfg.Governance.SessionMaxLifetime)
		}
	}
	cases := []struct {
		name string
		env  string
		want time.Duration
		err  string
	}{
		{"unset keeps the default", "", 12 * time.Hour, ""},
		{"set value wins", "90m", 90 * time.Minute, ""},
		{"unparseable fails boot", "soon", 0, "sessionMaxLifetime"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Loader{Getenv: envMap(map[string]string{"STRAZA_SESSION_MAX_LIFETIME": tc.env})}.Load()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want error containing %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.EffectiveSessionMaxLifetime(); got != tc.want {
				t.Errorf("EffectiveSessionMaxLifetime = %s, want %s", got, tc.want)
			}
		})
	}
	var zero Config
	if got := zero.EffectiveSessionMaxLifetime(); got != 12*time.Hour {
		t.Errorf("zero config EffectiveSessionMaxLifetime = %s, want 12h", got)
	}
}

// TestSentinelDefaults pins the sentinel contract: it is OPTIONAL and
// ships disabled in BOTH profiles (an eval stack enables it explicitly), with
// sane thresholds pre-filled so enabling is a one-key change.
func TestSentinelDefaults(t *testing.T) {
	for _, profile := range []string{ProfileStandalone, ProfileEnterprise} {
		cfg := defaults(profile)
		s := cfg.Governance.Sentinel
		if s.Enabled {
			t.Errorf("%s: sentinel enabled by default, must be opt-in", profile)
		}
		if s.DenyBurstWarn != 5 || s.DenyBurstCritical != 10 || s.DenyBurstWindow != time.Minute {
			t.Errorf("%s: deny-burst defaults = %d/%d/%s, want 5/10/1m", profile, s.DenyBurstWarn, s.DenyBurstCritical, s.DenyBurstWindow)
		}
		if s.VariantWindow != 10*time.Minute || s.WriteExecWindow != 30*time.Minute {
			t.Errorf("%s: windows = %s/%s, want 10m/30m", profile, s.VariantWindow, s.WriteExecWindow)
		}
		if s.BaselineMinEvents != 50 {
			t.Errorf("%s: baselineMinEvents = %d, want 50", profile, s.BaselineMinEvents)
		}
		if err := s.validate(); err != nil {
			t.Errorf("%s: sentinel defaults must validate: %v", profile, err)
		}
	}
}

// TestSentinelConfigFromFile: enabling + overriding one knob keeps the other
// defaults (yaml merges into the pre-populated struct).
func TestSentinelConfigFromFile(t *testing.T) {
	path := writeFile(t, "governance:\n  sentinel:\n    enabled: true\n    denyBurstWarn: 3\n    variantWindow: 5m\n")
	cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.Governance.Sentinel
	if !s.Enabled {
		t.Error("sentinel not enabled from file")
	}
	if s.DenyBurstWarn != 3 || s.VariantWindow != 5*time.Minute {
		t.Errorf("overrides lost: warn=%d variantWindow=%s", s.DenyBurstWarn, s.VariantWindow)
	}
	if s.DenyBurstCritical != 10 || s.WriteExecWindow != 30*time.Minute || s.BaselineMinEvents != 50 {
		t.Errorf("defaults lost: critical=%d writeExec=%s baseline=%d", s.DenyBurstCritical, s.WriteExecWindow, s.BaselineMinEvents)
	}
}
