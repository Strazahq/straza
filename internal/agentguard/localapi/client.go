package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrUnreachable marks a failure to reach the service: no socket, a socket
// owned by someone other than the service, or a connection that failed
// before an answer. The thin clients answer it with the "background service
// isn't running" sentence.
var ErrUnreachable = errors.New("the Straza background service is unreachable")

// ErrTimeout marks a service that took a connection but gave no answer
// within the route's timeout. The thin clients deny with their own sentence
// for it, because the service is running.
var ErrTimeout = errors.New("the Straza background service did not answer in time")

// RefusalError is an answer that is not a success. Msg is the service's sentence.
type RefusalError struct {
	Status int
	Msg    string
}

func (e *RefusalError) Error() string { return e.Msg }

// Client calls the service over its socket, a named pipe on Windows. It
// checks before every connection that the socket or pipe belongs to the
// service's account, or to the caller in user mode, so one someone else put
// at the path gets nothing.
type Client struct {
	socket string
	user   bool
	http   *http.Client
}

// NewClient returns a client for home, the value of STRAZA_HOME (see Paths).
func NewClient(home string) *Client {
	socket, _, user := Paths(home)
	c := &Client{socket: socket, user: user}
	c.http = &http.Client{
		Transport: &http.Transport{
			DialContext:       c.dial,
			DisableKeepAlives: true,
		},
		// The service never redirects, so a redirect is answered as the
		// failure it is and never followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c
}

// Socket returns the socket path the client dials.
func (c *Client) Socket() string { return c.socket }

// HTTPClient returns an HTTP client whose connections go to the service's
// socket after the owner check. URLs are BaseURL plus a route. It carries no
// overall timeout, because the MCP routes stream.
func (c *Client) HTTPClient() *http.Client { return c.http }

// Status returns the caller's enrollment, sessions and spool.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var out Status
	err := c.call(ctx, ShortTimeout, http.MethodGet, RouteStatus, nil, &out)
	return out, err
}

// StartSession starts or reuses the session of one harness window.
func (c *Client) StartSession(ctx context.Context, req SessionRequest) (SessionAnswer, error) {
	var out SessionAnswer
	err := c.call(ctx, SessionTimeout, http.MethodPost, RouteSessions, req, &out)
	return out, err
}

// Decide asks the service to decide one hook event.
func (c *Client) Decide(ctx context.Context, req DecideRequest) (DecideAnswer, error) {
	var out DecideAnswer
	err := c.call(ctx, DecideTimeout, http.MethodPost, RouteDecide, req, &out)
	return out, err
}

// Capture hands the service conversation text to record.
func (c *Client) Capture(ctx context.Context, req CaptureRequest) error {
	return c.call(ctx, ShortTimeout, http.MethodPost, RouteCapture, req, nil)
}

// Keygen makes the caller's agent key and returns its public half.
func (c *Client) Keygen(ctx context.Context, req KeygenRequest) (KeygenAnswer, error) {
	var out KeygenAnswer
	err := c.call(ctx, SessionTimeout, http.MethodPost, RouteKeygen, req, &out)
	return out, err
}

// Enroll enrolls the caller and hands every streamed event to on, the done
// and error events included. It returns the result of the done event, or a
// RefusalError carrying the sentence of the error event.
func (c *Client) Enroll(ctx context.Context, req EnrollRequest, on func(EnrollEvent)) (EnrollResult, error) {
	resp, err := c.send(ctx, http.MethodPost, RouteEnroll, req)
	if err != nil {
		return EnrollResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return EnrollResult{}, refusal(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev EnrollEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return EnrollResult{}, fmt.Errorf("read the service's enroll answer: %w", err)
		}
		on(ev)
		switch {
		case ev.Event == EnrollError:
			return EnrollResult{}, &RefusalError{Status: http.StatusOK, Msg: ev.Error}
		case ev.Event == EnrollDone && ev.Result != nil:
			return *ev.Result, nil
		}
	}
	if err := sc.Err(); err != nil {
		return EnrollResult{}, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return EnrollResult{}, fmt.Errorf("%w: the enroll answer ended without a result", ErrUnreachable)
}

// call sends one JSON request under timeout and decodes the answer into out.
func (c *Client) call(ctx context.Context, timeout time.Duration, method, route string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := c.send(ctx, method, route, in)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusOK:
	case out == nil && resp.StatusCode == http.StatusNoContent:
		return nil
	default:
		return refusal(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("read the service's answer to %s: %w", route, err)
	}
	return nil
}

func (c *Client) send(ctx context.Context, method, route string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+route, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	switch {
	case err == nil:
		return resp, nil
	case errors.Is(err, ErrUnreachable):
		return nil, err
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("%w: %w", ErrTimeout, err)
	}
	return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
}

// refusal reads the service's sentence from an answer that is not a
// success. A redirect or another unexpected status counts as a refusal.
func refusal(resp *http.Response) error {
	var r Refusal
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r)
	if r.Error == "" {
		r.Error = fmt.Sprintf("The Straza background service answered HTTP %d and gave no reason.", resp.StatusCode)
	}
	return &RefusalError{Status: resp.StatusCode, Msg: r.Error}
}
