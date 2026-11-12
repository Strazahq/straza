package scim

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

const (
	testCore       = `"urn:ietf:params:scim:schemas:core:2.0:User"`
	testAgentic    = `"urn:ietf:params:scim:schemas:extension:agent:2.0:Agent"`
	testEnterprise = `urn:ietf:params:scim:schemas:extension:enterprise:2.0:User`
)

// managerServer answers a SCIM surface over st that hides the account
// break-glass, with the writes it reports through Deps.UserWritten and the
// lines it logs.
func managerServer(t *testing.T, st store.Store) (*http.ServeMux, *[]userWrite, *bytes.Buffer) {
	t.Helper()
	var writes []userWrite
	logs := &bytes.Buffer{}
	s := New(Deps{
		Log:             slog.New(slog.NewTextHandler(logs, nil)),
		Store:           st,
		Authenticate:    func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
		Deactivate:      deactivateIn(st),
		Reactivate:      func(context.Context, string) {},
		IdentityChanged: func(context.Context, string, string) {},
		UserWritten: func(_ context.Context, action string, u store.User, changed []string) {
			writes = append(writes, userWrite{action, u.ID, changed})
		},
		ProtectedUsername: "break-glass",
	})
	mux := http.NewServeMux()
	s.Routes(mux)
	return mux, &writes, logs
}

func testPatch(ops string) string {
	return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[` + ops + `]}`
}

// TestAgentManagerBecomesSponsor pins that the SCIM enterprise manager of an
// agent becomes its sponsor: in every form an identity manager sends it, by
// the manager's Straza id or userName, judged on the user's type after the
// write. A sponsor the same write names wins, a remove clears the sponsor,
// and a person's manager is accepted and never stored. A manager Straza
// cannot apply is refused in a PATCH. In a POST or a PUT it is skipped with
// one warning, and the user is saved with the sponsor it had.
func TestAgentManagerBecomesSponsor(t *testing.T) {
	st := newPlanesTestStore(t)
	mux, writes, logs := managerServer(t, st)
	ctx := context.Background()
	const ben, ada = "ben@lab.example", "ada@lab.example"

	read := func(name string) store.User {
		t.Helper()
		u, err := st.Users().GetByUsername(ctx, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return u
	}
	post := func(body string) {
		t.Helper()
		if rec := scimReq(t, mux, http.MethodPost, "/scim/v2/Users", body); rec.Code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", body, rec.Code, rec.Body)
		}
	}
	for _, name := range []string{ben, ada} {
		post(`{"schemas":[` + testCore + `],"userName":"` + name + `","userType":"human"}`)
	}
	if _, err := st.Users().Create(ctx, store.User{Username: "break-glass", Origin: store.OriginLocal, Status: store.UserActive}); err != nil {
		t.Fatal(err)
	}
	benID, glassID := read(ben).ID, read("break-glass").ID
	agentDoc := func(name, extra string) string {
		return `{"schemas":[` + testCore + `,` + testAgentic + `],"userName":"` + name + `"` + extra + `}`
	}
	mgrPath := testEnterprise + `:manager`
	op := func(verb, path, value string) string {
		if value == "" {
			return `{"op":"` + verb + `","path":"` + path + `"}`
		}
		return `{"op":"` + verb + `","path":"` + path + `","value":` + value + `}`
	}

	type row struct {
		name string
		// seed is the sponsor the agent holds before the write, set over SCIM.
		seed string
		// person writes to a person instead of an agent.
		person bool
		// create sends body as the POST that makes the agent, and put as a
		// PUT on it. Otherwise body is a PATCH.
		create, put bool
		body        func(agent string) string
		code        int
		sponsor     string
		detail      []string
		// skipped lists what the one warning of a manager Straza did not
		// apply says. Empty means the write logs no such warning.
		skipped []string
		// disabled says the write leaves the user deactivated.
		disabled bool
	}
	rows := []row{
		{name: "Entra's PATCH, Add with the manager's Straza id as a string",
			body: func(string) string { return testPatch(op("Add", mgrPath, `"`+benID+`"`)) },
			code: http.StatusOK, sponsor: ben},
		{name: "the manager as an object with a value",
			body: func(string) string {
				return testPatch(op("replace", mgrPath, `{"value":"`+benID+`","displayName":"ben"}`))
			},
			code: http.StatusOK, sponsor: ben},
		{name: "the manager's value on its own path",
			body: func(string) string { return testPatch(op("add", mgrPath+".value", `"`+benID+`"`)) },
			code: http.StatusOK, sponsor: ben},
		{name: "the manager on the bare path, by userName",
			body: func(string) string { return testPatch(op("add", "manager", `"`+ben+`"`)) },
			code: http.StatusOK, sponsor: ben},
		{name: "the whole enterprise extension, with an attribute Straza does not map",
			body: func(string) string {
				return testPatch(op("add", testEnterprise, `{"department":"R&D","manager":{"value":"`+benID+`"}}`))
			},
			code: http.StatusOK, sponsor: ben},
		{name: "a PATCH without a path that carries the enterprise extension",
			body: func(string) string {
				return testPatch(`{"op":"add","value":{"` + testEnterprise + `":{"manager":{"value":"` + benID + `"}}}}`)
			},
			code: http.StatusOK, sponsor: ben},
		{name: "a create with the manager as an object", create: true,
			body: func(agent string) string {
				return agentDoc(agent, `,"`+testEnterprise+`":{"manager":{"value":"`+benID+`"}}`)
			},
			code: http.StatusCreated, sponsor: ben},
		{name: "a create with the manager as a string", create: true,
			body: func(agent string) string { return agentDoc(agent, `,"`+testEnterprise+`":{"manager":"`+ben+`"}`) },
			code: http.StatusCreated, sponsor: ben},
		{name: "a PUT with the manager", put: true, seed: ada,
			body: func(agent string) string {
				return agentDoc(agent, `,"`+testEnterprise+`":{"manager":{"value":"`+benID+`"}}`)
			},
			code: http.StatusOK, sponsor: ben},
		{name: "a PUT without a manager keeps the sponsor", put: true, seed: ada,
			body: func(agent string) string { return agentDoc(agent, "") },
			code: http.StatusOK, sponsor: ada},
		{name: "a sponsor the same PATCH names wins, whatever the order",
			body: func(string) string {
				return testPatch(op("add", "sponsor", `"`+ada+`"`) + `,` + op("add", mgrPath, `"`+benID+`"`))
			},
			code: http.StatusOK, sponsor: ada},
		{name: "a sponsor the same PATCH names again wins too", seed: ada,
			body: func(string) string {
				return testPatch(op("add", mgrPath, `"`+benID+`"`) + `,` + op("replace", "sponsor", `"`+ada+`"`))
			},
			code: http.StatusOK, sponsor: ada},
		{name: "a sponsor the same create names wins", create: true,
			body: func(agent string) string {
				return agentDoc(agent, `,"`+testEnterprise+`":{"manager":"`+ben+`"},"`+SchemaStrazaUser+`":{"sponsor":"`+ada+`"}`)
			},
			code: http.StatusCreated, sponsor: ada},
		{name: "a remove of the manager clears the sponsor", seed: ben,
			body: func(string) string { return testPatch(op("remove", mgrPath, "")) },
			code: http.StatusOK, sponsor: ""},
		{name: "an empty manager clears the sponsor", seed: ben,
			body: func(string) string { return testPatch(op("replace", mgrPath, `""`)) },
			code: http.StatusOK, sponsor: ""},
		{name: "a manager Straza does not know is refused", seed: ada,
			body: func(string) string { return testPatch(op("add", mgrPath, `"nobody@lab.example"`)) },
			code: http.StatusBadRequest, sponsor: ada,
			detail: []string{`"invalidValue"`, `manager \"nobody@lab.example\" is not a user Straza knows`, "Provision the manager first"}},
		{name: "the break-glass account is no manager, by id", seed: ada,
			body: func(string) string { return testPatch(op("add", mgrPath, `"`+glassID+`"`)) },
			code: http.StatusBadRequest, sponsor: ada,
			detail: []string{`"invalidValue"`, "is not a user Straza knows"}},
		{name: "the break-glass account is no manager, by userName",
			body: func(string) string { return testPatch(op("add", mgrPath, `"break-glass"`)) },
			code: http.StatusBadRequest, detail: []string{`"invalidValue"`, "is not a user Straza knows"}},
		{name: "a create with a manager Straza does not know saves the agent without a sponsor", create: true,
			body: func(agent string) string {
				return agentDoc(agent, `,"`+testEnterprise+`":{"manager":{"value":"nobody@lab.example"}}`)
			},
			code: http.StatusCreated, sponsor: "",
			skipped: []string{"user=agent-", `nobody@lab.example`, "is not a user Straza knows", "Provision the manager first"}},
		{name: "a PUT with a manager Straza does not know keeps the sponsor", put: true, seed: ada,
			body: func(agent string) string {
				return agentDoc(agent, `,"`+testEnterprise+`":{"manager":"nobody@lab.example"}`)
			},
			code: http.StatusOK, sponsor: ada,
			skipped: []string{"user=agent-", "is not a user Straza knows"}},
		{name: "a create with the break-glass account as manager saves the agent without a sponsor", create: true,
			body: func(agent string) string {
				return agentDoc(agent, `,"`+testEnterprise+`":{"manager":"break-glass"}`)
			},
			code: http.StatusCreated, sponsor: "",
			skipped: []string{"is not a user Straza knows"}},
		{name: "a create with a manager in a shape Straza does not read saves the agent", create: true,
			body: func(agent string) string {
				return agentDoc(agent, `,"`+testEnterprise+`":{"manager":{"managerId":"m-1"}}`)
			},
			code: http.StatusCreated, sponsor: "",
			skipped: []string{"user=agent-", "manager must be"}},
		{name: "a PUT with a manager in a shape Straza does not read keeps the sponsor", put: true, seed: ada,
			body: func(agent string) string { return agentDoc(agent, `,"`+testEnterprise+`":{"manager":7}`) },
			code: http.StatusOK, sponsor: ada,
			skipped: []string{"manager must be"}},
		{name: "a PUT that deactivates an agent is applied beside a manager Straza does not know", put: true, seed: ada,
			body: func(agent string) string {
				return agentDoc(agent, `,"active":false,"`+testEnterprise+`":{"manager":"nobody@lab.example"}`)
			},
			code: http.StatusOK, sponsor: ada, disabled: true,
			skipped: []string{"is not a user Straza knows"}},
		{name: "a manager that is neither a string nor an object is refused", seed: ada,
			body: func(string) string { return testPatch(op("add", mgrPath, `7`)) },
			code: http.StatusBadRequest, sponsor: ada,
			detail: []string{`"invalidValue"`, "manager must be"}},
		{name: "a person's manager is accepted and not stored", person: true,
			body: func(string) string { return testPatch(op("Add", mgrPath, `"`+benID+`"`)) },
			code: http.StatusOK, sponsor: ""},
		{name: "a person's manager is not looked up", person: true,
			body: func(string) string { return testPatch(op("add", mgrPath, `"nobody@lab.example"`)) },
			code: http.StatusOK, sponsor: ""},
		{name: "a person's manager in a shape Straza does not read is accepted", person: true,
			body: func(string) string { return testPatch(op("add", mgrPath, `{"managerId":"m-1"}`)) },
			code: http.StatusOK, sponsor: ""},
		{name: "a person's manager that is a number is accepted", person: true,
			body: func(string) string { return testPatch(op("add", mgrPath, `7`)) },
			code: http.StatusOK, sponsor: ""},
		{name: "a person is created with a manager whose value is null", create: true,
			body: func(name string) string {
				return `{"schemas":[` + testCore + `],"userName":"` + name + `","userType":"human","` + testEnterprise + `":{"manager":{"value":null}}}`
			},
			code: http.StatusCreated, sponsor: ""},
		{name: "an unclassified user is created with a manager Straza does not read", create: true,
			body: func(name string) string {
				return `{"schemas":[` + testCore + `],"userName":"` + name + `","` + testEnterprise + `":{"manager":{"managerId":"m-1"}}}`
			},
			code: http.StatusCreated, sponsor: ""},
		{name: "an agent's manager in a shape Straza does not read is refused", seed: ada,
			body: func(string) string { return testPatch(op("add", mgrPath, `{"managerId":"m-1"}`)) },
			code: http.StatusBadRequest, sponsor: ada,
			detail: []string{`"invalidValue"`, "manager must be"}},
		{name: "an agent's empty manager object in a PATCH clears the sponsor", seed: ben,
			body: func(string) string { return testPatch(op("replace", mgrPath, `{}`)) },
			code: http.StatusOK, sponsor: ""},
		{name: "an agent's empty manager object in a PUT leaves the sponsor", put: true, seed: ada,
			body: func(agent string) string { return agentDoc(agent, `,"`+testEnterprise+`":{"manager":{"value":null}}`) },
			code: http.StatusOK, sponsor: ada},
		{name: "a user typed agent by the same PATCH, after the manager", person: true,
			body: func(string) string {
				return testPatch(op("add", mgrPath, `"`+benID+`"`) + `,` + op("replace", "userType", `"agent"`))
			},
			code: http.StatusOK, sponsor: ben},
	}
	for i, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			name := fmt.Sprintf("agent-%02d", i)
			if !r.create {
				switch {
				case r.person:
					post(`{"schemas":[` + testCore + `],"userName":"` + name + `","userType":"human"}`)
				case r.seed != "":
					post(agentDoc(name, `,"`+SchemaStrazaUser+`":{"sponsor":"`+r.seed+`"}`))
				default:
					post(agentDoc(name, ""))
				}
			}
			method, path := http.MethodPatch, "/scim/v2/Users/"
			switch {
			case r.create:
				method = http.MethodPost
				path = "/scim/v2/Users"
			case r.put:
				method = http.MethodPut
				path += read(name).ID
			default:
				path += read(name).ID
			}
			*writes = nil
			logs.Reset()
			body := r.body(name)
			rec := scimReq(t, mux, method, path, body)
			if rec.Code != r.code {
				t.Fatalf("%s %s = %d %s, want %d", method, path, rec.Code, rec.Body, r.code)
			}
			warned := strings.Count(logs.String(), "SCIM manager not applied")
			if want := min(len(r.skipped), 1); warned != want {
				t.Errorf("the write logged %d warnings of a manager not applied, want %d: %s", warned, want, logs)
			}
			for _, want := range r.skipped {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("the warning %s does not say %q", logs, want)
				}
			}
			for _, want := range r.detail {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("answer %s does not say %q", rec.Body, want)
				}
			}
			// An answer may repeat a name the request sent, and never adds it.
			if !strings.Contains(body, "break-glass") && strings.Contains(rec.Body.String(), "break-glass") {
				t.Errorf("answer %s names the break-glass account", rec.Body)
			}
			if r.create && r.code != http.StatusCreated {
				if _, err := st.Users().GetByUsername(ctx, name); err == nil {
					t.Fatal("a refused create stored the agent")
				}
				return
			}
			if got := read(name).Sponsor; got != r.sponsor {
				t.Fatalf("sponsor = %q, want %q", got, r.sponsor)
			}
			if got := read(name).Status == store.UserDisabled; got != r.disabled {
				t.Errorf("deactivated = %v, want %v", got, r.disabled)
			}
			if r.code == http.StatusBadRequest && len(*writes) != 0 {
				t.Errorf("a refused write reported %+v", *writes)
			}
		})
	}

	// The write that turns a manager into a sponsor reports sponsor as the
	// changed field, once.
	post(agentDoc("agent-report", ""))
	*writes = nil
	rec := scimReq(t, mux, http.MethodPatch, "/scim/v2/Users/"+read("agent-report").ID, testPatch(op("Add", mgrPath, `"`+benID+`"`)))
	if rec.Code != http.StatusOK || len(*writes) != 1 || !slices.Equal((*writes)[0].changed, []string{"sponsor"}) {
		t.Fatalf("PATCH = %d, UserWritten = %+v, want one report of sponsor", rec.Code, *writes)
	}
}

// TestUserPatchIgnoresUnmappedAttributes pins that a PATCH ignores an
// attribute Straza does not map, as a create does, and still applies the
// mapped operations beside it. A mapped value stays checked, and the lock
// block, kind, origin and a user's groups stay refused.
func TestUserPatchIgnoresUnmappedAttributes(t *testing.T) {
	st := newPlanesTestStore(t)
	mux, writes, _ := managerServer(t, st)
	ctx := context.Background()
	rec := scimReq(t, mux, http.MethodPost, "/scim/v2/Users",
		`{"schemas":[`+testCore+`],"userName":"kim","displayName":"Kim","userType":"human"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	kim, err := st.Users().GetByUsername(ctx, "kim")
	if err != nil {
		t.Fatal(err)
	}
	ext := SchemaStrazaUser

	rows := []struct {
		name, ops string
		code      int
		scimType  string
		// changed is what the write reports, nil when it changed nothing.
		changed []string
		detail  string
	}{
		{name: "a core attribute Straza does not map",
			ops: `{"op":"replace","path":"nickName","value":"K"}`, code: http.StatusOK},
		{name: "an enterprise attribute Straza does not map",
			ops: `{"op":"Add","path":"` + testEnterprise + `:department","value":"R&D"}`, code: http.StatusOK},
		{name: "a filtered path Straza does not map",
			ops: `{"op":"add","path":"addresses[type eq \"work\"].locality","value":"Brno"}`, code: http.StatusOK},
		{name: "a remove of an attribute Straza does not map",
			ops: `{"op":"remove","path":"preferredLanguage"}`, code: http.StatusOK},
		{name: "an unmapped attribute beside a mapped one",
			ops:  `{"op":"replace","path":"nickName","value":"K"},{"op":"replace","path":"displayName","value":"Kim Novak"}`,
			code: http.StatusOK, changed: []string{"display"}},
		{name: "an unknown key inside the Straza extension",
			ops:  `{"op":"add","path":"` + ext + `","value":{"swarmId":"s1","futureAttribute":1}}`,
			code: http.StatusOK, changed: []string{"swarm_id"}},
		{name: "unknown keys in a PATCH without a path",
			ops:  `{"op":"replace","value":{"nickName":"K","title":"Engineer"}}`,
			code: http.StatusOK, changed: []string{"title"}},
		{name: "a mapped value is still checked",
			ops:  `{"op":"replace","path":"userType","value":"robot"}`,
			code: http.StatusBadRequest, scimType: "invalidValue", detail: "is not a value Straza accepts"},
		{name: "the lock block stays read-only",
			ops:  `{"op":"replace","path":"` + ext + `:locked","value":false}`,
			code: http.StatusBadRequest, scimType: "mutability", detail: "lock block is read-only"},
		{name: "kind stays read-only",
			ops:  `{"op":"replace","path":"kind","value":"nhi"}`,
			code: http.StatusBadRequest, scimType: "mutability", detail: "Straza-born facts"},
		{name: "a user's groups stay read-only",
			ops:  `{"op":"add","path":"groups","value":[{"value":"r1"}]}`,
			code: http.StatusBadRequest, scimType: "mutability", detail: "Send the membership as a members operation on /scim/v2/Groups"},
		{name: "a filtered remove of a user's group",
			ops:  `{"op":"remove","path":"groups[value eq \"r1\"]"}`,
			code: http.StatusBadRequest, scimType: "mutability", detail: "a user's groups are read-only"},
		{name: "groups inside a PATCH without a path",
			ops:  `{"op":"add","value":{"groups":[{"value":"r1"}]}}`,
			code: http.StatusBadRequest, scimType: "mutability", detail: "a user's groups are read-only"},
		{name: "groups qualified with the core schema URN",
			ops:  `{"op":"add","path":"` + SchemaUser + `:groups","value":[{"value":"r1"}]}`,
			code: http.StatusBadRequest, scimType: "mutability", detail: "a user's groups are read-only"},
		// A mapped attribute is never ignored for the form of its path: an
		// ignored deactivation would leave the user active behind a 200.
		{name: "the filtered email path Entra sends",
			ops:  `{"op":"Replace","path":"emails[type eq \"work\"].value","value":"kim@lab.example"}`,
			code: http.StatusOK, changed: []string{"email"}},
		{name: "a remove on a filtered email path",
			ops:  `{"op":"remove","path":"emails[type eq \"work\"].value"}`,
			code: http.StatusOK, changed: []string{"email"}},
		{name: "the name object on its path",
			ops:  `{"op":"replace","path":"name","value":{"givenName":"Kim","formatted":"Kim N."}}`,
			code: http.StatusOK, changed: []string{"display"}},
		{name: "the name object in a PATCH without a path",
			ops:  `{"op":"replace","value":{"name":{"formatted":"Kim Novak-Lee"}}}`,
			code: http.StatusOK, changed: []string{"display"}},
		{name: "the core schema URN as the path of an object",
			ops:  `{"op":"replace","path":"` + SchemaUser + `","value":{"title":"Principal"}}`,
			code: http.StatusOK, changed: []string{"title"}},
		{name: "groups inside the core schema object",
			ops:  `{"op":"add","path":"` + SchemaUser + `","value":{"groups":[{"value":"r1"}]}}`,
			code: http.StatusBadRequest, scimType: "mutability", detail: "a user's groups are read-only"},
		{name: "a mapped attribute qualified with the core schema URN",
			ops:  `{"op":"replace","path":"` + SchemaUser + `:title","value":"Lead"}`,
			code: http.StatusOK, changed: []string{"title"}},
		{name: "a deactivation qualified with the core schema URN",
			ops:  `{"op":"Replace","path":"` + SchemaUser + `:active","value":"False"}`,
			code: http.StatusOK, changed: []string{"status"}},
		{name: "a reactivation inside a PATCH without a path, keyed by the core schema URN",
			ops:  `{"op":"replace","value":{"` + SchemaUser + `":{"active":true}}}`,
			code: http.StatusOK, changed: []string{"status"}},
		{name: "a deactivation inside the core schema object",
			ops:  `{"op":"replace","path":"` + SchemaUser + `","value":{"active":false}}`,
			code: http.StatusOK, changed: []string{"status"}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			*writes = nil
			rec := scimReq(t, mux, http.MethodPatch, "/scim/v2/Users/"+kim.ID, testPatch(r.ops))
			if rec.Code != r.code {
				t.Fatalf("PATCH = %d %s, want %d", rec.Code, rec.Body, r.code)
			}
			if r.scimType != "" && scimErrorType(t, rec.Body.String()) != r.scimType {
				t.Errorf("scimType in %s, want %s", rec.Body, r.scimType)
			}
			if !strings.Contains(rec.Body.String(), r.detail) {
				t.Errorf("answer %s does not say %q", rec.Body, r.detail)
			}
			var got []string
			if len(*writes) == 1 {
				got = (*writes)[0].changed
			}
			if len(*writes) > 1 || !slices.Equal(got, r.changed) {
				t.Errorf("UserWritten = %+v, want changed %v", *writes, r.changed)
			}
		})
	}
}
