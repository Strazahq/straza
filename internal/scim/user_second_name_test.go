package scim

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestUserExtensionSecondName pins that the Straza User extension is read
// under its second name as under its own, on POST, PUT and PATCH: the same
// attributes, the same checks and the same read-only lock block, kind and
// origin. A write that names an attribute under both names stores the value
// under Straza's own name, bare or qualified, in either order, and still
// checks the form of the other value. The second name is not the agent
// mark, and no answer carries it. A write under it is reported through
// Deps.UserWritten and a refusal through Deps.WriteRefused, once each.
func TestUserExtensionSecondName(t *testing.T) {
	st := newPlanesTestStore(t)
	var writes []userWrite
	var refusals []Refusal
	srv := New(Deps{
		Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:           st,
		Authenticate:    func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
		Deactivate:      deactivateIn(st),
		Reactivate:      func(context.Context, string) {},
		IdentityChanged: func(context.Context, string, string) {},
		UserWritten: func(_ context.Context, action string, u store.User, changed []string) {
			writes = append(writes, userWrite{action, u.ID, changed})
		},
		WriteRefused: func(_ *http.Request, ref Refusal) { refusals = append(refusals, ref) },
	})
	mux := http.NewServeMux()
	srv.Routes(mux)
	ctx := context.Background()
	const (
		own      = SchemaStrazaUser
		second   = "urn:ietf:params:scim:schemas:extension:straza:2.0:User"
		mixed    = "URN:IETF:Params:SCIM:Schemas:Extension:Straza:2.0:USER"
		ben, ada = "ben@lab.example", "ada@lab.example"
		held     = "aaaaaaaa-0000-4000-8000-000000000001"
	)
	agentDoc := func(name, extra string) string {
		return `{"schemas":[` + testCore + `,` + testAgentic + `],"userName":"` + name + `"` + extra + `}`
	}
	for _, body := range []string{
		`{"schemas":[` + testCore + `],"userName":"` + ben + `","userType":"human"}`,
		`{"schemas":[` + testCore + `],"userName":"` + ada + `","userType":"human"}`,
		agentDoc("holder@lab.example", `,"`+own+`":{"entraAgentId":"`+held+`"}`),
	} {
		if rec := scimReq(t, mux, http.MethodPost, "/scim/v2/Users", body); rec.Code != http.StatusCreated {
			t.Fatalf("seed %s = %d %s", body, rec.Code, rec.Body)
		}
	}
	benUser, err := st.Users().GetByUsername(ctx, ben)
	if err != nil {
		t.Fatal(err)
	}
	op := func(verb, path, value string) string {
		if value == "" {
			return `{"op":"` + verb + `","path":"` + path + `"}`
		}
		return `{"op":"` + verb + `","path":"` + path + `","value":` + value + `}`
	}

	// ext is the writable half of the Straza extension a user holds.
	type ext struct {
		entra, sponsor, swarm, agency string
		ephemeral                     bool
	}
	type row struct {
		name string
		// seed is the extension the row's agent holds before the write, sent
		// under Straza's own name. A create row has no agent before it.
		seed string
		// create sends body as a POST and put as a PUT on the row's agent.
		// Otherwise body is a PATCH on it.
		create, put bool
		// body is the request. {NAME} is the row's userName, {A} and {B} are
		// two ids of the row and {BEN} is ben's Straza id.
		body   string
		code   int
		detail []string
		// want is what the row's user holds after the write. A refused
		// create leaves no user.
		want ext
		// untyped says the stored user has no userType and no agent mark.
		untyped bool
		// kind is the kind the stored user must have, when the row says one.
		kind string
		// changed is a field the one report of the write must name. quiet
		// says the write reports nothing, because it changed nothing.
		changed string
		quiet   bool
	}
	rows := []row{
		{name: "Entra's create, with entraAgentId under the second name only", create: true,
			body: `{"schemas":[` + testCore + `,"` + testEnterprise + `"],"externalId":"ext-{NAME}","userName":"{NAME}","active":true,` +
				`"displayName":"lab-entra-alias-agent","meta":{"resourceType":"User"},"userType":"agent","` + second + `":{"entraAgentId":"{A}"}}`,
			code: http.StatusCreated, want: ext{entra: "{A}"}},
		{name: "a create with every writable attribute under the second name", create: true,
			body: agentDoc("{NAME}", `,"`+second+`":{"agencyMode":"Autonomous","sponsor":"`+ada+`","swarmId":"swarm-1","ephemeral":"True","entraAgentId":"585D2BE7-0000-4000-8000-00000000ABCD"}`),
			code: http.StatusCreated,
			want: ext{entra: "585d2be7-0000-4000-8000-00000000abcd", sponsor: ada, swarm: "swarm-1", agency: "autonomous", ephemeral: true}},
		{name: "a create with both names stores the values under Straza's own", create: true,
			body: agentDoc("{NAME}", `,"`+second+`":{"entraAgentId":"{B}","swarmId":"second","agencyMode":"supervised","ephemeral":true}`+
				`,"`+own+`":{"entraAgentId":"{A}","swarmId":"own","ephemeral":false}`),
			code: http.StatusCreated, want: ext{entra: "{A}", swarm: "own", agency: "supervised"}},
		{name: "a create whose value under the second name is not a GUID is refused", create: true,
			body: agentDoc("{NAME}", `,"`+second+`":{"entraAgentId":"not-a-guid"}`),
			code: http.StatusBadRequest, detail: []string{`"invalidValue"`, "is not an Entra agent identity id"}},
		{name: "a create checks the value under the second name when Straza's own wins", create: true,
			body: agentDoc("{NAME}", `,"`+own+`":{"entraAgentId":"{A}"},"`+second+`":{"entraAgentId":"not-a-guid"}`),
			code: http.StatusBadRequest, detail: []string{`"invalidValue"`, "is not an Entra agent identity id"}},
		{name: "a person cannot hold entraAgentId under the second name", create: true,
			body: `{"schemas":[` + testCore + `],"userName":"{NAME}","userType":"human","` + second + `":{"entraAgentId":"{A}"}}`,
			code: http.StatusBadRequest, detail: []string{`"invalidValue"`, "only on a user of type agent"}},
		{name: "the second name in schemas is not the agent mark", create: true,
			body: `{"schemas":[` + testCore + `,"` + second + `"],"userName":"{NAME}","` + second + `":{"swarmId":"swarm-2"}}`,
			code: http.StatusCreated, want: ext{swarm: "swarm-2"}, untyped: true},
		{name: "a sponsor under the second name wins over the manager of the same create", create: true,
			body: agentDoc("{NAME}", `,"`+testEnterprise+`":{"manager":"`+ben+`"},"`+second+`":{"sponsor":"`+ada+`"}`),
			code: http.StatusCreated, want: ext{sponsor: ada}},
		{name: "a create with the second name as a key in mixed case", create: true,
			body: agentDoc("{NAME}", `,"`+mixed+`":{"SwarmId":"swarm-5"}`),
			code: http.StatusCreated, want: ext{swarm: "swarm-5"}},
		{name: "a create ignores the read-only half under the second name", create: true,
			body: agentDoc("{NAME}", `,"`+second+`":{"locked":true,"lockReason":"x","kind":"human","origin":"local","swarmId":"swarm-6"}`),
			code: http.StatusCreated, want: ext{swarm: "swarm-6"}, kind: "nhi"},

		{name: "a PUT ignores the read-only half under the second name", put: true,
			body: agentDoc("{NAME}", `,"`+second+`":{"locked":true,"kind":"human","origin":"local","swarmId":"swarm-7"}`),
			code: http.StatusOK, want: ext{swarm: "swarm-7"}, kind: "nhi"},
		{name: "a sponsor under the second name wins over the manager of the same PUT", put: true,
			body: agentDoc("{NAME}", `,"`+testEnterprise+`":{"manager":"`+ben+`"},"`+second+`":{"sponsor":"`+ada+`"}`),
			code: http.StatusOK, want: ext{sponsor: ada}},
		{name: "an empty value under Straza's own name in a PUT counts as absent", put: true, seed: `{"swarmId":"old"}`,
			body: agentDoc("{NAME}", `,"`+own+`":{"swarmId":""},"`+second+`":{"swarmId":"second"}`),
			code: http.StatusOK, want: ext{swarm: "second"}},
		{name: "a PUT with the second name sets its attributes", put: true,
			body: agentDoc("{NAME}", `,"`+second+`":{"entraAgentId":"{A}","swarmId":"swarm-3"}`),
			code: http.StatusOK, want: ext{entra: "{A}", swarm: "swarm-3"}},
		{name: "a PUT with both names stores the values under Straza's own", put: true, seed: `{"swarmId":"old"}`,
			body: agentDoc("{NAME}", `,"`+own+`":{"swarmId":"own"},"`+second+`":{"swarmId":"second","entraAgentId":"{B}"}`),
			code: http.StatusOK, want: ext{entra: "{B}", swarm: "own"}},
		{name: "a PUT with an agencyMode outside the vocabulary under the second name is refused", put: true, seed: `{"swarmId":"old"}`,
			body: agentDoc("{NAME}", `,"`+second+`":{"agencyMode":"rogue"}`),
			code: http.StatusBadRequest, detail: []string{`"invalidValue"`, "is not a value Straza accepts"}, want: ext{swarm: "old"}},

		{name: "Entra's PATCH, Add on the path under the second name",
			body: testPatch(op("Add", second+":entraAgentId", `"{A}"`)),
			code: http.StatusOK, want: ext{entra: "{A}"}, changed: "entra_agent_id"},
		{name: "a repeat of the stored value under the second name reports nothing", seed: `{"entraAgentId":"{B}"}`,
			body: testPatch(op("Add", second+":entraAgentId", `"{B}"`)),
			code: http.StatusOK, want: ext{entra: "{B}"}, quiet: true},
		{name: "a PATCH path under the second name in mixed case",
			body: testPatch(op("Replace", mixed+":EntraAgentId", `"{A}"`)),
			code: http.StatusOK, want: ext{entra: "{A}"}},
		{name: "a PATCH of the whole extension under the second name",
			body: testPatch(op("replace", second, `{"swarmId":"swarm-4","entraAgentId":"{A}","ephemeral":"True"}`)),
			code: http.StatusOK, want: ext{entra: "{A}", swarm: "swarm-4", ephemeral: true}},
		{name: "a PATCH without a path keyed by the second name",
			body: testPatch(`{"op":"add","value":{"` + second + `":{"entraAgentId":"{A}","agencyMode":"autonomous"}}}`),
			code: http.StatusOK, want: ext{entra: "{A}", agency: "autonomous"}},
		{name: "a PATCH without a path keyed by the second name in mixed case",
			body: testPatch(`{"op":"add","value":{"` + mixed + `":{"EntraAgentId":"{A}"}}}`),
			code: http.StatusOK, want: ext{entra: "{A}"}},
		{name: "a PATCH without a path keyed by the attribute's full name under the second name",
			body: testPatch(`{"op":"Add","value":{"` + second + `:entraAgentId":"{A}"}}`),
			code: http.StatusOK, want: ext{entra: "{A}"}},
		{name: "a remove on the path under the second name", seed: `{"entraAgentId":"{B}","swarmId":"kept"}`,
			body: testPatch(op("remove", second+":entraAgentId", "")),
			code: http.StatusOK, want: ext{swarm: "kept"}},
		{name: "a remove of the whole extension under the second name",
			seed: `{"entraAgentId":"{B}","swarmId":"old","sponsor":"` + ada + `","agencyMode":"supervised","ephemeral":true}`,
			body: testPatch(op("remove", second, "")),
			code: http.StatusOK, want: ext{}},
		{name: "a value under the second name that is not a GUID is refused", seed: `{"swarmId":"kept"}`,
			body: testPatch(op("add", second+":entraAgentId", `"not-a-guid"`)),
			code: http.StatusBadRequest, detail: []string{`"invalidValue"`, "is not an Entra agent identity id"}, want: ext{swarm: "kept"}},
		{name: "an id another agent holds is refused under the second name",
			body: testPatch(op("add", second+":entraAgentId", `"`+held+`"`)),
			code: http.StatusConflict, detail: []string{`"uniqueness"`, "already belongs to holder@lab.example"}},
		{name: "an attribute Straza does not map is ignored under the second name", seed: `{"swarmId":"kept"}`,
			body: testPatch(op("add", second+":costCenter", `"42"`)),
			code: http.StatusOK, want: ext{swarm: "kept"}},
		{name: "a name that only starts like the second name is not the extension", seed: `{"swarmId":"kept"}`,
			body: testPatch(op("add", second+"s:swarmId", `"new"`) + `,` + op("add", strings.TrimSuffix(second, "User")+"Group:swarmId", `"new"`)),
			code: http.StatusOK, want: ext{swarm: "kept"}},
		{name: "a sponsor under the second name wins over the manager of the same PATCH",
			body: testPatch(op("add", testEnterprise+":manager", `"{BEN}"`) + `,` + op("add", second+":sponsor", `"`+ada+`"`)),
			code: http.StatusOK, want: ext{sponsor: ada}},

		{name: "both names in one PATCH, Straza's own first",
			body: testPatch(op("Add", own+":entraAgentId", `"{A}"`) + `,` + op("Add", second+":entraAgentId", `"{B}"`)),
			code: http.StatusOK, want: ext{entra: "{A}"}},
		{name: "both names in one PATCH, the second name first",
			body: testPatch(op("Add", second+":entraAgentId", `"{B}"`) + `,` + op("Add", own+":entraAgentId", `"{A}"`)),
			code: http.StatusOK, want: ext{entra: "{A}"}},
		{name: "both names inside one PATCH without a path",
			body: testPatch(`{"op":"add","value":{"` + own + `":{"entraAgentId":"{A}"},"` + second + `":{"entraAgentId":"{B}","swarmId":"second"}}}`),
			code: http.StatusOK, want: ext{entra: "{A}", swarm: "second"}},
		{name: "both whole extensions in one PATCH, Straza's own first",
			body: testPatch(op("replace", own, `{"swarmId":"own"}`) + `,` + op("replace", second, `{"swarmId":"second","agencyMode":"supervised"}`)),
			code: http.StatusOK, want: ext{swarm: "own", agency: "supervised"}},
		{name: "both whole extensions in one PATCH, the second name first",
			body: testPatch(op("replace", second, `{"swarmId":"second","agencyMode":"supervised"}`) + `,` + op("replace", own, `{"swarmId":"own"}`)),
			code: http.StatusOK, want: ext{swarm: "own", agency: "supervised"}},
		{name: "a remove under Straza's own name wins, sent first", seed: `{"entraAgentId":"{B}"}`,
			body: testPatch(op("remove", own+":entraAgentId", "") + `,` + op("add", second+":entraAgentId", `"{A}"`)),
			code: http.StatusOK, want: ext{}},
		{name: "a remove under Straza's own name wins, sent last", seed: `{"entraAgentId":"{B}"}`,
			body: testPatch(op("add", second+":entraAgentId", `"{A}"`) + `,` + op("remove", own+":entraAgentId", "")),
			code: http.StatusOK, want: ext{}},
		{name: "a remove of Straza's own whole extension wins over an add under the second name", seed: `{"swarmId":"old"}`,
			body: testPatch(op("remove", own, "") + `,` + op("add", second+":swarmId", `"new"`)),
			code: http.StatusOK, want: ext{}},
		{name: "a remove of the whole extension under the second name keeps what Straza's own name wrote",
			seed: `{"swarmId":"old","agencyMode":"supervised"}`,
			body: testPatch(op("add", own+":swarmId", `"own"`) + `,` + op("remove", second, "")),
			code: http.StatusOK, want: ext{swarm: "own"}},
		{name: "the value under the second name is checked when Straza's own wins, sent last", seed: `{"swarmId":"kept"}`,
			body: testPatch(op("add", own+":entraAgentId", `"{A}"`) + `,` + op("add", second+":entraAgentId", `"not-a-guid"`)),
			code: http.StatusBadRequest, detail: []string{`"invalidValue"`, "is not an Entra agent identity id"}, want: ext{swarm: "kept"}},
		{name: "the value under the second name is checked when Straza's own wins, sent first", seed: `{"swarmId":"kept"}`,
			body: testPatch(op("add", second+":agencyMode", `"rogue"`) + `,` + op("add", own+":agencyMode", `"supervised"`)),
			code: http.StatusBadRequest, detail: []string{`"invalidValue"`, "is not a value Straza accepts"}, want: ext{swarm: "kept"}},
		{name: "an id another agent holds under the second name is not stored when Straza's own name wins",
			body: testPatch(op("add", own+":entraAgentId", `"{A}"`) + `,` + op("add", second+":entraAgentId", `"`+held+`"`)),
			code: http.StatusOK, want: ext{entra: "{A}"}},
		{name: "a bare path wins over the second name, sent first",
			body: testPatch(op("add", "swarmId", `"bare"`) + `,` + op("add", second+":swarmId", `"second"`)),
			code: http.StatusOK, want: ext{swarm: "bare"}},
		{name: "a bare path wins over the second name, sent last",
			body: testPatch(op("add", second+":swarmId", `"second"`) + `,` + op("add", "swarmId", `"bare"`)),
			code: http.StatusOK, want: ext{swarm: "bare"}},
		{name: "a bare key wins over the second name inside one PATCH without a path",
			body: testPatch(`{"op":"add","value":{"swarmId":"bare","` + second + `":{"swarmId":"second","agencyMode":"supervised"}}}`),
			code: http.StatusOK, want: ext{swarm: "bare", agency: "supervised"}},
	}
	// The read-only half answers a mutability error under the second name in
	// every form a PATCH can name it, and saves nothing of the PATCH.
	for _, attr := range []string{"locked", "lockReason", "lockedAt", "lockOrigin", "kind", "origin"} {
		detail := "the Straza lock block is read-only over SCIM"
		if attr == "kind" || attr == "origin" {
			detail = "kind and origin are Straza-born facts"
		}
		for _, form := range []struct{ name, body string }{
			{"on its path", testPatch(op("replace", second+":"+attr, `"x"`))},
			{"on its path in mixed case", testPatch(op("Remove", mixed+":"+strings.ToUpper(attr), ""))},
			{"inside the whole extension", testPatch(op("replace", second, `{"swarmId":"new","`+attr+`":"x"}`))},
			{"inside a PATCH without a path", testPatch(`{"op":"add","value":{"` + second + `":{"` + attr + `":"x"}}}`)},
		} {
			rows = append(rows, row{name: attr + " under the second name, " + form.name, seed: `{"swarmId":"kept"}`, body: form.body,
				code: http.StatusBadRequest, detail: []string{`"mutability"`, detail}, want: ext{swarm: "kept"}})
		}
	}

	for i, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			name := fmt.Sprintf("agent-%02d@lab.example", i)
			fill := strings.NewReplacer("{NAME}", name, "{BEN}", benUser.ID,
				"{A}", fmt.Sprintf("585d2be7-0000-4000-8000-%012d", i), "{B}", fmt.Sprintf("5f8c2a1e-0000-4000-8000-%012d", i))
			method, path := http.MethodPost, "/scim/v2/Users"
			if !r.create {
				seed := ""
				if r.seed != "" {
					seed = `,"` + own + `":` + fill.Replace(r.seed)
				}
				if rec := scimReq(t, mux, http.MethodPost, "/scim/v2/Users", agentDoc(name, seed)); rec.Code != http.StatusCreated {
					t.Fatalf("seed the agent = %d %s", rec.Code, rec.Body)
				}
				seeded, err := st.Users().GetByUsername(ctx, name)
				if err != nil {
					t.Fatal(err)
				}
				method, path = http.MethodPatch, "/scim/v2/Users/"+seeded.ID
				if r.put {
					method = http.MethodPut
				}
			}
			writes, refusals = nil, nil
			rec := scimReq(t, mux, method, path, fill.Replace(r.body))
			if rec.Code != r.code {
				t.Fatalf("%s %s = %d %s, want %d", method, path, rec.Code, rec.Body, r.code)
			}
			switch refused := r.code >= http.StatusBadRequest; {
			case refused && (len(writes) != 0 || len(refusals) != 1 || refusals[0].Status != r.code || refusals[0].Username != name):
				t.Fatalf("a refused write reported writes %+v and refusals %+v, want one refusal with status %d for %s", writes, refusals, r.code, name)
			case !refused && len(refusals) != 0:
				t.Fatalf("an accepted write reported the refusal %+v", refusals)
			case r.quiet && len(writes) != 0:
				t.Fatalf("a write that changed nothing reported %+v", writes)
			case r.changed != "" && (len(writes) != 1 || !slices.Contains(writes[0].changed, r.changed)):
				t.Fatalf("reported %+v, want one write that names %s", writes, r.changed)
			}
			for _, want := range r.detail {
				if !strings.Contains(rec.Body.String(), want) {
					t.Fatalf("answer %s does not say %q", rec.Body, want)
				}
			}
			if strings.Contains(strings.ToLower(rec.Body.String()), "extension:straza") {
				t.Fatalf("answer %s carries the second name", rec.Body)
			}
			u, err := st.Users().GetByUsername(ctx, name)
			if r.create && r.code >= http.StatusBadRequest {
				if err == nil {
					t.Fatalf("a refused create stored the user %+v", u)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := r.want
			want.entra = fill.Replace(want.entra)
			if got := (ext{u.EntraAgentID, u.Sponsor, u.SwarmID, u.AgencyMode, u.Ephemeral}); got != want {
				t.Fatalf("stored extension = %+v, want %+v", got, want)
			}
			if r.untyped && (u.UserType != "" || userKind(u) != "human") {
				t.Fatalf("stored userType %q and kind %q, want an untyped user without the agent mark", u.UserType, userKind(u))
			}
			if u.Origin != store.OriginSCIM || (r.kind != "" && userKind(u) != r.kind) {
				t.Fatalf("stored origin %q and kind %q, want scim and %q", u.Origin, userKind(u), r.kind)
			}
		})
	}

	// Discovery lists the extension under Straza's own name only.
	for _, path := range []string{"/scim/v2/Schemas", "/scim/v2/ResourceTypes", "/scim/v2/ServiceProviderConfig"} {
		rec := scimReq(t, mux, http.MethodGet, path, "")
		if rec.Code != http.StatusOK || strings.Contains(strings.ToLower(rec.Body.String()), "extension:straza") {
			t.Fatalf("GET %s = %d %s, want 200 without the second name", path, rec.Code, rec.Body)
		}
	}
	if rec := scimReq(t, mux, http.MethodGet, "/scim/v2/Schemas/"+second, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /scim/v2/Schemas/%s = %d, want 404", second, rec.Code)
	}
}
