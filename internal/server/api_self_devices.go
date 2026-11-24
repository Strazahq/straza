package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// selfDevicePayload is one of the caller's own approver devices on
// GET /v1/self/devices. It carries no key material, no token and no push
// endpoint: notifies says only whether a push route is registered.
type selfDevicePayload struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Platform         string     `json:"platform"`
	KeySecurityLevel string     `json:"key_security_level"`
	Attestation      string     `json:"attestation,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	LastSeen         *time.Time `json:"last_seen,omitempty"`
	Notifies         bool       `json:"notifies"`
}

// selfDeviceMissing is the 404 of a removal whose id names no device of the
// caller. Another person's device reads the same, so the answer never tells
// whether the id exists.
const selfDeviceMissing = "No approver phone or browser of yours has this id, so nothing was removed. " +
	"It may have been removed already. Reload the list of your devices and choose one from it."

// selfApprovalRoutes are the routes of a person's own approver devices and
// waiting requests, which routeTable appends. Each takes
// requirePersonClient.
func (a *App) selfApprovalRoutes() []route {
	return []route{
		{"GET", "/v1/self/devices", a.requirePersonClient(a.handleSelfDevices)},
		{"DELETE", "/v1/self/devices/{id}", a.requirePersonClient(a.handleSelfDeviceDelete)},
		{"GET", "/v1/self/requests", a.requirePersonClient(a.handleSelfRequests)},
	}
}

// handleSelfDevices serves GET /v1/self/devices (requirePersonClient): the
// caller's own approver devices, newest first. A removed device has no row,
// so it is not listed.
func (a *App) handleSelfDevices(w http.ResponseWriter, r *http.Request, userID string) {
	devs, err := a.store.Approvers().ListDevices(r.Context(), userID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError,
			"Straza could not read your approver devices, so it cannot list them. Try again, and tell your administrator if it keeps failing.", err)
		return
	}
	out := make([]selfDevicePayload, 0, len(devs))
	// ListDevices answers enrollment order, and an empty id would read every
	// user's rows, so the walk runs backwards and keeps the caller's alone.
	for i := len(devs) - 1; i >= 0; i-- {
		d := devs[i]
		if d.UserID != userID {
			continue
		}
		out = append(out, selfDevicePayload{
			ID: d.ID, Name: d.Name, Platform: d.Platform,
			KeySecurityLevel: d.KeySecurityLevel, Attestation: d.AttestationKind,
			CreatedAt: d.CreatedAt, LastSeen: d.LastSeenAt, Notifies: d.PushRoutes > 0,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// handleSelfDeviceDelete serves DELETE /v1/self/devices/{id}
// (requirePersonClient): the caller removes one of their own approver
// devices with the admin removal's effect, so the device's token is refused
// from its next request on and its push routes go inert. It writes the
// record a device writes when it retires itself, where user, with via self,
// names the actor, since a person removes only their own device. An id that
// names no device of the caller, another person's device included, answers
// selfDeviceMissing and writes nothing, as do a second removal and a
// removal whose row another lane deleted first.
func (a *App) handleSelfDeviceDelete(w http.ResponseWriter, r *http.Request, userID string) {
	const failed = "Straza could not remove the device, so it can still approve requests. Try again. If it keeps failing, ask your Straza administrator to remove it in the console."
	id := r.PathValue("id")
	dev, err := a.store.Approvers().GetDevice(r.Context(), id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.fail(w, r, http.StatusInternalServerError, failed, err)
		return
	}
	if err != nil || dev.UserID != userID {
		apiError(w, http.StatusNotFound, selfDeviceMissing)
		return
	}
	if err := a.store.Approvers().DeleteDevice(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, selfDeviceMissing)
			return
		}
		a.fail(w, r, http.StatusInternalServerError, failed, err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "approver-device-revoked", "device": id, "user": userID, "via": "self",
	})
	w.WriteHeader(http.StatusNoContent)
}
