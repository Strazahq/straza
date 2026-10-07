package server

import (
	"context"
	"errors"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

func TestAppFixturesAreIsolated(t *testing.T) {
	first, _ := testApp(t)
	seedIdentity(t, first)
	second, _ := testApp(t)
	ctx := context.Background()
	if _, err := second.store.Users().GetByUsername(ctx, "kim"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second app inherited the first app's user: %v", err)
	}
	if first.projectID == "" || first.projectID == second.projectID {
		t.Fatal("apps did not create independent project identities")
	}
	for _, purpose := range []string{store.KeyPurposeSession, store.KeyPurposeSnapshot} {
		firstKeys, err := first.store.SigningKeys().ListByPurpose(ctx, purpose)
		if err != nil {
			t.Fatal(err)
		}
		secondKeys, err := second.store.SigningKeys().ListByPurpose(ctx, purpose)
		if err != nil {
			t.Fatal(err)
		}
		if len(firstKeys) == 0 || len(secondKeys) == 0 || firstKeys[0].KID == secondKeys[0].KID {
			t.Fatalf("apps did not create independent %s keys", purpose)
		}
	}
}
