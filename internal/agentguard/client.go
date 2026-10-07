package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/trace"
	"github.com/strazahq/straza/internal/oidcflow"
	"github.com/strazahq/straza/internal/sameorigin"
	"github.com/strazahq/straza/internal/version"
)

// Client talks to strazad from the agent kit: device-flow login, checkin,
// serverCheck decide, snapshot fetch, audit batch drain.
type Client struct {
	Base string
	HTTP *http.Client
}

// NewClient builds a strazad client. A request of it that carries a
// credential, in the Authorization header or in the body, follows a redirect
// only within its own origin (sameorigin.CheckCredentialed).
func NewClient(base string) *Client {
	return &Client{Base: base, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: sameorigin.CheckCredentialed}}
}

// DeviceAuth is the RFC 8628 device authorization response.
type DeviceAuth struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri_complete"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// DiscoverLogin resolves where this client should run the device flow: the
// external IdP in enterprise, the built-in issuer in standalone, and the
// legacy paths against an older server that publishes no login discovery.
func (c *Client) DiscoverLogin(ctx context.Context) (oidcflow.Flow, error) {
	return oidcflow.Discover(ctx, c.HTTP, c.Base, "straza")
}

// DiscoverNHIOffer reads whether the server offers the NHI key lane and at
// which issuer (the nhi_issuer in idp.json), without dialing
// that issuer. Offered=false means the client-secret lane at the human IdP
// is the only option.
func (c *Client) DiscoverNHIOffer(ctx context.Context) (oidcflow.NHIOffer, error) {
	return oidcflow.DiscoverNHIOffer(ctx, c.HTTP, c.Base)
}

// ResolveNHI turns a key-lane offer into its concrete token flow, dialing
// the offered issuer. Called only once the key lane is the picked lane.
func (c *Client) ResolveNHI(ctx context.Context, offer oidcflow.NHIOffer) (oidcflow.Flow, error) {
	return oidcflow.ResolveNHI(ctx, c.HTTP, offer)
}

// DiscoverNHI resolves where a headless NHI authenticates on the key lane,
// composing the offer and its resolution for the caller already committed
// to that lane (a key-lane session start). offered=false means the server
// names no Straza issuer for NHIs.
func (c *Client) DiscoverNHI(ctx context.Context) (oidcflow.Flow, bool, error) {
	return oidcflow.DiscoverNHI(ctx, c.HTTP, c.Base)
}

// StartDeviceFlow requests a device code at the flow's discovered endpoint,
// asking for scope=openid so external IdPs mint an ID token (the built-in
// issuer implies it and ignores the parameter).
func (c *Client) StartDeviceFlow(ctx context.Context, flow oidcflow.Flow) (DeviceAuth, error) {
	var out DeviceAuth
	err := c.postForm(ctx, flow.DeviceAuthURL, url.Values{
		"client_id": {flow.ClientID},
		"scope":     {"openid"},
	}, &out)
	return out, err
}

// PollToken polls the flow's token endpoint once. Returns (idToken, pending,
// error). The endpoint answers protocol state per RFC 6749 §5.2:
// authorization_pending, slow_down, expired_token arrive as HTTP 400 with an
// `error` body (pinned by authn TestDeviceFlowWireContract), so it must be
// read with postFormOAuth, never the generic 4xx-is-failure path.
func (c *Client) PollToken(ctx context.Context, flow oidcflow.Flow, deviceCode string) (string, bool, error) {
	var out struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := c.postFormOAuth(ctx, flow.TokenURL, url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
		"client_id":   {flow.ClientID},
	}, &out); err != nil {
		return "", false, err
	}
	switch out.Error {
	case "":
		return out.IDToken, false, nil
	case "authorization_pending", "slow_down":
		return "", true, nil
	default:
		return "", false, fmt.Errorf("login failed: %s", out.Error)
	}
}

// EnrollResponse mirrors POST /v1/enroll.
type EnrollResponse struct {
	UserID               string `json:"user_id"`
	Username             string `json:"username"`
	DeviceID             string `json:"device_id"`
	DeviceToken          string `json:"device_token"`
	DeviceTokenExpiresIn int    `json:"device_token_expires_in"`
}

// Enroll binds the identity to a device.
func (c *Client) Enroll(ctx context.Context, idToken, name, platform, fingerprint string) (EnrollResponse, error) {
	var out EnrollResponse
	err := c.postJSON(ctx, "/v1/enroll", "", map[string]any{
		"id_token": idToken,
		"device":   map[string]string{"name": name, "platform": platform, "fingerprint": fingerprint},
		// The kit names itself (openapi 0.114.0), so the credential opens
		// sessions for coding harnesses only; an older server drops the field.
		"client_kind": "kit",
	}, &out)
	return out, err
}

// Attestation is the check-in attestation payload (spec/attestation
// v1beta1). Platform is GOOS/GOARCH; it selects platform-scoped rows in the
// server's expected-hash registry.
type Attestation struct {
	Managed  bool              `json:"managed"`
	Platform string            `json:"platform,omitempty"`
	Hashes   map[string]string `json:"hashes"`
}

// CheckinResponse mirrors POST /v1/checkin.
type CheckinResponse struct {
	SessionID    string        `json:"session_id"`
	SessionToken string        `json:"session_token"`
	ExpiresIn    int           `json:"expires_in"`
	Attestation  string        `json:"attestation"`
	User         string        `json:"user"`
	Roles        []string      `json:"roles"`
	SnapshotID   string        `json:"snapshot_id"`
	Packs        []PackPayload `json:"knowledge_packs"`
	// Identity typology (spec/policyset revision 9): mirrored onto the cached
	// session so local hook decisions match identity-scoped sets exactly like
	// the server (one engine, no drift). Absent from older servers = empty.
	UserType   string `json:"user_type,omitempty"`
	AgencyMode string `json:"agency_mode,omitempty"`
	SwarmID    string `json:"swarm_id,omitempty"`
	// DeviceToken is a renewed enroll credential: the server
	// sends one only on the device-token lane, when the presented credential
	// is past half its life. Absent from older servers and every other lane.
	DeviceToken          string `json:"device_token,omitempty"`
	DeviceTokenExpiresIn int    `json:"device_token_expires_in,omitempty"`
}

// PackPayload is a delivered knowledge pack.
type PackPayload struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Content  string `json:"content"`
	Checksum string `json:"checksum"`
}

// Checkin starts a session with an ID token.
func (c *Client) Checkin(ctx context.Context, idToken, deviceID, harnessName, harnessVersion string, att Attestation) (CheckinResponse, error) {
	return c.checkin(ctx, map[string]any{
		"id_token":    idToken,
		"client":      clientStamp(),
		"device_id":   deviceID,
		"harness":     map[string]string{"name": harnessName, "version": harnessVersion},
		"attestation": att,
	})
}

// CheckinDevice starts a session with the long-lived enroll credential. The
// server derives the device binding from the token itself.
func (c *Client) CheckinDevice(ctx context.Context, deviceToken, harnessName, harnessVersion string, att Attestation) (CheckinResponse, error) {
	return c.checkin(ctx, map[string]any{
		"device_token": deviceToken,
		"client":       clientStamp(),
		"harness":      map[string]string{"name": harnessName, "version": harnessVersion},
		"attestation":  att,
	})
}

// Refresh renews a session with a session token.
func (c *Client) Refresh(ctx context.Context, sessionToken, harnessName, harnessVersion string, att Attestation) (CheckinResponse, error) {
	return c.checkin(ctx, map[string]any{
		"session_token": sessionToken,
		"client":        clientStamp(),
		"harness":       map[string]string{"name": harnessName, "version": harnessVersion},
		"attestation":   att,
	})
}

// clientStamp names this build on every check-in shape (spec/attestation
// v1beta1) so the fleet screen can say which client a session
// runs; the server records it and never verifies it.
func clientStamp() map[string]string {
	return map[string]string{"version": version.Version, "commit": version.Commit}
}

func (c *Client) checkin(ctx context.Context, body map[string]any) (CheckinResponse, error) {
	var out CheckinResponse
	err := c.postJSON(ctx, "/v1/checkin", "", body, &out)
	return out, err
}

// DecideResponse mirrors POST /v1/decide.
type DecideResponse struct {
	Effect      string   `json:"effect"`
	RuleID      string   `json:"ruleId"`
	Reason      string   `json:"reason"`
	Obligations []string `json:"obligations"`
	SnapshotID  string   `json:"snapshotId"`
	// ApprovalID is the pending approval record id a mode:approve escalation
	// created (empty otherwise). Carried for correlation/logging; the deny
	// reason already embeds the reference the model retries against.
	ApprovalID string `json:"approvalId,omitempty"`
}

// Decide asks the server PDP (serverCheck rules). agentType and agentID are
// the delegate attribution of the call, sent beside the event for the
// server's audit record and omitted when empty.
func (c *Client) Decide(ctx context.Context, sessionToken string, event any, agentType, agentID string) (DecideResponse, error) {
	var out DecideResponse
	body := map[string]any{"event": event}
	if agentType != "" {
		body["agentType"] = agentType
	}
	if agentID != "" {
		body["agentId"] = agentID
	}
	err := c.postJSON(ctx, "/v1/decide", sessionToken, body, &out)
	return out, err
}

// FetchSnapshot downloads the active signed snapshot bytes; returns the id
// (from the ETag/header) and bytes. Uses If-None-Match to skip re-download.
// sessionToken rides as the bearer, which the enterprise profile requires. A
// refusal returns a *StatusError that carries the server's sentence.
func (c *Client) FetchSnapshot(ctx context.Context, sessionToken, knownID string) (id string, body []byte, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/v1/snapshot", nil)
	if err != nil {
		return "", nil, false, err
	}
	if sessionToken != "" {
		req.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	if knownID != "" {
		req.Header.Set("If-None-Match", `"`+knownID+`"`)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", nil, false, fmt.Errorf("fetch snapshot: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		return knownID, nil, true, nil
	}
	b, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &apiErr)
		if apiErr.Error == "" {
			apiErr.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", nil, false, fmt.Errorf("%s: %w", req.URL.Path, &StatusError{Status: resp.StatusCode, Msg: apiErr.Error})
	}
	return resp.Header.Get("X-Straza-Snapshot-Id"), b, false, err
}

// SnapshotKeys fetches the published snapshot verification keys (kid → base64).
func (c *Client) SnapshotKeys(ctx context.Context) (map[string]string, error) {
	var doc struct {
		Keys []struct {
			KID string `json:"kid"`
			Key string `json:"key"`
		} `json:"keys"`
	}
	if err := c.getJSON(ctx, "/.well-known/straza/snapshot-keys.json", &doc); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, k := range doc.Keys {
		out[k.KID] = k.Key
	}
	return out, nil
}

// Audit backpressure retry bounds. The doubling delay spans about three
// seconds at the floor, well past the second a tripped per-session ingest
// bucket needs to refill, and the attempt bound stops a drain from spinning
// against a server that refuses indefinitely.
const (
	auditRetryBase = 250 * time.Millisecond
	auditRetryCap  = 2 * time.Second
	auditRetries   = 5
)

// AuditBatch drains spooled audit events to the server. A backpressure
// answer (429 from the per-session ingest limiter or the outbox backlog
// gate, 503 from an unavailable audit store) is RETRIED inside the caller's
// context instead of returned: a refused batch stays parked in the spool,
// and a burst of hook decisions that ends the session without a session.end
// and runs no daemon has no later trigger to unpark it, so one refusal would
// keep those records off the chain for good. Every other refusal and every
// transport failure is returned unchanged, so an offline box still parks its
// records for the next trigger.
func (c *Client) AuditBatch(ctx context.Context, sessionToken string, events []json.RawMessage) (int, error) {
	var out struct {
		Accepted int `json:"accepted"`
	}
	delay := auditRetryBase
	for attempt := 0; ; attempt++ {
		err := c.postJSON(ctx, "/v1/audit/batch", sessionToken, map[string]any{"events": events}, &out)
		if err == nil || attempt == auditRetries || !auditBackpressure(err) {
			return out.Accepted, err
		}
		// Half the delay is a floor, so a retry cannot land before the
		// bucket has refilled; the jittered half spreads the drains of one
		// burst, which are separate detached processes refused together.
		wait := delay/2 + rand.N(delay/2) // #nosec G404 -- herd-spreading jitter, not a security boundary
		select {
		case <-ctx.Done():
			return out.Accepted, err
		case <-time.After(wait):
		}
		delay = min(delay*2, auditRetryCap)
	}
}

// auditBackpressure reports whether an audit-batch refusal is the server
// asking for a retry rather than rejecting the batch: the two statuses
// strazad pairs with Retry-After on that route.
func auditBackpressure(err error) bool {
	var refuse *StatusError
	if !errors.As(err, &refuse) {
		return false
	}
	return refuse.Status == http.StatusTooManyRequests || refuse.Status == http.StatusServiceUnavailable
}

// endpointURL joins a strazad path onto the base; an absolute URL (a
// discovered IdP endpoint) passes through untouched.
func (c *Client) endpointURL(pathOrURL string) string {
	if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		return pathOrURL
	}
	return c.Base + pathOrURL
}

func (c *Client) postForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpointURL(path), bytes.NewReader([]byte(form.Encode())))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

// postFormOAuth posts a form to an OAuth endpoint, where a 4xx carrying an
// `error` field is a protocol ANSWER (RFC 6749 §5.2) the caller interprets,
// not a failure. Bodies without one, and 5xx, error like c.do.
func (c *Client) postFormOAuth(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpointURL(path), bytes.NewReader([]byte(form.Encode())))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var probe struct {
			Error string `json:"error"`
		}
		if resp.StatusCode >= 500 || json.Unmarshal(buf, &probe) != nil || probe.Error == "" {
			msg := probe.Error
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
			return fmt.Errorf("%s: %w", req.URL.Path, &StatusError{Status: resp.StatusCode, Msg: msg})
		}
	}
	if out != nil && len(buf) > 0 {
		return json.Unmarshal(buf, out)
	}
	return nil
}

func (c *Client) postJSON(ctx context.Context, path, bearer string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return c.do(req, out)
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// StatusError is a strazad HTTP refusal (status >= 400), distinguishable
// from transport failures via errors.As: a 401 means "session refused",
// a connection error means "try again".
type StatusError struct {
	Status int
	Msg    string
}

func (e *StatusError) Error() string { return e.Msg }

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The decision journal observes every answer: status + the X-Request-Id
	// the server minted or echoed, the id that joins a client journal line to
	// the server's own log record. Nil-safe: a request without a Call in its
	// context is simply not observed.
	trace.CallFrom(req.Context()).Observe(resp.StatusCode, resp.Header.Get("X-Request-Id"))
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(buf, &apiErr)
		if apiErr.Error == "" {
			apiErr.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("%s: %w", req.URL.Path, &StatusError{Status: resp.StatusCode, Msg: apiErr.Error})
	}
	if out != nil && len(buf) > 0 {
		return json.Unmarshal(buf, out)
	}
	return nil
}
