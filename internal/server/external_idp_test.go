package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestExternalIdPLogin wires two strazad instances together: one acts as the
// external OIDC IdP, the other is the platform configured against it. A
// user unknown to the platform logs in via the IdP and is JIT
// provisioned at checkin.
func TestExternalIdPLogin(t *testing.T) {
	t.Parallel()
	// Instance 1: the "IdP", whose built-in issuer is a discoverable OIDC
	// provider.
	idpApp, idpBase := testApp(t)
	seedIdentity(t, idpApp) // creates user kim at the IdP

	// Instance 2: the platform, pointed at the IdP. JIT on (standalone
	// default); ClientID matches the device-flow client.
	platform, platformBase := testApp(t, func(cfg *config.Config) {
		cfg.OIDC.Issuer = idpBase
		cfg.OIDC.ClientID = "straza"
		cfg.OIDC.JITProvision = true
	})

	// kim exists at the IdP only.
	if _, err := platform.store.Users().GetByUsername(context.Background(), "kim"); err == nil {
		t.Fatal("kim must not pre-exist on the platform")
	}

	idToken := loginDeviceFlow(t, idpBase, "kim", "hunter2!")
	code, checkin := postJSON(t, platformBase+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusOK {
		t.Fatalf("external checkin = %d %v", code, checkin)
	}

	// JIT provisioned with the IdP subject linked.
	u, err := platform.store.Users().GetByUsername(context.Background(), "kim")
	if err != nil {
		t.Fatalf("JIT user missing: %v", err)
	}
	if u.ExternalID == "" || u.Email != "kim@x.io" {
		t.Errorf("provisioned user: %+v", u)
	}

	// The platform's own token service accepts the minted session token.
	claims, err := platform.tokens.Verify(checkin["session_token"].(string))
	if err != nil || claims.Subject != u.ID {
		t.Fatalf("session token: %+v, %v", claims, err)
	}

	// With JIT off, an unknown IdP identity is refused.
	strictPlatform, strictBase := testApp(t, func(cfg *config.Config) {
		cfg.OIDC.Issuer = idpBase
		cfg.OIDC.ClientID = "straza"
		cfg.OIDC.JITProvision = false
	})
	code, body := postJSON(t, strictBase+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("strict platform = %d %v", code, body)
	}
	if _, err := strictPlatform.store.Users().GetByUsername(context.Background(), "kim"); err == nil {
		t.Fatal("strict platform must not provision users")
	}
}

// TestEnterpriseBootstrapAdmin pins the enterprise first-admin story: a
// fresh enterprise store (nothing seeded, JIT off) reaches a working admin
// through one config line (`oidc.bootstrapAdmin`), and the grant is
// one-shot: once ANY straza-admin assignment exists, the knob is inert and
// a revoked bootstrap admin is never resurrected by logging in again.
func TestEnterpriseBootstrapAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idpApp, idpBase := testApp(t)
	seedIdentity(t, idpApp) // kim exists at the IdP only

	platform, platformBase := testApp(t, func(cfg *config.Config) {
		cfg.Profile = config.ProfileEnterprise
		cfg.OIDC.Issuer = idpBase
		cfg.OIDC.ClientID = "straza"
		cfg.OIDC.JITProvision = false
		cfg.OIDC.BootstrapAdmin = "kim"
	})

	// The enterprise discovery doc points logins at the external IdP.
	doc, status := getIdPDoc(t, platformBase)
	if status != http.StatusOK || doc["issuer"] != idpBase || doc["client_id"] != "straza" {
		t.Fatalf("idp.json = %d %v", status, doc)
	}

	checkinAs := func(idToken string) (int, map[string]any) {
		return postJSON(t, platformBase+"/v1/checkin", map[string]any{
			"id_token":    idToken,
			"harness":     map[string]string{"name": "strazactl", "version": "dev"},
			"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
		})
	}

	// First login: kim is provisioned despite JIT off and granted admin.
	code, checkin := checkinAs(loginDeviceFlow(t, idpBase, "kim", "hunter2!"))
	if code != http.StatusOK {
		t.Fatalf("bootstrap checkin = %d %v", code, checkin)
	}
	kim, err := platform.store.Users().GetByUsername(ctx, "kim")
	if err != nil {
		t.Fatalf("bootstrap user missing: %v", err)
	}
	adminAssignments := func() []string {
		role, err := platform.store.Roles().GetByName(ctx, AdminRole)
		if err != nil {
			t.Fatalf("admin role: %v", err)
		}
		all, err := platform.store.Roles().ListAllAssignments(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, a := range all {
			if a.RoleID == role.ID {
				ids = append(ids, a.ID+"="+a.SubjectID)
			}
		}
		return ids
	}
	// kim's bootstrap grant plus the standing break-glass assignment
	// (which deliberately does not make the knob inert).
	if got := adminAssignments(); len(got) != 2 {
		t.Fatalf("admin assignments after bootstrap = %v, want kim's + break-glass", got)
	}

	// The minted session token really is an admin session: it reaches a
	// working admin without touching the DB.
	req, _ := http.NewRequest(http.MethodGet, platformBase+"/v1/admin/users", nil)
	req.Header.Set("Authorization", "Bearer "+checkin["session_token"].(string))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin API with bootstrap session = %d", resp.StatusCode)
	}

	// Re-login: no duplicate assignment.
	if code, _ := checkinAs(loginDeviceFlow(t, idpBase, "kim", "hunter2!")); code != http.StatusOK {
		t.Fatal("re-login failed")
	}
	if got := adminAssignments(); len(got) != 2 {
		t.Fatalf("re-login duplicated the grant: %v", got)
	}

	// One-shot: hand admin to someone else, revoke kim's, and logging in again
	// must NOT resurrect it (the knob is inert once role management exists).
	role, _ := platform.store.Roles().GetByName(ctx, AdminRole)
	other, err := platform.store.Users().Create(ctx, store.User{Username: "real-admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platform.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: other.ID, RoleID: role.ID,
	}); err != nil {
		t.Fatal(err)
	}
	for _, a := range mustAssignments(t, platform) {
		if a.RoleID == role.ID && a.SubjectID == kim.ID {
			if err := platform.store.Roles().Unassign(ctx, a.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if code, _ := checkinAs(loginDeviceFlow(t, idpBase, "kim", "hunter2!")); code != http.StatusOK {
		t.Fatal("post-revocation login failed")
	}
	for _, a := range mustAssignments(t, platform) {
		if a.RoleID == role.ID && a.SubjectID == kim.ID {
			t.Fatal("revoked bootstrap admin was resurrected by re-login")
		}
	}
}

func mustAssignments(t *testing.T, app *App) []store.RoleAssignment {
	t.Helper()
	all, err := app.store.Roles().ListAllAssignments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// TestExternalBreakGlassRefused pins that an account named break-glass at the
// identity provider never becomes the platform's emergency admin. The session
// exchange and the admin plane answer 403 with the sentence, each refusal is
// one login failure record and one warning, the platform's break-glass row
// gains no external id, and nobody is provisioned.
func TestExternalBreakGlassRefused(t *testing.T) {
	t.Parallel()
	idpApp, idpBase := testApp(t)
	setPasswordDirect(t, idpApp, BreakGlassUsername, "idp-side-secret")
	const want = "the account break-glass at your identity provider cannot sign in to Straza, because break-glass is the local emergency admin and signs in with its own password only. Rename or remove that account at the identity provider."
	const reason = "external identity names the break-glass account"
	for _, jit := range []bool{false, true} {
		t.Run(fmt.Sprintf("provisioning %v", jit), func(t *testing.T) {
			ctx := context.Background()
			logger, logs := captureLogger()
			platform, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = logger }}, func(cfg *config.Config) {
				cfg.OIDC.Issuer, cfg.OIDC.ClientID, cfg.OIDC.JITProvision = idpBase, "straza", jit
			})
			before, _ := platform.store.Users().List(ctx)
			idToken := loginDeviceFlow(t, idpBase, BreakGlassUsername, "idp-side-secret")

			code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
				"id_token":    idToken,
				"harness":     map[string]string{"name": "console", "version": "1"},
				"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
			})
			if code != http.StatusForbidden || checkin["error"] != want || checkin["session_token"] != nil {
				t.Errorf("session exchange = %d %v, want 403 %q and no session", code, checkin, want)
			}
			var answer any
			code = adminReq(t, "GET", base+"/v1/admin/apps", idToken, nil, &answer)
			if refused, _ := answer.(map[string]any); code != http.StatusForbidden || refused["error"] != want {
				t.Errorf("admin plane = %d %v, want 403 %q", code, answer, want)
			}

			bg, err := platform.store.Users().GetByUsername(ctx, BreakGlassUsername)
			if err != nil || bg.ExternalID != "" {
				t.Errorf("the platform's break-glass row = %+v, %v, want it with no external id", bg, err)
			}
			if after, _ := platform.store.Users().List(ctx); len(after) != len(before) {
				t.Errorf("users = %d, want the %d from before the attempts", len(after), len(before))
			}
			records := func() int {
				return countAuthn(t, platform, func(d map[string]any) bool { return d["reason"] == reason })
			}
			record := waitAuthn(t, platform, "the refused break-glass login", func(d map[string]any) bool { return d["reason"] == reason })
			if record["outcome"] != "failure" || record["via"] != "id-token" || record["user"] != BreakGlassUsername || record["userId"] != nil {
				t.Errorf("record = %v, want a failure via id-token that names break-glass and no user id", record)
			}
			deadline := time.Now().Add(10 * time.Second)
			for records() < 2 && time.Now().Before(deadline) {
				time.Sleep(30 * time.Millisecond)
			}
			time.Sleep(200 * time.Millisecond)
			if n := records(); n != 2 {
				t.Errorf("login failure records = %d, want one per refused call, 2", n)
			}
			if n := strings.Count(logs.String(), "BREAK-GLASS: an external identity"); n != 2 {
				t.Errorf("warnings = %d, want one per refused call, 2", n)
			}
		})
	}
}

// TestBreakGlassExternalLinkWarnsAtBoot pins the boot check for a deployment
// where an external identity was linked to the break-glass row before the
// verifier refused it: the row keeps the link as evidence and boot warns with
// the next steps, and a row without a link warns nothing.
func TestBreakGlassExternalLinkWarnsAtBoot(t *testing.T) {
	t.Parallel()
	logger, logs := captureLogger()
	testAppPreRun(t, []func(*App){func(a *App) {
		ctx := context.Background()
		a.log = logger
		if err := a.ensureBreakGlass(ctx); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs.String(), "BREAK-GLASS") {
			t.Errorf("a row without a link warned: %s", logs.String())
		}
		bg, err := a.store.Users().GetByUsername(ctx, BreakGlassUsername)
		if err != nil {
			t.Fatal(err)
		}
		bg.ExternalID = "subject-from-before"
		if _, err := a.store.Users().Update(ctx, bg); err != nil {
			t.Fatal(err)
		}
		if err := a.ensureBreakGlass(ctx); err != nil {
			t.Fatal(err)
		}
		out := logs.String()
		if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "externalId=subject-from-before") || !strings.Contains(out, "strazactl users set-password break-glass") {
			t.Errorf("boot warning = %q, want a WARN with the external id and the password command", out)
		}
		if after, _ := a.store.Users().GetByUsername(ctx, BreakGlassUsername); after.ExternalID != "subject-from-before" {
			t.Errorf("the link was changed to %q, want it kept as evidence", after.ExternalID)
		}
	}})
}
