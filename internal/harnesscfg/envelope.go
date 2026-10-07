// Package harnesscfg defines the signed harness-config artifact
// (spec/harness-config v1alpha1): the wire shape strazad serves at
// GET /v1/harness-config and `straza install --managed` verifies against the
// snapshot keys pinned at enroll. Rendering lives in internal/agentguard (the
// installer logic is the single source of truth for content). This package
// holds only the envelope and its crypto, shared by server and client so the
// two cannot drift. It is deliberately NOT the policy-snapshot envelope: that
// one is kind-less and its verification is fused to policy.Engine construction.
package harnesscfg

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Format is the envelope format version. It changes only through the spec
// process (spec/harness-config).
const Format = 1

// Kind discriminates this artifact from every other signed document.
const Kind = "harness-config"

// signingDomain domain-separates harness-config signatures from every other
// use of the snapshot signing keys: the policy snapshot signs raw CBOR
// payload bytes with the same key, and without a domain prefix one signed
// blob could in principle be replayed as the other.
const signingDomain = "straza.harness-config.v1"

// Artifact is one managed file: content plus its detached signature. Name is
// the attestation-registry artifact key ("hooks.<harness>", "mcp.<harness>")
// and ContentHash is agentguard's fileSHA256 form (sha256:<hex>), so a served
// artifact, a registry row, and a measured on-disk file are directly
// comparable.
type Artifact struct {
	Name        string `json:"artifact"`
	Content     []byte `json:"content"`
	ContentHash string `json:"content_hash"`
	KeyID       string `json:"key_id"`
	Sig         []byte `json:"sig"`
}

// Document is the served envelope: every managed artifact for one harness on
// one platform (GOOS; content varies by OS only, never by architecture).
type Document struct {
	Format    int        `json:"format"`
	Kind      string     `json:"kind"`
	Harness   string     `json:"harness"`
	Platform  string     `json:"platform"`
	Artifacts []Artifact `json:"artifacts"`
}

// KeyLookup resolves a key id to its ed25519 public key.
type KeyLookup func(kid string) (ed25519.PublicKey, bool)

// signingInput builds the byte string a signature covers. NUL-delimited so no
// two field splits can produce the same input; signable fields therefore must
// be NUL-free and non-empty (enforced by SignArtifact).
func signingInput(harness, platform, name string, content []byte) []byte {
	var b bytes.Buffer
	b.WriteString(signingDomain)
	b.WriteByte(0)
	b.WriteString(harness)
	b.WriteByte(0)
	b.WriteString(platform)
	b.WriteByte(0)
	b.WriteString(name)
	b.WriteByte(0)
	b.Write(content)
	return b.Bytes()
}

// SignArtifact hashes and signs one artifact's content bound to its harness,
// platform, and registry name.
func SignArtifact(kid string, priv ed25519.PrivateKey, harness, platform, name string, content []byte) (Artifact, error) {
	for field, v := range map[string]string{"harness": harness, "platform": platform, "artifact name": name} {
		if v == "" || strings.ContainsRune(v, 0) {
			return Artifact{}, fmt.Errorf("harnesscfg: %s %q is empty or contains NUL: unsignable", field, v)
		}
	}
	sum := sha256.Sum256(content)
	return Artifact{
		Name:        name,
		Content:     content,
		ContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		KeyID:       kid,
		Sig:         ed25519.Sign(priv, signingInput(harness, platform, name, content)),
	}, nil
}

// Verify checks a whole document: envelope discriminators, per-artifact
// content hashes, and every signature against the resolved keys. Any failure
// is an error naming what broke. Callers fail closed on it, because clients
// verify before use.
func Verify(doc Document, lookup KeyLookup) error {
	if doc.Format != Format {
		return fmt.Errorf("harnesscfg: unsupported format %d (this build speaks format %d)", doc.Format, Format)
	}
	if doc.Kind != Kind {
		return fmt.Errorf("harnesscfg: kind %q is not %q", doc.Kind, Kind)
	}
	if doc.Harness == "" || doc.Platform == "" {
		return fmt.Errorf("harnesscfg: document names no harness/platform")
	}
	if len(doc.Artifacts) == 0 {
		return fmt.Errorf("harnesscfg: document carries no artifact")
	}
	for _, art := range doc.Artifacts {
		if art.Name == "" {
			return fmt.Errorf("harnesscfg: unnamed artifact")
		}
		sum := sha256.Sum256(art.Content)
		if want := "sha256:" + hex.EncodeToString(sum[:]); art.ContentHash != want {
			return fmt.Errorf("harnesscfg: %s content hash mismatch (declared %s, content is %s)", art.Name, art.ContentHash, want)
		}
		pub, ok := lookup(art.KeyID)
		if !ok {
			return fmt.Errorf("harnesscfg: %s signed by unknown key %q (re-enroll to refresh pinned keys?)", art.Name, art.KeyID)
		}
		if !ed25519.Verify(pub, signingInput(doc.Harness, doc.Platform, art.Name, art.Content), art.Sig) {
			return fmt.Errorf("harnesscfg: %s signature invalid", art.Name)
		}
	}
	return nil
}
