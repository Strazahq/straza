package entragraph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const (
	// plainSP is a made-up service principal of a plain application.
	plainSP = "5a5a5a5a-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	// castPath is the read of the role group's service principals.
	castPath = "/v1.0/groups/" + roleGroup + "/members/microsoft.graph.servicePrincipal"
	// transitivePath is the read of the role group's transitive members.
	transitivePath = "/v1.0/groups/" + roleGroup + "/transitiveMembers"
)

// entries is a page of a member read that lists the JSON objects items and
// links to next, a path on the Graph double, when next is not empty.
func entries(next string, items ...string) string {
	body := `{"value":[` + strings.Join(items, ",") + `]`
	if next != "" {
		body += `,"@odata.nextLink":"` + graphHost + next + `"`
	}
	return body + "}"
}

func TestAgentIdentities(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []Member
	}{
		{name: "the recorded agent identity", body: "@members-agent-identity.json", want: []Member{
			{ID: agentID, Kind: KindAgentIdentity, Enabled: on, DisplayName: "demo-agent"},
		}},
		{name: "an agent identity and plain service principals", body: entries("",
			`{"@odata.type":"#microsoft.graph.agentIdentity","id":"`+agentID+`","displayName":"demo-agent","accountEnabled":false}`,
			`{"@odata.type":"#microsoft.graph.servicePrincipal","id":"`+plainSP+`","displayName":"payroll app","accountEnabled":true}`,
			`{"id":"sp-without-type","displayName":"no type"}`,
		), want: []Member{
			{ID: agentID, Kind: KindAgentIdentity, Enabled: off, DisplayName: "demo-agent"},
			{ID: plainSP, Kind: KindServicePrincipal, Enabled: on, DisplayName: "payroll app"},
			{ID: "sp-without-type", Kind: KindServicePrincipal, DisplayName: "no type"},
		}},
		{name: "a group with no service principal", body: `{"value":[]}`, want: []Member{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, step{method: http.MethodGet, path: castPath, status: http.StatusOK, body: tt.body})
			got, err := f.client.AgentIdentities(context.Background(), roleGroup)
			f.done()
			if err != nil {
				t.Fatalf("AgentIdentities: %s", f.show(err))
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AgentIdentities =\n%s\nwant\n%s", membersText(got), membersText(tt.want))
			}
			if uri := f.requests()[0].uri; uri != castPath {
				t.Errorf("request = %s, want %s with no query", uri, castPath)
			}
		})
	}
}

func TestAgentUserParents(t *testing.T) {
	agentUserWith := func(id, parent string) string {
		if parent == "" {
			return fmt.Sprintf(`{"@odata.type":"#microsoft.graph.agentUser","id":%q}`, id)
		}
		return fmt.Sprintf(`{"@odata.type":"#microsoft.graph.agentUser","id":%q,"identityParent":{"id":%q}}`, id, parent)
	}
	tests := []struct {
		name string
		body string
		want map[string]string
	}{
		{name: "the recorded read with two people", body: "@transitive-members.json", want: map[string]string{agentUser: agentID}},
		{name: "an agent user without a parent", body: entries("", agentUserWith(agentUser, ""), agentUserWith("au-2", "parent-2")),
			want: map[string]string{"au-2": "parent-2"}},
		{name: "a parent on a person is not read", body: entries("",
			`{"@odata.type":"#microsoft.graph.user","id":"`+userPat+`","identityParent":{"id":"`+agentID+`"}}`),
			want: map[string]string{}},
		{name: "a group with no agent user", body: `{"value":[]}`, want: map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, step{method: http.MethodGet, path: transitivePath, status: http.StatusOK, body: tt.body})
			got, err := f.client.AgentUserParents(context.Background(), roleGroup)
			f.done()
			if err != nil {
				t.Fatalf("AgentUserParents: %s", f.show(err))
			}
			if got == nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AgentUserParents = %#v, want %#v", got, tt.want)
			}
			if uri := f.requests()[0].uri; uri != transitivePath {
				t.Errorf("request = %s, want %s with no query", uri, transitivePath)
			}
		})
	}
}

func TestAgentReadsPaging(t *testing.T) {
	agentIdentity := func(id string) string {
		return `{"@odata.type":"#microsoft.graph.agentIdentity","id":"` + id + `"}`
	}
	agentUserOf := func(id string) string {
		return `{"@odata.type":"#microsoft.graph.agentUser","id":"` + id + `","identityParent":{"id":"p-` + id + `"}}`
	}
	reads := map[string]struct {
		path string
		// entry is one entry of a page that the read answers as id.
		entry func(id string) string
		// ids are the IDs the read answered, nil when it answered nothing.
		ids func(*Client) ([]string, error)
	}{
		"AgentIdentities": {castPath, agentIdentity, func(c *Client) ([]string, error) {
			ms, err := c.AgentIdentities(context.Background(), roleGroup)
			if ms == nil {
				return nil, err
			}
			ids := []string{}
			for _, m := range ms {
				ids = append(ids, m.ID)
			}
			return ids, err
		}},
		"AgentUserParents": {transitivePath, agentUserOf, func(c *Client) ([]string, error) {
			parents, err := c.AgentUserParents(context.Background(), roleGroup)
			if parents == nil {
				return nil, err
			}
			ids := []string{}
			for id, parent := range parents {
				if parent != "p-"+id {
					return nil, fmt.Errorf("agent user %s has the parent %s, want p-%s", id, parent, id)
				}
				ids = append(ids, id)
			}
			sort.Strings(ids)
			return ids, err
		}},
	}
	for name, read := range reads {
		pageAt := func(n int) string { return fmt.Sprintf("%s?$skiptoken=page%d", read.path, n) }
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
			{name: "two pages", steps: []step{
				get(read.path, 200, entries(pageAt(2), read.entry("a1"))),
				get(pageAt(2), 200, entries("", read.entry("a2"), read.entry("a3"))),
			}, want: []string{"a1", "a2", "a3"}},
			{name: "500 on page two", steps: []step{
				get(read.path, 200, entries(pageAt(2), read.entry("a1"))),
				get(pageAt(2), 500, `{"error":{"code":"generalException","message":"General exception while processing."}}`),
			}, status: 500, fail: "HTTP 500 generalException"},
			{name: "403 on page one", steps: []step{get(read.path, 403, "@error-forbidden.json")}, status: 403, fail: "HTTP 403 Authorization_RequestDenied"},
			{name: "an entry without an id on page two", steps: []step{
				get(read.path, 200, entries(pageAt(2), read.entry("a1"))),
				get(pageAt(2), 200, entries("", `{"displayName":"x"}`)),
			}, fail: "a member that has no id"},
		}
		for _, tt := range tests {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				f := newFixture(t, tt.steps...)
				got, err := read.ids(f.client)
				f.done()
				if tt.fail == "" {
					if err != nil || !reflect.DeepEqual(got, tt.want) {
						t.Errorf("read = %v, %s, want %v", got, f.show(err), tt.want)
					}
					return
				}
				f.wantError(err, tt.fail, "group "+roleGroup)
				if got != nil {
					t.Errorf("read = %v, want nothing on a failed read", got)
				}
				var ge *Error
				if errors.As(err, &ge) != (tt.status != 0) || (tt.status != 0 && ge.Status != tt.status) {
					t.Errorf("err = %s, want an *Error with status %d", f.show(err), tt.status)
				}
			})
		}
	}
}

func TestAgentReadsRefuseEmptyGroup(t *testing.T) {
	f := newFixture(t)
	_, err := f.client.AgentIdentities(context.Background(), "")
	f.wantError(err, "ID is empty")
	_, err = f.client.AgentUserParents(context.Background(), "")
	f.wantError(err, "ID is empty")
	if n := f.tokenRequests(); n != 0 {
		t.Errorf("token requests = %d, want none", n)
	}
	f.done()
}
