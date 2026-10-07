package e2ematrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// syncBuf is a goroutine-safe buffer for a child's combined output.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// proc is a long-lived child (strazad, straza daemon, straza enroll) that is
// stopped by process id, never by name.
type proc struct {
	name string
	cmd  *exec.Cmd
	out  *syncBuf
	done chan struct{}
	err  error
}

// startProc spawns bin with args and env. Output goes to out when set and
// to a shared buffer otherwise.
func startProc(name, bin string, args, env []string, out io.Writer) (*proc, error) {
	cmd := exec.Command(bin, args...) // #nosec G204 -- the binaries this run built
	cmd.Env = env
	p := &proc{name: name, cmd: cmd, out: &syncBuf{}, done: make(chan struct{})}
	if out == nil {
		out = p.out
	}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Stdin = nil
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// exited reports whether the child has finished.
func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// wait blocks until the child exits or the timeout passes.
func (p *proc) wait(timeout time.Duration) error {
	select {
	case <-p.done:
		return p.err
	case <-time.After(timeout):
		return fmt.Errorf("%s still running after %s", p.name, timeout)
	}
}

// stop sends SIGTERM, waits, and falls back to SIGKILL.
func (p *proc) stop() {
	if p == nil || p.exited() {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
		return
	case <-time.After(5 * time.Second):
	}
	_ = p.cmd.Process.Kill()
	<-p.done
}

// waitForLine polls the child's output for a line matching re.
func (p *proc) waitForLine(re *regexp.Regexp, timeout time.Duration) ([]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if m := re.FindStringSubmatch(p.out.String()); m != nil {
			return m, nil
		}
		if p.exited() {
			return nil, fmt.Errorf("%s exited before printing %s: %s", p.name, re, trim(redact(p.out.String())))
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s did not print %s within %s: %s", p.name, re, timeout, trim(redact(p.out.String())))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// runOnce runs a short-lived child to completion with stdin, capturing
// stdout and stderr separately and timing spawn to exit.
func runOnce(ctx context.Context, bin string, args, env []string, stdin []byte) (hookOutcome, error) {
	cmd := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- the binaries this run built
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	err := cmd.Run()
	o := hookOutcome{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Elapsed: time.Since(start)}
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			o.Exit = exitErr.ExitCode()
		case ctx.Err() != nil:
			return o, fmt.Errorf("%s timed out", bin)
		default:
			return o, fmt.Errorf("run %s: %w", bin, err)
		}
	}
	return o, nil
}

// childEnv returns the parent environment without any STRAZA_*, CLAUDE* or
// ANTHROPIC_* variable, plus the given assignments.
func childEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if strings.HasPrefix(name, "STRAZA_") || strings.HasPrefix(name, "CLAUDE") || strings.HasPrefix(name, "ANTHROPIC_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// secretRE matches the values this run must never print: bearer headers
// and the token and password fields of the JSON bodies that carry them.
var secretRE = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+|("(?:password|token|id_token|access_token|session_token|sessionToken|deviceToken|device_token|public_key)"\s*:\s*")[^"]*(")`)

// redact masks bearer values and secret-bearing JSON fields in s.
func redact(s string) string {
	return secretRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretRE.FindStringSubmatch(m)
		if sub[1] != "" {
			return sub[1] + "<redacted>"
		}
		return sub[2] + "<redacted>" + sub[3]
	})
}

// httpCall sends one request and returns status and body. bearer is never
// part of any error or log line.
func httpCall(ctx context.Context, method, url, bearer, contentType string, body []byte) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return 0, nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, raw, err
}

var httpClient = &http.Client{Timeout: 60 * time.Second}

// postForm posts an x-www-form-urlencoded body and decodes the JSON answer.
// approveDevice submits a person's password for a device-flow user code.
// The issuer throttles that submit per client IP and answers 429 with
// Retry-After 1, which a scenario that signs people in step after step
// meets, so the runner waits it out a few times like a person would. Any
// other refusal comes back as it is, with the server's sentence.
func approveDevice(ctx context.Context, base, userCode, username, password string) error {
	var code int
	var body map[string]any
	var err error
	for attempt := 0; ; attempt++ {
		code, body, err = postForm(ctx, base+"/oidc/device", url.Values{
			"user_code": {userCode}, "username": {username}, "password": {password}})
		if err == nil && code == http.StatusTooManyRequests && attempt < 6 {
			time.Sleep(2 * time.Second)
			continue
		}
		break
	}
	if err != nil || code >= 400 {
		return fmt.Errorf("device approval as %s rejected: status %d, %v, %v (does a step create the user with this password first?)", username, code, err, body["error"])
	}
	return nil
}

func postForm(ctx context.Context, endpoint string, form url.Values) (int, map[string]any, error) {
	code, raw, err := httpCall(ctx, http.MethodPost, endpoint, "", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return code, nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return code, m, nil
}

// lookup resolves a dotted json path, with numeric segments indexing lists.
func lookup(doc any, path string) (any, bool) {
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		switch t := cur.(type) {
		case map[string]any:
			v, ok := t[seg]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(t) {
				return nil, false
			}
			cur = t[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// sameValue compares a decoded JSON value with a YAML-authored expectation
// through their canonical JSON encodings.
func sameValue(got, want any) bool {
	g, err1 := json.Marshal(got)
	w, err2 := json.Marshal(want)
	if err1 != nil || err2 != nil {
		return false
	}
	return bytes.Equal(g, w)
}

// asString renders a saved value the way a placeholder needs it.
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		raw, _ := json.Marshal(t)
		return string(raw)
	}
}
