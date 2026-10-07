package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/harnesscfg"
)

// Managed wiring CONTENT verification: doctor's wiring
// checks prove shape (events registered, binary present); this check proves
// the BYTES, closing the gap where a hand-edited managed file reads as
// wired while the next session start drops to att=none. Three-way verdict
// per harness: matches the server-published artifact (ok), matches a
// legitimate local render only (stale or custom layout: warn online, ok
// with a skipped note offline), matches neither (fail online; warn offline,
// where a newer published version cannot be ruled out).

// managedContentCheck aggregates one Check over every Tier-1 harness with a
// managed presence. Nil when no managed layout exists (user-mode box:
// content is unmeasured by design).
func managedContentCheck(ctx context.Context, cfg Config) *Check {
	if !ManagedInstalled() {
		return nil
	}
	worst := checkOK
	var verdicts []string
	for _, harness := range []string{"claude-code", "codex", "gemini"} {
		verdict, status := managedContentVerdict(ctx, cfg, harness)
		if verdict == "" {
			continue
		}
		verdicts = append(verdicts, verdict)
		if rank(status) > rank(worst) {
			worst = status
		}
	}
	if len(verdicts) == 0 {
		return nil
	}
	hint := ""
	if worst != checkOK {
		hint = "re-run elevated `straza install --managed --server <server-url> <harness>` to converge (custom layouts: pass your --bin-dir). A file matching no legitimate render reads as tamper: the next session start drops to attestation none"
	}
	return &Check{"managed-content", worst, strings.Join(verdicts, "; "), hint}
}

func rank(status string) int {
	switch status {
	case checkFail:
		return 2
	case checkWarn:
		return 1
	}
	return 0
}

// managedContentVerdict inspects one harness. Empty verdict = no managed
// presence to judge (never installed here, and no governance claim to
// contradict).
func managedContentVerdict(ctx context.Context, cfg Config, harness string) (string, string) {
	path, err := managedHookPath(harness)
	if err != nil {
		return "", checkOK
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- straza's own managed file
	if os.IsNotExist(err) {
		// Missing managed wiring is tamper-shaped only when the harness
		// carries a straza governance claim elsewhere: user-scope wiring
		// (caps at advisory) beside a managed layout means the managed file
		// was deleted.
		if user, uerr := SettingsPath(harness); uerr == nil && fileMentionsStraz(user) {
			return harness + ": managed wiring MISSING at " + path + " while user-scope wiring exists (user scope caps attestation at advisory; deleted?)", checkFail
		}
		return "", checkOK
	}
	if err != nil {
		return harness + ": managed wiring unreadable (" + err.Error() + ")", checkWarn
	}

	doc, reachable, servedErr := fetchDoctorDoc(ctx, cfg, harness)
	if doc != nil {
		for _, art := range doc.Artifacts {
			if art.Name == "hooks."+harness && bytes.Equal(art.Content, raw) {
				return harness + ": matches the server-published artifact (" + servedArtifactID(harness, doc) + ")", checkOK
			}
		}
	}
	localMatch := matchesLocalRender(harness, raw)
	switch {
	case localMatch && doc != nil:
		return harness + ": matches this build's local render but NOT the published artifact (stale or custom layout)", checkWarn
	case localMatch && !reachable:
		return harness + ": matches this build's local render (published-artifact comparison skipped: server unreachable)", checkOK
	case localMatch:
		// Reachable but the served document was unusable: the local match is
		// real, the serving side deserves the eyeball.
		return harness + ": matches this build's local render; the served artifact could not be verified (" + servedErr.Error() + ")", checkFail
	case doc != nil || (reachable && servedErr != nil):
		return harness + ": managed wiring matches NO legitimate render (hand-edited or torn)", checkFail
	default:
		return harness + ": cannot verify offline (matches no local render; a newer published version cannot be ruled out)", checkWarn
	}
}

// fetchDoctorDoc fetches and verifies one harness's served document on a
// short bound. doc nil + reachable false = offline; doc nil + reachable
// true = served but unusable (err says why); unverified documents are never
// returned.
func fetchDoctorDoc(ctx context.Context, cfg Config, harness string) (*harnesscfg.Document, bool, error) {
	lookup, err := keyLookup(cfg.SnapshotKeys)
	if err != nil {
		return nil, false, err
	}
	fctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	doc, err := NewClient(cfg.ServerURL).FetchHarnessConfig(fctx, harness, osName())
	if err != nil {
		return nil, false, err
	}
	if err := harnesscfg.Verify(doc, harnesscfg.KeyLookup(lookup)); err != nil {
		return nil, true, fmt.Errorf("served document failed verification: %w", err)
	}
	return &doc, true, nil
}

// matchesLocalRender tries every plausible binary path this box could have
// installed with: the vendor default, the env-relocated managed path, and
// whatever the on-disk wiring itself invokes (custom --bin-dir installs).
func matchesLocalRender(harness string, raw []byte) bool {
	goos := osName()
	candidates := map[string]bool{
		vendorDefaultBinPath(goos): true,
		ManagedBinaryPath(""):      true,
	}
	for _, p := range wiringBinPaths(harness, raw) {
		candidates[p] = true
	}
	for binPath := range candidates {
		arts, err := RenderManagedArtifacts(harness, goos, binPath)
		if err == nil && bytes.Equal(arts["hooks."+harness], raw) {
			return true
		}
	}
	return false
}

// wiringBinPaths extracts the binary paths the on-disk wiring invokes, so a
// custom-layout box's own render is a candidate.
func wiringBinPaths(harness string, raw []byte) []string {
	var out []string
	marker := " " + hookMarker(harness)
	if harness == "codex" {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			value, ok := strings.CutPrefix(line, "command = ")
			if !ok {
				continue
			}
			cmd := codexMCPCommandPath(value)
			if head, ok := strings.CutSuffix(cmd, marker); ok {
				out = append(out, strings.TrimSpace(head))
			} else if codexShimCommand(cmd) {
				if target, ok := codexShimTarget(cmd); ok {
					out = append(out, target)
				}
			}
		}
		return out
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil
	}
	hooks, _ := settings["hooks"].(map[string]any)
	for _, ev := range allEventsIn(hooks) {
		arr, _ := hooks[ev].([]any)
		for _, item := range arr {
			m, _ := item.(map[string]any)
			inner, _ := m["hooks"].([]any)
			for _, h := range inner {
				hm, _ := h.(map[string]any)
				cmd, _ := hm["command"].(string)
				if head, ok := strings.CutSuffix(cmd, marker); ok {
					out = append(out, strings.Trim(strings.TrimSpace(head), `"`))
				}
			}
		}
	}
	return out
}
