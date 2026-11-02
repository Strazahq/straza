package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/trace"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/sameorigin"
)

// maxReplayBody is the largest request body the transport keeps in memory
// to send it again after a nonce challenge. A larger body that cannot be
// rewound is sent once.
const maxReplayBody = 8 << 20

// nonceCache holds the newest DPoP-Nonce strazad sent and when it arrived.
type nonceCache struct {
	now func() time.Time

	mu    sync.Mutex
	value string
	at    time.Time
}

func (n *nonceCache) get() (string, time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.value, n.at
}

// keep stores the nonce of an answer, when it carries one.
func (n *nonceCache) keep(h http.Header) {
	v := h.Get(dpop.HeaderNonce)
	if v == "" {
		return
	}
	n.mu.Lock()
	n.value, n.at = v, n.now()
	n.mu.Unlock()
}

// keyAttempt is one key check-in in flight. The transport signs counter as
// ctr with nonce, which arrived at nonceAt and which the caller chose and
// recorded before the request left, and it records whether the request was
// written, which tells a lost answer from a request that never left.
type keyAttempt struct {
	counter int64
	nonce   string
	nonceAt time.Time
	sent    atomic.Bool
}

type ctxKey int

const (
	attemptKey ctxKey = iota
	bodyTokenKey
)

// withAttempt marks the requests of ctx as the key check-in att.
func withAttempt(ctx context.Context, att *keyAttempt) context.Context {
	return context.WithValue(ctx, attemptKey, att)
}

// withBodyToken names the session token a request carries in its body, as
// a refresh does, so its proof hashes it as ath.
func withBodyToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, bodyTokenKey, token)
}

// proofTransport signs a DPoP proof for every request it sends, with the
// newest nonce and htu from the server URL's origin. A request whose
// Authorization names a token under Bearer or DPoP is sent under DPoP with
// a proof whose ath hashes that token, and so is a request whose context
// names a body token. A key check-in's proof signs its counter as ctr. On
// the nonce challenge the request is signed again and sent once more,
// except a key check-in, whose caller takes a new counter first.
type proofTransport struct {
	base   http.RoundTripper
	key    ed25519.PrivateKey
	origin string
	nonces *nonceCache
}

// RoundTrip sends req with a proof, and once more under the server's
// nonce when the answer is the nonce challenge.
func (t *proofTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	getBody, once, err := replayable(req)
	if err != nil {
		return nil, err
	}
	resp, err := t.send(req, getBody, once)
	if err != nil || !nonceChallenge(resp) || getBody == nil || req.Context().Value(attemptKey) != nil {
		return resp, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return t.send(req, getBody, nil)
}

// send signs and sends one copy of req. The body comes from getBody, or
// from once for a body that cannot be sent twice.
func (t *proofTransport) send(req *http.Request, getBody func() (io.ReadCloser, error), once io.ReadCloser) (*http.Response, error) {
	ctx := req.Context()
	out := req.Clone(ctx)
	out.Body, out.GetBody = once, getBody
	if getBody != nil {
		body, err := getBody()
		if err != nil {
			return nil, err
		}
		out.Body = body
	}
	token, _ := ctx.Value(bodyTokenKey).(string)
	if scheme, tok, ok := strings.Cut(out.Header.Get("Authorization"), " "); ok && tok != "" &&
		(strings.EqualFold(scheme, "Bearer") || strings.EqualFold(scheme, dpop.Scheme)) {
		token = tok
		out.Header.Set("Authorization", dpop.Scheme+" "+tok)
	}
	nonce, _ := t.nonces.get()
	htu := t.origin + req.URL.EscapedPath()
	var proof string
	var err error
	if att, ok := ctx.Value(attemptKey).(*keyAttempt); ok {
		proof, err = dpop.NewCheckinProof(t.key, htu, att.nonce, att.counter, time.Now())
		out = out.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			WroteRequest: func(w httptrace.WroteRequestInfo) {
				if w.Err == nil {
					att.sent.Store(true)
				}
			},
		}))
	} else {
		proof, err = dpop.NewProof(t.key, req.Method, htu, nonce, token, time.Now())
	}
	if err != nil {
		if out.Body != nil {
			_ = out.Body.Close()
		}
		return nil, fmt.Errorf("sign the DPoP proof: %w", err)
	}
	out.Header.Set(dpop.HeaderProof, proof)
	resp, err := t.base.RoundTrip(out)
	if err == nil {
		t.nonces.keep(resp.Header)
	}
	return resp, err
}

// replayable returns a function that yields req's body for each send. A
// body without GetBody is read into memory up to maxReplayBody. A larger
// one comes back as once, to be sent a single time.
func replayable(req *http.Request) (func() (io.ReadCloser, error), io.ReadCloser, error) {
	switch {
	case req.Body == nil || req.Body == http.NoBody:
		return func() (io.ReadCloser, error) { return http.NoBody, nil }, nil, nil
	case req.GetBody != nil:
		_ = req.Body.Close()
		return req.GetBody, nil, nil
	}
	head, err := io.ReadAll(io.LimitReader(req.Body, maxReplayBody+1))
	if err != nil {
		_ = req.Body.Close()
		return nil, nil, err
	}
	if len(head) > maxReplayBody {
		return nil, struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), req.Body), req.Body}, nil
	}
	_ = req.Body.Close()
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(head)), nil }, nil, nil
}

// nonceChallenge reports whether resp is the 401 that asks for a fresh
// nonce.
func nonceChallenge(resp *http.Response) bool {
	return resp.StatusCode == http.StatusUnauthorized &&
		strings.Contains(resp.Header.Get("WWW-Authenticate"), `error="use_dpop_nonce"`) &&
		resp.Header.Get(dpop.HeaderNonce) != ""
}

// originOf returns the scheme://host of server, the origin every htu names.
func originOf(server string) string {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(server, "/")
	}
	return u.Scheme + "://" + u.Host
}

// signedClient is an agentguard client for server whose every request
// carries a proof from t. A request that carries a credential follows a
// redirect only within its own origin, as agentguard.NewClient does.
func signedClient(server string, t *proofTransport) *agentguard.Client {
	return &agentguard.Client{Base: strings.TrimRight(server, "/"), HTTP: &http.Client{
		Timeout: requestTimeout, CheckRedirect: sameorigin.CheckCredentialed, Transport: t,
	}}
}

// answer is one strazad answer read whole.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// postJSON sends body as JSON to path on c's server and returns the answer,
// the nonce challenge included. The answer's status and request id reach
// the trace.Call of ctx, as agentguard's client reports them.
func postJSON(ctx context.Context, c *agentguard.Client, path string, body any) (answer, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return answer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(raw))
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return answer{}, fmt.Errorf("%s %s: %w", req.Method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	trace.CallFrom(ctx).Observe(resp.StatusCode, resp.Header.Get("X-Request-Id"))
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return answer{}, fmt.Errorf("read the answer to %s %s: %w", req.Method, path, err)
	}
	return answer{status: resp.StatusCode, header: resp.Header, body: buf}, nil
}

// challenge reports whether a is the nonce challenge.
func (a answer) challenge() bool {
	return nonceChallenge(&http.Response{StatusCode: a.status, Header: a.header})
}

// refusal returns a as the *agentguard.StatusError agentguard's client
// returns for an answer at or above 400, with strazad's sentence.
func (a answer) refusal(path string) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(a.body, &body)
	if body.Error == "" {
		body.Error = fmt.Sprintf("HTTP %d", a.status)
	}
	return fmt.Errorf("%s: %w", path, &agentguard.StatusError{Status: a.status, Msg: body.Error})
}
