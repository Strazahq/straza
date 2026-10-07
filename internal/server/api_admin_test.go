package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// adminReq performs an authenticated admin API call and decodes the JSON
// response into out (which may be nil).
func adminReq(t *testing.T, method, urlStr, bearer string, body any, out any) int {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, urlStr, reader)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s: %v", method, urlStr, err)
		}
	}
	return resp.StatusCode
}

// TestSessionJanitorAndUsernames pins the sessions list: a janitor pass closes
// active sessions whose tokens expired long ago (they can never refresh, so
// they are history, not principals), leaves live ones alone, and the admin
// list resolves owner usernames so operators aren't reading UUIDs.
func TestSessionJanitorAndUsernames(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, liveID := checkinToken(t, app, base)

	// A session whose token died an hour ago.
	stale, err := app.store.Sessions().Create(ctx, store.Session{
		UserID: user.ID, HarnessName: "claude-code", HarnessVersion: "2.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Sessions().Touch(ctx, stale.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	app.closeIdleSessions(ctx)

	var sessions []struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Status   string `json:"status"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/sessions", adminTok, nil, &sessions); code != http.StatusOK {
		t.Fatalf("sessions list = %d", code)
	}
	byID := map[string]struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Status   string `json:"status"`
	}{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	if got := byID[stale.ID]; got.Status != "closed" {
		t.Errorf("stale session status = %q, want closed", got.Status)
	}
	if got := byID[liveID]; got.Status != "active" {
		t.Errorf("live session status = %q, want active (janitor must not touch it)", got.Status)
	}
	if got := byID[liveID]; got.Username != "kim" {
		t.Errorf("session username = %q, want kim (operators read names, not UUIDs)", got.Username)
	}
	// A closed session's token can no longer act anywhere; its revoke path
	// still works for hygiene but the session never refreshes back to life.
	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"session_token": "expired-anyway", "harness": map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code == http.StatusOK {
		t.Errorf("garbage session refresh accepted: %v", body)
	}
}

// TestStandaloneStarterPolicy pins that a fresh standalone store boots with
// the starter PolicySet active (recursive force-delete denies with an
// actionable reason while ordinary commands stay allowed), and neither a
// restart nor an operator deletion resurrects or duplicates it.
func TestStandaloneStarterPolicy(t *testing.T) {
	t.Parallel()
	sharedDir := t.TempDir()
	shared := func(cfg *config.Config) {
		cfg.DataDir = sharedDir
		cfg.Store.DSN = filepath.Join(sharedDir, "straza.db")
	}

	t.Run("fresh boot seeds and enforces", func(t *testing.T) {
		app, base := testApp(t, shared)
		policies, err := app.store.Policies().List(context.Background())
		if err != nil || len(policies) != 1 || policies[0].Name != "standalone-starter" {
			t.Fatalf("policies after fresh boot = %v (err %v), want exactly standalone-starter", policies, err)
		}
		if policies[0].Status != "active" {
			t.Fatalf("starter policy status = %q, want active", policies[0].Status)
		}

		seedGatewayUser(t, app, "alice", "dev")
		tok := sessionToken(t, base, "alice")
		code, dec := decide(t, base, tok, map[string]any{
			"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x",
		})
		if code != http.StatusOK || dec["effect"] != "deny" || dec["ruleId"] != "block-recursive-delete" {
			t.Fatalf("rm -rf decision = %d %v, want deny by block-recursive-delete", code, dec)
		}
		if reason, _ := dec["reason"].(string); !strings.Contains(reason, "starter policy") {
			t.Errorf("deny reason not actionable: %v", dec)
		}
		// Ordinary work is untouched (standalone allow default).
		if _, dec := decide(t, base, tok, map[string]any{
			"kind": "tool.pre", "tool": "shell.exec", "command": "git status",
		}); dec["effect"] != "allow" {
			t.Errorf("git status decision = %v, want allow", dec)
		}
	})

	t.Run("restart does not duplicate", func(t *testing.T) {
		app, _ := testApp(t, shared)
		policies, err := app.store.Policies().List(context.Background())
		if err != nil || len(policies) != 1 {
			t.Fatalf("policies after restart = %d (err %v), want still exactly 1", len(policies), err)
		}
		// The operator turns it off and deletes it: that choice must stick
		// across boots.
		activateTexts(t, app, map[string]string{"standalone-starter": ""})
		if err := app.store.Policies().Delete(context.Background(), policies[0].ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("deletion sticks across restart", func(t *testing.T) {
		app, _ := testApp(t, shared)
		policies, err := app.store.Policies().List(context.Background())
		if err != nil || len(policies) != 0 {
			t.Fatalf("policies after delete+restart = %v (err %v), want none (no reseed on non-fresh store)", policies, err)
		}
	})
}

// grantRole assigns an existing role (by name) to the user and bumps the
// resolver so the grant is live. Bootstrap-created roles (straza-admin, the
// straza-enroll-* pair) exist from app start.
func grantRole(t *testing.T, app *App, userID, roleName string) {
	t.Helper()
	ctx := context.Background()
	role, err := app.store.Roles().GetByName(ctx, roleName)
	if err != nil {
		t.Fatalf("role %s missing: %v", roleName, err)
	}
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: userID, RoleID: role.ID,
	}); err != nil {
		t.Fatal(err)
	}
	app.resolver.Bump()
}

// grantAdmin makes the given user a straza-admin (the bootstrap role exists
// from app start).
func grantAdmin(t *testing.T, app *App, userID string) {
	t.Helper()
	grantRole(t, app, userID, AdminRole)
}

// TestAdminCRUDFlow pins the admin loop: create role → assign → list via the API,
// plus authz, packs, sessions, and the disable-user revocation path.
func TestAdminCRUDFlow(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	// Bootstrap created the admin user + role on first boot.
	if _, err := app.store.Users().GetByUsername(context.Background(), "admin"); err != nil {
		t.Fatalf("bootstrap admin missing: %v", err)
	}

	// Not an admin yet → 403; no token → 401.
	if code := adminReq(t, "GET", base+"/v1/admin/users", idToken, nil, nil); code != http.StatusForbidden {
		t.Fatalf("non-admin = %d, want 403", code)
	}
	if code := adminReq(t, "GET", base+"/v1/admin/users", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", code)
	}

	grantAdmin(t, app, user.ID)

	// Create role → assign → list.
	var role rolePayload
	if code := adminReq(t, "POST", base+"/v1/admin/roles", idToken, map[string]string{"name": "finance"}, &role); code != http.StatusCreated {
		t.Fatalf("create role = %d", code)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/roles", idToken, map[string]string{"name": "finance"}, nil); code != http.StatusConflict {
		t.Fatalf("duplicate role = %d, want 409", code)
	}
	var asg assignmentPayload
	code := adminReq(t, "POST", base+"/v1/admin/assignments", idToken,
		map[string]string{"subject_kind": "user", "subject_id": user.ID, "role_id": role.ID}, &asg)
	if code != http.StatusCreated || asg.ID == "" {
		t.Fatalf("assign = %d %+v", code, asg)
	}
	var listed []assignmentPayload
	code = adminReq(t, "GET", base+"/v1/admin/assignments?subject_kind=user&subject_id="+user.ID, idToken, nil, &listed)
	if code != http.StatusOK {
		t.Fatalf("list assignments = %d", code)
	}
	found := false
	for _, x := range listed {
		if x.RoleID == role.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("assignment not listed: %+v", listed)
	}

	// The API path bumps the resolver: checkin now carries finance.
	_, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token": idToken,
		"harness":  map[string]string{"name": "strazactl", "version": "dev"},
		"attestation": map[string]any{
			"managed": false, "hashes": map[string]string{},
		},
	})
	roles := checkin["roles"].([]any)
	hasFinance := false
	for _, r := range roles {
		if r == "finance" {
			hasFinance = true
		}
	}
	if !hasFinance {
		t.Fatalf("checkin roles missing finance: %v", roles)
	}

	// Implication cycle guard: finance ⇒ dev fine; dev ⇒ finance cycles.
	var devRole store.Role
	devRole, err := app.store.Roles().GetByName(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/roles/"+role.ID+"/implications", idToken,
		map[string]string{"implies_role_id": devRole.ID}, nil); code != http.StatusCreated {
		t.Fatalf("implication = %d", code)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/roles/"+devRole.ID+"/implications", idToken,
		map[string]string{"implies_role_id": role.ID}, nil); code != http.StatusConflict {
		t.Fatalf("cycle implication = %d, want 409", code)
	}

	// Packs: create + bind + delivered at checkin.
	var pack packAdminPayload
	if code := adminReq(t, "POST", base+"/v1/admin/packs", idToken,
		map[string]string{"name": "finance-rules", "version": "1", "content": "no wire transfers"}, &pack); code != http.StatusCreated {
		t.Fatalf("create pack = %d", code)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/packs/"+pack.ID+"/bindings", idToken,
		map[string]string{"role_id": role.ID}, nil); code != http.StatusCreated {
		t.Fatalf("bind pack = %d", code)
	}
	_, checkin = postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "strazactl", "version": "dev"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	packNames := []string{}
	for _, p := range checkin["knowledge_packs"].([]any) {
		packNames = append(packNames, p.(map[string]any)["name"].(string))
	}
	if !strings.Contains(strings.Join(packNames, ","), "finance-rules") {
		t.Fatalf("packs = %v, want finance-rules", packNames)
	}

	// Sessions list + revoke kills the session token.
	var sessions []sessionPayload
	if code := adminReq(t, "GET", base+"/v1/admin/sessions?status=active", idToken, nil, &sessions); code != http.StatusOK || len(sessions) == 0 {
		t.Fatalf("sessions = %d (%d entries)", code, len(sessions))
	}
	target := checkin["session_id"].(string)
	if code := adminReq(t, "POST", base+"/v1/admin/sessions/"+target+"/revoke", idToken, nil, nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	codeN, _ := postJSON(t, base+"/v1/checkin", map[string]any{
		"session_token": checkin["session_token"],
		"harness":       map[string]string{"name": "strazactl", "version": "dev"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if codeN != http.StatusUnauthorized {
		t.Fatalf("refresh after revoke = %d, want 401", codeN)
	}

	// Disabling a user via PATCH creates a revocation + revokes sessions.
	var updated userPayload
	if code := adminReq(t, "PATCH", base+"/v1/admin/users/"+user.ID, idToken,
		map[string]string{"status": "disabled"}, &updated); code != http.StatusOK || updated.Status != "disabled" {
		t.Fatalf("disable user = %d %+v", code, updated)
	}
	revs, err := app.store.Revocations().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foundRev := false
	for _, rv := range revs {
		if rv.Kind == store.RevokeUser && rv.TargetID == user.ID {
			foundRev = true
		}
	}
	if !foundRev {
		t.Fatalf("no user revocation recorded: %+v", revs)
	}
	// The disabled user's ID token no longer works on the admin API.
	var refusal map[string]string
	if code := adminReq(t, "GET", base+"/v1/admin/users", idToken, nil, &refusal); code != http.StatusForbidden ||
		refusal["error"] != "user is disabled. Contact your administrator" {
		t.Fatalf("disabled admin still accepted = %d %v", code, refusal)
	}
}

func TestSessionListTimestampsSane(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	_, _ = postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	var sessions []sessionPayload
	adminReq(t, "GET", base+"/v1/admin/sessions", idToken, nil, &sessions)
	if len(sessions) == 0 {
		t.Fatal("no sessions listed")
	}
	s := sessions[len(sessions)-1]
	if s.StartedAt.IsZero() || time.Since(s.StartedAt) > time.Minute {
		t.Errorf("started_at = %v", s.StartedAt)
	}
	if s.Harness != "claude-code/2.1.0" {
		t.Errorf("harness = %q", s.Harness)
	}
}
