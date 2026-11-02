package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

type deviceInfo struct {
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	Fingerprint string `json:"fingerprint"`
}

type enrollRequest struct {
	IDToken string     `json:"id_token"`
	Device  deviceInfo `json:"device"`
	// ClientKind names the client that enrols and will hold the credential
	// (openapi 0.114.0): store.DeviceClientKit or store.DeviceClientHuman.
	// Missing means the kit, whose enrollment needs a DPoP key proof, so a
	// kit build that predates the field gets the old-client refusal. Human
	// was strazactl's, whose enrollment is refused because strazactl no
	// longer signs in.
	ClientKind string `json:"client_kind"`
	// Custody, MachineBinding and Counter come with a key enrollment, the
	// one whose request carries a DPoP header (identity_key.go).
	Custody        custodyInfo `json:"custody"`
	MachineBinding string      `json:"machine_binding"`
	Counter        int64       `json:"counter"`
}

// handleEnroll binds a person's machine key to a device, the step before the
// first key check-in. A bearer enrollment, which brings no key, is refused
// once its ID token verified, so the refusal record names the user.
func (a *App) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req enrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	if req.IDToken == "" {
		apiError(w, http.StatusBadRequest, "id_token is required")
		return
	}
	kind, ok := deviceClientKind(req.ClientKind)
	if !ok {
		apiError(w, http.StatusBadRequest, "client_kind must be kit or human")
		return
	}
	// A DPoP header makes this a key enrollment, whose proof is checked
	// before the ID token.
	keyed := r.Header.Get(dpop.HeaderProof) != ""
	var proof authn.Proof
	if keyed {
		if proof, ok = a.enrollProof(w, r, req, kind); !ok {
			return
		}
	}
	u, facts, err := a.verifyLoginFacts(r, req.IDToken)
	var refused *loginRefusal
	if errors.As(err, &refused) {
		a.refuseLogin(w, r, refused, "")
		return
	}
	if err != nil {
		// An outage (store or issuer unreachable) is not the person's
		// credential failing: 503 + generic body, cause in the log.
		if a.answerOutage(w, r, "enroll", err) {
			return
		}
		// The client-facing text stays opaque (no oracle); the reason is an
		// operator concern. Without this line a rejected login is
		// undiagnosable from the server side.
		a.log.Warn("enroll: login not accepted", "err", err)
		apiError(w, http.StatusUnauthorized, "login not accepted: run `straza enroll` again")
		return
	}

	if keyed {
		a.enrollKeyDevice(w, r, req, u, proof, facts)
		return
	}
	// The Straza client enrolls with a key the Straza background service
	// holds, and strazactl runs on an admin API token, so every bearer
	// enrollment is refused and no device row is written.
	if kind == store.DeviceClientHuman {
		a.refuseStrazactl(w, r, authnFields{Via: "id-token", User: u.Username, UserID: u.ID})
		return
	}
	a.emitAuthnLogin(r, "failure", authnFields{Via: "id-token", User: u.Username, UserID: u.ID,
		Reason: bearerRetiredReason})
	apiError(w, http.StatusForbidden, oldClientMsg)
}
