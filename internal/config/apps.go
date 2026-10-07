package config

// Apps section: the MCP manager and the gateway's virtual tool
// catalog. Types, validation. The env face stays in config.go's applyEnv.

import (
	"fmt"
	"time"
)

// Apps configures the MCP manager.
type Apps struct {
	// Dir is the watched GitOps directory: an app.yaml dropped or
	// changed there becomes a draft a person publishes, and a removed one a
	// removal draft. Empty means the default "<dataDir>/apps", resolved at
	// boot by AppsDir.
	//
	// This field cannot switch the watcher OFF: AppsDir never returns empty,
	// so every boot builds a watcher. Disabling the GitOps channel would need
	// its own knob.
	Dir string `yaml:"dir"`
	// PollInterval paces the GitOps directory scan (default 1s).
	PollInterval time.Duration `yaml:"pollInterval"`
	// HealthInterval paces app health pings and inventory drift checks
	// (default 20s).
	HealthInterval time.Duration `yaml:"healthInterval"`
	// UpstreamTimeout bounds a single gateway→MCP-server tools/call so a slow
	// upstream cannot pin a gateway goroutine (default 30s).
	UpstreamTimeout time.Duration `yaml:"upstreamTimeout"`
	// AllowLoopbackUpstreams lets strazad dial a remote server published at
	// a loopback address, such as 127.0.0.1, which reaches strazad's own
	// host. Default true under the standalone profile, where an upstream often
	// runs on the same machine as strazad, and false under enterprise. Unspecified,
	// link-local and cloud metadata addresses are refused whatever it says,
	// and private addresses always pass.
	AllowLoopbackUpstreams bool `yaml:"allowLoopbackUpstreams"`
	// Catalog tunes the per-session virtual tool catalog the gateway serves.
	Catalog Catalog `yaml:"catalog"`
}

// Catalog tunes the gateway's virtual tool catalog (per-session tool
// visibility). Both knobs are governance-forward defaults: hide what policy
// denies, and warn when a role's catalog grows unwieldy.
type Catalog struct {
	// PolicyFilter, when true, drops policy-denied tools from a session's
	// tools/list entirely (invisible = nonexistent) instead of listing them and
	// answering the call with a deny reason. Default true in both profiles.
	// The `_straza_justification` schema injection for approve-gated tools is
	// independent of this knob; justification keeps working when it is off.
	PolicyFilter bool `yaml:"policyFilter"`
	// WarnSize logs a warning (and bumps straza_gateway_catalog_oversize_total)
	// when a role's built catalog exceeds this many tools, a signal to tighten
	// bindings. Default 100; -1 disables the check.
	WarnSize int `yaml:"warnSize"`
	// PageSize bounds a single gateway tools/list response: the catalog is
	// served in pages of this many tools, the client following an opaque cursor
	// for the rest. Default 200; <= 0 disables pagination (the whole catalog in
	// one response).
	PageSize int `yaml:"pageSize"`
}

// validate holds the apps section's checks.
func (a Apps) validate() error {
	if a.PollInterval < 0 || a.HealthInterval < 0 {
		return fmt.Errorf("apps.pollInterval and apps.healthInterval must be >= 0")
	}
	return nil
}

// applyEnvApps binds the apps section's env face; applyEnv
// (config.go) sequences the binders, and each face writes a field no other
// face writes.
func applyEnvApps(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	set("STRAZA_APPS_DIR", func(v string) { cfg.Apps.Dir = v })
	// Only "true" or "1" lifts the refusal, the idiom of the enabled knobs,
	// so an env typo never opens strazad's own host.
	set("STRAZA_APPS_ALLOW_LOOPBACK_UPSTREAMS", func(v string) { cfg.Apps.AllowLoopbackUpstreams = v == "true" || v == "1" })
}
