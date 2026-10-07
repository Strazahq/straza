package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// verifyAttestation maps a check-in payload to an attestation level. No
// measurements → none. Unmanaged installs are advisory at best:
// their hashes are recorded but never verified. A managed claim is verified
// against the expected-hash registry: every artifact registered for the
// session's harness+platform must be reported with a hash from that
// artifact's allowed set. A mismatch or a missing measurement is tamper
// evidence and yields none; an empty relevant registry can
// verify nothing and caps at advisory (spec/attestation).
func verifyAttestation(p attestationPayload, harness string, expected []store.AttestationHash) string {
	if len(p.Hashes) == 0 {
		return store.AttestationNone
	}
	if !p.Managed {
		return store.AttestationAdvisory
	}
	allowed := map[string]map[string]bool{}
	for _, h := range expected {
		if h.Harness != "" && h.Harness != harness {
			continue
		}
		if h.Platform != "" && h.Platform != p.Platform {
			continue
		}
		if allowed[h.Artifact] == nil {
			allowed[h.Artifact] = map[string]bool{}
		}
		allowed[h.Artifact][h.Hash] = true
	}
	if len(allowed) == 0 {
		return store.AttestationAdvisory
	}
	for artifact, set := range allowed {
		if !set[p.Hashes[artifact]] {
			return store.AttestationNone
		}
	}
	return store.AttestationManaged
}

// adminHarnesses are the harness names the human web/CLI clients check in
// with (strazactl CLI, embedded web console, the self-service page). Listing
// here has exactly two effects: exemption from the checkin minAttestation
// gate (bootstrap: admins must be able to log in before any hash is
// registered; the gateway still refuses these tokens on the data plane
// under the same minimum) and a blank wiring status in the sessions list.
// "self-service" is the self-service page, which checks in under its own
// name so the sessions list tells it from a console sign-in.
var adminHarnesses = map[string]bool{"strazactl": true, "console": true, "self-service": true}

// --- /v1/admin/attestation-hashes: the expected-hash registry ---

// hashRe mirrors the checkin-request schema's hash pattern.
var hashRe = regexp.MustCompile(`^sha256:[0-9a-f]{6,64}$`)

type attestationHashPayload struct {
	ID        string    `json:"id,omitempty"`
	Artifact  string    `json:"artifact"`
	Harness   string    `json:"harness,omitempty"`
	Platform  string    `json:"platform,omitempty"`
	Hash      string    `json:"hash"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	// Current says the row's hash is the wiring this server renders right
	// now for that artifact and harness. False covers every other row,
	// including an upgrade-window survivor and a harness this server does
	// not render at all, so an operator can tell what is retirable.
	Current bool `json:"current"`
}

// toAttestationHashPayload renders one registry row. Current reads the same
// boot-rendered index harnessWiringStatus classifies sessions against, so a
// row and a session never disagree about what is current.
func (a *App) toAttestationHashPayload(h store.AttestationHash) attestationHashPayload {
	return attestationHashPayload{ID: h.ID, Artifact: h.Artifact, Harness: h.Harness,
		Platform: h.Platform, Hash: h.Hash, Note: h.Note, CreatedAt: h.CreatedAt,
		Current: h.Artifact == "hooks."+h.Harness && a.harnessCfgHooks[h.Harness][h.Hash]}
}

func (a *App) handleAttestationHashesList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.AttestationHashes().List(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list attestation hashes failed", err)
		return
	}
	out := make([]attestationHashPayload, len(rows))
	for i, h := range rows {
		out[i] = a.toAttestationHashPayload(h)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleAttestationHashCreate(w http.ResponseWriter, r *http.Request) {
	var req attestationHashPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Artifact == "" {
		apiError(w, http.StatusBadRequest, "artifact is required")
		return
	}
	if !hashRe.MatchString(req.Hash) {
		apiError(w, http.StatusBadRequest, "hash must match sha256:<hex> (spec/attestation)")
		return
	}
	created, err := a.store.AttestationHashes().Create(r.Context(), store.AttestationHash{
		Artifact: req.Artifact, Harness: req.Harness, Platform: req.Platform,
		Hash: req.Hash, Note: req.Note,
	})
	if errors.Is(err, store.ErrConflict) {
		apiError(w, http.StatusConflict, "this measurement is already registered")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "create failed", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "attestation-hash.registered", "artifact": created.Artifact,
		"harness": created.Harness, "platform": created.Platform,
	})
	writeJSON(w, http.StatusCreated, a.toAttestationHashPayload(created))
}

func (a *App) handleAttestationHashDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.store.AttestationHashes().Delete(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such attestation hash")
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "delete failed", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "attestation-hash.removed", "id": id,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
