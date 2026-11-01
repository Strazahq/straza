package agentguard

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/agentguard/trace"
)

// Home returns the straza user-mode state root: $STRAZA_HOME
// or ~/.straza, the user layer. The managed layout is elsewhere.
func Home() (string, error) {
	if v := os.Getenv("STRAZA_HOME"); v != "" {
		return v, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home: %w", err)
	}
	return filepath.Join(h, ".straza"), nil
}

// Config is the persisted straza configuration (~/.straza/config.yaml).
type Config struct {
	ServerURL string `yaml:"serverUrl"`
	// SnapshotKeys are the pinned base64 ed25519 snapshot verification keys
	// (fetched at enroll from /.well-known/straza/snapshot-keys.json).
	SnapshotKeys map[string]string `yaml:"snapshotKeys"`
	// SnapshotLagSeconds bounds how far behind the active policy a busy
	// session may run: after a decision, the Straza background service asks
	// the server for a newer snapshot at most this often, with a conditional
	// fetch that costs a 304 when nothing changed. 0 = default 30.
	SnapshotLagSeconds int `yaml:"snapshotLagSeconds,omitempty"`
	// Trace is the install's diagnostic trace level: "" or "journal" (the
	// default: the always-on content-free decision journal, and the user's
	// `straza trace on` window may raise it to debug), "off" (nothing is
	// written and the window cannot be opened; the error log errors.jsonl is
	// unaffected and always written), or "debug" (debug forced on, no window
	// needed). The managed config wins whole-file, so an operator's value
	// here applies to every user of the install.
	Trace string `yaml:"trace,omitempty"`
}

// Store is the straza on-disk state manager. Always handled by pointer (it
// carries the lazily built trace logger and its sync.Once).
type Store struct {
	root string

	traceOnce sync.Once
	tracer    *trace.Logger
}

// OpenStore returns the state store rooted at Home().
func OpenStore() (*Store, error) {
	root, err := Home()
	if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) configPath() string        { return filepath.Join(s.root, "config.yaml") }
func (s *Store) statePath(n string) string { return filepath.Join(s.root, "state", n) }

// LoadConfig reads the managed system config when present, falling back
// to the user config.yaml. The root-owned config wins so a user-writable file
// cannot re-point enforcement at a rogue server.
func (s *Store) LoadConfig() (Config, error) {
	var c Config
	if raw, err := os.ReadFile(ManagedConfigPath()); err == nil { // #nosec G304 -- fixed managed layout path
		if err := yaml.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("parse managed config: %w", err)
		}
		return c, nil
	}
	raw, err := os.ReadFile(s.configPath())
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	return c, nil
}
