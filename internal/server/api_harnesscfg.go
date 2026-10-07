package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/harnesscfg"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/version"
)

// GET /v1/harness-config: the server-rendered managed harness config
// (spec/harness-config). At boot the server renders, signs, and
// self-verifies the artifact matrix once, registers the hooks.* hashes into
// the attestation expected-hash registry (additively: old rows stay allowed
// until an operator retires them, which IS the stale-version policy), and
// serves the documents from memory: no DB reads on this path.
// Unauthenticated in both profiles. The documents are rendered from the
// build alone and carry no policy and no secret (hook commands, pins, the
// credential-free proxy entry), every managed box writes them
// world-readable, and the privileged install fetches them before any session
// exists. A gate would also defeat the version-skew fallback: an install
// that cannot fetch renders locally, and an older render attests none
// against the hashes this server registered.

// harnessCfgHarnesses is the served harness roster (Tier-1).
var harnessCfgHarnesses = []string{"claude-code", "codex", "gemini"}

// harnessCfgArches enumerates the GOARCHes registry rows are written for.
// Rendered content varies by GOOS only, but registry platform matching is
// exact GOOS/GOARCH (spec/attestation), and a platform-blank row would put
// every OS's hash into every OS's allowed set: a file carrying another
// platform's content (dead paths, governance off) would still attest
// managed. Exact rows only.
var harnessCfgArches = []string{"amd64", "arm64"}

// harnessCfgDoc is one precomputed served document.
type harnessCfgDoc struct {
	body []byte
	id   string // sha256 hex of body: the ETag
}

// buildHarnessConfigs renders, signs, self-verifies, caches, and registers
// the full harness x GOOS matrix. Boot fails on any error: a strazad that
// cannot sign or persist what it publishes must say so, not serve a matrix
// that fleets would attest against (fail closed).
func (a *App) buildHarnessConfigs(ctx context.Context) error {
	kid, priv, err := a.snapKeys.Active(ctx)
	if err != nil {
		return fmt.Errorf("signing key: %w", err)
	}
	lookup := harnesscfg.KeyLookup(func(k string) (ed25519.PublicKey, bool) {
		return a.snapKeys.Public(ctx, k)
	})

	docs := make(map[string]harnessCfgDoc, len(harnessCfgHarnesses)*len(agentguard.SupportedGOOS))
	hooksIdx := make(map[string]map[string]bool, len(harnessCfgHarnesses))
	registered := 0
	for _, harness := range harnessCfgHarnesses {
		for _, goos := range agentguard.SupportedGOOS {
			arts, err := agentguard.RenderManagedArtifacts(harness, goos, "")
			if err != nil {
				return fmt.Errorf("render %s/%s: %w", harness, goos, err)
			}
			names := make([]string, 0, len(arts))
			for name := range arts {
				names = append(names, name)
			}
			sort.Strings(names)

			doc := harnesscfg.Document{Format: harnesscfg.Format, Kind: harnesscfg.Kind, Harness: harness, Platform: goos}
			for _, name := range names {
				art, err := harnesscfg.SignArtifact(kid, priv, harness, goos, name, arts[name])
				if err != nil {
					return fmt.Errorf("sign %s %s/%s: %w", name, harness, goos, err)
				}
				doc.Artifacts = append(doc.Artifacts, art)
			}
			// Never publish what we cannot re-verify (the snapshot service's
			// self-verify rule).
			if err := harnesscfg.Verify(doc, lookup); err != nil {
				return fmt.Errorf("self-verify %s/%s: %w", harness, goos, err)
			}
			body, err := json.Marshal(doc)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			docs[harness+"/"+goos] = harnessCfgDoc{body: body, id: hex.EncodeToString(sum[:])}
			for _, art := range doc.Artifacts {
				if strings.HasPrefix(art.Name, "hooks.") {
					if hooksIdx[harness] == nil {
						hooksIdx[harness] = map[string]bool{}
					}
					hooksIdx[harness][art.ContentHash] = true
				}
			}

			n, err := a.registerHarnessCfgHashes(ctx, doc)
			if err != nil {
				return fmt.Errorf("register %s/%s: %w", harness, goos, err)
			}
			registered += n
		}
	}
	a.harnessCfgs = docs
	a.harnessCfgHooks = hooksIdx
	if registered > 0 {
		a.log.Info("harness-config hashes registered", "rows", registered, "renderedBy", version.Version)
	}
	return nil
}

// registerHarnessCfgHashes writes one document's hooks.* hashes into the
// expected-hash registry, per exact GOOS/GOARCH. Conflicts are the steady
// state (every boot re-registers idempotently, multi-pod included). The mcp
// artifact is served but deliberately NOT registered: it is not a measured
// attestation artifact (verifyAttestation in attestation.go fails
// closed on any registered-but-unreported artifact, so registering it would
// drop every deployed managed fleet to att=none until clients measure it).
func (a *App) registerHarnessCfgHashes(ctx context.Context, doc harnesscfg.Document) (int, error) {
	created := 0
	for _, art := range doc.Artifacts {
		if !strings.HasPrefix(art.Name, "hooks.") {
			continue
		}
		for _, arch := range harnessCfgArches {
			platform := doc.Platform + "/" + arch
			_, err := a.store.AttestationHashes().Create(ctx, store.AttestationHash{
				Artifact: art.Name, Harness: doc.Harness, Platform: platform,
				Hash: art.ContentHash, Note: "harness-config render (strazad " + version.Version + ")",
			})
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			if err != nil {
				return created, err
			}
			created++
			a.emitEventCtx(ctx, "straza.identity.updated", map[string]any{
				"action": "attestation-hash.registered", "artifact": art.Name,
				"harness": doc.Harness, "platform": platform, "via": "harness-config render",
			})
		}
	}
	return created, nil
}

// harnessWiringStatus classifies the managed-wiring hash a session reported
// at start, so a stale managed copy is visible. current = matches the
// artifact this
// server publishes now; allowed = matches a registered row that is not the
// current render (an upgrade-window survivor or a per-box custom-layout
// registration: converge or retire); mismatch = no row allows it (the
// attestation level already gated the session; this surfaces WHICH sessions
// carry it); unmeasured = no managed measurement (user-mode/advisory).
// Admin harnesses report nothing meaningful and stay blank.
func (a *App) harnessWiringStatus(harness, hashesJSON string, registry []store.AttestationHash) (status, hash string) {
	if adminHarnesses[harness] {
		return "", ""
	}
	var hashes map[string]string
	if json.Unmarshal([]byte(hashesJSON), &hashes) != nil || hashes["hooks."+harness] == "" {
		return "unmeasured", ""
	}
	h := hashes["hooks."+harness]
	if a.harnessCfgHooks[harness][h] {
		return "current", h
	}
	for _, row := range registry {
		if row.Artifact == "hooks."+harness && (row.Harness == "" || row.Harness == harness) && row.Hash == h {
			return "allowed", h
		}
	}
	return "mismatch", h
}

// handleHarnessConfig serves one precomputed document with the body hash as
// ETag, mirroring handleSnapshot's cheap-poll contract.
func (a *App) handleHarnessConfig(w http.ResponseWriter, r *http.Request) {
	harness := r.URL.Query().Get("harness")
	platform := r.URL.Query().Get("platform")
	doc, ok := a.harnessCfgs[harness+"/"+platform]
	if !ok {
		apiError(w, http.StatusNotFound, fmt.Sprintf(
			"no harness config for harness %q platform %q (harnesses: %s; platforms: %s)",
			harness, platform, strings.Join(harnessCfgHarnesses, ", "), strings.Join(agentguard.SupportedGOOS, ", ")))
		return
	}
	etag := `"` + doc.id + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Straza-Harness-Config-Id", doc.id)
	_, _ = w.Write(doc.body)
}
