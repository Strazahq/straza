package scim

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestUserEntraAgentID walks entraAgentId through the SCIM lane: it is
// stored lower case and rendered in the Straza extension, a PUT without it
// keeps it, a PATCH sets and removes it on the bare and the URN path, and
// a person, a value that is not a GUID, an id another agent holds and a
// type change of a holder are refused with a sentence that names the fix.
func TestUserEntraAgentID(t *testing.T) {
	const (
		id      = "dc013442-2b7a-4c1e-9f1a-0123456789ab"
		other   = "5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b"
		core    = `"urn:ietf:params:scim:schemas:core:2.0:User"`
		agentic = `"urn:ietf:params:scim:schemas:extension:agent:2.0:Agent"`
		ext     = `urn:straza:params:scim:schemas:extension:2.0:User`
	)
	st := newPlanesTestStore(t)
	mux, writes := userWrittenServer(t, st)
	ctx := context.Background()
	agentDoc := func(name, extBlock string) string {
		return `{"schemas":[` + core + `,` + agentic + `],"userName":"` + name + `"` + extBlock + `}`
	}
	idOf := func(name string) string {
		u, err := st.Users().GetByUsername(ctx, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return u.ID
	}
	stored := func(name string) string {
		u, err := st.Users().GetByUsername(ctx, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return u.EntraAgentID
	}
	patch := func(ops string) string {
		return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[` + ops + `]}`
	}
	type step struct {
		name, method, path, body string
		code                     int
		detail                   []string
	}
	run := func(s step) *http.Response {
		t.Helper()
		rec := scimReq(t, mux, s.method, s.path, s.body)
		if rec.Code != s.code {
			t.Fatalf("%s: %s %s = %d %s, want %d", s.name, s.method, s.path, rec.Code, rec.Body, s.code)
		}
		for _, want := range s.detail {
			if !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("%s: answer %s does not say %q", s.name, rec.Body, want)
			}
		}
		return rec.Result()
	}

	run(step{"create sam with an upper-case id", http.MethodPost, "/scim/v2/Users",
		agentDoc("sam-sre-agent", `,"`+ext+`":{"entraAgentId":"`+strings.ToUpper(id)+`"}`), http.StatusCreated, nil})
	if got := stored("sam-sre-agent"); got != id {
		t.Fatalf("stored id = %q, want the lower-case %q", got, id)
	}
	sam := idOf("sam-sre-agent")
	rec := scimReq(t, mux, http.MethodGet, "/scim/v2/Users/"+sam, "")
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if block, _ := res[ext].(map[string]any); block["entraAgentId"] != id {
		t.Fatalf("rendered extension = %v, want entraAgentId %s", res[ext], id)
	}

	run(step{"create joe without an id", http.MethodPost, "/scim/v2/Users", agentDoc("joe-java-developer", ""), http.StatusCreated, nil})
	joe := idOf("joe-java-developer")

	run(step{"a PUT that leaves the attribute out keeps it", http.MethodPut, "/scim/v2/Users/" + sam,
		agentDoc("sam-sre-agent", ""), http.StatusOK, nil})
	if got := stored("sam-sre-agent"); got != id {
		t.Fatalf("after PUT, stored id = %q, want %q", got, id)
	}

	for _, s := range []step{
		{"joe takes sam's id", http.MethodPatch, "/scim/v2/Users/" + joe,
			patch(`{"op":"replace","path":"` + ext + `:entraAgentId","value":"` + id + `"}`), http.StatusConflict,
			[]string{`"uniqueness"`, "already belongs to sam-sre-agent", "PATCH remove of entraAgentId"}},
		{"a new agent takes sam's id", http.MethodPost, "/scim/v2/Users",
			agentDoc("pam-personal-agent", `,"`+ext+`":{"entraAgentId":"`+id+`"}`), http.StatusConflict,
			[]string{`"uniqueness"`, "already belongs to sam-sre-agent"}},
		{"a person takes an id", http.MethodPost, "/scim/v2/Users",
			`{"schemas":[` + core + `],"userName":"carol","userType":"human","` + ext + `":{"entraAgentId":"` + other + `"}}`,
			http.StatusBadRequest, []string{`"invalidValue"`, "only on a user of type agent", "carol is a person"}},
		{"a value that is not a GUID", http.MethodPatch, "/scim/v2/Users/" + joe,
			patch(`{"op":"replace","path":"entraAgentId","value":"sam-sre-agent"}`), http.StatusBadRequest,
			[]string{`"invalidValue"`, "is not an Entra agent identity id"}},
		{"a holder turns person", http.MethodPatch, "/scim/v2/Users/" + sam,
			patch(`{"op":"replace","path":"userType","value":"human"}`), http.StatusBadRequest,
			[]string{`"invalidValue"`, "sam-sre-agent holds entraAgentId " + id, "then change userType"}},
	} {
		run(s)
	}
	if _, err := st.Users().GetByUsername(ctx, "pam-personal-agent"); err == nil {
		t.Fatal("a refused create stored the user")
	}
	if _, err := st.Users().GetByUsername(ctx, "carol"); err == nil {
		t.Fatal("a refused create stored the person")
	}

	*writes = nil
	run(step{"sam's id is removed on the bare path", http.MethodPatch, "/scim/v2/Users/" + sam,
		patch(`{"op":"remove","path":"entraAgentId"}`), http.StatusOK, nil})
	if got := stored("sam-sre-agent"); got != "" {
		t.Fatalf("after remove, stored id = %q", got)
	}
	if len(*writes) != 1 || !slices.Contains((*writes)[0].changed, "entra_agent_id") {
		t.Fatalf("UserWritten after remove = %+v, want entra_agent_id changed", *writes)
	}

	run(step{"joe takes the freed id", http.MethodPatch, "/scim/v2/Users/" + joe,
		patch(`{"op":"replace","path":"entraAgentId","value":"` + id + `"}`), http.StatusOK, nil})
	run(step{"the whole extension is removed", http.MethodPatch, "/scim/v2/Users/" + joe,
		patch(`{"op":"remove","path":"` + ext + `"}`), http.StatusOK, nil})
	if got := stored("joe-java-developer"); got != "" {
		t.Fatalf("after the whole-extension remove, stored id = %q", got)
	}

	// A deactivated agent that comes back through a create with its own id
	// is revived, not refused as a holder of that id.
	run(step{"joe takes the id again", http.MethodPatch, "/scim/v2/Users/" + joe,
		patch(`{"op":"replace","path":"entraAgentId","value":"` + id + `"}`), http.StatusOK, nil})
	run(step{"joe is deactivated", http.MethodDelete, "/scim/v2/Users/" + joe, "", http.StatusNoContent, nil})
	run(step{"joe comes back with its id", http.MethodPost, "/scim/v2/Users",
		agentDoc("joe-java-developer", `,"`+ext+`":{"entraAgentId":"`+id+`"}`), http.StatusCreated, nil})
	if u, err := st.Users().GetByUsername(ctx, "joe-java-developer"); err != nil || u.ID != joe || u.EntraAgentID != id ||
		u.Status != store.UserActive {
		t.Fatalf("revived joe = %+v, %v", u, err)
	}
}
