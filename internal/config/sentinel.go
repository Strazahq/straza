package config

import (
	"fmt"
	"time"
)

// Sentinel configures the audit sentinel: an in-process JetStream consumer
// over the audit stream that emits straza.audit.sentinel verdicts. OPTIONAL and
// default-off in BOTH profiles: it is detection, not enforcement; a
// deployment without it pays zero on the request path. Alert-only: it has
// no auto-revoke knob by design.
type Sentinel struct {
	// Enabled boots the sentinel consumer inside strazad.
	Enabled bool `yaml:"enabled"`
	// DenyBurstWarn / DenyBurstCritical are the deny counts within
	// DenyBurstWindow that raise a warn / critical deny-burst verdict
	// (defaults 5 / 10 within 60s).
	DenyBurstWarn     int           `yaml:"denyBurstWarn"`
	DenyBurstCritical int           `yaml:"denyBurstCritical"`
	DenyBurstWindow   time.Duration `yaml:"denyBurstWindow"`
	// VariantWindow is how long a denied shell command is remembered for the
	// deny-then-variant detector (default 10m).
	VariantWindow time.Duration `yaml:"variantWindow"`
	// WriteExecWindow is how long written paths are remembered for the
	// write-then-execute detector (default 30m).
	WriteExecWindow time.Duration `yaml:"writeExecWindow"`
	// BaselineMinEvents is how many audit events a user must accrue before
	// their tool-mix baseline counts as established (default 50).
	BaselineMinEvents int `yaml:"baselineMinEvents"`
}

// validate rejects sentinel thresholds strazad cannot act on.
func (s Sentinel) validate() error {
	if s.DenyBurstWarn <= 0 || s.DenyBurstCritical <= 0 {
		return fmt.Errorf("governance.sentinel: denyBurstWarn and denyBurstCritical must be positive counts")
	}
	if s.DenyBurstCritical < s.DenyBurstWarn {
		return fmt.Errorf("governance.sentinel: denyBurstCritical (%d) must be >= denyBurstWarn (%d)",
			s.DenyBurstCritical, s.DenyBurstWarn)
	}
	if s.DenyBurstWindow <= 0 || s.VariantWindow <= 0 || s.WriteExecWindow <= 0 {
		return fmt.Errorf("governance.sentinel: detector windows must be positive durations")
	}
	if s.BaselineMinEvents <= 0 {
		return fmt.Errorf("governance.sentinel: baselineMinEvents must be positive")
	}
	return nil
}

// defaultSentinel returns the sentinel defaults shared by both profiles:
// disabled (the component is opt-in) with sane thresholds pre-filled so
// enabling it is a one-key change.
func defaultSentinel() Sentinel {
	return Sentinel{
		Enabled:           false,
		DenyBurstWarn:     5,
		DenyBurstCritical: 10,
		DenyBurstWindow:   time.Minute,
		VariantWindow:     10 * time.Minute,
		WriteExecWindow:   30 * time.Minute,
		BaselineMinEvents: 50,
	}
}
