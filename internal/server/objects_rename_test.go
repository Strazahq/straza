package server

import (
	"context"
	"maps"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestObjectRenameAnnouncesRoles pins what a listing that renames an object
// puts on the change feed: one role update for each role whose covers line
// names the object and for a business role composing one, and none for a
// role on every object, a business role composing only that, a role that
// names other objects or a role with no covers line.
func TestObjectRenameAnnouncesRoles(t *testing.T) {
	t.Parallel()
	r := newGHRig(t)
	ctx := context.Background()
	names := map[string]string{}
	compose := func(name string, implied string) {
		t.Helper()
		ro, err := r.app.store.Roles().Create(ctx, store.Role{Name: name, Kind: store.RoleKindBusiness})
		if err != nil {
			t.Fatal(err)
		}
		target, err := r.app.store.Roles().GetByName(ctx, implied)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.app.store.Roles().AddImplication(ctx, ro.ID, target.ID); err != nil {
			t.Fatal(err)
		}
	}
	compose("gh-team", "gh-a-pay")
	compose("gh-all", "gh-every")
	roles, err := r.app.store.Roles().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ro := range roles {
		names[ro.ID] = ro.Name
	}
	admin := seedIdentity(t, r.app)
	grantAdmin(t, r.app, admin.ID)
	bearer := loginDeviceFlow(t, r.base, "kim", "hunter2!")
	head, err := r.app.store.Outbox().Head(ctx)
	if err != nil {
		t.Fatal(err)
	}

	r.up.rename("101", "Strazahq/payments-api")
	if err := r.app.objects.MarkDue(ctx, r.appID); err != nil {
		t.Fatal(err)
	}
	r.waitList(t, func(l []string) bool { return len(l) == 3 && l[0] == "Strazahq/pay-web" })

	updates := func() map[string]int {
		t.Helper()
		var page changesPage
		if code := adminReq(t, http.MethodGet, r.base+"/v1/admin/changes?types=role&limit=1000&since="+head, bearer, nil, &page); code != http.StatusOK {
			t.Fatalf("changes = %d", code)
		}
		out := map[string]int{}
		for _, c := range page.Changes {
			if c.Type == "role" && c.Op == "update" {
				out[names[c.ID]]++
			}
		}
		return out
	}
	want := map[string]int{"gh-pay": 1, "gh-a-pay": 1, "gh-team": 1}
	// The hook runs after the replica loads the new list, so the feed is
	// read until every wanted update is there, then once more after a
	// pause, so an update that follows the last wanted one also shows.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if got := updates(); got["gh-pay"] > 0 && got["gh-a-pay"] > 0 && got["gh-team"] > 0 {
			break
		}
	}
	time.Sleep(200 * time.Millisecond)
	if got := updates(); !maps.Equal(got, want) {
		t.Errorf("role updates on the change feed after the rename = %v, want %v", got, want)
	}
}

// TestObjectRenameReadsAreBounded pins that the three store reads behind a
// rename's identity events run under a deadline at most auditInsertTimeout
// away, though the listing loop hands the hook a context that never
// cancels, so a stuck store cannot hold shutdown.
func TestObjectRenameReadsAreBounded(t *testing.T) {
	t.Parallel()
	seen := &renameReads{deadlines: map[string]time.Duration{}}
	app, _ := testAppPreRun(t, []func(*App){func(a *App) { seen.Store, a.store = a.store, seen }})
	ctx := t.Context()
	srv, err := app.store.Apps().Create(ctx, store.App{Name: "ghapp", RuntimeKind: "remote", Status: "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	ro, err := app.store.Roles().Create(ctx, store.Role{Name: "gh-pay", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ro.ID, AppID: srv.ID, ToolMatcher: `["*"]`,
		Scope: &store.ObjectScope{Objects: []store.CoveredObject{{ID: "101", Value: "org/api"}}}}); err != nil {
		t.Fatal(err)
	}
	app.renamedObjects(context.WithValue(context.WithoutCancel(ctx), renameReadKey{}, true), srv.ID, []string{"101"})
	seen.mu.Lock()
	defer seen.mu.Unlock()
	for _, read := range []string{"bindings", "roles", "implications"} {
		left, ok := seen.deadlines[read]
		switch {
		case !ok:
			t.Errorf("the %s read did not run", read)
		case left <= 0 || left > auditInsertTimeout:
			t.Errorf("the %s read ran with %v left before its deadline, want a deadline at most %v away", read, left, auditInsertTimeout)
		}
	}
}

// renameReadKey marks the context of the hook call under test, so reads by
// the app's own loops are not counted.
type renameReadKey struct{}

// renameReads wraps a store and keeps, for each read renamedObjects makes
// on a marked context, how long its context had before its deadline, or
// a negative duration when it had none.
type renameReads struct {
	store.Store
	mu        sync.Mutex
	deadlines map[string]time.Duration
}

func (s *renameReads) note(ctx context.Context, read string) {
	if ctx.Value(renameReadKey{}) == nil {
		return
	}
	left := time.Duration(-1)
	if d, ok := ctx.Deadline(); ok {
		left = time.Until(d)
	}
	s.mu.Lock()
	s.deadlines[read] = left
	s.mu.Unlock()
}

func (s *renameReads) ToolBindings() store.ToolBindingRepo {
	return renameReadBindings{s.Store.ToolBindings(), s}
}

func (s *renameReads) Roles() store.RoleRepo { return renameReadRoles{s.Store.Roles(), s} }

type renameReadBindings struct {
	store.ToolBindingRepo
	s *renameReads
}

func (r renameReadBindings) List(ctx context.Context) ([]store.ToolBinding, error) {
	r.s.note(ctx, "bindings")
	return r.ToolBindingRepo.List(ctx)
}

type renameReadRoles struct {
	store.RoleRepo
	s *renameReads
}

func (r renameReadRoles) List(ctx context.Context) ([]store.Role, error) {
	r.s.note(ctx, "roles")
	return r.RoleRepo.List(ctx)
}

func (r renameReadRoles) ListImplications(ctx context.Context) ([]store.RoleImplication, error) {
	r.s.note(ctx, "implications")
	return r.RoleRepo.ListImplications(ctx)
}
