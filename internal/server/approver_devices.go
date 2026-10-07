package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/store"
)

// --- push registration ---

// approverPushRequest mirrors ApproverPushRequest (openapi 0.40.0). p256dh
// and auth are the OPTIONAL Web Push subscription keys (unpadded base64url):
// required for kind=webpush, the encrypted-protocol opt-in for
// kind=unifiedpush, refused for fcm/apns; validation is the approval
// service's, strict, and fail-closed.
type approverPushRequest struct {
	Kind            string `json:"kind"`
	TokenOrEndpoint string `json:"token_or_endpoint"`
	P256DH          string `json:"p256dh"`
	Auth            string `json:"auth"`
}

func (in approverPushRequest) registration() approval.PushRegistration {
	return approval.PushRegistration{
		Kind: in.Kind, TokenOrEndpoint: in.TokenOrEndpoint,
		P256DH: in.P256DH, Auth: in.Auth,
	}
}

func (a *App) handleApproverPushPut(w http.ResponseWriter, r *http.Request, deviceID, _ string) {
	var req approverPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	if err := a.approval.RegisterPush(r.Context(), deviceID, req.registration()); err != nil {
		if errors.Is(err, approval.ErrBadPushKind) || errors.Is(err, approval.ErrPushEndpointNotAllowed) ||
			errors.Is(err, approval.ErrBadPushRegistration) {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "push registration failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) handleApproverPushDelete(w http.ResponseWriter, r *http.Request, deviceID, _ string) {
	var req approverPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	if err := a.approval.DeletePush(r.Context(), deviceID, req.registration()); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "push removal failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- self-unenroll ---

// handleApproverSelfUnenroll serves DELETE /v1/approver/enrollment
// (requireApprover): the calling device retires its OWN enrollment with the
// admin revoke's exact effect, the same row-backed DeleteDevice (the row dies,
// so every future use=approver verification answers 401 device_revoked, and
// the device's push registrations go inert behind the ListPushTargets device
// join, identical to an admin revoke). Deliberately bearer-only and body-less:
// a stolen bearer gains exactly one net-new capability, a visible fail-closed
// self-DoS, accepted by design (recovery is one re-enrollment). The audit
// event is the admin lane's action attributed via=self, so the record says
// who initiated. A lost delete
// race still answers 204: the row being gone IS the requested end state, and
// the next bearer use 401s regardless.
func (a *App) handleApproverSelfUnenroll(w http.ResponseWriter, r *http.Request, deviceID, userID string) {
	if err := a.store.Approvers().DeleteDevice(r.Context(), deviceID); err != nil && !errors.Is(err, store.ErrNotFound) {
		a.fail(w, r, http.StatusInternalServerError, "could not retire enrollment", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "approver-device-revoked", "device": deviceID, "user": userID,
		"via": "self",
	})
	w.WriteHeader(http.StatusNoContent)
}

// approverDevicePayload is one enrolled phone on the admin list: identity,
// owner, posture, liveness, and the push-registration count that attributes
// the channel-status "enrolled vs push routes" diagnostic to a specific
// device. Key material and attestation blobs stay server-side; the list is
// for picking a device, not re-verifying it.
type approverDevicePayload struct {
	ID               string     `json:"id"`
	UserID           string     `json:"user_id"`
	Username         string     `json:"username,omitempty"`
	Name             string     `json:"name"`
	Platform         string     `json:"platform"`
	KeySecurityLevel string     `json:"key_security_level"`
	Attestation      string     `json:"attestation"`
	EnrolledAt       time.Time  `json:"enrolled_at"`
	LastSeen         *time.Time `json:"last_seen,omitempty"`
	PushRoutes       int        `json:"push_routes"`
}

// handleApproversList serves GET /v1/admin/approvers (?user= filters by
// username or id): the discoverability half of the approver-device kill
// switch. The enroll response goes to the phone, not the admin, so this list
// is where an admin finds the apd_ id the DELETE below needs to revoke a
// lost phone.
// An unknown ?user simply lists empty (the bulk session revoke's user-face
// idiom), and owners come back as usernames the way the sessions list does.
func (a *App) handleApproversList(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user")
	if userID != "" {
		if u, err := a.store.Users().GetByUsername(r.Context(), userID); err == nil {
			userID = u.ID
		} else if !errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, http.StatusInternalServerError, "user lookup failed", err)
			return
		}
	}
	devs, err := a.store.Approvers().ListDevices(r.Context(), userID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list approver devices failed", err)
		return
	}
	ids := make([]string, 0, len(devs))
	seen := map[string]bool{}
	for _, d := range devs {
		if !seen[d.UserID] {
			seen[d.UserID] = true
			ids = append(ids, d.UserID)
		}
	}
	names := map[string]string{}
	if users, err := a.store.Users().GetByIDs(r.Context(), ids); err == nil {
		for _, u := range users {
			names[u.ID] = u.Username
		}
	}
	out := make([]approverDevicePayload, len(devs))
	for i, d := range devs {
		out[i] = approverDevicePayload{
			ID: d.ID, UserID: d.UserID, Username: names[d.UserID],
			Name: d.Name, Platform: d.Platform,
			KeySecurityLevel: d.KeySecurityLevel, Attestation: d.AttestationKind,
			EnrolledAt: d.CreatedAt, LastSeen: d.LastSeenAt, PushRoutes: d.PushRoutes,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleApproverDeviceDelete serves DELETE /v1/admin/approvers/{id}: it removes
// the device row, which immediately fails all future use=approver verification
// (row-backed revocation, no denylist plumbing).
func (a *App) handleApproverDeviceDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Read-before-delete only to attribute the audit event: the enroll event
	// names {user, device}, so the revocation names the same pair.
	owner := ""
	if dev, err := a.store.Approvers().GetDevice(r.Context(), id); err == nil {
		owner = dev.UserID
	}
	if err := a.store.Approvers().DeleteDevice(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such approver device")
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "could not revoke device", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "approver-device-revoked", "device": id, "user": owner,
		// via distinguishes the initiator on the shared action: this admin
		// kill-switch lane vs the device retiring itself (self-unenroll).
		"via": "admin",
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
