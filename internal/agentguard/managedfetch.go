package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/strazahq/straza/internal/harnesscfg"
)

// The install fetch lane (spec/harness-config): a default-layout box writes
// the SERVER-published wiring verbatim, so its on-disk hashes equal the
// boot-registered registry rows, including under version skew (an old
// client installs what the newer server expects). Security boundaries, in
// order: only the sudo one-shot writes; the document is verified against the
// enroll-pinned snapshot keys BEFORE any write and a crypto failure ABORTS
// (never a fallback: bad crypto is evidence, not inconvenience); the server
// never names paths: artifacts carry only content plus a registry name the
// client maps to its own fixed resolvers, and an unknown name is refused
// outright. Benign deviations (custom bin dir, operator content in the file,
// unreachable or pre-0.54.0 server) take the LOCAL lane: the local render,
// nothing dropped, per-box hash registration. Foreign content is preserved
// rather than overwritten, because the codex lockdown line is operator
// content straza itself recommends.

// vendorDefaultBinPath resolves the vendor-default managed binary path the
// server renders for; a seam so tests can point the default at a temp
// layout.
var vendorDefaultBinPath = defaultManagedBinaryPath

// FetchHarnessConfig downloads the signed server-rendered managed config
// document for one harness+GOOS (GET /v1/harness-config).
func (c *Client) FetchHarnessConfig(ctx context.Context, harness, goos string) (harnesscfg.Document, error) {
	q := url.Values{"harness": {harness}, "platform": {goos}}
	var doc harnesscfg.Document
	err := c.getJSON(ctx, "/v1/harness-config?"+q.Encode(), &doc)
	return doc, err
}

// fetchServedWiring decides one harness's install lane. A non-nil document
// means the server lane: verified, name-checked, ready to write verbatim.
// A nil document with a note means the local lane, and the note is printed
// so the operator knows which lane ran and why. An error aborts the install.
func fetchServedWiring(ctx context.Context, cfg Config, harness, binPath string) (*harnesscfg.Document, string, error) {
	goos := osName()
	if binPath != vendorDefaultBinPath(goos) {
		return nil, "custom binary path " + binPath + " (the server publishes the vendor-default layout)", nil
	}
	lookup, err := keyLookup(cfg.SnapshotKeys)
	if err != nil {
		return nil, "", err
	}
	doc, err := NewClient(cfg.ServerURL).FetchHarnessConfig(ctx, harness, goos)
	if err != nil {
		// Unreachable or unpublished (pre-0.54.0 server, 404) is benign: the
		// local render is byte-identical by construction for this build.
		return nil, "no published artifact fetched (" + err.Error() + ")", nil
	}
	if err := harnesscfg.Verify(doc, harnesscfg.KeyLookup(lookup)); err != nil {
		return nil, "", fmt.Errorf("refusing the served harness config for %s: verification failed: %w. Not falling back: resolve this before installing (re-enroll refreshes pinned keys; a persistent failure means the server or the path to it cannot be trusted)", harness, err)
	}
	if doc.Harness != harness || doc.Platform != goos {
		return nil, "", fmt.Errorf("server answered a document for %s/%s when %s/%s was requested; refusing", doc.Harness, doc.Platform, harness, goos)
	}
	for _, art := range doc.Artifacts {
		if art.Name != "hooks."+harness && art.Name != "mcp."+harness {
			return nil, "", fmt.Errorf("served document carries unknown artifact %q; refusing to write it anywhere (this client maps only hooks.%s and mcp.%s to paths)", art.Name, harness, harness)
		}
	}
	// Disk already equal to the served artifacts is by definition
	// server-published state (an earlier fetch-lane run), so the foreignness
	// heuristics below must not get a vote: they cannot tell a NEWER server
	// render from operator content, and a re-run must be a no-op, not a
	// lane flip.
	if servedMatchesDisk(harness, &doc) {
		return &doc, "", nil
	}
	if foreign, where, err := managedWiringForeign(harness); err != nil {
		return nil, "", err
	} else if foreign {
		return nil, "operator content preserved in " + where + " (movable to the customer overlay once it exists)", nil
	}
	return &doc, "", nil
}

// servedMatchesDisk reports whether every served artifact already sits on
// disk byte for byte.
func servedMatchesDisk(harness string, doc *harnesscfg.Document) bool {
	for _, art := range doc.Artifacts {
		var path string
		var err error
		switch art.Name {
		case "hooks." + harness:
			path, err = managedHookPath(harness)
		case "mcp." + harness:
			path, err = ManagedMCPConfigPath(harness)
		}
		if err != nil || path == "" {
			return false
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- straza's own managed file
		if err != nil || !bytes.Equal(raw, art.Content) {
			return false
		}
	}
	return true
}

// managedWiringForeign reports whether a harness's managed files carry
// operator content beyond straza's own entries; where names the file for
// the printed lane reason. Foreign content forces the local (merge) lane so
// nothing an operator added is ever dropped.
func managedWiringForeign(harness string) (bool, string, error) {
	path, err := managedHookPath(harness)
	if err != nil {
		return false, "", err
	}
	if harness == "codex" {
		foreign, err := codexRequirementsForeign(path)
		if err != nil || foreign {
			return foreign, path, err
		}
	} else {
		foreign, err := settingsForeign(harness, path)
		if err != nil || foreign {
			return foreign, path, err
		}
	}
	mcpPath, err := ManagedMCPConfigPath(harness)
	if err != nil || mcpPath == "" {
		return false, "", err
	}
	foreign, err := managedMCPForeign(mcpPath)
	return foreign, mcpPath, err
}

// codexRequirementsForeign: anything outside straza's marker block is the
// operator's (the lockdown line straza recommends lives exactly there).
func codexRequirementsForeign(path string) (bool, error) {
	content, existed, err := readCodexConfig(path)
	if err != nil || !existed {
		return false, err
	}
	lines := strings.Split(content, "\n")
	begin, end, err := findCodexManagedRequirementsBlock(lines)
	if err != nil {
		return false, err
	}
	if begin >= 0 {
		lines = append(append([]string{}, lines[:begin]...), lines[end+1:]...)
	}
	return strings.TrimSpace(strings.Join(lines, "\n")) != "", nil
}

// settingsForeign: strip straza's own hook entries (and gemini's bare
// enabled-pin); any remaining key or hook is the operator's.
func settingsForeign(harness, path string) (bool, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- straza's own managed file
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, fmt.Errorf("existing %s is not valid JSON: %w", filepath.Base(path), err)
	}
	if hooks, _ := settings["hooks"].(map[string]any); hooks != nil {
		removeStrazaHooks(hooks, allEventsIn(hooks), hookMarker(harness), "")
		if len(hooks) == 0 {
			delete(settings, "hooks")
		}
	}
	if harness == "gemini" {
		if hc, _ := settings["hooksConfig"].(map[string]any); len(hc) == 1 && hc["enabled"] == true {
			delete(settings, "hooksConfig")
		}
	}
	return len(settings) > 0, nil
}

// managedMCPForeign: entries besides the straza proxy (or any other key)
// are operator policy; the exclusive-control lever is theirs to compose.
func managedMCPForeign(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	cfg, err := readJSONMap(path)
	if err != nil {
		return false, err
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	for name := range servers {
		if name != mcpServerName {
			return true, nil
		}
	}
	for key := range cfg {
		if key != "mcpServers" {
			return true, nil
		}
	}
	return false, nil
}

// writeServedArtifacts lands a verified document on disk, verbatim, through
// the same fixed resolvers the local lane uses. The windows codex shim is
// written first, exactly like the local writer: between the two writes the
// worst state is a shim nothing invokes, never hooks invoking a missing
// shim.
func writeServedArtifacts(harness string, doc *harnesscfg.Document, binPath string) error {
	if harness == "codex" && osName() == "windows" {
		if _, err := writeCodexHookShim(binPath); err != nil {
			return err
		}
	}
	for _, art := range doc.Artifacts {
		var path string
		var err error
		switch art.Name {
		case "hooks." + harness:
			path, err = managedHookPath(harness)
		case "mcp." + harness:
			path, err = ManagedMCPConfigPath(harness)
			if err == nil && path == "" {
				return fmt.Errorf("server published %s but %s has no vendor-documented managed MCP file on this platform", art.Name, harness)
			}
		}
		if err != nil {
			return err
		}
		if err := writeManagedVerbatim(path, art.Content); err != nil {
			return fmt.Errorf("write %s (need sudo/admin?): %w", art.Name, err)
		}
	}
	return nil
}

// writeManagedVerbatim writes managed-layout bytes: fresh files 0644 in 0755
// dirs (world-readable by design, root-writable only), an existing file
// keeps the mode its operator chose, exactly like every managed writer.
func writeManagedVerbatim(path string, content []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- managed layout is deliberately world-readable
		return err
	}
	return os.WriteFile(path, content, mode) // #nosec G306 -- managed layout is deliberately world-readable
}

// docHasArtifact reports whether a served document carries the named
// artifact.
func docHasArtifact(doc *harnesscfg.Document, name string) bool {
	for _, art := range doc.Artifacts {
		if art.Name == name {
			return true
		}
	}
	return false
}

// servedArtifactID is the short identity install output prints for a served
// document (the hooks artifact's hash, the one attestation verifies).
func servedArtifactID(harness string, doc *harnesscfg.Document) string {
	for _, art := range doc.Artifacts {
		if art.Name == "hooks."+harness {
			return strings.TrimPrefix(art.ContentHash, "sha256:")[:12]
		}
	}
	return ""
}
