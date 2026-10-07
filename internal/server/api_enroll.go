package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
	// Missing means the kit, so kit builds that predate the field keep
	// enrolling.
	ClientKind string `json:"client_kind"`
}

type enrollResponse struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	DeviceID string `json:"device_id"`
	// DeviceToken is the long-lived enroll credential: purpose-scoped,
	// bound to this user+device, usable only at /v1/checkin, revocable via
	// user disable or device revoke. It outlives the 10-minute login token so
	// enrolling is once per device.
	DeviceToken          string `json:"device_token"`
	DeviceTokenExpiresIn int    `json:"device_token_expires_in"`
}

// handleEnroll binds an authenticated identity to a device, the step before
// the first check-in. Re-enrolling the same fingerprint is idempotent.
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
	u, err := a.verifyLogin(r, req.IDToken)
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

	if kind == store.DeviceClientHuman && !personUser(u) {
		a.emitAuthnLogin(r, "failure", authnFields{Via: "id-token", User: u.Username, UserID: u.ID,
			Reason: "only a person can enrol a credential for the human clients"})
		apiError(w, http.StatusForbidden, fmt.Sprintf(
			"the user %s is %s, and only a person can enrol a credential for strazactl or the console",
			u.Username, userTypeWord(u)))
		return
	}

	var device store.Device
	if req.Device.Fingerprint != "" {
		// A row of the other client kind is never reused; a row from before
		// the kind was recorded is bound to this enrol's kind below.
		for _, d := range mustDevices(a, r, u.ID) {
			if d.Fingerprint == req.Device.Fingerprint && (d.ClientKind == "" || d.ClientKind == kind) {
				device = d
				break
			}
		}
	}
	if device.ID != "" && device.ClientKind == "" {
		device.ClientKind = kind
		if device, err = a.store.Devices().Update(r.Context(), device); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "could not register device", err)
			return
		}
	}
	if device.ID == "" {
		device, err = a.store.Devices().Create(r.Context(), store.Device{
			UserID: u.ID, Name: req.Device.Name,
			Platform: req.Device.Platform, Fingerprint: req.Device.Fingerprint, ClientKind: kind,
		})
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "could not register device", err)
			return
		}
	}
	ttl := a.deviceTokenTTL()
	deviceToken, err := a.tokens.MintDeviceToken(u.ID, device.ID, ttl)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not mint device credential", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "enroll", "user": u.ID, "device": device.ID,
	})
	writeJSON(w, http.StatusOK, enrollResponse{
		UserID: u.ID, Username: u.Username, DeviceID: device.ID,
		DeviceToken: deviceToken, DeviceTokenExpiresIn: int(ttl.Seconds()),
	})
}

func mustDevices(a *App, r *http.Request, userID string) []store.Device {
	devs, err := a.store.Devices().ListByUser(r.Context(), userID)
	if err != nil {
		return nil
	}
	return devs
}
