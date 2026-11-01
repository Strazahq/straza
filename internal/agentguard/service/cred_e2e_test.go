package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/server"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// realBlockRM is the policy the real strazad activates: dev may not delete
// recursively.
const realBlockRM = `apiVersion: straza.dev/v1beta1
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

// realStrazad starts a real standalone strazad with kim, who holds the dev
// and admin roles and a knowledge pack bound to dev, and the block-rm policy
// active. It returns the base URL.
func realStrazad(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	base := "http://" + addr
	cfg := config.Config{
		Profile: config.ProfileStandalone, DataDir: dir,
		Server: config.Server{Listen: addr, PublicURL: base},
		Log:    config.Log{Level: "error", Format: "json"},
		Store:  config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events: config.Events{Embedded: true},
		Governance: config.Governance{OfflineGraceTTL: 15 * time.Minute, LocalToolDefault: config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop},
	}
	storetest.SeedSQLite(t, cfg.SQLitePath())
	ctx, cancel := context.WithCancel(context.Background())
	app, err := server.New(ctx, cfg, logging.New(cfg.Log, &bytes.Buffer{}))
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
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	bg := context.Background()
	hash, _ := storetest.PasswordHash("hunter2!")
	kim, err := st.Users().Create(bg, store.User{Username: "kim", Email: "kim@x.io", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := st.Roles().Create(bg, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	admin, _ := st.Roles().GetByName(bg, server.AdminRole)
	for _, rid := range []string{dev.ID, admin.ID} {
		if _, err := st.Roles().Assign(bg, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: kim.ID, RoleID: rid}); err != nil {
			t.Fatal(err)
		}
	}
	pack, _ := st.Packs().Create(bg, store.KnowledgePack{Name: "golang-style", Version: "1", Content: "always run gofmt", Checksum: "c1"})
	_ = st.Packs().Bind(bg, dev.ID, pack.ID)
	token := realLogin(t, base)
	realAdmin(t, base, token, http.MethodPut, "/v1/admin/policies", "application/yaml", realBlockRM, http.StatusCreated)
	realAdmin(t, base, token, http.MethodPost, "/v1/admin/policies/block-rm/activate", "application/json", `{"status":"active"}`, http.StatusOK)
	return base
}

// realLogin signs kim in to strazactl's client by device code and returns
// the ID token.
func realLogin(t *testing.T, base string) string {
	t.Helper()
	resp, err := http.PostForm(base+"/oidc/device_authorization", url.Values{"client_id": {"strazactl"}})
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&auth)
	_ = resp.Body.Close()
	realApprove(t, base, auth["user_code"].(string))
	resp, err = http.PostForm(base+"/oidc/token", url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {auth["device_code"].(string)}, "client_id": {"strazactl"}})
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	_ = resp.Body.Close()
	return tok["id_token"].(string)
}

// realApprove approves code as kim at the built-in issuer, as her browser
// would.
func realApprove(t *testing.T, base, code string) {
	t.Helper()
	resp, err := http.PostForm(base+"/oidc/device", url.Values{"user_code": {code}, "username": {"kim"}, "password": {"hunter2!"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

// realAdmin sends one admin request as kim and checks its status.
func realAdmin(t *testing.T, base, token, method, path, contentType, body string, want int) []byte {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, buf.String(), want)
	}
	return buf.Bytes()
}

// TestCredAgainstRealStrazad runs the credential half against a real
// strazad: a person enrolls by device code and approves after the first
// poll was still pending, three windows start at once on the key lane,
// the snapshot and /v1/decide answer through DPoP, the push stream comes
// up, audit records reach the audit chain, a session revoke stops one
// window until it starts again, every live window decides after a service
// restart with no check-in, and no file in the state directory but the
// saved sessions holds a token, where each is bound to the key beside it.
func TestCredAgainstRealStrazad(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real strazad")
	}
	base := realStrazad(t)
	r := newCredRig(t)
	r.s.now = time.Now
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	codes := make(chan string, 1)
	type outcome struct {
		res localapi.EnrollResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.s.Enroll(ctx, alice, localapi.EnrollRequest{ServerURL: base}, func(ev localapi.EnrollEvent) {
			if ev.Event == localapi.EnrollCode {
				codes <- ev.UserCode
			}
		})
		done <- outcome{res, err}
	}()
	code := <-codes
	time.Sleep(2500 * time.Millisecond) // the person is still signing in, so the first poll answers authorization_pending
	select {
	case o := <-done:
		t.Fatalf("enroll gave up while the person was still signing in: %v", o.err)
	default:
	}
	realApprove(t, base, code)
	o := <-done
	if o.err != nil || o.res.Username != "kim" || o.res.Custody != localapi.CustodyAccount || o.res.DeviceID == "" {
		t.Fatalf("enroll: %+v, %v", o.res, o.err)
	}

	sessions := startMany(t, r, alice, "w", 3)
	for _, ses := range sessions {
		if !slices.Contains(ses.Roles, "dev") || ses.SnapshotID == "" || len(ses.Packs) != 1 || ses.Packs[0].Name != "golang-style" {
			t.Fatalf("session %+v lacks its role, pin or pack", ses)
		}
	}
	ses := sessions[0]
	eng, _, _, err := r.s.Policy(ctx, ses)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	rm := policy.Event{Kind: policy.EventToolPre, Tool: "shell.exec", Command: "rm -rf /"}
	if d := eng.Evaluate(rm, ses.Subject()); d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, "destructive delete blocked") {
		t.Fatalf("local decision on rm -rf: %+v", d)
	}
	if d := eng.Evaluate(policy.Event{Kind: policy.EventToolPre, Tool: "shell.exec", Command: "ls -la"}, ses.Subject()); d.Effect != policy.EffectAllow {
		t.Fatalf("local decision on ls: %+v", d)
	}
	out, err := r.s.Decide(ctx, ses, rm, "", "")
	if err != nil || out.Answer.Effect != policy.EffectDeny || out.Status != http.StatusOK || out.Correlation == "" || out.Bounced {
		t.Fatalf("server decide: %+v, %v", out, err)
	}
	waitFor(t, "three push streams", func() bool {
		st := r.s.Status(alice)
		return len(st.Sessions) == 3 && !slices.ContainsFunc(st.Sessions, func(s localapi.SessionStatus) bool { return s.Push != localapi.PushConnected })
	})

	for _, cmd := range []string{"rm -rf /", "ls -la"} {
		if err := r.s.Record(alice, "straza.audit.tool", map[string]any{"session": ses.ID, "command": cmd, "effect": "deny"}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the spool to drain", func() bool { return r.s.Status(alice).Spool.Pending == 0 })
	admin := realLogin(t, base)
	waitFor(t, "the records in the audit chain", func() bool {
		var recs []struct {
			CE string `json:"ce"`
		}
		_ = json.Unmarshal(realAdmin(t, base, admin, http.MethodGet, "/v1/admin/audit?limit=200", "", "", http.StatusOK), &recs)
		n := 0
		for _, rec := range recs {
			if strings.Contains(rec.CE, `"source":"straza"`) && strings.Contains(rec.CE, ses.ID) {
				n++
			}
		}
		return n == 2
	})

	realAdmin(t, base, admin, http.MethodPost, "/v1/admin/sessions/"+ses.ID+"/revoke", "application/json", `{}`, http.StatusOK)
	waitFor(t, "the revocation to reach the window", func() bool {
		_, err := r.s.Session(ctx, alice, ses.Harness, false)
		var refused *Refused
		return errors.As(err, &refused) && refused.Status == http.StatusForbidden
	})
	for _, other := range sessions[1:] {
		if _, err := r.s.Session(ctx, alice, other.Harness, false); err != nil {
			t.Fatalf("a revoke of one window stopped %s: %v", other.Harness.Session, err)
		}
	}
	again, err := r.start(alice, ses.Harness.Session)
	if err != nil || again.ID == ses.ID {
		t.Fatalf("start after the revoke: %s, %v", again.ID, err)
	}

	r.stop()
	r.openAt(time.Now)
	for _, w := range []Session{again, sessions[1], sessions[2]} {
		got, err := r.s.Session(ctx, alice, w.Harness, false)
		if err != nil || got.ID != w.ID {
			t.Fatalf("window %s after a restart: %+v, %v", w.Harness.Session, got, err)
		}
		out, err := r.s.Decide(ctx, got, rm, "", "")
		if err != nil || out.Answer.Effect != policy.EffectDeny || out.Status != http.StatusOK || out.Bounced {
			t.Fatalf("server decide of %s after a restart: %+v, %v", w.Harness.Session, out, err)
		}
	}
	if sent := r.sent.carrying(`"counter":`); len(sent) != 0 {
		t.Fatalf("the restarted service checked in: %q", sent)
	}
	waitFor(t, "three resumed push streams", func() bool {
		st := r.s.Status(alice)
		return !slices.ContainsFunc(st.Sessions, func(s localapi.SessionStatus) bool { return s.Revoked == "" && s.Push != localapi.PushConnected })
	})

	err = filepath.WalkDir(r.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "lock" || d.Name() == sessionsFile {
			return err
		}
		raw, err := os.ReadFile(path)
		if err == nil && bytes.Contains(raw, []byte("eyJ")) {
			t.Errorf("%s holds a token", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSavedTokensBound(t, r.savedPath(alice), o.res.KeyThumbprint)
}

// assertSavedTokensBound checks that every token in the sessions file at
// path is bound to the key whose thumbprint is jkt, so a copy of the file
// works on no other machine.
func assertSavedTokensBound(t *testing.T, path, jkt string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	var saved savedSessions
	if err != nil || json.Unmarshal(raw, &saved) != nil || len(saved.Sessions) == 0 {
		t.Fatalf("the saved sessions in %s: %v", path, err)
	}
	for _, v := range saved.Sessions {
		if v.Token == "" {
			continue
		}
		parts := strings.Split(v.Token, ".")
		var claims struct {
			CNF struct {
				JKT string `json:"jkt"`
			} `json:"cnf"`
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[min(1, len(parts)-1)])
		if err != nil || json.Unmarshal(payload, &claims) != nil || claims.CNF.JKT != jkt {
			t.Errorf("the saved token of %s is not bound to this machine's key %s: %+v, %v", v.Harness.Session, jkt, claims, err)
		}
	}
}
