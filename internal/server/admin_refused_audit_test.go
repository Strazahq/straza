package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestAdminRefusedChangeLogsAndRecordsOnce pins that every admin change the
// admin plane refuses with 403 after it authenticated the caller leaves
// exactly one WARN line, "admin change refused", and exactly one
// admin.refused record. The record names the caller in the actor fields,
// the route pattern, the status, the reason the caller was answered word for
// word, and the id the route names as target. That holds for a refusal in
// each admin wrapper, in a handler, and on a connect write an admin API
// token makes. A refused read, a request that is not authenticated, and a
// refusal that already writes its own record leave no admin.refused record
// and only the WARN lines they logged before.
func TestAdminRefusedChangeLogsAndRecordsOnce(t *testing.T) {
	t.Parallel()
	logger, logs := captureLogger()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = logger }}, func(cfg *config.Config) {
		cfg.Admin.RoleAreas = map[string][]string{"user-clerks": {"identity:read", "identity:write"}}
	})
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	session, _ := checkinToken(t, app, base)
	coding, _ := checkinTokenAs(t, base, "claude-code")
	auditorID, auditor := mintConnectToken(t, base, session, "auditor", "audit:read")
	_, provisioner := mintConnectToken(t, base, session, "idm-provisioner", "scim:read,scim:write")
	ciReadID, ciRead := mintConnectToken(t, base, session, "ci-read", "apps:read")
	if _, err := app.store.Roles().Create(ctx, store.Role{Name: "user-clerks"}); err != nil {
		t.Fatal(err)
	}
	dee := mkHuman(t, app, "dee", "user-clerks")
	deeTok := loginDeviceFlow(t, base, "dee", "hunter2!")
	up := startEchoUpstream(t)
	for _, manifest := range []string{echoManifest(up.URL), strings.Replace(echoManifest(up.URL), "name: echoapp", "name: otherapp", 1)} {
		if code := rawReq(t, http.MethodPost, base+"/v1/admin/apps", root, "application/yaml", []byte(manifest), nil); code != http.StatusCreated {
			t.Fatalf("install = %d", code)
		}
	}
	erin := mkHuman(t, app, "erin", "mcp-admin-echoapp")
	erinTok := loginDeviceFlow(t, base, "erin", "hunter2!")
	glass, err := app.store.Users().GetByUsername(ctx, BreakGlassUsername)
	if err != nil {
		t.Fatalf("the test server holds no break-glass account: %v", err)
	}
	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := app.store.Users().Create(ctx, store.User{Username: "svc", PasswordHash: hash, UserType: store.UserTypeService})
	if err != nil {
		t.Fatal(err)
	}
	grantAdmin(t, app, svc.ID)
	svcToken, err := app.tokens.MintIDToken(svc.ID, "straza", time.Minute, "svc", "")
	if err != nil {
		t.Fatal(err)
	}

	by := func(name, id, via string) func(route, target string) map[string]any {
		return func(route, target string) map[string]any {
			m := map[string]any{"action": "admin.refused", "route": route, "httpStatus": 403, "actor": name, "actorId": id, "actorVia": via}
			if target != "" {
				m["target"] = target
			}
			return m
		}
	}
	byKim := by("kim", kim.ID, "session")
	byAuditor, byDee, byErin := by("auditor", auditorID, "api-token"), by("dee", dee.ID, "login"), by("erin", erin.ID, "login")
	byCIRead := by("ci-read", ciReadID, "api-token")
	rows := []struct {
		name, bearer, method, path, body string
		code                             int
		// says is the error sentence the answer carries, or "" for an
		// answer this test does not read.
		says string
		// refused is the data of the one admin.refused record without its
		// reason, which must be says, or nil when the request writes none.
		refused map[string]any
		// others are the actions of the other admin and authn records the
		// request writes, in order.
		others []string
		// warns are the messages of every WARN line a request with no
		// admin.refused record logs, in order. A refused change logs one,
		// admin change refused.
		warns []string
	}{
		{
			name: "an admin API token whose scope lacks the area", bearer: auditor, method: http.MethodPost, path: "/v1/admin/users", body: `{}`,
			code: http.StatusForbidden, says: "token lacks scope identity:write", refused: byAuditor("POST /v1/admin/users", ""),
		},
		{
			name: "a delegated admin whose role lacks the area", bearer: deeTok, method: http.MethodPost, path: "/v1/admin/sessions/s-probe/revoke",
			code: http.StatusForbidden, says: "session lacks scope sessions:write", refused: byDee("POST /v1/admin/sessions/{id}/revoke", "s-probe"),
		},
		{
			name: "a delegated admin on a full-admin route", bearer: deeTok, method: http.MethodPost, path: "/v1/admin/signing-keys/client-assertion/rotate",
			code: http.StatusForbidden, says: fullAdminRefusal, refused: byDee("POST /v1/admin/signing-keys/client-assertion/rotate", ""),
		},
		{
			name: "a server admin on another server", bearer: erinTok, method: http.MethodPost, path: "/v1/admin/apps/otherapp/health",
			code: http.StatusForbidden, says: "this server's admin role is mcp-admin-otherapp, which you do not hold. Ask your identity manager for mcp-admin-otherapp, or a holder of straza-global-mcp-admin to make the change.",
			refused: byErin("POST /v1/admin/apps/{id}/health", "otherapp"),
		},
		{
			name: "a coding harness session", bearer: coding, method: http.MethodPost, path: "/v1/admin/users", body: `{}`,
			code: http.StatusForbidden, says: fmt.Sprintf(codingHarnessRefusal, "claude-code"), refused: byKim("POST /v1/admin/users", ""),
		},
		{
			name: "a coding harness session's refused read", bearer: coding, method: http.MethodGet, path: "/v1/admin/users",
			code: http.StatusForbidden, says: fmt.Sprintf(codingHarnessRefusal, "claude-code"), warns: []string{"coding-harness session refused on an admin route"},
		},
		{
			name: "a connect write for an admin API token without apps:write", bearer: ciRead, method: http.MethodPost, path: "/v1/connect/anyapp", body: `{"user":"kim","token":"x"}`,
			code: http.StatusForbidden, says: "this admin API token lacks the scope apps:write, which a change to a user's connection needs. A person mints a token that holds it in the console under Settings, API tokens.",
			refused: byCIRead("POST /v1/connect/{app}", ""),
		},
		{
			name: "a handler's break-glass delete refusal", bearer: session, method: http.MethodDelete, path: "/v1/admin/users/" + glass.ID,
			code: http.StatusForbidden, says: "the break-glass admin cannot be deleted (lockout guarantee); rotate its credential on-box instead",
			refused: byKim("DELETE /v1/admin/users/{id}", glass.ID),
		},
		{
			name: "a drafts route for a token without a drafts scope", bearer: auditor, method: http.MethodPost, path: "/v1/admin/drafts", body: `{}`,
			code: http.StatusForbidden, says: "This token holds no drafts scope, and writing or checking a draft needs drafts:write. Mint a token with the scopes drafts:read and drafts:write with strazactl api-token create.",
			refused: byAuditor("POST /v1/admin/drafts", ""),
		},
		{
			name: "the contact route for an admin API token", bearer: auditor, method: http.MethodPost, path: "/v1/admin/drafts/7/contact", body: `{"object":"App/probe"}`,
			code: http.StatusForbidden, says: contactAdminAPIRefusal, refused: byAuditor("POST /v1/admin/drafts/{id}/contact", "7"),
		},
		{
			name: "a refused read", bearer: auditor, method: http.MethodGet, path: "/v1/admin/users",
			code: http.StatusForbidden, says: "token lacks scope identity:read",
		},
		{
			name: "the enroll-token route, which records its own refusal", bearer: session, method: http.MethodPost, path: "/v1/admin/approvers/enroll-token", body: `{}`,
			code: http.StatusForbidden, others: []string{"enroll-token.refused"},
		},
		{
			name: "a SCIM write, which the SCIM plane records", bearer: provisioner, method: http.MethodPost, path: "/scim/v2/Groups", body: `{"displayName":"new-role"}`,
			code: http.StatusNotImplemented, others: []string{"scim.refused"}, warns: []string{"SCIM write refused"},
		},
		{
			name: "a caller who is not a person, which a login failure records", bearer: svcToken, method: http.MethodPost, path: "/v1/admin/users", body: `{}`,
			code: http.StatusForbidden, others: []string{"login"},
		},
		{
			name: "a caller who is not authenticated", method: http.MethodPost, path: "/v1/admin/users", body: `{}`,
			code: http.StatusUnauthorized,
		},
	}
	every := func(map[string]any) bool { return true }
	for _, row := range rows {
		since := outboxSince(t, app)
		logs.Reset()
		code, raw, _ := adminBytes(t, row.method, base+row.path, row.bearer, "application/json", []byte(row.body))
		var answer map[string]any
		_ = json.Unmarshal(raw, &answer)
		if code != row.code {
			t.Fatalf("%s: status %d, want %d (%s)", row.name, code, row.code, raw)
		}
		says, _ := answer["error"].(string)
		if row.says != "" && says != row.says {
			t.Errorf("%s: the answer says %q, want %q", row.name, says, row.says)
		}

		var refused []map[string]any
		var others []string
		for _, ev := range append(since("straza.audit.admin", every), since("straza.audit.authn", every)...) {
			if ev["action"] == actionAdminRefused {
				refused = append(refused, ev)
				continue
			}
			others = append(others, fmt.Sprint(ev["action"]))
		}
		if !slices.Equal(others, row.others) {
			t.Errorf("%s: other records %v, want %v", row.name, others, row.others)
		}
		var lines, warns []string
		for _, line := range strings.Split(logs.String(), "\n") {
			if m := warnMsg.FindStringSubmatch(line); m != nil {
				lines, warns = append(lines, line), append(warns, m[1]+m[2])
			}
		}
		wantWarns := row.warns
		if row.refused != nil {
			wantWarns = []string{"admin change refused"}
		}
		if !slices.Equal(warns, wantWarns) {
			t.Errorf("%s: WARN lines %q, want %q:\n%s", row.name, warns, wantWarns, logs.String())
		}
		if row.refused == nil {
			if len(refused) != 0 {
				t.Errorf("%s: chained %v, want no admin.refused record", row.name, refused)
			}
			continue
		}
		row.refused["reason"] = says
		if len(lines) == 1 {
			want := []string{"level=WARN", "status=403", `route="` + row.refused["route"].(string) + `"`,
				"actor=" + row.refused["actor"].(string), "reason="}
			if target, ok := row.refused["target"].(string); ok {
				want = append(want, "target="+target)
			}
			for _, w := range want {
				if !strings.Contains(lines[0], w) {
					t.Errorf("%s: refusal line lacks %s: %s", row.name, w, lines[0])
				}
			}
		}
		if len(refused) != 1 {
			t.Errorf("%s: chained %d admin.refused records, want exactly one: %v", row.name, len(refused), refused)
			continue
		}
		if got, want := mustJSON(refused[0]), mustJSON(row.refused); string(got) != string(want) {
			t.Errorf("%s: record\n got %s\nwant %s", row.name, got, want)
		}
	}
}

// warnMsg matches a WARN line of the capture and its message, quoted or
// bare.
var warnMsg = regexp.MustCompile(`level=WARN msg=(?:"([^"]*)"|(\S+))`)

// TestAdminRefusalPastTheBoundRecordsNothing pins that a refusal a handler
// starts after the write bound records nothing: the caller reads the bound's
// 503, which writes its own Error record, and no admin.refused record or
// refusal line is written. It shrinks the bound, so it does not run in
// parallel.
func TestAdminRefusalPastTheBoundRecordsNothing(t *testing.T) {
	old := adminWriteBound
	adminWriteBound = 300 * time.Millisecond
	t.Cleanup(func() { adminWriteBound = old })
	logger, logs := captureLogger()
	app, ls, bearer, _, _ := adminWithTarget(t, logger)
	glass, err := app.store.Users().GetByUsername(context.Background(), BreakGlassUsername)
	if err != nil {
		t.Fatalf("the test server holds no break-glass account: %v", err)
	}
	since := outboxSince(t, app)
	ls.arm(afterGet, func(ctx context.Context) error { _ = untilBound(ctx); return nil })
	logs.Reset()
	rec := serveOn(context.Background(), app, http.MethodDelete, "/v1/admin/users/"+glass.ID, bearer, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("the break-glass delete past the bound = %d %s, want 503", rec.Code, rec.Body.String())
	}
	for _, ev := range since("straza.audit.admin", func(map[string]any) bool { return true }) {
		if ev["action"] == actionAdminRefused {
			t.Errorf("a refusal past the bound chained %v, want no admin.refused record", ev)
		}
	}
	if strings.Contains(logs.String(), `msg="admin change refused"`) {
		t.Errorf("a refusal past the bound logged a refusal line:\n%s", logs.String())
	}
}
