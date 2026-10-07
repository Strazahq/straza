package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestRolesUpdateAndKindValidation pins PATCH /v1/admin/roles/{id} (0.58.0)
// and the kind guard on create: a bad kind is a 400 naming the enum, never
// the DB CHECK surfacing as a 500.
func TestRolesUpdateAndKindValidation(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	dev, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if code := adminReq(t, "POST", base+"/v1/admin/roles", tok,
		map[string]string{"name": "badkind", "kind": "banana"}, &body); code != http.StatusBadRequest {
		t.Fatalf("create with bad kind = %d (%v), want 400", code, body)
	}

	patches := []struct {
		name string
		id   string
		body map[string]any
		want int
	}{
		{"unknown role", "nope", map[string]any{"description": "x"}, http.StatusNotFound},
		{"bad kind", dev.ID, map[string]any{"kind": "banana"}, http.StatusBadRequest},
		{"explicit empty kind", dev.ID, map[string]any{"kind": ""}, http.StatusBadRequest},
		{"no fields", dev.ID, map[string]any{}, http.StatusBadRequest},
	}
	for _, tc := range patches {
		t.Run(tc.name, func(t *testing.T) {
			var out map[string]any
			if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+tc.id, tok, tc.body, &out); code != tc.want {
				t.Errorf("PATCH = %d (%v), want %d", code, out, tc.want)
			}
		})
	}

	// Description-only: kind and name survive, assigned_count rides along
	// (dev holds one assignment from seedIdentity).
	var updated struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		Kind          string `json:"kind"`
		AssignedCount int    `json:"assigned_count"`
	}
	if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+dev.ID, tok,
		map[string]any{"description": "developer seat"}, &updated); code != http.StatusOK {
		t.Fatalf("PATCH description = %d, want 200", code)
	}
	if updated.Name != "dev" || updated.Description != "developer seat" || updated.Kind != "application" {
		t.Errorf("after description patch = %+v, want name dev, kind application kept", updated)
	}
	if updated.AssignedCount != 1 {
		t.Errorf("assigned_count = %d, want 1", updated.AssignedCount)
	}

	// Kind-only: the fresh description survives (PATCH absent = keep).
	if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+dev.ID, tok,
		map[string]any{"kind": "application"}, &updated); code != http.StatusOK {
		t.Fatalf("PATCH kind = %d, want 200", code)
	}
	if updated.Kind != "application" || updated.Description != "developer seat" {
		t.Errorf("after kind patch = %+v, want kind application and description kept", updated)
	}
}

// TestImplicationsReadDeleteResolverEffect pins the implications read/delete
// (0.58.0) and the part that matters: removing an edge changes ROLE
// RESOLUTION, proven over the wire via the paged users list's
// effective_roles (kim holds dev, dev⇒reader from seedIdentity).
func TestImplicationsReadDeleteResolverEffect(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	dev, _ := app.store.Roles().GetByName(ctx, "dev")
	reader, _ := app.store.Roles().GetByName(ctx, "reader")

	if code := adminReq(t, "GET", base+"/v1/admin/roles/nope/implications", tok, nil, nil); code != http.StatusNotFound {
		t.Errorf("implications of unknown role = %d, want 404", code)
	}

	var edges []struct {
		ID          string `json:"id"`
		ImpliesID   string `json:"implies_id"`
		ImpliesName string `json:"implies_name"`
	}
	if code := adminReq(t, "GET", base+"/v1/admin/roles/"+dev.ID+"/implications", tok, nil, &edges); code != http.StatusOK {
		t.Fatalf("implications list = %d, want 200", code)
	}
	if len(edges) != 1 || edges[0].ID != reader.ID || edges[0].ImpliesID != reader.ID || edges[0].ImpliesName != "reader" {
		t.Fatalf("implications = %+v, want one edge to reader (id = implied role id)", edges)
	}

	effective := func() []string {
		var page struct {
			Items []struct {
				Username       string   `json:"username"`
				EffectiveRoles []string `json:"effective_roles"`
			} `json:"items"`
		}
		if code := adminReq(t, "GET", base+"/v1/admin/users?limit=50", tok, nil, &page); code != http.StatusOK {
			t.Fatalf("users page = %d", code)
		}
		for _, it := range page.Items {
			if it.Username == user.Username {
				return it.EffectiveRoles
			}
		}
		t.Fatalf("user %s not in page", user.Username)
		return nil
	}
	has := func(roles []string, want string) bool {
		for _, r := range roles {
			if r == want {
				return true
			}
		}
		return false
	}
	if pre := effective(); !has(pre, "reader") {
		t.Fatalf("pre-delete effective roles = %v, want reader via implication", pre)
	}

	if code := adminReq(t, "DELETE", base+"/v1/admin/roles/"+dev.ID+"/implications/"+reader.ID, tok, nil, nil); code != http.StatusOK {
		t.Fatalf("implication delete = %d, want 200", code)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/roles/"+dev.ID+"/implications/"+reader.ID, tok, nil, nil); code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", code)
	}
	if code := adminReq(t, "GET", base+"/v1/admin/roles/"+dev.ID+"/implications", tok, nil, &edges); code != http.StatusOK || len(edges) != 0 {
		t.Errorf("post-delete implications = %d %v, want 200 empty", code, edges)
	}
	post := effective()
	if has(post, "reader") {
		t.Errorf("post-delete effective roles = %v: resolver still grants reader (bump missing?)", post)
	}
	if !has(post, "dev") {
		t.Errorf("post-delete effective roles = %v, want dev kept", post)
	}
}

// TestUsersListLocksBatched pins the paged users list's locks enrichment
// (0.58.0): server truth for the row the console renders, empty array when
// unlocked, and both lock lanes visible on a locked user.
func TestUsersListLocksBatched(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	other, err := app.store.Users().Create(ctx, store.User{Username: "lockee"})
	if err != nil {
		t.Fatal(err)
	}

	// Raw map decode: the contract is the key existing as an array, not a
	// Go zero value appearing after decode.
	locksOf := func(username string) []any {
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if code := adminReq(t, "GET", base+"/v1/admin/users?limit=50", tok, nil, &page); code != http.StatusOK {
			t.Fatalf("users page = %d", code)
		}
		for _, it := range page.Items {
			if it["username"] == username {
				raw, ok := it["locks"]
				if !ok {
					t.Fatalf("row %s has no locks key", username)
				}
				arr, ok := raw.([]any)
				if !ok {
					t.Fatalf("locks is %T, want array", raw)
				}
				return arr
			}
		}
		t.Fatalf("user %s not in page", username)
		return nil
	}

	if pre := locksOf("lockee"); len(pre) != 0 {
		t.Errorf("unlocked user locks = %v, want empty array", pre)
	}

	if code := adminReq(t, "POST", base+"/v1/admin/users/"+other.ID+"/lock", tok,
		map[string]string{"reason": "compromise drill"}, nil); code != http.StatusOK {
		t.Fatalf("lock = %d, want 200", code)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/users/"+other.ID+"/lock", tok,
		map[string]string{"reason": "soar alert", "origin": "external"}, nil); code != http.StatusOK {
		t.Fatalf("external lock = %d, want 200", code)
	}

	locks := locksOf("lockee")
	if len(locks) != 2 {
		t.Fatalf("locked user locks = %v, want both lanes", locks)
	}
	first, _ := locks[0].(map[string]any)
	if first["origin"] != "admin" || first["reason"] != "compromise drill" {
		t.Errorf("first lock = %v, want admin lane with its reason (created_at order)", first)
	}
	if admin := locksOf(user.Username); len(admin) != 0 {
		t.Errorf("admin's own locks = %v, want empty", admin)
	}
}

// TestPacksBindingsAndUnbind pins the packs list's bindings enrichment and
// the unbind DELETE (0.58.0): each binding's id is the role id (the edge
// has no row id), and unbinding is visible on the next read. It also pins
// the pack delete: a pack a role is still bound to answers 409 naming the
// roles and the fix, the unbound pack deletes, and a second delete is 404.
func TestPacksBindingsAndUnbind(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	reader, _ := app.store.Roles().GetByName(ctx, "reader")
	dev, _ := app.store.Roles().GetByName(ctx, "dev")
	pack, err := app.store.Packs().GetByName(ctx, "golang-style")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Packs().Bind(ctx, dev.ID, pack.ID); err != nil {
		t.Fatal(err)
	}
	deletePack := func(t *testing.T) (int, string) {
		t.Helper()
		var out struct {
			Error  string `json:"error"`
			Status string `json:"status"`
		}
		code := adminReq(t, "DELETE", base+"/v1/admin/packs/"+pack.ID, tok, nil, &out)
		return code, out.Error + out.Status
	}
	for _, tc := range []struct {
		name   string
		unbind string
		want   int
		says   []string
		not    string
	}{
		{"bound to two roles", "", http.StatusConflict, []string{"the roles dev and reader", "Unbind it from each role first, then delete it"}, ""},
		{"bound to one role", dev.ID, http.StatusConflict, []string{"the role reader", "Unbind it from the role first, then delete it"}, "dev"},
	} {
		if tc.unbind != "" {
			if code := adminReq(t, "DELETE", base+"/v1/admin/packs/"+pack.ID+"/bindings/"+tc.unbind, tok, nil, nil); code != http.StatusOK {
				t.Fatalf("%s: unbind = %d, want 200", tc.name, code)
			}
		}
		code, says := deletePack(t)
		if code != tc.want {
			t.Fatalf("%s: delete = %d %q, want %d", tc.name, code, says, tc.want)
		}
		for _, want := range tc.says {
			if !strings.Contains(says, want) {
				t.Errorf("%s: refusal %q does not say %q", tc.name, says, want)
			}
		}
		if tc.not != "" && strings.Contains(says, tc.not) {
			t.Errorf("%s: refusal %q names the unbound role %q", tc.name, says, tc.not)
		}
	}

	type binding struct {
		ID       string `json:"id"`
		RoleID   string `json:"role_id"`
		RoleName string `json:"role_name"`
	}
	packBindings := func() []binding {
		var packs []struct {
			Name     string    `json:"name"`
			Bindings []binding `json:"bindings"`
		}
		if code := adminReq(t, "GET", base+"/v1/admin/packs", tok, nil, &packs); code != http.StatusOK {
			t.Fatalf("packs list = %d", code)
		}
		for _, p := range packs {
			if p.Name == "golang-style" {
				if p.Bindings == nil {
					t.Fatal("bindings key missing or null, want array")
				}
				return p.Bindings
			}
		}
		t.Fatal("pack golang-style not listed")
		return nil
	}

	pre := packBindings()
	if len(pre) != 1 || pre[0].ID != reader.ID || pre[0].RoleID != reader.ID || pre[0].RoleName != "reader" {
		t.Fatalf("bindings = %+v, want one edge to reader (id = role id)", pre)
	}

	if code := adminReq(t, "DELETE", base+"/v1/admin/packs/"+pack.ID+"/bindings/"+reader.ID, tok, nil, nil); code != http.StatusOK {
		t.Fatalf("unbind = %d, want 200", code)
	}
	if post := packBindings(); len(post) != 0 {
		t.Errorf("post-unbind bindings = %+v, want empty", post)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/packs/"+pack.ID+"/bindings/"+reader.ID, tok, nil, nil); code != http.StatusNotFound {
		t.Errorf("second unbind = %d, want 404", code)
	}

	if code, says := deletePack(t); code != http.StatusOK || says != "deleted" {
		t.Fatalf("delete of the unbound pack = %d %q, want 200 deleted", code, says)
	}
	if _, err := app.store.Packs().GetByID(ctx, pack.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleted pack still stored: %v", err)
	}
	if code, says := deletePack(t); code != http.StatusNotFound || says != "no such knowledge pack" {
		t.Errorf("second delete = %d %q, want 404 no such knowledge pack", code, says)
	}
}

// TestRolesListHoldersReachAndPools pins the roles list fields the console's
// Roles area reads (openapi 0.101.0): holder_count counts every subject
// holding the role at read time through any path, once each, with rows
// outside their window left out; implies names the directly implied roles;
// areas words a straza role's console grants from admin.roleAreas, full for
// the root role; decider_in names the active sets naming an approver role
// as a pool; and each field stays off the rows it does not apply to.
func TestRolesListHoldersReachAndPools(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Admin.RoleAreas = map[string][]string{"auditor": {"sessions:read", "audit:read"}}
	})
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	reader, err := app.store.Roles().GetByName(ctx, "reader")
	if err != nil {
		t.Fatal(err)
	}
	// mo holds reader directly and in force; lee's row on reader expired.
	past := time.Now().Add(-time.Hour).UTC()
	for _, u := range []struct {
		name string
		to   *time.Time
	}{{"mo", nil}, {"lee", &past}} {
		row, err := app.store.Users().Create(ctx, store.User{Username: u.name})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: row.ID, RoleID: reader.ID, ValidTo: u.to}); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []map[string]string{{"name": "auditor", "kind": "straza"}, {"name": "sec-approvers", "kind": "approver"}} {
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/roles", tok, body, nil); code != http.StatusCreated {
			t.Fatalf("create %v = %d", body, code)
		}
	}
	doc := strings.Replace(approvePoolYAML, "[POOL]", "[sec-approvers]", 1)
	if code, b, _ := adminBytes(t, http.MethodPut, base+"/v1/admin/policies", tok, "application/yaml", []byte(doc)); code != http.StatusCreated {
		t.Fatalf("apply = %d %s", code, b)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/pool-gate/activate", tok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}

	var rows []map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/roles", tok, nil, &rows); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	byName := map[string]map[string]any{}
	for _, row := range rows {
		byName[row["name"].(string)] = row
	}
	list := func(row map[string]any, key string) []string {
		v, ok := row[key].([]any)
		if !ok {
			return nil
		}
		out := make([]string, len(v))
		for i, x := range v {
			out[i] = x.(string)
		}
		return out
	}
	cases := []struct {
		role    string
		holders float64
		implies []string
		areas   []string
		pools   []string
	}{
		// kim holds dev; dev implies reader, so kim holds reader too.
		{"dev", 1, []string{"reader"}, nil, nil},
		// kim through dev and mo directly; lee's expired row does not count.
		{"reader", 2, nil, nil, nil},
		{"auditor", 0, nil, []string{"audit:read", "sessions:read"}, nil},
		// Boot and the test seed both grant the root role, so only its
		// areas word is pinned; -1 skips the count.
		{AdminRole, -1, nil, []string{"full"}, nil},
		{"sec-approvers", 0, nil, nil, []string{"pool-gate"}},
	}
	for _, tc := range cases {
		row, ok := byName[tc.role]
		if !ok {
			t.Fatalf("role %s missing from the list", tc.role)
		}
		if got := row["holder_count"]; tc.holders >= 0 && got != tc.holders {
			t.Errorf("%s holder_count = %v, want %v", tc.role, got, tc.holders)
		}
		for key, want := range map[string][]string{"implies": tc.implies, "areas": tc.areas, "decider_in": tc.pools} {
			got := list(row, key)
			if _, present := row[key]; present != (want != nil) || strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s %s = %v (present %v), want %v", tc.role, key, got, present, want)
			}
		}
	}
	if byName["reader"]["assigned_count"] != float64(2) {
		t.Errorf("reader assigned_count = %v, want 2 rows, the expired one included", byName["reader"]["assigned_count"])
	}
}
