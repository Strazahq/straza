// Package entragraph is Straza's client for Microsoft Graph. It creates,
// renames and deletes the Entra security groups that stand for Straza's
// roles, and reads their members back, as the Straza service app with an
// app-only token. It follows no redirect, sends its token to the Graph host
// only, follows paging links only on that host and reads every answer
// through a size cap. It never sleeps or retries: the caller decides when to
// ask again. Nothing here logs, and no token reaches an error.
package entragraph

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	// defaultGraph is the Microsoft Graph endpoint, and defaultLogin the
	// Microsoft identity platform that issues its tokens.
	defaultGraph = "https://graph.microsoft.com"
	defaultLogin = "https://login.microsoftonline.com"
	// graphScope asks for every application permission granted to the app.
	graphScope    = "https://graph.microsoft.com/.default"
	assertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	// defaultTimeout bounds one request of the client New builds.
	defaultTimeout = 30 * time.Second
	// renewWindow is how long before its expiry a cached token is replaced.
	renewWindow = 60 * time.Second
	// maxLifetime caps the lifetime a token answer names, so the arithmetic
	// cannot overflow.
	maxLifetime = 24 * time.Hour
)

// Signer signs the certificate assertion of RFC 7523 with which the service
// app signs in to Entra, and names the key that signed it. The production
// signer is *authn.ClientAssertionKeys.
type Signer interface {
	SignCertificateAssertion(now time.Time, clientID, audience string) (assertion, kid string, err error)
}

// Options configures a Client. Tenant, ClientID and Signer are required.
type Options struct {
	// HTTP sends every request. Nil means a client with a 30 second timeout.
	// Redirects are refused with either.
	HTTP *http.Client
	// Tenant is the Entra tenant's ID or primary domain, and ClientID the
	// application ID of the Straza service app in that tenant.
	Tenant   string
	ClientID string
	Signer   Signer
	// Now replaces time.Now in tests.
	Now func() time.Time

	// graphBase and tokenURL replace the Graph endpoint and the token
	// endpoint in tests.
	graphBase string
	tokenURL  string
}

// Client calls Microsoft Graph as the Straza service app. It is safe for
// concurrent use.
type Client struct {
	http     *http.Client
	signer   Signer
	tenant   string
	clientID string
	now      func() time.Time
	// base is the scheme and host of the Graph endpoint, which every request
	// address starts with, and graph is the same address parsed.
	base     string
	graph    *url.URL
	tokenURL string

	// mu guards the token cache. It is held across a token request, so
	// concurrent calls wait for the one request in flight.
	mu      sync.Mutex
	token   string
	renewAt time.Time
	// ownerID is the service app's own object ID, from the token's oid claim.
	ownerID string
}

// New returns a Client for the service app ClientID in Tenant. It sends no
// request: the first call fetches the token. It fails with a sentence when
// a required option is missing.
func New(o Options) (*Client, error) {
	if o.Tenant == "" || o.ClientID == "" {
		return nil, errorf("The Entra Graph client needs the tenant and the application ID of the Straza service app. Set identity.entra.tenant and identity.entra.clientId in strazad's config.")
	}
	if o.Signer == nil {
		return nil, errorf("The Entra Graph client needs the client assertion signer, and none was given.")
	}
	base := o.graphBase
	if base == "" {
		base = defaultGraph
	}
	graph, err := url.Parse(base)
	if err != nil || (graph.Scheme != "https" && graph.Scheme != "http") || graph.Host == "" {
		return nil, errorf("The Graph endpoint %q is not an absolute http or https address.", base)
	}
	tokenURL := o.tokenURL
	if tokenURL == "" {
		tokenURL = fmt.Sprintf("%s/%s/oauth2/v2.0/token", defaultLogin, url.PathEscape(o.Tenant))
	}
	hc := &http.Client{Timeout: defaultTimeout}
	if o.HTTP != nil {
		copied := *o.HTTP
		hc = &copied
	}
	// Following a redirect could carry the token or the assertion to
	// another host, so the last answer comes back and counts as a failure.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		http:     hc,
		signer:   o.Signer,
		tenant:   o.Tenant,
		clientID: o.ClientID,
		now:      now,
		base:     graph.Scheme + "://" + graph.Host,
		graph:    graph,
		tokenURL: tokenURL,
	}, nil
}
