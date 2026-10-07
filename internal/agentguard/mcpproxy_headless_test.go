package agentguard_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

// TestExecMintsSessionForHeadlessNHI pins the lazy-checkin lane the MCP proxy
// and `straza exec` share (ensureSession): a headless NHI enrollment has no
// device token, so the session must be minted through the identity lane the
// hook path already uses. Without it ensureSession answers "no device token"
// and the Tier-3 and proxy surfaces are unusable for headless NHIs.
func TestExecMintsSessionForHeadlessNHI(t *testing.T) {
	base, adminPW := bootStrazad(t)

	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)

	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}

	// Standalone headless lane, same ritual as TestHeadlessNHIDemo: keygen,
	// admin registers the public key, non-interactive enroll.
	var keygenOut bytes.Buffer
	if err := agentguard.Keygen(store, "exec-bot", false, &keygenOut); err != nil {
		t.Fatal(err)
	}
	key, err := store.LoadNHIKey()
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(key.Seed)
	if err != nil {
		t.Fatal(err)
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	adminTok := deviceLogin(t, base, "strazactl", "kim", adminPW)
	code, created := adminJSON(t, http.MethodPost, base+"/v1/admin/users", adminTok,
		map[string]any{"username": "exec-bot", "kind": "nhi"})
	if code != http.StatusCreated {
		t.Fatalf("create nhi = %d %v", code, created)
	}
	nhiID, _ := created["id"].(string)
	if code, body := adminJSON(t, http.MethodPut, base+"/v1/admin/users/"+nhiID+"/nhi-key", adminTok,
		map[string]any{"public_key": pubB64}); code != http.StatusOK {
		t.Fatalf("register key = %d %v", code, body)
	}
	var enrollOut bytes.Buffer
	if err := agentguard.EnrollHeadless(context.Background(), store, base, "exec-bot", &enrollOut); err != nil {
		t.Fatalf("EnrollHeadless: %v\n%s", err, enrollOut.String())
	}

	// No hook session.start has run: exec must mint the session itself
	// through ensureSession's identity lane.
	var execErr bytes.Buffer
	if code := agentguard.Exec(context.Background(), []string{"ls", "-la"}, &execErr); code != 0 {
		t.Fatalf("exec for headless NHI = exit %d, want 0. stderr:\n%s", code, execErr.String())
	}
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatalf("session.json missing after exec checkin: %v", err)
	}
	if ses.User != "exec-bot" {
		t.Errorf("session user = %q, want exec-bot", ses.User)
	}

	// Same session now decides a deny: the starter policy blocks rm -rf.
	var denyErr bytes.Buffer
	if code := agentguard.Exec(context.Background(), []string{"rm", "-rf", "/tmp/probe"}, &denyErr); code != 2 {
		t.Fatalf("destructive exec = exit %d, want 2. stderr:\n%s", code, denyErr.String())
	}
	if !strings.Contains(denyErr.String(), "Straza") {
		t.Errorf("deny must carry a reason, got:\n%s", denyErr.String())
	}
}

// TestExecNoCredentialFailsClosed pins the error text for an identity that
// carries no credential at all: not headless, no device token, no ID token.
func TestExecNoCredentialFailsClosed(t *testing.T) {
	base, _ := bootStrazad(t)

	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)

	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(agentguard.Config{ServerURL: base}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIdentity(agentguard.Identity{Username: "ghost"}); err != nil {
		t.Fatal(err)
	}

	var execErr bytes.Buffer
	if code := agentguard.Exec(context.Background(), []string{"ls"}, &execErr); code != 2 {
		t.Fatalf("credential-less exec = exit %d, want 2 (fail closed)", code)
	}
	if !strings.Contains(execErr.String(), "no device token") {
		t.Errorf("error should keep the enroll remedy text, got:\n%s", execErr.String())
	}
}
