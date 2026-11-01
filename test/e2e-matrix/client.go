package e2ematrix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// client is one identity's straza installation: its own STRAZA_HOME and
// HOME under the scenario's temp dir, its own straza service, which holds
// its key, enrolment and sessions, and its hooks. Only straza service,
// hook, enroll and keygen are ever spawned.
type client struct {
	name     string
	kind     string
	password string
	home     string
	fake     string
	bin      string
	server   *strazad
	service  *proc
	local    *localapi.Client
}

// newClient lays out the identity's directories under dir and starts its
// service.
func newClient(ctx context.Context, name string, id identity, bin, dir string, server *strazad) (*client, error) {
	c := &client{name: name, kind: id.Kind, password: id.Password, bin: bin, server: server,
		home: filepath.Join(dir, name, "straza"), fake: filepath.Join(dir, name, "home")}
	for _, d := range []string{c.home, c.fake} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	c.local = localapi.NewClient(c.home)
	if err := c.startService(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// startService runs straza service for the identity in user mode: the
// identity's STRAZA_HOME puts its socket at $STRAZA_HOME/service.sock and its
// state under $STRAZA_HOME/service, so every identity has a service of its
// own. It waits until the service answers, a refusal included.
func (c *client) startService(ctx context.Context) error {
	p, err := startProc("straza service", c.bin, []string{"service"}, c.env(), nil)
	if err != nil {
		return err
	}
	c.service = p
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, err := c.local.Status(ctx)
		var refused *localapi.RefusalError
		if err == nil || errors.As(err, &refused) {
			return nil
		}
		if p.exited() || time.Now().After(deadline) {
			p.stop()
			return fmt.Errorf("straza service for %s did not answer on %s: %v: %s", c.name, c.local.Socket(), err, trim(redact(p.out.String())))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// env is the child environment: the parent's without STRAZA_*, CLAUDE* and
// ANTHROPIC_* variables, HOME redirected, STRAZA_HOME set, plus extras.
// STRAZA_SYSTEM names a directory that is never created, because a managed
// config on the host wins over the identity's own and would point the
// client at the host's server.
func (c *client) env(extra ...string) []string {
	return childEnv(append([]string{"HOME=" + c.fake, "STRAZA_HOME=" + c.home,
		"STRAZA_SYSTEM=" + filepath.Join(filepath.Dir(c.home), "system")}, extra...)...)
}

var (
	userCodeRE  = regexp.MustCompile(`confirm code ([A-Z2-9]{4}-[A-Z2-9]{4})`)
	publicKeyRE = regexp.MustCompile(`Public key: (\S+)`)
)

// enroll runs the identity's enrolment. A human signs in by the code lane,
// which --use-device-code picks and the runner approves with the declared
// password, or by the browser lane when signIn is browser. An NHI runs
// keygen and the headless enrolment.
func (c *client) enroll(ctx context.Context, signIn string) error {
	if c.kind == "nhi" {
		return c.enrollHeadless(ctx)
	}
	if signIn == "browser" {
		return c.enrollBrowser(ctx)
	}
	p, err := startProc("straza enroll", c.bin, []string{"enroll", "--use-device-code", "--server", c.server.URL}, c.env(), nil)
	if err != nil {
		return err
	}
	defer p.stop()
	m, err := p.waitForLine(userCodeRE, 20*time.Second)
	if err != nil {
		return err
	}
	if err := approveDevice(ctx, c.server.URL, m[1], c.name, c.password); err != nil {
		return err
	}
	if err := p.wait(60 * time.Second); err != nil {
		return fmt.Errorf("straza enroll as %s: %v: %s", c.name, err, trim(redact(p.out.String())))
	}
	return nil
}

// enrollHeadless generates the local key, registers its public half on the
// NHI user a step provisioned, looked up by username, and runs the headless
// enrolment. The runner never creates the user itself.
func (c *client) enrollHeadless(ctx context.Context) error {
	o, err := runOnce(ctx, c.bin, []string{"keygen", "--user", c.name, "--force"}, c.env(), nil)
	if err != nil || o.Exit != 0 {
		return fmt.Errorf("straza keygen for %s: exit %d, %v: %s", c.name, o.Exit, err, trim(string(o.Stderr)))
	}
	m := publicKeyRE.FindSubmatch(o.Stdout)
	if m == nil {
		return fmt.Errorf("straza keygen for %s printed no public key: %s", c.name, trim(string(o.Stdout)))
	}
	user, err := c.server.userByName(ctx, c.name)
	if err != nil {
		return err
	}
	if user == nil {
		return fmt.Errorf("no user named %s exists: a step provisions the NHI before its enroll step", c.name)
	}
	id, _ := user["id"].(string)
	code, body, err := c.server.adminJSON(ctx, http.MethodPut, "/v1/admin/users/"+id+"/nhi-key",
		map[string]any{"public_key": string(m[1])})
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("register NHI key for %s: status %d: %s", c.name, code, trim(redact(string(body))))
	}
	o, err = runOnce(ctx, c.bin, []string{"enroll", "--headless", "--user", c.name, "--server", c.server.URL}, c.env(), nil)
	if err != nil || o.Exit != 0 {
		return fmt.Errorf("straza enroll --headless as %s: exit %d, %v: %s", c.name, o.Exit, err, trim(redact(string(o.Stderr)+string(o.Stdout))))
	}
	return nil
}

// hook spawns straza hook with the payload on stdin and STRAZA_HARNESS set
// to the dialect, the protocol the conformance suite states.
func (c *client) hook(ctx context.Context, dialect string, payload []byte) (hookOutcome, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return runOnce(ctx, c.bin, []string{"hook"}, c.env("STRAZA_HARNESS="+dialect), payload)
}

// mcp sends one JSON-RPC body to the gateway through the identity's
// service, naming harness and the harness session id window, so the call
// rides that window's session. The service adds the token and its proof, so
// no token leaves it.
func (c *client) mcp(ctx context.Context, harness, window string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, localapi.BaseURL+localapi.RouteMCP, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(localapi.HeaderHarness, harness)
	req.Header.Set(localapi.HeaderHarnessSession, window)
	resp, err := c.local.HTTPClient().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("gateway call through the service of %s: %w", c.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, raw, err
}

// waitPush waits until the service holds the revocation stream of the
// session it keeps for the harness window, so a kill-switch timer measured
// after a session start covers the pushed path.
func (c *client) waitPush(ctx context.Context, harness, window string) error {
	deadline := time.Now().Add(10 * time.Second)
	last := "no session for the window"
	for {
		st, err := c.local.Status(ctx)
		if err != nil {
			last = err.Error()
		}
		for _, ses := range st.Sessions {
			if ses.Harness.Name == harness && ses.Harness.Session == window {
				if ses.Push == localapi.PushConnected {
					return nil
				}
				last = "push " + ses.Push
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the service of %s did not connect the revocation stream of window %s within 10s (%s)", c.name, window, last)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// cleanup stops the service and removes the identity's directories with a
// short retry, so a file the service wrote while stopping never trips the
// temp dir cleanup.
func (c *client) cleanup() {
	c.service.stop()
	for i := 0; i < 5; i++ {
		if os.RemoveAll(filepath.Dir(c.home)) == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
