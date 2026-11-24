package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
)

// TestPackRoutesAnswerGone pins the five retired knowledge pack routes: a
// call with no credential still answers 401, a signed-in user without the
// admin plane still answers 403, and an administrator reads 410 with the
// sentence that says to remove the call, whatever body the call sends.
func TestPackRoutesAnswerGone(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	routes := []struct {
		method, path string
		body         string
	}{
		{http.MethodGet, "/v1/admin/packs", ""},
		{http.MethodPost, "/v1/admin/packs", `{"name":"golang-style","version":"1","content":"use gofmt"}`},
		{http.MethodDelete, "/v1/admin/packs/p1", ""},
		{http.MethodPost, "/v1/admin/packs/p1/bindings", `{"role_id":"r1"}`},
		{http.MethodDelete, "/v1/admin/packs/p1/bindings/r1", ""},
	}
	for _, caller := range []struct {
		name   string
		bearer string
		admin  bool
		want   int
		says   string
	}{
		{"no credential", "", false, http.StatusUnauthorized, ""},
		{"a user without the admin plane", idToken, false, http.StatusForbidden, ""},
		{"an administrator", idToken, true, http.StatusGone, packsRetired},
	} {
		if caller.admin {
			grantAdmin(t, app, user.ID)
		}
		for _, rt := range routes {
			code, body, _ := adminBytes(t, rt.method, base+rt.path, caller.bearer, "application/json", []byte(rt.body))
			if code != caller.want {
				t.Errorf("%s: %s %s = %d %s, want %d", caller.name, rt.method, rt.path, code, body, caller.want)
				continue
			}
			if caller.says == "" {
				continue
			}
			var answer struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &answer); err != nil || answer.Error != caller.says {
				t.Errorf("%s: %s %s answered %s (%v), want the error %q", caller.name, rt.method, rt.path, body, err, caller.says)
			}
		}
	}
}

// TestRoleDocumentWithPacksPublishes pins that the revision 7 fixture
// role-dev.yaml, which names a pack, still makes a draft that publishes:
// the stored item keeps the pack, the check refuses nothing and notes that
// the packs field is ignored, the live document of dev names no pack
// afterwards, the same document checked again equals live and keeps the
// note, and the undo of that publish, whose base is the stored document
// with the pack, publishes too. The fixture's binding is cut, because the
// check refuses a binding on a role no server owns.
func TestRoleDocumentWithPacksPublishes(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	doc, err := os.ReadFile(filepath.Join("..", "..", "spec", "objects", "fixtures", "role-dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	reapplied := strings.Replace(string(doc), "    bindings:\n        - app: demo-tools\n          tools:\n            - add\n            - echo\n", "", 1)
	created := f.create(t, f.root, reapplied)
	if _, items := f.stored(t, created.Draft.ID); len(items) != 1 || !strings.Contains(items[0].Doc, "packs:\n        - golang-style\n") {
		t.Fatalf("stored items = %+v, want the role document with its pack", items)
	}
	noted := false
	for _, fnd := range created.Verdict.Info {
		noted = noted || fnd.Code == "info.packs" && fnd.Object == "Role/dev" &&
			fnd.Sentence == "This Role names knowledge packs, which were removed from Straza. The packs field is ignored."
	}
	if len(created.Verdict.Refused) > 0 || !noted {
		t.Fatalf("verdict refused %+v and info %+v, want no refusal and the info.packs note on Role/dev", created.Verdict.Refused, created.Verdict.Info)
	}
	if code, a := f.publishAll(t, f.root, created.Draft.ID); code != http.StatusOK {
		t.Fatalf("publish = %d %q %+v", code, a.Error, a.Verdict.Refused)
	}
	w, err := f.app.readWorld(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if live, ok := drafts.RoleDocOf(w, "dev"); !ok || len(live.Spec.Packs) != 0 || live.Spec.Description != "Developer seat: governed demo-tools access" {
		t.Errorf("live dev = %+v (%v), want the published description and no pack", live.Spec, ok)
	}
	code, again := f.call(t, http.MethodPost, "/v1/admin/drafts/check", f.root, map[string]any{"documents": []string{reapplied}})
	var lines []string
	for _, fnd := range again.Verdict.Info {
		if fnd.Object == "Role/dev" {
			lines = append(lines, fnd.Code)
		}
	}
	if code != http.StatusOK || !slices.Equal(lines, []string{"info.no-change", "info.packs"}) {
		t.Errorf("checking the same document again = %d with Role/dev info %q, want info.no-change and info.packs", code, lines)
	}
	code, undo := f.call(t, http.MethodPost, "/v1/admin/drafts/"+created.Draft.ID+"/revert", f.root, map[string]any{"note": "Undo the role."})
	if code != http.StatusCreated {
		t.Fatalf("revert = %d %q %v", code, undo.Error, undo.Findings)
	}
	if _, items := f.stored(t, undo.Draft.ID); len(items) != 1 || !strings.Contains(items[0].BaseDoc, "packs:\n        - golang-style\n") {
		t.Fatalf("stored undo items = %+v, want the published document with its pack as the base", items)
	}
	if code, a := f.publishAll(t, f.root, undo.Draft.ID); code != http.StatusOK {
		t.Fatalf("publishing the undo = %d %q %+v", code, a.Error, a.Verdict.Refused)
	}
}
