package service

import (
	"os"
	"runtime"
	"sync"

	"github.com/strazahq/straza/internal/agentguard"
)

// selfHash hashes the running executable once per process. The running
// image cannot change, and after an upgrade swaps the file the cached value
// is the truer measurement of what executes.
var selfHash = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	h, err := agentguard.FileSHA256(exe)
	if err != nil {
		return ""
	}
	return h
})

// measureAttestation measures what the service's decisions depend on: its
// own binary always, and with the managed layout installed the root-owned
// managed config and this harness's managed hook wiring. A tampered or
// deleted managed file changes or drops a measurement, which the server
// maps to att=none. Wiring under a person's home is user-writable and out
// of the service's sight, so it is never measured.
func measureAttestation(harness string) agentguard.Attestation {
	att := agentguard.Attestation{Platform: runtime.GOOS + "/" + runtime.GOARCH, Hashes: map[string]string{}}
	if h := selfHash(); h != "" {
		att.Hashes["self"] = h
	}
	if !agentguard.ManagedInstalled() {
		return att
	}
	att.Managed = true
	if h, err := agentguard.FileSHA256(agentguard.ManagedConfigPath()); err == nil {
		att.Hashes["config"] = h
	}
	if path, err := agentguard.ManagedHookPath(harness); err == nil {
		if h, err := agentguard.FileSHA256(path); err == nil {
			att.Hashes["hooks."+harness] = h
		}
	}
	return att
}
