package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// sessionsAnswerFixture is a fleet for the sessions answer: kim, a person
// signed in to the console, three sessions of the agent bot-a that kim
// sponsors, one of them revoked, one session of the service account svc-b,
// and one of gone-d, whose user row was deleted after it checked in.
type sessionsAnswerFixture struct {
	app      *App
	base     string
	adminTok string
	users    map[string]string   // username to user id
	sessions map[string][]string // username to session ids, oldest first
}

func newSessionsAnswerFixture(t *testing.T) sessionsAnswerFixture {
	t.Helper()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, kimSession := checkinToken(t, app, base)
	f := sessionsAnswerFixture{app: app, base: base, adminTok: adminTok,
		users: map[string]string{"kim": kim.ID}, sessions: map[string][]string{"kim": {kimSession}}}
	for _, u := range []store.User{
		{Username: "bot-a", UserType: store.UserTypeAgent, Sponsor: "kim"},
		{Username: "svc-b", UserType: store.UserTypeService},
		{Username: "gone-d"},
	} {
		created, err := app.store.Users().Create(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		f.users[u.Username] = created.ID
	}
	for _, name := range []string{"bot-a", "bot-a", "bot-a", "svc-b", "gone-d"} {
		ses, err := app.store.Sessions().Create(ctx, store.Session{UserID: f.users[name], HarnessName: "claude-code", HarnessVersion: "2.1.0"})
		if err != nil {
			t.Fatal(err)
		}
		f.sessions[name] = append(f.sessions[name], ses.ID)
	}
	if err := app.store.Sessions().SetStatus(ctx, f.sessions["bot-a"][2], store.SessionRevoked); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Users().SoftDelete(ctx, f.users["gone-d"], nil); err != nil {
		t.Fatal(err)
	}
	// bot-a's first session waits on two requests. Its decided and expired
	// requests, and svc-b's waiting one, are not its own waiting.
	now := time.Now().UTC()
	for i, a := range []struct {
		owner, state string
		expires      time.Time
	}{
		{"bot-a", "pending", now.Add(time.Hour)},
		{"bot-a", "pending", now.Add(10 * time.Minute)},
		{"bot-a", "approved", now.Add(time.Hour)},
		{"bot-a", "pending", now.Add(-time.Minute)},
		{"svc-b", "pending", now.Add(time.Hour)},
	} {
		if _, err := app.store.Approvals().Insert(ctx, store.Approval{
			SessionID: f.sessions[a.owner][0], UserID: f.users[a.owner], Username: a.owner,
			RuleID: "r", ArgvHash: "h" + strconv.Itoa(i), State: a.state,
			CreatedAt: now.Add(-time.Hour), ExpiresAt: a.expires,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// page reads one page of the paged lane and answers its rows and cursor.
func (f sessionsAnswerFixture) page(t *testing.T, query string) ([]map[string]any, string) {
	t.Helper()
	var e struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	if code := adminReq(t, http.MethodGet, f.base+"/v1/admin/sessions?"+query, f.adminTok, nil, &e); code != http.StatusOK {
		t.Fatalf("GET /v1/admin/sessions?%s = %d", query, code)
	}
	return e.Items, e.NextCursor
}

// walk reads every page of query, size rows a page, and answers the ids in
// the order read.
func (f sessionsAnswerFixture) walk(t *testing.T, query string, size int) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for range 20 {
		q := query + "&limit=" + strconv.Itoa(size)
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		rows, next := f.page(t, q)
		for _, r := range rows {
			ids = append(ids, r["id"].(string))
		}
		if next == "" {
			return ids
		}
		cursor = next
	}
	t.Fatalf("%s: the cursor never ended", query)
	return nil
}

// TestSessionsAnswerFields pins the three fields of a session row on both
// lanes of the list: user_kind and sponsor from the owner's user row,
// left out when that row is gone, and waiting, which counts only the
// session's own pending requests that have not expired and is left out at
// zero.
func TestSessionsAnswerFields(t *testing.T) {
	t.Parallel()
	f := newSessionsAnswerFixture(t)
	want := map[string]map[string]any{
		f.sessions["kim"][0]:    {"user_kind": "person"},
		f.sessions["bot-a"][0]:  {"user_kind": "agent", "sponsor": "kim", "waiting": 2.0},
		f.sessions["bot-a"][1]:  {"user_kind": "agent", "sponsor": "kim"},
		f.sessions["svc-b"][0]:  {"user_kind": "service", "waiting": 1.0},
		f.sessions["gone-d"][0]: {},
	}
	var bare []map[string]any
	if code := adminReq(t, http.MethodGet, f.base+"/v1/admin/sessions", f.adminTok, nil, &bare); code != http.StatusOK {
		t.Fatalf("bare lane = %d", code)
	}
	paged, _ := f.page(t, "limit=50")
	for lane, rows := range map[string][]map[string]any{"bare": bare, "paged": paged} {
		seen := 0
		for _, row := range rows {
			fields, ok := want[row["id"].(string)]
			if !ok {
				continue
			}
			seen++
			for _, key := range []string{"user_kind", "sponsor", "waiting"} {
				got, present := row[key]
				if w, wanted := fields[key]; wanted != present || got != w {
					t.Errorf("%s lane, session of %v: %s = %v (present %v), want %v (present %v)",
						lane, row["username"], key, got, present, w, wanted)
				}
			}
		}
		if seen != len(want) {
			t.Errorf("%s lane listed %d of the %d sessions", lane, seen, len(want))
		}
	}
}

// TestSessionsSearch pins q on the paged lane: a part of a username or the
// start of a session id without regard to case, AND with status and user,
// under every sort with the cursor reading each match once, refused on the
// bare lane and over 128 bytes, and an empty page when nothing matches.
func TestSessionsSearch(t *testing.T) {
	t.Parallel()
	f := newSessionsAnswerFixture(t)
	bot, svc := f.sessions["bot-a"], f.sessions["svc-b"][0]
	cases := []struct {
		name, query string
		want        []string
	}{
		{"a part of a name in another case", "q=OT-", bot},
		{"the start of an id", "q=" + svc[:len(svc)-4], []string{svc}},
		{"with a status", "q=bot&status=active", bot[:2]},
		{"with the owner as user", "q=bot&user=" + f.users["bot-a"], bot},
		{"with another user", "q=bot&user=" + f.users["svc-b"], nil},
		{"spaces around it", "q=%20bot%20", bot},
		{"matching nothing", "q=zz-nobody", nil},
		{"128 bytes", "q=" + strings.Repeat("a", 128), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.walk(t, tc.query, 50); !reflect.DeepEqual(sortedIDs(got), sortedIDs(tc.want)) {
				t.Errorf("%s = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
	for _, sort := range []string{"started", "last_seen", "user", "status", "attestation"} {
		for _, order := range []string{"asc", "desc"} {
			query := "q=bot&sort=" + sort + "&order=" + order
			if got := f.walk(t, query, 1); !reflect.DeepEqual(sortedIDs(got), sortedIDs(bot)) {
				t.Errorf("%s, one row a page = %v, want each of %v once", query, got, bot)
			}
		}
	}
	if rows, next := f.page(t, "q=zz-nobody&limit=5"); len(rows) != 0 || next != "" {
		t.Errorf("a search matching nothing = %v, cursor %q, want no rows and no cursor", rows, next)
	}
	for _, tc := range []struct{ query, want string }{
		{"q=bot", sessionSearchBareMsg},
		{"q=bot&status=active", sessionSearchBareMsg},
		{"limit=5&q=" + strings.Repeat("a", 129), fmt.Sprintf(sessionSearchLongMsg, 129, 128)},
	} {
		var body map[string]any
		if code := adminReq(t, http.MethodGet, f.base+"/v1/admin/sessions?"+tc.query, f.adminTok, nil, &body); code != http.StatusBadRequest || body["error"] != tc.want {
			t.Errorf("%s = %d %v, want 400 %q", tc.query, code, body["error"], tc.want)
		}
	}
}

// TestSessionGet pins the read of one session: the list's row for a known
// id, valid against the Session schema, a 404 for an id no session has, a
// 403 without the sessions area, and the segment revoke, which a GET does
// not read as an id while the bulk revoke still answers POST.
func TestSessionGet(t *testing.T) {
	t.Parallel()
	f := newSessionsAnswerFixture(t)
	target := f.sessions["bot-a"][0]
	var listed map[string]any
	rows, _ := f.page(t, "limit=50")
	for _, r := range rows {
		if r["id"] == target {
			listed = r
		}
	}
	var got map[string]any
	if code := adminReq(t, http.MethodGet, f.base+"/v1/admin/sessions/"+target, f.adminTok, nil, &got); code != http.StatusOK {
		t.Fatalf("read of %s = %d", target, code)
	}
	if !reflect.DeepEqual(got, listed) {
		t.Errorf("read = %v, want the list's row %v", got, listed)
	}
	if got["user_kind"] != "agent" || got["sponsor"] != "kim" || got["waiting"] != 2.0 {
		t.Errorf("read = %v, want an agent sponsored by kim with 2 waiting", got)
	}
	if err := openAPISchema(t, "Session").Validate(got); err != nil {
		t.Errorf("the read does not match the Session schema: %v", err)
	}

	var body map[string]any
	if code := adminReq(t, http.MethodGet, f.base+"/v1/admin/sessions/nope", f.adminTok, nil, &body); code != http.StatusNotFound || body["error"] != fmt.Sprintf(noSessionMsg, "nope") {
		t.Errorf("unknown id = %d %v, want 404 %q", code, body["error"], fmt.Sprintf(noSessionMsg, "nope"))
	}

	code, raw, header := adminBytes(t, http.MethodGet, f.base+"/v1/admin/sessions/revoke", f.adminTok, "", nil)
	if code != http.StatusMethodNotAllowed || header.Get("Allow") != http.MethodPost || !strings.Contains(string(raw), "reads no session") {
		t.Errorf("GET of revoke = %d, Allow %q, %s; want 405 with Allow POST", code, header.Get("Allow"), raw)
	}
	body = nil
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/sessions/revoke", f.adminTok, map[string]any{}, &body); code != http.StatusBadRequest || body["error"] != "exactly one of sessions[] or user is required" {
		t.Errorf("POST of revoke = %d %v, want the bulk revoke's 400", code, body["error"])
	}

	for _, tc := range []struct {
		scope string
		want  int
	}{
		{"identity:read", http.StatusForbidden},
		{"sessions:read", http.StatusOK},
	} {
		var minted struct {
			Token string `json:"token"`
		}
		if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/api-tokens", f.adminTok,
			map[string]any{"name": "read-" + strings.ReplaceAll(tc.scope, ":", "-"), "scope": tc.scope}, &minted); code != http.StatusCreated {
			t.Fatalf("mint %s = %d", tc.scope, code)
		}
		code, raw, _ := adminBytes(t, http.MethodGet, f.base+"/v1/admin/sessions/"+target, minted.Token, "", nil)
		if code != tc.want || (code == http.StatusForbidden && !strings.Contains(string(raw), "sessions:read")) {
			t.Errorf("read with %s = %d %s, want %d", tc.scope, code, raw, tc.want)
		}
	}
}

// TestOpenAPINamesTheSessionsAnswer pins the contract of the sessions
// answer: the Session schema names user_kind, sponsor and waiting, the list
// names q, and the read of one session answers a Session.
func TestOpenAPINamesTheSessionsAnswer(t *testing.T) {
	t.Parallel()
	doc := loadOpenAPIDoc(t)
	props, _ := doc.properties(asMap(asMap(asMap(doc["components"])["schemas"])["Session"]))
	for _, field := range []string{"user_kind", "sponsor", "waiting"} {
		if _, ok := props[field]; !ok {
			t.Errorf("the Session schema does not name %s", field)
		}
	}
	paths := asMap(doc["paths"])
	named := false
	params, _ := asMap(asMap(paths["/v1/admin/sessions"])["get"])["parameters"].([]any)
	for _, p := range params {
		if asMap(p)["name"] == "q" {
			named = true
		}
	}
	if !named {
		t.Error("GET /v1/admin/sessions does not name the parameter q")
	}
	ok := asMap(asMap(asMap(asMap(paths["/v1/admin/sessions/{id}"])["get"])["responses"])["200"])
	schema := asMap(asMap(asMap(ok["content"])["application/json"])["schema"])
	if schema["$ref"] != "#/components/schemas/Session" {
		t.Errorf("GET /v1/admin/sessions/{id} answers %v, want a Session", schema)
	}
}

// sortedIDs answers ids sorted, and nil for none, so two sets compare equal.
func sortedIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}
