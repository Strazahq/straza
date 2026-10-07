package server

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// freshPostgresDSN creates an isolated database for one test so the shared
// CI service database (used by the store package's own cross-driver suite,
// possibly concurrently) is never touched.
func freshPostgresDSN(t *testing.T) string {
	t.Helper()
	base := os.Getenv("STRAZA_TEST_POSTGRES_DSN")
	if base == "" {
		t.Skip("STRAZA_TEST_POSTGRES_DSN not set")
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	name := fmt.Sprintf("straza_parity_%d_%d", time.Now().UnixNano(), rand.Intn(1e6)) // #nosec G404 -- test db name, not crypto
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE " + name + " WITH (FORCE)")
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// TestPostgresServerParity pins parity above the store layer: a full
// strazad (bootstrap, device-flow login, checkin, policy snapshot, app
// manager, gateway PEP, audit chain) runs the same flows against Postgres
// that the rest of the suite runs against sqlite.
func TestPostgresServerParity(t *testing.T) {
	t.Parallel()
	dsn := freshPostgresDSN(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()

	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Store = config.Store{Driver: config.DriverPostgres, DSN: dsn}
		cfg.Apps.HealthInterval = time.Hour
	})

	// Bootstrap admin exists (the bootstrap runs on an empty store regardless
	// of driver).
	if _, err := app.store.Users().GetByUsername(ctx, "admin"); err != nil {
		t.Fatalf("bootstrap admin missing on postgres: %v", err)
	}

	// Identity + policy + app + binding: the GitOps demo core, on postgres.
	seedGatewayUser(t, app, "kim", "dev")
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: echoapp}
server: {name: straza.test/echo, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
`, up.URL))
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	devRole, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: row.ID, ToolMatcher: `["echo"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "allow-echo", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: allow-echo}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-echo
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := app.manager.View("echoapp"); ok && v.Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("echoapp not running on postgres")
		}
		time.Sleep(20 * time.Millisecond)
	}

	tok := sessionToken(t, base, "kim")
	_, list, _ := mcpCall(t, base, tok, "tools/list", nil)
	if names := toolNamesOf(t, list); len(names) != 1 || names[0] != "echoapp__echo" {
		t.Fatalf("catalog on postgres = %v", names)
	}
	if code, res, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
		"name": "echoapp__echo", "arguments": map[string]string{"text": "pg"},
	}); code != http.StatusOK || !strings.Contains(string(raw), "echo: pg") {
		t.Fatalf("call on postgres = %d %v", code, res)
	}

	// The async audit chain builds on postgres too (outbox → JetStream →
	// hash-chained audit_log).
	auditDeadline := time.Now().Add(10 * time.Second)
	for {
		recs, err := app.store.Audit().List(ctx, 0, 1000)
		if err == nil {
			for _, r := range recs {
				if strings.Contains(r.CE, "straza.audit.mcp") {
					return
				}
			}
		}
		if time.Now().After(auditDeadline) {
			t.Fatal("no straza.audit.mcp record in the postgres audit chain")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
