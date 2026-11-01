package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/dpop"
)

// maxAnswerBytes caps how much of an answer body the client reads.
const maxAnswerBytes = 1 << 20

// client sends the requests of one state. It signs a proof for each, keeps
// the newest DPoP-Nonce of any answer in the state, and prints every answer.
type client struct {
	http *http.Client
	key  ed25519.PrivateKey
	st   *state
	out  io.Writer
	now  func() time.Time
}

// newClient returns a client for st that signs with key and prints to out.
func newClient(st *state, key ed25519.PrivateKey, out io.Writer) *client {
	return &client{http: &http.Client{Timeout: 30 * time.Second}, key: key, st: st, out: out, now: time.Now}
}

// request is one call to strazad.
type request struct {
	method    string
	path      string
	body      []byte
	authToken string // sent in Authorization when set
	bearer    bool   // authToken goes under Bearer, and no proof is sent
	proofATH  string // the token the proof's ath hashes, "" for none
	counter   int64  // the key check-in counter the proof signs as ctr, 0 for none
}

// answer is what strazad sent back.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// nonceChallenge reports whether a is the 401 that asks for a fresh nonce.
func (a answer) nonceChallenge() bool {
	return a.status == http.StatusUnauthorized &&
		strings.Contains(a.header.Get("WWW-Authenticate"), `error="use_dpop_nonce"`) &&
		a.header.Get(dpop.HeaderNonce) != ""
}

// build returns the HTTP request for r. It carries proof when proof is set,
// a fresh proof under nonce when it is not, and no proof on the bearer path.
func (c *client) build(r request, nonce, proof string) (*http.Request, string, error) {
	target := strings.TrimRight(c.st.Server, "/") + r.path
	req, err := http.NewRequest(r.method, target, bytes.NewReader(r.body)) // #nosec G704 -- a test tool that calls the server its operator names with -server
	if err != nil {
		return nil, "", fmt.Errorf("build %s %s: %w", r.method, r.path, err)
	}
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !r.bearer {
		if proof == "" {
			proof, err = c.sign(r, proofURL(req.URL), nonce)
			if err != nil {
				return nil, "", fmt.Errorf("sign the proof: %w", err)
			}
		}
		req.Header.Set(dpop.HeaderProof, proof)
	}
	if r.authToken != "" {
		scheme := dpop.Scheme
		if r.bearer {
			scheme = "Bearer"
		}
		req.Header.Set("Authorization", scheme+" "+r.authToken)
	}
	return req, proof, nil
}

// sign makes the proof of r for htu under nonce: a check-in proof that signs
// the counter as ctr on a key check-in, a plain proof otherwise.
func (c *client) sign(r request, htu, nonce string) (string, error) {
	if r.counter > 0 {
		return dpop.NewCheckinProof(c.key, htu, nonce, r.counter, c.now())
	}
	return dpop.NewProof(c.key, r.method, htu, nonce, r.proofATH, c.now())
}

// proofURL is the htu of a proof: the request URL without query and fragment.
func proofURL(u *url.URL) string {
	v := *u
	v.RawQuery, v.Fragment, v.RawFragment = "", "", ""
	return v.String()
}

// do sends r, and once more under the server's nonce when the first answer
// is the nonce challenge. It returns the last answer and the proof it sent.
func (c *client) do(r request) (answer, string, error) {
	req, proof, err := c.build(r, c.st.Nonce, "")
	if err != nil {
		return answer{}, "", err
	}
	a, err := c.send(req)
	if err != nil || !a.nonceChallenge() {
		return a, proof, err
	}
	fmt.Fprintln(c.out, "-- the server asked for a fresh nonce, so the request is signed again with it")
	if req, proof, err = c.build(r, c.st.Nonce, ""); err != nil {
		return answer{}, "", err
	}
	a, err = c.send(req)
	return a, proof, err
}

// send performs req, keeps the answer's DPoP-Nonce and prints the answer.
func (c *client) send(req *http.Request) (answer, error) {
	resp, err := c.http.Do(req) // #nosec G704 -- the request build made for the -server the operator named
	if err != nil {
		return answer{}, fmt.Errorf("%s %s did not get an answer: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return answer{}, fmt.Errorf("read the answer to %s %s: %w", req.Method, req.URL.Path, err)
	}
	if n := resp.Header.Get(dpop.HeaderNonce); n != "" {
		c.st.Nonce = n
	}
	a := answer{status: resp.StatusCode, header: resp.Header, body: body}
	c.print(req, a)
	return a, nil
}

// printedHeaders are the answer headers worth showing.
var printedHeaders = []string{dpop.HeaderNonce, "WWW-Authenticate", "X-Request-Id"}

// print writes the status, the headers worth showing and the JSON body with
// every token field redacted.
func (c *client) print(req *http.Request, a answer) {
	fmt.Fprintf(c.out, "%s %s -> %d %s\n", req.Method, req.URL.Path, a.status, http.StatusText(a.status))
	for _, h := range printedHeaders {
		if v := a.header.Get(h); v != "" {
			fmt.Fprintf(c.out, "%s: %s\n", h, v)
		}
	}
	var v any
	if err := json.Unmarshal(a.body, &v); err != nil {
		fmt.Fprintf(c.out, "(a body of %d bytes that is not JSON)\n", len(a.body))
		return
	}
	pretty, err := json.MarshalIndent(redact(v), "", "  ")
	if err != nil {
		return
	}
	fmt.Fprintln(c.out, string(pretty))
}

// redact replaces the value of every field whose name ends in _token, at any
// depth, so no token reaches the terminal.
func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, inner := range t {
			if strings.HasSuffix(k, "_token") {
				t[k] = "(redacted)"
				continue
			}
			t[k] = redact(inner)
		}
	case []any:
		for i, inner := range t {
			t[i] = redact(inner)
		}
	}
	return v
}
