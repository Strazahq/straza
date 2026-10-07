package agentguard_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/config"
)

// TestM5Demo runs the managed-attestation demo as a test. With the
// enterprise require-managed gate on:
//  1. a user-mode install gets NO session token (advisory < managed);
//  2. `install --managed` + hash registration makes check-in verify → managed;
//  3. tampering the managed hook wiring → attestation mismatch → att=none →
//     no token → the session is fully blocked, because the client fails closed.
func TestM5Demo(t *testing.T) {
	base, _ := bootStrazad(t, func(c *config.Config) {
		c.Governance.MinAttestation = config.AttestationManaged
	})

	// Relocated managed layout + isolated user home (env seams).
	sysDir := t.TempDir()
	t.Setenv("STRAZA_SYSTEM", filepath.Join(sysDir, "etc"))
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sysDir, "harness"))
	t.Setenv("STRAZA_MANAGED_BIN_DIR", filepath.Join(sysDir, "bin"))
	t.Setenv("STRAZA_HOME", t.TempDir())

	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	driveEnroll(t, store, base)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. User-mode install: advisory attestation → the gate refuses a token
	// with an actionable reason.
	_, _, err = agentguard.SessionStart(ctx, store, "claude-code", "2.1.0")
	if err == nil {
		t.Fatal("advisory checkin must be refused under require-managed")
	}
	if !strings.Contains(err.Error(), `"advisory"`) || !strings.Contains(err.Error(), "install --managed") {
		t.Errorf("refusal must name the level and the fix: %v", err)
	}

	// 2. Managed install. This layout relocates the binary, so the wiring
	// is a local render the server never published; kim, straza-admin,
	// registers its hash the way the install's closing line tells her to.
	if err := agentguard.InstallManaged(ctx, agentguard.ManagedInstallOptions{
		Harnesses: []string{"claude-code"}, ServerURL: base,
	}, io.Discard); err != nil {
		t.Fatalf("InstallManaged: %v", err)
	}
	registerWiringHash(t, base, deviceLogin(t, base, "strazactl", "kim", "hunter2!"), "claude-code")

	// Verified managed check-in → session token, att=managed.
	if _, _, err := agentguard.SessionStart(ctx, store, "claude-code", "2.1.0"); err != nil {
		t.Fatalf("managed checkin failed: %v", err)
	}
	ses, err := store.LoadSession()
	if err != nil || ses.Attestation != "managed" || ses.SessionToken == "" {
		t.Fatalf("session after managed install = %+v, %v", ses, err)
	}

	// 3. Tamper the managed hook wiring, as an attacker with sudo would.
	// Without sudo the file is unwritable, which is the point of a managed install.
	wiring, err := agentguard.ManagedSettingsPath("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(wiring)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wiring, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.DropSession(); err != nil {
		t.Fatal(err)
	}
	_, _, err = agentguard.SessionStart(ctx, store, "claude-code", "2.1.0")
	if err == nil {
		t.Fatal("tampered wiring must yield att=none → no token (M5 demo AC)")
	}
	if !strings.Contains(err.Error(), `"none"`) {
		t.Errorf("tamper refusal must state the computed level none: %v", err)
	}
	// Hooks fail closed too: without session state, tool.pre denies.
	if _, err := store.LoadSession(); err == nil {
		t.Error("no session state may survive a refused checkin")
	}
}

// registerWiringHash does the administrator's step for a local render:
// one hooks row for this harness and platform in the expected-hash registry.
func registerWiringHash(t *testing.T, base, adminToken, harness string) {
	t.Helper()
	wiring, err := agentguard.ManagedSettingsPath(harness)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(wiring)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	body, _ := json.Marshal(map[string]string{
		"artifact": "hooks." + harness, "harness": harness,
		"platform": runtime.GOOS + "/" + runtime.GOARCH, "hash": "sha256:" + hex.EncodeToString(sum[:]),
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/admin/attestation-hashes", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register wiring hash: status %d", resp.StatusCode)
	}
}
