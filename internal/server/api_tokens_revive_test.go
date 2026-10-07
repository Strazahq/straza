package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestAPITokenLateStampCannotReviveRevokedToken pins the race between a use
// of an admin API token and its revocation: the use read the row, the
// revoke deleted it, and the use's last-used stamp lands last. The stamp
// must leave the token revoked and the row gone.
func TestAPITokenLateStampCannotReviveRevokedToken(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	var minted struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "connector", "scope": "identity:read"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint = %d", code)
	}
	sum := sha256.Sum256([]byte(minted.Token))
	key := apiTokenKeyPrefix + hex.EncodeToString(sum[:])
	// The use in flight read the row before the revoke.
	raw, err := app.store.Settings().Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var meta apiTokenMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/api-tokens/"+minted.ID, adminTok, nil, nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}

	// The stamp of that use lands after the revoke.
	app.stampAPITokenUse(ctx, key, meta)
	if _, ok := app.apiTokenAuthenticate(ctx, minted.Token); ok {
		t.Fatal("the revoked token authenticates after a late stamp")
	}
	if _, err := app.store.Settings().Get(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the late stamp brought the token row back: %v", err)
	}
}
