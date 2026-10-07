package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"
)

// TestNHIKeyAcceptsTypologyAgent pins the read-side derivation end to end
// on the kind-gated lane that matters operationally: an identity classified
// non-human ONLY by typology (userType=agent, the shape a midPoint-born
// agent lands in, since the SCIM connector cannot send the agentic URN and
// attrs.kind is create-only) must be able to hold an NHI assertion key and
// must read kind=nhi on the admin wire, without anyone re-creating it.
func TestNHIKeyAcceptsTypologyAgent(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)

	var created struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/users", adminTok,
		map[string]any{"username": "mp-agent"}, &created); code != http.StatusCreated {
		t.Fatalf("create user = %d", code)
	}
	if created.Kind != "human" {
		t.Fatalf("pre-classification kind = %q, want human", created.Kind)
	}

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	// Unclassified: the key lane refuses, exactly as for any human.
	if code := adminReq(t, http.MethodPut, base+"/v1/admin/users/"+created.ID+"/nhi-key", adminTok,
		map[string]any{"public_key": pubB64}, nil); code != http.StatusBadRequest {
		t.Fatalf("unclassified user key registration = %d, want 400", code)
	}

	// Classify by typology alone (the IdM-mastered lane; admin PATCH is its
	// standalone equivalent). No attrs.kind is ever written.
	var patched struct {
		Kind     string `json:"kind"`
		UserType string `json:"user_type"`
	}
	if code := adminReq(t, http.MethodPatch, base+"/v1/admin/users/"+created.ID, adminTok,
		map[string]any{"user_type": "agent"}, &patched); code != http.StatusOK {
		t.Fatalf("patch user_type = %d", code)
	}
	if patched.UserType != "agent" || patched.Kind != "nhi" {
		t.Fatalf("patched row = kind %q user_type %q, want nhi/agent", patched.Kind, patched.UserType)
	}

	if code := adminReq(t, http.MethodPut, base+"/v1/admin/users/"+created.ID+"/nhi-key", adminTok,
		map[string]any{"public_key": pubB64}, nil); code != http.StatusOK {
		t.Fatalf("typology-agent key registration = %d, want 200", code)
	}
}
