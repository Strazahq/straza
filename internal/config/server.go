package config

// Server section: the API/gateway listener, native TLS, the dedicated
// approver listener and the approver public URL. Types, validation.
// The env faces stay in config.go's applyEnv (the knob tripwire scans
// that file).

import (
	"fmt"
)

// Server configures the HTTP listener.
type Server struct {
	// Listen is the address for the API/gateway listener, e.g. "127.0.0.1:8420".
	Listen string `yaml:"listen"`
	// PublicURL is the externally reachable base URL. It is the session-token
	// issuer and the base of the built-in OIDC discovery documents.
	PublicURL string `yaml:"publicUrl"`
	// ProjectName is the human label for THIS deployment, shown by approver
	// apps that hold several ("Acme Production"). Display only: clients key
	// on the persisted project id, never the name. Unset = a stable
	// "straza-<4hex>" default derived from the project id.
	ProjectName string `yaml:"projectName"`
	// TLS enables native HTTPS on the listener.
	TLS TLS `yaml:"tls"`
	// MaxBodyBytes caps every request body (bytes). Bulk lanes with their
	// own tighter contracts (/mcp, /v1/audit/batch at 4 MiB) are exempt.
	// 0 disables; the built-in default is 1 MiB.
	MaxBodyBytes int64 `yaml:"maxBodyBytes"`
	// MetricsToken, when set, gates GET /metrics behind `Authorization:
	// Bearer <token>` (Prometheus: bearer_token). Empty keeps /metrics
	// open, acceptable only on private networks (the labels are
	// deliberately low-cardinality and content-free either way).
	MetricsToken string `yaml:"metricsToken"`
	// LoginPerIPRPS throttles the profile's unauthenticated credential
	// route per client IP: the standalone password submit (POST
	// /oidc/device) or the enterprise NHI token endpoint (POST
	// /oidc/token). 0 disables; default 2/s. Behind a reverse proxy all
	// clients share the proxy's IP; raise or disable there.
	LoginPerIPRPS float64 `yaml:"loginPerIPRPS"`
	// ApproverTLS opens a SECOND, https-only listener serving ONLY the
	// mobile-approver surface (/v1/approver/* + /readyz), the zero-disruption
	// answer to "the phone is https+pin only but the main listener must stay
	// plaintext" (eval stacks, ingress-terminated deployments where the pin
	// cannot be minted). When set, the enroll QR names its PublicURL and pins
	// its certificate.
	ApproverTLS ApproverTLS `yaml:"approverTLS"`
	// ApproverPublicURL is the https base URL where approver devices reach the
	// approver surface when a fronting proxy/ingress terminates TLS for it:
	// the k8s shape, where the ingress serves /v1/approver/* on its own public
	// host while publicUrl stays private (VPN-only). It is used in enroll
	// payloads; the dedicated approverTLS listener's publicUrl wins when that
	// listener is configured. Empty means "the phone dials publicUrl".
	ApproverPublicURL string `yaml:"approverPublicUrl"`
}

// ApproverTLS configures the dedicated approver listener. Bring-your-own is
// all four fields together or none: a partial config is an authoring mistake
// and fails boot, unless autoMint is on, in which case strazad fills
// whatever was left unset (first-boot self-signed pair persisted under
// <dataDir>/approver-tls, listen :8443, best-effort LAN publicUrl). The same
// no-insecure-skip rule applies: the phone pins the SPKI from the QR, so a
// self-signed certificate is sufficient and rotation re-enrolls.
type ApproverTLS struct {
	// Listen is the bind address, e.g. "0.0.0.0:8443".
	Listen string `yaml:"listen"`
	// CertFile / KeyFile are the PEM pair this listener serves (may be the
	// same files as server.tls or a dedicated pair).
	CertFile string `yaml:"certFile"`
	KeyFile  string `yaml:"keyFile"`
	// PublicURL is the https URL the PHONE dials; it becomes the enroll
	// QR's server list verbatim (there is no request-host fallback), so it
	// must be reachable from the device (tunnel/adb chain, tailnet, or LAN).
	PublicURL string `yaml:"publicUrl"`
	// AutoMint lets strazad complete this block itself so a virgin
	// standalone boot serves the approver surface with zero TLS config.
	// Explicitly set fields always win; auto-mint only fills the gaps.
	// Profile defaults: standalone on, enterprise off. nil (a bare struct
	// that never went through the Loader) means off.
	AutoMint *bool `yaml:"autoMint"`
	// PerIPRPS throttles the dedicated approver listener per client IP:
	// it is the one surface designed to face hostile networks, so the
	// limit is on by default (10/s; a phone enrolls once and polls
	// rarely). 0 disables. The key is the transport peer address, never
	// X-Forwarded-For.
	PerIPRPS float64 `yaml:"perIPRPS"`
}

// AutoMintEnabled reports whether the auto-mint path may fill unset
// approverTLS fields at boot.
func (a ApproverTLS) AutoMintEnabled() bool { return a.AutoMint != nil && *a.AutoMint }

// TLS configures native TLS for the API/gateway listener. Both files
// set = HTTPS with a TLS 1.2 floor (Go's modern cipher defaults); both empty
// = plaintext, acceptable only on loopback (standalone) or behind a
// TLS-terminating ingress/LB (enterprise; strazad warns loudly at boot).
// There is deliberately no insecure-skip-verify anywhere in Straza.
type TLS struct {
	// CertFile is the PEM server certificate (leaf + intermediates).
	CertFile string `yaml:"certFile"`
	// KeyFile is the PEM private key.
	KeyFile string `yaml:"keyFile"`
}

// validate holds the server section's checks, in Validate's first-error order.
func (s Server) validate() error {
	if (s.TLS.CertFile == "") != (s.TLS.KeyFile == "") {
		return fmt.Errorf("server.tls requires both certFile and keyFile (or neither)")
	}
	// server.publicUrl is consumed VERBATIM as the session-token issuer and
	// the built-in OIDC discovery base (authn.NewTokenService, authn.NewIssuer),
	// so it is base-only WITHOUT the trailing-slash tolerance the approver
	// URLs get: a slash here mints tokens under a different issuer identity
	// and turns the discovery endpoints into //-paths ServeMux answers with
	// method-dropping 301s. Rejected, never normalized.
	if s.PublicURL == "" {
		return fmt.Errorf("server.publicUrl is required: it is the session-token issuer and the base of every link strazad hands out (enroll QRs, discovery documents, OAuth callbacks)")
	}
	if err := checkBaseURL("server.publicUrl", s.PublicURL, false, false); err != nil {
		return err
	}
	if s.MaxBodyBytes < 0 {
		return fmt.Errorf("server.maxBodyBytes must be >= 0 (0 disables the cap)")
	}
	if s.LoginPerIPRPS < 0 {
		return fmt.Errorf("server.loginPerIPRPS must be >= 0 (0 disables the throttle)")
	}
	if s.ApproverTLS.PerIPRPS < 0 {
		return fmt.Errorf("server.approverTLS.perIPRPS must be >= 0 (0 disables the throttle)")
	}
	if a := s.ApproverTLS; a.Listen != "" || a.CertFile != "" || a.KeyFile != "" || a.PublicURL != "" {
		if (a.CertFile == "") != (a.KeyFile == "") {
			return fmt.Errorf("server.approverTLS.certFile and keyFile go together (or both empty)")
		}
		// https-only base URL (trailing slash fine; the enroll QR builder
		// trims it): the value becomes the QR's server list verbatim.
		if a.PublicURL != "" {
			if err := checkBaseURL("server.approverTLS.publicUrl", a.PublicURL, true, true); err != nil {
				return err
			}
		}
		if !a.AutoMintEnabled() && (a.Listen == "" || a.CertFile == "" || a.KeyFile == "" || a.PublicURL == "") {
			return fmt.Errorf("server.approverTLS requires listen, certFile, keyFile, and publicUrl together (or none). Alternatively set server.approverTLS.autoMint: true and strazad mints/fills the rest at boot")
		}
	}
	if p := s.ApproverPublicURL; p != "" {
		if err := checkBaseURL("server.approverPublicUrl", p, true, true); err != nil {
			return err
		}
	}
	return nil
}

// applyEnvServer binds the server section's env faces; applyEnv
// (config.go) sequences the binders, and each face writes a field no other
// face writes.
func applyEnvServer(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	set("STRAZA_LISTEN", func(v string) { cfg.Server.Listen = v })
	set("STRAZA_PUBLIC_URL", func(v string) { cfg.Server.PublicURL = v })
	set("STRAZA_PROJECT_NAME", func(v string) { cfg.Server.ProjectName = v })
	set("STRAZA_TLS_CERT_FILE", func(v string) { cfg.Server.TLS.CertFile = v })
	set("STRAZA_TLS_KEY_FILE", func(v string) { cfg.Server.TLS.KeyFile = v })
	set("STRAZA_APPROVER_TLS_LISTEN", func(v string) { cfg.Server.ApproverTLS.Listen = v })
	set("STRAZA_APPROVER_TLS_CERT_FILE", func(v string) { cfg.Server.ApproverTLS.CertFile = v })
	set("STRAZA_APPROVER_TLS_KEY_FILE", func(v string) { cfg.Server.ApproverTLS.KeyFile = v })
	set("STRAZA_APPROVER_TLS_PUBLIC_URL", func(v string) { cfg.Server.ApproverTLS.PublicURL = v })
	set("STRAZA_APPROVER_TLS_AUTO_MINT", func(v string) { b := v == "true"; cfg.Server.ApproverTLS.AutoMint = &b })
	set("STRAZA_APPROVER_PUBLIC_URL", func(v string) { cfg.Server.ApproverPublicURL = v })
	set("STRAZA_METRICS_TOKEN", func(v string) { cfg.Server.MetricsToken = v })
}
