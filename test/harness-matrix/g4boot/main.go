// g4boot is the codex live-model lane's toolbox: everything g3-live and g4-e2e
// need that shell should not be doing, stdlib-only. Modes (-mode):
//
//	port      one free TCP port. The lane needs strazad's port BEFORE boot:
//	          server.publicUrl feeds OIDC discovery and `straza enroll` follows
//	          it, so --listen 127.0.0.1:0 leaves the enroll leg 404ing.
//	bootstrap wait for /healthz, read the one-time admin password from strazad's
//	          OWN log (never argv or env: it must not reach the process table
//	          or the drift report), device flow, NHI, keygen key, PolicySet.
//	          The admin token is written 0600 into the work dir, never printed.
//	evidence  poll the admin API for the server-side facts the g4 gate asserts
//	          on and write them as JSON for check/codex.go.
//	mcp-echo  line-delimited stdio MCP server, the tool-event ladder's target,
//	          registered in $CODEX_HOME/config.toml as [mcp_servers.github]
//	          (g3-live) or [mcp_servers.straza], the GatewayProxied key.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func main() {
	mode := flag.String("mode", "", "port | bootstrap | evidence | mcp-echo")
	server := flag.String("server", "", "strazad base URL")
	logFile := flag.String("log", "", "strazad log to read the one-time bootstrap admin password from")
	user := flag.String("user", "g4-agent", "NHI username")
	strazaBin := flag.String("straza", "", "path to the straza binary (keygen)")
	state := flag.String("state", "", "lane state dir (admin token, ids)")
	out := flag.String("out", "", "evidence JSON path")
	waitFor := flag.Duration("wait", 45*time.Second, "how long to wait for async server-side state")
	name := flag.String("name", "echo", "mcp-echo: server name reported in serverInfo")
	flag.Parse()

	var err error
	switch *mode {
	case "port":
		err = printFreePort()
	case "bootstrap":
		err = bootstrap(*server, *logFile, *user, *strazaBin, *state, *waitFor)
	case "evidence":
		err = evidence(*server, *user, *state, *out, *waitFor)
	case "mcp-echo":
		err = mcpEcho(*name)
	default:
		fmt.Fprintln(os.Stderr, "g4boot: -mode must be port|bootstrap|evidence|mcp-echo")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "g4boot[%s]: %v\n", *mode, err)
		os.Exit(1)
	}
}

// ------------------------------------------------------------------- port

func printFreePort() error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		return err
	}
	fmt.Println(port)
	return nil
}

// --------------------------------------------------------------- HTTP bits

var client = &http.Client{Timeout: 60 * time.Second}

func do(method, u string, body []byte, hdr map[string]string) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, r)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

func postForm(u string, form url.Values) (int, map[string]any, error) {
	code, raw, err := do("POST", u, []byte(form.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if err != nil {
		return code, nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return code, m, nil
}

func postJSON(u, bearer string, body any, into any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	code, resp, err := do("POST", u, raw, map[string]string{
		"Content-Type": "application/json", "Authorization": "Bearer " + bearer})
	if err != nil {
		return code, err
	}
	if into != nil && len(resp) > 0 {
		_ = json.Unmarshal(resp, into)
	}
	return code, nil
}

func getJSON(u, bearer string, into any) error {
	_, resp, err := do("GET", u, nil, map[string]string{"Authorization": "Bearer " + bearer})
	if err != nil {
		return err
	}
	if into != nil && len(resp) > 0 {
		if err := json.Unmarshal(resp, into); err != nil {
			return fmt.Errorf("%s: %w (%s)", u, err, truncate(string(resp), 200))
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// -------------------------------------------------------------- bootstrap

// bootstrapPassword mirrors the agent-e2e run.sh approach: the one-time
// admin password is printed ONCE, to strazad's log, and read from there. It
// never becomes an argv, an env var, or a report line.
var passwordRE = regexp.MustCompile(`"password":"([^"]+)"`)

func readBootstrapPassword(path string, deadline time.Time) (string, error) {
	for {
		raw, err := os.ReadFile(path) // #nosec G304 -- the lane's own log file
		if err == nil {
			if m := passwordRE.FindSubmatch(raw); m != nil {
				return string(m[1]), nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no bootstrap admin password in %s: strazad never reached first-boot admin creation", path)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func waitHealthy(server string, deadline time.Time) error {
	for {
		if code, _, err := do("GET", server+"/healthz", nil, nil); err == nil && code < 500 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("strazad never became reachable at %s", server)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// adminToken runs the built-in issuer's RFC 8628 device flow the way a human
// browser would, without a browser: request a code, approve it with the
// bootstrap admin credentials, poll the token endpoint.
func adminToken(server, password string) (string, error) {
	code, auth, err := postForm(server+"/oidc/device_authorization",
		url.Values{"client_id": {"strazactl"}, "scope": {"openid"}})
	if err != nil || code != 200 {
		return "", fmt.Errorf("device_authorization: %d %v", code, err)
	}
	userCode, _ := auth["user_code"].(string)
	deviceCode, _ := auth["device_code"].(string)
	if userCode == "" || deviceCode == "" {
		return "", errors.New("device_authorization returned no codes")
	}
	code, _, err = postForm(server+"/oidc/device", url.Values{
		"user_code": {userCode}, "username": {"admin"}, "password": {password}})
	if err != nil || code >= 400 {
		return "", fmt.Errorf("device approval rejected: %d %v", code, err)
	}
	for i := 0; i < 30; i++ {
		code, tok, err := postForm(server+"/oidc/token", url.Values{
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
		return "", fmt.Errorf("token poll: %d %v", code, tok)
	}
	return "", errors.New("device-flow token never arrived")
}

// capturePolicy is the ONE thing the g4 lane adds to the starter set. A
// fresh standalone store already seeds a working policy, so no apply is
// needed for the DECISION path. But nothing a turn does without a tool call
// leaves a server-side trace: session.start is emitted as
// straza.identity.updated (outbox only, and /v1/admin/changes filters it as
// liveSync noise), and session.end has no audit CE type at all: it drains
// the client spool and returns. Capture gives prompt.submit something to
// spool, so the gate can assert the WHOLE async audit path (hook → local PDP
// → spool → drain → /v1/audit/batch → outbox → spine → hash-chained
// audit_log) on a turn that makes no tool call. rules[] must be non-empty
// (the parser rejects an empty set), so the placeholder rule is written to
// match nothing.
const capturePolicy = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: harness-matrix-capture
  description: harness-matrix g4-e2e lane. Exists for its capture directive. One live turn has to leave a server-side audit trail.
spec:
  priority: 10
  capture:
    conversations: true
    mode: verbatim
  rules:
    - id: harness-matrix-placeholder
      tools: [net.fetch]
      paths:
        deny: ["**/straza-harness-matrix-never-matches/**"]
      effect: deny
      reason: "harness-matrix placeholder rule (this set exists for its capture directive)"
`

func bootstrap(server, logFile, user, strazaBin, state string, wait time.Duration) error {
	if server == "" || logFile == "" || strazaBin == "" || state == "" {
		return errors.New("-server, -log, -straza and -state are required")
	}
	deadline := time.Now().Add(wait)
	if err := waitHealthy(server, deadline); err != nil {
		return err
	}
	password, err := readBootstrapPassword(logFile, deadline)
	if err != nil {
		return err
	}
	token, err := adminToken(server, password)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(state, "admin-token"), []byte(token), 0o600); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "g4boot: admin login ok (scripted device flow)")

	var created struct {
		ID string `json:"id"`
	}
	code, err := postJSON(server+"/v1/admin/users", token,
		map[string]any{"username": user, "display": "harness-matrix g4 NHI", "kind": "nhi"}, &created)
	if err != nil {
		return fmt.Errorf("create NHI: %w", err)
	}
	if code != 201 || created.ID == "" {
		return fmt.Errorf("create NHI: %d (a virgin store must accept it)", code)
	}
	if err := os.WriteFile(filepath.Join(state, "user-id"), []byte(created.ID), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "g4boot: NHI %s ready (%s)\n", user, created.ID)

	// keygen writes the private half under the lane's redirected STRAZA_HOME
	// (inherited from this process's env; the lane never touches a real one).
	cmd := exec.Command(strazaBin, "keygen", "--user", user, "--force") // #nosec G204 -- the lane's own binary path
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keygen: %w (%s)", err, truncate(stderr.String(), 300))
	}
	m := regexp.MustCompile(`Public key: (\S+)`).FindStringSubmatch(stdout.String())
	if m == nil {
		return fmt.Errorf("keygen printed no public key: %s", truncate(stdout.String(), 300))
	}
	code, resp, err := do("PUT", server+"/v1/admin/users/"+created.ID+"/nhi-key",
		mustJSON(map[string]any{"public_key": m[1]}),
		map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + token})
	if err != nil || code >= 400 {
		return fmt.Errorf("nhi-key set: %d %v %s", code, err, truncate(string(resp), 200))
	}

	code, resp, err = do("PUT", server+"/v1/admin/policies", []byte(capturePolicy),
		map[string]string{"Content-Type": "application/yaml", "Authorization": "Bearer " + token})
	if err != nil || (code != 200 && code != 201) {
		return fmt.Errorf("capture policy apply: %d %v %s", code, err, truncate(string(resp), 300))
	}
	code, err = postJSON(server+"/v1/admin/policies/harness-matrix-capture/activate", token,
		map[string]any{"status": "active"}, nil)
	if err != nil || code >= 400 {
		return fmt.Errorf("capture policy activate: %d %v", code, err)
	}
	fmt.Fprintln(os.Stderr, "g4boot: NHI key registered, capture PolicySet active")
	return nil
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// --------------------------------------------------------------- evidence

// g4Evidence is the contract between this tool and check/codex.go's g4-e2e
// gate: what the SERVER saw, read back over the admin API with the admin
// token. Everything here is a fact the live turn produced; nothing is
// derived from the client's own files.
type g4Evidence struct {
	Server   string      `json:"server"`
	User     string      `json:"user"`
	UserID   string      `json:"userId"`
	Sessions []g4Session `json:"sessions"`
	// AuditCounts is CE type → rows for this user in the hash-chained log.
	AuditCounts map[string]int `json:"auditCounts"`
	// ToolDecisions are the straza.audit.tool rows, flattened.
	ToolDecisions []g4Tool `json:"toolDecisions"`
	// Waited records how long the poll ran, so a thin result is readable as
	// "async audit had not landed yet" rather than "never happened".
	Waited string `json:"waited"`
}

type g4Session struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Harness  string `json:"harness"`
	Status   string `json:"status"`
}

type g4Tool struct {
	Event    string `json:"event"`
	Tool     string `json:"tool"`
	App      string `json:"app"`
	ToolName string `json:"toolName"`
	Effect   string `json:"effect"`
	Reason   string `json:"reason"`
}

type auditRow struct {
	Seq      int64  `json:"seq"`
	CE       string `json:"ce"`
	Username string `json:"username"`
}

func evidence(server, user, state, out string, wait time.Duration) error {
	if server == "" || state == "" || out == "" {
		return errors.New("-server, -state and -out are required")
	}
	tokenRaw, err := os.ReadFile(filepath.Join(state, "admin-token")) // #nosec G304 -- lane state dir
	if err != nil {
		return fmt.Errorf("admin token unreadable (did bootstrap run?): %w", err)
	}
	token := strings.TrimSpace(string(tokenRaw))
	uid, _ := os.ReadFile(filepath.Join(state, "user-id")) // #nosec G304 -- lane state dir

	ev := g4Evidence{Server: server, User: user, UserID: strings.TrimSpace(string(uid)),
		AuditCounts: map[string]int{}}
	start := time.Now()
	deadline := start.Add(wait)
	for {
		ev.Sessions, ev.AuditCounts, ev.ToolDecisions = nil, map[string]int{}, nil

		var sessions []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Harness  string `json:"harness"`
			Status   string `json:"status"`
		}
		if err := getJSON(server+"/v1/admin/sessions", token, &sessions); err != nil {
			return fmt.Errorf("list sessions: %w", err)
		}
		for _, s := range sessions {
			if s.Username == user {
				ev.Sessions = append(ev.Sessions, g4Session{s.ID, s.Username, s.Harness, s.Status})
			}
		}

		var rows []auditRow
		if err := getJSON(server+"/v1/admin/audit?limit=500&user="+url.QueryEscape(user), token, &rows); err != nil {
			return fmt.Errorf("list audit: %w", err)
		}
		for _, r := range rows {
			var ce struct {
				Type string `json:"type"`
				Data struct {
					Event    string `json:"event"`
					Tool     string `json:"tool"`
					App      string `json:"app"`
					ToolName string `json:"toolName"`
					Effect   string `json:"effect"`
					Reason   string `json:"reason"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(r.CE), &ce) != nil {
				continue
			}
			ev.AuditCounts[ce.Type]++
			if ce.Type == "straza.audit.tool" {
				ev.ToolDecisions = append(ev.ToolDecisions, g4Tool{
					Event: ce.Data.Event, Tool: ce.Data.Tool, App: ce.Data.App,
					ToolName: ce.Data.ToolName, Effect: ce.Data.Effect, Reason: ce.Data.Reason})
			}
		}
		// The audit path is async by invariant (it never runs on a request
		// path): poll until the rows the gate needs have landed, then stop,
		// and never sleep the full budget on a healthy run. A capture-active
		// completing turn owes BOTH conversation rows. Exiting on prompt
		// alone would make the gate blind to reply capture: the prompt row is
		// minutes old by the time evidence runs, while the reply, drained at
		// session.end moments before codex exits, is still in the async
		// ingest path (outbox relay → NATS → batched chain writer, about 2 s),
		// so a first-poll exit would read "no reply" on every green run.
		if len(ev.Sessions) > 0 && ev.AuditCounts["straza.audit.prompt"] > 0 &&
			ev.AuditCounts["straza.audit.reply"] > 0 {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	ev.Waited = time.Since(start).Round(time.Second).String()
	raw, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "g4boot: evidence after %s: sessions=%d audit=%v\n",
		ev.Waited, len(ev.Sessions), ev.AuditCounts)
	return os.WriteFile(out, append(raw, '\n'), 0o600)
}

// --------------------------------------------------------------- mcp-echo

// mcpEcho speaks just enough MCP over line-delimited stdio for codex to
// register the server and advertise its tools to the model: initialize,
// tools/list, tools/call. Two tools by design: a read (`get_item`, which
// the tier-1 conformance policy ALLOWS for app github) and a destructive one
// (`delete_repo`), so the same fixture can grow a deny case without a second
// server. Notifications (no id) are consumed silently, as the transport
// requires.
//
// This is the target of the tool-event ATTEMPT ladder, never of a hard
// assertion: codex --oss with ollama delivers tool calls to codex's router
// with an EMPTY function name
// ("codex_core::tools::router: error=unsupported call:"), so no MCP dispatch
// reaches the hook. Direct /v1/responses probes return the name correctly,
// and the defect appears only under codex's full request, so it is recorded as
// vendor drift, not as a Straza bug.
func mcpEcho(name string) error {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		id, hasID := msg["id"]
		method, _ := msg["method"].(string)
		params, _ := msg["params"].(map[string]any)

		var result any
		switch method {
		case "initialize":
			version := "2025-06-18"
			if v, ok := params["protocolVersion"].(string); ok && v != "" {
				version = v
			}
			result = map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": name, "version": "1.0.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{
					"name":        "get_item",
					"description": "Fetch an item by name and return its value.",
					"inputSchema": map[string]any{"type": "object",
						"properties": map[string]any{"name": map[string]any{"type": "string"}},
						"required":   []string{"name"}},
				},
				map[string]any{
					"name":        "delete_repo",
					"description": "Delete a repository by name.",
					"inputSchema": map[string]any{"type": "object",
						"properties": map[string]any{"name": map[string]any{"type": "string"}},
						"required":   []string{"name"}},
				},
			}}
		case "tools/call":
			tool, _ := params["name"].(string)
			args := mustJSON(params["arguments"])
			result = map[string]any{"content": []any{map[string]any{
				"type": "text", "text": fmt.Sprintf("MCPECHO %s ok: %s", tool, args)}}}
		default:
			result = map[string]any{}
		}
		if !hasID { // notification: no response, by transport contract
			continue
		}
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
			return err
		}
	}
}
