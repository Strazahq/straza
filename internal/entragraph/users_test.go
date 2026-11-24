package entragraph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

const (
	usersPath = "/v1.0/users/delta"
	// usersFirst is the first request of the users feed, which starts from now.
	usersFirst = usersPath + "?$select=id,userPrincipalName,displayName,mail,accountEnabled,identityParentId&$deltatoken=latest"
)

func TestUsersDelta(t *testing.T) {
	// deltaLink is the path of the n-th made-up delta link of the users feed.
	deltaLink := func(n int) string { return fmt.Sprintf("%s?$deltatoken=TEST-USERS-DELTATOKEN-%d", usersPath, n) }
	get := func(at, body string) step {
		return step{method: http.MethodGet, path: at, status: http.StatusOK, body: body}
	}
	// kim is the user the recorded pages disable, enable, delete and restore.
	// The pages carry no mail, because the account has none.
	kim := func(enabled *bool) UserChange {
		return UserChange{ID: userKim, Enabled: enabled, UPN: "kim@lab.example", DisplayName: "Kim Example"}
	}
	tests := []struct {
		name string
		// link is the path of the delta link handed in, empty for a fresh read.
		link  string
		steps []step
		want  []UserChange
		// next is the path of the delta link the read must answer.
		next string
	}{
		{name: "a fresh read starts from now", steps: []step{get(usersFirst, "@users-delta-latest.json")}, next: deltaLink(1)},
		{name: "a disabled user", link: deltaLink(1), steps: []step{get(deltaLink(1), "@users-delta-disabled.json")},
			want: []UserChange{kim(off)}, next: deltaLink(2)},
		{name: "an enabled user", link: deltaLink(2), steps: []step{get(deltaLink(2), "@users-delta-enabled.json")},
			want: []UserChange{kim(on)}, next: deltaLink(3)},
		{name: "a deleted user", link: deltaLink(3), steps: []step{get(deltaLink(3), "@users-delta-deleted.json")},
			want: []UserChange{{ID: userKim, Removed: true}}, next: deltaLink(4)},
		{name: "a restored user", link: deltaLink(4), steps: []step{get(deltaLink(4), "@users-delta-restored.json")},
			want: []UserChange{kim(on)}, next: deltaLink(5)},
		// The recorded page ends in a next link, so a made-up last page follows it.
		{name: "an agent user", link: deltaLink(5), steps: []step{
			get(deltaLink(5), "@users-delta-agent-user.json"),
			get(usersPath+"?$skiptoken=TEST-USERS-SKIPTOKEN-1", pageBody(`"@odata.deltaLink":"`+graphHost+deltaLink(6)+`"`)),
		}, want: []UserChange{
			{ID: agentUser, Enabled: on, UPN: "demo-agent@lab.example", DisplayName: "demo-agent user", ParentID: agentID},
		}, next: deltaLink(6)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.steps...)
			link := ""
			if tt.link != "" {
				link = f.graph.URL + tt.link
			}
			got, err := f.client.UsersDelta(context.Background(), link)
			f.done()
			if err != nil {
				t.Fatalf("UsersDelta: %s", f.show(err))
			}
			if (len(got.Users) != 0 || len(tt.want) != 0) && !reflect.DeepEqual(got.Users, tt.want) {
				t.Errorf("users =\n%s\nwant\n%s", usersText(got.Users), usersText(tt.want))
			}
			if got.Link != f.graph.URL+tt.next {
				t.Errorf("link = %s, want %s", got.Link, f.graph.URL+tt.next)
			}
		})
	}
}

func TestUsersDeltaPaging(t *testing.T) {
	const start = usersPath + "?$deltatoken=start"
	page := func(n string) string { return usersPath + "?$skiptoken=" + n }
	get := func(at string, status int, body string) step {
		return step{method: http.MethodGet, path: at, status: status, body: body}
	}
	user := func(id string, enabled bool) string { return fmt.Sprintf(`{"id":%q,"accountEnabled":%t}`, id, enabled) }
	next := func(n string, users ...string) string {
		return pageBody(`"@odata.nextLink":"`+graphHost+page(n)+`"`, users...)
	}
	last := func(users ...string) string {
		return pageBody(`"@odata.deltaLink":"`+graphHost+usersPath+`?$deltatoken=end"`, users...)
	}

	tests := []struct {
		name   string
		steps  []step
		want   []UserChange
		status int
		fail   string
	}{
		{name: "two pages", steps: []step{
			get(start, 200, next("2", user("u1", true))),
			get(page("2"), 200, last(user("u2", false))),
		}, want: []UserChange{{ID: "u1", Enabled: on}, {ID: "u2", Enabled: off}}},
		{name: "an empty page with a next link", steps: []step{
			get(start, 200, next("2")),
			get(page("2"), 200, last(user("u1", true))),
		}, want: []UserChange{{ID: "u1", Enabled: on}}},
		{name: "one account listed twice", steps: []step{
			get(start, 200, next("2", user("u1", true), user("u2", true))),
			get(page("2"), 200, last(user("u1", false))),
		}, want: []UserChange{{ID: "u1", Enabled: on}, {ID: "u2", Enabled: on}, {ID: "u1", Enabled: off}}},
		{name: "a user without an id", steps: []step{get(start, 200, last(`{"accountEnabled":true}`))}, fail: "a user that has no id"},
		{name: "a page with no list", steps: []step{
			get(start, 200, `{"@odata.deltaLink":"`+graphHost+usersPath+`?$deltatoken=end"}`),
		}, fail: "holds no list"},
		{name: "500 on page two", steps: []step{
			get(start, 200, next("2", user("u1", true))),
			get(page("2"), 500, `{"error":{"code":"generalException","message":"General exception while processing."}}`),
		}, status: 500, fail: "HTTP 500 generalException"},
		{name: "429 on page two", steps: []step{
			get(start, 200, next("2", user("u1", true))),
			{method: http.MethodGet, path: page("2"), status: 429, body: "@error-throttled.json", header: map[string]string{"Retry-After": "9"}},
		}, status: 429, fail: "wait 9 seconds"},
		{name: "410 that asks for a fresh read", steps: []step{
			{method: http.MethodGet, path: start, status: 410, body: "{}", header: map[string]string{"Location": graphHost + usersPath + "?$deltatoken="}},
		}, status: 410, fail: "HTTP 410"},
		{name: "a next link on another host", steps: []step{
			get(start, 200, pageBody(`"@odata.nextLink":"https://graph.example`+page("2")+`"`, user("u1", true))),
		}, fail: "a link that is not on"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.steps...)
			got, err := f.client.UsersDelta(context.Background(), f.graph.URL+start)
			f.done()
			if tt.fail != "" {
				f.wantError(err, tt.fail, "the read of the changed users")
				if !reflect.DeepEqual(got, UsersResult{}) {
					t.Errorf("UsersDelta = %+v, want no result on a failed read", got)
				}
				var ge *Error
				if errors.As(err, &ge) != (tt.status != 0) || (tt.status != 0 && ge.Status != tt.status) {
					t.Errorf("err = %s, want an *Error with status %d", f.show(err), tt.status)
				}
				return
			}
			if err != nil {
				t.Fatalf("UsersDelta: %s", f.show(err))
			}
			if !reflect.DeepEqual(got.Users, tt.want) || got.Link != f.graph.URL+usersPath+"?$deltatoken=end" {
				t.Errorf("UsersDelta =\n%s%s\nwant\n%sand the end link", usersText(got.Users), got.Link, usersText(tt.want))
			}
		})
	}
}

func TestUsersDeltaLinkOffGraph(t *testing.T) {
	tests := []struct {
		name string
		link func(f *fixture) string
	}{
		{"another host", func(f *fixture) string { return f.away.URL + usersPath + "?$deltatoken=TEST-USERS-DELTATOKEN-1" }},
		{"a relative link", func(*fixture) string { return usersPath + "?$deltatoken=TEST-USERS-DELTATOKEN-1" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			got, err := f.client.UsersDelta(context.Background(), tt.link(f))
			f.wantError(err, "did not follow the delta link of the users", "Start a fresh read with an empty link.")
			if !reflect.DeepEqual(got, UsersResult{}) {
				t.Errorf("UsersDelta = %+v, want no result", got)
			}
			if n := f.tokenRequests(); n != 0 {
				t.Errorf("the login double got %d token requests, want none before a refused link", n)
			}
			f.done()
		})
	}
}

// usersText prints user changes with the value behind Enabled.
func usersText(us []UserChange) string {
	out := ""
	for _, u := range us {
		out += fmt.Sprintf("  %s removed=%t enabled=%s upn=%q name=%q mail=%q parent=%q\n",
			u.ID, u.Removed, boolText(u.Enabled), u.UPN, u.DisplayName, u.Mail, u.ParentID)
	}
	return out
}
