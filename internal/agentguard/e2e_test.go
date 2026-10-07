package agentguard_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/server"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuf) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

// bootStrazad starts a real standalone strazad and seeds a dev user with the
// block-rm policy active. Returns base URL and the dev user's password.
// Mutators adjust the config before boot (e.g. the require-managed attestation gate).
func bootStrazad(t *testing.T, mutators ...func(*config.Config)) (base, adminPassword string) {
	t.Helper()
	dir := t.TempDir()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	base = "http://" + addr

	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: addr, PublicURL: base},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
	}
	for _, mutate := range mutators {
		mutate(&cfg)
	}
	logBuf := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())

	// The migrated template leaves the boot nothing to migrate, which is
	// most of a boot's cost under the race detector.
	storetest.SeedSQLite(t, cfg.SQLitePath())
	app, err := server.New(ctx, cfg, logging.New(cfg.Log, logBuf))
	if err != nil {
		cancel()
		t.Fatalf("server.New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("strazad shutdown timed out")
		}
	})

	// Seed: user kim (dev role + straza-admin) with a password.
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctxb := context.Background()
	hash, _ := storetest.PasswordHash("hunter2!")
	kim, err := st.Users().Create(ctxb, store.User{Username: "kim", Email: "kim@x.io", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	// Application kind: the fixture policy matches [dev] and revision 16
	// lets only application-kind roles appear in match.roles (the store
	// would default "" to business, which activation now refuses).
	dev, _ := st.Roles().Create(ctxb, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	admin, _ := st.Roles().GetByName(ctxb, server.AdminRole)
	for _, rid := range []string{dev.ID, admin.ID} {
		if _, err := st.Roles().Assign(ctxb, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: kim.ID, RoleID: rid}); err != nil {
			t.Fatal(err)
		}
	}
	// Knowledge pack bound to dev (delivered at session.start).
	pack, _ := st.Packs().Create(ctxb, store.KnowledgePack{Name: "golang-style", Version: "1", Content: "always run gofmt", Checksum: "c1"})
	_ = st.Packs().Bind(ctxb, dev.ID, pack.ID)

	// The block-rm policy, applied and activated via the admin API so the
	// snapshot recompiles and distributes.
	activatePolicy(t, base)
	return base, "hunter2!"
}

const blockRM = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: block-rm }
spec:
  match: { roles: [dev] }
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "Straza: destructive delete blocked for role dev"
`

// activatePolicy logs in as kim (admin) and applies+activates block-rm.
func activatePolicy(t *testing.T, base string) {
	t.Helper()
	idToken := deviceLogin(t, base, "strazactl", "kim", "hunter2!")
	req, _ := http.NewRequest("PUT", base+"/v1/admin/policies", strings.NewReader(blockRM))
	req.Header.Set("Authorization", "Bearer "+idToken)
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("apply policy: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", base+"/v1/admin/policies/block-rm/activate", strings.NewReader(`{"status":"active"}`))
	req.Header.Set("Authorization", "Bearer "+idToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activate policy: %d", resp.StatusCode)
	}
}

// deviceLogin runs the device flow for clientID and approves it as the user.
func deviceLogin(t *testing.T, base, clientID, username, password string) string {
	t.Helper()
	resp, err := http.PostForm(base+"/oidc/device_authorization", url.Values{"client_id": {clientID}})
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&auth)
	_ = resp.Body.Close()

	resp, err = http.PostForm(base+"/oidc/device", url.Values{
		"user_code": {auth["user_code"].(string)}, "username": {username}, "password": {password},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	resp, err = http.PostForm(base+"/oidc/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {auth["device_code"].(string)},
		"client_id":   {clientID},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	_ = resp.Body.Close()
	return tok["id_token"].(string)
}

func hook(t *testing.T, harness, payload string, env []string) (stdout string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	err := agentguard.RunHook(agentguard.HookIO{
		Harness: harness,
		Stdin:   strings.NewReader(payload),
		Stdout:  &out,
		Stderr:  &errb,
		Environ: func() []string { return env },
	})
	if err != nil {
		if c, ok := err.(interface{ Code() int }); ok {
			code = c.Code()
		} else {
			t.Fatalf("RunHook: %v", err)
		}
	}
	return out.String(), code
}

// TestM2Demo runs the end-to-end demo as a test: enroll → session.start
// (checkin + pack injection) → PreToolUse blocks `rm -rf` with a reason,
// allows a benign command, and the block is auditable server-side.
func TestM2Demo(t *testing.T) {
	base, _ := bootStrazad(t)

	// Isolate straza state to a temp home.
	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)
	env := []string{"STRAZA_HOME=" + home, "CLAUDECODE=1"}

	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	driveEnroll(t, store, base)

	// SessionStart hook → checkin, pack injected as additionalContext.
	ssPayload := `{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/work"}`
	ssOut, ssCode := hook(t, "claude-code", ssPayload, env)
	if ssCode != 0 {
		t.Fatalf("session.start blocked: %d", ssCode)
	}
	if !strings.Contains(ssOut, "golang-style") || !strings.Contains(ssOut, "gofmt") {
		t.Errorf("knowledge pack not injected at session.start:\n%s", ssOut)
	}

	// PreToolUse rm -rf → DENY with reason and exit 2 (the demo).
	rmPayload := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/work","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`
	rmOut, rmCode := hook(t, "claude-code", rmPayload, env)
	if rmCode != 2 {
		t.Fatalf("rm -rf not blocked (exit %d): %s", rmCode, rmOut)
	}
	var resp2 struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(rmOut), &resp2); err != nil {
		t.Fatalf("deny output not JSON: %v: %s", err, rmOut)
	}
	if resp2.HookSpecificOutput.PermissionDecision != "deny" ||
		!strings.Contains(resp2.HookSpecificOutput.PermissionDecisionReason, "destructive delete blocked") {
		t.Errorf("unexpected deny response: %+v", resp2.HookSpecificOutput)
	}

	// A benign command is allowed.
	lsPayload := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/work","tool_name":"Bash","tool_input":{"command":"ls -la"}}`
	lsOut, lsCode := hook(t, "claude-code", lsPayload, env)
	if lsCode != 0 {
		t.Fatalf("benign command blocked (exit %d): %s", lsCode, lsOut)
	}

	// The local decision matched the server's snapshot. Verify the session
	// carries the expected role and a real snapshot id.
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if len(ses.Roles) == 0 || ses.SnapshotID == "" {
		t.Errorf("session state incomplete: %+v", ses)
	}
	found := false
	for _, r := range ses.Roles {
		if r == "dev" {
			found = true
		}
	}
	if !found {
		t.Errorf("session missing dev role: %v", ses.Roles)
	}

	// The two governed decisions (deny rm, allow ls) were spooled.
	sp := spool.NewSpool(store.SpoolPath())
	if pending, _ := sp.Pending(); pending != 2 {
		t.Fatalf("spool pending = %d, want 2 governed decisions", pending)
	}

	// session.end drains the spool to the server; the events reach the
	// tamper-evident audit chain (full-stack zero loss).
	endPayload := `{"hook_event_name":"Stop","session_id":"s1","cwd":"/work"}`
	if _, endCode := hook(t, "claude-code", endPayload, env); endCode != 0 {
		t.Fatalf("session.end blocked: %d", endCode)
	}
	if pending, _ := sp.Pending(); pending != 0 {
		t.Errorf("spool not drained on session.end: %d pending", pending)
	}

	// The drained client audit reached the server audit log (source=agentguard).
	deadline := time.Now().Add(5 * time.Second)
	var clientAudits int
	for time.Now().Before(deadline) {
		recs := fetchAudit(t, base)
		clientAudits = 0
		for _, ce := range recs {
			if strings.Contains(ce, `"source":"straza"`) {
				clientAudits++
			}
		}
		if clientAudits >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if clientAudits < 2 {
		t.Errorf("client-spooled audits in chain = %d, want ≥2", clientAudits)
	}
	_ = io.Discard
}

// TestEnrollHumanTimeline enrolls with the approval arriving AFTER the first
// token poll: the timeline every real human produces and every fast-approve
// test dodges. The first poll's HTTP 400 authorization_pending must keep
// Enroll waiting, not end it. Costs ~5 s wall (the issuer's 2 s poll interval is not configurable from
// here); the wire states themselves are unit-pinned in client_test.go.
func TestEnrollHumanTimeline(t *testing.T) {
	base, _ := bootStrazad(t)
	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)
	agStore, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}

	enrollOut := &syncBuf{}
	enrollErr := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() { enrollErr <- agentguard.Enroll(ctx, agStore, base, enrollOut) }()

	codeRe := regexp.MustCompile(`confirm code ([A-Z2-9]{4}-[A-Z2-9]{4})`)
	var userCode string
	for i := 0; i < 200 && userCode == ""; i++ {
		if m := codeRe.FindStringSubmatch(enrollOut.String()); m != nil {
			userCode = m[1]
		} else {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if userCode == "" {
		t.Fatalf("no user code from enroll: %q", enrollOut.String())
	}

	// The human is typing their password: let at least one poll hit the wire
	// while the grant is still pending. Poll-first fires the first poll
	// immediately (authorization_pending); the 3 s hold lets that pending poll
	// (and the 2 s-interval retry behind it) land before we approve.
	time.Sleep(3 * time.Second)
	select {
	case err := <-enrollErr:
		t.Fatalf("enroll gave up while the user was still authorizing: %v", err)
	default:
	}
	resp, err := http.PostForm(base+"/oidc/device", url.Values{
		"user_code": {userCode}, "username": {"kim"}, "password": {"hunter2!"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if err := <-enrollErr; err != nil {
		t.Fatalf("Enroll after delayed approval: %v", err)
	}
	if _, err := agStore.LoadIdentity(); err != nil {
		t.Fatalf("no identity persisted: %v", err)
	}
}

// TestSessionStartOutlivesLoginToken pins the device credential client-side: the 10-minute
// login token dying must not stop new sessions; the device credential
// carries them. Simulated by poisoning the stored ID token after enroll
// (equivalent to it having expired); SessionStart must still check in.
func TestSessionStartOutlivesLoginToken(t *testing.T) {
	base, _ := bootStrazad(t)
	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)
	agStore, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	driveEnroll(t, agStore, base)

	id, err := agStore.LoadIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if id.DeviceToken == "" {
		t.Fatal("enroll persisted no device token: D27 credential missing")
	}
	id.IDToken = "expired.long.ago"
	if err := agStore.SaveIdentity(id); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, _, err := agentguard.SessionStart(ctx, agStore, "claude-code", "2.1.0"); err != nil {
		t.Fatalf("SessionStart with dead login token: %v", err)
	}
	ses, err := agStore.LoadSession()
	if err != nil || ses.SessionToken == "" {
		t.Fatalf("no session persisted: %+v (%v)", ses, err)
	}
}

// driveEnroll runs `straza enroll` as kim, driving the device approval
// concurrently the way a human browser would.
func driveEnroll(t *testing.T, store *agentguard.Store, base string) {
	t.Helper()
	enrollOut := &syncBuf{}
	enrollErr := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() { enrollErr <- agentguard.Enroll(ctx, store, base, enrollOut) }()

	codeRe := regexp.MustCompile(`confirm code ([A-Z2-9]{4}-[A-Z2-9]{4})`)
	var userCode string
	for i := 0; i < 200 && userCode == ""; i++ {
		if m := codeRe.FindStringSubmatch(enrollOut.String()); m != nil {
			userCode = m[1]
		} else {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if userCode == "" {
		t.Fatalf("no user code from enroll: %q", enrollOut.String())
	}
	resp, err := http.PostForm(base+"/oidc/device", url.Values{
		"user_code": {userCode}, "username": {"kim"}, "password": {"hunter2!"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if err := <-enrollErr; err != nil {
		t.Fatalf("Enroll: %v", err)
	}
}

// fetchAudit reads the server audit log as an admin (kim).
func fetchAudit(t *testing.T, base string) []string {
	t.Helper()
	idToken := deviceLogin(t, base, "strazactl", "kim", "hunter2!")
	req, _ := http.NewRequest("GET", base+"/v1/admin/audit?limit=200", nil)
	req.Header.Set("Authorization", "Bearer "+idToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var recs []struct {
		CE string `json:"ce"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&recs)
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.CE
	}
	return out
}
