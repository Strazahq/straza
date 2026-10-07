package server

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/harnesscfg"
	"github.com/strazahq/straza/internal/store"
)

// snapshotKeyLookup fetches the published verification keys the way an
// enrolling client pins them, so verification in these tests goes through
// the same material a real `straza install --managed` would hold.
func snapshotKeyLookup(t *testing.T, base string) harnesscfg.KeyLookup {
	t.Helper()
	resp, err := http.Get(base + "/.well-known/straza/snapshot-keys.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var doc struct {
		Keys []struct{ Kid, Key string } `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{}
	for _, k := range doc.Keys {
		raw, err := base64.StdEncoding.DecodeString(k.Key)
		if err != nil {
			t.Fatal(err)
		}
		keys[k.Kid] = ed25519.PublicKey(raw)
	}
	return func(kid string) (ed25519.PublicKey, bool) {
		pub, ok := keys[kid]
		return pub, ok
	}
}

func fetchHarnessConfig(t *testing.T, base, harness, goos string) (harnesscfg.Document, *http.Response) {
	t.Helper()
	resp, err := http.Get(base + "/v1/harness-config?harness=" + harness + "&platform=" + goos)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var doc harnesscfg.Document
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
			t.Fatal(err)
		}
	}
	return doc, resp
}

// TestHarnessConfigServedAndVerifiable: every Tier-1 harness x GOOS document
// is served, verifies against the published snapshot keys, and matches the
// renderer byte for byte.
func TestHarnessConfigServedAndVerifiable(t *testing.T) {
	t.Parallel()
	_, base := testApp(t)
	lookup := snapshotKeyLookup(t, base)

	for _, harness := range harnessCfgHarnesses {
		for _, goos := range agentguard.SupportedGOOS {
			doc, resp := fetchHarnessConfig(t, base, harness, goos)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s/%s: status %d", harness, goos, resp.StatusCode)
			}
			if err := harnesscfg.Verify(doc, lookup); err != nil {
				t.Fatalf("%s/%s: %v", harness, goos, err)
			}
			want, err := agentguard.RenderManagedArtifacts(harness, goos, "")
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, art := range doc.Artifacts {
				got[art.Name] = true
				if string(art.Content) != string(want[art.Name]) {
					t.Fatalf("%s/%s %s: served content diverges from renderer", harness, goos, art.Name)
				}
			}
			if !got["hooks."+harness] {
				t.Fatalf("%s/%s: no hooks artifact served", harness, goos)
			}
			if harness == "claude-code" != got["mcp.claude-code"] {
				t.Fatalf("%s/%s: mcp artifact presence wrong (got %v)", harness, goos, got)
			}
		}
	}
}

// TestHarnessConfigETag pins the cheap-poll contract and the unknown-target
// answer.
func TestHarnessConfigETag(t *testing.T) {
	t.Parallel()
	_, base := testApp(t)
	_, resp := fetchHarnessConfig(t, base, "claude-code", "linux")
	etag := resp.Header.Get("ETag")
	if etag == "" || resp.Header.Get("X-Straza-Harness-Config-Id") == "" {
		t.Fatal("missing ETag or id header")
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/harness-config?harness=claude-code&platform=linux", nil)
	req.Header.Set("If-None-Match", etag)
	again, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Body.Close()
	if again.StatusCode != http.StatusNotModified {
		t.Fatalf("matching If-None-Match got %d, want 304", again.StatusCode)
	}

	_, missing := fetchHarnessConfig(t, base, "cursor", "linux")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown harness got %d, want 404", missing.StatusCode)
	}
}

// TestHarnessConfigRegistersHashes: boot auto-registration writes exact
// GOOS/GOARCH rows for the hooks artifacts only, is idempotent across
// re-boots over the same store, and the rows align end to end with
// verifyAttestation: a managed check-in reporting the SERVED content hash
// attests managed, and one reporting another platform's served hash (a file
// whose hook paths cannot exist on this OS: governance off while wired-
// looking) drops to none. This is the drift-detection contract.
func TestHarnessConfigRegistersHashes(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()

	rows, err := app.store.AttestationHashes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantRows := len(harnessCfgHarnesses) * len(agentguard.SupportedGOOS) * len(harnessCfgArches)
	if len(rows) != wantRows {
		t.Fatalf("registry holds %d rows, want %d", len(rows), wantRows)
	}
	for _, row := range rows {
		if !strings.HasPrefix(row.Artifact, "hooks.") {
			t.Fatalf("registered non-hooks artifact %q (mcp is deliberately unmeasured)", row.Artifact)
		}
		if !strings.Contains(row.Platform, "/") {
			t.Fatalf("row platform %q is not exact GOOS/GOARCH", row.Platform)
		}
	}

	// Idempotent: a second build over the same store adds nothing and errors
	// nothing (every boot and every pod re-registers).
	if err := app.buildHarnessConfigs(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := app.store.AttestationHashes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != wantRows {
		t.Fatalf("re-registration changed row count %d -> %d", wantRows, len(again))
	}

	// End-to-end alignment with the attestation verifier.
	linuxDoc, _ := fetchHarnessConfig(t, base, "claude-code", "linux")
	windowsDoc, _ := fetchHarnessConfig(t, base, "claude-code", "windows")
	hooksHash := func(doc harnesscfg.Document) string {
		for _, art := range doc.Artifacts {
			if art.Name == "hooks.claude-code" {
				return art.ContentHash
			}
		}
		t.Fatal("no hooks artifact in served doc")
		return ""
	}
	genuine := attestationPayload{Managed: true, Platform: "linux/amd64",
		Hashes: map[string]string{"hooks.claude-code": hooksHash(linuxDoc)}}
	if got := verifyAttestation(genuine, "claude-code", again); got != store.AttestationManaged {
		t.Fatalf("served hash attests %q, want managed", got)
	}
	laundered := attestationPayload{Managed: true, Platform: "linux/amd64",
		Hashes: map[string]string{"hooks.claude-code": hooksHash(windowsDoc)}}
	if got := verifyAttestation(laundered, "claude-code", again); got != store.AttestationNone {
		t.Fatalf("cross-platform content attests %q, want none", got)
	}
}

// TestSessionsWiringStatus pins the wiring-status read model: the admin sessions
// list classifies each session's reported managed-wiring hash against the
// published render and the registry, entirely from data already stored (no
// migration): current / allowed (registered but not the current render) /
// mismatch / unmeasured, with admin harnesses blank.
func TestSessionsWiringStatus(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	grantAdmin(t, app, user.ID)

	doc, _ := fetchHarnessConfig(t, base, "claude-code", "linux")
	var current string
	for _, art := range doc.Artifacts {
		if art.Name == "hooks.claude-code" {
			current = art.ContentHash
		}
	}
	if current == "" {
		t.Fatal("no hooks artifact served")
	}
	allowed := "sha256:" + strings.Repeat("ab", 32)
	if _, err := app.store.AttestationHashes().Create(ctx, store.AttestationHash{
		Artifact: "hooks.claude-code", Harness: "claude-code", Platform: "linux/amd64",
		Hash: allowed, Note: "per-box custom layout",
	}); err != nil {
		t.Fatal(err)
	}

	mk := func(harness, hashesJSON string) string {
		t.Helper()
		ses, err := app.store.Sessions().Create(ctx, store.Session{
			UserID: user.ID, HarnessName: harness, HarnessVersion: "1.0",
			AttestationLevel: "managed", AttestationHashes: hashesJSON,
		})
		if err != nil {
			t.Fatal(err)
		}
		return ses.ID
	}
	want := map[string][2]string{
		mk("claude-code", `{"hooks.claude-code":"`+current+`"}`): {"current", current},
		mk("claude-code", `{"hooks.claude-code":"`+allowed+`"}`): {"allowed", allowed},
		mk("claude-code", `{"hooks.claude-code":"sha256:`+strings.Repeat("ff", 32)+`"}`): {
			"mismatch", "sha256:" + strings.Repeat("ff", 32)},
		mk("claude-code", `{"self":"sha256:beef"}`): {"unmeasured", ""},
		mk("strazactl", `{}`):                       {"", ""},
	}

	var list []sessionPayload
	if code := adminReq(t, "GET", base+"/v1/admin/sessions", idToken, nil, &list); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	seen := 0
	for _, s := range list {
		exp, ok := want[s.ID]
		if !ok {
			continue
		}
		seen++
		if s.WiringStatus != exp[0] || s.WiringHash != exp[1] {
			t.Errorf("session %s: wiring %q/%q, want %q/%q", s.ID, s.WiringStatus, s.WiringHash, exp[0], exp[1])
		}
	}
	if seen != len(want) {
		t.Fatalf("only %d of %d crafted sessions surfaced", seen, len(want))
	}
}
