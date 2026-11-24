package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// testEntraOn is an identity.entra section that is on and reads kinds.
func testEntraOn(kinds ...string) config.Entra {
	return config.Entra{
		Tenant: "d1d1d1d1-0000-4000-8000-000000000001", ClientID: "d2d2d2d2-0000-4000-8000-000000000002",
		Read: kinds, RoleGroups: true,
	}
}

// TestEntraOwnsReadsTheConfig pins which kinds of user Microsoft Entra
// owns while the identity.entra section is on: a person when its read list
// names people, an agent when it names agentUsers or agentIdentities, and
// never a service. While the section is off, Entra owns no kind.
func TestEntraOwnsReadsTheConfig(t *testing.T) {
	t.Parallel()
	all := []string{config.EntraReadPeople, config.EntraReadAgentUsers, config.EntraReadAgentIdentities}
	both := map[string]bool{store.UserKindPerson: true, store.UserKindAgent: true}
	agents := map[string]bool{store.UserKindAgent: true}
	for _, tc := range []struct {
		name  string
		entra config.Entra
		want  map[string]bool
	}{
		{"off", config.Entra{Read: []string{config.EntraReadPeople}}, map[string]bool{}},
		{"off with every kind", config.Entra{Read: all}, map[string]bool{}},
		{"on with no kind", testEntraOn(), map[string]bool{}},
		{"on with people", testEntraOn(config.EntraReadPeople), map[string]bool{store.UserKindPerson: true}},
		{"on with agent users", testEntraOn(config.EntraReadAgentUsers), agents},
		{"on with agent identities", testEntraOn(config.EntraReadAgentIdentities), agents},
		{"on with both agent kinds", testEntraOn(config.EntraReadAgentUsers, config.EntraReadAgentIdentities), agents},
		{"on with people and agent users", testEntraOn(config.EntraReadPeople, config.EntraReadAgentUsers), both},
		{"on with every kind", testEntraOn(all...), both},
	} {
		a := &App{cfg: config.Config{Identity: config.Identity{Entra: tc.entra}}}
		for _, kind := range []string{store.UserKindPerson, store.UserKindAgent, store.UserKindService} {
			if got := a.entraOwns(kind); got != tc.want[kind] {
				t.Errorf("%s: entraOwns(%s) = %v, want %v", tc.name, kind, got, tc.want[kind])
			}
		}
	}
}

// TestSCIMEntraOwnedCreateRecordsOnce pins that a SCIM create of a person
// refused while Microsoft Entra owns people, and of an agent with an
// entraAgentId refused while Entra owns agents, each answer 403 with the
// detail of its kind, chain exactly one scim.refused record that names the
// token, the route, the status, the reason the client was told and the
// userName, and store no user.
func TestSCIMEntraOwnedCreateRecordsOnce(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)
	var minted struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "idm-provisioner", "scope": "scim:read,scim:write"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint the SCIM admin API token = %d", code)
	}
	for _, tc := range []struct {
		name   string
		read   string
		user   string
		body   string
		detail string
	}{
		{"a person", config.EntraReadPeople, "newbie", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"newbie"}`,
			"Straza did not accept this person, because it takes people from Microsoft Entra ID: identity.entra.read lists people. " +
				"Stop provisioning people to Straza in your identity manager, or take people out of identity.entra.read."},
		{"an agent in Entra", config.EntraReadAgentUsers, "sam-sre-agent",
			`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User","urn:ietf:params:scim:schemas:extension:agent:2.0:Agent"],"userName":"sam-sre-agent",` +
				`"urn:straza:params:scim:schemas:extension:2.0:User":{"entraAgentId":"5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b"}}`,
			"Straza did not accept this agent, because it takes agents that are in Microsoft Entra ID from Entra: identity.entra.read lists agentUsers or agentIdentities. " +
				"Stop provisioning this agent to Straza in your identity manager, or take both kinds out of identity.entra.read."},
	} {
		// The section is set once the server answers, so the Entra sync never
		// starts and only the SCIM refusal reads it.
		app.cfg.Identity.Entra = testEntraOn(tc.read)
		admins := len(adminAuditEvents(t, app))
		code, resp := scimReq(t, http.MethodPost, base, minted.Token, "/scim/v2/Users", tc.body)
		if code != http.StatusForbidden || resp["detail"] != tc.detail {
			t.Fatalf("create of %s = %d %v, want 403 with the detail", tc.name, code, resp)
		}
		events := adminAuditEvents(t, app)[admins:]
		want := map[string]any{
			"action": "scim.refused", "actor": "idm-provisioner", "actorId": minted.ID, "actorVia": "api-token",
			"route": "POST /scim/v2/Users", "httpStatus": http.StatusForbidden, "reason": tc.detail, "username": tc.user,
		}
		gotJSON, _ := json.Marshal(events)
		wantJSON, _ := json.Marshal([]map[string]any{want})
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s: records\n got %s\nwant %s", tc.name, gotJSON, wantJSON)
		}
		if _, err := app.store.Users().GetByUsername(context.Background(), tc.user); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s after the refused create: %v, want not found", tc.user, err)
		}
	}
}
