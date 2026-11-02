package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/strazahq/straza/internal/sameorigin"
)

// Client talks to strazad on an admin API token.
type Client struct {
	Base string
	HTTP *http.Client
	// APIToken is the admin API token that every authenticated call sends
	// as its bearer. A refusal of it is final, because there is nothing to
	// renew.
	APIToken string
}

// NewClient builds a client for the given strazad base URL. A request that
// carries a credential, in the Authorization header or in the body, follows
// a redirect only within its own origin (sameorigin.CheckCredentialed).
func NewClient(base string) *Client {
	return &Client{
		Base: base,
		HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: sameorigin.CheckCredentialed},
	}
}

// Do performs an authenticated API call with a JSON body and decodes the
// answer into out.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	buf, err := checked(c.doRaw(ctx, method, path, body))
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(buf, out)
}

// DoBytes performs an authenticated call and returns the raw response body
// (e.g. a YAML export).
func (c *Client) DoBytes(ctx context.Context, method, path string) ([]byte, error) {
	return checked(c.doRaw(ctx, method, path, nil))
}

// DoRawBody performs an authenticated call with a pre-encoded body and an
// explicit content type (e.g. YAML upload) and decodes the answer into out.
func (c *Client) DoRawBody(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	buf, err := checked(c.sendBytes(ctx, method, path, contentType, body))
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(buf, out)
}

// checked is the answer of one call with a status of 400 or more turned into
// the server's error sentence. Do, DoBytes and DoRawBody all go through it.
func checked(buf []byte, code int, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return buf, answerError(buf, code)
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

// sendBytes sends body with its content type on the admin API token and
// answers the body and the status as the server sent them.
func (c *Client) sendBytes(ctx context.Context, method, path, contentType string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", contentType)
	if c.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIToken)
	}
	resp, err := c.exchange(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	buf, err := c.readAnswer(req, resp.Body)
	return buf, resp.StatusCode, err
}

// doRaw sends body as JSON, or no body when it is nil, on the admin API
// token and answers the body and the status as the server sent them. It is
// the one authenticated path besides sendBytes.
func (c *Client) doRaw(ctx context.Context, method, path string, body any) ([]byte, int, error) {
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
	if c.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIToken)
	}
	resp, err := c.exchange(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	buf, err := c.readAnswer(req, resp.Body)
	return buf, resp.StatusCode, err
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
