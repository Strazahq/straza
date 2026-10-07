package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// TestSigningKeyRotate pins POST /v1/admin/signing-keys/rotate: a 200 with
// the staged kid, status staged and the two timings derived from the reload
// interval and the retirement horizon, exactly one straza.audit.admin record
// naming the actor and the kid, the staged public key in the JWKS at once,
// and a second call answering the same kid because a staged key exists.
func TestSigningKeyRotate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	grantAdmin(t, app, user.ID)

	type rotation struct {
		KID         string `json:"kid"`
		Status      string `json:"status"`
		ActiveIn    int    `json:"active_in_seconds"`
		PreviousFor int    `json:"previous_key_verifies_for_seconds"`
	}
	var first rotation
	if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/rotate", idToken, nil, &first); code != http.StatusOK {
		t.Fatalf("rotate = %d", code)
	}
	if first.KID == "" || first.Status != store.KeyStaged {
		t.Errorf("body = %+v, want a kid with status staged", first)
	}
	if want := int((2 * authn.KeyReloadInterval).Seconds()); first.ActiveIn != want {
		t.Errorf("active_in_seconds = %d, want %d", first.ActiveIn, want)
	}
	if want := int((2*authn.KeyReloadInterval + app.keyRetireAfter()).Seconds()); first.PreviousFor != want {
		t.Errorf("previous_key_verifies_for_seconds = %d, want %d", first.PreviousFor, want)
	}

	var rotates []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "signing-keys.rotate" {
			rotates = append(rotates, ev)
		}
	}
	if len(rotates) != 1 {
		t.Fatalf("straza.audit.admin signing-keys.rotate records = %d, want exactly 1", len(rotates))
	}
	if ev := rotates[0]; ev["kid"] != first.KID || ev["actor"] != "kim" || ev["actorId"] != user.ID || ev["actorVia"] != "login" {
		t.Errorf("audit record = %v, want kid %s by kim via login", ev, first.KID)
	}
	for k := range rotates[0] {
		if k == "private_key" || k == "privateKey" {
			t.Errorf("audit record carries %s", k)
		}
	}

	keys, err := app.store.SigningKeys().ListByPurpose(context.Background(), store.KeyPurposeSession)
	if err != nil {
		t.Fatal(err)
	}
	var staged int
	for _, k := range keys {
		if k.Status == store.KeyStaged {
			staged++
			if k.KID != first.KID {
				t.Errorf("staged key in store = %s, want %s", k.KID, first.KID)
			}
		}
	}
	if staged != 1 {
		t.Errorf("staged keys in store = %d, want 1", staged)
	}

	resp, err := http.Get(base + "/.well-known/straza/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var jwks struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(raw, &jwks); err != nil {
		t.Fatalf("jwks: %v", err)
	}
	var published bool
	for _, k := range jwks.Keys {
		if k["kid"] == first.KID {
			published = true
		}
	}
	if !published || len(jwks.Keys) != 2 {
		t.Errorf("JWKS = %d keys, staged key published %v; want 2 keys with the staged one", len(jwks.Keys), published)
	}

	var second rotation
	if code := adminReq(t, "POST", base+"/v1/admin/signing-keys/rotate", idToken, nil, &second); code != http.StatusOK {
		t.Fatalf("second rotate = %d", code)
	}
	if second.KID != first.KID {
		t.Errorf("second call staged %s, want the existing staged key %s", second.KID, first.KID)
	}
}
