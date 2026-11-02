package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// state is what dpopclient keeps between runs: the server, the enrolled
// device, the highest counter it sent, the session and the newest nonce.
// The file holds the session token, so it is written with mode 0600.
type state struct {
	Server        string `json:"server"`
	UserID        string `json:"user_id,omitempty"`
	Username      string `json:"username,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	KeyThumbprint string `json:"key_thumbprint,omitempty"`
	Counter       int64  `json:"counter"`
	Harness       string `json:"harness,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	SessionToken  string `json:"session_token,omitempty"`
	Nonce         string `json:"nonce,omitempty"`
}

// loadState reads the state file at path. A missing file is an empty state
// when allowMissing is set, and an error that says to enroll otherwise.
func loadState(path string, allowMissing bool) (state, error) {
	var st state
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the state file
	if errors.Is(err, fs.ErrNotExist) && allowMissing {
		return st, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return st, fmt.Errorf("the state file %s does not exist. Run dpopclient enroll first", path)
	}
	if err != nil {
		return st, fmt.Errorf("read the state file %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, fmt.Errorf("the state file %s is not valid JSON: %w. Enroll again with a new state file", path, err)
	}
	return st, nil
}

// saveState writes st to path with mode 0600.
func saveState(path string, st state) error {
	raw, err := json.MarshalIndent(st, "", "  ") // #nosec G117 -- this IS the test client's state file (0600)
	if err != nil {
		return fmt.Errorf("encode the state: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write the state file %s: %w", path, err)
	}
	return nil
}

// loadKey reads the PKCS #8 PEM Ed25519 key at path.
func loadKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the key file
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("the key file %s does not exist. Run dpopclient enroll first, which writes it", path)
	}
	if err != nil {
		return nil, fmt.Errorf("read the key file %s: %w", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("the key file %s holds no PKCS #8 PEM private key. Run dpopclient enroll again", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the key file %s holds a key that does not parse: %w", path, err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the key file %s holds a key that is not Ed25519. Run dpopclient enroll again", path)
	}
	return key, nil
}

// writeKey writes key to path with mode 0600, replacing an earlier key file
// in one rename, so a reader sees the old key or the new one and never half
// of either.
func writeKey(path string, key ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode the key: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dpopclient-key-*") // CreateTemp makes the file with mode 0600
	if err != nil {
		return fmt.Errorf("write the key file %s: %w", path, err)
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return fmt.Errorf("write the key file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("write the key file %s: %w", path, err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("write the key file %s: %w", path, err)
	}
	return nil
}
