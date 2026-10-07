package config

import (
	"strings"
	"testing"
)

// TestApproverTLSValidation pins the all-or-none + https-only contract for
// the dedicated approver listener.
func TestApproverTLSValidation(t *testing.T) {
	base := func() Config {
		c := defaults(ProfileStandalone)
		c.Server.ApproverTLS = ApproverTLS{
			Listen: "0.0.0.0:8443", CertFile: "cert.pem", KeyFile: "key.pem",
			PublicURL: "https://127.0.0.1:8443",
		}
		return c
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("complete approverTLS rejected: %v", err)
	}

	partial := base()
	partial.Server.ApproverTLS.KeyFile = ""
	if err := partial.Validate(); err == nil || !strings.Contains(err.Error(), "approverTLS") {
		t.Errorf("partial approverTLS: err = %v, want approverTLS all-or-none rejection", err)
	}

	plaintext := base()
	plaintext.Server.ApproverTLS.PublicURL = "http://127.0.0.1:8443"
	if err := plaintext.Validate(); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("http publicUrl: err = %v, want https-only rejection", err)
	}

	none := defaults(ProfileStandalone)
	if err := none.Validate(); err != nil {
		t.Errorf("unset approverTLS must stay valid: %v", err)
	}
}

// TestApproverPublicURLValidation pins the ingress-terminated knob: https-only
// (same reason as the listener's publicUrl: approver devices are https+pin
// only), a bare base URL, empty is fine, and it composes with approverTLS
// rather than replacing it.
func TestApproverPublicURLValidation(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr string // substring of the expected error; "" = must validate
	}{
		{"empty", "", ""},
		{"https host", "https://approve.example.com", ""},
		{"https host trailing slash", "https://approve.example.com/", ""},
		{"https host and port", "https://approve.example.com:8443", ""},
		{"plaintext", "http://approve.example.com", "https"},
		{"scheme-less", "approve.example.com", "https"},
		{"path", "https://approve.example.com/v1/approver", "base URL"},
		{"query", "https://approve.example.com/?q=1", "base URL"},
		{"fragment", "https://approve.example.com/#pin", "base URL"},
		{"no host", "https://", "host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := defaults(ProfileStandalone)
			c.Server.ApproverPublicURL = tc.url
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("approverPublicUrl %q rejected: %v", tc.url, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("approverPublicUrl %q: err = %v, want an error naming %q", tc.url, err, tc.wantErr)
			}
		})
	}

	// It composes: a complete approverTLS block alongside the ingress knob is
	// valid config; deciding which one the QR names is enrollServers' job,
	// not validation's.
	both := defaults(ProfileStandalone)
	both.Server.ApproverTLS = ApproverTLS{
		Listen: "0.0.0.0:8443", CertFile: "cert.pem", KeyFile: "key.pem",
		PublicURL: "https://127.0.0.1:8443",
	}
	both.Server.ApproverPublicURL = "https://approve.example.com"
	if err := both.Validate(); err != nil {
		t.Errorf("approverPublicUrl + complete approverTLS rejected: %v", err)
	}

	env := defaults(ProfileStandalone)
	applyEnv(&env, func(k string) string {
		if k == "STRAZA_APPROVER_PUBLIC_URL" {
			return "https://approve.example.com"
		}
		return ""
	})
	if env.Server.ApproverPublicURL != "https://approve.example.com" {
		t.Errorf("STRAZA_APPROVER_PUBLIC_URL did not apply, got %q", env.Server.ApproverPublicURL)
	}
}

// TestApproverPublicURLFromFile pins the YAML key an operator actually types.
// The struct tag is the entire contract for a config-file deployment, so a
// misspelled tag would be silently ignored and reproduce the very failure this
// knob exists to fix: an enroll QR naming the internal publicUrl.
func TestApproverPublicURLFromFile(t *testing.T) {
	path := writeFile(t, `profile: enterprise
store:
  dsn: postgres://straza@db/straza
server:
  publicUrl: https://straza.example.com
  approverPublicUrl: https://approve.example.com
`)
	cfg, err := Loader{FilePath: path, ExplicitFile: true, Getenv: noEnv}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.ApproverPublicURL != "https://approve.example.com" {
		t.Errorf("server.approverPublicUrl from file = %q, want https://approve.example.com (yaml tag wrong?)", cfg.Server.ApproverPublicURL)
	}
	if cfg.Server.PublicURL != "https://straza.example.com" {
		t.Errorf("server.publicUrl = %q, want it left alone", cfg.Server.PublicURL)
	}
}

// TestApproverTLSAutoMint pins the auto-mint contract: profile defaults
// (standalone on, enterprise off, bare structs off), and validation that
// tolerates a partial block exactly when auto-mint will fill the rest.
func TestApproverTLSAutoMint(t *testing.T) {
	if !defaults(ProfileStandalone).Server.ApproverTLS.AutoMintEnabled() {
		t.Error("standalone default: autoMint should be on")
	}
	if defaults(ProfileEnterprise).Server.ApproverTLS.AutoMintEnabled() {
		t.Error("enterprise default: autoMint should be off")
	}
	if (Config{}).Server.ApproverTLS.AutoMintEnabled() {
		t.Error("bare config: autoMint should be off (nil knob)")
	}

	on := func(mutate func(*Config)) error {
		c := defaults(ProfileStandalone) // carries autoMint: on
		mutate(&c)
		return c.Validate()
	}

	// Partial blocks are fine when auto-mint fills the rest…
	if err := on(func(c *Config) { c.Server.ApproverTLS.PublicURL = "https://phone.example:8443" }); err != nil {
		t.Errorf("autoMint + publicUrl only: %v", err)
	}
	if err := on(func(c *Config) { c.Server.ApproverTLS.Listen = "0.0.0.0:9443" }); err != nil {
		t.Errorf("autoMint + listen only: %v", err)
	}
	// …but a half cert pair is always an authoring mistake…
	if err := on(func(c *Config) { c.Server.ApproverTLS.CertFile = "cert.pem" }); err == nil ||
		!strings.Contains(err.Error(), "keyFile") {
		t.Errorf("autoMint + certFile only: err = %v, want pair rejection", err)
	}
	// …and https-only still holds for an explicit publicUrl.
	if err := on(func(c *Config) { c.Server.ApproverTLS.PublicURL = "http://phone.example:8443" }); err == nil ||
		!strings.Contains(err.Error(), "https") {
		t.Errorf("autoMint + http publicUrl: err = %v, want https-only rejection", err)
	}

	// With auto-mint off (enterprise default), partial stays rejected and the
	// error now names the knob that would fix it.
	ent := defaults(ProfileEnterprise)
	ent.Store.DSN = "postgres://x" // satisfy the unrelated enterprise store check
	ent.Server.ApproverTLS.PublicURL = "https://phone.example:8443"
	if err := ent.Validate(); err == nil || !strings.Contains(err.Error(), "autoMint") {
		t.Errorf("enterprise partial: err = %v, want quad rejection naming autoMint", err)
	}

	// Env override flips the knob both ways.
	envOn := defaults(ProfileEnterprise)
	applyEnv(&envOn, func(k string) string {
		if k == "STRAZA_APPROVER_TLS_AUTO_MINT" {
			return "true"
		}
		return ""
	})
	if !envOn.Server.ApproverTLS.AutoMintEnabled() {
		t.Error("STRAZA_APPROVER_TLS_AUTO_MINT=true did not enable")
	}
	envOff := defaults(ProfileStandalone)
	applyEnv(&envOff, func(k string) string {
		if k == "STRAZA_APPROVER_TLS_AUTO_MINT" {
			return "false"
		}
		return ""
	})
	if envOff.Server.ApproverTLS.AutoMintEnabled() {
		t.Error("STRAZA_APPROVER_TLS_AUTO_MINT=false did not disable")
	}
}

// TestHardeningKnobs pins the public-surface hardening defaults (body cap,
// login throttle, approver per-IP throttle, metrics token) and their
// validation: zero disables, negative fails boot.
func TestHardeningKnobs(t *testing.T) {
	for _, profile := range []string{ProfileStandalone, ProfileEnterprise} {
		d := defaults(profile)
		if d.Server.MaxBodyBytes != 1<<20 {
			t.Errorf("%s maxBodyBytes default = %d, want 1 MiB", profile, d.Server.MaxBodyBytes)
		}
		if d.Server.LoginPerIPRPS != 2 {
			t.Errorf("%s loginPerIPRPS default = %v, want 2", profile, d.Server.LoginPerIPRPS)
		}
		if d.Server.ApproverTLS.PerIPRPS != 10 {
			t.Errorf("%s approverTLS.perIPRPS default = %v, want 10", profile, d.Server.ApproverTLS.PerIPRPS)
		}
		if d.Server.MetricsToken != "" {
			t.Errorf("%s metricsToken default = %q, want empty (open on private networks)", profile, d.Server.MetricsToken)
		}
	}

	for name, mutate := range map[string]func(*Config){
		"negative maxBodyBytes":  func(c *Config) { c.Server.MaxBodyBytes = -1 },
		"negative loginPerIPRPS": func(c *Config) { c.Server.LoginPerIPRPS = -1 },
		"negative perIPRPS":      func(c *Config) { c.Server.ApproverTLS.PerIPRPS = -1 },
	} {
		c := defaults(ProfileStandalone)
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: validation accepted it", name)
		}
	}

	zeros := defaults(ProfileStandalone)
	zeros.Server.MaxBodyBytes = 0
	zeros.Server.LoginPerIPRPS = 0
	zeros.Server.ApproverTLS.PerIPRPS = 0
	if err := zeros.Validate(); err != nil {
		t.Errorf("zero knobs (disabled) rejected: %v", err)
	}

	env := defaults(ProfileStandalone)
	applyEnv(&env, func(k string) string {
		if k == "STRAZA_METRICS_TOKEN" {
			return "scrape-me"
		}
		return ""
	})
	if env.Server.MetricsToken != "scrape-me" {
		t.Error("STRAZA_METRICS_TOKEN did not apply")
	}
}
