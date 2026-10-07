package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestSCIMPlaneAcceptsAdminAPITokenByScope pins the one-token rule: the
// SCIM plane opens for an admin API token whose scope
// carries the scim area, the verb derives from the method exactly as on the
// admin plane, a token without the area is refused naming the missing
// grant, and an unknown wat_ is 401. The deactivation cascade attributes its
// records to the admin API token, so the fourth actor lane is pinned here.
func TestSCIMPlaneAcceptsAdminAPITokenByScope(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)

	mint := func(t *testing.T, name, scope string) (string, string) {
		t.Helper()
		var out struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		}
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
			map[string]any{"name": name, "scope": scope}, &out); code != http.StatusCreated {
			t.Fatalf("mint %q = %d", scope, code)
		}
		return out.Token, out.ID
	}
	detail := func(body map[string]any) string {
		s, _ := body["detail"].(string)
		return s
	}

	reader, _ := mint(t, "idm-aggregator", "scim:read")
	writer, writerID := mint(t, "idm-provisioner", "scim:write")
	elsewhere, _ := mint(t, "evidence-only", "apps:read")

	t.Run("scim:read reads and cannot write", func(t *testing.T) {
		if code, _ := scimReq(t, http.MethodGet, base, reader, "/scim/v2/Users", ""); code != http.StatusOK {
			t.Fatalf("GET Users with scim:read = %d, want 200", code)
		}
		if code, _ := scimReq(t, http.MethodGet, base, reader, "/scim/v2/ServiceProviderConfig", ""); code != http.StatusOK {
			t.Fatalf("GET ServiceProviderConfig with scim:read = %d, want 200", code)
		}
		code, body := scimReq(t, http.MethodPost, base, reader, "/scim/v2/Users", `{"userName":"nope@x.io","active":true}`)
		if code != http.StatusForbidden || !strings.Contains(detail(body), "scim:write") {
			t.Fatalf("POST Users with scim:read = %d %q, want 403 naming scim:write", code, detail(body))
		}
	})

	t.Run("a grant outside the plane is refused naming scim:read", func(t *testing.T) {
		code, body := scimReq(t, http.MethodGet, base, elsewhere, "/scim/v2/Users", "")
		if code != http.StatusForbidden || !strings.Contains(detail(body), "scim:read") {
			t.Fatalf("GET Users with apps:read = %d %q, want 403 naming scim:read", code, detail(body))
		}
	})

	t.Run("an unknown wat_ is 401 with the admin API token sentence", func(t *testing.T) {
		code, body := scimReq(t, http.MethodGet, base, "wat_notatoken", "/scim/v2/Users", "")
		if code != http.StatusUnauthorized || !strings.Contains(detail(body), "admin API token rejected") {
			t.Fatalf("GET Users with a bogus wat_ = %d %q", code, detail(body))
		}
	})

	t.Run("no token names the one mint", func(t *testing.T) {
		code, body := scimReq(t, http.MethodGet, base, "", "/scim/v2/Users", "")
		d := detail(body)
		if code != http.StatusUnauthorized || !strings.Contains(d, "api-token create --scope scim:read,scim:write") {
			t.Fatalf("GET Users with no token = %d %q, want 401 naming the mint command", code, d)
		}
	})

	t.Run("scim:write provisions and the cascade names the token", func(t *testing.T) {
		uid := scimCreateUser(t, base, writer, `{"userName":"leaver@x.io","externalId":"idm-42","active":true}`)
		if u, err := app.store.Users().GetByID(t.Context(), uid); err != nil || u.Origin != "scim" {
			t.Fatalf("user created over the SCIM plane by a wat_: origin %q err %v, want scim", u.Origin, err)
		}
		_, cred := grantUserApp(t, app, "github", uid)
		if code, _ := scimReq(t, http.MethodDelete, base, writer, "/scim/v2/Users/"+uid, ""); code != http.StatusNoContent {
			t.Fatalf("DELETE Users with scim:write = %d, want 204", code)
		}
		removes := grantRemoveEvents(t, app, uid)
		if len(removes) != 1 {
			t.Fatalf("apps.grant.remove records = %d, want exactly one", len(removes))
		}
		ev := removes[0]
		if ev["credentialId"] != cred.ID || ev["actor"] != "idm-provisioner" || ev["actorId"] != writerID || ev["actorVia"] != "api-token" {
			t.Errorf("cascade record actor = %v/%v/%v on %v, want idm-provisioner/%s/api-token", ev["actor"], ev["actorId"], ev["actorVia"], ev["credentialId"], writerID)
		}
	})

}

// TestSCIMPlaneRefusesRetiredTokenShape pins the removal of the SCIM token
// kind: a bearer with the retired wst_ prefix is an
// unknown credential, and the admin API's own mint route for it is gone.
func TestSCIMPlaneRefusesRetiredTokenShape(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)
	if code, body := scimReq(t, http.MethodGet, base, "wst_retired", "/scim/v2/Users", ""); code != http.StatusUnauthorized || !strings.Contains(body["detail"].(string), "api-token create") {
		t.Fatalf("wst_ bearer = %d %v, want 401 naming the admin API token mint", code, body)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/scim-tokens", adminTok, map[string]any{"name": "legacy"}, nil); code != http.StatusNotFound {
		t.Fatalf("POST /v1/admin/scim-tokens = %d, want 404 (route retired)", code)
	}
}
