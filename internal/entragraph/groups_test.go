package entragraph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

const (
	byNamePath = "/v1.0/groups(uniqueName='" + uniqueName + "')"
	byNameRead = byNamePath + "?$select=id,displayName,description,uniqueName"
)

// readBack is the script of the read of the group by its uniqueName.
func readBack(status int, body string) step {
	return step{method: http.MethodGet, path: byNameRead, status: status, body: body}
}

// create is the script of the create-if-missing write.
func create(status int, body string) step {
	return step{method: http.MethodPatch, path: byNamePath, status: status, body: body}
}

func TestEnsureGroup(t *testing.T) {
	immutable := `{"error":{"code":"Request_BadRequest","message":"Property 'UniqueName' is immutable and cannot be updated."}}`
	tests := []struct {
		name   string
		steps  []step
		status int // the *Error's status, or zero for none
		notYet bool
		want   string
	}{
		{name: "201 returns the created group", steps: []step{create(http.StatusCreated, "@group-created.json")}},
		{name: "204 reads the group back", steps: []step{create(http.StatusNoContent, ""), readBack(http.StatusOK, "@group-by-unique-name.json")}},
		{name: "400 name taken reads the group back", steps: []step{create(http.StatusBadRequest, "@group-name-taken.json"), readBack(http.StatusOK, "@group-by-unique-name.json")}},
		{name: "another 400 is an error", steps: []step{create(http.StatusBadRequest, immutable)}, status: http.StatusBadRequest,
			want: "HTTP 400 Request_BadRequest: Property 'UniqueName' is immutable"},
		{name: "403 is an error", steps: []step{create(http.StatusForbidden, "@error-forbidden.json")}, status: http.StatusForbidden,
			want: "the create of the group " + uniqueName + " with HTTP 403 Authorization_RequestDenied"},
		{name: "200 is not done", steps: []step{create(http.StatusOK, "@group-created.json")}, want: "where Straza expects 201 or 204"},
		{name: "201 without an id", steps: []step{create(http.StatusCreated, `{"displayName":"x"}`)}, want: "without a group that has an id"},
		{name: "the read back is not settled", steps: []step{create(http.StatusNoContent, ""), readBack(http.StatusNotFound, "@error-not-found.json")},
			status: http.StatusNotFound, notYet: true, want: "the read of the group " + uniqueName},
		{name: "the read back fails", steps: []step{create(http.StatusBadRequest, "@group-name-taken.json"), readBack(http.StatusForbidden, "@error-forbidden.json")},
			status: http.StatusForbidden, want: "HTTP 403"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.steps...)
			g, err := f.client.EnsureGroup(context.Background(), GroupSpec{UniqueName: uniqueName, DisplayName: "Straza: readers", Description: "Reads the reports."})
			f.done()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("EnsureGroup: %s", f.show(err))
				}
				if g.ID != roleGroup || g.UniqueName != uniqueName {
					t.Errorf("group = %+v, want id %s and uniqueName %s", g, roleGroup, uniqueName)
				}
				return
			}
			f.wantError(err, tt.want)
			var ge *Error
			if got := errors.As(err, &ge); got != (tt.status != 0) || (got && ge.Status != tt.status) {
				t.Errorf("err = %s, want an *Error with status %d", f.show(err), tt.status)
			}
			if errors.Is(err, ErrNotFound) != tt.notYet {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v", !tt.notYet, tt.notYet)
			}
		})
	}
}

func TestEnsureGroupRequest(t *testing.T) {
	f := newFixture(t, create(http.StatusCreated, "@group-created.json"))
	spec := GroupSpec{UniqueName: uniqueName, DisplayName: "Straza: readers", Description: "Reads the reports."}
	if _, err := f.client.EnsureGroup(context.Background(), spec); err != nil {
		t.Fatalf("EnsureGroup: %s", f.show(err))
	}
	f.done()
	req := f.requests()[0]
	if got := req.header.Get("Prefer"); got != "create-if-missing" {
		t.Errorf("Prefer = %q, want create-if-missing", got)
	}
	if got := req.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(req.body, &body); err != nil {
		t.Fatalf("the body is not JSON: %v", err)
	}
	want := map[string]string{
		"displayName":       `"Straza: readers"`,
		"description":       `"Reads the reports."`,
		"mailEnabled":       `false`,
		"mailNickname":      `"` + uniqueName + `"`,
		"securityEnabled":   `true`,
		"groupTypes":        `[]`,
		"owners@odata.bind": `["` + f.graph.URL + `/v1.0/servicePrincipals/` + testOwner + `"]`,
	}
	for k, v := range want {
		if string(body[k]) != v {
			t.Errorf("body %s = %s, want %s", k, body[k], v)
		}
	}
	if len(body) != len(want) {
		t.Errorf("the body has %d fields, want %d", len(body), len(want))
	}
}

func TestGroupByUniqueName(t *testing.T) {
	tests := []struct {
		name     string
		step     step
		notFound bool
		want     string
	}{
		{name: "200", step: readBack(http.StatusOK, "@group-by-unique-name.json")},
		{name: "404", step: readBack(http.StatusNotFound, "@error-not-found.json"), notFound: true, want: "HTTP 404 Request_ResourceNotFound"},
		{name: "200 without an id", step: readBack(http.StatusOK, `{"value":[]}`), want: "without a group that has an id"},
		{name: "204", step: readBack(http.StatusNoContent, ""), want: "where Straza expects 200"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.step)
			g, err := f.client.GroupByUniqueName(context.Background(), uniqueName)
			f.done()
			if tt.want == "" {
				if err != nil || g.ID != roleGroup || g.DisplayName != uniqueName || g.Description == "" {
					t.Errorf("GroupByUniqueName = %+v, %s, want the group %s", g, f.show(err), roleGroup)
				}
				return
			}
			f.wantError(err, tt.want)
			if errors.Is(err, ErrNotFound) != tt.notFound {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v", !tt.notFound, tt.notFound)
			}
		})
	}

	f := newFixture(t, step{method: http.MethodGet, path: "/v1.0/groups(uniqueName='a%20b%2Fc%3Fd')?$select=id,displayName,description,uniqueName",
		status: http.StatusOK, body: "@group-by-unique-name.json"})
	if _, err := f.client.GroupByUniqueName(context.Background(), "a b/c?d"); err != nil {
		t.Errorf("GroupByUniqueName of a name to escape: %s", f.show(err))
	}
	f.done()
}

func TestUpdateAndDeleteGroup(t *testing.T) {
	update := func(c *Client, id string) error {
		return c.UpdateGroup(context.Background(), id, "Straza: writers", "Writes the reports.")
	}
	remove := func(c *Client, id string) error { return c.DeleteGroup(context.Background(), id) }
	tests := []struct {
		name     string
		call     func(*Client, string) error
		id       string
		step     step
		notFound bool
		want     string
	}{
		{name: "update 204", call: update, id: roleGroup, step: step{http.MethodPatch, "/v1.0/groups/" + roleGroup, http.StatusNoContent, "", nil}},
		{name: "update escapes the id", call: update, id: "a/b c?d", step: step{http.MethodPatch, "/v1.0/groups/a%2Fb%20c%3Fd", http.StatusNoContent, "", nil}},
		{name: "update 403", call: update, id: roleGroup, step: step{http.MethodPatch, "/v1.0/groups/" + roleGroup, http.StatusForbidden, "@error-forbidden.json", nil},
			want: "the update of group " + roleGroup + " with HTTP 403 Authorization_RequestDenied: Insufficient privileges to complete the operation."},
		{name: "update 200", call: update, id: roleGroup, step: step{http.MethodPatch, "/v1.0/groups/" + roleGroup, http.StatusOK, "{}", nil}, want: "where Straza expects 204"},
		{name: "delete 204", call: remove, id: roleGroup, step: step{http.MethodDelete, "/v1.0/groups/" + roleGroup, http.StatusNoContent, "", nil}},
		{name: "delete 404", call: remove, id: roleGroup, step: step{http.MethodDelete, "/v1.0/groups/" + roleGroup, http.StatusNotFound, "@error-not-found.json", nil},
			notFound: true, want: "the delete of group " + roleGroup},
		{name: "delete 403", call: remove, id: roleGroup, step: step{http.MethodDelete, "/v1.0/groups/" + roleGroup, http.StatusForbidden, "@error-forbidden.json", nil}, want: "HTTP 403"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.step)
			err := tt.call(f.client, tt.id)
			f.done()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("err = %s, want nil", f.show(err))
				}
			} else {
				f.wantError(err, tt.want)
			}
			if errors.Is(err, ErrNotFound) != tt.notFound {
				t.Errorf("errors.Is(err, ErrNotFound) = %v, want %v", !tt.notFound, tt.notFound)
			}
		})
	}

	f := newFixture(t, step{http.MethodPatch, "/v1.0/groups/" + roleGroup, http.StatusNoContent, "", nil})
	if err := update(f.client, roleGroup); err != nil {
		t.Fatalf("UpdateGroup: %s", f.show(err))
	}
	if got := string(f.requests()[0].body); got != `{"displayName":"Straza: writers","description":"Writes the reports."}` {
		t.Errorf("the update body = %s", got)
	}
}

func TestRefusedBeforeAnyRequest(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		call func(c *Client, away string) error
		want string
	}{
		{"uniqueName with a quote", func(c *Client, _ string) error {
			_, err := c.EnsureGroup(ctx, GroupSpec{UniqueName: "straza-x'or'1", DisplayName: "x"})
			return err
		}, "single quote"},
		{"empty uniqueName", func(c *Client, _ string) error {
			_, err := c.EnsureGroup(ctx, GroupSpec{DisplayName: "x"})
			return err
		}, "uniqueName is empty"},
		{"no display name", func(c *Client, _ string) error {
			_, err := c.EnsureGroup(ctx, GroupSpec{UniqueName: uniqueName})
			return err
		}, "no display name"},
		{"read with a quote", func(c *Client, _ string) error {
			_, err := c.GroupByUniqueName(ctx, "a'b")
			return err
		}, "single quote"},
		{"update without an id", func(c *Client, _ string) error { return c.UpdateGroup(ctx, "", "x", "y") }, "ID is empty"},
		{"delete without an id", func(c *Client, _ string) error { return c.DeleteGroup(ctx, "") }, "ID is empty"},
		{"members without an id", func(c *Client, _ string) error {
			_, err := c.Members(ctx, "")
			return err
		}, "ID is empty"},
		{"transitive members without an id", func(c *Client, _ string) error {
			_, err := c.TransitiveMembers(ctx, "")
			return err
		}, "ID is empty"},
		{"delta of no group", func(c *Client, _ string) error {
			_, err := c.Delta(ctx, "", nil)
			return err
		}, "every group in the tenant"},
		{"delta of an empty id", func(c *Client, _ string) error {
			_, err := c.Delta(ctx, "", []string{roleGroup, ""})
			return err
		}, "ID is empty"},
		{"delta of an id with a quote", func(c *Client, _ string) error {
			_, err := c.Delta(ctx, "", []string{"x' or id ne 'y"})
			return err
		}, "single quote"},
		{"delta link off Graph", func(c *Client, away string) error {
			_, err := c.Delta(ctx, away+"/v1.0/groups/delta?$deltatoken=x", nil)
			return err
		}, "Start a fresh delta read"},
		{"a call off Graph", func(c *Client, away string) error {
			_, err := c.call(ctx, "a test request", http.MethodGet, away+"/v1.0/groups", nil, "")
			return err
		}, "the only host that gets its Graph token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.wantError(tt.call(f.client, f.away.URL), tt.want)
			if f.tokenRequests() != 0 {
				t.Errorf("token requests = %d, want none", f.tokenRequests())
			}
			f.done()
		})
	}
}
