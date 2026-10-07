package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// Sessions list ergonomics and the devices wire shape. Both pins are
// console-seat contracts: what a UI reads
// off these endpoints must be filterable, ordered, and JSON-conventional.

// TestSessionsListFilters pins ?user=, ?status= validation, and newest-first
// ordering on GET /v1/admin/sessions.
func TestSessionsListFilters(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	other, err := app.store.Users().Create(ctx, store.User{Username: "nova"})
	if err != nil {
		t.Fatal(err)
	}
	// Three sessions, created in order: kim-old, nova, kim-new.
	kimOld, err := app.store.Sessions().Create(ctx, store.Session{
		UserID: user.ID, HarnessName: "claude-code", HarnessVersion: "2.1.0", Status: store.SessionActive})
	if err != nil {
		t.Fatal(err)
	}
	novaSes, err := app.store.Sessions().Create(ctx, store.Session{
		UserID: other.ID, HarnessName: "codex", HarnessVersion: "0.146.0", Status: store.SessionRevoked})
	if err != nil {
		t.Fatal(err)
	}
	kimNew, err := app.store.Sessions().Create(ctx, store.Session{
		UserID: user.ID, HarnessName: "claude-code", HarnessVersion: "2.1.0", Status: store.SessionActive})
	if err != nil {
		t.Fatal(err)
	}

	list := func(query string, want int) []sessionPayload {
		t.Helper()
		var out []sessionPayload
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/sessions"+query, adminTok, nil, &out); code != want {
			t.Fatalf("sessions%s = %d, want %d", query, code, want)
		}
		return out
	}

	// Newest first: the three seeded sessions in reverse creation order,
	// then the (older) checkin session from checkinToken.
	all := list("", http.StatusOK)
	if len(all) != 4 {
		t.Fatalf("list = %d rows, want 4", len(all))
	}
	if all[0].ID != kimNew.ID || all[1].ID != novaSes.ID || all[2].ID != kimOld.ID {
		t.Errorf("order = %s,%s,%s want newest first %s,%s,%s",
			all[0].ID, all[1].ID, all[2].ID, kimNew.ID, novaSes.ID, kimOld.ID)
	}

	// ?user= narrows to that user's sessions, still newest first.
	mine := list("?user="+other.ID, http.StatusOK)
	if len(mine) != 1 || mine[0].ID != novaSes.ID {
		t.Errorf("?user= = %+v, want just %s", mine, novaSes.ID)
	}
	// user + status combine.
	none := list("?user="+other.ID+"&status=active", http.StatusOK)
	if len(none) != 0 {
		t.Errorf("?user&status=active = %+v, want empty", none)
	}
	// An unknown status is a 400, not an empty 200 that reads like "no
	// sessions" (a console filter typo must be loud).
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/sessions?status=bogus", adminTok, nil, nil); code != http.StatusBadRequest {
		t.Errorf("sessions?status=bogus = %d, want 400", code)
	}
}

// TestDevicesListShape pins GET /v1/admin/users/{id}/devices: 404 for an
// unknown user, a real empty JSON array for the no-devices case, and
// snake_case keys, never the raw untagged store struct.
func TestDevicesListShape(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/nope/devices", adminTok, nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown user devices = %d, want 404", code)
	}

	var raw json.RawMessage
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+user.ID+"/devices", adminTok, nil, &raw); code != http.StatusOK {
		t.Fatalf("devices = %d", code)
	}
	if string(raw) != "[]" {
		t.Errorf("no-devices body = %s, want []", raw)
	}

	if _, err := app.store.Devices().Create(ctx, store.Device{
		UserID: user.ID, Name: "kim-laptop", Fingerprint: "sha256:abc", Platform: "linux",
		Status: "active"}); err != nil {
		t.Fatal(err)
	}
	var devs []map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+user.ID+"/devices", adminTok, nil, &devs); code != http.StatusOK {
		t.Fatalf("devices = %d", code)
	}
	if len(devs) != 1 {
		t.Fatalf("devices = %+v, want 1 row", devs)
	}
	for _, key := range []string{"id", "user_id", "name", "fingerprint", "platform", "status", "enrolled_at"} {
		if _, ok := devs[0][key]; !ok {
			t.Errorf("device payload misses %q: %+v", key, devs[0])
		}
	}
}

// TestAdminListPagination pins the opt-in pagination envelope: ?limit=/?cursor=
// switch a list to {items, next_cursor}; a bare call keeps the legacy shape
// byte-compatible; cursors walk newest-first to a clean end; malformed or
// stale cursors answer 400.
func TestAdminListPagination(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	type envelope struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	walk := func(path, sep string, pageSize, wantTotal int) []map[string]any {
		t.Helper()
		var all []map[string]any
		cursor := ""
		for i := 0; i < 20; i++ {
			url := base + path + sep + "limit=" + strconv.Itoa(pageSize)
			if cursor != "" {
				url += "&cursor=" + cursor
			}
			var e envelope
			if code := adminReq(t, http.MethodGet, url, adminTok, nil, &e); code != http.StatusOK {
				t.Fatalf("page %s = %d", url, code)
			}
			all = append(all, e.Items...)
			if e.NextCursor == "" {
				if len(all) != wantTotal {
					t.Fatalf("%s walked %d rows, want %d", path, len(all), wantTotal)
				}
				return all
			}
			cursor = e.NextCursor
		}
		t.Fatalf("%s cursor never terminated", path)
		return nil
	}

	// Sessions: 3 seeded + the checkin session.
	var sesIDs []string
	for i := 0; i < 3; i++ {
		ses, err := app.store.Sessions().Create(ctx, store.Session{
			UserID: user.ID, HarnessName: "h", HarnessVersion: "1", Status: store.SessionActive})
		if err != nil {
			t.Fatal(err)
		}
		sesIDs = append(sesIDs, ses.ID)
	}
	rows := walk("/v1/admin/sessions", "?", 2, 4)
	if rows[0]["id"] != sesIDs[2] || rows[1]["id"] != sesIDs[1] {
		t.Errorf("sessions pages not newest-first: %v", rows)
	}
	// Legacy shape stays a bare array.
	var legacy []sessionPayload
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/sessions", adminTok, nil, &legacy); code != http.StatusOK || len(legacy) != 4 {
		t.Fatalf("legacy sessions = %d rows (code %d), want bare array of 4", len(legacy), code)
	}

	// Users: kim + two more.
	for _, n := range []string{"nova", "rex"} {
		if _, err := app.store.Users().Create(ctx, store.User{Username: n}); err != nil {
			t.Fatal(err)
		}
	}
	// kim + nova + rex + the bootstrap admin + break-glass.
	users := walk("/v1/admin/users", "?", 2, 5)
	if users[0]["username"] != "rex" {
		t.Errorf("users page 1 head = %v, want the newest user rex", users[0]["username"])
	}

	// Approvals: 3 pending; state filter composes with the cursor.
	for i := 0; i < 3; i++ {
		seedApproval(t, app, "u-page"+strconv.Itoa(i), []string{"dev"})
	}
	walk("/v1/admin/approvals", "?state=pending&", 2, 3)

	// API tokens: settings-map list, slice cursor.
	for _, n := range []string{"one", "two", "three"} {
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
			map[string]any{"name": n, "scope": "audit:read"}, nil); code != http.StatusCreated {
			t.Fatalf("mint %s = %d", n, code)
		}
	}
	walk("/v1/admin/api-tokens", "?", 2, 3)

	// Devices: paged walk + the bare shape untouched.
	for i := 0; i < 3; i++ {
		if _, err := app.store.Devices().Create(ctx, store.Device{UserID: user.ID, Name: "d" + strconv.Itoa(i), Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	walk("/v1/admin/users/"+user.ID+"/devices", "?", 2, 3)

	// Malformed cursor = 400 on both backing kinds.
	for _, path := range []string{"/v1/admin/sessions", "/v1/admin/api-tokens"} {
		if code := adminReq(t, http.MethodGet, base+path+"?cursor=%21%21not-base64", adminTok, nil, nil); code != http.StatusBadRequest {
			t.Errorf("%s bad cursor = %d, want 400", path, code)
		}
	}
}

// TestUserDetailEnriched pins the enriched user read: effective roles from the
// resolver, the lock block the lock API writes, counts, timestamps.
func TestUserDetailEnriched(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	user := seedIdentity(t, app) // kim, holds role dev
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	// Enrich a SECOND user: locking the admin would revoke the admin's own
	// session through the kill-switch cascade.
	other, err := app.store.Users().Create(ctx, store.User{Username: "nova"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Devices().Create(ctx, store.Device{UserID: other.ID, Name: "nova-box", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	seedApproval(t, app, other.ID, []string{"dev"})
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/users/"+other.ID+"/lock", tok,
		map[string]any{"reason": "incident 42"}, nil); code != http.StatusOK {
		t.Fatalf("lock = %d", code)
	}

	var d struct {
		Username       string   `json:"username"`
		EffectiveRoles []string `json:"effective_roles"`
		Locks          []struct {
			Origin string `json:"origin"`
			Reason string `json:"reason"`
		} `json:"locks"`
		CreatedAt string `json:"created_at"`
		Counts    struct {
			Sessions  int `json:"sessions"`
			Devices   int `json:"devices"`
			Approvals int `json:"approvals"`
		} `json:"counts"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+other.ID, tok, nil, &d); code != http.StatusOK {
		t.Fatalf("detail = %d", code)
	}
	if len(d.Locks) != 1 || d.Locks[0].Reason != "incident 42" || d.Locks[0].Origin != "admin" {
		t.Errorf("locks = %+v, want the admin lock with its reason", d.Locks)
	}
	if d.Counts.Devices != 1 || d.Counts.Approvals != 1 {
		t.Errorf("counts = %+v, want 1 device, 1 approval", d.Counts)
	}
	// The admin's own detail carries the resolver's effective roles.
	var mine struct {
		EffectiveRoles []string `json:"effective_roles"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+user.ID, tok, nil, &mine); code != http.StatusOK {
		t.Fatalf("self detail = %d", code)
	}
	hasRole := func(name string) bool {
		for _, r := range mine.EffectiveRoles {
			if r == name {
				return true
			}
		}
		return false
	}
	if !hasRole("dev") || !hasRole("straza-admin") {
		t.Errorf("effective_roles = %v, want dev + straza-admin", mine.EffectiveRoles)
	}
	if d.CreatedAt == "" || d.CreatedAt == "0001-01-01T00:00:00Z" {
		t.Errorf("created_at missing: %q", d.CreatedAt)
	}

	// A user who never checked in must not
	// serialize a zero time for the drawer to render as "1-01-01 00:00:00Z".
	// The field is absent and clients say "never"; a checked-in user carries
	// a real stamp (the positive control).
	var raw map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+other.ID, tok, nil, &raw); code != http.StatusOK {
		t.Fatalf("detail = %d", code)
	}
	if v, ok := raw["last_seen"]; ok {
		t.Errorf("never-seen user serializes last_seen = %v, want the field absent", v)
	}
	var mineRaw map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+user.ID, tok, nil, &mineRaw); code != http.StatusOK {
		t.Fatalf("self detail = %d", code)
	}
	if s, _ := mineRaw["last_seen"].(string); s == "" || strings.HasPrefix(s, "0001-") {
		t.Errorf("checked-in user's last_seen = %q, want a real stamp", s)
	}

	// The revocations read surface: the lock row is listable.
	var revs []struct {
		Kind     string `json:"kind"`
		TargetID string `json:"target_id"`
		Reason   string `json:"reason"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/revocations", tok, nil, &revs); code != http.StatusOK {
		t.Fatalf("revocations = %d", code)
	}
	found := false
	for _, rv := range revs {
		if rv.Kind == "user" && rv.TargetID == other.ID && rv.Reason == "incident 42" {
			found = true
		}
	}
	if !found {
		t.Errorf("revocations list misses the lock row: %+v", revs)
	}
}

// The paged users lane carries server-resolved effective_roles
// (implication closure included, which no client-side fold could see) plus
// batched last_seen; q/status/role parse strictly (filters demand the paged
// lane, unknown values 400); the legacy bare lane stays shape-identical.
func TestUsersListFiltersAndEnrichment(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	user := seedIdentity(t, app) // kim, holds role dev
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	nova, err := app.store.Users().Create(ctx, store.User{Username: "nova", Email: "nova@corp.example"})
	if err != nil {
		t.Fatal(err)
	}
	senior, err := app.store.Roles().Create(ctx, store.Role{Name: "senior", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	shelf, err := app.store.Roles().Create(ctx, store.Role{Name: "shelf-reader", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Roles().AddImplication(ctx, senior.ID, shelf.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: nova.ID, RoleID: senior.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Sessions().Create(ctx, store.Session{UserID: nova.ID}); err != nil {
		t.Fatal(err)
	}
	app.resolver.Bump() // seeded behind the API's back

	var page struct {
		Items []struct {
			Username       string   `json:"username"`
			EffectiveRoles []string `json:"effective_roles"`
			LastSeen       string   `json:"last_seen"`
		} `json:"items"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users?limit=50", tok, nil, &page); code != http.StatusOK {
		t.Fatalf("paged users = %d", code)
	}
	found := false
	for _, row := range page.Items {
		if row.Username != "nova" {
			continue
		}
		found = true
		if len(row.EffectiveRoles) != 2 || row.EffectiveRoles[0] != "senior" || row.EffectiveRoles[1] != "shelf-reader" {
			t.Errorf("nova effective_roles = %v, want [senior shelf-reader] incl. the implied role", row.EffectiveRoles)
		}
		if row.LastSeen == "" {
			t.Errorf("nova last_seen empty although a session exists")
		}
	}
	if !found {
		t.Fatalf("nova missing from the paged list: %+v", page.Items)
	}

	// The role filter matches through the implication: nova never held
	// shelf-reader directly.
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users?limit=50&role=shelf-reader", tok, nil, &page); code != http.StatusOK {
		t.Fatalf("role filter = %d", code)
	}
	if len(page.Items) != 1 || page.Items[0].Username != "nova" {
		t.Errorf("role=shelf-reader = %+v, want exactly nova via the implication", page.Items)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users?limit=50&q=NOVA@corp", tok, nil, &page); code != http.StatusOK || len(page.Items) != 1 {
		t.Errorf("q email cross-case = %d items %+v, want just nova", code, page.Items)
	}

	// Strict parsing: unknown role, unknown status, filters without paging.
	var apiErr map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users?limit=50&role=ghost", tok, nil, &apiErr); code != http.StatusBadRequest {
		t.Errorf("unknown role = %d, want 400", code)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users?limit=50&status=weird", tok, nil, &apiErr); code != http.StatusBadRequest {
		t.Errorf("unknown status = %d, want 400", code)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users?q=nova", tok, nil, &apiErr); code != http.StatusBadRequest {
		t.Errorf("filter without limit = %d, want 400 (never silently ignored)", code)
	} else if msg, _ := apiErr["error"].(string); !strings.Contains(msg, "limit=") {
		t.Errorf("bare-lane filter error %q must teach the limit= fix", msg)
	}

	// Legacy bare lane: no enrichment keys, shape byte-compatible.
	var raw []map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users", tok, nil, &raw); code != http.StatusOK {
		t.Fatalf("legacy users = %d", code)
	}
	for _, row := range raw {
		if _, ok := row["effective_roles"]; ok {
			t.Fatalf("legacy lane grew effective_roles: %+v", row)
		}
		if _, ok := row["last_seen"]; ok {
			t.Fatalf("legacy lane grew last_seen: %+v", row)
		}
	}
}

// TestRolesListAssignedCounts pins the Assigned column's server half: every
// roles-list row carries assigned_count from ONE grouped query (assignment
// rows, windows included, group subjects counted too), zero for a role
// nobody holds. The console counts on this contract instead of reading the
// full assignments list, which is unbounded at directory scale.
func TestRolesListAssignedCounts(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	reader, err := app.store.Roles().Create(ctx, store.Role{Name: "count-reader"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Roles().Create(ctx, store.Role{Name: "count-writer"}); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).UTC()
	for _, asg := range []store.RoleAssignment{
		{SubjectKind: store.SubjectUser, SubjectID: user.ID, RoleID: reader.ID},
		{SubjectKind: store.SubjectUser, SubjectID: "u-second", RoleID: reader.ID},
		// Windowed-out row still counts: the column mirrors the list rows.
		{SubjectKind: store.SubjectUser, SubjectID: "u-gone", RoleID: reader.ID, ValidTo: &past},
	} {
		if _, err := app.store.Roles().Assign(ctx, asg); err != nil {
			t.Fatalf("Assign %+v: %v", asg, err)
		}
	}

	var out []rolePayload
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/roles", adminTok, nil, &out); code != http.StatusOK {
		t.Fatalf("roles list = %d, want 200", code)
	}
	byName := map[string]rolePayload{}
	for _, r := range out {
		byName[r.Name] = r
	}
	if got := byName["count-reader"].AssignedCount; got != 3 {
		t.Errorf("count-reader assigned_count = %d, want 3", got)
	}
	if got := byName["count-writer"].AssignedCount; got != 0 {
		t.Errorf("count-writer assigned_count = %d, want 0", got)
	}
}
