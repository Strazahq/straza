// Package secrets is the credential plane: a SecretsProvider seam with a
// built-in NaCl-secretbox implementation (KEK from file), and the in-memory
// Broker that resolves role-bound static secrets on gateway request paths
// without touching the database. Plaintext exists only transiently in
// gateway/runtime memory, and no API surface returns it.
package secrets

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/nacl/secretbox"
)

// Provider seals and opens secret material. `builtin` is secretbox with a
// file KEK.
type Provider interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(ciphertext []byte) ([]byte, error)
}

const (
	kekSize   = 32
	nonceSize = 24
)

// Builtin is the secretbox Provider. Ciphertext layout: nonce || box.
type Builtin struct {
	key [kekSize]byte
}

// LoadOrCreateKEK reads the key-encryption key from path, generating and
// persisting one (0600) when absent: the zero-config standalone path.
// Enterprise deployments should provision the file themselves.
func LoadOrCreateKEK(path string) (*Builtin, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-configured key path
	switch {
	case err == nil:
		if len(raw) != kekSize {
			return nil, fmt.Errorf("secrets: KEK %s must be exactly %d bytes, got %d", path, kekSize, len(raw))
		}
	case os.IsNotExist(err):
		raw = make([]byte, kekSize)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("secrets: generate KEK: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("secrets: KEK dir: %w", err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return nil, fmt.Errorf("secrets: persist KEK: %w", err)
		}
	default:
		return nil, fmt.Errorf("secrets: read KEK %s: %w", path, err)
	}
	b := &Builtin{}
	copy(b.key[:], raw)
	return b, nil
}

// Seal encrypts plaintext under the KEK with a random nonce.
func (b *Builtin) Seal(plaintext []byte) ([]byte, error) {
	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	return secretbox.Seal(nonce[:], plaintext, &nonce, &b.key), nil
}

// Open authenticates and decrypts ciphertext; any tamper fails.
func (b *Builtin) Open(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < nonceSize+secretbox.Overhead {
		return nil, fmt.Errorf("secrets: ciphertext too short")
	}
	var nonce [nonceSize]byte
	copy(nonce[:], ciphertext[:nonceSize])
	out, ok := secretbox.Open(nil, ciphertext[nonceSize:], &nonce, &b.key)
	if !ok {
		return nil, fmt.Errorf("secrets: decrypt failed (wrong KEK or tampered ciphertext)")
	}
	return out, nil
}
