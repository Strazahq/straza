package server

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestOddLiveNameStillMeetsTheItemRules pins that an item named like a live
// role whose name holds an invisible character, whose name refusal the
// check may waive, still answers 422 for a document naming another object,
// an op that is not put, remove or off, a role turned off, a removal with a
// document and two items for one object, each with that refusal listed and
// nothing stored, never 500.
func TestOddLiveNameStillMeetsTheItemRules(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	odd := "dev\u200bops"
	for _, name := range []string{odd, "target"} {
		if _, err := f.app.store.Roles().Create(context.Background(), store.Role{Name: name, Kind: store.RoleKindBusiness}); err != nil {
			t.Fatal(err)
		}
	}
	item := func(op, doc string) map[string]string {
		return map[string]string{"kind": "Role", "name": odd, "op": op, "doc": doc}
	}
	for _, tc := range []struct {
		name  string
		items []map[string]string
		code  string
	}{
		{"a document naming another role", []map[string]string{item("put", draftRole("target", "Other words."))}, "bundle.name"},
		{"an op that is not put, remove or off", []map[string]string{item("bogus", draftRole(odd, "Words."))}, "bundle.op"},
		{"a role turned off", []map[string]string{item("off", "")}, "bundle.off"},
		{"a removal with a document", []map[string]string{item("remove", draftRole(odd, "Words."))}, "bundle.removal-doc"},
		{"two items for one object", []map[string]string{item("put", draftRole(odd, "Words.")), item("remove", "")}, "bundle.duplicate"},
	} {
		before := f.drafts(t)
		code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"items": tc.items})
		if code != http.StatusUnprocessableEntity || !slices.ContainsFunc(a.Findings, func(w wireFinding) bool { return w.Code == tc.code }) {
			t.Errorf("%s = %d %q with %v, want 422 listing %s", tc.name, code, a.Error, a.Findings, tc.code)
		}
		if f.drafts(t) != before {
			t.Errorf("%s stored a draft", tc.name)
		}
	}
}
