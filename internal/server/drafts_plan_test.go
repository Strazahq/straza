package server

import (
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// TestObjectsOnly pins which server changes keep its instances: a change
// whose every path lies under straza.objects, and nothing else.
func TestObjectsOnly(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  bool
	}{
		{"objects paths", []string{"straza.objects.list.tool", "straza.objects.type"}, true},
		{"the whole block", []string{"straza.objects"}, true},
		{"objects and limits", []string{"straza.limits.rps", "straza.objects.type"}, false},
		{"a key that only starts like objects", []string{"straza.objectsx"}, false},
		{"no paths", nil, false},
	}
	for _, tc := range cases {
		if got := objectsOnly(tc.paths); got != tc.want {
			t.Errorf("%s: objectsOnly(%q) = %v, want %v", tc.name, tc.paths, got, tc.want)
		}
	}
}

// TestPlanForACoversOnlyChange pins that a draft that changes only a
// binding's covers line writes the role with the new line by id, moves its
// fingerprint, so an open draft based on the old one goes stale, and names
// the role among the rows the gateway narrows, while a draft that names a
// renamed repo by its new value changes nothing.
func TestPlanForACoversOnlyChange(t *testing.T) {
	t.Parallel()
	w := drafts.World{
		Apps: map[string]drafts.App{"ghapp": {ID: "app-ghapp", Name: "ghapp", Pinned: &drafts.PinnedObjects{Type: "repo", Plural: "repos", LiveType: "repo",
			Reach: map[string]string{"pr_create": "argument"}, IDs: map[string]string{"Strazahq/pay-api-v2": "101", "Strazahq/site": "103"}}}},
		Roles:  map[string]drafts.Role{"ghapp-pay": {ID: "r1", Name: "ghapp-pay", Kind: drafts.RoleKindApplication, Plane: drafts.PlaneAccess, Owner: "ghapp", Owned: true}},
		Access: map[string]drafts.Access{"ghapp-pay": {ID: "b1", Server: "ghapp", Tools: []string{"pr_create"}, Covers: &drafts.Scope{Objects: []drafts.ObjectRef{{Value: "Strazahq/pay-api", ID: "101"}}}}},
	}
	base := store.FingerprintRole(roleConfig(w, "ghapp-pay"))
	doc := func(covers string) string {
		return "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: ghapp-pay\nspec:\n    kind: application\n    server: ghapp\n" +
			"    bindings:\n        - app: ghapp\n          tools: [pr_create]\n          covers: " + covers + "\n"
	}
	cases := []struct {
		name, covers, key string
		changed           bool
	}{
		{"another repo", "{objects: [Strazahq/site]}", "objects:103", true},
		{"every repo", "every", "every", true},
		{"the renamed repo by its new value", "{objects: [Strazahq/pay-api-v2]}", "objects:101", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := drafts.Draft{Items: []drafts.Item{{Kind: drafts.KindRole, Name: "ghapp-pay", Op: drafts.OpPut, Doc: doc(tc.covers), Base: drafts.Fingerprint(base)}}}
			rows := []store.DraftItemRow{{Kind: "Role", Name: "ghapp-pay", Op: "put", Doc: doc(tc.covers), Base: base, BaseOp: "put"}}
			dp, err := planFor(w, drafts.CheckInput{}, d, rows)
			if err != nil {
				t.Fatal(err)
			}
			pi := dp.plan.Items[0]
			if got := pi.Role.Scope.Key(); got != tc.key {
				t.Errorf("the role writes covers %q, want %q", got, tc.key)
			}
			if (pi.After != pi.Base) != tc.changed || slices.Contains(dp.head.AccessRoles, "ghapp-pay") != tc.changed {
				t.Errorf("After moved %v, AccessRoles %q, want a change %v", pi.After != pi.Base, dp.head.AccessRoles, tc.changed)
			}
		})
	}
}

// TestPlanForAnUnpinClearsEveryRow pins the unpin's publish: the store
// clears every covers line on a server whose pinned type the draft
// removes, so a role the draft writes only as an implied put, a role that
// loses an edge to a removed role here, reads back with no covers line.
func TestPlanForAnUnpinClearsEveryRow(t *testing.T) {
	t.Parallel()
	pinned, err := manager.Parse([]byte(coversManifest))
	if err != nil {
		t.Fatal(err)
	}
	unpinned := strings.SplitN(coversManifest, "  objects:", 2)[0]
	mf, err := manager.Parse([]byte(unpinned))
	if err != nil {
		t.Fatal(err)
	}
	w := drafts.World{
		Apps: map[string]drafts.App{"ghapp": {ID: "app-ghapp", Name: "ghapp", Manifest: objectsTestJSON(t, pinned),
			Pinned: &drafts.PinnedObjects{Type: "repo", Plural: "repos", LiveType: "repo"}}},
		Roles: map[string]drafts.Role{
			"ghapp-read": {ID: "r1", Name: "ghapp-read", Kind: drafts.RoleKindApplication, Plane: drafts.PlaneAccess, Owner: "ghapp", Owned: true},
			"ops":        {ID: "r3", Name: "ops", Kind: drafts.RoleKindBusiness, Plane: drafts.PlaneAccess},
		},
		Implies: map[string][]string{"ghapp-read": {"ops"}},
		Access:  map[string]drafts.Access{"ghapp-read": {ID: "b1", Server: "ghapp", Tools: []string{"get_me"}, Covers: &drafts.Scope{Every: true}}},
	}
	in := drafts.CheckInput{Apps: map[string]drafts.App{"ghapp": manifestFacts(drafts.App{Name: "ghapp", Manifest: objectsTestJSON(t, mf)}, mf)}}
	d := drafts.Draft{Items: []drafts.Item{
		{Kind: drafts.KindApp, Name: "ghapp", Op: drafts.OpPut, Doc: unpinned},
		{Kind: drafts.KindRole, Name: "ops", Op: drafts.OpRemove},
	}}
	rows := []store.DraftItemRow{{Kind: "App", Name: "ghapp", Op: "put", Doc: unpinned}, {Kind: "Role", Name: "ops", Op: "remove"}}
	dp, err := planFor(w, in, d, rows)
	if err != nil {
		t.Fatal(err)
	}
	cfg := roleConfig(w, "ghapp-read")
	cfg.Scope, cfg.Implies = nil, nil
	for _, pi := range dp.plan.Items {
		if pi.Ref.Name == "ghapp-read" && pi.After != store.FingerprintRole(cfg) {
			t.Errorf("the implied put of ghapp-read reads back %s, want its row with no covers line", pi.After)
		}
	}
	if !slices.ContainsFunc(dp.plan.Items, func(pi store.PlanItem) bool { return pi.Ref.Name == "ghapp-read" && pi.Implied }) {
		t.Errorf("plan items %+v, want an implied put of ghapp-read", dp.plan.Items)
	}
}
