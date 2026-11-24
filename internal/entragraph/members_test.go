package entragraph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

// on and off are the two values of accountEnabled.
var on, off = new(true), new(false)

func TestMembersShapes(t *testing.T) {
	tests := []struct {
		name string
		// transitive serves body to a transitiveMembers read instead of a members read.
		transitive bool
		body       string
		want       []Member
	}{
		{name: "plain member read", body: "@members.json", want: []Member{
			{ID: userPat, Kind: KindUser, UPN: "pat@lab.example", DisplayName: "pat"},
			{ID: userKim, Kind: KindUser, UPN: "kim@lab.example", DisplayName: "kim"},
			{ID: agentUser, Kind: KindAgentUser, ParentID: agentID, UPN: "demo-agent@lab.example", DisplayName: "demo-agent user"},
			{ID: nestedGroup, Kind: KindGroup, DisplayName: "straza-test-nested"},
		}},
		{name: "accountEnabled with User.Read.All", body: "@members-enabled.json", want: []Member{
			{ID: userPat, Kind: KindUser, Enabled: on, DisplayName: "pat"},
			{ID: agentUser, Kind: KindAgentUser, Enabled: on, DisplayName: "demo-agent user"},
			{ID: agentID, Kind: KindOther, Enabled: on, DisplayName: "demo-agent"},
			{ID: userKim, Kind: KindUser, Enabled: off, DisplayName: "kim"},
			{ID: nestedGroup, Kind: KindGroup, DisplayName: "straza-test-nested"},
		}},
		{name: "accountEnabled null without User.Read.All", body: "@members-enabled-unknown.json", want: []Member{
			{ID: userPat, Kind: KindUser, DisplayName: "pat"},
			{ID: agentUser, Kind: KindAgentUser, DisplayName: "demo-agent user"},
			{ID: agentID, Kind: KindOther, Enabled: on, DisplayName: "demo-agent"},
			{ID: userKim, Kind: KindUser, DisplayName: "kim"},
			{ID: nestedGroup, Kind: KindGroup, DisplayName: "straza-test-nested"},
		}},
		// Under the client's $select Graph leaves identityParent out, so the
		// agent user has no ParentID and only its Kind tells it from a person.
		{name: "the client's $select with User.Read.All", transitive: true, body: "@members-select.json", want: []Member{
			{ID: userPat, Kind: KindUser, Enabled: on, UPN: "pat@lab.example", DisplayName: "pat"},
			{ID: userKim, Kind: KindUser, Enabled: on, UPN: "kim@lab.example", DisplayName: "Kim Example"},
			{ID: agentUser, Kind: KindAgentUser, Enabled: on, UPN: "demo-agent@lab.example", DisplayName: "demo-agent user"},
		}},
		{name: "the client's $select after a user was disabled", transitive: true, body: "@members-select-disabled.json", want: []Member{
			{ID: userPat, Kind: KindUser, Enabled: on, UPN: "pat@lab.example", DisplayName: "pat"},
			{ID: userKim, Kind: KindUser, Enabled: off, UPN: "kim@lab.example", DisplayName: "Kim Example"},
			{ID: agentUser, Kind: KindAgentUser, Enabled: on, UPN: "demo-agent@lab.example", DisplayName: "demo-agent user"},
		}},
		{name: "every name of a user", body: `{"value":[{"@odata.type":"#microsoft.graph.user","id":"u1","userPrincipalName":"ana@example.com","displayName":"Ana Example","mail":"ana.example@example.com","accountEnabled":true}]}`, want: []Member{
			{ID: "u1", Kind: KindUser, Enabled: on, UPN: "ana@example.com", DisplayName: "Ana Example", Mail: "ana.example@example.com"},
		}},
		{name: "empty group", body: `{"value":[]}`, want: []Member{}},
		{name: "unknown type and no type", body: `{"value":[{"@odata.type":"#microsoft.graph.device","id":"d1"},{"id":"d2"}]}`, want: []Member{
			{ID: "d1", Kind: KindOther}, {ID: "d2", Kind: KindOther},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collection, read := "members", (*Client).Members
			if tt.transitive {
				collection, read = "transitiveMembers", (*Client).TransitiveMembers
			}
			f := newFixture(t, step{method: http.MethodGet, path: "/v1.0/groups/" + roleGroup + "/" + collection, status: http.StatusOK, body: tt.body})
			got, err := read(f.client, context.Background(), roleGroup)
			f.done()
			if err != nil {
				t.Fatalf("%s: %s", collection, f.show(err))
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Members =\n%s\nwant\n%s", membersText(got), membersText(tt.want))
			}
		})
	}
}

func TestMembersQuery(t *testing.T) {
	tests := []struct {
		name string
		path string
		read func(*Client) ([]Member, error)
	}{
		{"members", "/v1.0/groups/" + roleGroup + "/members?$select=id,userPrincipalName,displayName,mail,accountEnabled",
			func(c *Client) ([]Member, error) { return c.Members(context.Background(), roleGroup) }},
		{"transitiveMembers", "/v1.0/groups/" + roleGroup + "/transitiveMembers?$select=id,userPrincipalName,displayName,mail,accountEnabled",
			func(c *Client) ([]Member, error) { return c.TransitiveMembers(context.Background(), roleGroup) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, step{method: http.MethodGet, path: tt.path, status: http.StatusOK, body: memberPage("", "u1")})
			got, err := tt.read(f.client)
			f.done()
			if err != nil {
				t.Fatalf("read: %s", f.show(err))
			}
			if len(got) != 1 || got[0].ID != "u1" {
				t.Errorf("members = %v, want u1", got)
			}
		})
	}
}

// membersText prints members with the value behind Enabled and their names.
func membersText(ms []Member) string {
	out := ""
	for _, m := range ms {
		out += fmt.Sprintf("  %s %s parent=%q enabled=%s upn=%q name=%q mail=%q\n",
			m.ID, m.Kind, m.ParentID, boolText(m.Enabled), m.UPN, m.DisplayName, m.Mail)
	}
	return out
}

// boolText prints the value behind b, or nil.
func boolText(b *bool) string {
	if b == nil {
		return "nil"
	}
	return fmt.Sprint(*b)
}

func TestMembersPaging(t *testing.T) {
	read := map[string]func(*Client) ([]Member, error){
		"members": func(c *Client) ([]Member, error) {
			return c.Members(context.Background(), roleGroup)
		},
		"transitiveMembers": func(c *Client) ([]Member, error) {
			return c.TransitiveMembers(context.Background(), roleGroup)
		},
	}
	for collection, call := range read {
		path := "/v1.0/groups/" + roleGroup + "/" + collection
		pageAt := func(n int) string { return fmt.Sprintf("%s?$skiptoken=page%d", path, n) }
		get := func(at string, status int, body string) step {
			return step{method: http.MethodGet, path: at, status: status, body: body}
		}
		tests := []struct {
			name   string
			steps  []step
			want   []string
			status int
			fail   string
		}{
			{name: "one page", steps: []step{get(path, 200, memberPage("", "u1", "u2"))}, want: []string{"u1", "u2"}},
			{name: "two pages", steps: []step{
				get(path, 200, memberPage(pageAt(2), "u1")),
				get(pageAt(2), 200, memberPage("", "u2", "u3")),
			}, want: []string{"u1", "u2", "u3"}},
			{name: "three pages", steps: []step{
				get(path, 200, memberPage(pageAt(2), "u1")),
				get(pageAt(2), 200, memberPage(pageAt(3), "u2")),
				get(pageAt(3), 200, memberPage("", "u3")),
			}, want: []string{"u1", "u2", "u3"}},
			{name: "500 on page two", steps: []step{
				get(path, 200, memberPage(pageAt(2), "u1")),
				get(pageAt(2), 500, `{"error":{"code":"generalException","message":"General exception while processing."}}`),
			}, status: 500, fail: "HTTP 500 generalException"},
			{name: "429 on page three", steps: []step{
				get(path, 200, memberPage(pageAt(2), "u1")),
				get(pageAt(2), 200, memberPage(pageAt(3), "u2")),
				{method: http.MethodGet, path: pageAt(3), status: 429, body: "@error-throttled.json", header: map[string]string{"Retry-After": "9"}},
			}, status: 429, fail: "wait 9 seconds"},
			{name: "404 on a new group", steps: []step{get(path, 404, "@error-not-found.json")}, status: 404, fail: "HTTP 404"},
			{name: "a page with no list", steps: []step{
				get(path, 200, memberPage(pageAt(2), "u1")),
				get(pageAt(2), 200, `{"@odata.context":"x"}`),
			}, fail: "holds no list"},
			{name: "a page whose list is null", steps: []step{get(path, 200, `{"value":null}`)}, fail: "holds no list"},
			{name: "a page that is not JSON", steps: []step{get(path, 200, "<html>")}, fail: "holds no list"},
			{name: "a list Straza cannot read", steps: []step{get(path, 200, `{"value":{"id":"u1"}}`)}, fail: "cannot read"},
			{name: "a member without an id on page two", steps: []step{
				get(path, 200, memberPage(pageAt(2), "u1")),
				get(pageAt(2), 200, `{"value":[{"@odata.type":"#microsoft.graph.user"}]}`),
			}, fail: "a member that has no id"},
			{name: "a 204 page", steps: []step{get(path, 204, "")}, fail: "where Straza expects 200"},
		}
		for _, tt := range tests {
			t.Run(collection+"/"+tt.name, func(t *testing.T) {
				f := newFixture(t, tt.steps...)
				got, err := call(f.client)
				f.done()
				if tt.fail != "" {
					f.wantError(err, tt.fail, "group "+roleGroup)
					if got != nil {
						t.Errorf("members = %v, want none on a failed read", got)
					}
					var ge *Error
					if errors.As(err, &ge) != (tt.status != 0) || (tt.status != 0 && ge.Status != tt.status) {
						t.Errorf("err = %s, want an *Error with status %d", f.show(err), tt.status)
					}
					return
				}
				if err != nil {
					t.Fatalf("read: %s", f.show(err))
				}
				var ids []string
				for _, m := range got {
					ids = append(ids, m.ID)
				}
				if !reflect.DeepEqual(ids, tt.want) {
					t.Errorf("members = %v, want %v", ids, tt.want)
				}
			})
		}
	}
}
