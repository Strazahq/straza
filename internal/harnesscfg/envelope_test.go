package harnesscfg

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

// testKey returns a deterministic keypair for table cases (seed bytes are
// arbitrary; determinism keeps failure output stable).
func testKey(t *testing.T, fill byte) (string, ed25519.PrivateKey, KeyLookup) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = fill
	}
	priv := ed25519.NewKeyFromSeed(seed)
	kid := "test-key-" + string(rune('a'+fill%26))
	pub := priv.Public().(ed25519.PublicKey)
	lookup := func(k string) (ed25519.PublicKey, bool) {
		if k == kid {
			return pub, true
		}
		return nil, false
	}
	return kid, priv, lookup
}

func signedDoc(t *testing.T, kid string, priv ed25519.PrivateKey) Document {
	t.Helper()
	doc := Document{Format: Format, Kind: Kind, Harness: "claude-code", Platform: "linux"}
	for name, content := range map[string]string{
		"hooks.claude-code": `{"hooks":{}}`,
		"mcp.claude-code":   `{"mcpServers":{}}`,
	} {
		art, err := SignArtifact(kid, priv, doc.Harness, doc.Platform, name, []byte(content))
		if err != nil {
			t.Fatalf("SignArtifact(%s): %v", name, err)
		}
		doc.Artifacts = append(doc.Artifacts, art)
	}
	return doc
}

// TestSignVerifyRoundtrip pins the happy path plus the hash format the
// attestation registry consumes (sha256:<hex>, agentguard's fileSHA256 form:
// the served content hash and the measured on-disk hash MUST be comparable).
func TestSignVerifyRoundtrip(t *testing.T) {
	kid, priv, lookup := testKey(t, 1)
	doc := signedDoc(t, kid, priv)
	if err := Verify(doc, lookup); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, art := range doc.Artifacts {
		if !strings.HasPrefix(art.ContentHash, "sha256:") || len(art.ContentHash) != len("sha256:")+64 {
			t.Fatalf("content hash %q is not sha256:<hex64>", art.ContentHash)
		}
	}
}

// TestVerifyRejectsTampering is the table of every field an attacker could
// rewrite in transit or at rest; each mutation must fail verification with an
// error naming the artifact.
func TestVerifyRejectsTampering(t *testing.T) {
	kid, priv, lookup := testKey(t, 2)
	_, otherPriv, _ := testKey(t, 3)

	cases := []struct {
		name    string
		mutate  func(*Document)
		wantErr string
	}{
		{"content swapped, hash stale", func(d *Document) {
			d.Artifacts[0].Content = []byte(`{"hooks":{"evil":true}}`)
		}, "content hash"},
		{"content and hash swapped consistently", func(d *Document) {
			art, err := SignArtifact(kid, otherPriv, d.Harness, d.Platform,
				d.Artifacts[0].Name, []byte(`{"hooks":{"evil":true}}`))
			if err != nil {
				t.Fatal(err)
			}
			// Keep the original key id so the lookup resolves: only the
			// signature (from the wrong key) must fail.
			art.KeyID = kid
			d.Artifacts[0] = art
		}, "signature"},
		{"artifact renamed", func(d *Document) {
			d.Artifacts[0].Name = "hooks.codex"
		}, "signature"},
		{"harness rebound", func(d *Document) { d.Harness = "codex" }, "signature"},
		{"platform rebound", func(d *Document) { d.Platform = "windows" }, "signature"},
		{"unknown key id", func(d *Document) { d.Artifacts[0].KeyID = "ghost" }, "key"},
		{"wrong format", func(d *Document) { d.Format = 99 }, "format"},
		{"wrong kind", func(d *Document) { d.Kind = "policy-snapshot" }, "kind"},
		{"no artifacts", func(d *Document) { d.Artifacts = nil }, "artifact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := signedDoc(t, kid, priv)
			tc.mutate(&doc)
			err := Verify(doc, lookup)
			if err == nil {
				t.Fatal("Verify accepted a tampered document")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestSignArtifactRejectsUnsignableMetadata: the signing input is
// NUL-delimited, so a NUL inside a metadata field would let two different
// field splits share one signature. Refused at signing time.
func TestSignArtifactRejectsUnsignableMetadata(t *testing.T) {
	kid, priv, _ := testKey(t, 4)
	cases := []struct {
		name                     string
		harness, platform, aname string
	}{
		{"nul in harness", "claude\x00code", "linux", "hooks.x"},
		{"nul in platform", "claude-code", "li\x00nux", "hooks.x"},
		{"nul in artifact name", "claude-code", "linux", "hooks\x00x"},
		{"empty harness", "", "linux", "hooks.x"},
		{"empty platform", "claude-code", "", "hooks.x"},
		{"empty artifact name", "claude-code", "linux", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := SignArtifact(kid, priv, tc.harness, tc.platform, tc.aname, []byte("x")); err == nil {
				t.Fatal("SignArtifact accepted unsignable metadata")
			}
		})
	}
}
