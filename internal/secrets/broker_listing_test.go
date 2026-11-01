package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// TestBrokerListingSecret pins the listing credential (object scope threat
// T16): ListingSecret answers exactly the listing row and never the
// server's own secret or a role's, and AppSecret and ForRoles never answer
// the listing row, even when it is the only row of the server.
func TestBrokerListingSecret(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	broker := NewBroker(st, testKEK(t))
	dev, err := st.Roles().Create(ctx, store.Role{Name: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	newApp := func(name string) string {
		app, err := st.Apps().Create(ctx, store.App{Name: name, RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		return app.ID
	}
	onlyListing, withApp, withRole, none := newApp("only-listing"), newApp("with-app"), newApp("with-role"), newApp("none")

	row, err := broker.SetListing(ctx, onlyListing, "listing-1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Scope != store.CredScopeListing || row.OwnerID != onlyListing || row.Kind != store.CredStatic {
		t.Fatalf("listing row = %+v, want scope listing owned by the app", row)
	}
	if _, err := broker.SetListing(ctx, withApp, "listing-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Set(ctx, withApp, "", "server-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Set(ctx, withApp, dev.ID, "dev-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Set(ctx, withRole, dev.ID, "dev-token-2"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		app     string
		listing string // "" = nil
		appSec  string
		forDev  string
		forNone string
	}{
		{name: "the listing row is the only row", app: onlyListing, listing: "listing-1"},
		{name: "beside the server's own and a role's row", app: withApp, listing: "listing-2", appSec: "server-token", forDev: "dev-token", forNone: "server-token"},
		{name: "a role row and no listing row", app: withRole, appSec: "dev-token-2", forDev: "dev-token-2"},
		{name: "no row at all", app: none},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := value(broker.ListingSecret(tc.app)); got != tc.listing {
				t.Errorf("ListingSecret = %q, want %q", got, tc.listing)
			}
			if got := value(broker.AppSecret(tc.app)); got != tc.appSec {
				t.Errorf("AppSecret = %q, want %q", got, tc.appSec)
			}
			if got := value(broker.ForRoles(tc.app, []string{"dev"}, "dev")); got != tc.forDev {
				t.Errorf("ForRoles(dev) = %q, want %q", got, tc.forDev)
			}
			if got := value(broker.ForRoles(tc.app, nil, "")); got != tc.forNone {
				t.Errorf("ForRoles(no roles) = %q, want %q", got, tc.forNone)
			}
		})
	}

	// The listing row rotates in place, stays out of the static secrets
	// list, and goes on delete without touching the other rows.
	rotated, err := broker.SetListing(ctx, withApp, "listing-3")
	if err != nil {
		t.Fatal(err)
	}
	if got := value(broker.ListingSecret(withApp)); got != "listing-3" {
		t.Errorf("rotated listing secret = %q", got)
	}
	creds, err := st.Credentials().ListByApp(ctx, withApp)
	if err != nil || len(creds) != 3 {
		t.Fatalf("rows = %d, %v; want 3 (app, role, one listing row rotated in place)", len(creds), err)
	}
	statics, err := broker.Statics(ctx, withApp)
	if err != nil || len(statics) != 2 {
		t.Fatalf("statics = %+v, %v; want the app and role rows only", statics, err)
	}
	if err := broker.DeleteListing(ctx, withApp); err != nil {
		t.Fatalf("DeleteListing = %v", err)
	}
	if _, err := st.Credentials().GetByID(ctx, rotated.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the listing row survived its delete: %v", err)
	}
	if s := broker.ListingSecret(withApp); s != nil {
		t.Errorf("listing secret after delete = %+v, want nil and no fallback", s)
	}
	if got := value(broker.AppSecret(withApp)); got != "server-token" {
		t.Errorf("app secret after the listing delete = %q", got)
	}
	if err := broker.DeleteListing(ctx, withApp); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second DeleteListing = %v, want ErrNotFound", err)
	}

	// An undecryptable listing row answers nil (fail closed).
	other := NewBroker(st, testKEK(t))
	if err := other.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if s := other.ListingSecret(onlyListing); s != nil {
		t.Errorf("a listing row sealed under another key opened as %+v", s)
	}
}

// value is a resolved secret's plaintext, empty for nil.
func value(s *manager.Secret) string {
	if s == nil {
		return ""
	}
	return s.Value
}
