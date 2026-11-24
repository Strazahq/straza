package entragraph

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

const (
	deltaPath = "/v1.0/groups/delta"
	// deltaFirst is the first delta request for roleGroup and nestedGroup.
	deltaFirst = deltaPath + "?$select=members&$filter=id%20eq%20%27" + roleGroup + "%27%20or%20id%20eq%20%27" + nestedGroup + "%27"
)

func TestDeltaStart(t *testing.T) {
	f := newFixture(t,
		step{method: http.MethodGet, path: deltaFirst, status: http.StatusOK, body: "@delta-first.json"},
		step{method: http.MethodGet, path: deltaPath + "?$skiptoken=TEST-SKIPTOKEN-1", status: http.StatusOK, body: "@delta-last.json"},
	)
	got, err := f.client.Delta(context.Background(), "", []string{roleGroup, nestedGroup})
	f.done()
	if err != nil {
		t.Fatalf("Delta: %s", f.show(err))
	}
	u, err := url.Parse(f.requests()[0].uri)
	if err != nil {
		t.Fatalf("parse the first request: %v", err)
	}
	q := u.Query()
	if q.Get("$select") != "members" || q.Get("$filter") != "id eq '"+roleGroup+"' or id eq '"+nestedGroup+"'" {
		t.Errorf("query = %v, want members of the two groups", q)
	}
	want := DeltaResult{
		Groups: []GroupChange{{
			ID: roleGroup,
			Added: []MemberRef{
				{ID: userPat, Kind: KindUser},
				{ID: nestedGroup, Kind: KindGroup},
				{ID: agentUser, Kind: KindUser},
				{ID: agentID, Kind: KindServicePrincipal},
				{ID: userKim, Kind: KindUser},
			},
			Gone: []string{"1c1c1c1c-7777-4777-8777-777777777777", "1d1d1d1d-8888-4888-8888-888888888888"},
		}},
		Link: f.graph.URL + deltaPath + "?$deltatoken=TEST-DELTATOKEN-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Delta =\n%+v\nwant\n%+v", got, want)
	}
}

func TestDeltaContinue(t *testing.T) {
	f := newFixture(t, step{method: http.MethodGet, path: deltaPath + "?$deltatoken=TEST-DELTATOKEN-1", status: http.StatusOK, body: "@delta-group-removed.json"})
	got, err := f.client.Delta(context.Background(), f.graph.URL+deltaPath+"?$deltatoken=TEST-DELTATOKEN-1", nil)
	f.done()
	if err != nil {
		t.Fatalf("Delta: %s", f.show(err))
	}
	want := DeltaResult{Groups: []GroupChange{{ID: roleGroup, Removed: true}}, Link: f.graph.URL + deltaPath + "?$deltatoken=TEST-DELTATOKEN-2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Delta = %+v, want %+v", got, want)
	}
}

func TestDeltaPaging(t *testing.T) {
	const start = deltaPath + "?$deltatoken=start"
	page := func(n string) string { return deltaPath + "?$skiptoken=" + n }
	get := func(at string, status int, body string) step {
		return step{method: http.MethodGet, path: at, status: status, body: body}
	}
	member := func(id string) string { return `{"@odata.type":"#microsoft.graph.user","id":"` + id + `"}` }
	gone := `{"@odata.type":"#microsoft.graph.user","id":"u9","@removed":{"reason":"deleted"}}`
	groupOf := func(id string, members ...string) string {
		out := `{"id":"` + id + `","members@delta":[`
		for i, m := range members {
			if i > 0 {
				out += ","
			}
			out += m
		}
		return out + "]}"
	}
	next := func(n string, groups ...string) string {
		return pageBody(`"@odata.nextLink":"`+graphHost+page(n)+`"`, groups...)
	}
	last := func(groups ...string) string {
		return pageBody(`"@odata.deltaLink":"`+graphHost+deltaPath+`?$deltatoken=end"`, groups...)
	}

	tests := []struct {
		name  string
		steps []step
		want  []GroupChange
		fail  string
	}{
		{name: "three pages, one group merged", steps: []step{
			get(start, 200, next("2", groupOf("g1", member("u1")))),
			get(page("2"), 200, next("3", groupOf("g2", member("u2")))),
			get(page("3"), 200, last(groupOf("g1", member("u3"), gone), `{"id":"g3","@removed":{"reason":"changed"}}`)),
		}, want: []GroupChange{
			{ID: "g1", Added: []MemberRef{{ID: "u1", Kind: KindUser}, {ID: "u3", Kind: KindUser}}, Gone: []string{"u9"}},
			{ID: "g2", Added: []MemberRef{{ID: "u2", Kind: KindUser}}},
			{ID: "g3", Removed: true},
		}},
		{name: "two pages", steps: []step{
			get(start, 200, next("2", groupOf("g1", member("u1")))),
			get(page("2"), 200, last()),
		}, want: []GroupChange{{ID: "g1", Added: []MemberRef{{ID: "u1", Kind: KindUser}}}}},
		{name: "unknown member type", steps: []step{
			get(start, 200, last(groupOf("g1", `{"@odata.type":"#microsoft.graph.orgContact","id":"c1"}`))),
		}, want: []GroupChange{{ID: "g1", Added: []MemberRef{{ID: "c1", Kind: KindOther}}}}},
		{name: "500 on page two", steps: []step{
			get(start, 200, next("2", groupOf("g1", member("u1")))),
			get(page("2"), 500, `{"error":{"code":"generalException","message":"General exception while processing."}}`),
		}, fail: "HTTP 500"},
		{name: "a page with neither link", steps: []step{
			get(start, 200, next("2", groupOf("g1", member("u1")))),
			get(page("2"), 200, `{"value":[]}`),
		}, fail: "neither a next link nor a delta link"},
		{name: "a page with no list", steps: []step{get(start, 200, `{"@odata.deltaLink":"`+graphHost+`/v1.0/groups/delta?$deltatoken=end"}`)}, fail: "holds no list"},
		{name: "a group without an id", steps: []step{get(start, 200, last(`{"members@delta":[]}`))}, fail: "a group that has no id"},
		{name: "a member without an id", steps: []step{get(start, 200, last(groupOf("g1", `{"@odata.type":"#microsoft.graph.user"}`)))}, fail: "a member of group g1 that has no id"},
		{name: "a delta link off Graph", steps: []step{
			get(start, 200, pageBody(`"@odata.deltaLink":"https://graph.example/v1.0/groups/delta?$deltatoken=end"`)),
		}, fail: "a link that is not on"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.steps...)
			got, err := f.client.Delta(context.Background(), f.graph.URL+start, nil)
			f.done()
			if tt.fail != "" {
				f.wantError(err, tt.fail, "the delta read")
				if !reflect.DeepEqual(got, DeltaResult{}) {
					t.Errorf("Delta = %+v, want no result on a failed read", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Delta: %s", f.show(err))
			}
			if !reflect.DeepEqual(got.Groups, tt.want) || got.Link != f.graph.URL+deltaPath+"?$deltatoken=end" {
				t.Errorf("Delta = %+v, want %+v and the end link", got, tt.want)
			}
		})
	}
}

// pageBody is a delta page with the link field link and the group objects.
func pageBody(link string, groups ...string) string {
	out := `{` + link + `,"value":[`
	for i, g := range groups {
		if i > 0 {
			out += ","
		}
		out += g
	}
	return out + "]}"
}
