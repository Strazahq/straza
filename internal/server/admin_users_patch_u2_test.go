package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// sendPatch sends one PATCH of the user id with body and answers the status.
// It reports a failure as an error, because it runs on goroutines that must
// not call t.Fatal.
func sendPatch(base, bearer, id string, body map[string]any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest("PATCH", base+"/v1/admin/users/"+id, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// userUpdateRecords answers the changed lists of the user.update records
// that name the user id, oldest first. It reads more of the outbox than
// adminAuditEvents does, because the concurrency test writes hundreds.
func userUpdateRecords(t *testing.T, app *App, id string) [][]string {
	t.Helper()
	rows, err := app.store.Outbox().ListRecent(context.Background(), 5000)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	var out [][]string
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Subject != "straza.audit.admin" {
			continue
		}
		var ce struct {
			Data struct {
				Action  string   `json:"action"`
				User    string   `json:"user"`
				Changed []string `json:"changed"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(rows[i].CE), &ce); err != nil {
			t.Fatalf("bad CE in outbox: %v", err)
		}
		if ce.Data.Action == actionUserUpdate && ce.Data.User == id {
			out = append(out, ce.Data.Changed)
		}
	}
	return out
}

// TestConcurrentUserPatchesKeepBothFields pins that two PATCH calls on one
// user at the same instant, one of the email and one of the title, never
// undo each other: after each of 150 pairs the row holds both values, and
// each call's user.update record names only the field that call sent.
func TestConcurrentUserPatchesKeepBothFields(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	login := loginDeviceFlow(t, base, "kim", "hunter2!")
	ada := mkHuman(t, app, "ada")
	const pairs = 150
	lost := 0
	for i := range pairs {
		email, title := fmt.Sprintf("ada-%d@x.io", i), fmt.Sprintf("Engineer %d", i)
		bodies := []map[string]any{{"email": email}, {"title": title}}
		start := make(chan struct{})
		codes := make([]int, len(bodies))
		errs := make([]error, len(bodies))
		var wg sync.WaitGroup
		for j, body := range bodies {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[j], errs[j] = sendPatch(base, login, ada.ID, body)
			}()
		}
		close(start)
		wg.Wait()
		for j := range bodies {
			if errs[j] != nil || codes[j] != http.StatusOK {
				t.Fatalf("pair %d: PATCH %v = %d, %v", i, bodies[j], codes[j], errs[j])
			}
		}
		row, err := app.store.Users().GetByID(context.Background(), ada.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Email != email || row.Title != title {
			lost++
			if lost <= 3 {
				t.Errorf("pair %d: the row holds email %q and title %q, want %q and %q", i, row.Email, row.Title, email, title)
			}
		}
	}
	if lost > 0 {
		t.Errorf("%d of %d concurrent PATCH pairs lost a field", lost, pairs)
	}
	named := map[string]int{}
	for _, changed := range userUpdateRecords(t, app, ada.ID) {
		if len(changed) != 1 {
			t.Errorf("a user.update record names %v, want only the field its PATCH sent", changed)
			continue
		}
		named[changed[0]]++
	}
	if named["email"] != pairs || named["title"] != pairs || len(named) != 2 {
		t.Errorf("user.update records name %v, want email and title %d times each", named, pairs)
	}
}

// TestUserPatchRecords pins the user.update record of a PATCH that writes
// only the fields it sent: a PATCH of two fields writes both and one record
// naming both, a typology PATCH stores the normalized values, and a PATCH
// with no field or with the values the row already holds answers the row
// and writes no record.
func TestUserPatchRecords(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	login := loginDeviceFlow(t, base, "kim", "hunter2!")
	cases := []struct {
		name  string
		body  func(u store.User) map[string]any
		after func(u store.User) store.User // the row after the call, from the row before
		want  []string                      // the changed list of the one record, nil for none
	}{
		{name: "a PATCH of two fields writes one record naming both",
			body: func(store.User) map[string]any { return map[string]any{"display": "Ada L", "sponsor": "kim"} },
			after: func(u store.User) store.User {
				u.Display, u.Sponsor = "Ada L", "kim"
				return u
			},
			want: []string{"display", "sponsor"}},
		{name: "a typology PATCH stores the normalized values",
			body: func(store.User) map[string]any {
				return map[string]any{"user_type": " AGENT ", "agency_mode": "Supervised"}
			},
			after: func(u store.User) store.User {
				u.UserType, u.AgencyMode = store.UserTypeAgent, store.AgencySupervised
				return u
			},
			want: []string{"user_type", "agency_mode"}},
		{name: "a PATCH with no field writes no record",
			body:  func(store.User) map[string]any { return map[string]any{} },
			after: func(u store.User) store.User { return u }},
		{name: "a PATCH of the current values writes no record",
			body: func(u store.User) map[string]any {
				return map[string]any{"display": "", "sponsor": "", "email": u.Email}
			},
			after: func(u store.User) store.User { return u }},
	}
	for i, tc := range cases {
		ada := mkHuman(t, app, fmt.Sprintf("ada%d", i))
		var resp map[string]any
		if code := adminReq(t, "PATCH", base+"/v1/admin/users/"+ada.ID, login, tc.body(ada), &resp); code != http.StatusOK {
			t.Fatalf("%s: PATCH = %d (%v)", tc.name, code, resp)
		}
		if resp["id"] != ada.ID || resp["email"] != ada.Email {
			t.Errorf("%s: the answer %v is not the user's row", tc.name, resp)
		}
		row, err := app.store.Users().GetByID(context.Background(), ada.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := tc.after(ada)
		if tc.want == nil && !row.UpdatedAt.Equal(ada.UpdatedAt) {
			t.Errorf("%s: updated_at moved from %v to %v with no record", tc.name, ada.UpdatedAt, row.UpdatedAt)
		}
		want.CreatedAt, want.UpdatedAt = row.CreatedAt, row.UpdatedAt
		if row != want {
			t.Errorf("%s: the row after the call\n got %+v\nwant %+v", tc.name, row, want)
		}
		records := userUpdateRecords(t, app, ada.ID)
		switch {
		case tc.want == nil && len(records) != 0:
			t.Errorf("%s: wrote records %v, want none", tc.name, records)
		case tc.want != nil && (len(records) != 1 || !slices.Equal(records[0], tc.want)):
			t.Errorf("%s: wrote records %v, want one naming %v", tc.name, records, tc.want)
		}
	}
}

// patchGate orders two PATCH calls on one user: both read the row before
// either writes, and the write first names runs before the other one.
type patchGate struct {
	mu        sync.Mutex
	target    string
	reads     int
	bothRead  chan struct{}
	first     func(f store.UserFields) bool
	firstDone chan struct{}
}

// wait blocks on ch for a few seconds and reports whether it closed.
func (g *patchGate) wait(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

type gateStore struct {
	store.Store
	g *patchGate
}

func (s *gateStore) Users() store.UserRepo { return gateUsers{s.Store.Users(), s.g} }

type gateUsers struct {
	store.UserRepo
	g *patchGate
}

func (r gateUsers) GetByID(ctx context.Context, id string) (store.User, error) {
	u, err := r.UserRepo.GetByID(ctx, id)
	r.g.mu.Lock()
	if id == r.g.target && r.g.reads < 2 {
		if r.g.reads++; r.g.reads == 2 {
			close(r.g.bothRead)
		}
	}
	r.g.mu.Unlock()
	return u, err
}

func (r gateUsers) UpdateFields(ctx context.Context, id string, f store.UserFields) (store.User, error) {
	r.g.mu.Lock()
	gated, first, bothRead, firstDone := id == r.g.target, r.g.first, r.g.bothRead, r.g.firstDone
	r.g.mu.Unlock()
	if !gated {
		return r.UserRepo.UpdateFields(ctx, id, f)
	}
	if !r.g.wait(bothRead) {
		return store.User{}, fmt.Errorf("the two PATCH calls never both read the row")
	}
	if first(f) {
		defer close(firstDone)
	} else if !r.g.wait(firstDone) {
		return store.User{}, fmt.Errorf("the first write never ran")
	}
	return r.UserRepo.UpdateFields(ctx, id, f)
}

// patchDirect runs the PATCH handler as kim for the user id and answers the
// status. It calls no t method, because it runs on goroutines.
func patchDirect(app *App, id string, body map[string]any) int {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("PATCH", "/v1/admin/users/"+id, bytes.NewReader(raw))
	r.SetPathValue("id", id)
	r = r.WithContext(withActor(r.Context(), auditActor{Name: "kim", ID: "kim-id", Via: "login"}))
	w := httptest.NewRecorder()
	app.handleUsersUpdate(w, r)
	return w.Code
}

// TestStalePatchNeverRestoresAValue pins the PATCH of a value the request
// read, which another PATCH changed before this one wrote: both calls read
// the row first, then the change is written, then the stale call. The stale
// call writes nothing it did not change, so the row keeps the change and the
// one record of it, as a disable does against a PATCH of the active status
// it read. Two PATCH calls of different fields keep both (positive control).
func TestStalePatchNeverRestoresAValue(t *testing.T) {
	t.Parallel()
	g := &patchGate{}
	app, _ := testAppPreRun(t, []func(*App){func(a *App) { a.store = &gateStore{Store: a.store, g: g} }})
	cases := []struct {
		name         string
		stale, first map[string]any
		isFirst      func(f store.UserFields) bool
		row          func(u store.User) bool
		want         [][]string // the user.update records, in any order
	}{
		{name: "a disable and a PATCH of the active status it read end disabled with one record",
			stale: map[string]any{"status": store.UserActive}, first: map[string]any{"status": store.UserDisabled},
			isFirst: func(f store.UserFields) bool { return f.Status != nil && *f.Status == store.UserDisabled },
			row:     func(u store.User) bool { return u.Status == store.UserDisabled },
			want:    [][]string{{"status"}}},
		{name: "a PATCH of the email it read after another PATCH changed it keeps the change",
			stale: map[string]any{"email": "x@x.io"}, first: map[string]any{"email": "y@x.io"},
			isFirst: func(f store.UserFields) bool { return f.Email != nil && *f.Email == "y@x.io" },
			row:     func(u store.User) bool { return u.Email == "y@x.io" },
			want:    [][]string{{"email"}}},
		{name: "two PATCH calls of different fields keep both",
			stale: map[string]any{"email": "e1@x.io"}, first: map[string]any{"title": "T1"},
			isFirst: func(f store.UserFields) bool { return f.Title != nil },
			row:     func(u store.User) bool { return u.Email == "e1@x.io" && u.Title == "T1" },
			want:    [][]string{{"email"}, {"title"}}},
	}
	for i, tc := range cases {
		u, err := app.store.Users().Create(context.Background(), store.User{Username: fmt.Sprintf("stale%d", i), Email: "x@x.io", Title: "T0"})
		if err != nil {
			t.Fatal(err)
		}
		g.mu.Lock()
		g.target, g.reads, g.bothRead, g.first, g.firstDone = u.ID, 0, make(chan struct{}), tc.isFirst, make(chan struct{})
		g.mu.Unlock()
		codes := make([]int, 2)
		var wg sync.WaitGroup
		for j, body := range []map[string]any{tc.stale, tc.first} {
			wg.Add(1)
			go func() { defer wg.Done(); codes[j] = patchDirect(app, u.ID, body) }()
		}
		wg.Wait()
		g.mu.Lock()
		g.target = ""
		g.mu.Unlock()
		if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
			t.Fatalf("%s: PATCH statuses %v, want 200 twice", tc.name, codes)
		}
		row, err := app.store.Users().GetByID(context.Background(), u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !tc.row(row) {
			t.Errorf("%s: the row holds email %q, title %q, status %q", tc.name, row.Email, row.Title, row.Status)
		}
		records := userUpdateRecords(t, app, u.ID)
		matched := len(records) == len(tc.want)
		for _, w := range tc.want {
			matched = matched && slices.ContainsFunc(records, func(r []string) bool { return slices.Equal(r, w) })
		}
		if !matched {
			t.Errorf("%s: records %v, want %v", tc.name, records, tc.want)
		}
	}
}
