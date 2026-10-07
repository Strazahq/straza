package ctl

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// APIToken is the long-lived admin API token representation, the one
// credential for /v1/admin callers and the SCIM plane. Token carries the
// plaintext only in the create response, exactly once.
type APIToken struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Scope   string    `json:"scope"`
	Created time.Time `json:"created"`
	// Lifecycle fields: a nil Expires never expires, and a nil LastUsed was never used.
	Expires  *time.Time `json:"expires,omitempty"`
	LastUsed *time.Time `json:"lastUsed,omitempty"`
	Token    string     `json:"token,omitempty"`
}

// CreateAPIToken mints a long-lived admin API bearer token. Scope is
// required by the server. A ttl of 0 is non-expiring.
func (c *Client) CreateAPIToken(ctx context.Context, name, scope string, ttl time.Duration) (APIToken, error) {
	var out APIToken
	body := map[string]any{"name": name}
	if scope != "" {
		body["scope"] = scope
	}
	if ttl > 0 {
		body["expires_in"] = int64(ttl.Seconds())
	}
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/api-tokens", body, &out)
}

// APITokens lists token metadata (never plaintext).
func (c *Client) APITokens(ctx context.Context) ([]APIToken, error) {
	var out []APIToken
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/api-tokens", nil, &out)
}

// RevokeAPIToken deletes a token by id.
func (c *Client) RevokeAPIToken(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/admin/api-tokens/"+url.PathEscape(id), nil, nil)
}
