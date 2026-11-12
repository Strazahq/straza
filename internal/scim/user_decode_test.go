package scim

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestUserDocumentUnreadable pins the 400 a POST or a PUT of a User answers
// when Straza cannot read its body. A value of the wrong JSON type names the
// attribute, the type Straza expects, the type that came and the fix, under
// both names of the Straza extension. Broken JSON keeps its own sentence and
// a missing userName keeps "userName is required", on both verbs. Nothing is
// saved, and each refusal is reported through Deps.WriteRefused once, with
// the name of the user it is about wherever Straza could read one.
func TestUserDocumentUnreadable(t *testing.T) {
	st := newPlanesTestStore(t)
	var refusals []Refusal
	srv := New(Deps{
		Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:           st,
		Authenticate:    func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
		Deactivate:      deactivateIn(st),
		Reactivate:      func(context.Context, string) {},
		IdentityChanged: func(context.Context, string, string) {},
		UserWritten:     func(context.Context, string, store.User, []string) {},
		WriteRefused:    func(_ *http.Request, ref Refusal) { refusals = append(refusals, ref) },
	})
	mux := http.NewServeMux()
	srv.Routes(mux)
	ctx := context.Background()
	const (
		own    = SchemaStrazaUser
		second = "urn:ietf:params:scim:schemas:extension:straza:2.0:User"
		kept   = "kept@lab.example"
		fresh  = "fresh@lab.example"
		broken = "malformed JSON body"
	)
	seed := `{"schemas":[` + testCore + `],"userName":"` + kept + `","displayName":"Kept As Is","userType":"human"}`
	if rec := scimReq(t, mux, http.MethodPost, "/scim/v2/Users", seed); rec.Code != http.StatusCreated {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body)
	}
	before, err := st.Users().GetByUsername(ctx, kept)
	if err != nil {
		t.Fatal(err)
	}
	// mistyped is the detail for attr, which must be of the JSON type want
	// and came as sent.
	mistyped := func(attr, a, want, sent string) string {
		return attr + " must be " + a + " " + want + ", and this request sent " + sent + ", so nothing was saved. " +
			"Set its type to " + want + " in the identity manager's attribute mapping, then send the user again"
	}
	// doc is a User document that names the new user and holds extra.
	doc := func(extra string) string {
		return `{"schemas":[` + testCore + `],"userName":"` + fresh + `"` + extra + `}`
	}

	tests := []struct {
		name   string
		body   string
		detail string
		// postType and putType are the scimType each verb answers. Broken
		// JSON is the one case where the two differ.
		postType, putType string
		// postUser is the userName the refusal of the POST names: the one the
		// body asks for when Straza can read it, else none. The refusal of
		// the PUT always names the stored user.
		postUser string
	}{
		{
			name: "a number under the second name", body: doc(`,"` + second + `":{"sponsor":7}`),
			detail: "urn:ietf:params:scim:schemas:extension:straza:2.0:User:sponsor must be a string, and this request sent a number, so nothing was saved. " +
				"Set its type to string in the identity manager's attribute mapping, then send the user again",
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "a boolean under Straza's own name", body: doc(`,"` + own + `":{"entraAgentId":true}`),
			detail: "urn:straza:params:scim:schemas:extension:2.0:User:entraAgentId must be a string, and this request sent a boolean, so nothing was saved. " +
				"Set its type to string in the identity manager's attribute mapping, then send the user again",
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "an array under the second name", body: doc(`,"` + second + `":{"swarmId":["s-1"]}`),
			detail:   mistyped(second+":swarmId", "a", "string", "an array"),
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "an object under Straza's own name", body: doc(`,"` + own + `":{"agencyMode":{"value":"autonomous"}}`),
			detail:   mistyped(own+":agencyMode", "a", "string", "an object"),
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "the second name in another case", body: doc(`,"URN:IETF:Params:SCIM:Schemas:Extension:Straza:2.0:USER":{"SPONSOR":7}`),
			detail:   mistyped(second+":sponsor", "a", "string", "a number"),
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "the second name's block as a string", body: doc(`,"` + second + `":"autonomous"`),
			detail:   mistyped(second, "an", "object", "a string"),
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "Straza's own block as an array", body: doc(`,"` + own + `":[{"sponsor":"ben"}]`),
			detail:   mistyped(own, "an", "object", "an array"),
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "a core attribute", body: doc(`,"externalId":12345`),
			detail:   mistyped("externalId", "a", "string", "a number"),
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "a sub-attribute", body: doc(`,"name":{"formatted":1}`),
			detail:   mistyped("name.formatted", "a", "string", "a number"),
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "a boolean sub-attribute", body: doc(`,"emails":[{"value":"f@lab.example","primary":"yes"}]`),
			detail:   mistyped("emails.primary", "a", "boolean", "a string"),
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "a multi-valued attribute", body: doc(`,"emails":"f@lab.example"`),
			detail:   mistyped("emails", "an", "array", "a string"),
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "a wrong type ahead of userName", body: `{"schemas":[` + testCore + `],"externalId":12345,"userName":"` + fresh + `"}`,
			detail:   mistyped("externalId", "a", "string", "a number"),
			postType: "invalidValue", putType: "invalidValue", postUser: fresh,
		},
		{
			name: "userName itself", body: `{"schemas":[` + testCore + `],"userName":7}`,
			detail:   mistyped("userName", "a", "string", "a number"),
			postType: "invalidValue", putType: "invalidValue",
		},
		{
			name: "a wrong type and no userName", body: `{"schemas":[` + testCore + `],"` + second + `":{"sponsor":7}}`,
			detail:   mistyped(second+":sponsor", "a", "string", "a number"),
			postType: "invalidValue", putType: "invalidValue",
		},
		{
			name: "broken JSON", body: `{"schemas":[` + testCore + `],"userName":"` + fresh + `",`,
			detail: broken, postType: "invalidSyntax", putType: "invalidValue",
		},
		{
			name: "no body", body: "",
			detail: broken, postType: "invalidSyntax", putType: "invalidValue",
		},
		{
			name: "a body that is not an object", body: `[{"userName":"` + fresh + `"}]`,
			detail: broken, postType: "invalidSyntax", putType: "invalidValue",
		},
		{
			name: "no userName", body: `{"schemas":[` + testCore + `],"displayName":"Nameless"}`,
			detail: "userName is required", postType: "invalidValue", putType: "invalidValue",
		},
	}
	for _, tc := range tests {
		for _, verb := range []struct{ method, path, scimType, user string }{
			{http.MethodPost, "/scim/v2/Users", tc.postType, tc.postUser},
			{http.MethodPut, "/scim/v2/Users/" + before.ID, tc.putType, kept},
		} {
			t.Run(tc.name+" on "+verb.method, func(t *testing.T) {
				refusals = nil
				rec := scimReq(t, mux, verb.method, verb.path, tc.body)
				var got struct {
					SCIMType string `json:"scimType"`
					Detail   string `json:"detail"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("answer %s: %v", rec.Body, err)
				}
				if rec.Code != http.StatusBadRequest || got.SCIMType != verb.scimType {
					t.Errorf("answered %d %q, want 400 %q", rec.Code, got.SCIMType, verb.scimType)
				}
				if got.Detail != tc.detail {
					t.Errorf("detail\n got %q\nwant %q", got.Detail, tc.detail)
				}
				if len(refusals) != 1 || refusals[0].Detail != tc.detail || refusals[0].SCIMType != verb.scimType {
					t.Errorf("refusals = %+v, want one with the detail and %q", refusals, verb.scimType)
				}
				if len(refusals) == 1 && refusals[0].Username != verb.user {
					t.Errorf("the refusal names user %q, want %q", refusals[0].Username, verb.user)
				}
				if _, err := st.Users().GetByUsername(ctx, fresh); err == nil {
					t.Error("the refused write stored the new user")
				}
				after, err := st.Users().GetByID(ctx, before.ID)
				if err != nil {
					t.Fatal(err)
				}
				if after != before {
					t.Errorf("the refused write changed the user\n got %+v\nwant %+v", after, before)
				}
			})
		}
	}
}
