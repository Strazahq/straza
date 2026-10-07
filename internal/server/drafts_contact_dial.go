package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/version"
)

// The sentences of a contact that did not answer. A reason comes
// from the closed list below and never quotes a byte the server sent.
const (
	contactAddressRefusal = "Straza does not contact %s: it resolves to %s, %s address, where a request from strazad reaches strazad's own host or its cloud's metadata. " +
		"Publish the server if you mean it, or give it an address outside those ranges."
	contactFailed          = "Straza contacted %s and it did not answer as an MCP server: %s. Check the address in the draft."
	contactNeedsCredential = "Straza contacted %s and it did not answer as an MCP server: it answered with HTTP %d, which asks for a credential, and Straza contacts a proposed server without one. " +
		"Publish the server and store its credential to read its tools."
	contactRedirect = "Straza contacted %s and it answered with a redirect, which Straza does not follow, because a credential must go only where the draft says."
)

// The closed list of reasons a contact failed.
const (
	reasonStatus         = "it answered with HTTP %d"
	reasonContentType    = "it answered with the content type %s, which is neither JSON nor an event stream"
	reasonNoContentType  = "it answered with an unreadable content type, which is neither JSON nor an event stream"
	reasonTimeout        = "it did not answer within 10 seconds"
	reasonUntrusted      = "its TLS certificate is not trusted"
	reasonOtherHost      = "its TLS certificate names another host"
	reasonExpired        = "its TLS certificate has expired"
	reasonHandshake      = "the TLS handshake failed"
	reasonRefused        = "the connection was refused"
	reasonNoName         = "the name does not resolve"
	reasonNotInitialize  = "its answer is not an MCP initialize result"
	reasonNotToolList    = "its answer to tools/list is not a list of tools"
	reasonTooLarge       = "its answer is larger than 1 MiB"
	reasonConnectionLost = "the connection failed before it answered"
)

// The outcomes a draft.contact record names.
const (
	outcomeAnswered = "answered"
	outcomeRefused  = "refused"
	outcomeFailed   = "failed"
)

var (
	errContactRefused = errors.New("contact: the address is one Straza does not contact")
	errContactFault   = errors.New("contact: the server answered with something Straza does not read")
	// mediaTypeRe is a media type Straza may quote: token characters only.
	mediaTypeRe = regexp.MustCompile(`^[a-z0-9!#$&^_.+-]+/[a-z0-9!#$&^_.+-]+$`)
)

// contactDialer is how Contact reaches a proposed server: resolve answers
// a host's addresses, refuse names the class of an address Straza does not
// dial, roots are the trusted certificate authorities, nil for the
// system's, and limit and timeout bound one contact.
type contactDialer struct {
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	refuse  func(netip.Addr) string
	roots   *x509.CertPool
	limit   int64
	timeout time.Duration
}

// newContactDialer is the dialer of the contact route: the system's
// resolver and roots, the classes of drafts.ContactClass, 1 MiB and 10
// seconds.
func newContactDialer() contactDialer {
	return contactDialer{
		resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		refuse:  drafts.ContactClass,
		limit:   1 << 20,
		timeout: 10 * time.Second,
	}
}

// contactAnswer is what a server answered a contact: the host as dialed,
// in its ASCII form, when, its name and version, and its tools.
type contactAnswer struct {
	host   string
	at     time.Time
	server contactedServer
	tools  []contactedTool
}

// contactFault is why a contact did not answer: the status and sentence
// of the route's answer and the outcome its record names.
type contactFault struct {
	status   int
	outcome  string
	sentence string
}

// contact opens one MCP connection to address with no credential, reads
// the initialize result and every page of tools/list, and closes it. The
// address loses any user information first, because the HTTP client would
// send it as a credential. Every address it dials, a name's resolved
// addresses included, meets d.refuse in the dialer's Control hook, the
// transport has no proxy, follows no redirect and verifies TLS, and the
// whole contact reads at most d.limit bytes within d.timeout.
func (d contactDialer) contact(ctx context.Context, address string) (contactAnswer, *contactFault) {
	u, err := url.Parse(address)
	host := ""
	if err == nil {
		u.User = nil
		host = drafts.ASCIIHost(u.Hostname())
	}
	run := &contactRun{d: d, host: host}
	got := contactAnswer{host: host, at: time.Now()}
	if err != nil || host == "" {
		return got, run.failed(reasonNoName)
	}
	transport := run.transport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: run, Timeout: d.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errContactFault }}
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	run.ctx = ctx
	session, err := mcp.NewClient(&mcp.Implementation{Name: "strazad-contact", Version: version.Version}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: u.String(), HTTPClient: client, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		return got, run.fault(ctx, reasonNotInitialize)
	}
	defer func() {
		run.finish()
		_ = session.Close()
	}()
	if res := session.InitializeResult(); res != nil && res.ServerInfo != nil {
		got.server = contactedServer{Name: res.ServerInfo.Name, Version: res.ServerInfo.Version}
	}
	got.tools = []contactedTool{}
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			return got, run.fault(ctx, reasonNotToolList)
		}
		got.tools = append(got.tools, contactedTool{Name: t.Name, Description: t.Description,
			ReadOnly: t.Annotations != nil && t.Annotations.ReadOnlyHint})
	}
	return got, run.noted()
}

// contactRun is one contact under way: its dialer, the host, the bytes
// read so far, the first fault noted, which names the answer, and whether
// the tools were read, after which the close answers what it may.
type contactRun struct {
	d    contactDialer
	host string
	base http.RoundTripper
	// ctx ends at the contact's deadline. Every request ends with it, the
	// close of the MCP session included, which the SDK sends on a context
	// of its own.
	ctx  context.Context
	mu   sync.Mutex
	read int64
	note *contactFault
	done bool
}

// transport is the contact's HTTP transport: no proxy, every dial through
// dial, and TLS verified against the dialer's roots.
func (c *contactRun) transport() *http.Transport {
	t := &http.Transport{
		Proxy:                  nil,
		DialContext:            c.dial,
		TLSClientConfig:        &tls.Config{RootCAs: c.d.roots, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    c.d.timeout,
		ResponseHeaderTimeout:  c.d.timeout,
		MaxResponseHeaderBytes: 64 << 10,
		ForceAttemptHTTP2:      true,
	}
	c.base = t
	return t
}

// dial resolves address's host with the dialer's resolver and dials each
// address in turn, the dialer's Control hook checking every one. A refused
// address ends the dial, since the name is one Straza does not contact.
func (c *contactRun) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else if addrs, err = c.d.resolve(ctx, host); err != nil || len(addrs) == 0 {
		if ctx.Err() == nil {
			c.fail(reasonNoName)
		}
		return nil, errors.Join(errors.New("contact: the name does not resolve"), err)
	}
	dialer := net.Dialer{Control: c.control}
	var last error
	for _, a := range addrs {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		if errors.Is(err, errContactRefused) {
			return nil, err
		}
		last = err
	}
	return nil, last
}

// control is the dialer's Control hook: it refuses an address of a class
// Straza does not contact, read after resolution, so a name that resolves
// or rebinds to one is refused like a literal.
func (c *contactRun) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("contact: the address %s does not read: %w", address, err)
	}
	if class := c.d.refuse(ap.Addr()); class != "" {
		article := "a"
		if strings.HasPrefix(class, "u") {
			article = "an"
		}
		c.noteFault(&contactFault{status: http.StatusConflict, outcome: outcomeRefused,
			sentence: fmt.Sprintf(contactAddressRefusal, c.host, ap.Addr(), article+" "+class)})
		return errContactRefused
	}
	return nil
}

// RoundTrip sends req with no credential header and reads the answer: a
// redirect, a status other than 2xx, or a content type that is neither
// JSON nor an event stream ends the contact, and the body counts toward
// the limit.
func (c *contactRun) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	if c.ctx != nil {
		stop := context.AfterFunc(c.ctx, cancel)
		release := cancel
		cancel = func() { stop(); release() }
	}
	req = req.Clone(ctx)
	for _, h := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
		req.Header.Del(h)
	}
	resp, err := c.base.RoundTrip(req)
	if err != nil {
		c.noteFault(c.transportFault(ctx, err))
		cancel()
		return nil, err
	}
	if c.finished() {
		resp.Body = &contactBody{ReadCloser: resp.Body, run: c, release: cancel}
		return resp, nil
	}
	var fault *contactFault
	switch code := resp.StatusCode; {
	case code >= 300 && code < 400:
		fault = &contactFault{status: http.StatusBadGateway, outcome: outcomeFailed, sentence: fmt.Sprintf(contactRedirect, c.host)}
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		fault = &contactFault{status: http.StatusBadGateway, outcome: outcomeFailed, sentence: fmt.Sprintf(contactNeedsCredential, c.host, code)}
	case code < 200 || code >= 300:
		fault = c.reason(fmt.Sprintf(reasonStatus, code))
	case req.Method == http.MethodPost && code != http.StatusAccepted && code != http.StatusNoContent && resp.ContentLength != 0:
		if why := contentTypeReason(resp.Header.Get("Content-Type")); why != "" {
			fault = c.reason(why)
		}
	}
	if fault != nil {
		_ = resp.Body.Close()
		cancel()
		c.noteFault(fault)
		return nil, errContactFault
	}
	resp.Body = &contactBody{ReadCloser: resp.Body, run: c, release: cancel}
	return resp, nil
}

// contentTypeReason answers the reason for an answer's content type, or
// "" for JSON or an event stream. It quotes the media type only when it
// is token characters of at most 64 bytes.
func contentTypeReason(header string) string {
	mt, _, err := mime.ParseMediaType(header)
	switch {
	case err == nil && (mt == "application/json" || mt == "text/event-stream"):
		return ""
	case err == nil && len(mt) <= 64 && mediaTypeRe.MatchString(mt):
		return fmt.Sprintf(reasonContentType, mt)
	}
	return reasonNoContentType
}

// contactBody counts what the contact reads from an answer and ends the
// contact past the limit. Close releases the request's context.
type contactBody struct {
	io.ReadCloser
	run     *contactRun
	release context.CancelFunc
}

func (b *contactBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}

func (b *contactBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	c := b.run
	c.mu.Lock()
	c.read += int64(n)
	over := c.read > c.d.limit && !c.done
	c.mu.Unlock()
	switch {
	case over:
		c.fail(reasonTooLarge)
		return n, errContactFault
	case err != nil && !errors.Is(err, io.EOF):
		c.noteFault(c.transportFault(context.Background(), err))
	}
	return n, err
}

// transportFault answers the reason of a transport error, or nil for a
// refusal already noted.
func (c *contactRun) transportFault(ctx context.Context, err error) *contactFault {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var dns *net.DNSError
	var timeout net.Error
	switch {
	case errors.Is(err, errContactRefused), errors.Is(err, errContactFault):
		return nil
	case errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil, errors.As(err, &timeout) && timeout.Timeout():
		return c.reason(reasonTimeout)
	case errors.As(err, &unknown):
		return c.reason(reasonUntrusted)
	case errors.As(err, &hostname):
		return c.reason(reasonOtherHost)
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		return c.reason(reasonExpired)
	case tlsError(err):
		return c.reason(reasonHandshake)
	case errors.Is(err, syscall.ECONNREFUSED):
		return c.reason(reasonRefused)
	case errors.As(err, &dns):
		return c.reason(reasonNoName)
	}
	return c.reason(reasonConnectionLost)
}

// tlsError reports whether err came from the TLS handshake.
func tlsError(err error) bool {
	var header tls.RecordHeaderError
	var alert tls.AlertError
	var verify *tls.CertificateVerificationError
	if errors.As(err, &header) || errors.As(err, &alert) || errors.As(err, &verify) {
		return true
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.HasPrefix(e.Error(), "tls: ") {
			return true
		}
	}
	return false
}

// reason is the 502 fault of a reason of the closed list.
func (c *contactRun) reason(why string) *contactFault {
	return &contactFault{status: http.StatusBadGateway, outcome: outcomeFailed, sentence: fmt.Sprintf(contactFailed, c.host, why)}
}

// fail notes the fault of reason why.
func (c *contactRun) fail(why string) { c.noteFault(c.reason(why)) }

// failed notes the fault of reason why and answers the fault noted.
func (c *contactRun) failed(why string) *contactFault {
	c.fail(why)
	return c.noted()
}

// noteFault keeps f unless a fault was noted first or the tools were read.
func (c *contactRun) noteFault(f *contactFault) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f != nil && c.note == nil && !c.done {
		c.note = f
	}
}

// noted answers the fault noted, nil when none was.
func (c *contactRun) noted() *contactFault {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.note
}

// fault answers why a step of the contact failed: the fault noted first,
// the timeout when the contact ran out of time, and fallback, the step's
// own reason, when the server's answer itself was the fault.
func (c *contactRun) fault(ctx context.Context, fallback string) *contactFault {
	switch f := c.noted(); {
	case f != nil:
		return f
	case ctx.Err() != nil:
		return c.failed(reasonTimeout)
	}
	return c.failed(fallback)
}

// finish marks the tools read, after which nothing the close meets is a
// fault.
func (c *contactRun) finish() {
	c.mu.Lock()
	c.done = true
	c.mu.Unlock()
}

// finished reports whether the tools were read.
func (c *contactRun) finished() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.done
}
