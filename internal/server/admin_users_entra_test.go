package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"reflect"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestAdminUserEntraOwnedRefused pins the admin API answers for a person
// Microsoft Entra owns. A PATCH that changes the status, the display name
// or the email and a DELETE each answer 409 with the one sentence, leave
// the row as it was and chain no user.update or user.delete record. A PATCH
// of another field, a PATCH that sends the stored values, a lock and an
// unlock go through. So do the same writes of a person of origin scim or
// local, and of a person of origin entra once people are out of
// identity.entra.read. The rows run in order on one server.
func TestAdminUserEntraOwnedRefused(t *testing.T) {
	t.Parallel()
	const refusal = "dan was not changed here, because the account comes from Microsoft Entra ID and Straza would write Entra's state back at its next read. " +
		"Change the account in Entra. To stop dan at once, lock the account."
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	bearer, _ := checkinToken(t, app, base)
	ctx := context.Background()
	seed := func(name, origin string) store.User {
		t.Helper()
		u, err := app.store.Users().Create(ctx, store.User{Username: name, Email: name + "@x.io", Display: name,
			Status: store.UserActive, Origin: origin, UserType: store.UserTypeHuman})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	dan, sue, loc := seed("dan", store.OriginEntra), seed("sue", store.OriginSCIM), seed("loc", store.OriginLocal)
	// The section is set once the server answers, so the Entra sync never
	// starts and only the refusals read it.
	app.cfg.Identity.Entra = testEntraOn(config.EntraReadPeople)

	for _, tc := range []struct {
		name   string
		method string
		user   store.User
		path   string
		body   map[string]any
		code   int
		// peopleOut takes people out of identity.entra.read for the row.
		peopleOut bool
	}{
		{"a status change", http.MethodPatch, dan, "", map[string]any{"status": "disabled"}, http.StatusConflict, false},
		{"a display name change", http.MethodPatch, dan, "", map[string]any{"display": "Dan L"}, http.StatusConflict, false},
		{"an email change", http.MethodPatch, dan, "", map[string]any{"email": "dan@y.io"}, http.StatusConflict, false},
		{"a status change beside a title", http.MethodPatch, dan, "", map[string]any{"title": "Engineer", "status": "disabled"}, http.StatusConflict, false},
		{"a delete", http.MethodDelete, dan, "", nil, http.StatusConflict, false},
		{"the stored values sent again", http.MethodPatch, dan, "", map[string]any{"status": "active", "display": "dan", "email": "dan@x.io"}, http.StatusOK, false},
		{"a title change", http.MethodPatch, dan, "", map[string]any{"title": "Engineer"}, http.StatusOK, false},
		{"a lock", http.MethodPost, dan, "/lock", map[string]any{"reason": "the laptop was stolen"}, http.StatusOK, false},
		{"an unlock", http.MethodPost, dan, "/unlock", nil, http.StatusOK, false},
		{"a status change of a person of origin scim", http.MethodPatch, sue, "", map[string]any{"status": "disabled"}, http.StatusOK, false},
		{"a display name change of a person of origin local", http.MethodPatch, loc, "", map[string]any{"display": "Lo C"}, http.StatusOK, false},
		{"a delete of a person of origin local", http.MethodDelete, loc, "", nil, http.StatusOK, false},
		{"a status change once people are out", http.MethodPatch, dan, "", map[string]any{"status": "disabled"}, http.StatusOK, true},
		{"a delete once people are out", http.MethodDelete, dan, "", nil, http.StatusOK, true},
	} {
		read := []string{config.EntraReadPeople}
		if tc.peopleOut {
			read = nil
		}
		app.cfg.Identity.Entra.Read = read
		before, err := app.store.Users().GetByID(ctx, tc.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		records := userWriteRecords(t, app)
		var body any
		if tc.body != nil {
			body = tc.body
		}
		var resp map[string]any
		code := adminReq(t, tc.method, base+"/v1/admin/users/"+tc.user.ID+tc.path, bearer, body, &resp)
		if code != tc.code {
			t.Fatalf("%s: status %d, want %d: %v", tc.name, code, tc.code, resp)
		}
		if code != http.StatusConflict {
			continue
		}
		if resp["error"] != refusal {
			t.Errorf("%s: error %q, want %q", tc.name, resp["error"], refusal)
		}
		after, err := app.store.Users().GetByID(ctx, tc.user.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Errorf("%s: stored row\n got %+v (%v)\nwant %+v", tc.name, after, err, before)
		}
		if n := userWriteRecords(t, app); n != records {
			t.Errorf("%s: %d user.update or user.delete records, want %d", tc.name, n, records)
		}
	}
}

// TestAdminUserEntraOwnedAgentRefused pins the admin API answers for an
// agent Microsoft Entra owns. A PATCH that changes the status, the display
// name, the email, the sponsor or the Entra agent id and a DELETE each
// answer 409 with the one sentence, leave the row as it was and chain no
// user.update or user.delete record. A PATCH of another field, a PATCH that
// sends the stored values, a lock, an unlock and the key and device routes
// go through. So do the sponsor change of a person of origin entra, the
// writes of an agent of origin local that an admin gave an Entra agent id,
// and the writes of the agent of origin entra once the agent kinds are out
// of identity.entra.read. The rows run in order on one server.
func TestAdminUserEntraOwnedAgentRefused(t *testing.T) {
	t.Parallel()
	const refusal = "eva was not changed here, because the agent comes from Microsoft Entra ID and Straza would write Entra's state back at its next read. " +
		"Change the agent, or its sponsor, in Entra. To stop eva at once, lock the account."
	const (
		evaID   = "5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b"
		otherID = "dc013442-2b7a-4c1e-9f1a-0123456789ab"
		locID   = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
		movedID = "7e6d5c4b-3a29-4817-b6c5-d4e3f2a1b0c9"
	)
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	bearer, _ := checkinToken(t, app, base)
	ctx := context.Background()
	seed := func(u store.User) store.User {
		t.Helper()
		u.Email, u.Display, u.Status = u.Username+"@x.io", u.Username, store.UserActive
		created, err := app.store.Users().Create(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	eva := seed(store.User{Username: "eva", Origin: store.OriginEntra, UserType: store.UserTypeAgent, Attrs: `{"kind":"nhi"}`,
		Sponsor: "dan", EntraAgentID: evaID})
	loc := seed(store.User{Username: "loc", Origin: store.OriginLocal, UserType: store.UserTypeAgent, Attrs: `{"kind":"nhi"}`, Sponsor: "dan"})
	dan := seed(store.User{Username: "dan", Origin: store.OriginEntra, UserType: store.UserTypeHuman})
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// The section is set once the server answers, so the Entra sync never
	// starts and only the refusals read it.
	app.cfg.Identity.Entra = testEntraOn(config.EntraReadPeople, config.EntraReadAgentUsers)

	for _, tc := range []struct {
		name   string
		method string
		user   store.User
		path   string
		body   map[string]any
		code   int
		// agentsOut takes the agent kinds out of identity.entra.read for the
		// row.
		agentsOut bool
	}{
		{"a status change", http.MethodPatch, eva, "", map[string]any{"status": "disabled"}, http.StatusConflict, false},
		{"a display name change", http.MethodPatch, eva, "", map[string]any{"display": "Eva L"}, http.StatusConflict, false},
		{"an email change", http.MethodPatch, eva, "", map[string]any{"email": "eva@y.io"}, http.StatusConflict, false},
		{"a sponsor change", http.MethodPatch, eva, "", map[string]any{"sponsor": "kim"}, http.StatusConflict, false},
		{"an Entra agent id change", http.MethodPatch, eva, "", map[string]any{"entra_agent_id": otherID}, http.StatusConflict, false},
		{"an Entra agent id clear", http.MethodPatch, eva, "", map[string]any{"entra_agent_id": ""}, http.StatusConflict, false},
		{"a sponsor change beside a title", http.MethodPatch, eva, "", map[string]any{"title": "SRE bot", "sponsor": "kim"}, http.StatusConflict, false},
		{"a delete", http.MethodDelete, eva, "", nil, http.StatusConflict, false},
		{"the stored values sent again", http.MethodPatch, eva, "", map[string]any{"status": "active", "display": "eva", "email": "eva@x.io",
			"sponsor": "dan", "entra_agent_id": evaID}, http.StatusOK, false},
		{"a title change", http.MethodPatch, eva, "", map[string]any{"title": "SRE bot"}, http.StatusOK, false},
		{"an agency mode change", http.MethodPatch, eva, "", map[string]any{"agency_mode": "autonomous"}, http.StatusOK, false},
		{"a swarm change", http.MethodPatch, eva, "", map[string]any{"swarm_id": "s-1"}, http.StatusOK, false},
		{"a lock", http.MethodPost, eva, "/lock", map[string]any{"reason": "the agent misbehaved"}, http.StatusOK, false},
		{"an unlock", http.MethodPost, eva, "/unlock", nil, http.StatusOK, false},
		{"a key registration", http.MethodPut, eva, "/nhi-key", map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub)}, http.StatusOK, false},
		{"a key read", http.MethodGet, eva, "/nhi-key", nil, http.StatusOK, false},
		{"a device list", http.MethodGet, eva, "/devices", nil, http.StatusOK, false},
		{"a sponsor change of a person of origin entra", http.MethodPatch, dan, "", map[string]any{"sponsor": "kim"}, http.StatusOK, false},
		{"an admin gives a local agent an Entra agent id", http.MethodPatch, loc, "", map[string]any{"entra_agent_id": locID}, http.StatusOK, false},
		{"a status change of that local agent", http.MethodPatch, loc, "", map[string]any{"status": "disabled"}, http.StatusOK, false},
		{"a sponsor change of that local agent", http.MethodPatch, loc, "", map[string]any{"sponsor": "kim"}, http.StatusOK, false},
		{"an Entra agent id change of that local agent", http.MethodPatch, loc, "", map[string]any{"entra_agent_id": movedID}, http.StatusOK, false},
		{"a delete of that local agent", http.MethodDelete, loc, "", nil, http.StatusOK, false},
		{"a status change once agents are out", http.MethodPatch, eva, "", map[string]any{"status": "disabled"}, http.StatusOK, true},
		{"a sponsor change once agents are out", http.MethodPatch, eva, "", map[string]any{"sponsor": "kim"}, http.StatusOK, true},
		{"a delete once agents are out", http.MethodDelete, eva, "", nil, http.StatusOK, true},
	} {
		read := []string{config.EntraReadPeople, config.EntraReadAgentUsers}
		if tc.agentsOut {
			read = []string{config.EntraReadPeople}
		}
		app.cfg.Identity.Entra.Read = read
		before, err := app.store.Users().GetByID(ctx, tc.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		records := userWriteRecords(t, app)
		var body any
		if tc.body != nil {
			body = tc.body
		}
		// The device list answers an array, so the answer is read as any.
		var resp any
		code := adminReq(t, tc.method, base+"/v1/admin/users/"+tc.user.ID+tc.path, bearer, body, &resp)
		if code != tc.code {
			t.Fatalf("%s: status %d, want %d: %v", tc.name, code, tc.code, resp)
		}
		if code != http.StatusConflict {
			continue
		}
		if got, _ := resp.(map[string]any); got["error"] != refusal {
			t.Errorf("%s: answer %v, want the error %q", tc.name, resp, refusal)
		}
		after, err := app.store.Users().GetByID(ctx, tc.user.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Errorf("%s: stored row\n got %+v (%v)\nwant %+v", tc.name, after, err, before)
		}
		if n := userWriteRecords(t, app); n != records {
			t.Errorf("%s: %d user.update or user.delete records, want %d", tc.name, n, records)
		}
	}
}

// userWriteRecords counts the user.update and user.delete records chained
// so far.
func userWriteRecords(t *testing.T, app *App) int {
	t.Helper()
	n := 0
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == actionUserUpdate || ev["action"] == actionUserDelete {
			n++
		}
	}
	return n
}
