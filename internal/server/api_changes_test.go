package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

type changesPage struct {
	Changes []struct {
		Cursor string `json:"cursor"`
		Type   string `json:"type"`
		Op     string `json:"op"`
		ID     string `json:"id"`
	} `json:"changes"`
	NextCursor string `json:"nextCursor"`
	More       bool   `json:"more"`
	Head       string `json:"head"`
}

// TestAdminChangesFeed pins the IGA liveSync contract: a cursored, typed,
// at-least-once change feed over the events
// outbox, create/update/delete per object, identity ids classified to
// user/role by store lookup, session noise skipped, cursor strictly
// advancing even across skipped rows.
func TestAdminChangesFeed(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	role, err := app.store.Roles().Create(ctx, store.Role{Name: "auditor", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}

	// Baseline cursor: consume whatever checkin/seed produced so the
	// assertions below see only OUR events.
	var page changesPage
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/changes?limit=1000", adminTok, nil, &page); code != http.StatusOK {
		t.Fatalf("baseline = %d", code)
	}
	start := page.NextCursor

	insert := func(subject, ce string) {
		t.Helper()
		if _, err := app.store.Outbox().Insert(ctx, store.OutboxEvent{Subject: subject, CE: ce}); err != nil {
			t.Fatal(err)
		}
	}
	insert("straza.identity.created", fmt.Sprintf(`{"data":{"id":%q,"origin":"scim"}}`, user.ID))
	insert("straza.identity.updated", fmt.Sprintf(`{"data":{"id":%q}}`, role.ID))                            // role CRUD + wire-group membership ride the identity channel
	insert("straza.identity.updated", fmt.Sprintf(`{"data":{"action":"session.start","user":%q}}`, user.ID)) // noise
	insert("straza.identity.updated", `{"data":{"id":"01ffffff-dead-7000-8000-000000000000"}}`)              // hard-deleted
	// Real emission shapes (manager.go): deployed/removed/drift carry the app
	// UID in `app` (+ `name`); apps.updated binding events carry the app NAME.
	insert("straza.apps.deployed", `{"data":{"app":"01aaaaaa-0000-7000-8000-000000000001","name":"midpoint","version":"0.2.1"}}`)
	insert("straza.apps.updated", `{"data":{"change":"binding","app":"midpoint"}}`)
	insert("straza.apps.updated", `{"data":{"change":"secret","app":"midpoint"}}`) // not IGA-visible
	insert("straza.apps.removed", `{"data":{"app":"01aaaaaa-0000-7000-8000-000000000001","name":"midpoint"}}`)
	insert("straza.audit.tool", `{"data":{"command":"ls"}}`) // never in the feed

	url := base + "/v1/admin/changes?since=" + start
	if code := adminReq(t, http.MethodGet, url, adminTok, nil, &page); code != http.StatusOK {
		t.Fatalf("changes = %d", code)
	}
	want := []struct{ typ, op, id string }{
		{"user", "create", user.ID},
		{"role", "update", role.ID},
		{"identity", "delete", "01ffffff-dead-7000-8000-000000000000"},
		{"app", "create", "01aaaaaa-0000-7000-8000-000000000001"}, // app records key by UID
		{"binding", "update", "midpoint"},                         // binding records key by app NAME
		{"app", "delete", "01aaaaaa-0000-7000-8000-000000000001"},
	}
	if len(page.Changes) != len(want) {
		t.Fatalf("changes = %d rows (%+v), want %d", len(page.Changes), page.Changes, len(want))
	}
	prevCursor := start
	for i, w := range want {
		got := page.Changes[i]
		if got.Type != w.typ || got.Op != w.op || got.ID != w.id {
			t.Errorf("row %d = %s/%s/%s, want %s/%s/%s", i, got.Type, got.Op, got.ID, w.typ, w.op, w.id)
		}
		if got.Cursor <= prevCursor {
			t.Errorf("row %d cursor %q does not advance past %q", i, got.Cursor, prevCursor)
		}
		prevCursor = got.Cursor
	}
	// Non-feed subjects (the audit row) are excluded by the SQL filter and
	// never advance the cursor; nextCursor sits at the last FETCHED row.
	if page.NextCursor != prevCursor {
		t.Errorf("nextCursor = %q, want last consumed row %q", page.NextCursor, prevCursor)
	}
	// head covers EVERYTHING, including non-feed rows (the audit row is the
	// newest insert): the sync-from-now point for first-time consumers.
	if page.Head <= page.NextCursor {
		t.Errorf("head = %q, want past nextCursor %q (audit row is newer)", page.Head, page.NextCursor)
	}
	if page.More {
		t.Error("more = true on a drained feed")
	}

	// Type filter: only app rows (create + delete).
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/changes?since="+start+"&types=app", adminTok, nil, &page); code != http.StatusOK {
		t.Fatalf("filtered = %d", code)
	}
	if len(page.Changes) != 2 || page.Changes[0].Op != "create" || page.Changes[1].Op != "delete" {
		t.Errorf("app-only rows = %+v", page.Changes)
	}

	// The deleted-identity marker answers a role-only consumer too.
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/changes?since="+start+"&types=role", adminTok, nil, &page); code != http.StatusOK {
		t.Fatalf("role filter = %d", code)
	}
	foundDelete := false
	for _, c := range page.Changes {
		if c.Type == "identity" && c.Op == "delete" {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Errorf("role consumer misses the hard-delete marker: %+v", page.Changes)
	}

	// The "group" type retired with the unified role model: asking for it
	// is a loud client error, never a silent empty feed.
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/changes?since="+start+"&types=group", adminTok, nil, &page); code != http.StatusBadRequest {
		t.Fatalf("retired group type = %d, want 400", code)
	}

	// Pagination: limit=1 pages with more=true and a resumable cursor.
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/changes?since="+start+"&limit=1", adminTok, nil, &page); code != http.StatusOK {
		t.Fatalf("paged = %d", code)
	}
	if !page.More || page.NextCursor == "" {
		t.Errorf("paged: more=%v nextCursor=%q", page.More, page.NextCursor)
	}

	// Unknown type is a loud 400, not a silent empty feed.
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/changes?types=nonsense", adminTok, nil, nil); code != http.StatusBadRequest {
		t.Errorf("unknown type = %d, want 400", code)
	}

	// Assignment delete names the SUBJECT losing the role: the association
	// lives on the user shadow, so liveSync must be told "user U changed",
	// never "assignment row <uuid> changed" (which resolves to nothing).
	as, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: user.ID, RoleID: role.ID, Origin: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	mark := page.Head
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/assignments/"+as.ID, adminTok, nil, nil); code != http.StatusOK {
		t.Fatalf("assignment delete = %d", code)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/changes?since="+mark+"&types=user", adminTok, nil, &page); code != http.StatusOK {
		t.Fatalf("post-unassign changes = %d", code)
	}
	found := false
	for _, c := range page.Changes {
		if c.Type == "user" && c.Op == "update" && c.ID == user.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("unassign did not surface as a user change: %+v", page.Changes)
	}

	// Binding DELETE names the affected app like binding create does (a
	// liveSync consumer re-derives role associations off it; the row id
	// resolves to nothing post-delete).
	appRow, err := app.store.Apps().Create(ctx, store.App{Name: "demoapp", RuntimeKind: "remote", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	// The global role's row is a store fixture: only a role a server owns
	// gains a new row through the API.
	created, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: mustRole(t, app, "auditor").ID, AppID: appRow.ID, ToolMatcher: `["*"]`})
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/changes?limit=1", adminTok, nil, &page); code != http.StatusOK {
		t.Fatal("head refresh failed")
	}
	mark = page.Head
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/bindings/"+created.ID, adminTok, nil, nil); code != http.StatusOK {
		t.Fatalf("binding delete = %d", code)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/changes?since="+mark+"&types=binding", adminTok, nil, &page); code != http.StatusOK {
		t.Fatalf("post-unbind changes = %d", code)
	}
	found = false
	for _, c := range page.Changes {
		if c.Type == "binding" && c.ID == "demoapp" {
			found = true
		}
	}
	if !found {
		t.Errorf("binding delete did not name its app: %+v", page.Changes)
	}
}

// TestAdminChangesReadScopeToken: the pull connector's read-scope wat_ token
// (its existing credential) can consume the feed; liveSync needs no new
// credential kind.
func TestAdminChangesReadScopeToken(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	var minted struct {
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "livesync", "scope": "changes:read"}, &minted); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("api-token mint = %d", code)
	}
	var page changesPage
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/changes", minted.Token, nil, &page); code != http.StatusOK {
		t.Fatalf("read-scope token on changes = %d, want 200", code)
	}
}
