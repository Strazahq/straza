package server

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestAdminListSort pins the sort and order parameters of the users and
// sessions lists (openapi 0.100.0): the order is the server's over the
// whole set and survives the cursor walk, a cursor continues only the order
// it was minted under, the refusals say what to do, the lists that take no
// sort refuse one, and the sponsor filter and count read the same rows.
func TestAdminListSort(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	ids := map[string]string{}
	for _, u := range []store.User{
		{Username: "zoe"},
		{Username: "amy", Sponsor: "kim", UserType: store.UserTypeAgent},
		{Username: "bob", Sponsor: "kim", UserType: store.UserTypeAgent},
	} {
		created, err := app.store.Users().Create(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		ids[u.Username] = created.ID
	}
	// kim was seen at the check-in just now, zoe an hour ago, amy three
	// hours ago; bob has never checked in.
	base8 := time.Now().Add(-3 * time.Hour)
	for i, n := range []string{"amy", "zoe", "zoe"} {
		ses, err := app.store.Sessions().Create(ctx, store.Session{UserID: ids[n], HarnessName: "h", HarnessVersion: "1", AttestationLevel: store.AttestationAdvisory})
		if err != nil {
			t.Fatal(err)
		}
		if err := app.store.Sessions().Touch(ctx, ses.ID, base8.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	type envelope struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	walk := func(path string, pageSize int) []map[string]any {
		t.Helper()
		var all []map[string]any
		cursor := ""
		for i := 0; i < 20; i++ {
			url := base + path + "&limit=" + strconv.Itoa(pageSize)
			if cursor != "" {
				url += "&cursor=" + cursor
			}
			var e envelope
			if code := adminReq(t, http.MethodGet, url, adminTok, nil, &e); code != http.StatusOK {
				t.Fatalf("page %s = %d", url, code)
			}
			all = append(all, e.Items...)
			if e.NextCursor == "" {
				return all
			}
			cursor = e.NextCursor
		}
		t.Fatalf("%s cursor never terminated", path)
		return nil
	}
	names := func(rows []map[string]any) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i], _ = r["username"].(string)
		}
		return out
	}

	// A walk by name, two rows a page, never goes backwards and visits every
	// user once (kim, the bootstrap admin and break-glass included).
	byName := names(walk("/v1/admin/users?sort=name&order=asc", 2))
	seen := map[string]bool{}
	for i, n := range byName {
		if i > 0 && byName[i-1] > n {
			t.Errorf("name asc walk: %q before %q", byName[i-1], n)
		}
		if seen[n] {
			t.Errorf("name asc walk: %q twice", n)
		}
		seen[n] = true
	}
	for _, n := range []string{"amy", "bob", "kim", "zoe"} {
		if !seen[n] {
			t.Errorf("name asc walk missed %s: %v", n, byName)
		}
	}
	byNameDesc := names(walk("/v1/admin/users?sort=name&order=desc", 2))
	if len(byNameDesc) != len(byName) || byNameDesc[0] != byName[len(byName)-1] {
		t.Errorf("name desc walk = %v, want the reverse of %v", byNameDesc, byName)
	}

	// last_seen opens on the newest session and ends on the never-seen.
	byLast := walk("/v1/admin/users?sort=last_seen", 2)
	if n := names(byLast); n[0] != "kim" || n[1] != "zoe" || n[2] != "amy" {
		t.Errorf("last_seen desc = %v, want kim, zoe, amy first", n)
	}
	for _, r := range byLast[3:] {
		if _, ok := r["last_seen"]; ok {
			t.Errorf("a seen user %v sorted after the never-seen", r["username"])
		}
	}

	// A cursor minted under one order is stale under another.
	var first envelope
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users?sort=name&order=asc&limit=2", adminTok, nil, &first); code != http.StatusOK || first.NextCursor == "" {
		t.Fatalf("first page = %d, cursor %q", code, first.NextCursor)
	}
	for _, path := range []string{
		"/v1/admin/users?sort=name&order=desc&limit=2&cursor=",
		"/v1/admin/users?sort=status&limit=2&cursor=",
		"/v1/admin/users?limit=2&cursor=",
	} {
		var body map[string]any
		if code := adminReq(t, http.MethodGet, base+path+first.NextCursor, adminTok, nil, &body); code != http.StatusBadRequest {
			t.Errorf("%s with a name-asc cursor = %d, want 400", path, code)
		} else if body["error"] != "invalid or stale cursor: re-fetch from the start" {
			t.Errorf("stale cursor error = %v", body["error"])
		}
	}

	// The refusals name what to do.
	for _, tc := range []struct{ path, want string }{
		{"/v1/admin/users?sort=name", "sort and order require pagination: add limit="},
		{"/v1/admin/users?limit=5&sort=email", "sort must be one of created, last_seen, name, status"},
		{"/v1/admin/users?limit=5&sort=name&order=sideways", "order must be asc or desc"},
		{"/v1/admin/sessions?limit=5&sort=harness", "sort must be one of attestation, last_seen, started, status, user"},
		{"/v1/admin/api-tokens?limit=5&sort=name", "this list has one order and takes no sort"},
		{"/v1/admin/users?sponsor=kim", "q/status/role/sponsor filters require pagination: add limit="},
	} {
		var body map[string]any
		if code := adminReq(t, http.MethodGet, base+tc.path, adminTok, nil, &body); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", tc.path, code)
		} else if body["error"] != tc.want {
			t.Errorf("%s error = %q, want %q", tc.path, body["error"], tc.want)
		}
	}

	// Sponsorship reads both ways: the count on kim's row and detail, and
	// the filter that lists the agents kim sponsors.
	for _, r := range walk("/v1/admin/users?sort=name", 10) {
		want := 0.0
		if r["username"] == "kim" {
			want = 2
		}
		if r["sponsored_count"] != want {
			t.Errorf("%v sponsored_count = %v, want %v", r["username"], r["sponsored_count"], want)
		}
	}
	var detail map[string]any
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+kim.ID, adminTok, nil, &detail); code != http.StatusOK || detail["sponsored_count"] != 2.0 {
		t.Errorf("kim detail = %d, sponsored_count %v, want 2", code, detail["sponsored_count"])
	}
	if got := names(walk("/v1/admin/users?sponsor=kim&sort=name&order=asc", 10)); len(got) != 2 || got[0] != "amy" || got[1] != "bob" {
		t.Errorf("sponsor=kim = %v, want [amy bob]", got)
	}

	// Sessions sort by owner and by attestation across pages; the checkin
	// session of kim rides along.
	byUser := walk("/v1/admin/sessions?sort=user&order=asc", 2)
	for i := 1; i < len(byUser); i++ {
		if a, b := byUser[i-1]["username"].(string), byUser[i]["username"].(string); a > b {
			t.Errorf("sessions by user: %q before %q", a, b)
		}
	}
	if len(byUser) != 4 {
		t.Errorf("sessions by user walked %d rows, want 4", len(byUser))
	}
	byAtt := walk("/v1/admin/sessions?sort=attestation&order=desc", 3)
	for i := 1; i < len(byAtt); i++ {
		if a, b := byAtt[i-1]["attestation"].(string), byAtt[i]["attestation"].(string); a < b {
			t.Errorf("sessions by attestation desc: %q before %q", a, b)
		}
	}
	byLastSeen := walk("/v1/admin/sessions?sort=last_seen&status=active", 2)
	if len(byLastSeen) != 4 || byLastSeen[len(byLastSeen)-1]["username"] != "amy" {
		t.Errorf("sessions by last_seen desc = %v, want amy's hour-old session last", names(byLastSeen))
	}
}
