package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// upstreamMarker is in every byte a failing test upstream sends, so a test
// can prove that no answer quotes the server.
const upstreamMarker = "MARKER-7f3a"

// fakeMCP is an upstream that speaks enough MCP over streamable HTTP for a
// contact: initialize, the initialized notification, the pages of
// tools/list and the close. It keeps the headers of every request.
type fakeMCP struct {
	mu             sync.Mutex
	headers        []http.Header
	pages          [][]map[string]any
	listError      bool
	closeAfterInit bool
	onList         func()
}

func (f *fakeMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.headers = append(f.headers, r.Header.Clone())
	f.mu.Unlock()
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusOK)
		return
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
			Cursor          string `json:"cursor"`
		} `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&msg)
	answer := func(result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}
	switch msg.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "s-1")
		if f.closeAfterInit {
			w.Header().Set("Connection", "close")
		}
		answer(map[string]any{"protocolVersion": msg.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "fake", "version": "1.2.3"}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		if f.onList != nil {
			f.onList()
		}
		if f.listError {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID,
				"error": map[string]any{"code": -32601, "message": upstreamMarker}})
			return
		}
		page, _ := strconv.Atoi(msg.Params.Cursor)
		result := map[string]any{"tools": f.pages[page]}
		if page+1 < len(f.pages) {
			result["nextCursor"] = strconv.Itoa(page + 1)
		}
		answer(result)
	default:
		answer(map[string]any{})
	}
}

// tool is a tool of a fakeMCP page.
func tool(name, description string, readOnly bool) map[string]any {
	return map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"type": "object"},
		"annotations": map[string]any{"readOnlyHint": readOnly}}
}

// twoTools is the fakeMCP of two tools on two pages.
func twoTools() *fakeMCP {
	return &fakeMCP{pages: [][]map[string]any{{tool("get_me", "Read the user.", true)}, {tool("create_issue", "Open an issue.", false)}}}
}

// loopback is the address the test upstreams listen on.
var loopback = netip.MustParseAddr("127.0.0.1")

// resolver answers names from answers, one list per lookup of a name, the
// last list for every later lookup, and a DNS miss for a name it lacks.
type resolver struct {
	mu      sync.Mutex
	answers map[string][][]string
	asked   map[string]int
}

func (r *resolver) resolve(_ context.Context, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lists, ok := r.answers[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	n := min(r.asked[host], len(lists)-1)
	r.asked[host]++
	var out []netip.Addr
	for _, a := range lists[n] {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

// testDialer is the contact route's dialer as a test runs it: 127.0.0.1,
// where the test upstreams listen, passes and every other address meets
// the route's classes, names resolve through answers, roots are trusted,
// and a contact ends after timeout.
func testDialer(answers map[string][][]string, roots *x509.CertPool, timeout time.Duration) contactDialer {
	d := newContactDialer()
	d.refuse = func(a netip.Addr) string {
		if a.Unmap() == loopback {
			return ""
		}
		return drafts.ContactClass(a)
	}
	if answers != nil {
		d.resolve = (&resolver{answers: answers, asked: map[string]int{}}).resolve
	}
	d.roots, d.timeout = roots, timeout
	return d
}

// hostPort is the port of the test server srv.
func hostPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

// TestContactAddressClasses pins the address check of Contact: each
// class the route refuses is refused in the dialer's Control hook before a
// packet leaves, an IPv4-mapped IPv6 address reads as its IPv4 address, a
// NAT64 or 6to4 address as the IPv4 address it embeds, and a private or
// public address passes the class check.
func TestContactAddressClasses(t *testing.T) {
	t.Parallel()
	refused := []struct{ addr, class string }{
		{"127.0.0.1", "a loopback"}, {"[::1]", "a loopback"}, {"0.0.0.0", "an unspecified"}, {"[::]", "an unspecified"},
		{"169.254.169.254", "a cloud metadata"}, {"[fe80::1]", "a link-local"}, {"[fd00:ec2::254]", "a cloud metadata"},
		{"100.100.100.200", "a cloud metadata"}, {"[::ffff:169.254.169.254]", "a cloud metadata"}, {"[::ffff:127.0.0.1]", "a loopback"},
		{"[64:ff9b::a9fe:a9fe]", "a cloud metadata"}, {"[64:ff9b::7f00:1]", "a loopback"},
		{"[2002:a9fe:a9fe::1]", "a cloud metadata"}, {"[2002:7f00:1::1]", "a loopback"},
	}
	d := newContactDialer()
	for _, tc := range refused {
		t.Run(tc.addr, func(t *testing.T) {
			_, fault := d.contact(context.Background(), "http://"+tc.addr+":9/mcp")
			host := strings.Trim(tc.addr, "[]")
			// Go dials an IPv4-mapped IPv6 address as its IPv4 address.
			want := "Straza does not contact " + host + ": it resolves to " + netip.MustParseAddr(host).Unmap().String() + ", " + tc.class +
				" address, where a request from strazad reaches strazad's own host or its cloud's metadata. Publish the server if you mean it, or give it an address outside those ranges."
			if fault == nil || fault.status != http.StatusConflict || fault.outcome != outcomeRefused || fault.sentence != want {
				t.Fatalf("fault = %+v\nwant 409 %q", fault, want)
			}
		})
	}
	for _, addr := range []string{"10.0.0.1", "192.168.1.10", "100.64.0.1", "93.184.216.34", "2001:db8::1", "64:ff9b::5db8:d822", "2002:5db8:d822::1"} {
		if class := drafts.ContactClass(netip.MustParseAddr(addr)); class != "" {
			t.Errorf("%s is refused as %s, want it to pass", addr, class)
		}
	}
}

// TestContactRefusesANameResolvingToARefusedAddress pins that a name is
// checked by the addresses it resolves to: localhost through the system's
// resolver, and any name whose answer holds a refused address, even one
// that also answers an address Straza would dial.
func TestContactRefusesANameResolvingToARefusedAddress(t *testing.T) {
	t.Parallel()
	_, fault := newContactDialer().contact(context.Background(), "http://localhost:9/mcp")
	if fault == nil || fault.status != http.StatusConflict || !strings.HasPrefix(fault.sentence, "Straza does not contact localhost: it resolves to ") ||
		!strings.Contains(fault.sentence, ", a loopback address,") {
		t.Errorf("localhost: fault = %+v, want the loopback refusal", fault)
	}
	up := twoTools()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	d := testDialer(map[string][][]string{"meta.internal.test": {{"169.254.169.254"}}, "mixed.test": {{"169.254.169.254", "127.0.0.1"}}}, nil, 3*time.Second)
	for _, host := range []string{"meta.internal.test", "mixed.test"} {
		_, fault = d.contact(context.Background(), "http://"+host+":"+hostPort(t, srv)+"/mcp")
		if fault == nil || fault.status != http.StatusConflict || !strings.HasPrefix(fault.sentence, "Straza does not contact "+host+": it resolves to 169.254.169.254, a cloud metadata address") {
			t.Errorf("%s: fault = %+v, want the metadata address refused", host, fault)
		}
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.headers) != 0 {
		t.Errorf("a name with a refused address reached the upstream %d times", len(up.headers))
	}
}

// TestContactStripsCredentialHeaders pins the transport's own guard: a
// credential header on a request, whoever set it, never leaves strazad.
func TestContactStripsCredentialHeaders(t *testing.T) {
	t.Parallel()
	var seen http.Header
	run := &contactRun{d: newContactDialer(), base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.Header.Clone()
		return &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})}
	req := httptest.NewRequest(http.MethodPost, "http://mcp.example/mcp", nil)
	for _, h := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
		req.Header.Set(h, "secret")
	}
	if _, err := run.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
		if seen.Get(h) != "" {
			t.Errorf("%s left strazad", h)
		}
	}
}

// roundTripFunc is a function as an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestContactRefusesARebindingName pins the check of every dial: a name
// that answers the upstream's address first and the metadata address when
// the client dials again is refused on the second dial, although the first
// passed.
func TestContactRefusesARebindingName(t *testing.T) {
	t.Parallel()
	up := &fakeMCP{closeAfterInit: true, pages: [][]map[string]any{{tool("get_me", "", true)}}}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	d := testDialer(map[string][][]string{"rebind.test": {{"127.0.0.1"}, {"169.254.169.254"}}}, nil, 3*time.Second)
	_, fault := d.contact(context.Background(), "http://rebind.test:"+hostPort(t, srv)+"/mcp")
	if fault == nil || fault.status != http.StatusConflict || !strings.Contains(fault.sentence, "it resolves to 169.254.169.254, a cloud metadata address") {
		t.Fatalf("fault = %+v, want the second dial refused", fault)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.headers) != 1 {
		t.Errorf("the upstream saw %d requests, want the initialize alone", len(up.headers))
	}
}

// expiredCert is a self-signed certificate for 127.0.0.1 that expired a
// day ago, with its pool.
func expiredCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "expired"},
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// failing answers every request with status and content type ct, the
// marker in its body and its headers.
func failing(status int, ct string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Upstream", upstreamMarker)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("<p>" + upstreamMarker + "</p>"))
	}
}

// TestContactReasons pins the closed list of reasons: each upstream
// fails one way, the answer is 502 with the reason's words, and no answer
// holds a byte the upstream sent.
func TestContactReasons(t *testing.T) {
	t.Parallel()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := strconv.Itoa(closed.Addr().(*net.TCPAddr).Port)
	_ = closed.Close()
	expired, expiredPool := expiredCert(t)
	cases := []struct {
		name     string
		serve    func(t *testing.T) (address string, d contactDialer)
		host     string
		sentence string
	}{
		{name: "a status", serve: plainUpstream(failing(http.StatusInternalServerError, "text/plain")),
			sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: it answered with HTTP 500. Check the address in the draft."},
		{name: "a status that asks for a credential", serve: plainUpstream(failing(http.StatusUnauthorized, "application/json")),
			sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: it answered with HTTP 401, which asks for a credential, " +
				"and Straza contacts a proposed server without one. Publish the server and store its credential to read its tools."},
		{name: "a content type", serve: plainUpstream(failing(http.StatusOK, "text/html; charset=utf-8")),
			sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: it answered with the content type text/html, which is neither JSON nor an event stream. Check the address in the draft."},
		{name: "a content type Straza does not quote", serve: plainUpstream(failing(http.StatusOK, "x-"+upstreamMarker+"/<b>")),
			sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: it answered with an unreadable content type, which is neither JSON nor an event stream. Check the address in the draft."},
		{name: "no answer in time", serve: plainUpstream(func(w http.ResponseWriter, r *http.Request) {
			// The server notices a closed connection only once the body is read.
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}),
			sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: it did not answer within 10 seconds. Check the address in the draft."},
		{name: "an untrusted certificate", serve: tlsUpstream(nil, x509.NewCertPool(), ""),
			sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: its TLS certificate is not trusted. Check the address in the draft."},
		{name: "a certificate for another host", serve: tlsUpstream(nil, nil, "mcp.test"),
			sentence: "Straza contacted mcp.test and it did not answer as an MCP server: its TLS certificate names another host. Check the address in the draft."},
		{name: "an expired certificate", serve: tlsUpstream(&expired, expiredPool, ""),
			sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: its TLS certificate has expired. Check the address in the draft."},
		{name: "no TLS on a TLS address", serve: func(t *testing.T) (string, contactDialer) {
			srv := httptest.NewServer(twoTools())
			t.Cleanup(srv.Close)
			return "https://127.0.0.1:" + hostPort(t, srv) + "/mcp", testDialer(nil, nil, 3*time.Second)
		}, sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: the TLS handshake failed. Check the address in the draft."},
		{name: "a refused connection", serve: func(*testing.T) (string, contactDialer) {
			return "http://127.0.0.1:" + closedPort + "/mcp", testDialer(nil, nil, 3*time.Second)
		}, sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: the connection was refused. Check the address in the draft."},
		{name: "a name that does not resolve", serve: func(*testing.T) (string, contactDialer) {
			return "http://nowhere.test/mcp", testDialer(map[string][][]string{}, nil, 3*time.Second)
		}, sentence: "Straza contacted nowhere.test and it did not answer as an MCP server: the name does not resolve. Check the address in the draft."},
		{name: "an answer that is no initialize result", serve: plainUpstream(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"` + upstreamMarker + `": 1}`))
		}), sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: its answer is not an MCP initialize result. Check the address in the draft."},
		{name: "a tools/list that fails", serve: plainUpstream((&fakeMCP{listError: true}).ServeHTTP),
			sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: its answer to tools/list is not a list of tools. Check the address in the draft."},
		{name: "an answer over 1 MiB", serve: plainUpstream(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"x":"` + strings.Repeat(upstreamMarker, 200_000) + `"}}`))
		}), sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: its answer is larger than 1 MiB. Check the address in the draft."},
		{name: "a connection closed without an answer", serve: plainUpstream(func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}), sentence: "Straza contacted 127.0.0.1 and it did not answer as an MCP server: the connection failed before it answered. Check the address in the draft."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			address, d := tc.serve(t)
			_, fault := d.contact(context.Background(), address)
			if fault == nil || fault.status != http.StatusBadGateway || fault.outcome != outcomeFailed || fault.sentence != tc.sentence {
				t.Fatalf("fault = %+v\nwant 502 %q", fault, tc.sentence)
			}
			if strings.Contains(fault.sentence, upstreamMarker) || strings.Contains(strings.ToLower(fault.sentence), strings.ToLower(upstreamMarker)) {
				t.Errorf("the answer quotes the upstream: %q", fault.sentence)
			}
		})
	}
}

// plainUpstream serves h over HTTP on 127.0.0.1 and contacts it with a
// dialer that gives up after half a second.
func plainUpstream(h http.HandlerFunc) func(t *testing.T) (string, contactDialer) {
	return func(t *testing.T) (string, contactDialer) {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return "http://127.0.0.1:" + hostPort(t, srv) + "/mcp", testDialer(nil, nil, 500*time.Millisecond)
	}
}

// tlsUpstream serves twoTools over TLS, with cert when given and the test
// server's own otherwise, and contacts it trusting roots, or the test
// server's certificate when roots is nil, at host, which resolves to
// 127.0.0.1, or at 127.0.0.1 itself.
func tlsUpstream(cert *tls.Certificate, roots *x509.CertPool, host string) func(t *testing.T) (string, contactDialer) {
	return func(t *testing.T) (string, contactDialer) {
		srv := httptest.NewUnstartedServer(twoTools())
		if cert != nil {
			srv.TLS = &tls.Config{Certificates: []tls.Certificate{*cert}}
		}
		srv.StartTLS()
		t.Cleanup(srv.Close)
		if roots == nil {
			roots = x509.NewCertPool()
			roots.AddCert(srv.Certificate())
		}
		answers := map[string][][]string{}
		name := "127.0.0.1"
		if host != "" {
			answers[host], name = [][]string{{"127.0.0.1"}}, host
		}
		return "https://" + name + ":" + hostPort(t, srv) + "/mcp", testDialer(answers, roots, 3*time.Second)
	}
}

// TestContactEndsWithinItsBound pins the time bound of a contact on every
// request it sends, the close of the MCP session included: an upstream
// that holds the close open, after a slow tools/list, holds strazad no
// longer than the bound.
func TestContactEndsWithinItsBound(t *testing.T) {
	t.Parallel()
	up := twoTools()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
			return
		}
		if r.Method == http.MethodPost {
			var peek bytes.Buffer
			_, _ = peek.ReadFrom(r.Body)
			if strings.Contains(peek.String(), `"tools/list"`) {
				time.Sleep(300 * time.Millisecond)
			}
			r.Body = io.NopCloser(&peek)
		}
		up.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	start := time.Now()
	_, fault := testDialer(nil, nil, time.Second).contact(context.Background(), "http://127.0.0.1:"+hostPort(t, srv)+"/mcp")
	if took := time.Since(start); took > 1300*time.Millisecond {
		t.Errorf("the contact took %v against a bound of 1s (fault %+v)", took, fault)
	}
}

// TestContactRefusesARedirect pins that Contact follows no redirect: the
// answer names the redirect and the target sees no request.
func TestContactRefusesARedirect(t *testing.T) {
	t.Parallel()
	target := twoTools()
	to := httptest.NewServer(target)
	t.Cleanup(to.Close)
	from := httptest.NewServer(http.RedirectHandler(to.URL+"/mcp", http.StatusTemporaryRedirect))
	t.Cleanup(from.Close)
	_, fault := testDialer(nil, nil, 3*time.Second).contact(context.Background(), "http://127.0.0.1:"+hostPort(t, from)+"/mcp")
	want := "Straza contacted 127.0.0.1 and it answered with a redirect, which Straza does not follow, because a credential must go only where the draft says."
	if fault == nil || fault.status != http.StatusBadGateway || fault.sentence != want {
		t.Fatalf("fault = %+v, want 502 %q", fault, want)
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if len(target.headers) != 0 {
		t.Errorf("the redirect's target saw %d requests", len(target.headers))
	}
}

// TestContactTransportUsesNoProxy pins that the transport dials the
// address itself, never through a proxy the environment names, since the
// address check must see the address it dials.
func TestContactTransportUsesNoProxy(t *testing.T) {
	t.Parallel()
	run := &contactRun{d: newContactDialer()}
	if tr := run.transport(); tr.Proxy != nil || tr.TLSClientConfig.InsecureSkipVerify {
		t.Errorf("transport proxy set %v, TLS verification skipped %v, want neither", tr.Proxy != nil, tr.TLSClientConfig.InsecureSkipVerify)
	}
}

// TestContactReadsToolsWithNoCredential pins a contact that answers: the
// server's name and version, every page of tools with their read-only
// hint, and no credential on any request, the user information of the
// address included.
func TestContactReadsToolsWithNoCredential(t *testing.T) {
	t.Parallel()
	up := twoTools()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	got, fault := testDialer(nil, nil, 3*time.Second).contact(context.Background(), "http://alice:s3cret@127.0.0.1:"+hostPort(t, srv)+"/mcp")
	if fault != nil {
		t.Fatalf("fault = %+v", fault)
	}
	want := []contactedTool{{Name: "get_me", Description: "Read the user.", ReadOnly: true}, {Name: "create_issue", Description: "Open an issue."}}
	if got.host != "127.0.0.1" || got.server != (contactedServer{Name: "fake", Version: "1.2.3"}) || fmt.Sprint(got.tools) != fmt.Sprint(want) {
		t.Errorf("answer = %+v, want fake 1.2.3 with %+v", got, want)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	for _, h := range up.headers {
		for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
			if h.Get(name) != "" {
				t.Errorf("a request carried %s", name)
			}
		}
	}
}

// contactFixture is the drafts fixture with an upstream the contact route
// reaches through the test dialer, and the person mia, who holds
// straza-global-mcp-admin.
type contactFixture struct {
	*draftsFixture
	up    *fakeMCP
	srv   *httptest.Server
	mia   string
	route http.HandlerFunc
}

func newContactFixture(t *testing.T) *contactFixture {
	t.Helper()
	f := &contactFixture{draftsFixture: newDraftsFixture(t, nil), up: twoTools()}
	f.srv = httptest.NewServer(f.up)
	t.Cleanup(f.srv.Close)
	mkHuman(t, f.app, "mia", MCPAdminRole)
	f.mia = loginDeviceFlow(t, f.base, "mia", "hunter2!")
	f.route = f.app.requireContact(f.app.contactHandler(testDialer(nil, nil, 3*time.Second)))
	return f
}

// contact sends body to the contact route of draft id as bearer.
func (f *contactFixture) contact(t *testing.T, id, bearer string, body any) (int, []byte) {
	t.Helper()
	return serveRoute(t, "POST /v1/admin/drafts/{id}/contact", f.route, "/v1/admin/drafts/"+id+"/contact", bearer, body)
}

// serveRoute sends body as JSON to path, served by h under pattern, as
// bearer, and answers the status and the raw answer.
func serveRoute(t *testing.T, pattern string, h http.HandlerFunc, path, bearer string, body any) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	if s, ok := body.(string); ok {
		raw = []byte(s)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, h)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// errorOf is the sentence of an error answer.
func errorOf(t *testing.T, raw []byte) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("%v in %s", err, raw)
	}
	return e.Error
}

// probeDraft creates, as kim, a draft that adds the remote server probe
// at the fixture's upstream, and answers its id.
func (f *contactFixture) probeDraft(t *testing.T) string {
	t.Helper()
	return f.create(t, f.root, draftApp("probe", "http://127.0.0.1:"+hostPort(t, f.srv)+"/mcp", "The probe server.")).Draft.ID
}

// TestContactStanding pins who may contact: step 1 of a publish in
// a contact's words, then the scope apps:write or straza-global-mcp-admin,
// whatever drafts grant the caller holds, and the coding harness refusal
// of every drafts route.
func TestContactStanding(t *testing.T) {
	t.Parallel()
	f := newContactFixture(t)
	id := f.probeDraft(t)
	harness, _ := checkinTokenAs(t, f.base, "claude-code")
	cases := []struct {
		name, bearer string
		code         int
		sentence     string
	}{
		{"root", f.root, http.StatusOK, ""},
		{"the MCP admin", f.mia, http.StatusOK, ""},
		{"an admin API token", f.token, http.StatusForbidden, contactAdminAPIRefusal},
		{"an agent with the drafts grants", f.bot, http.StatusForbidden, fmt.Sprintf(nonPersonAdminRefusal, "bot", "an agent")},
		{"a person with the drafts grants alone", f.ada, http.StatusForbidden, contactStandingRefusal},
		{"a person with identity:write and apps:read", f.nell, http.StatusForbidden, contactStandingRefusal},
		{"a server's admin", f.erin, http.StatusForbidden, contactStandingRefusal},
		{"a coding harness's session", harness, http.StatusForbidden, fmt.Sprintf(codingHarnessRefusal, "claude-code")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := f.contact(t, id, tc.bearer, map[string]string{"object": "App/probe"})
			if code != tc.code || (tc.sentence != "" && errorOf(t, raw) != tc.sentence) {
				t.Errorf("%d %s, want %d %q", code, raw, tc.code, tc.sentence)
			}
		})
	}
	for _, c := range []struct {
		caller draftCaller
		want   string
	}{
		{draftCaller{author: drafts.Principal{Username: "bot", Agent: true}, p: adminPrincipal{roles: []store.Role{}}}, contactAgentRefusal},
		{draftCaller{person: true, disabled: true, author: drafts.Principal{Username: "dora"}, p: adminPrincipal{roles: []store.Role{}}},
			"The user dora is disabled, so it cannot contact a proposed server. Ask an administrator to enable it again."},
		{draftCaller{person: true, locked: true, author: drafts.Principal{Username: "lou"}, p: adminPrincipal{roles: []store.Role{}}},
			"The user lou is locked, so it cannot contact a proposed server. An administrator lifts the lock with strazactl users unlock lou."},
	} {
		if got := c.caller.contactRefusal(); got != c.want {
			t.Errorf("contactRefusal = %q, want %q", got, c.want)
		}
	}
}

// TestContactItemRefusals pins what a contact refuses before it dials: a
// body that is not a contact, an object that is not a server, a server
// the draft does not put, a server that runs on this host, and a draft
// that is not open.
func TestContactItemRefusals(t *testing.T) {
	t.Parallel()
	f := newContactFixture(t)
	id := f.probeDraft(t)
	mixed := f.create(t, f.root, runnerApp(""), "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: boxed\nserver:\n  name: io.x/boxed\n  version: 1.0.0\n"+
		"straza:\n  runtime:\n    kind: oci\n    oci:\n      image: ghcr.io/example/boxed:1\n",
		"apiVersion: straza.dev/v1beta1\nkind: Removal\nmetadata:\n  name: jira\nspec:\n  kind: App\n", draftRole("dev-helpers", "Helps.")).Draft.ID
	closed := f.probeDraft(t)
	if code, a := f.call(t, http.MethodPost, "/v1/admin/drafts/"+closed+"/discard", f.root, map[string]any{}); code != http.StatusOK {
		t.Fatalf("discard = %d %q", code, a.Error)
	}
	cases := []struct {
		name, id string
		body     any
		code     int
		sentence string
	}{
		{"a body that is not JSON", id, "{", http.StatusBadRequest, "The request body is not a contact: unexpected EOF. Send object as JSON, such as App/github."},
		{"an object that is not a server", id, map[string]string{"object": "Role/dev"}, http.StatusBadRequest, contactObjectRefusal},
		{"an object with no name", id, map[string]string{"object": "App/"}, http.StatusBadRequest, contactObjectRefusal},
		{"a server the draft does not hold", id, map[string]string{"object": "App/github"}, http.StatusNotFound, "Draft " + id + " holds no server github."},
		{"a removal", mixed, map[string]string{"object": "App/jira"}, http.StatusNotFound, "Draft " + mixed + " holds no server jira."},
		{"a command", mixed, map[string]string{"object": "App/runner"}, http.StatusConflict,
			"runner runs as a command, and Straza starts such a server only when a person publishes it."},
		{"a container", mixed, map[string]string{"object": "App/boxed"}, http.StatusConflict,
			"boxed runs as a container, and Straza starts such a server only when a person publishes it."},
		{"a discarded draft", closed, map[string]string{"object": "App/probe"}, http.StatusConflict,
			"Draft " + closed + " is discarded, so it cannot change. Create a new draft from its documents."},
		{"no such draft", "999", map[string]string{"object": "App/probe"}, http.StatusNotFound,
			"There is no draft 999. List the drafts with strazactl drafts list, or open Drafts on the console."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := f.contact(t, tc.id, f.root, tc.body)
			if code != tc.code || errorOf(t, raw) != tc.sentence {
				t.Errorf("%d %s\nwant %d %q", code, raw, tc.code, tc.sentence)
			}
		})
	}
	f.up.mu.Lock()
	defer f.up.mu.Unlock()
	if len(f.up.headers) != 0 {
		t.Errorf("a refused contact dialed: the upstream saw %d requests", len(f.up.headers))
	}
}

// TestContactStoresOfferedAndRecords pins a contact that answers: its 200,
// the tool names kept on the item with the digest of its document
// and nothing else written, GET's contacted and a verdict that no longer
// calls the tools unknown, and one draft.contact record per contact, for
// an answer, a refused address and a failure alike.
func TestContactStoresOfferedAndRecords(t *testing.T) {
	t.Parallel()
	f := newContactFixture(t)
	id := f.probeDraft(t)
	before, _ := f.stored(t, id)
	code, raw := f.contact(t, id, f.mia, map[string]string{"object": "App/probe"})
	if code != http.StatusOK {
		t.Fatalf("contact = %d %s", code, raw)
	}
	var got contactedPayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Object != "App/probe" || got.Host != "127.0.0.1" || got.Server.Name != "fake" || len(got.Tools) != 2 || got.ContactedAt == "" {
		t.Errorf("answer = %s", raw)
	}
	row, items := f.stored(t, id)
	var o offered
	if err := json.Unmarshal([]byte(items[0].Offered), &o); err != nil {
		t.Fatalf("offered %q: %v", items[0].Offered, err)
	}
	sum := sha256.Sum256([]byte(items[0].Doc))
	if o.Digest != hex.EncodeToString(sum[:]) || strings.Join(o.Tools, ",") != "get_me,create_issue" || o.At != got.ContactedAt {
		t.Errorf("offered = %+v, want the digest of the item's document and both names", o)
	}
	if row.Revision != before.Revision || !row.UpdatedAt.Equal(before.UpdatedAt) || row.CheckedRevision != before.CheckedRevision {
		t.Errorf("the draft row moved: %+v, was %+v", row, before)
	}
	var detail struct {
		Contacted map[string]struct {
			Tools []string `json:"tools"`
		} `json:"contacted"`
		Verdict wireVerdict `json:"verdict"`
	}
	if code := adminReq(t, http.MethodGet, f.base+"/v1/admin/drafts/"+id, f.root, nil, &detail); code != http.StatusOK {
		t.Fatalf("GET = %d", code)
	}
	if strings.Join(detail.Contacted["App/probe"].Tools, ",") != "get_me,create_issue" {
		t.Errorf("contacted = %+v", detail.Contacted)
	}
	for _, u := range detail.Verdict.Unchecked {
		if u.Code == "unchecked.tools" {
			t.Errorf("the verdict still calls the tools unknown: %+v", u)
		}
	}
	answered := draftRecords(t, f.app, "draft.contact", id)
	if len(answered) != 1 {
		t.Fatalf("%d draft.contact records, want 1: %+v", len(answered), answered)
	}
	rec := answered[0]
	if rec["app"] != "probe" || rec["host"] != "127.0.0.1" || rec["outcome"] != "answered" || rec["tools"] != float64(2) {
		t.Errorf("draft.contact = %+v", rec)
	}
	if actor, _ := rec["actor"].(string); actor != "mia" {
		t.Errorf("draft.contact actor = %v, want mia", rec["actor"])
	}
	refusedID := f.create(t, f.root, draftApp("meta", "http://169.254.169.254/mcp", "The metadata service.")).Draft.ID
	if code, raw := f.contact(t, refusedID, f.root, map[string]string{"object": "App/meta"}); code != http.StatusConflict {
		t.Errorf("a metadata address = %d %s, want 409", code, raw)
	}
	f.srv.Close()
	if code, raw := f.contact(t, id, f.root, map[string]string{"object": "App/probe"}); code != http.StatusBadGateway {
		t.Errorf("a closed upstream = %d %s, want 502", code, raw)
	}
	for _, want := range []struct{ id, outcome string }{{refusedID, "refused"}, {id, "failed"}} {
		recs := draftRecords(t, f.app, "draft.contact", want.id)
		if last := recs[len(recs)-1]; last["outcome"] != want.outcome || last["tools"] != float64(0) {
			t.Errorf("draft.contact of draft %s = %+v, want %s with no tools", want.id, last, want.outcome)
		}
	}
}

// TestContactOfferedCountsForItsDocumentOnly pins the digest rule of every
// reader of Offered: once the draft's document of the server changes, the
// names a contact read for the old one count for nothing, on GET and in
// the verdict.
func TestContactOfferedCountsForItsDocumentOnly(t *testing.T) {
	t.Parallel()
	f := newContactFixture(t)
	id := f.probeDraft(t)
	if code, raw := f.contact(t, id, f.root, map[string]string{"object": "App/probe"}); code != http.StatusOK {
		t.Fatalf("contact = %d %s", code, raw)
	}
	changed := draftApp("probe", "http://127.0.0.1:"+hostPort(t, f.srv)+"/mcp", "Another description.")
	if code, a := f.call(t, http.MethodPut, "/v1/admin/drafts/"+id, f.root, map[string]any{"revision": 1, "documents": []string{changed}}); code != http.StatusOK {
		t.Fatalf("update = %d %q", code, a.Error)
	}
	var detail struct {
		Contacted map[string]any `json:"contacted"`
		Verdict   wireVerdict    `json:"verdict"`
	}
	if code := adminReq(t, http.MethodGet, f.base+"/v1/admin/drafts/"+id, f.root, nil, &detail); code != http.StatusOK {
		t.Fatalf("GET = %d", code)
	}
	if len(detail.Contacted) != 0 {
		t.Errorf("contacted = %+v, want nothing for a changed document", detail.Contacted)
	}
	unknown := false
	for _, u := range detail.Verdict.Unchecked {
		unknown = unknown || (u.Code == "unchecked.tools" && u.Object == "App/probe")
	}
	if !unknown {
		t.Errorf("unchecked = %+v, want the tools of probe unknown again", detail.Verdict.Unchecked)
	}
}

// TestContactKeepsNothingWhenTheDraftMoves pins the 409 of a draft that a
// revision moved while the contact ran: the names are not kept.
func TestContactKeepsNothingWhenTheDraftMoves(t *testing.T) {
	t.Parallel()
	f := newContactFixture(t)
	id := f.probeDraft(t)
	n, _ := strconv.ParseInt(id, 10, 64)
	var once sync.Once
	f.up.onList = func() { once.Do(func() { revise(t, f, n) }) }
	code, raw := f.contact(t, id, f.root, map[string]string{"object": "App/probe"})
	if want := "Draft " + id + " changed while Straza contacted probe, so the tool names were not kept. Contact it again."; code != http.StatusConflict || errorOf(t, raw) != want {
		t.Errorf("%d %s, want 409 %q", code, raw, want)
	}
	if _, items := f.stored(t, id); items[0].Offered != "" {
		t.Errorf("offered = %q, want nothing kept", items[0].Offered)
	}
}

// revise writes revision 2 of draft n as kim, with the items it holds.
func revise(t *testing.T, f *contactFixture, n int64) {
	_, items, err := f.app.store.Drafts().Get(context.Background(), n)
	if err == nil {
		_, err = f.app.store.Drafts().Revise(context.Background(), n, store.DraftRevise{From: 1, Items: items,
			Rev: store.DraftRevisionRow{Author: store.DraftActor{ID: f.kim.ID, Name: "kim"}, Door: "console", Digest: "d2"}})
	}
	if err != nil {
		t.Error(err)
	}
}
