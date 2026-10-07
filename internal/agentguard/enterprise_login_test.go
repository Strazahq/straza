package agentguard_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/ctl"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/server"
	"github.com/strazahq/straza/internal/store/storetest"
)

// bootEnterprise starts a strazad whose ONLY issuer is external: enterprise
// profile, empty store (the enterprise profile seeds nothing), pointed at
// idpBase.
func bootEnterprise(t *testing.T, idpBase string) string {
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
		Profile: config.ProfileEnterprise,
		DataDir: dir,
		Server:  config.Server{Listen: addr, PublicURL: base},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Governance: config.Governance{
			LocalToolDefault:  config.EffectDeny,
			AuditBackpressure: config.BackpressureDrop,
		},
		OIDC: config.OIDC{
			Issuer:         idpBase,
			ClientID:       "straza",
			JITProvision:   false,
			BootstrapAdmin: "kim",
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	// The template holds the migrated schema and no rows, so the store
	// is still empty of users and roles the way the enterprise boot expects.
	storetest.SeedSQLite(t, cfg.SQLitePath())
	app, err := server.New(ctx, cfg, logging.New(cfg.Log, &syncBuf{}))
	if err != nil {
		cancel()
		t.Fatalf("server.New (enterprise): %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("enterprise strazad shutdown timed out")
		}
	})
	return base
}

// approveAtIdP watches out for the printed user code and approves it at the
// IdP's login page as kim.
func approveAtIdP(t *testing.T, out *syncBuf, idpBase string) {
	t.Helper()
	codeRe := regexp.MustCompile(`confirm code ([A-Z2-9]{4}-[A-Z2-9]{4})`)
	var userCode string
	for i := 0; i < 200 && userCode == ""; i++ {
		if m := codeRe.FindStringSubmatch(out.String()); m != nil {
			userCode = m[1]
		} else {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if userCode == "" {
		t.Fatalf("no user code printed: %q", out.String())
	}
	resp, err := http.PostForm(idpBase+"/oidc/device", url.Values{
		"user_code": {userCode}, "username": {"kim"}, "password": {"hunter2!"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

// TestEnterpriseLoginViaExternalIdP pins the enterprise login path: `straza
// enroll` and `strazactl login` work against a strazad whose only issuer is
// external, and the fresh enterprise store reaches a working admin without
// anyone touching the DB: the whole flow a pure-enterprise deployment runs.
func TestEnterpriseLoginViaExternalIdP(t *testing.T) {
	idpBase, _ := bootStrazad(t) // kim exists at the IdP only
	platformBase := bootEnterprise(t, idpBase)

	// --- straza enroll: device flow at the IdP, enrollment at the platform.
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
	go func() { enrollErr <- agentguard.Enroll(ctx, agStore, platformBase, enrollOut) }()
	approveAtIdP(t, enrollOut, idpBase)
	if err := <-enrollErr; err != nil {
		t.Fatalf("enroll against enterprise: %v\noutput: %s", err, enrollOut.String())
	}
	if !strings.Contains(enrollOut.String(), "identity provider: "+idpBase) {
		t.Errorf("enroll never told the user it redirected to the IdP:\n%s", enrollOut.String())
	}
	id, err := agStore.LoadIdentity()
	if err != nil || id.Username != "kim" || id.DeviceToken == "" {
		t.Fatalf("enrolled identity: %+v, %v", id, err)
	}

	// --- the enterprise profile serves the policy snapshot to a checked-in
	// session only: the session start fetches it with the token it was just
	// given, and a request without one is refused with a sentence.
	if _, info, err := agentguard.SessionStart(ctx, agStore, "claude-code", "2.1.0"); err != nil || info.SnapshotID == "" {
		t.Fatalf("session start against enterprise: %+v, %v", info, err)
	}
	resp, err := http.Get(platformBase + "/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var refusal struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&refusal)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(refusal.Error, "only to a checked-in session") {
		t.Fatalf("tokenless snapshot fetch = %d %q, want 401 with the session sentence", resp.StatusCode, refusal.Error)
	}

	// --- strazactl login: same discovery, and the very first login is the
	// bootstrap admin, so admin API calls work immediately.
	client := ctl.NewClient(platformBase)
	client.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
	loginOut := &syncBuf{}
	loginErr := make(chan error, 1)
	go func() { loginErr <- client.Login(ctx, loginOut, false) }()
	approveAtIdP(t, loginOut, idpBase)
	if err := <-loginErr; err != nil {
		t.Fatalf("strazactl login against enterprise: %v\noutput: %s", err, loginOut.String())
	}
	users, err := client.Users(ctx)
	if err != nil {
		t.Fatalf("admin call with the bootstrap session: %v", err)
	}
	// break-glass exists on every store (unified role model); the IdP
	// lane must have provisioned exactly kim beside it.
	names := map[string]bool{}
	for _, u := range users {
		names[u.Username] = true
	}
	if len(users) != 2 || !names["kim"] || !names["break-glass"] {
		t.Fatalf("platform users = %+v, want the IdP-provisioned kim + break-glass", users)
	}
}
