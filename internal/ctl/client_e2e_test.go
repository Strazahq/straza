package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/server"
)

// syncBuffer is a goroutine-safe writer for capturing CLI/log output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// bootStrazad starts a real standalone strazad and returns its base URL plus
// the bootstrap admin password harvested from the logs.
func bootStrazad(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: "127.0.0.1:0", PublicURL: "placeholder"},
		Log:     config.Log{Level: "info", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
	}
	logBuf := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())

	// The issuer needs PublicURL == the bound address before boot: reserve a
	// free port, release it, and bind strazad to it explicitly.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	base := "http://" + addr
	cfg.Server.Listen = addr
	cfg.Server.PublicURL = base

	app, err := server.New(ctx, cfg, logging.New(cfg.Log, logBuf))
	if err != nil {
		cancel()
		t.Fatalf("New: %v", err)
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

	// The bootstrap password is printed once at first boot. Two
	// accounts print one now (bootstrap admin + break-glass), so key on the
	// username to capture the right credential.
	var password string
	for _, line := range strings.Split(logBuf.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil {
			if p, ok := rec["password"].(string); ok && rec["username"] == "admin" {
				password = p
			}
		}
	}
	if password == "" {
		t.Fatalf("bootstrap admin password not found in logs:\n%s", logBuf.String())
	}
	return base, password
}

// adminTokenClient signs the admin in at the built-in issuer as the console does,
// mints an admin API token with the full scope on that sign-in, as the
// console's Settings page does, and answers a client that sends the token,
// the way a person hands strazactl one.
func adminTokenClient(t *testing.T, base, adminPassword string) *Client {
	t.Helper()
	resp, err := http.PostForm(base+"/oidc/device_authorization", url.Values{"client_id": {"console"}})
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&auth)
	_ = resp.Body.Close()
	userCode, _ := auth["user_code"].(string)
	deviceCode, _ := auth["device_code"].(string)
	if userCode == "" || deviceCode == "" {
		t.Fatalf("device authorization answered %v", auth)
	}
	resp, err = http.PostForm(base+"/oidc/device", url.Values{
		"user_code": {userCode}, "username": {"admin"}, "password": {adminPassword},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	resp, err = http.PostForm(base+"/oidc/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
		"client_id":   {"console"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	_ = resp.Body.Close()
	idToken, _ := tok["id_token"].(string)
	if idToken == "" {
		t.Fatalf("no id_token for the admin: %v", tok)
	}
	// The console sends its sign-in as the bearer of the mint.
	console := NewClient(base)
	console.APIToken = idToken
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	minted, err := console.CreateAPIToken(ctx, "e2e", "full", time.Hour)
	if err != nil {
		t.Fatalf("mint an admin API token: %v", err)
	}
	c := NewClient(base)
	c.APIToken = minted.Token
	return c
}

// TestCLISetPassword pins the password rotation path: a user created without a
// password (the SCIM-born shape) cannot use the built-in issuer until an
// admin runs `users set-password`, and can afterwards.
func TestCLISetPassword(t *testing.T) {
	base, adminPassword := bootStrazad(t)
	admin := adminTokenClient(t, base, adminPassword)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	u, err := admin.CreateUser(ctx, "sam", "sam@x.io", "Sam", "")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Raw device flow: without a credential the login page refuses sam.
	resp, err := http.PostForm(base+"/oidc/device_authorization", url.Values{"client_id": {"console"}})
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&auth)
	_ = resp.Body.Close()
	userCode, _ := auth["user_code"].(string)

	approve := func() string {
		resp, err := http.PostForm(base+"/oidc/device", url.Values{
			"user_code": {userCode}, "username": {"sam"}, "password": {"n3w-pass!"},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		return body.String()
	}
	if page := approve(); !strings.Contains(page, "Invalid username or password") {
		t.Fatalf("passwordless user must be refused, got: %s", page)
	}

	if _, err := admin.SetUserPassword(ctx, u.ID, "n3w-pass!"); err != nil {
		t.Fatalf("SetUserPassword: %v", err)
	}
	if page := approve(); !strings.Contains(page, "Signed in") {
		t.Fatalf("post-rotation approval failed: %s", page)
	}
	resp, err = http.PostForm(base+"/oidc/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {auth["device_code"].(string)},
		"client_id":   {"console"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	_ = resp.Body.Close()
	if idt, _ := tok["id_token"].(string); idt == "" {
		t.Fatalf("no id_token after set-password: %v", tok)
	}
}

// TestCLIEndToEnd pins create role → assign → list and the rest, all
// through the strazactl client on an admin API token against a real strazad.
func TestCLIEndToEnd(t *testing.T) {
	base, adminPassword := bootStrazad(t)

	client := adminTokenClient(t, base, adminPassword)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create role → create user → assign → list.
	if _, err := client.CreateRole(ctx, "finance", "money people", "", "", nil); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if _, err := client.CreateUser(ctx, "bob", "bob@x.io", "Bob", "s3cret!pw"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := client.Assign(ctx, "bob", "finance"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	roles, err := client.Roles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	users, err := client.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	asg, err := client.Assignments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !containsRole(roles, "finance") || !containsUser(users, "bob") || len(asg) < 2 {
		t.Fatalf("lists: roles=%v users=%v assignments=%d", roles, users, len(asg))
	}
	// Unassign takes the row back, and a second run says that bob no longer
	// holds finance directly.
	if err := client.Unassign(ctx, "bob", "finance"); err != nil {
		t.Fatalf("Unassign: %v", err)
	}
	if err := client.Unassign(ctx, "bob", "finance"); err == nil || !strings.Contains(err.Error(), "bob does not hold finance directly") {
		t.Fatalf("Unassign again = %v, want the refusal that names the missing row", err)
	}
	if after, err := client.Assignments(ctx); err != nil || len(after) != len(asg)-1 {
		t.Fatalf("assignments after Unassign = %d, %v; want %d", len(after), err, len(asg)-1)
	}

	// Role kind + implication edges against the real handlers (the
	// wireshape rows pin the bytes, and this pins that strazad accepts them): an
	// explicit kind survives the round trip, add creates the edge the list
	// reports, remove clears it.
	lead, err := client.CreateRole(ctx, "finance-lead", "runs the desk", "business", "", nil)
	if err != nil || lead.Kind != "business" {
		t.Fatalf("CreateRole with kind: %+v, %v", lead, err)
	}
	if err := client.AddRoleImplication(ctx, "finance-lead", "finance"); err != nil {
		t.Fatalf("AddRoleImplication: %v", err)
	}
	imps, err := client.RoleImplications(ctx, "finance-lead")
	if err != nil || len(imps) != 1 || imps[0].ImpliesName != "finance" {
		t.Fatalf("RoleImplications after add = %+v, %v", imps, err)
	}
	if err := client.RemoveRoleImplication(ctx, "finance-lead", "finance"); err != nil {
		t.Fatalf("RemoveRoleImplication: %v", err)
	}
	imps, err = client.RoleImplications(ctx, "finance-lead")
	if err != nil || len(imps) != 0 {
		t.Fatalf("RoleImplications after remove = %+v, %v", imps, err)
	}
	// DeleteRole answers the server's status, and no policy set names
	// finance-lead, so none is turned off.
	if gone, err := client.DeleteRole(ctx, "finance-lead"); err != nil || gone.Status != "deleted" || len(gone.SetsOff) != 0 {
		t.Fatalf("DeleteRole = %+v, %v; want status deleted and no set turned off", gone, err)
	}

	// Packs through the CLI client.
	if _, err := client.CreatePack(ctx, "fin-rules", "1", "no wire transfers"); err != nil {
		t.Fatalf("CreatePack: %v", err)
	}
	if err := client.BindPack(ctx, "fin-rules", "finance"); err != nil {
		t.Fatalf("BindPack: %v", err)
	}

	// Policy: apply → activate through the CLI client.
	const pol = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: cli-block-rm }
spec:
  rules:
    - id: no-rm
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "blocked"
`
	if _, err := client.ApplyPolicy(ctx, []byte(pol)); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	act, err := client.ActivatePolicy(ctx, "cli-block-rm", "active")
	if err != nil || act.Snapshot == "" || claim(act.Changed) != "true" {
		t.Fatalf("ActivatePolicy: %q changed %s, %v", act.Snapshot, claim(act.Changed), err)
	}
	snapID := act.Snapshot
	// A repeated activate answers the running snapshot and says nothing
	// changed.
	again, err := client.ActivatePolicy(ctx, "cli-block-rm", "active")
	if err != nil || again.Snapshot != snapID || claim(again.Changed) != "false" {
		t.Fatalf("repeated ActivatePolicy: %q changed %s, %v; want %q changed false", again.Snapshot, claim(again.Changed), err, snapID)
	}
	// Two policies: the standalone starter set (seeded at boot) + ours.
	pols, err := client.Policies(ctx)
	if err != nil || len(pols) != 2 {
		t.Fatalf("Policies: %v, %v", pols, err)
	}
	byName := map[string]string{}
	for _, p := range pols {
		byName[p.Name] = p.Status
	}
	if byName["cli-block-rm"] != "active" || byName["standalone-starter"] != "active" {
		t.Fatalf("policy statuses = %v, want both active", byName)
	}

	// Deactivate lane (strazactl policy deactivate): status back to draft,
	// recompile returns a fresh snapshot id.
	deact, err := client.ActivatePolicy(ctx, "cli-block-rm", "draft")
	if err != nil || deact.Snapshot == "" || claim(deact.Changed) != "true" {
		t.Fatalf("ActivatePolicy(draft): %q changed %s, %v", deact.Snapshot, claim(deact.Changed), err)
	}
	if deact.Snapshot == snapID {
		t.Fatalf("deactivate did not recompile: snapshot stayed %s", snapID)
	}
	pols, err = client.Policies(ctx)
	if err != nil {
		t.Fatalf("Policies after deactivate: %v", err)
	}
	for _, p := range pols {
		if p.Name == "cli-block-rm" && p.Status != "draft" {
			t.Fatalf("cli-block-rm status = %s, want draft", p.Status)
		}
	}
}

func containsRole(roles []Role, name string) bool {
	for _, r := range roles {
		if r.Name == name {
			return true
		}
	}
	return false
}

func containsUser(users []User, name string) bool {
	for _, u := range users {
		if u.Username == name {
			return true
		}
	}
	return false
}

// claim spells the changed field of an activate answer: true, false, or
// absent when the server sent none.
func claim(changed *bool) string {
	if changed == nil {
		return "absent"
	}
	return strconv.FormatBool(*changed)
}
