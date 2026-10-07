package ctl

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Users lists all users.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var out []User
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/users", nil, &out)
}

// CreateUser creates a local user.
func (c *Client) CreateUser(ctx context.Context, username, email, display, password string) (User, error) {
	var out User
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/users", map[string]string{
		"username": username, "email": email, "display": display, "password": password,
	}, &out)
}

// CreateNHI provisions a non-human identity (kind=nhi): the standalone
// counterpart of agentic-SCIM provisioning. userType is agent or service and
// the server refuses any other value. NHIs never hold passwords. Their
// credential is the per-NHI assertion key (SetNHIKey).
func (c *Client) CreateNHI(ctx context.Context, username, display, userType string) (User, error) {
	var out User
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/users", map[string]string{
		"username": username, "display": display, "kind": "nhi", "user_type": userType,
	}, &out)
}

// SetNHIKey registers (or rotates) an NHI's Ed25519 assertion public key,
// what the built-in issuer's client_credentials grant verifies against.
func (c *Client) SetNHIKey(ctx context.Context, userID, publicKeyB64 string) error {
	return c.Do(ctx, http.MethodPut, "/v1/admin/users/"+url.PathEscape(userID)+"/nhi-key",
		map[string]string{"public_key": publicKeyB64}, nil)
}

// UnsetNHIKey revokes an NHI's assertion key: the next token grant fails.
func (c *Client) UnsetNHIKey(ctx context.Context, userID string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/admin/users/"+url.PathEscape(userID)+"/nhi-key", nil, nil)
}

// SetUserStatus enables or disables a user.
func (c *Client) SetUserStatus(ctx context.Context, id, status string) (User, error) {
	var out User
	return out, c.Do(ctx, http.MethodPatch, "/v1/admin/users/"+url.PathEscape(id),
		map[string]string{"status": status}, &out)
}

// SetUserPassword rotates a user's built-in-issuer password. The
// value travels in the request body only; the API never echoes it.
func (c *Client) SetUserPassword(ctx context.Context, id, password string) (User, error) {
	var out User
	return out, c.Do(ctx, http.MethodPatch, "/v1/admin/users/"+url.PathEscape(id),
		map[string]string{"password": password}, &out)
}

// LockUser places a Straza-lane lock: an origin-tagged revocation that
// survives IdM enables and reconciliation. users.status is untouched.
// origin "" defaults server-side to admin, and "external" marks SOAR/SIEM
// automation.
func (c *Client) LockUser(ctx context.Context, id, reason, origin string) error {
	body := map[string]string{"reason": reason}
	if origin != "" {
		body["origin"] = origin
	}
	return c.Do(ctx, http.MethodPost, "/v1/admin/users/"+url.PathEscape(id)+"/lock", body, nil)
}

// UnlockUser lifts every revocation lane for the user, the explicit Straza
// action admin/external locks require.
func (c *Client) UnlockUser(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodPost, "/v1/admin/users/"+url.PathEscape(id)+"/unlock", nil, nil)
}

// UserByUsername resolves a username to a user.
func (c *Client) UserByUsername(ctx context.Context, username string) (User, error) {
	users, err := c.Users(ctx)
	if err != nil {
		return User{}, err
	}
	for _, u := range users {
		if u.Username == username {
			return u, nil
		}
	}
	return User{}, fmt.Errorf("no user named %q", username)
}

// Device is the admin API device representation: the snake_case
// devicePayload wire that the openapi Device schema pins.
type Device struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	Platform    string    `json:"platform"`
	Status      string    `json:"status"`
	EnrolledAt  time.Time `json:"enrolled_at"`
}

// UserDevices lists a user's enrolled devices.
func (c *Client) UserDevices(ctx context.Context, userID string) ([]Device, error) {
	var out []Device
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/users/"+url.PathEscape(userID)+"/devices", nil, &out)
}

// RevokeDevice removes a user's device enrollment, the enroll-credential
// kill switch: the device token stops minting sessions, sessions carrying
// the device binding stop refreshing, and a live kit on the device gets the
// revocation push. Re-enrolling needs a fresh interactive login.
func (c *Client) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	return c.Do(ctx, http.MethodDelete,
		"/v1/admin/users/"+url.PathEscape(userID)+"/devices/"+url.PathEscape(deviceID), nil, nil)
}
