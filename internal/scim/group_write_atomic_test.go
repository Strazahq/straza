package scim

// Atomic Group writes (RFC 7644 §3.5.2): a PATCH or PUT is worked out in
// memory against the holders it starts from and stored in one write, so a
// refused operation or a failed write leaves the group as it was, with no
// audit record and no identity event, and a write that lands reports one
// record per row that really changed.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// atomicGroupServer builds a Server over a fresh sqlite store, optionally
// wrapped, and captures the identity events and membership changes it
// reports under a lock, so concurrent requests can share it.
type atomicGroupServer struct {
	st      store.Store
	mux     *http.ServeMux
	mu      sync.Mutex
	emitted []string
	changes []MembershipChange
}

func newAtomicGroupServer(t *testing.T, wrap func(store.Store) store.Store) *atomicGroupServer {
	t.Helper()
	g := &atomicGroupServer{st: newPlanesTestStore(t)}
	deps := g.st
	if wrap != nil {
		deps = wrap(g.st)
	}
	s := New(Deps{
		Store:        deps,
		Authenticate: func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
		Deactivate:   func(context.Context, string, string) {},
		Reactivate:   func(context.Context, string) {},
		IdentityChanged: func(_ context.Context, _ string, id string) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.emitted = append(g.emitted, id)
		},
		MembershipChanged: func(_ context.Context, c MembershipChange) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.changes = append(g.changes, c)
		},
		ProtectedUsername: "break-glass",
	})
	g.mux = http.NewServeMux()
	s.Routes(g.mux)
	return g
}

// reported returns copies of the identity events and membership changes so
// far.
func (g *atomicGroupServer) reported() ([]string, []MembershipChange) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.emitted...), append([]MembershipChange(nil), g.changes...)
}

// holders lists the user ids holding the role, sorted.
func (g *atomicGroupServer) holders(t *testing.T, roleID string) []string {
	t.Helper()
	asg, err := g.st.Roles().AssignmentsByRole(context.Background(), roleID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(asg))
	for _, a := range asg {
		out = append(out, a.SubjectID)
	}
	sort.Strings(out)
	return out
}

func patchOps(ops string) string {
	return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":` + ops + `}`
}

func addOp(ids ...string) string {
	vals := make([]string, 0, len(ids))
	for _, id := range ids {
		vals = append(vals, `{"value":"`+id+`"}`)
	}
	return `{"op":"add","path":"members","value":[` + strings.Join(vals, ",") + `]}`
}

func removeOp(id string) string {
	return `{"op":"remove","path":"members[value eq \"` + id + `\"]"}`
}

// scimErrorType reads the scimType of a SCIM error body ("" when absent).
func scimErrorType(t *testing.T, body string) string {
	t.Helper()
	var e struct {
		ScimType string `json:"scimType"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("error body %q: %v", body, err)
	}
	return e.ScimType
}

// TestGroupPatchRefusalWritesNothing pins that a PATCH with one refused
// operation changes nothing, however many operations before it would have:
// no holder added or removed, no membership change reported, no identity
// event. The status and scimType are the ones the refused operation gets.
func TestGroupPatchRefusalWritesNothing(t *testing.T) {
	cases := []struct {
		name     string
		kimHolds bool
		ops      func(bob, kim string) string
		scimType string
	}{
		{"an add followed by an unknown id", false,
			func(bob, _ string) string { return "[" + addOp(bob) + "," + addOp("nope") + "]" }, "invalidValue"},
		{"an add followed by a displayName replace", false,
			func(bob, _ string) string {
				return "[" + addOp(bob) + `,{"op":"replace","path":"displayName","value":"renamed"}]`
			}, "mutability"},
		{"a remove of a holder followed by an unknown id", true,
			func(_, kim string) string { return "[" + removeOp(kim) + "," + addOp("nope") + "]" }, "invalidValue"},
		{"an add followed by a write to the read-only projection", false,
			func(bob, _ string) string {
				return "[" + addOp(bob) + `,{"op":"replace","path":"` + strazaGroupURNLower + `:role","value":"x"}]`
			}, "mutability"},
		{"an add followed by a value filter with add", false,
			func(bob, kim string) string {
				return "[" + addOp(bob) + `,{"op":"add","path":"members[value eq \"` + kim + `\"]"}]`
			}, "invalidValue"},
		{"an add followed by an unmapped path", false,
			func(bob, _ string) string { return "[" + addOp(bob) + `,{"op":"replace","path":"owner","value":"x"}]` }, "invalidValue"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := newAtomicGroupServer(t, nil)
			dev, _ := g.st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
			bob, _ := g.st.Users().Create(ctx, store.User{Username: "bob"})
			kim, _ := g.st.Users().Create(ctx, store.User{Username: "kim"})
			if tc.kimHolds {
				if _, err := g.st.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: kim.ID, RoleID: dev.ID}); err != nil {
					t.Fatal(err)
				}
			}
			before := g.holders(t, dev.ID)

			rec := scimReq(t, g.mux, "PATCH", "/scim/v2/Groups/"+dev.ID, patchOps(tc.ops(bob.ID, kim.ID)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PATCH = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if got := scimErrorType(t, rec.Body.String()); got != tc.scimType {
				t.Errorf("scimType = %q, want %q", got, tc.scimType)
			}
			if after := g.holders(t, dev.ID); strings.Join(after, ",") != strings.Join(before, ",") {
				t.Errorf("holders = %v after a refused PATCH, want %v unchanged", after, before)
			}
			if emitted, changes := g.reported(); len(emitted) != 0 || len(changes) != 0 {
				t.Errorf("a refused PATCH reported identity events %v and changes %+v, want none", emitted, changes)
			}
		})
	}
}

// TestGroupPatchNoPathIsDeterministic pins that the no-path form answers
// the same every time. Its keys are worked out in sorted order and nothing
// is written unless every key is accepted, so a displayName beside members
// is refused and the members are never written.
func TestGroupPatchNoPathIsDeterministic(t *testing.T) {
	ctx := context.Background()
	g := newAtomicGroupServer(t, nil)
	dev, _ := g.st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
	bob, _ := g.st.Users().Create(ctx, store.User{Username: "bob"})
	body := patchOps(`[{"op":"replace","value":{"members":[{"value":"` + bob.ID + `"}],"displayName":"renamed"}}]`)

	var first string
	for i := 0; i < 20; i++ {
		rec := scimReq(t, g.mux, "PATCH", "/scim/v2/Groups/"+dev.ID, body)
		if rec.Code != http.StatusBadRequest || scimErrorType(t, rec.Body.String()) != "mutability" {
			t.Fatalf("run %d = %d: %s, want 400 mutability", i, rec.Code, rec.Body.String())
		}
		if i == 0 {
			first = rec.Body.String()
		} else if rec.Body.String() != first {
			t.Fatalf("run %d answered %s, run 0 answered %s", i, rec.Body.String(), first)
		}
		if h := g.holders(t, dev.ID); len(h) != 0 {
			t.Fatalf("run %d wrote holders %v", i, h)
		}
	}
	if emitted, changes := g.reported(); len(emitted) != 0 || len(changes) != 0 {
		t.Errorf("refused no-path PATCHes reported identity events %v and changes %+v, want none", emitted, changes)
	}
}

// TestGroupPatchReportsTheNetChange pins what a PATCH that lands reports:
// one change per row that really changed, in the order the operations
// produced them, the role's identity event once and each affected user's
// once. An add and a remove of the same member cancel out.
func TestGroupPatchReportsTheNetChange(t *testing.T) {
	type change struct{ who, action string }
	cases := []struct {
		name        string
		samHolds    bool
		ops         func(bob, kim, sam string) string
		wantHolders func(bob, kim, sam string) []string
		want        func(bob, kim, sam string) []change
	}{
		{"an add and a remove of the same member cancel out", false,
			func(bob, _, _ string) string { return "[" + addOp(bob) + "," + removeOp(bob) + "]" },
			func(_, _, _ string) []string { return nil },
			func(_, _, _ string) []change { return nil }},
		{"two adds and a remove report three changes in operation order", true,
			func(bob, kim, sam string) string { return "[" + addOp(bob, kim) + "," + removeOp(sam) + "]" },
			func(bob, kim, _ string) []string { return []string{bob, kim} },
			func(bob, kim, sam string) []change {
				return []change{{bob, MembershipAssign}, {kim, MembershipAssign}, {sam, MembershipUnassign}}
			}},
		{"a value-filter remove of a non-holder reports nothing", false,
			func(bob, _, _ string) string { return "[" + removeOp(bob) + "]" },
			func(_, _, _ string) []string { return nil },
			func(_, _, _ string) []change { return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := newAtomicGroupServer(t, nil)
			dev, _ := g.st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
			bob, _ := g.st.Users().Create(ctx, store.User{Username: "bob"})
			kim, _ := g.st.Users().Create(ctx, store.User{Username: "kim"})
			sam, _ := g.st.Users().Create(ctx, store.User{Username: "sam"})
			if tc.samHolds {
				if _, err := g.st.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: sam.ID, RoleID: dev.ID, Origin: store.OriginSCIM}); err != nil {
					t.Fatal(err)
				}
			}

			rec := scimReq(t, g.mux, "PATCH", "/scim/v2/Groups/"+dev.ID, patchOps(tc.ops(bob.ID, kim.ID, sam.ID)))
			if rec.Code != http.StatusOK {
				t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body.String())
			}
			wantHolders := tc.wantHolders(bob.ID, kim.ID, sam.ID)
			sort.Strings(wantHolders)
			if got := g.holders(t, dev.ID); strings.Join(got, ",") != strings.Join(wantHolders, ",") {
				t.Errorf("holders = %v, want %v", got, wantHolders)
			}
			emitted, changes := g.reported()
			want := tc.want(bob.ID, kim.ID, sam.ID)
			if len(changes) != len(want) {
				t.Fatalf("changes = %+v, want %v", changes, want)
			}
			wantEmitted := []string{dev.ID}
			for i, w := range want {
				c := changes[i]
				if c.UserID != w.who || c.Action != w.action || c.RoleID != dev.ID || c.AssignmentID == "" {
					t.Errorf("change[%d] = %+v, want %s %s of %s", i, c, w.who, w.action, dev.ID)
				}
				wantEmitted = append(wantEmitted, w.who)
			}
			if strings.Join(emitted, ",") != strings.Join(wantEmitted, ",") {
				t.Errorf("identity events = %v, want %v (the role once, then each affected user once)", emitted, wantEmitted)
			}
		})
	}
}

// failingApplyStore fails the one write a Group change makes, so the tests
// can prove a failed write leaves the group as it was.
type failingApplyStore struct{ store.Store }

func (f failingApplyStore) Roles() store.RoleRepo { return failingApplyRoles{f.Store.Roles()} }

type failingApplyRoles struct{ store.RoleRepo }

func (failingApplyRoles) ApplyMembership(context.Context, string, []store.RoleAssignment, []string) ([]store.RoleAssignment, []store.RoleAssignment, error) {
	return nil, nil, errors.New("injected: the database went away")
}

// TestGroupWriteFailureWritesNothing pins that a Group write whose store
// write fails answers 500 with the sentence, changes no holder and reports
// nothing, for PATCH and for PUT alike.
func TestGroupWriteFailureWritesNothing(t *testing.T) {
	cases := []struct {
		name, method string
		body         func(bob, kim string) string
	}{
		{"PATCH", "PATCH", func(bob, kim string) string { return patchOps("[" + addOp(bob) + "," + removeOp(kim) + "]") }},
		{"PUT", "PUT", func(bob, _ string) string {
			return `{"schemas":["` + SchemaGroup + `"],"displayName":"dev","members":[{"value":"` + bob + `"}]}`
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := newAtomicGroupServer(t, func(st store.Store) store.Store { return failingApplyStore{st} })
			dev, _ := g.st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
			bob, _ := g.st.Users().Create(ctx, store.User{Username: "bob"})
			kim, _ := g.st.Users().Create(ctx, store.User{Username: "kim"})
			if _, err := g.st.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: kim.ID, RoleID: dev.ID, Origin: store.OriginSCIM}); err != nil {
				t.Fatal(err)
			}

			rec := scimReq(t, g.mux, tc.method, "/scim/v2/Groups/"+dev.ID, tc.body(bob.ID, kim.ID))
			const want = "the membership change to group dev was not applied, because writing it failed, so nothing changed. Retry the request"
			if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("%s = %d: %s, want 500 saying %q", tc.method, rec.Code, rec.Body.String(), want)
			}
			if got := g.holders(t, dev.ID); len(got) != 1 || got[0] != kim.ID {
				t.Errorf("holders = %v after a failed write, want kim alone", got)
			}
			if emitted, changes := g.reported(); len(emitted) != 0 || len(changes) != 0 {
				t.Errorf("a failed write reported identity events %v and changes %+v, want none", emitted, changes)
			}
		})
	}
}

// meetStore holds the first two lookups of one user id until both have
// arrived, so two requests are sure to have read the group before either
// writes.
type meetStore struct {
	store.Store
	userID string
	mu     sync.Mutex
	n      int
	both   chan struct{}
}

func (m *meetStore) Users() store.UserRepo { return meetUsers{m.Store.Users(), m} }

type meetUsers struct {
	store.UserRepo
	m *meetStore
}

func (u meetUsers) GetByID(ctx context.Context, id string) (store.User, error) {
	if id == u.m.userID {
		u.m.mu.Lock()
		u.m.n++
		if u.m.n == 2 {
			close(u.m.both)
		}
		u.m.mu.Unlock()
		select {
		case <-u.m.both:
		case <-time.After(3 * time.Second):
		}
	}
	return u.UserRepo.GetByID(ctx, id)
}

// TestGroupConcurrentAddsRecordOnce pins that two requests adding the same
// member at the same moment both succeed and the member's assignment is
// reported once: the second write finds the row already there and skips it
// instead of failing.
func TestGroupConcurrentAddsRecordOnce(t *testing.T) {
	ctx := context.Background()
	var meet *meetStore
	g := newAtomicGroupServer(t, func(st store.Store) store.Store {
		meet = &meetStore{Store: st, both: make(chan struct{})}
		return meet
	})
	dev, _ := g.st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
	bob, _ := g.st.Users().Create(ctx, store.User{Username: "bob"})
	meet.userID = bob.ID

	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = scimReq(t, g.mux, "PATCH", "/scim/v2/Groups/"+dev.ID, patchOps("["+addOp(bob.ID)+"]")).Code
		}(i)
	}
	wg.Wait()
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("concurrent adds answered %v, want both 200", codes)
	}
	if got := g.holders(t, dev.ID); len(got) != 1 || got[0] != bob.ID {
		t.Errorf("holders = %v, want bob once", got)
	}
	if _, changes := g.reported(); len(changes) != 1 || changes[0].UserID != bob.ID || changes[0].Action != MembershipAssign {
		t.Errorf("changes = %+v, want exactly one assign of bob", changes)
	}
}
