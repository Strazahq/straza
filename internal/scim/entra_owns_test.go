package scim

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestEntraOwnsPeopleRefusesWrites pins the SCIM writes refused while
// Microsoft Entra owns people. A create, a replace, a patch and a delete of
// a person, a create that would revive a deactivated person, and a replace
// or a patch that would turn an agent into a person each answer 403 with
// no scimType and the one detail, report one refusal that names the user,
// and leave the store as it was. The same writes of an agent and the create
// of a service succeed, a read of the person answers 200, and a group write
// that adds the person as a member succeeds. The rows run in order on one
// store.
func TestEntraOwnsPeopleRefusesWrites(t *testing.T) {
	const detail = "Straza did not accept this person, because it takes people from Microsoft Entra ID: identity.entra.read lists people. " +
		"Stop provisioning people to Straza in your identity manager, or take people out of identity.entra.read."
	const (
		core     = `"urn:ietf:params:scim:schemas:core:2.0:User"`
		agentURN = `"` + AgentSchemaPrefix + `:2.0:Agent"`
		patch    = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[`
	)
	ctx := context.Background()
	st := newPlanesTestStore(t)
	var refused []Refusal
	s := New(Deps{
		Store:           st,
		Authenticate:    func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
		Deactivate:      deactivateIn(st),
		Reactivate:      func(context.Context, string) {},
		IdentityChanged: func(context.Context, string, string) {},
		WriteRefused:    func(_ *http.Request, ref Refusal) { refused = append(refused, ref) },
		EntraOwns:       func(kind string) bool { return kind == store.UserKindPerson },
	})
	mux := http.NewServeMux()
	s.Routes(mux)

	seed := func(u store.User) store.User {
		t.Helper()
		created, err := st.Users().Create(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	ada := seed(store.User{Username: "ada", ExternalID: "oid-ada", Email: "ada@x.io", Display: "Ada",
		Status: store.UserActive, Origin: store.OriginEntra, UserType: store.UserTypeHuman})
	gone := seed(store.User{Username: "gone", Status: store.UserDisabled, Origin: store.OriginSCIM})
	// An agent by its type alone, so a write of userType human would make it
	// a person.
	typed := seed(store.User{Username: "typed-agent", Status: store.UserActive, Origin: store.OriginSCIM, UserType: store.UserTypeAgent})
	bot := seed(store.User{Username: "bot", Status: store.UserActive, Origin: store.OriginSCIM,
		UserType: store.UserTypeAgent, Attrs: `{"kind":"nhi"}`})
	role, err := st.Roles().Create(ctx, store.Role{Name: "deployers"})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		code   int
		// user is the username a refusal names, and the stored row a refused
		// write must leave as it was, or must leave absent for a create.
		user string
	}{
		{"a create of a person", http.MethodPost, "/scim/v2/Users", `{"schemas":[` + core + `],"userName":"newbie"}`, http.StatusForbidden, "newbie"},
		{"a create that would revive a deactivated person", http.MethodPost, "/scim/v2/Users", `{"schemas":[` + core + `],"userName":"gone"}`, http.StatusForbidden, "gone"},
		{"a create of an agent over a deactivated person", http.MethodPost, "/scim/v2/Users", `{"schemas":[` + core + `,"` + AgentSchemaPrefix + `:2.0:Agent"],"userName":"gone"}`, http.StatusConflict, "gone"},
		{"a replace of a person", http.MethodPut, "/scim/v2/Users/" + ada.ID, `{"schemas":[` + core + `],"userName":"ada","displayName":"Ada L"}`, http.StatusForbidden, "ada"},
		{"a patch of a person", http.MethodPatch, "/scim/v2/Users/" + ada.ID, patch + `{"op":"replace","path":"displayName","value":"Ada L"}]}`, http.StatusForbidden, "ada"},
		{"a delete of a person", http.MethodDelete, "/scim/v2/Users/" + ada.ID, "", http.StatusForbidden, "ada"},
		{"a replace that makes an agent a person", http.MethodPut, "/scim/v2/Users/" + typed.ID, `{"schemas":[` + core + `],"userName":"typed-agent","userType":"human"}`, http.StatusForbidden, "typed-agent"},
		{"a patch that makes an agent a person", http.MethodPatch, "/scim/v2/Users/" + typed.ID, patch + `{"op":"replace","path":"userType","value":"human"}]}`, http.StatusForbidden, "typed-agent"},
		{"a create of an agent", http.MethodPost, "/scim/v2/Users", `{"schemas":[` + core + `,` + agentURN + `],"userName":"bot-new"}`, http.StatusCreated, ""},
		{"a create of a service", http.MethodPost, "/scim/v2/Users", `{"schemas":[` + core + `],"userName":"svc","userType":"service"}`, http.StatusCreated, ""},
		{"a replace of an agent", http.MethodPut, "/scim/v2/Users/" + bot.ID, `{"schemas":[` + core + `],"userName":"bot","displayName":"Bot two"}`, http.StatusOK, ""},
		{"a patch of an agent", http.MethodPatch, "/scim/v2/Users/" + bot.ID, patch + `{"op":"replace","path":"displayName","value":"Bot three"}]}`, http.StatusOK, ""},
		{"a delete of an agent", http.MethodDelete, "/scim/v2/Users/" + bot.ID, "", http.StatusNoContent, ""},
		{"a read of a person", http.MethodGet, "/scim/v2/Users/" + ada.ID, "", http.StatusOK, ""},
		{"a group write that adds a person", http.MethodPatch, "/scim/v2/Groups/" + role.ID, patch + `{"op":"add","path":"members","value":[{"value":"` + ada.ID + `"}]}]}`, http.StatusOK, ""},
	} {
		before, beforeErr := st.Users().GetByUsername(ctx, tc.user)
		refused = nil
		rec := scimReq(t, mux, tc.method, tc.path, tc.body)
		if rec.Code != tc.code {
			t.Fatalf("%s: status %d, want %d: %s", tc.name, rec.Code, tc.code, rec.Body)
		}
		if tc.code == http.StatusConflict {
			// The create is an agent's, so it is not refused for its own
			// kind. It must not revive the person who has the name.
			after, _ := st.Users().GetByUsername(ctx, tc.user)
			if !reflect.DeepEqual(before, after) || len(refused) != 1 || refused[0].Status != http.StatusConflict {
				t.Errorf("%s: stored row %+v, refusals %+v, want the row as it was and one 409", tc.name, after, refused)
			}
			continue
		}
		if tc.code != http.StatusForbidden {
			if len(refused) != 0 {
				t.Errorf("%s: reported refusals %+v, want none", tc.name, refused)
			}
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: body %s: %v", tc.name, rec.Body, err)
		}
		if body["detail"] != detail {
			t.Errorf("%s: detail %q, want %q", tc.name, body["detail"], detail)
		}
		if _, ok := body["scimType"]; ok {
			t.Errorf("%s: scimType %v, want none", tc.name, body["scimType"])
		}
		if len(refused) != 1 || refused[0].Status != http.StatusForbidden || refused[0].Detail != detail || refused[0].Username != tc.user {
			t.Errorf("%s: reported refusals %+v, want one 403 that names %s", tc.name, refused, tc.user)
		}
		after, afterErr := st.Users().GetByUsername(ctx, tc.user)
		if (beforeErr == nil) != (afterErr == nil) || !reflect.DeepEqual(before, after) {
			t.Errorf("%s: stored row\n got %+v (%v)\nwant %+v (%v)", tc.name, after, afterErr, before, beforeErr)
		}
	}
	if u, err := st.Users().GetByID(ctx, gone.ID); err != nil || u.Status != store.UserDisabled {
		t.Errorf("gone after the refused revive = %+v, %v, want disabled", u, err)
	}
	holders, err := st.Roles().AssignmentsByRole(ctx, role.ID)
	if err != nil || len(holders) != 1 || holders[0].SubjectID != ada.ID {
		t.Errorf("holders of %s = %+v, %v, want ada", role.Name, holders, err)
	}
}

// TestEntraOwnsAgentsRefusesWrites pins the SCIM writes refused while
// Microsoft Entra owns agents. A create of an agent with an entraAgentId, a
// replace, a patch and a delete of an agent that holds one or that came
// from Entra, and a patch that would give a plain agent one each answer 403
// with no scimType and the one detail, report one refusal that names the
// user, and leave the store as it was. A create over a deactivated agent
// that holds one answers 409 and does not revive it. The writes of a plain
// agent and of a person succeed. While Entra owns no agent, every row
// succeeds. The rows run in order on one store for each case.
func TestEntraOwnsAgentsRefusesWrites(t *testing.T) {
	const detail = "Straza did not accept this agent, because it takes agents that are in Microsoft Entra ID from Entra: identity.entra.read lists agentUsers or agentIdentities. " +
		"Stop provisioning this agent to Straza in your identity manager, or take both kinds out of identity.entra.read."
	const (
		core     = `"urn:ietf:params:scim:schemas:core:2.0:User"`
		agentURN = `"` + AgentSchemaPrefix + `:2.0:Agent"`
		ext      = `urn:straza:params:scim:schemas:extension:2.0:User`
		patch    = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[`
		newID    = "dc013442-2b7a-4c1e-9f1a-0123456789ab"
		heldID   = "5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b"
		goneID   = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
		setID    = "7e6d5c4b-3a29-4817-b6c5-d4e3f2a1b0c9"
	)
	for _, owned := range []bool{true, false} {
		ctx := context.Background()
		st := newPlanesTestStore(t)
		var refused []Refusal
		s := New(Deps{
			Store:           st,
			Authenticate:    func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
			Deactivate:      deactivateIn(st),
			Reactivate:      func(context.Context, string) {},
			IdentityChanged: func(context.Context, string, string) {},
			WriteRefused:    func(_ *http.Request, ref Refusal) { refused = append(refused, ref) },
			EntraOwns:       func(kind string) bool { return owned && kind == store.UserKindAgent },
		})
		mux := http.NewServeMux()
		s.Routes(mux)
		ids := map[string]string{}
		for _, u := range []store.User{
			{Username: "sam", EntraAgentID: heldID},
			{Username: "eva", Origin: store.OriginEntra},
			{Username: "joe"},
			{Username: "tim"},
			{Username: "gone", EntraAgentID: goneID, Status: store.UserDisabled},
			{Username: "ada", UserType: store.UserTypeHuman},
		} {
			if u.UserType == "" {
				u.UserType, u.Attrs = store.UserTypeAgent, `{"kind":"nhi"}`
			}
			u.Origin = cmp.Or(u.Origin, store.OriginSCIM)
			u.Status = cmp.Or(u.Status, store.UserActive)
			created, err := st.Users().Create(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			ids[u.Username] = created.ID
		}
		agentDoc := func(name, extra string) string {
			return `{"schemas":[` + core + `,` + agentURN + `],"userName":"` + name + `"` + extra + `}`
		}

		for _, tc := range []struct {
			name   string
			method string
			path   string
			body   string
			// code is the answer while Entra owns agents, and free the answer
			// while it owns none.
			code, free int
			// user is the username a refusal names, and the stored row a refused
			// write must leave as it was, or must leave absent for a create.
			user string
		}{
			{"a create with an entraAgentId", http.MethodPost, "/scim/v2/Users",
				agentDoc("new-agent", `,"`+ext+`":{"entraAgentId":"`+newID+`"}`), http.StatusForbidden, http.StatusCreated, "new-agent"},
			{"a create without one", http.MethodPost, "/scim/v2/Users", agentDoc("plain-agent", ""), http.StatusCreated, http.StatusCreated, ""},
			{"a create over a deactivated agent that holds one", http.MethodPost, "/scim/v2/Users", agentDoc("gone", ""), http.StatusConflict, http.StatusCreated, "gone"},
			{"a replace of an agent that holds one", http.MethodPut, "/scim/v2/Users/" + ids["sam"],
				`{"schemas":[` + core + `],"userName":"sam","displayName":"Sam two"}`, http.StatusForbidden, http.StatusOK, "sam"},
			{"a patch of an agent that holds one", http.MethodPatch, "/scim/v2/Users/" + ids["sam"],
				patch + `{"op":"replace","path":"displayName","value":"Sam three"}]}`, http.StatusForbidden, http.StatusOK, "sam"},
			{"a patch of an agent from Entra", http.MethodPatch, "/scim/v2/Users/" + ids["eva"],
				patch + `{"op":"replace","path":"displayName","value":"Eva two"}]}`, http.StatusForbidden, http.StatusOK, "eva"},
			{"a patch that gives a plain agent one", http.MethodPatch, "/scim/v2/Users/" + ids["tim"],
				patch + `{"op":"replace","path":"` + ext + `:entraAgentId","value":"` + setID + `"}]}`, http.StatusForbidden, http.StatusOK, "tim"},
			{"a replace of a plain agent", http.MethodPut, "/scim/v2/Users/" + ids["joe"],
				`{"schemas":[` + core + `],"userName":"joe","displayName":"Joe two"}`, http.StatusOK, http.StatusOK, ""},
			{"a patch of a plain agent", http.MethodPatch, "/scim/v2/Users/" + ids["joe"],
				patch + `{"op":"replace","path":"displayName","value":"Joe three"}]}`, http.StatusOK, http.StatusOK, ""},
			{"a patch of a person", http.MethodPatch, "/scim/v2/Users/" + ids["ada"],
				patch + `{"op":"replace","path":"displayName","value":"Ada L"}]}`, http.StatusOK, http.StatusOK, ""},
			{"a delete of a plain agent", http.MethodDelete, "/scim/v2/Users/" + ids["joe"], "", http.StatusNoContent, http.StatusNoContent, ""},
			{"a delete of an agent that holds one", http.MethodDelete, "/scim/v2/Users/" + ids["sam"], "", http.StatusForbidden, http.StatusNoContent, "sam"},
			{"a delete of an agent from Entra", http.MethodDelete, "/scim/v2/Users/" + ids["eva"], "", http.StatusForbidden, http.StatusNoContent, "eva"},
		} {
			name, want := tc.name+" while Entra owns agents", tc.code
			if !owned {
				name, want = tc.name+" while Entra owns no agent", tc.free
			}
			before, beforeErr := st.Users().GetByUsername(ctx, tc.user)
			refused = nil
			rec := scimReq(t, mux, tc.method, tc.path, tc.body)
			if rec.Code != want {
				t.Fatalf("%s: status %d, want %d: %s", name, rec.Code, want, rec.Body)
			}
			after, afterErr := st.Users().GetByUsername(ctx, tc.user)
			unchanged := (beforeErr == nil) == (afterErr == nil) && reflect.DeepEqual(before, after)
			switch want {
			case http.StatusConflict:
				if !unchanged || after.Status != store.UserDisabled || len(refused) != 1 || refused[0].Status != http.StatusConflict {
					t.Errorf("%s: stored row %+v, refusals %+v, want the disabled row as it was and one 409", name, after, refused)
				}
			case http.StatusForbidden:
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("%s: body %s: %v", name, rec.Body, err)
				}
				if _, ok := body["scimType"]; ok || body["detail"] != detail {
					t.Errorf("%s: scimType %v and detail %q, want no scimType and %q", name, body["scimType"], body["detail"], detail)
				}
				if len(refused) != 1 || refused[0].Status != http.StatusForbidden || refused[0].Detail != detail || refused[0].Username != tc.user {
					t.Errorf("%s: reported refusals %+v, want one 403 that names %s", name, refused, tc.user)
				}
				if !unchanged {
					t.Errorf("%s: stored row\n got %+v (%v)\nwant %+v (%v)", name, after, afterErr, before, beforeErr)
				}
			default:
				if len(refused) != 0 {
					t.Errorf("%s: reported refusals %+v, want none", name, refused)
				}
			}
		}
	}
}
