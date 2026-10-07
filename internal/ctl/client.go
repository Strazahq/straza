package ctl

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/strazahq/straza/internal/oidcflow"
	"github.com/strazahq/straza/internal/sameorigin"
	"github.com/strazahq/straza/internal/version"
)

// Client talks to strazad: device-code login, session refresh, admin calls.
type Client struct {
	Base      string
	HTTP      *http.Client
	CredsPath string // defaults to ~/.straza/credentials.json
	// APIToken is an admin API token that every authenticated call sends in
	// place of the stored login. Empty means the login.
	APIToken string
	// AgentMarker names the environment variable that shows this process
	// runs inside a coding agent, empty outside one (see CodingAgentMarker).
	// With it set, a call that changes Straza and would go out on the stored
	// login is refused before anything is sent (see guardRefuses).
	AgentMarker string
}

// NewClient builds a client for the given strazad base URL. A request that
// carries a credential, in the Authorization header or in the body, follows
// a redirect only within its own origin (sameorigin.CheckCredentialed).
func NewClient(base string) *Client {
	return &Client{
		Base:      base,
		HTTP:      &http.Client{Timeout: 30 * time.Second, CheckRedirect: sameorigin.CheckCredentialed},
		CredsPath: DefaultCredsPath(),
	}
}

// DefaultCredsPath is where strazactl keeps the session it established:
// ~/.straza/credentials.json. It records the server that was logged into, so
// it doubles as the source of the default target (see ResolveTarget).
func DefaultCredsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".straza", "credentials.json")
}

type credentials struct {
	Server       string `json:"server"`
	SessionToken string `json:"session_token"`
	SessionID    string `json:"session_id"`
	// DeviceToken is the long-lived enroll credential: it re-establishes a
	// session after the 300 s token dies, so an idle CLI does not demand a
	// browser login every few minutes. It is revocable via user disable or
	// device revoke.
	DeviceToken string `json:"device_token,omitempty"`
}

func (c *Client) loadCreds() (credentials, error) {
	raw, err := os.ReadFile(c.CredsPath)
	if err != nil {
		return credentials{}, fmt.Errorf("not logged in (run `strazactl login`): %w", err)
	}
	var creds credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return credentials{}, fmt.Errorf("corrupt credentials file %s: %w", c.CredsPath, err)
	}
	return creds, nil
}

func (c *Client) saveCreds(creds credentials) error {
	if err := os.MkdirAll(filepath.Dir(c.CredsPath), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(creds, "", "  ") // #nosec G117 -- this IS the credential store (0600)
	return os.WriteFile(c.CredsPath, raw, 0o600)
}

// Login runs the OIDC device-code flow at the issuer the server advertises
// (the external IdP in enterprise, the built-in issuer in standalone)
// and establishes a strazactl session. Progress goes to w. breakGlass skips
// the advertised issuer and signs in at the server's own emergency page,
// which accepts the break-glass admin only.
func (c *Client) Login(ctx context.Context, w io.Writer, breakGlass bool) error {
	var flow oidcflow.Flow
	if breakGlass {
		flow = oidcflow.Builtin(c.Base, "strazactl")
		fmt.Fprintf(w, "Signing in at the server's emergency page as break-glass: %s\n", flow.Issuer)
	} else {
		var err error
		if flow, err = oidcflow.Discover(ctx, c.HTTP, c.Base, "strazactl"); err != nil {
			return err
		}
		if flow.Issuer != strings.TrimSuffix(c.Base, "/") {
			fmt.Fprintf(w, "Signing in at your identity provider: %s\n", flow.Issuer)
		}
	}
	var auth struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri_complete"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := c.postFormJSON(ctx, flow.DeviceAuthURL,
		url.Values{"client_id": {flow.ClientID}, "scope": {"openid"}}, &auth); err != nil {
		return fmt.Errorf("start device flow: %w", err)
	}
	fmt.Fprintf(w, "Open %s\nand confirm code %s\n", auth.VerificationURI, auth.UserCode)

	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	var idToken string
poll:
	for time.Now().Before(deadline) {
		// Poll first: the user has usually already approved in the browser, so
		// a wait before the first poll is pure latency they eat every run. The
		// wait belongs between polls (loop bottom). A cancelled context must
		// fail closed here without an HTTP call: a clean ctx.Err().
		if err := ctx.Err(); err != nil {
			return err
		}
		var tok struct {
			IDToken     string `json:"id_token"`
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if err := c.postFormJSON(ctx, flow.TokenURL, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {auth.DeviceCode},
			"client_id":   {flow.ClientID},
		}, &tok); err != nil {
			return fmt.Errorf("token poll: %w", err)
		}
		switch tok.Error {
		case "":
			idToken = tok.IDToken
			break poll
		case "authorization_pending":
			// keep waiting
		case "slow_down":
			interval += time.Second
		default:
			return loginRefused(flow.Issuer, tok.Error, tok.Description)
		}
		// Wait between polls (never before the first); cancellation during the
		// wait still returns ctx.Err().
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	if idToken == "" {
		return fmt.Errorf("login timed out (device code expired)")
	}
	user, err := c.checkin(ctx, map[string]any{"id_token": idToken})
	if err != nil {
		return err
	}
	// Register this workstation and keep the long-lived device
	// credential so later sessions re-establish without a browser round-trip.
	// Best-effort: an older server without device tokens still logs in.
	host, _ := os.Hostname()
	var enrolled struct {
		DeviceToken string `json:"device_token"`
	}
	code, err := c.doJSON(ctx, http.MethodPost, "/v1/enroll", "", map[string]any{
		"id_token": idToken,
		"device":   map[string]string{"name": "strazactl@" + host, "platform": runtime.GOOS, "fingerprint": "cli:" + host},
		// A credential for a person (openapi 0.114.0): it opens strazactl and
		// console sessions and no coding harness session.
		"client_kind": "human",
	}, &enrolled)
	if err == nil && code == http.StatusOK && enrolled.DeviceToken != "" {
		creds, err := c.loadCreds()
		if err != nil {
			return err
		}
		creds.DeviceToken = enrolled.DeviceToken
		if err := c.saveCreds(creds); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(w, "note: no device credential issued. Sessions will need `strazactl login` after expiry")
	}
	if user != "" {
		fmt.Fprintf(w, "Logged in as %s.\n", user)
		return nil
	}
	fmt.Fprintln(w, "Logged in.")
	return nil
}

// loginRefused words a token endpoint refusal for the operator: which
// identity provider refused the device login, its error code with the
// description when it sent one, and what to do next.
func loginRefused(issuer, code, description string) error {
	why := "the code " + code
	if description != "" {
		why += " (" + description + ")"
	}
	return fmt.Errorf("login failed: the identity provider at %s refused the device login with %s. Run `strazactl login` once more, and if it fails the same way check that provider's logs for the code", issuer, why)
}

// reauth re-establishes the session: refresh with the current session token,
// else fall back to the device credential. Only when both fail does
// the CLI demand an interactive login.
func (c *Client) reauth(ctx context.Context, creds credentials) error {
	_, refreshErr := c.checkin(ctx, map[string]any{"session_token": creds.SessionToken})
	if refreshErr == nil {
		return nil
	}
	if creds.DeviceToken == "" {
		return reauthError("session refresh failed", refreshErr)
	}
	if _, err := c.checkin(ctx, map[string]any{"device_token": creds.DeviceToken}); err != nil {
		return reauthError("session re-establish failed", err)
	}
	return nil
}

// reauthError words a failed re-authentication with the advice to log in. A
// refused redirect stands alone, as transportError returns it, because a new
// login would meet the same redirect.
func reauthError(what string, err error) error {
	var redirect *sameorigin.RedirectError
	if errors.As(err, &redirect) {
		return redirect
	}
	return fmt.Errorf("%s (run `strazactl login`): %w", what, err)
}

// checkin starts or refreshes the strazactl session, stores credentials and
// answers the username the server checked in, empty when it names none.
func (c *Client) checkin(ctx context.Context, auth map[string]any) (string, error) {
	body := map[string]any{
		"harness":     map[string]string{"name": "strazactl", "version": version.Version},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
		// The same build stamp the straza client sends (spec/attestation
		// v1beta1), so an admin CLI session names its build too.
		"client": map[string]string{"version": version.Version, "commit": version.Commit},
	}
	for k, v := range auth {
		body[k] = v
	}
	var out struct {
		SessionID    string `json:"session_id"`
		SessionToken string `json:"session_token"`
		DeviceToken  string `json:"device_token"`
		User         string `json:"user"`
		Error        string `json:"error"`
	}
	code, err := c.doJSON(ctx, http.MethodPost, "/v1/checkin", "", body, &out)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("checkin refused: %s", out.Error)
	}
	// Carry the device credential forward: every refresh rewrites the file,
	// and dropping it would silently bring back a login per expiry. A
	// device_token in the answer is a renewal (the server judged the
	// presented credential past half its life) and replaces the old one.
	prev, _ := c.loadCreds()
	device := prev.DeviceToken
	if out.DeviceToken != "" {
		device = out.DeviceToken
	}
	return out.User, c.saveCreds(credentials{
		Server: c.Base, SessionToken: out.SessionToken, SessionID: out.SessionID,
		DeviceToken: device,
	})
}

// Do performs an authenticated API call with a JSON body and decodes the
// answer into out. See send for the credential and the renewal.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	buf, err := c.send(ctx, method, path, func(bearer string) ([]byte, int, error) {
		return c.doRaw(ctx, method, path, bearer, body)
	})
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(buf, out)
}

// DoBytes performs an authenticated call and returns the raw response body
// (e.g. a YAML export). See send for the credential and the renewal.
func (c *Client) DoBytes(ctx context.Context, method, path string) ([]byte, error) {
	return c.send(ctx, method, path, func(bearer string) ([]byte, int, error) {
		return c.doRaw(ctx, method, path, bearer, nil)
	})
}

// DoRawBody performs an authenticated call with a pre-encoded body and an
// explicit content type (e.g. YAML upload) and decodes the answer into out.
// See send for the credential and the renewal.
func (c *Client) DoRawBody(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	buf, err := c.send(ctx, method, path, func(bearer string) ([]byte, int, error) {
		return c.sendBytes(ctx, method, path, bearer, contentType, body)
	})
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(buf, out)
}

// send is sendCode with an answer of 400 or more turned into the server's
// error sentence. Do, DoBytes and DoRawBody all go through it.
func (c *Client) send(ctx context.Context, method, path string, attempt func(bearer string) ([]byte, int, error)) ([]byte, error) {
	buf, code, err := c.sendCode(ctx, method, path, attempt)
	if err != nil {
		return nil, err
	}
	return buf, answerError(buf, code)
}

// sendCode is the one authenticated path, and it returns the answer with its
// status. attempt sends the request with the bearer it is given. An admin
// API token in APIToken goes out as it is: it serves the admin API only, and
// a refusal of it is final because there is no session to renew. Otherwise
// the call goes out on the stored login, and inside a coding agent a call
// that changes Straza is refused first (see guardRefuses). The login's
// session token is renewed before the call when it is about to expire, and
// once more after a 401, each time through reauth, which falls back to the
// device token.
func (c *Client) sendCode(ctx context.Context, method, path string, attempt func(bearer string) ([]byte, int, error)) ([]byte, int, error) {
	if c.APIToken != "" {
		if !strings.HasPrefix(path, "/v1/admin/") {
			return nil, 0, errTokenOffAdmin
		}
		return attempt(c.APIToken)
	}
	if c.guardRefuses(method, path) {
		return nil, 0, agentGuardError(c.AgentMarker)
	}
	creds, err := c.loadCreds()
	if err != nil {
		return nil, 0, err
	}
	if tokenExpiringSoon(creds.SessionToken) {
		if creds, err = c.renew(ctx, creds); err != nil {
			return nil, 0, err
		}
	}
	buf, code, err := attempt(creds.SessionToken)
	if err == nil && code == http.StatusUnauthorized {
		if creds, err = c.renew(ctx, creds); err != nil {
			return nil, 0, err
		}
		buf, code, err = attempt(creds.SessionToken)
	}
	return buf, code, err
}

// renew re-establishes the session through reauth and returns the
// credentials it stored.
func (c *Client) renew(ctx context.Context, creds credentials) (credentials, error) {
	if err := c.reauth(ctx, creds); err != nil {
		return credentials{}, err
	}
	return c.loadCreds()
}

// answerError maps an answer of 400 or more to the server's error sentence,
// or to the bare status when the body carries none. It returns nil below 400.
func answerError(buf []byte, code int) error {
	if code < 400 {
		return nil
	}
	var apiErr struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(buf, &apiErr)
	if apiErr.Error == "" {
		return fmt.Errorf("HTTP %d", code)
	}
	return errors.New(apiErr.Error)
}

func (c *Client) sendBytes(ctx context.Context, method, path, bearer, contentType string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", contentType)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.exchange(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	buf, err := c.readAnswer(req, resp.Body)
	return buf, resp.StatusCode, err
}

func (c *Client) doRaw(ctx context.Context, method, path, bearer string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.exchange(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	buf, err := c.readAnswer(req, resp.Body)
	return buf, resp.StatusCode, err
}

// doJSON is doRaw + decode without auth handling (used pre-login).
func (c *Client) doJSON(ctx context.Context, method, path, bearer string, body, out any) (int, error) {
	buf, code, err := c.doRaw(ctx, method, path, bearer, body)
	if err != nil {
		return 0, err
	}
	if out != nil && len(buf) > 0 {
		if err := json.Unmarshal(buf, out); err != nil {
			return code, fmt.Errorf("unexpected response from %s: %w", path, err)
		}
	}
	return code, nil
}

// postFormJSON posts a form to a strazad path or, when given an absolute
// URL, to a discovered IdP endpoint.
func (c *Client) postFormJSON(ctx context.Context, pathOrURL string, form url.Values, out any) error {
	target := pathOrURL
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = c.Base + target
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target,
		bytes.NewReader([]byte(form.Encode())))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return c.transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return json.NewDecoder(resp.Body).Decode(out)
}

// exchange sends a request to strazad and words a failure. Go words a
// timeout the same before and after the connection is made, so exchange
// traces whether the request went out whole on its last connection. A
// timeout after that is a request that got no answer, whose change may
// still run on strazad, or on whatever holds the connection in front of it.
// Every other failure goes to transportError.
func (c *Client) exchange(req *http.Request) (*http.Response, error) {
	var sent atomic.Bool
	trace := &httptrace.ClientTrace{
		GetConn:      func(string) { sent.Store(false) },
		WroteRequest: func(info httptrace.WroteRequestInfo) { sent.Store(info.Err == nil) },
	}
	resp, err := c.HTTP.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err == nil {
		return resp, nil
	}
	var uerr *url.Error
	if sent.Load() && errors.As(err, &uerr) && uerr.Timeout() {
		return nil, c.unanswered(req, "no answer", err)
	}
	return nil, c.transportError(err)
}

// readAnswer reads the body of an answer to req. A read that timed out
// follows a request that went out in full, so it is worded as a request
// that got no complete answer.
func (c *Client) readAnswer(req *http.Request, body io.Reader) ([]byte, error) {
	buf, err := io.ReadAll(body)
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return nil, c.unanswered(req, "no complete answer", err)
	}
	return buf, err
}

// unanswered words req, which went out in full and got answer, "no answer"
// or "no complete answer", within the client's timeout. A change may still
// run on strazad, so its sentence says how to check before a retry. A read
// changes nothing, so its sentence says a retry is safe.
func (c *Client) unanswered(req *http.Request, answer string, err error) error {
	next := "The change may still be running on strazad. " +
		"Check whether it took effect with the matching strazactl show or list command before you run this command again"
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		next = "A read changes nothing on strazad, so it is safe to run the command again"
	}
	return &unansweredError{err: err, sentence: fmt.Sprintf("strazactl sent the request %s %s to strazad at %s in full and got %s within %s. %s",
		req.Method, req.URL.Path, c.Base, answer, c.HTTP.Timeout, next)}
}

// unansweredError is a request that went out in full and got no complete
// answer in time. It prints its sentence alone and unwraps to the transport's
// error, so a caller that tells a timeout by the error chain, as the drafts
// calls do, still finds one.
type unansweredError struct {
	sentence string
	err      error
}

func (e *unansweredError) Error() string { return e.sentence }
func (e *unansweredError) Unwrap() error { return e.err }

// transportError words a request that got no answer to use. A redirect the
// client refused came from the server, so its own sentence stands alone,
// and any other failure means strazad could not be reached at c.Base.
func (c *Client) transportError(err error) error {
	var redirect *sameorigin.RedirectError
	if errors.As(err, &redirect) {
		return redirect
	}
	return fmt.Errorf("strazad unreachable at %s: %w", c.Base, err)
}

// tokenExpiringSoon reports whether the JWT expires within 60 s. Parse-only;
// signature verification is the server's job.
func tokenExpiringSoon(raw string) bool {
	parts := bytes.Split([]byte(raw), []byte("."))
	if len(parts) != 3 {
		return true
	}
	payload, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
	if err != nil {
		return true
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return true
	}
	return time.Until(time.Unix(claims.Exp, 0)) < 60*time.Second
}
