package server

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestApprovalsListUserFilter pins user= on both lanes of the approvals
// list: only that user's requests, AND with each state, an id no user has
// answers an empty list, the cursor walks one user's requests, and the
// list without the filter answers every user's requests in each state.
func TestApprovalsListUserFilter(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	now := time.Now().UTC()
	ids := map[string][]string{} // "user state" to approval ids
	for i, a := range []struct{ user, state string }{
		{"ann", "pending"}, {"ann", "approved"}, {"ann", "denied"}, {"ann", "expired"},
		{"bob", "pending"}, {"bob", "approved"}, {"bob", "pending"},
	} {
		rec, err := app.store.Approvals().Insert(ctx, store.Approval{
			SessionID: "ses-" + a.user, UserID: "id-" + a.user, Username: a.user, RuleID: "r",
			ArgvHash: "h" + strconv.Itoa(i), State: a.state, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[a.user+" "+a.state] = append(ids[a.user+" "+a.state], rec.ID)
		ids[a.user+" all"] = append(ids[a.user+" all"], rec.ID)
	}
	want := func(state string, users ...string) []string {
		var out []string
		for _, u := range users {
			out = append(out, ids[u+" "+state]...)
		}
		return sortedIDs(out)
	}

	bare := func(t *testing.T, query string) []string {
		t.Helper()
		var body struct {
			Approvals []map[string]any `json:"approvals"`
		}
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/approvals?"+query, adminTok, nil, &body); code != http.StatusOK {
			t.Fatalf("bare %s = %d", query, code)
		}
		var out []string
		for _, a := range body.Approvals {
			out = append(out, a["id"].(string))
		}
		return sortedIDs(out)
	}
	paged := func(t *testing.T, query string) []string {
		t.Helper()
		var out []string
		cursor := ""
		for range 20 {
			q := query + "&limit=1"
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			var e struct {
				Items      []map[string]any `json:"items"`
				NextCursor string           `json:"next_cursor"`
			}
			if code := adminReq(t, http.MethodGet, base+"/v1/admin/approvals?"+q, adminTok, nil, &e); code != http.StatusOK {
				t.Fatalf("paged %s = %d", q, code)
			}
			for _, a := range e.Items {
				out = append(out, a["id"].(string))
			}
			if e.NextCursor == "" {
				return sortedIDs(out)
			}
			cursor = e.NextCursor
		}
		t.Fatalf("%s: the cursor never ended", query)
		return nil
	}

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"user=id-ann", want("pending", "ann")},
		{"user=id-ann&state=pending", want("pending", "ann")},
		{"user=id-ann&state=approved", want("approved", "ann")},
		{"user=id-ann&state=denied", want("denied", "ann")},
		{"user=id-ann&state=expired", want("expired", "ann")},
		{"user=id-ann&state=all", want("all", "ann")},
		{"user=id-bob&state=pending", want("pending", "bob")},
		{"user=id-nobody&state=all", nil},
		{"user=id-nobody", nil},
		{"state=pending", want("pending", "ann", "bob")},
		{"state=approved", want("approved", "ann", "bob")},
		{"state=all", want("all", "ann", "bob")},
		{"state=denied", want("denied", "ann")},
	} {
		t.Run(tc.query, func(t *testing.T) {
			if got := bare(t, tc.query); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("bare lane = %v, want %v", got, tc.want)
			}
			if got := paged(t, tc.query); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("paged lane, one a page = %v, want %v", got, tc.want)
			}
		})
	}
}
