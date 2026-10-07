package e2ematrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// strazad controls the throwaway server: boot on a reserved loopback port,
// the bound port and the one-time bootstrap password read from its own log,
// stop and restart on the same port and data directory, and the admin and
// IdM credentials the steps act with. The password and both tokens live
// only in this struct and never reach a log line or the report.
type strazad struct {
	bin          string
	dataDir      string
	logPath      string
	configPath   string
	governance   map[string]string
	port         int
	approverPort int
	URL          string
	proc         *proc
	logFile      *os.File
	password     string
	admin        string
	idm          string
	coldStart    time.Duration
	booted       bool
}

// freePort reserves and releases a loopback port.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return port, ln.Close()
}

// newStrazad prepares a server under dir with the built binary. A non-empty
// governance map becomes the server's config file, the only face the
// offline grace bound has.
func newStrazad(bin, dir string, governance map[string]string) (*strazad, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	approver, err := freePort()
	if err != nil {
		return nil, err
	}
	s := &strazad{
		bin: bin, dataDir: filepath.Join(dir, "data"), logPath: filepath.Join(dir, "strazad.log"),
		governance: governance, port: port, approverPort: approver, URL: fmt.Sprintf("http://127.0.0.1:%d", port),
	}
	if len(governance) > 0 {
		s.configPath = filepath.Join(dir, "straza.yaml")
	}
	return s, nil
}

// governanceYAML renders the scenario's lifetimes as the config file's
// governance section, keys in order. The values passed validation as
// durations, so the document carries nothing but them.
func governanceYAML(g map[string]string) []byte {
	var b strings.Builder
	b.WriteString("governance:\n")
	for _, key := range sortedKeys(g) {
		fmt.Fprintf(&b, "  %s: %s\n", key, g[key])
	}
	return []byte(b.String())
}

var (
	servingRE   = regexp.MustCompile(`"strazad serving".*"addr":"127\.0\.0\.1:(\d+)"`)
	bootstrapRE = regexp.MustCompile(`"password":"([^"]+)"`)
)

// start spawns strazad serve and waits for /readyz. On the first boot it
// reads the bootstrap password from the log; the cold-start timer runs from
// spawn to the first ready answer.
func (s *strazad) start(ctx context.Context) error {
	if s.proc != nil && !s.proc.exited() {
		return errors.New("strazad is already running")
	}
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- the run's own log under its temp dir
	if err != nil {
		return err
	}
	s.logFile = f
	env := childEnv(
		"STRAZA_PUBLIC_URL="+s.URL,
		fmt.Sprintf("STRAZA_APPROVER_TLS_LISTEN=127.0.0.1:%d", s.approverPort),
		"STRAZA_APPROVAL_GATEWAY_HOLD_SECONDS=2",
	)
	args := []string{"serve", "--profile", "standalone", "--data-dir", s.dataDir,
		"--listen", fmt.Sprintf("127.0.0.1:%d", s.port), "--log-level", "info"}
	if s.configPath != "" {
		if err := os.WriteFile(s.configPath, governanceYAML(s.governance), 0o600); err != nil {
			_ = f.Close()
			return err
		}
		args = append(args, "--config", s.configPath)
	}
	spawn := time.Now()
	p, err := startProc("strazad", s.bin, args, env, f)
	if err != nil {
		_ = f.Close()
		return err
	}
	s.proc = p
	if err := s.waitReady(ctx, 60*time.Second); err != nil {
		return err
	}
	s.coldStart = time.Since(spawn)
	raw, err := os.ReadFile(s.logPath) // #nosec G304 -- the run's own log
	if err != nil {
		return err
	}
	m := servingRE.FindSubmatch(raw)
	if m == nil {
		return errors.New("strazad log carries no serving line with the bound address")
	}
	if bound, _ := strconv.Atoi(string(m[1])); bound != s.port {
		return fmt.Errorf("strazad bound port %d, the run reserved %d", bound, s.port)
	}
	if !s.booted {
		pw := bootstrapRE.FindSubmatch(raw)
		if pw == nil {
			return errors.New("strazad log carries no bootstrap admin password on a virgin data directory")
		}
		s.password = string(pw[1])
		s.booted = true
	}
	return nil
}

func (s *strazad) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		code, _, err := httpCall(ctx, http.MethodGet, s.URL+"/readyz", "", "", nil)
		if err == nil && code == http.StatusOK {
			return nil
		}
		if s.proc.exited() {
			raw, _ := os.ReadFile(s.logPath) // #nosec G304 -- the run's own log
			return fmt.Errorf("strazad exited before ready: %s", trim(redact(string(raw))))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("strazad not ready at %s after %s", s.URL, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stop ends the server by process id and closes its log.
func (s *strazad) stop() {
	if s.proc != nil {
		s.proc.stop()
	}
	if s.logFile != nil {
		_ = s.logFile.Close()
		s.logFile = nil
	}
}

// login signs in as the bootstrap admin through the built-in issuer's
// device flow, the way strazactl login does, and keeps the id token.
func (s *strazad) login(ctx context.Context) error {
	token, err := s.deviceToken(ctx, "admin", s.password)
	if err != nil {
		return err
	}
	s.admin = token
	return nil
}

// deviceToken signs one person in through the built-in issuer's device flow
// and returns their ID token, the person-bound credential the decide routes
// take. The password is never part of an error string.
func (s *strazad) deviceToken(ctx context.Context, username, password string) (string, error) {
	code, auth, err := postForm(ctx, s.URL+"/oidc/device_authorization",
		url.Values{"client_id": {"strazactl"}, "scope": {"openid"}})
	if err != nil || code != http.StatusOK {
		return "", fmt.Errorf("device_authorization: status %d, %v", code, err)
	}
	userCode, _ := auth["user_code"].(string)
	deviceCode, _ := auth["device_code"].(string)
	if userCode == "" || deviceCode == "" {
		return "", errors.New("device_authorization returned no codes")
	}
	if err := approveDevice(ctx, s.URL, userCode, username, password); err != nil {
		return "", err
	}
	for i := 0; i < 30; i++ {
		code, tok, err := postForm(ctx, s.URL+"/oidc/token", url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {deviceCode}, "client_id": {"strazactl"}})
		if err != nil {
			return "", fmt.Errorf("token poll: %w", err)
		}
		if id, _ := tok["id_token"].(string); id != "" {
			return id, nil
		}
		if e, _ := tok["error"].(string); e == "authorization_pending" || e == "slow_down" {
			time.Sleep(time.Second)
			continue
		}
		return "", fmt.Errorf("token poll: status %d, error %v", code, tok["error"])
	}
	return "", errors.New("device-flow token never arrived")
}

// mintIdM creates the admin API token with the SCIM grants that scenarios
// act as an IdM with.
func (s *strazad) mintIdM(ctx context.Context) error {
	code, body, err := s.adminJSON(ctx, http.MethodPost, "/v1/admin/api-tokens",
		map[string]any{"name": "e2e-matrix-idm", "scope": "scim:read,scim:write"})
	if err != nil {
		return err
	}
	if code != http.StatusCreated {
		return fmt.Errorf("api-token create: status %d", code)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return errors.New("api-token create answered without a token")
	}
	s.idm = out.Token
	return nil
}

// adminJSON sends a JSON body to the admin API with the admin token.
func (s *strazad) adminJSON(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return 0, nil, err
		}
	}
	return httpCall(ctx, method, s.URL+path, s.admin, "application/json", raw)
}

// adminYAML sends a YAML document to the admin API with the admin token.
func (s *strazad) adminYAML(ctx context.Context, method, path string, doc []byte) (int, []byte, error) {
	return httpCall(ctx, method, s.URL+path, s.admin, "application/yaml", doc)
}

// userByName finds a user through the admin API.
func (s *strazad) userByName(ctx context.Context, username string) (map[string]any, error) {
	code, body, err := s.adminJSON(ctx, http.MethodGet, "/v1/admin/users", nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("list users: status %d", code)
	}
	var users []map[string]any
	if err := json.Unmarshal(body, &users); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	for _, u := range users {
		if u["username"] == username {
			return u, nil
		}
	}
	return nil, nil
}

// lastAuditSeq returns the newest audit sequence number, 0 on an empty chain.
func (s *strazad) lastAuditSeq(ctx context.Context) (int64, error) {
	code, body, err := s.adminJSON(ctx, http.MethodGet, "/v1/admin/audit?order=desc&limit=1", nil)
	if err != nil {
		return 0, err
	}
	if code != http.StatusOK {
		return 0, fmt.Errorf("audit tail: status %d", code)
	}
	var rows []auditRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Seq, nil
}

// auditRow is one /v1/admin/audit item: the chain envelope with the event as
// a JSON string and the username the server resolved for it.
type auditRow struct {
	Seq      int64  `json:"seq"`
	CE       string `json:"ce"`
	Username string `json:"username"`
}

// auditSince pages the chain after seq, 100 rows a page.
func (s *strazad) auditSince(ctx context.Context, seq int64) ([]auditRow, error) {
	var out []auditRow
	for {
		code, body, err := s.adminJSON(ctx, http.MethodGet, fmt.Sprintf("/v1/admin/audit?after=%d&limit=100", seq), nil)
		if err != nil {
			return nil, err
		}
		if code != http.StatusOK {
			return nil, fmt.Errorf("audit page: status %d", code)
		}
		var rows []auditRow
		if err := json.Unmarshal(body, &rows); err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(rows) < 100 {
			return out, nil
		}
		seq = rows[len(rows)-1].Seq
	}
}
