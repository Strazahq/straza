package ctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// AppInfo is the admin API app representation.
type AppInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Runtime string   `json:"runtime"`
	Status  string   `json:"status"`
	Detail  string   `json:"detail"`
	Source  string   `json:"source"`
	Tools   []string `json:"tools"`
	// Empty until the manager has probed the app or seen it healthy.
	LastProbeAt   string `json:"last_probe_at"`
	LastHealthyAt string `json:"last_healthy_at"`
	// Paused reports an admin pause: the app stays stopped until enabled. The
	// list, the install answer and the enable and disable answers fill it.
	// The health recheck answer leaves it false, since only a running app
	// can be rechecked.
	Paused bool `json:"paused"`
	// ReachedBy lists the application roles holding access to the app,
	// sorted by name. Empty when no role has access, and absent on a server
	// that predates the field.
	ReachedBy []string `json:"reached_by"`
	// AdminRole names the control-plane role that administers this app and
	// no other. Every app has one, and admin API 0.127.0 sends it under this
	// name. It is empty only against a strazad older than that.
	AdminRole string `json:"admin_role"`
	// Manifest is the stored manifest as the JSON object the apps list
	// carries, in the app.yaml shape. Only the list fills it.
	Manifest json.RawMessage `json:"manifest"`
}

// Apps lists MCP apps with live manager status.
func (c *Client) Apps(ctx context.Context) ([]AppInfo, error) {
	var out []AppInfo
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/apps", nil, &out)
}

// AppsJSON returns the apps list exactly as the server answered it.
func (c *Client) AppsJSON(ctx context.Context) ([]byte, error) {
	return c.DoBytes(ctx, http.MethodGet, "/v1/admin/apps")
}

// InstallApp uploads a raw app.yaml manifest (server validates against the
// spec) and runs the install pipeline.
func (c *Client) InstallApp(ctx context.Context, yaml []byte) (AppInfo, error) {
	var out AppInfo
	return out, c.DoRawBody(ctx, http.MethodPost, "/v1/admin/apps", "application/yaml", yaml, &out)
}

// RemoveApp stops and removes an app by id or name.
func (c *Client) RemoveApp(ctx context.Context, ref string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/admin/apps/"+url.PathEscape(ref), nil, nil)
}

// ToolInfo is one exposed MCP tool from the live catalog (admin API).
type ToolInfo struct {
	ID          string `json:"id"`
	App         string `json:"app"`
	AppID       string `json:"app_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Tools lists every exposed MCP tool across running apps with upstream
// descriptions, for IGA access certification.
func (c *Client) Tools(ctx context.Context) ([]ToolInfo, error) {
	var out []ToolInfo
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/tools", nil, &out)
}

// ToolsJSON returns the tools list exactly as the server answered it.
func (c *Client) ToolsJSON(ctx context.Context) ([]byte, error) {
	return c.DoBytes(ctx, http.MethodGet, "/v1/admin/tools")
}

// RecheckApp triggers an immediate health probe for an app by id or name and
// returns the refreshed state.
func (c *Client) RecheckApp(ctx context.Context, ref string) (AppInfo, error) {
	var out AppInfo
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/apps/"+url.PathEscape(ref)+"/health", nil, &out)
}

// EnableApp clears an app's admin pause and (re)starts it by id or name,
// returning the refreshed state.
func (c *Client) EnableApp(ctx context.Context, ref string) (AppInfo, error) {
	var out AppInfo
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/apps/"+url.PathEscape(ref)+"/enable", nil, &out)
}

// DisableApp sets an app's admin pause and stops it by id or name, returning
// the refreshed state.
func (c *Client) DisableApp(ctx context.Context, ref string) (AppInfo, error) {
	var out AppInfo
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/apps/"+url.PathEscape(ref)+"/disable", nil, &out)
}

// AppLogs fetches recent runtime log lines for an app by id or name.
func (c *Client) AppLogs(ctx context.Context, ref string, limit int) ([]string, error) {
	var out struct {
		Lines []string `json:"lines"`
	}
	path := fmt.Sprintf("/v1/admin/apps/%s/logs?limit=%d", url.PathEscape(ref), limit)
	return out.Lines, c.Do(ctx, http.MethodGet, path, nil, &out)
}

// AppSecret is one stored static secret as the admin API describes it: the
// row id, the scope (app for the server's own secret, role for a per-role
// override), the role when scoped to one, the first four hex characters of
// the value's SHA-256 as a fingerprint, and when it was set. The value is
// never on the wire after the write.
type AppSecret struct {
	ID          string `json:"id"`
	App         string `json:"app"`
	Scope       string `json:"scope"`
	Role        string `json:"role"`
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
	SetAt       string `json:"set_at"`
}

// SetAppSecret stores a static secret for an app: the server's own secret
// when role is empty, the override for one role otherwise. The value is
// sent once over the authenticated channel and never echoed back. The
// answer carries the row's fingerprint so an operator can match it on
// apps show.
func (c *Client) SetAppSecret(ctx context.Context, appRef, role, value string) (AppSecret, error) {
	body := map[string]string{"value": value}
	if role != "" {
		body["role"] = role
	}
	var out AppSecret
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/apps/"+url.PathEscape(appRef)+"/secrets", body, &out)
}

// AppSecrets lists the static secret rows stored for an app, never their
// values.
func (c *Client) AppSecrets(ctx context.Context, appRef string) ([]AppSecret, error) {
	var out []AppSecret
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/apps/"+url.PathEscape(appRef)+"/secrets", nil, &out)
}

// RemoveAppSecret deletes the server's own secret when role is empty, else
// the override stored for that role.
func (c *Client) RemoveAppSecret(ctx context.Context, appRef, role string) error {
	path := "/v1/admin/apps/" + url.PathEscape(appRef) + "/secrets"
	if role != "" {
		path += "/" + url.PathEscape(role)
	}
	return c.Do(ctx, http.MethodDelete, path, nil, nil)
}

// ToolBinding is the admin API binding representation.
type ToolBinding struct {
	ID    string   `json:"id"`
	App   string   `json:"app"`
	Role  string   `json:"role"`
	Tools []string `json:"tools"`
}

// BindAppTools gives a role access to an app's tools: a list of names, or
// the glob "*" (also the meaning of an empty list) for every tool including
// tools added later.
func (c *Client) BindAppTools(ctx context.Context, appRef, role string, tools []string) (ToolBinding, error) {
	var out ToolBinding
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/apps/"+url.PathEscape(appRef)+"/bindings",
		map[string]any{"role": role, "tools": tools}, &out)
}

// Bindings lists role-to-app tool bindings.
func (c *Client) Bindings(ctx context.Context) ([]ToolBinding, error) {
	var out []ToolBinding
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/bindings", nil, &out)
}

// BindingsJSON returns the access rows exactly as the server answered them.
func (c *Client) BindingsJSON(ctx context.Context) ([]byte, error) {
	return c.DoBytes(ctx, http.MethodGet, "/v1/admin/bindings")
}

// UnbindTools deletes a tool binding by id.
func (c *Client) UnbindTools(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/admin/bindings/"+url.PathEscape(id), nil, nil)
}
