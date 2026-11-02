package service

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File names in a uid's directory under the state directory.
const (
	recordFile   = "enrollment.json"
	keyFile      = "key.pem"
	snapshotFile = "snapshot.cbor"
	spoolFile    = "audit-spool.jsonl"
	sessionsFile = "sessions.json"
)

// systemMachine reads the machine identity the bindings hash:
// /etc/machine-id on Linux, IOPlatformUUID on macOS and MachineGuid on
// Windows. It returns "" for a system without one, which then gets a random
// identity kept in the state directory, and an error stops the service.
// Tests replace it.
var systemMachine = readSystemMachine

// enrollment is the record of one uid's enrollment, kept as JSON with mode
// 0600 beside the key. Kind is empty for a key `straza keygen` made that is
// not enrolled yet. Counter is the last key check-in counter handed out,
// and WaitUntil holds the next key check-in after a lost answer. InFlight
// is the counter of a key check-in sent and not yet answered, with the
// arrival time of the nonce it carries, zero for none.
type enrollment struct {
	ServerURL       string            `json:"server_url,omitempty"`
	Kind            string            `json:"kind,omitempty"`
	Lane            string            `json:"lane,omitempty"`
	Username        string            `json:"username,omitempty"`
	UserID          string            `json:"user_id,omitempty"`
	DeviceID        string            `json:"device_id,omitempty"`
	KeyThumbprint   string            `json:"key_thumbprint"`
	Custody         string            `json:"custody,omitempty"`
	SnapshotKeys    map[string]string `json:"snapshot_keys,omitempty"`
	BindingKey      string            `json:"binding_key,omitempty"`
	Binding         string            `json:"binding,omitempty"`
	SignedInAt      time.Time         `json:"signed_in_at,omitzero"`
	SignInExpiresIn int               `json:"sign_in_expires_in,omitempty"`
	Counter         int64             `json:"counter"`
	WaitUntil       time.Time         `json:"wait_until,omitzero"`
	InFlight        int64             `json:"in_flight,omitempty"`
	InFlightNonceAt time.Time         `json:"in_flight_nonce_at,omitzero"`
}

// readRecord reads the record at path. A missing file returns nil and no
// error.
func readRecord(path string) (*enrollment, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the service's own state directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec enrollment
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("the enrollment record %s is not valid JSON: %w", path, err)
	}
	return &rec, nil
}

// writeRecord writes rec to path atomically and syncs it.
func writeRecord(path string, rec enrollment) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw)
}

// readKey reads the PKCS #8 PEM Ed25519 key at path. A missing file
// returns nil and no error.
func readKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the service's own state directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("the key file %s holds no PKCS #8 PEM private key", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the key file %s holds a key that does not parse: %w", path, err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the key file %s holds a key that is not Ed25519", path)
	}
	return key, nil
}

// writeKey writes key to path as PKCS #8 PEM, atomically.
func writeKey(path string, key ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode the key: %w", err)
	}
	return writeFileAtomic(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// writeFileAtomic writes data to path with mode 0600 via a same-directory
// temp file that is synced and then put over the target with
// replaceDurably, so a reader or a crash never sees a half-written file and
// a synced write survives a power loss. The temp file is removed on any
// error.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return replaceDurably(tmp, path)
}

// machineIdentity returns the machine identity: the system's own
// (systemMachine), or for a system without one a random identity made once
// and kept in stateDir.
func machineIdentity(stateDir string) (string, error) {
	system, err := systemMachine()
	if err != nil {
		return "", err
	}
	if system != "" {
		return system, nil
	}
	path := filepath.Join(stateDir, "machine-id")
	raw, err := os.ReadFile(path) // #nosec G304 -- the service's own state directory
	switch {
	case err == nil && strings.TrimSpace(string(raw)) != "":
		return strings.TrimSpace(string(raw)), nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("read the machine identity %s: %w", path, err)
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	value := hex.EncodeToString(id)
	if err := writeFileAtomic(path, []byte(value+"\n")); err != nil {
		return "", fmt.Errorf("keep the machine identity in %s: %w", path, err)
	}
	return value, nil
}

// newBinding returns a fresh per-enrollment binding key and the binding of
// machine under it.
func newBinding(machine string) (key, binding string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	key = base64.RawURLEncoding.EncodeToString(raw)
	return key, bindingOf(key, machine), nil
}

// bindingOf is the machine binding: the unpadded base64url HMAC-SHA256 of
// machine under the enrollment's binding key. It is empty for a key that
// does not decode, which never matches a stored binding.
func bindingOf(key, machine string) string {
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte(machine))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// bindingHolds reports whether rec was enrolled on machine.
func bindingHolds(rec enrollment, machine string) bool {
	return rec.Binding != "" && hmac.Equal([]byte(rec.Binding), []byte(bindingOf(rec.BindingKey, machine)))
}
