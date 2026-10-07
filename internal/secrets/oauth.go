package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ProviderConfig is the resolved OAuth client registration for one upstream
// provider. The GitHub reference provider gets its endpoint defaults from
// config, and nothing here is GitHub-specific.
type ProviderConfig struct {
	Name         string
	ClientID     string
	ClientSecret string
	AuthURL      string   // authorization endpoint (browser)
	TokenURL     string   // token endpoint (server-to-server)
	Scopes       []string // default scopes when a manifest declares none
}

// Grant is one user's tokens for one app. It is sealed (secretbox) into
// Credential.EncPayload as JSON. Plaintext never touches the store or any
// API response.
type Grant struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// GrantMeta is the plaintext Credential.OAuthMeta JSON of a user's own row,
// an OAuth grant or a pasted token: scheduling and ownership data only, no
// secret material. Provider and Scopes are empty on a token row. SetBy is
// the user id of whoever stored the row when it was not the owner. AllowAgents
// is the owner's opt-in for their sponsored agents to run on the row.
type GrantMeta struct {
	Provider    string     `json:"provider,omitempty"`
	Scopes      []string   `json:"scopes,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"` // nil = non-expiring token
	SetBy       string     `json:"set_by,omitempty"`
	AllowAgents bool       `json:"allow_agents,omitempty"`
}

// AuthorizeURL builds the provider authorization URL the user's browser
// opens: standard RFC 6749 auth-code request, scopes space-joined.
func AuthorizeURL(p ProviderConfig, redirectURI, state string, scopes []string) string {
	q := url.Values{
		"client_id":     {p.ClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"state":         {state},
	}
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	return p.AuthURL + "?" + q.Encode()
}

// ExchangeCode redeems an authorization code at the provider's token
// endpoint (server-side; the client secret never leaves strazad). The
// returned expiry is nil for non-expiring tokens (classic GitHub OAuth apps).
func ExchangeCode(ctx context.Context, hc *http.Client, p ProviderConfig, code, redirectURI string) (Grant, *time.Time, error) {
	return tokenRequest(ctx, hc, p, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	}, "")
}

// RefreshGrant redeems a refresh token for a fresh access token. When the
// provider omits a new refresh token (RFC 6749 §6 MAY), the old one is kept
// so the next cycle still works; providers that rotate (GitHub Apps) get
// their new one stored.
func RefreshGrant(ctx context.Context, hc *http.Client, p ProviderConfig, refreshToken string) (Grant, *time.Time, error) {
	return tokenRequest(ctx, hc, p, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}, refreshToken)
}

// maxTokenBody caps how much of a provider response we read: the token
// endpoint answers with a small JSON object; anything bigger is hostile.
const maxTokenBody = 1 << 20

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func tokenRequest(ctx context.Context, hc *http.Client, p ProviderConfig, form url.Values, keepRefresh string) (Grant, *time.Time, error) {
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", p.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return Grant{}, nil, fmt.Errorf("secrets: oauth %s: %w", p.Name, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GitHub answers form-encoded unless asked for JSON explicitly.
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return Grant{}, nil, fmt.Errorf("secrets: oauth %s token endpoint: %w", p.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody))
	if err != nil {
		return Grant{}, nil, fmt.Errorf("secrets: oauth %s response: %w", p.Name, err)
	}

	tr, parseErr := parseTokenResponse(resp.Header.Get("Content-Type"), body)
	switch {
	case parseErr == nil && tr.Error != "":
		// Providers signal protocol errors both RFC-style (4xx) and
		// GitHub-style (error field in a 200 body); the field is the answer.
		msg := tr.Error
		if tr.ErrorDesc != "" {
			msg += ": " + tr.ErrorDesc
		}
		return Grant{}, nil, fmt.Errorf("secrets: oauth %s refused: %s", p.Name, msg)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return Grant{}, nil, fmt.Errorf("secrets: oauth %s token endpoint answered HTTP %d", p.Name, resp.StatusCode)
	case parseErr != nil:
		return Grant{}, nil, fmt.Errorf("secrets: oauth %s: unparseable token response: %w", p.Name, parseErr)
	case tr.AccessToken == "":
		return Grant{}, nil, fmt.Errorf("secrets: oauth %s: no access token in response", p.Name)
	}

	g := Grant{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	if g.RefreshToken == "" {
		g.RefreshToken = keepRefresh
	}
	var expiry *time.Time
	if tr.ExpiresIn > 0 {
		e := time.Now().UTC().Add(time.Duration(tr.ExpiresIn) * time.Second)
		expiry = &e
	}
	return g, expiry, nil
}

// parseTokenResponse understands both wire shapes: JSON (what we ask for)
// and form-encoded (providers that ignore the Accept header).
func parseTokenResponse(contentType string, body []byte) (tokenResponse, error) {
	var tr tokenResponse
	if mt, _, err := mime.ParseMediaType(contentType); err == nil && mt == "application/x-www-form-urlencoded" {
		vals, err := url.ParseQuery(string(body))
		if err != nil {
			return tr, err
		}
		tr.AccessToken = vals.Get("access_token")
		tr.RefreshToken = vals.Get("refresh_token")
		tr.Error = vals.Get("error")
		tr.ErrorDesc = vals.Get("error_description")
		if v := vals.Get("expires_in"); v != "" {
			tr.ExpiresIn, _ = strconv.ParseInt(v, 10, 64)
		}
		return tr, nil
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return tr, err
	}
	return tr, nil
}
