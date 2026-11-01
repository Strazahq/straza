package ctl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestAppObjectsWire pins the object list wrappers to the wire contract of
// the /v1/admin/apps/{id}/objects routes: verb, escaped path and query,
// request body, and that the canned answers decode, an attribute value as a
// string or as a list of strings alike.
func TestAppObjectsWire(t *testing.T) {
	rec := &wireRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handle))
	t.Cleanup(srv.Close)
	c := tokenClient(t, srv.URL)
	ctx := context.Background()

	listed := `{"app":"objects-demo","type":"repo","plural":"repos","attributes":[{"name":"area","setBy":"org"}],` +
		`"pinned":{"type":"repo","list":{"tool":"repo_list"}},"declared":null,"declaredFaults":[],"drift":false,` +
		`"listing":{"tool":"repo_list","credential":"set","outcome":"ok","version":3,"count":2,"joined":1,"left":0,` +
		`"listedAt":"2026-10-18T08:00:00Z"},` +
		`"objects":[{"value":"Strazahq/straza","id":"9012331","title":"straza","attributes":{"area":"product"},"new":false,"coveredBy":["objects-demo-tools"]},` +
		`{"value":"Strazahq/site","id":"9012332","title":"","attributes":{"area":["web","docs"]},"new":true,"coveredBy":[]}],` +
		`"total":2,"next":"Strazahq/site","later":"an additive field"}`

	tests := []struct {
		name   string
		after  string
		limit  int
		uri    string
		answer string
	}{
		{name: "first page with the server's default size", uri: "/v1/admin/apps/objects-demo/objects", answer: listed},
		{name: "a later page with a size", after: "Strazahq/a b", limit: 2,
			uri: "/v1/admin/apps/objects-demo/objects?after=Strazahq%2Fa+b&limit=2", answer: listed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec.script(tc.answer)
			got, err := c.AppObjects(ctx, "objects-demo", tc.after, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if method, uri, _, _ := rec.last(); method != http.MethodGet || uri != tc.uri {
				t.Errorf("wire = %s %s, want GET %s", method, uri, tc.uri)
			}
			if got.Plural != "repos" || got.Listing == nil || got.Listing.Version != 3 || got.Next != "Strazahq/site" || len(got.Objects) != 2 {
				t.Fatalf("decoded = %+v", got)
			}
			if a := got.Objects[0].Attributes["area"]; !reflect.DeepEqual(a, AttributeValue{"product"}) {
				t.Errorf("string attribute = %#v", a)
			}
			if a := got.Objects[1].Attributes["area"]; !reflect.DeepEqual(a, AttributeValue{"web", "docs"}) {
				t.Errorf("list attribute = %#v", a)
			}
		})
	}

	rec.script(`{"app":"objects-demo","dueAt":"2026-10-18T08:00:00Z"}`)
	queued, err := c.RefreshAppObjects(ctx, "objects-demo")
	if err != nil || queued.App != "objects-demo" || queued.DueAt == "" {
		t.Errorf("refresh = %+v, %v", queued, err)
	}
	if method, uri, _, _ := rec.last(); method != http.MethodPost || uri != "/v1/admin/apps/objects-demo/objects/refresh" {
		t.Errorf("refresh wire = %s %s", method, uri)
	}

	rec.script(`{"app":"objects-demo","fingerprint":"a1c4","setAt":"2026-10-18T08:00:00Z"}`)
	cred, err := c.SetListingCredential(ctx, "objects-demo", "s3cret")
	if err != nil || cred.Fingerprint != "a1c4" {
		t.Errorf("set listing credential = %+v, %v", cred, err)
	}
	if method, uri, _, body := rec.last(); method != http.MethodPost || uri != "/v1/admin/apps/objects-demo/objects/credential" ||
		strings.TrimSpace(string(body)) != `{"value":"s3cret"}` {
		t.Errorf("set wire = %s %s %s", method, uri, body)
	}

	rec.script("")
	if err := c.RemoveListingCredential(ctx, "objects-demo"); err != nil {
		t.Errorf("remove listing credential: %v", err)
	}
	if method, uri, _, _ := rec.last(); method != http.MethodDelete || uri != "/v1/admin/apps/objects-demo/objects/credential" {
		t.Errorf("remove wire = %s %s", method, uri)
	}

	rec.script(`{"unchanged":true,"sentence":"objects-demo's pinned repos already match its marks. Nothing to draft."}`)
	raw, code, err := c.DraftAppObjects(ctx, "objects-demo")
	if err != nil || code != http.StatusOK || !strings.Contains(string(raw), "Nothing to draft.") {
		t.Errorf("draft = %s %d %v", raw, code, err)
	}
	if method, uri, _, _ := rec.last(); method != http.MethodPost || uri != "/v1/admin/apps/objects-demo/objects/draft" {
		t.Errorf("draft wire = %s %s", method, uri)
	}
}
