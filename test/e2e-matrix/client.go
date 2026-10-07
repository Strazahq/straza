package e2ematrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// client is one identity's straza installation: its own STRAZA_HOME and
// HOME under the scenario's temp dir, its enrolment, its hooks and its
// daemon. Only straza hook, enroll, keygen and daemon are ever spawned.
type client struct {
	name     string
	kind     string
	password string
	home     string
	fake     string
	bin      string
	server   *strazad
	daemon   *proc
	// lastToken is the session token the identity's last session.start
	// opened, kept so a gateway call after a kill-switch still presents the
	// revoked session once the daemon has dropped the state file.
	lastToken string
}

// newClient lays out the identity's directories under dir.
func newClient(name string, id identity, bin, dir string, server *strazad) (*client, error) {
	c := &client{name: name, kind: id.Kind, password: id.Password, bin: bin, server: server,
		home: filepath.Join(dir, name, "straza"), fake: filepath.Join(dir, name, "home")}
	for _, d := range []string{c.home, c.fake} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	return c, nil
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
	daemonUpRE  = regexp.MustCompile(`kill-switch push active`)
)

// enroll runs the identity's enrolment: the device flow approved with the
// declared password for a human, keygen plus headless enrolment for an NHI.
func (c *client) enroll(ctx context.Context) error {
	if c.kind == "nhi" {
		return c.enrollHeadless(ctx)
	}
	p, err := startProc("straza enroll", c.bin, []string{"enroll", "--server", c.server.URL}, c.env(), nil)
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

// sessionToken returns the session the identity's last session.start opened:
// the client's own state file while it exists, else the token remembered
// when that session.start ran, so a revoked session can still be presented.
func (c *client) sessionToken() (string, error) {
	raw, err := os.ReadFile(filepath.Join(c.home, "state", "session.json")) // #nosec G304 -- this identity's state under the temp dir
	if err != nil {
		if c.lastToken != "" {
			return c.lastToken, nil
		}
		return "", fmt.Errorf("%s has no session state (a session.start hook step opens one): %w", c.name, err)
	}
	var ses struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(raw, &ses); err != nil || ses.SessionToken == "" {
		return "", fmt.Errorf("%s session state carries no token", c.name)
	}
	c.lastToken = ses.SessionToken
	return ses.SessionToken, nil
}

// startDaemon runs straza daemon for the identity and waits until its push
// stream is live, so a timer measured after it covers the connected path.
func (c *client) startDaemon() error {
	if c.daemon != nil && !c.daemon.exited() {
		return fmt.Errorf("%s already has a daemon", c.name)
	}
	p, err := startProc("straza daemon", c.bin, []string{"daemon"}, c.env(), nil)
	if err != nil {
		return err
	}
	c.daemon = p
	if _, err := p.waitForLine(daemonUpRE, 20*time.Second); err != nil {
		p.stop()
		return err
	}
	return nil
}

// stopDaemon ends the daemon by process id; an already exited daemon (one
// that saw its revocation) is not an error.
func (c *client) stopDaemon() error {
	if c.daemon == nil {
		return errors.New("no daemon is running for " + c.name)
	}
	c.daemon.stop()
	return nil
}

// cleanup stops the daemon and removes the identity's directories with a
// short retry, so a detached audit drain finishing late never leaves a
// file behind for the temp dir cleanup to trip on.
func (c *client) cleanup() {
	if c.daemon != nil {
		c.daemon.stop()
	}
	for i := 0; i < 5; i++ {
		if os.RemoveAll(filepath.Dir(c.home)) == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
