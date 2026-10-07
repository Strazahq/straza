package config

import (
	"strings"
	"testing"
)

// TestPublicURLValidation pins the shape contract for server.publicUrl: it is
// the session-token issuer and the base of the built-in OIDC discovery
// document, consumed VERBATIM (authn.NewTokenService, authn.NewIssuer,
// /v1/idp), so a trailing slash is rejected rather than normalized:
// normalizing would silently rotate the deployment's issuer identity, and the
// double-slash endpoints a slash produces turn device-flow POSTs into 301s.
func TestPublicURLValidation(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr string // substring of the expected error; "" = must validate
	}{
		{"default loopback", "http://127.0.0.1:8420", ""},
		{"https host", "https://straza.example.com", ""},
		{"https host and port", "https://straza.example.com:8420", ""},
		{"in-cluster dns", "http://straza-straza.straza.svc:8420", ""},
		// url.Parse lowercases the scheme; the helm chart's render-time
		// mirror (deploy/helm .. straza.publicUrl) tolerates scheme case for
		// exactly this reason; this case pins the strazad half of that pact.
		{"uppercase scheme", "HTTP://straza.example.com", ""},
		{"empty", "", "server.publicUrl is required"},
		{"trailing slash", "https://straza.example.com/", "trailing slash"},
		{"path", "https://straza.example.com/straza", "base URL"},
		{"query", "https://straza.example.com?x=1", "base URL"},
		{"fragment", "https://straza.example.com#f", "base URL"},
		{"scheme-less", "straza.example.com", "http:// or https://"},
		{"garbage scheme", "gopher://straza.example.com", "http:// or https://"},
		{"embedded credentials", "https://admin:hunter2@straza.example.com", "credentials"},
		{"no host", "https://", "host"},
		{"whitespace", "https://straza .example.com", "whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := defaults(ProfileStandalone)
			c.Server.PublicURL = tc.url
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("publicUrl %q rejected: %v", tc.url, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("publicUrl %q: err = %v, want an error naming %q", tc.url, err, tc.wantErr)
			}
			if tc.wantErr != "" && err != nil && !strings.Contains(err.Error(), "server.publicUrl") {
				t.Fatalf("publicUrl %q: error %v does not name the field path", tc.url, err)
			}
		})
	}
}

// TestOIDCIssuerValidation pins the shape contract for oidc.issuer: the string
// must match the IdP's issuer byte-for-byte, so paths (Keycloak realms) and a
// trailing slash (Auth0) pass VERBATIM (never normalized), while query and
// fragment are rejected (OpenID Connect Discovery forbids both) along with
// embedded credentials and non-http schemes.
func TestOIDCIssuerValidation(t *testing.T) {
	cases := []struct {
		name    string
		issuer  string
		wantErr string
	}{
		{"empty means built-in issuer", "", ""},
		{"bare host", "https://idp.example.com", ""},
		{"keycloak realm path", "https://auth.example.com/realms/straza", ""},
		{"auth0 trailing slash", "https://tenant.auth0.com/", ""},
		{"http dev idp", "http://keycloak:8080/realms/dev", ""},
		{"scheme-less", "auth.example.com/realms/straza", "http:// or https://"},
		{"query", "https://idp.example.com?tenant=x", "query"},
		{"fragment", "https://idp.example.com/realms/x#top", "fragment"},
		{"embedded credentials", "https://svc:token@idp.example.com", "credentials"},
		{"whitespace", "https://idp.example.com/realms/my realm", "whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := defaults(ProfileStandalone)
			c.OIDC.Issuer = tc.issuer
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("issuer %q rejected: %v", tc.issuer, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("issuer %q: err = %v, want an error naming %q", tc.issuer, err, tc.wantErr)
			}
			if tc.wantErr != "" && err != nil && !strings.Contains(err.Error(), "oidc.issuer") {
				t.Fatalf("issuer %q: error %v does not name the field path", tc.issuer, err)
			}
		})
	}
}

// TestOIDCDiscoveryURLValidation pins oidc.discoveryUrl: an http(s) URL with
// a host and no credentials or fragment, a query allowed because some
// providers select a policy there, and never without oidc.issuer, because
// the document fetched there must name that issuer.
func TestOIDCDiscoveryURLValidation(t *testing.T) {
	cases := []struct {
		name      string
		issuer    string
		discovery string
		wantErr   string
	}{
		{"empty", "", "", ""},
		{"compose network address", "http://localhost:8480/realms/straza", "http://keycloak:8080/realms/straza/.well-known/openid-configuration", ""},
		{"policy query", "https://tenant.example/v2.0/", "https://tenant.example/v2.0/.well-known/openid-configuration?p=B2C_1_signin", ""},
		{"without an issuer", "", "http://keycloak:8080/realms/straza/.well-known/openid-configuration", "oidc.issuer is empty"},
		{"scheme-less", "https://idp.example", "keycloak:8080/realms/straza", "http:// or https://"},
		{"fragment", "https://idp.example", "https://idp.example/.well-known/openid-configuration#top", "fragment"},
		{"embedded credentials", "https://idp.example", "https://svc:token@idp.example/.well-known/openid-configuration", "credentials"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := defaults(ProfileStandalone)
			c.OIDC.Issuer = tc.issuer
			c.OIDC.DiscoveryURL = tc.discovery
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("discovery URL %q rejected: %v", tc.discovery, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("discovery URL %q: err = %v, want an error naming %q", tc.discovery, err, tc.wantErr)
			}
			if tc.wantErr != "" && err != nil && !strings.Contains(err.Error(), "oidc.discoveryUrl") {
				t.Fatalf("discovery URL %q: error %v does not name the field path", tc.discovery, err)
			}
		})
	}
}

// TestSinkURLValidation pins the shape contract for sinks[].url: http(s)
// only, host required, no fragment, and no embedded credentials; headers: is
// the receiver-auth lane. Paths and query strings pass: Splunk HEC routes and
// token-in-query receivers are legitimate webhook endpoints.
func TestSinkURLValidation(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr string
	}{
		{"https endpoint", "https://siem.example.com/services/collector", ""},
		{"query token", "https://hooks.example.com/ingest?token=abc", ""},
		{"http in-cluster", "http://vector.observability:8080/events", ""},
		{"scheme-less", "siem.example.com/collector", "http:// or https://"},
		{"garbage scheme", "kafka://broker:9092", "http:// or https://"},
		{"embedded credentials", "https://elastic:pw@es.example.com/_bulk", "headers"},
		{"fragment", "https://siem.example.com/collector#frag", "fragment"},
		{"no host", "https://", "host"},
		{"whitespace", "https://siem.example.com/a b", "whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := defaults(ProfileStandalone)
			c.Sinks = []Sink{{Name: "siem", Type: SinkWebhook, URL: tc.url}}
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("sink url %q rejected: %v", tc.url, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("sink url %q: err = %v, want an error naming %q", tc.url, err, tc.wantErr)
			}
			if tc.wantErr != "" && err != nil && !strings.Contains(err.Error(), `sink "siem"`) {
				t.Fatalf("sink url %q: error %v does not name the sink", tc.url, err)
			}
		})
	}
}

// TestOAuthEndpointURLValidation pins the shape contract for
// oauth.providers.*.{authUrl,tokenUrl}: http(s), host, no fragment (RFC 6749
// §3.1), no embedded credentials; query strings pass (the RFC allows them).
func TestOAuthEndpointURLValidation(t *testing.T) {
	provider := func(authURL, tokenURL string) Config {
		c := defaults(ProfileStandalone)
		c.OAuth.Providers = map[string]OAuthProvider{
			"acme": {ClientID: "id", ClientSecret: "s", AuthURL: authURL, TokenURL: tokenURL},
		}
		return c
	}
	good := provider("https://idp.example.com/authorize?audience=x", "https://idp.example.com/token")
	if err := good.Validate(); err != nil {
		t.Fatalf("valid provider rejected: %v", err)
	}
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"authUrl scheme-less", provider("idp.example.com/authorize", "https://idp.example.com/token"), "oauth.providers.acme.authUrl"},
		{"tokenUrl fragment", provider("https://idp.example.com/authorize", "https://idp.example.com/token#f"), "oauth.providers.acme.tokenUrl"},
		{"authUrl credentials", provider("https://u:p@idp.example.com/authorize", "https://idp.example.com/token"), "credentials"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want an error naming %q", err, tc.wantErr)
			}
		})
	}
}

// TestBodyStoreEndpointValidation pins capture.bodyStore.endpoint as
// host[:port] with NO scheme and NO path, the shape the minio client dials
// (bodystore.Options documents it; disableSSL selects the transport). A
// scheme or path here would boot and fail on the first externalized body.
func TestBodyStoreEndpointValidation(t *testing.T) {
	base := func(endpoint string) Config {
		c := defaults(ProfileEnterprise)
		c.Store.DSN = "postgres://straza@db/straza"
		c.Capture.BodyStore = BodyStoreCfg{Type: "s3", Endpoint: endpoint, Bucket: "transcripts"}
		return c
	}
	cases := []struct {
		name     string
		endpoint string
		wantErr  string
	}{
		{"host port", "minio.storage:9000", ""},
		{"bare host", "s3.eu-central-1.amazonaws.com", ""},
		{"scheme", "https://minio.storage:9000", "scheme"},
		{"path", "minio.storage:9000/transcripts", "host[:port]"},
		{"whitespace", "minio storage:9000", "whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := base(tc.endpoint).Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("endpoint %q rejected: %v", tc.endpoint, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("endpoint %q: err = %v, want an error naming %q", tc.endpoint, err, tc.wantErr)
			}
			if tc.wantErr != "" && err != nil && !strings.Contains(err.Error(), "capture.bodyStore.endpoint") {
				t.Fatalf("endpoint %q: error %v does not name the field path", tc.endpoint, err)
			}
		})
	}
}

// TestApproverURLsRejectCredentials applies the userinfo rule the rest of the
// URL family gets to the two approver fields too: an approver URL
// with user:password@ ends up verbatim in enroll QRs and boot logs.
func TestApproverURLsRejectCredentials(t *testing.T) {
	ingress := defaults(ProfileStandalone)
	ingress.Server.ApproverPublicURL = "https://u:p@approve.example.com"
	if err := ingress.Validate(); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("approverPublicUrl with userinfo: err = %v, want credentials rejection", err)
	}

	listener := defaults(ProfileStandalone)
	listener.Server.ApproverTLS = ApproverTLS{
		Listen: "0.0.0.0:8443", CertFile: "cert.pem", KeyFile: "key.pem",
		PublicURL: "https://u:p@approve.example.com:8443",
	}
	if err := listener.Validate(); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("approverTLS.publicUrl with userinfo: err = %v, want credentials rejection", err)
	}
}

// TestApproverTLSPublicURLBaseOnly: the dedicated listener's publicUrl becomes
// the enroll QR's server list (trailing slash trimmed by the consumer), so it
// is a base URL: a path silently produces phone dials that 404.
func TestApproverTLSPublicURLBaseOnly(t *testing.T) {
	c := defaults(ProfileStandalone)
	c.Server.ApproverTLS = ApproverTLS{
		Listen: "0.0.0.0:8443", CertFile: "cert.pem", KeyFile: "key.pem",
		PublicURL: "https://approve.example.com:8443/approver",
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Errorf("approverTLS.publicUrl with path: err = %v, want base-URL rejection", err)
	}
	slash := defaults(ProfileStandalone)
	slash.Server.ApproverTLS = ApproverTLS{
		Listen: "0.0.0.0:8443", CertFile: "cert.pem", KeyFile: "key.pem",
		PublicURL: "https://approve.example.com:8443/",
	}
	if err := slash.Validate(); err != nil {
		t.Errorf("approverTLS.publicUrl trailing slash must stay valid (consumer trims): %v", err)
	}
}
