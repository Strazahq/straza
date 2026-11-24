package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestUsersKindFilter pins kind and kind_counts on the paged users lane:
// each kind filtered under each other filter and paged one row at a time,
// every sort walked under a kind, the counts under each other filter on
// every page and unchanged by kind and the cursor, an untyped user of kind
// nhi counted as an agent, and the refusals on the bare lane and of a kind
// the list does not know.
func TestUsersKindFilter(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	for _, u := range []store.User{
		{Username: "zk-ann"},
		{Username: "zk-pat", UserType: store.UserTypeHuman, Status: store.UserDisabled},
		{Username: "zk-bot1", UserType: store.UserTypeAgent, Sponsor: "kim"},
		{Username: "zk-bot2", Attrs: `{"kind":"nhi"}`, Sponsor: "kim"},
		{Username: "zk-svc1", UserType: store.UserTypeService},
		{Username: "zk-svc2", UserType: store.UserTypeService, Status: store.UserDisabled},
	} {
		created, err := app.store.Users().Create(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		if u.Username == "zk-ann" || u.Username == "zk-bot1" {
			grantNamedRole(t, app, created.ID, "reader")
		}
	}

	type envelope struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
		KindCounts map[string]int   `json:"kind_counts"`
	}
	// walk reads every page of query, one row a page, and answers the
	// usernames in order and the kind_counts of each page.
	walk := func(t *testing.T, query string) ([]string, []map[string]int) {
		t.Helper()
		var names []string
		var counts []map[string]int
		cursor := ""
		for range 20 {
			q := query + "&limit=1"
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			var e envelope
			if code := adminReq(t, http.MethodGet, base+"/v1/admin/users?"+q, adminTok, nil, &e); code != http.StatusOK {
				t.Fatalf("GET /v1/admin/users?%s = %d", q, code)
			}
			for _, row := range e.Items {
				names = append(names, row["username"].(string))
			}
			counts = append(counts, e.KindCounts)
			if e.NextCursor == "" {
				return names, counts
			}
			cursor = e.NextCursor
		}
		t.Fatalf("%s: the cursor never ended", query)
		return nil, nil
	}

	for _, tc := range []struct {
		filter string
		kinds  map[string][]string
	}{
		{"q=zk-", map[string][]string{"person": {"zk-ann", "zk-pat"}, "agent": {"zk-bot1", "zk-bot2"}, "service": {"zk-svc1", "zk-svc2"}}},
		{"q=zk-&status=disabled", map[string][]string{"person": {"zk-pat"}, "service": {"zk-svc2"}}},
		{"q=zk-&role=reader", map[string][]string{"person": {"zk-ann"}, "agent": {"zk-bot1"}}},
		{"sponsor=kim", map[string][]string{"agent": {"zk-bot1", "zk-bot2"}}},
	} {
		wantCounts := map[string]int{"person": len(tc.kinds["person"]), "agent": len(tc.kinds["agent"]), "service": len(tc.kinds["service"])}
		var all []string
		for _, kind := range []string{"person", "agent", "service", ""} {
			query := tc.filter + "&sort=name&order=asc"
			want := tc.kinds[kind]
			if kind != "" {
				query += "&kind=" + kind
				all = append(all, want...)
			} else {
				want = sortedIDs(all)
			}
			names, counts := walk(t, query)
			if !reflect.DeepEqual(sortedIDs(names), sortedIDs(want)) {
				t.Errorf("%s = %v, want %v", query, names, want)
			}
			for i, c := range counts {
				if !reflect.DeepEqual(c, wantCounts) {
					t.Errorf("%s page %d kind_counts = %v, want %v", query, i+1, c, wantCounts)
				}
			}
		}
	}

	for _, sort := range []string{"created", "name", "status", "last_seen"} {
		for _, order := range []string{"asc", "desc"} {
			query := "q=zk-&kind=agent&sort=" + sort + "&order=" + order
			if names, _ := walk(t, query); !reflect.DeepEqual(sortedIDs(names), []string{"zk-bot1", "zk-bot2"}) {
				t.Errorf("%s = %v, want zk-bot1 and zk-bot2 once each", query, names)
			}
		}
	}

	for _, tc := range []struct{ query, want string }{
		{"kind=agent", "q/status/role/sponsor/kind filters require pagination: add limit="},
		{"limit=5&kind=robot", fmt.Sprintf(userKindRefusedMsg, "robot")},
		{"limit=5&kind=nhi", fmt.Sprintf(userKindRefusedMsg, "nhi")},
	} {
		var body map[string]any
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/users?"+tc.query, adminTok, nil, &body); code != http.StatusBadRequest || body["error"] != tc.want {
			t.Errorf("%s = %d %v, want 400 %q", tc.query, code, body["error"], tc.want)
		}
	}

	// The whole directory's counts add up to every live user.
	var e envelope
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users?limit=2", adminTok, nil, &e); code != http.StatusOK {
		t.Fatalf("users page = %d", code)
	}
	users, err := app.store.Users().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum := e.KindCounts["person"] + e.KindCounts["agent"] + e.KindCounts["service"]; sum != len(users) {
		t.Errorf("kind_counts %v sum to %d, want the %d live users", e.KindCounts, sum, len(users))
	}
}
