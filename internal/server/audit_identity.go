package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/store"
)

// Identity audit actions: one straza.audit.admin record per identity setup
// write that succeeds (spec/events revision 40). Each name lives here once.
const (
	actionUserCreate        = "user.create"
	actionUserUpdate        = "user.update"
	actionEnrollTokenCreate = "enroll-token.create" // #nosec G101 -- an audit action name, not a credential
	actionApproverEnroll    = "approver.enroll"
	actionNHIKeySet         = "nhi-key.set"
	actionNHIKeyRemoved     = "nhi-key.removed"
)

// actorViaEnrollToken is the actorVia of the approver.enroll record: the
// enrol route has no admin principal, and the person's one-time enroll
// token is the only credential the call presents.
const actorViaEnrollToken = "enroll-token"

// emitIdentityAudit writes one identity setup record to the outbox with the
// actor ctx holds. It writes on context.WithoutCancel, as the drafts
// records do, because the store write it records has already happened, so
// a client that disconnects now must not lose the record.
func (a *App) emitIdentityAudit(ctx context.Context, data map[string]any) {
	a.emitEventCtx(context.WithoutCancel(ctx), "straza.audit.admin", data)
}

// auditUserCreate chains the user.create record of a user the lane origin
// created, admin or scim: target and user the new id, username, kind human
// or nhi, userType when the row has one, and origin.
func (a *App) auditUserCreate(ctx context.Context, u store.User, origin string) {
	data := map[string]any{
		"action": actionUserCreate, "target": u.ID, "user": u.ID, "username": u.Username,
		"kind": userKind(u), "origin": origin,
	}
	if u.UserType != "" {
		data["userType"] = u.UserType
	}
	a.emitIdentityAudit(ctx, data)
}

// userChanges answers the wire names of the fields a PATCH changed, in the
// order the request declares them, and never a value. passwordSet reports
// a password in the request, which always replaces the stored hash.
func userChanges(before, after store.User, passwordSet bool) []string {
	var changed []string
	for _, f := range []struct {
		path    string
		differs bool
	}{
		{"email", before.Email != after.Email},
		{"display", before.Display != after.Display},
		{"title", before.Title != after.Title},
		{"status", before.Status != after.Status},
		{"password", passwordSet},
		{"user_type", before.UserType != after.UserType},
		{"agency_mode", before.AgencyMode != after.AgencyMode},
		{"sponsor", before.Sponsor != after.Sponsor},
		{"swarm_id", before.SwarmID != after.SwarmID},
		{"ephemeral", before.Ephemeral != after.Ephemeral},
	} {
		if f.differs {
			changed = append(changed, f.path)
		}
	}
	return changed
}

// auditUserUpdate chains the user.update record of an admin PATCH or a SCIM
// write that changed at least one field of u, with the names in changed,
// and nothing for a write that changed none.
func (a *App) auditUserUpdate(ctx context.Context, u store.User, changed []string) {
	if len(changed) == 0 {
		return
	}
	a.emitIdentityAudit(ctx, map[string]any{
		"action": actionUserUpdate, "target": u.ID, "user": u.ID, "username": u.Username, "changed": changed,
	})
}

// auditEnrollTokenCreate chains the enroll-token.create record of a one-time
// enroll token minted for u, never the token itself. Only the self route
// scopes a token to a channel, so a channel marks the record self as well.
func (a *App) auditEnrollTokenCreate(ctx context.Context, u store.User, channel string) {
	data := map[string]any{"action": actionEnrollTokenCreate, "target": u.ID, "user": u.ID, "username": u.Username}
	if channel != "" {
		data["channel"] = channel
		data["self"] = true
	}
	a.emitIdentityAudit(ctx, data)
}

// auditApproverEnroll chains the approver.enroll record of a device the
// enrol route registered. The actor is the person whose enroll token was
// consumed, with actorVia enroll-token. The record names the device, its
// platform and its name, never the device key or the enroll token.
func (a *App) auditApproverEnroll(ctx context.Context, res approval.EnrollResult, platform, name string) {
	ctx = withActor(ctx, auditActor{Name: res.Username, ID: res.UserID, Via: actorViaEnrollToken})
	a.emitIdentityAudit(ctx, map[string]any{
		"action": actionApproverEnroll, "target": res.DeviceID, "user": res.UserID, "username": res.Username,
		"device": res.DeviceID, "platform": platform, "name": name,
	})
}

// auditNHIKeySet chains the nhi-key.set record of an assertion key
// registered for the agent u: the fingerprint of publicKeyB64, never the key.
func (a *App) auditNHIKeySet(ctx context.Context, u store.User, publicKeyB64 string) {
	a.emitIdentityAudit(ctx, map[string]any{
		"action": actionNHIKeySet, "target": u.ID, "user": u.ID, "username": u.Username,
		"fingerprint": nhiKeyFingerprint(publicKeyB64),
	})
}

// auditNHIKeyRemoved chains the nhi-key.removed record of a key removed by
// hand, the shape the user delete's deprovisionNHIKey writes without a reason.
func (a *App) auditNHIKeyRemoved(ctx context.Context, userID string) {
	a.emitIdentityAudit(ctx, map[string]any{"action": actionNHIKeyRemoved, "target": userID, "user": userID})
}

// nhiKeyFingerprint renders an agent's assertion key as the key read shows
// it, "sha256:" and the hex digest of the raw key bytes, or "" when the
// base64 does not decode.
func nhiKeyFingerprint(publicKeyB64 string) string {
	raw, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
