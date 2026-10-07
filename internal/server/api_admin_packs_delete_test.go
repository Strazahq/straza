package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// bindingPacks wraps a PackRepo so a test can land a bind right after the
// handler's first bindings read, inside the window before the delete.
type bindingPacks struct {
	store.PackRepo
	mu         sync.Mutex
	afterFirst func()
	listCalls  int
}

func (p *bindingPacks) ListBindings(ctx context.Context) ([]store.PackBinding, error) {
	out, err := p.PackRepo.ListBindings(ctx)
	p.mu.Lock()
	p.listCalls++
	hook := p.afterFirst
	p.afterFirst = nil
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	return out, err
}

// bindingStore serves bindingPacks in place of the store's own pack repo.
type bindingStore struct {
	store.Store
	packs *bindingPacks
}

func (s bindingStore) Packs() store.PackRepo { return s.packs }

// TestPackDeleteRereadsBindingsAfterAZeroRowDelete pins the handler's second
// bindings read: a bind that lands after the first read and before the
// statement leaves the delete with zero rows, and the answer is 409 naming
// that role, never 404 for a pack that exists and is bound.
func TestPackDeleteRereadsBindingsAfterAZeroRowDelete(t *testing.T) {
	t.Parallel()
	var packs *bindingPacks
	app, base := testAppPreRun(t, []func(*App){func(a *App) {
		packs = &bindingPacks{PackRepo: a.store.Packs()}
		a.store = bindingStore{Store: a.store, packs: packs}
	}})
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	reader, _ := app.store.Roles().GetByName(ctx, "reader")
	dev, _ := app.store.Roles().GetByName(ctx, "dev")
	pack, err := app.store.Packs().GetByName(ctx, "golang-style")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Packs().Unbind(ctx, reader.ID, pack.ID); err != nil {
		t.Fatal(err)
	}
	var bindErr error
	packs.mu.Lock()
	packs.afterFirst = func() { bindErr = packs.Bind(ctx, dev.ID, pack.ID) }
	packs.mu.Unlock()

	var out struct {
		Error string `json:"error"`
	}
	code := adminReq(t, "DELETE", base+"/v1/admin/packs/"+pack.ID, tok, nil, &out)
	if bindErr != nil {
		t.Fatalf("the bind in the window failed: %v", bindErr)
	}
	if code != http.StatusConflict || !strings.Contains(out.Error, "the role dev") {
		t.Errorf("delete with a bind in the window = %d %q, want 409 naming the role dev", code, out.Error)
	}
	if packs.listCalls != 2 {
		t.Errorf("bindings reads = %d, want 2, one before the statement and one after its zero rows", packs.listCalls)
	}
	if _, err := app.store.Packs().GetByID(ctx, pack.ID); err != nil {
		t.Errorf("the pack is gone: %v", err)
	}
	if bindings, err := app.store.Packs().ListBindings(ctx); err != nil || len(bindings) != 1 {
		t.Errorf("bindings = %d (%v), want the one that landed in the window", len(bindings), err)
	}
}

// TestPackDeleteNamesRolesToReadersOnly pins who reads the role names in the
// 409: a token holding identity:read beside its write grant reads them, and
// a token holding identity:write alone reads how many roles are bound and
// the grant that would list them. Root reads the names in
// TestPacksBindingsAndUnbind.
func TestPackDeleteNamesRolesToReadersOnly(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	dev, _ := app.store.Roles().GetByName(ctx, "dev")
	pack, err := app.store.Packs().GetByName(ctx, "golang-style")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Packs().Bind(ctx, dev.ID, pack.ID); err != nil {
		t.Fatal(err)
	}
	mint := func(t *testing.T, name, scope string) string {
		t.Helper()
		var minted struct {
			Token string `json:"token"`
		}
		if code := adminReq(t, "POST", base+"/v1/admin/api-tokens", tok, map[string]any{"name": name, "scope": scope}, &minted); code != http.StatusCreated {
			t.Fatalf("mint %s = %d", name, code)
		}
		return minted.Token
	}
	for i, tc := range []struct {
		name   string
		scope  string
		unbind string
		says   string
		not    string
	}{
		{"a reader of the identity area reads the names", "identity:read,identity:write", "", "the roles dev and reader, and deleting it would change what their sessions receive. Unbind it from each role first, then delete it", ""},
		{"a writer without the read grant reads the count", "identity:write", "", "bound to 2 roles, and deleting it would change what their sessions receive. Unbind it from each role first, then delete it. Listing the roles needs the identity:read grant", "reader"},
		{"a writer reads that one role holds it", "identity:write", dev.ID, "bound to a role, and deleting it would change what that role's sessions receive. Unbind it from the role first, then delete it. Listing the roles needs the identity:read grant", "reader"},
	} {
		if tc.unbind != "" {
			if err := app.store.Packs().Unbind(ctx, tc.unbind, pack.ID); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		}
		var out struct {
			Error string `json:"error"`
		}
		code := adminReq(t, "DELETE", base+"/v1/admin/packs/"+pack.ID, mint(t, fmt.Sprintf("t-%d", i), tc.scope), nil, &out)
		if code != http.StatusConflict || !strings.Contains(out.Error, tc.says) {
			t.Errorf("%s: delete = %d %q, want 409 saying %q", tc.name, code, out.Error, tc.says)
		}
		if tc.not != "" && strings.Contains(out.Error, tc.not) {
			t.Errorf("%s: refusal %q names the role %q", tc.name, out.Error, tc.not)
		}
	}
	if _, err := app.store.Packs().GetByID(ctx, pack.ID); err != nil {
		t.Errorf("the pack is gone: %v", err)
	}
}
