package scim

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// userWrite is one call a test captured from Deps.UserWritten.
type userWrite struct {
	action  string
	id      string
	changed []string
}

// userWrittenServer builds a Server over st whose UserWritten captures
// every call, and answers its mux and the captured calls.
func userWrittenServer(t *testing.T, st store.Store) (*http.ServeMux, *[]userWrite) {
	t.Helper()
	var writes []userWrite
	s := New(Deps{
		Store:           st,
		Authenticate:    func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
		Deactivate:      func(context.Context, string, string) {},
		Reactivate:      func(context.Context, string) {},
		IdentityChanged: func(context.Context, string, string) {},
		UserWritten: func(_ context.Context, action string, u store.User, changed []string) {
			writes = append(writes, userWrite{action, u.ID, changed})
		},
	})
	mux := http.NewServeMux()
	s.Routes(mux)
	return mux, &writes
}

// seedSCIMUser stores a SCIM-born user with every writable field set.
func seedSCIMUser(t *testing.T, st store.Store, username string) store.User {
	t.Helper()
	u, err := st.Users().Create(context.Background(), store.User{
		Username: username, ExternalID: "idm-" + username, Email: username + "@x.io",
		Display: "Ada", Title: "Engineer", Status: store.UserActive, Origin: store.OriginSCIM,
		UserType: store.UserTypeHuman, AgencyMode: store.AgencyInteractive, Sponsor: "kim", SwarmID: "s-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestUserPatchReportsChangedFieldNames pins the changed list a SCIM PATCH
// reports: one admin API field name per field whose stored value changed,
// in one fixed order, and no report for a PATCH that changes nothing.
func TestUserPatchReportsChangedFieldNames(t *testing.T) {
	st := newPlanesTestStore(t)
	mux, writes := userWrittenServer(t, st)
	for _, tc := range []struct {
		name string
		ops  string
		want []string
	}{
		{"nothing", `{"op":"replace","path":"title","value":"Engineer"}`, nil},
		{"username", `{"op":"replace","path":"userName","value":"renamed"}`, []string{"username"}},
		{"external_id", `{"op":"replace","path":"externalId","value":"idm-2"}`, []string{"external_id"}},
		{"email", `{"op":"replace","path":"emails","value":[{"value":"new@x.io","primary":true}]}`, []string{"email"}},
		{"display", `{"op":"replace","path":"displayName","value":"Ada L"}`, []string{"display"}},
		{"title", `{"op":"remove","path":"title"}`, []string{"title"}},
		{"status", `{"op":"replace","path":"active","value":false}`, []string{"status"}},
		{"user_type", `{"op":"replace","path":"userType","value":"agent"}`, []string{"user_type"}},
		{"agency_mode", `{"op":"replace","path":"agencyMode","value":"autonomous"}`, []string{"agency_mode"}},
		{"sponsor", `{"op":"replace","path":"sponsor","value":"lin"}`, []string{"sponsor"}},
		{"swarm_id", `{"op":"remove","path":"swarmId"}`, []string{"swarm_id"}},
		{"ephemeral", `{"op":"replace","path":"ephemeral","value":true}`, []string{"ephemeral"}},
		{"several in the fixed order",
			`{"op":"replace","path":"sponsor","value":""},{"op":"replace","path":"active","value":false},{"op":"replace","path":"userName","value":"renamed-too"}`,
			[]string{"username", "status", "sponsor"}},
	} {
		u := seedSCIMUser(t, st, "u-"+tc.name)
		*writes = nil
		rec := scimReq(t, mux, http.MethodPatch, "/scim/v2/Users/"+u.ID,
			`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[`+tc.ops+`]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: PATCH = %d %s", tc.name, rec.Code, rec.Body)
		}
		if tc.want == nil {
			if len(*writes) != 0 {
				t.Errorf("%s: reported %v, want nothing", tc.name, *writes)
			}
			continue
		}
		if len(*writes) != 1 || (*writes)[0].action != UserUpdated || (*writes)[0].id != u.ID || !slices.Equal((*writes)[0].changed, tc.want) {
			t.Errorf("%s: reported %v, want one %s of %s with changed %v", tc.name, *writes, UserUpdated, u.ID, tc.want)
		}
	}
}

// statusFailStore fails every user Update that writes the disabled
// status, so a write's field write lands and its status write does not.
type statusFailStore struct{ store.Store }

func (s statusFailStore) Users() store.UserRepo { return statusFailUsers{s.Store.Users()} }

type statusFailUsers struct{ store.UserRepo }

func (u statusFailUsers) Update(ctx context.Context, row store.User) (store.User, error) {
	if row.Status == store.UserDisabled {
		return store.User{}, errors.New("the store refused the status write")
	}
	return u.UserRepo.Update(ctx, row)
}

// sponsorRaceStore lands another writer's sponsor change between each user
// write and its read-back, so the row read back differs from the row the
// handler wrote.
type sponsorRaceStore struct{ store.Store }

func (s sponsorRaceStore) Users() store.UserRepo { return sponsorRaceUsers{s.Store.Users()} }

type sponsorRaceUsers struct{ store.UserRepo }

func (u sponsorRaceUsers) Update(ctx context.Context, row store.User) (store.User, error) {
	if _, err := u.UserRepo.Update(ctx, row); err != nil {
		return store.User{}, err
	}
	row.Sponsor = "another-writer"
	return u.UserRepo.Update(ctx, row)
}

// titleWrites are a PATCH and a PUT that change only the title of a user
// seedSCIMUser stored, with active set to the given value.
func titleWrites(active string) []struct {
	method string
	body   func(u store.User) string
} {
	return []struct {
		method string
		body   func(u store.User) string
	}{
		{http.MethodPatch, func(store.User) string {
			return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[` +
				`{"op":"replace","path":"title","value":"Staff engineer"},{"op":"replace","path":"active","value":` + active + `}]}`
		}},
		{http.MethodPut, func(u store.User) string {
			return `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"` + u.Username +
				`","externalId":"` + u.ExternalID + `","displayName":"` + u.Display + `","title":"Staff engineer",` +
				`"emails":[{"value":"` + u.Email + `","primary":true}],"active":` + active + `}`
		}},
	}
}

// TestUserWriteReportsStoredFieldsWhenStatusWriteFails pins that a PATCH or
// a PUT whose field write lands and whose status write fails answers 500
// and still reports the fields it stored, and not the status it did not.
func TestUserWriteReportsStoredFieldsWhenStatusWriteFails(t *testing.T) {
	st := newPlanesTestStore(t)
	mux, writes := userWrittenServer(t, statusFailStore{st})
	for _, w := range titleWrites("false") {
		u := seedSCIMUser(t, st, "fail-"+strings.ToLower(w.method))
		*writes = nil
		rec := scimReq(t, mux, w.method, "/scim/v2/Users/"+u.ID, w.body(u))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s = %d %s, want 500", w.method, rec.Code, rec.Body)
		}
		stored, err := st.Users().GetByID(context.Background(), u.ID)
		if err != nil || stored.Title != "Staff engineer" || stored.Status != store.UserActive {
			t.Fatalf("%s: stored row = %+v err %v, want the new title and still active", w.method, stored, err)
		}
		if len(*writes) != 1 || (*writes)[0].action != UserUpdated || (*writes)[0].id != u.ID || !slices.Equal((*writes)[0].changed, []string{"title"}) {
			t.Errorf("%s: reported %v, want one %s of %s with changed [title]", w.method, *writes, UserUpdated, u.ID)
		}
	}
}

// TestUserWriteReportsTheWrittenRowNotTheReadBack pins that a PATCH or a
// PUT reports the fields of the row it wrote against the row it read: a
// sponsor another writer changed between the write and its read-back is
// not this write's change, so it never enters the report.
func TestUserWriteReportsTheWrittenRowNotTheReadBack(t *testing.T) {
	st := newPlanesTestStore(t)
	mux, writes := userWrittenServer(t, sponsorRaceStore{st})
	for _, w := range titleWrites("true") {
		u := seedSCIMUser(t, st, "race-"+strings.ToLower(w.method))
		*writes = nil
		rec := scimReq(t, mux, w.method, "/scim/v2/Users/"+u.ID, w.body(u))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s, want 200", w.method, rec.Code, rec.Body)
		}
		if stored, err := st.Users().GetByID(context.Background(), u.ID); err != nil || stored.Sponsor != "another-writer" {
			t.Fatalf("%s: stored sponsor = %q err %v, want the other writer's value, or the test proves nothing", w.method, stored.Sponsor, err)
		}
		if len(*writes) != 1 || (*writes)[0].action != UserUpdated || (*writes)[0].id != u.ID || !slices.Equal((*writes)[0].changed, []string{"title"}) {
			t.Errorf("%s: reported %v, want one %s of %s with changed [title] and never the other writer's sponsor", w.method, *writes, UserUpdated, u.ID)
		}
	}
}
