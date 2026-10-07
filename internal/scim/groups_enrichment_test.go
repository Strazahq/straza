package scim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// enrichmentServer builds a handler-capable Server over a real sqlite
// store: auth always passes, identity bumps are no-ops, and RoleProjection
// returns a canned block (the REAL projection is server-side; this package
// owns only the embedding and the write refusals).
func enrichmentServer(t *testing.T, projection func(ctx context.Context, roleID string) map[string]any) (*Server, store.Store) {
	t.Helper()
	st := newPlanesTestStore(t)
	s := New(Deps{
		Store:           st,
		Authenticate:    func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
		Deactivate:      func(context.Context, string, string) {},
		Reactivate:      func(context.Context, string) {},
		IdentityChanged: func(context.Context, string, string) {},
		RoleProjection:  projection,
	})
	return s, st
}

// TestGroupResourceCarriesProjection: a role whose projection provider
// returns a block renders the §4.1 extension and declares its URN in
// schemas; a role the provider answers nil for stays a bare core Group
// (revision 12: the render is the role's own data).
func TestGroupResourceCarriesProjection(t *testing.T) {
	ctx := context.Background()
	var projectedRole string
	var devID string
	s, st := enrichmentServer(t, func(_ context.Context, roleID string) map[string]any {
		projectedRole = roleID
		if roleID != devID {
			return nil
		}
		return map[string]any{"role": "dev", "plane": store.RolePlaneAccess}
	})

	dev, err := st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness, Description: "Developer access"})
	if err != nil {
		t.Fatal(err)
	}
	devID = dev.ID
	plain, err := st.Roles().Create(ctx, store.Role{Name: "finance", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}

	res := s.groupResource(ctx, dev)
	block, ok := res[SchemaStrazaGroup].(map[string]any)
	if !ok {
		t.Fatalf("projected role carries no extension block: %+v", res)
	}
	if block["role"] != "dev" {
		t.Errorf("block role = %v, want dev", block["role"])
	}
	if projectedRole != dev.ID {
		t.Errorf("projection called with role id %q, want %q", projectedRole, dev.ID)
	}
	schemas, _ := res["schemas"].([]string)
	if len(schemas) != 2 || schemas[0] != SchemaGroup || schemas[1] != SchemaStrazaGroup {
		t.Errorf("projected role schemas = %v", schemas)
	}

	bare := s.groupResource(ctx, plain)
	if _, present := bare[SchemaStrazaGroup]; present {
		t.Errorf("nil-projection role must not carry the extension: %+v", bare)
	}
	if schemas, _ := bare["schemas"].([]string); len(schemas) != 1 || schemas[0] != SchemaGroup {
		t.Errorf("nil-projection role schemas = %v", schemas)
	}

	// A nil provider (projection unwired) must degrade to the bare shape.
	s2, _ := enrichmentServer(t, nil)
	if res := s2.groupResource(ctx, dev); res[SchemaStrazaGroup] != nil {
		t.Errorf("nil provider must render no block")
	}
}

// TestGroupProjectionWriteRefusals: the extension is read-only evidence.
// PATCH against the URN, its sub-attributes, or the bare projection names
// answers scimType mutability (mirroring the §3.1 lock block); PUT bodies
// carrying the URN are ignored per the mapped-subset rule.
func TestGroupProjectionWriteRefusals(t *testing.T) {
	ctx := context.Background()
	s, st := enrichmentServer(t, func(context.Context, string) map[string]any {
		return map[string]any{"role": "dev"}
	})
	dev, err := st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}

	var me mutabilityError
	plan, err := s.planFor(ctx, dev)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		strazaGroupURNLower,
		strazaGroupURNLower + ":role",
		"apps", "tools", "policies", "administers", "server", "role", "rolekind", "plane", "description",
	} {
		err := s.planGroupOp(ctx, plan, patchOp{op: "replace", path: path, value: "x"})
		if !errors.As(err, &me) {
			t.Errorf("PATCH path %q: err = %v, want mutabilityError", path, err)
		}
	}

	// Handler-level: the mutability class must reach the wire as its own
	// scimType, not the generic invalidValue.
	mux := http.NewServeMux()
	s.Routes(mux)
	body := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],` +
		`"Operations":[{"op":"replace","path":"` + SchemaStrazaGroup + `:role","value":"x"}]}`
	req := httptest.NewRequest("PATCH", "/scim/v2/Groups/"+dev.ID, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Content-Type", "application/scim+json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH status = %d, want 400", rec.Code)
	}
	var scimErr struct {
		ScimType string `json:"scimType"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&scimErr); err != nil || scimErr.ScimType != "mutability" {
		t.Errorf("PATCH scimType = %q (%v), want mutability", scimErr.ScimType, err)
	}

	// PUT with the URN present: ignored, membership applies normally, block
	// re-renders from server truth.
	putBody := `{"schemas":["` + SchemaGroup + `","` + SchemaStrazaGroup + `"],` +
		`"displayName":"dev",` +
		`"` + SchemaStrazaGroup + `":{"role":"straza-admin","apps":["evil"]}}`
	req = httptest.NewRequest("PUT", "/scim/v2/Groups/"+dev.ID, strings.NewReader(putBody))
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Content-Type", "application/scim+json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (URN ignored)", rec.Code)
	}
	var out map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	block, _ := out[SchemaStrazaGroup].(map[string]any)
	if block == nil || block["role"] != "dev" {
		t.Errorf("PUT response block = %v, want server-truth role dev", block)
	}
}
