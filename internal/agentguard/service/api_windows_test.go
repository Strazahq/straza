//go:build windows

package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// serveOn serves a over ln until the test ends.
func serveOn(t *testing.T, ln net.Listener, a *api) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, a) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
}

// testPipe returns a pipe name no other test or service uses.
func testPipe(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return `\\.\pipe\straza-test-` + hex.EncodeToString(b[:])
}

// dialPipe opens the pipe at path at level with the thin client's access,
// without its owner check.
func dialPipe(ctx context.Context, path string, level winio.PipeImpLevel) (net.Conn, error) {
	return winio.DialPipeAccessImpLevel(ctx, path, localapi.PipeAccess, level)
}

// pipeHTTP returns an HTTP client that dials the pipe at path at level,
// one connection per request.
func pipeHTTP(path string, level winio.PipeImpLevel) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialPipe(ctx, path, level)
		},
	}}
}

// servePipe serves the API half over an apiDouble on a new pipe made by
// listen, as the test's own account, and returns the pipe, the double and
// the refusal log.
func servePipe(t *testing.T) (string, *apiDouble, *bytes.Buffer) {
	t.Helper()
	f := &apiDouble{}
	path, logs := serveDoubleOnPipe(t, f)
	return path, f, logs
}

// serveDoubleOnPipe serves the API half over f, scripted before the first
// request, on a new pipe made by listen, as the test's own account, and
// returns the pipe and the refusal log.
func serveDoubleOnPipe(t *testing.T, f *apiDouble) (string, *bytes.Buffer) {
	t.Helper()
	path := testPipe(t)
	ln, err := listen(Options{Socket: path})
	if err != nil {
		t.Fatal(err)
	}
	a, logs := testAPI(f, false)
	a.self = ownUID()
	serveOn(t, ln, a)
	return path, logs
}

// getStatus sends GET status through hc and returns the answer's status
// and body.
func getStatus(t *testing.T, hc *http.Client) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, localapi.BaseURL+localapi.RouteStatus, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

// TestPipeNamesTheCaller pins that a caller on the pipe is the account the
// pipe's client runs as, read by impersonation at identification level,
// with its process id, and that custody is user because the test serves
// as the same account.
func TestPipeNamesTheCaller(t *testing.T) {
	own := ownUID()
	if own == "" || !isUID(own) {
		t.Fatalf("ownUID() = %q, want this account's SID", own)
	}
	path, f, _ := servePipe(t)
	code, body := getStatus(t, pipeHTTP(path, winio.PipeImpLevelIdentification))
	var st localapi.Status
	if code != http.StatusOK || json.Unmarshal(body, &st) != nil || st.Custody != localapi.CustodyUser {
		t.Fatalf("status = %d %s, want 200 with custody user", code, body)
	}
	want := Caller{UID: own, PID: int32(os.Getpid())}
	if len(f.callers) != 1 || f.callers[0] != want {
		t.Errorf("callers = %+v, want [%+v]", f.callers, want)
	}
}

// TestPipeRefusesAnAnonymousCaller pins that a client that dialed the pipe
// anonymously, so the service cannot read its account, is refused with
// the Windows sentence and one log line, and reaches no handler.
func TestPipeRefusesAnAnonymousCaller(t *testing.T) {
	path, f, logs := servePipe(t)
	code, body := getStatus(t, pipeHTTP(path, winio.PipeImpLevelAnonymous))
	var r localapi.Refusal
	if code != http.StatusForbidden || json.Unmarshal(body, &r) != nil || r.Error != callerUnknownWindows {
		t.Fatalf("answer = %d %s, want 403 with %q", code, body, callerUnknownWindows)
	}
	lines := logLines(t, logs)
	if len(lines) != 1 || lines[0]["uid"] != "unknown" ||
		!strings.HasPrefix(lines[0]["reason"].(string), "the kernel did not name the caller: ") {
		t.Errorf("log = %v, want one line with an unknown uid and the reason", lines)
	}
	if len(f.callers) != 0 {
		t.Errorf("callers = %+v, want none", f.callers)
	}
}

// TestPipeNameTaken pins that the service never joins a pipe name that
// exists, whether a running service or another program holds it, and
// answers the taken sentence.
func TestPipeNameTaken(t *testing.T) {
	for _, row := range []struct {
		name string
		hold func(path string) (net.Listener, error)
	}{
		{"a service already listening", func(path string) (net.Listener, error) {
			return listen(Options{Socket: path})
		}},
		{"another program's pipe", func(path string) (net.Listener, error) {
			return winio.ListenPipe(path, nil)
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			path := testPipe(t)
			held, err := row.hold(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = held.Close() }()
			ln, err := listen(Options{Socket: path})
			if err == nil {
				_ = ln.Close()
				t.Fatal("listen succeeded on a held name")
			}
			if !strings.Contains(err.Error(), "could not create its pipe "+path+", because another program already holds that name") {
				t.Errorf("listen = %v, want the taken sentence", err)
			}
		})
	}
}

// TestPipeServesFifteenAtOnce pins that 15 clients dialing at once, as 15
// windows starting together do, all get through and are all named.
func TestPipeServesFifteenAtOnce(t *testing.T) {
	const n = 15
	path, f, _ := servePipe(t)
	hc := pipeHTTP(path, winio.PipeImpLevelIdentification)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, localapi.BaseURL+localapi.RouteStatus, nil)
			resp, err := hc.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs[i] = errors.New(resp.Status)
			}
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("client %d: %v, want 200", i, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.callers) != n {
		t.Fatalf("%d callers named, want %d", len(f.callers), n)
	}
	for _, c := range f.callers {
		if c.UID != ownUID() {
			t.Errorf("caller %+v, want %s", c, ownUID())
		}
	}
}

// TestPipeDescriptor pins the pipe's security as a client reads it: the
// service's account owns it, the DACL is protected, SYSTEM and the
// service get full access, and every other account gets PipeAccess,
// which cannot create a pipe instance.
func TestPipeDescriptor(t *testing.T) {
	path, _, _ := servePipe(t)
	conn, err := dialPipe(context.Background(), path, winio.PipeImpLevelIdentification)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	h := windows.Handle(conn.(interface{ Fd() uintptr }).Fd())
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner.String() != ownUID() {
		t.Errorf("owner = %v (%v), want %s", owner, err, ownUID())
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("control = %#x (%v), want a protected DACL", control, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("DACL = %v (%v)", dacl, err)
	}
	const fileAllAccess = 0x1f01ff // FILE_ALL_ACCESS, the SDDL right FA
	want := map[string]windows.ACCESS_MASK{"S-1-5-18": fileAllAccess, ownUID(): fileAllAccess,
		"S-1-5-11": localapi.PipeAccess}
	got := map[string]windows.ACCESS_MASK{}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Errorf("ACE %d has type %d, want only allow entries", i, ace.Header.AceType)
			continue
		}
		got[(*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()] = ace.Mask
	}
	if len(got) != len(want) || int(dacl.AceCount) != len(want) {
		t.Errorf("DACL = %v, want %v", got, want)
	}
	for sid, mask := range want {
		if got[sid] != mask {
			t.Errorf("%s has %#x, want %#x", sid, got[sid], mask)
		}
	}
}

// pipeServerHandle connects a client at level to a new pipe made by
// listen, sends one byte across, because Windows names the client of the
// last message read, and returns the server end's handle.
func pipeServerHandle(t *testing.T, level winio.PipeImpLevel) windows.Handle {
	t.Helper()
	ln, err := listen(Options{Socket: testPipe(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := dialPipe(ctx, ln.Addr().String(), level)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, ok := <-accepted
	if !ok {
		t.Fatal("the listener accepted no connection")
	}
	t.Cleanup(func() { _ = server.Close() })
	// A pipe write may wait for the reader, so the byte goes from its own
	// goroutine while this one reads it.
	wrote := make(chan error, 1)
	go func() { _, err := client.Write([]byte{1}); wrote <- err }()
	if _, err := io.ReadFull(server, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	return windows.Handle(server.(interface{ Fd() uintptr }).Fd())
}

// TestPipeCallerReadBothWays pins both ways the service reads the account
// of the pipe client it acts as: the thread token opened as the service,
// and the effective-token pseudo-handle it falls back to when Windows
// denies that open. With client and service one account, both must name
// ownUID() for an identification client and refuse an anonymous one. The
// control proves the pseudo-handle's type check refuses the service's own
// token, which alone tells it from the client's here.
func TestPipeCallerReadBothWays(t *testing.T) {
	opened := func() (string, error) {
		tok, err := openThreadToken()
		if err != nil {
			return "", err
		}
		defer func() { _ = tok.Close() }()
		return tokenAccount(tok, viaOpened)
	}
	ways := []struct {
		name string
		read func() (string, error)
	}{
		{viaOpened, opened},
		{viaEffective, effectiveAccount},
		{"the way peerOf takes", threadAccount},
	}
	identified := pipeServerHandle(t, winio.PipeImpLevelIdentification)
	anonymous := pipeServerHandle(t, winio.PipeImpLevelAnonymous)
	for _, w := range ways {
		if got, err := asPipeClient(identified, w.read); err != nil || got != ownUID() {
			t.Errorf("%s, identification client: %q, %v; want %s", w.name, got, err, ownUID())
		}
		if got, err := asPipeClient(anonymous, w.read); err == nil {
			t.Errorf("%s, anonymous client: named %q, want a refusal", w.name, got)
		}
	}
	if got, err := effectiveAccount(); err == nil || !strings.Contains(err.Error(), "is the service's own token") {
		t.Errorf("effective token while acting as no one: %q, %v; want the own-token refusal", got, err)
	}
}

// TestIsUIDTakesOnlyCanonicalSIDs pins the folder names the service takes
// for an account's: a SID spelled as Windows spells it, never an SDDL
// alias, a lower-case or padded spelling, or a decimal uid.
func TestIsUIDTakesOnlyCanonicalSIDs(t *testing.T) {
	for name, want := range map[string]bool{
		"S-1-5-21-1004336348-1177238915-682003330-512":         true,
		"S-1-12-1-1234567890-1234567890-1234567890-1234567890": true,
		"S-1-5-18":  true,
		"SY":        false,
		"s-1-5-18":  false,
		"S-1-5-018": false,
		"1000":      false,
		"":          false,
		"..":        false,
	} {
		if got := isUID(name); got != want {
			t.Errorf("isUID(%q) = %v, want %v", name, got, want)
		}
	}
}

// pipeGitReq is a git door request of window w1 as the helper sends it.
func pipeGitReq(ctx context.Context, t *testing.T, method, route string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, localapi.BaseURL+route, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(localapi.HeaderHarness, "claude-code")
	req.Header.Set(localapi.HeaderHarnessSession, "w1")
	req.Header.Set(localapi.HeaderGitURL, gitRepoURL)
	req.Header.Set(localapi.HeaderGitOp, strings.Repeat("ab", 16))
	return req
}

// TestPipeGitGrantStreams pins that the grant route's wait lines cross the
// pipe as the grant sends them: the helper reads each one while the grant
// still waits, before the last line exists.
func TestPipeGitGrantStreams(t *testing.T) {
	waits := []string{"Straza: waiting for approval.", "Straza: still waiting for approval."}
	read := make(chan struct{}, len(waits))
	f := &apiDouble{session: liveSession()}
	f.git.grant = func(ctx context.Context, _ Session, _ string, _ localapi.GitGrantRequest, wait func(string)) error {
		for _, w := range waits {
			wait(w)
			select {
			case <-read:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	path, _ := serveDoubleOnPipe(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := pipeGitReq(ctx, t, http.MethodPost, localapi.RouteGitGrant, strings.NewReader(`{"op":"fetch"}`))
	resp, err := pipeHTTP(path, winio.PipeImpLevelIdentification).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("answer %d %q, want 200 NDJSON", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	br := bufio.NewReader(resp.Body)
	for _, w := range waits {
		raw, err := br.ReadBytes('\n')
		var line localapi.GitGrantLine
		if err != nil || json.Unmarshal(raw, &line) != nil || line.Wait != w {
			t.Fatalf("line %q (%v), want the wait line %q before the grant goes on", raw, err, w)
		}
		read <- struct{}{}
	}
	if rest, err := io.ReadAll(br); err != nil || string(rest) != `{"ok":true}`+"\n" {
		t.Fatalf("last line %q (%v), want the ok line", rest, err)
	}
}

// TestPipeGitRelayFullDuplex pins that a relayed push crosses the pipe
// both ways at once and whole: the service streams the first half of a
// 32 MiB answer while the helper still holds back the second half of its
// 32 MiB body, and the answer's second half follows only once the body has
// arrived.
func TestPipeGitRelayFullDuplex(t *testing.T) {
	const size, half = 32 << 20, 16 << 20
	type received struct {
		sum []byte
		n   int64
		err error
	}
	got := make(chan received, 1)
	f := &apiDouble{session: liveSession()}
	f.git.send = func(_ Session, _ string, _ gitRoute, in *http.Request) (*http.Response, error) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			h := sha256.New()
			n, err := io.Copy(h, in.Body)
			got <- received{h.Sum(nil), n, err}
		}()
		answer := seeded(2, size)
		body := io.MultiReader(io.LimitReader(answer, half), gated{done, answer})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(body)}, nil
	}
	path, _ := serveDoubleOnPipe(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pr, pw := io.Pipe()
	firstHalf := make(chan struct{})
	go func() {
		push := seeded(1, size)
		_, err := io.CopyN(pw, push, half)
		if err == nil {
			select {
			case <-firstHalf:
				_, err = io.Copy(pw, push)
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		_ = pw.CloseWithError(err)
	}()
	resp, err := pipeHTTP(path, winio.PipeImpLevelIdentification).Do(pipeGitReq(ctx, t, http.MethodPost, localapi.RouteGit+"git-receive-pack", pr))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	h := sha256.New()
	if _, err := io.CopyN(h, resp.Body, half); err != nil {
		t.Fatalf("first half of the answer while the body is open: %v", err)
	}
	close(firstHalf)
	n, err := io.Copy(h, resp.Body)
	if err != nil || n != size-half || !bytes.Equal(h.Sum(nil), sumOf(t, seeded(2, size))) {
		t.Errorf("answer: %d bytes after the first half (%v), want %d bytes of the stream the service sent", n, err, size-half)
	}
	select {
	case r := <-got:
		if r.err != nil || r.n != size || !bytes.Equal(r.sum, sumOf(t, seeded(1, size))) {
			t.Errorf("body: %d bytes (%v), want %d bytes of the stream the helper sent", r.n, r.err, size)
		}
	case <-ctx.Done():
		t.Fatal("the service never read the whole body")
	}
}

// seeded returns size bytes of a stream that seed alone fixes.
func seeded(seed byte, size int64) io.Reader {
	return io.LimitReader(mrand.NewChaCha8([32]byte{seed}), size)
}

// sumOf returns the SHA-256 of what r holds.
func sumOf(t *testing.T, r io.Reader) []byte {
	t.Helper()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		t.Fatal(err)
	}
	return h.Sum(nil)
}

// gated reads r only once open is closed.
type gated struct {
	open <-chan struct{}
	r    io.Reader
}

func (g gated) Read(p []byte) (int, error) {
	<-g.open
	return g.r.Read(p)
}
