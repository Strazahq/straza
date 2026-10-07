package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// devicePayload is the wire view of an enrolled device: snake_case like every
// payload sibling, never the raw untagged store struct.
type devicePayload struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	Platform    string    `json:"platform"`
	Status      string    `json:"status"`
	ClientKind  string    `json:"client_kind"`
	EnrolledAt  time.Time `json:"enrolled_at"`
}

func (a *App) handleDevicesList(w http.ResponseWriter, r *http.Request) {
	if _, err := a.store.Users().GetByID(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such user")
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "get failed", err)
		return
	}
	devs, err := a.store.Devices().ListByUser(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list devices failed", err)
		return
	}
	out := make([]devicePayload, len(devs))
	for i, d := range devs {
		out[i] = devicePayload{ID: d.ID, UserID: d.UserID, Name: d.Name,
			Fingerprint: d.Fingerprint, Platform: d.Platform, Status: d.Status,
			ClientKind: d.ClientKind, EnrolledAt: d.EnrolledAt}
	}
	if p, ok := parsePageParams(w, r); p.paged || !ok {
		if !ok {
			return
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
		page, next, valid := pageAfter(out, func(d devicePayload) string { return d.ID }, p.before, p.limit)
		if !valid {
			apiError(w, http.StatusBadRequest, "invalid or stale cursor: re-fetch from the start")
			return
		}
		writePage(w, page, next)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeviceRevoke serves DELETE /v1/admin/users/{id}/devices/{deviceId}:
// the enroll-credential kill switch. Revocation is row-backed, like the
// approver-device sibling (approver_devices.go): the row is deleted, so the
// long-lived device token dies at checkin's device check for both its lanes,
// starting a session and refreshing one whose row carries this device binding
// (the schema pins status to active|disabled, so deletion is also the only
// migration-free terminal state, and it is the stronger one: re-enrolling
// needs a fresh interactive login and mints a NEW row, never resurrects
// this one). The kill-switch cascade covers what a dead row cannot: the denylist
// entry bites during the in-flight window on this pod, the revocation row
// rebuilds it at boot, the push stands down a live kit on the device, and the
// straza.revocation.device event (spec/events §subjects) converges peer pods.
func (a *App) handleDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	userID, deviceID := r.PathValue("id"), r.PathValue("deviceId")
	d, err := a.store.Devices().GetByID(r.Context(), deviceID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.fail(w, r, http.StatusInternalServerError, "device lookup failed", err)
		return
	}
	// An unknown id and a real device under the wrong user answer identically:
	// the nesting is a belongs-to check, never an existence oracle.
	if err != nil || d.UserID != userID {
		apiError(w, http.StatusNotFound, "no such device for this user")
		return
	}
	if err := a.store.Devices().Delete(r.Context(), deviceID); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not revoke device", err)
		return
	}
	_, _ = a.store.Revocations().Create(r.Context(), store.Revocation{
		Kind: store.RevokeDevice, TargetID: deviceID, Reason: "device revoked by admin",
	})
	a.denylist.RevokeDevice(deviceID)
	a.pushRevocation("device", deviceID)
	// Payload carries ONLY the device id: the spine consumer keys the denylist
	// scope off which field is set, so a user id riding along would lock the
	// whole user (spine/revocation.go apply).
	a.emitEvent(r, "straza.revocation.device", map[string]any{"device": deviceID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

type sessionPayload struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	Username string `json:"username,omitempty"`
	Harness  string `json:"harness"`
	// ClientVersion is the straza build the client named at check-in
	// (0.95.0); blank for a client older than the field.
	ClientVersion string    `json:"client_version,omitempty"`
	Attestation   string    `json:"attestation"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	LastSeen      time.Time `json:"last_seen"`
	// WiringStatus/WiringHash: the session's managed-wiring hash classified
	// against the published render + registry (0.55.0):
	// current | allowed | mismatch | unmeasured; blank for admin harnesses
	// and when the registry read failed (reporting garnish never 500s the
	// fleet screen).
	WiringStatus string `json:"wiring_status,omitempty"`
	WiringHash   string `json:"wiring_hash,omitempty"`
}

// handleSessionsList serves GET /v1/admin/sessions?status=&user= (both
// optional, both validated loud): a typo answering an empty 200 reads as "no
// sessions", which on this screen means "no fleet". Rows come newest-first.
func (a *App) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	switch status {
	case "", store.SessionActive, store.SessionRevoked, store.SessionClosed:
	default:
		apiError(w, http.StatusBadRequest, "status must be active, revoked or closed")
		return
	}
	p, ok := parseSortedPage(w, r, sessionSortKeys)
	if !ok {
		return
	}
	var sessions []store.Session
	var err error
	var after *store.Session
	user := r.URL.Query().Get("user")
	switch {
	case p.paged:
		sessions, err = a.store.Sessions().Page(r.Context(), status, user, p.sort, store.Cursor{Value: p.value, ID: p.before}, p.limit+1)
		if errors.Is(err, store.ErrBadCursor) {
			apiError(w, http.StatusBadRequest, "invalid or stale cursor: re-fetch from the start")
			return
		}
		if err == nil && len(sessions) > p.limit {
			sessions = sessions[:p.limit]
			after = &sessions[p.limit-1]
		}
	case user != "":
		sessions, err = a.store.Sessions().ListByUser(r.Context(), user)
		if err == nil && status != "" {
			kept := sessions[:0]
			for _, s := range sessions {
				if s.Status == status {
					kept = append(kept, s)
				}
			}
			sessions = kept
		}
	default:
		sessions, err = a.store.Sessions().List(r.Context(), status)
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list sessions failed", err)
		return
	}
	// Resolve owners in one batch; operators think in usernames, not UUIDs.
	ids := make([]string, 0, len(sessions))
	seen := map[string]bool{}
	for _, s := range sessions {
		if !seen[s.UserID] {
			seen[s.UserID] = true
			ids = append(ids, s.UserID)
		}
	}
	names := map[string]string{}
	if users, err := a.store.Users().GetByIDs(r.Context(), ids); err == nil {
		for _, u := range users {
			names[u.ID] = u.Username
		}
	}
	// One registry read serves every row's wiring classification; a failed
	// read omits the statuses rather than failing the fleet screen.
	registry, regErr := a.store.AttestationHashes().List(r.Context())
	if regErr != nil {
		a.log.Warn("sessions list: attestation registry unavailable; wiring status omitted", "err", regErr)
	}
	out := make([]sessionPayload, len(sessions))
	for i, s := range sessions {
		out[i] = sessionPayload{ID: s.ID, UserID: s.UserID, Username: names[s.UserID],
			Harness: joinHarness(s.HarnessName, s.HarnessVersion), ClientVersion: s.ClientVersion, Attestation: s.AttestationLevel,
			Status: s.Status, StartedAt: s.StartedAt, LastSeen: s.LastSeen}
		if regErr == nil {
			out[i].WiringStatus, out[i].WiringHash = a.harnessWiringStatus(s.HarnessName, s.AttestationHashes, registry)
		}
	}
	if p.paged {
		cursor := ""
		if after != nil {
			cursor = pageCursor(p, sessionSortValue(p.sort.Key, *after, names), after.ID)
		}
		writePageCursor(w, out, cursor)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// closeIdleSessions is one janitor pass: an active session not seen for two
// token TTLs holds an expired token that can never refresh (checkin refuses
// it), so it is closed; `sessions list` and the console then show live
// principals, not history. Runs on a ticker from Run.
func (a *App) closeIdleSessions(ctx context.Context) {
	cutoff := time.Now().Add(-2 * authn.DefaultTokenTTL)
	closed, err := a.store.Sessions().CloseIdle(ctx, cutoff)
	if err != nil {
		a.log.Warn("session janitor pass failed", "err", err)
		return
	}
	if len(closed) == 0 {
		return
	}
	a.log.Info("closed idle sessions", "count", len(closed))
	for _, c := range closed {
		// No request produced this: nil r keeps sourceIp/userAgent absent,
		// which the spec defines as "no client connection" (rev 21).
		a.emitAuthnSessionEnd(ctx, nil, "idle-closed", authnFields{
			UserID: c.UserID, Session: c.ID,
			Reason: "idle past two token TTLs; the expired token can never refresh",
		})
	}
}

// closeLifetimeSessions is the janitor's other pass: an active session older
// than governance.sessionMaxLifetime is closed even while it is still being
// refreshed, so a copied session token stops working at a known point. The
// daemon reads the next refresh's 401 as a stand-down and starts a fresh
// session from its device credential; no person acts. Runs on the same
// ticker as closeIdleSessions.
func (a *App) closeLifetimeSessions(ctx context.Context) {
	lifetime := a.cfg.EffectiveSessionMaxLifetime()
	closed, err := a.store.Sessions().CloseStartedBefore(ctx, time.Now().Add(-lifetime))
	if err != nil {
		a.log.Warn("session lifetime pass failed", "err", err)
		return
	}
	if len(closed) == 0 {
		return
	}
	a.log.Info("closed sessions past the maximum lifetime", "count", len(closed), "maxLifetime", lifetime.String())
	reason := fmt.Sprintf("session older than the maximum lifetime of %s; the client starts a new session from its device credential", lifetime)
	for _, c := range closed {
		// No request produced this: nil r keeps sourceIp/userAgent absent.
		a.emitAuthnSessionEnd(ctx, nil, "lifetime-closed", authnFields{
			UserID: c.UserID, Session: c.ID, Reason: reason,
		})
	}
}

func (a *App) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	changed, err := a.store.Sessions().SetStatusIfChanged(r.Context(), id, store.SessionRevoked)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such session")
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "revoke failed", err)
		return
	}
	// The kill-switch trio fires only on the actual row transition: a
	// re-revoke keeps its long-standing 200 but must not re-amplify the
	// outbox/DB (see handleSessionSelfRevoke, where the caller is unprivileged
	// and the same replay costs nothing but a valid token).
	if changed {
		_, _ = a.store.Revocations().Create(r.Context(), store.Revocation{
			Kind: store.RevokeSession, TargetID: id, Reason: "revoked by admin",
		})
		a.denylist.revokeSession(id)
		a.subjects.drop(id)
		a.pushRevocation("session", id)
		a.emitEvent(r, "straza.revocation.session", map[string]any{"session": id})
		// Rev 21 chained end record, transition-gated like the trio above.
		// The row outlives the revoke, so the owner read only garnishes; a
		// failed read emits the end without a userId (never fabricated).
		f := authnFields{Session: id, Reason: "revoked by admin"}
		if ses, err := a.store.Sessions().GetByID(r.Context(), id); err == nil {
			f.UserID = ses.UserID
		}
		a.emitAuthnSessionEnd(r.Context(), r, "revoked-admin", f)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// The 401 answers of a self revoke to a token it cannot use. The expired
// session token's answer is formatted with the session id twice, because a
// refresh keeps the id, so a newer token of the same session may be live.
const (
	expiredSessionTokenMsg = "token rejected: this session token has expired, so it cannot end a session. " +
		"Session %s ends by itself once no client refreshes it or at its lifetime limit. " +
		"The session token that is live now ends it at once, and so does an administrator with strazactl sessions revoke %s. " +
		"Run strazactl login to start a new session."
	expiredIDTokenMsg = "token rejected: this ID token has expired, and an ID token names no session, so there is nothing for it to end. " +
		"Only a session token ends its own session. A person runs strazactl login to sign in again, " +
		"and an AI agent requests a new token with a fresh client assertion."
	expiredDeviceCredentialMsg = "token rejected: this device credential has expired, and a device credential names no session, so there is nothing for it to end. " +
		"Only a session token ends its own session. Run strazactl login, or straza enroll on a machine with the straza client, for a new device credential."
	notSessionTokenMsg = "token rejected: only a session token can end its own session"
)

// expiredTokenRefusal picks the self revoke's answer to an expired token by
// the kind its claims name. jwt.Parse verifies the signature before it
// validates the expiry, so a token that failed only on its expiry was
// signed by this server, and its claims choose the sentence and nothing
// else. A kind with no sentence of its own names no session and gets that
// answer.
func expiredTokenRefusal(raw string) string {
	tok, err := jwt.ParseInsecure([]byte(raw))
	if err != nil {
		return notSessionTokenMsg
	}
	var ses, use string
	_ = tok.Get("ses", &ses)
	_ = tok.Get("use", &use)
	_, audience := tok.Audience()
	switch {
	case ses != "":
		return fmt.Sprintf(expiredSessionTokenMsg, ses, ses)
	case use == "device":
		return expiredDeviceCredentialMsg
	case use == "" && audience:
		return expiredIDTokenMsg
	}
	return notSessionTokenMsg
}

// handleSessionSelfRevoke serves POST /v1/session/revoke: the authenticated
// caller ends the session its own token names. No id parameter and no role
// gate on purpose: the token both authenticates the request and selects the
// only session the handler can touch (confused-deputy-proof by construction),
// so `strazactl logout` revokes server-side for every caller, not only
// holders of straza-admin. Per-target semantics match the admin revoke
// exactly: stand-down, not lockout.
//
// Two deliberate response choices. The revoked id is echoed because the
// caller names no id: a client whose token was re-established mid-logout must
// be able to see WHICH session just died. And the route never answers 404
// (a token that verifies but whose session row is gone or already dead gets an
// idempotent 200), so a 404 from this path means exactly one thing to the
// CLI: an older strazad without the route (fall back to the admin revoke).
func (a *App) handleSessionSelfRevoke(w http.ResponseWriter, r *http.Request) {
	raw := bearerToken(r)
	if raw == "" {
		apiError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	claims, err := a.tokens.Verify(raw)
	if errors.Is(err, jwt.TokenExpiredError()) {
		apiError(w, http.StatusUnauthorized, expiredTokenRefusal(raw))
		return
	}
	if err != nil || claims.Session == "" {
		// ID tokens and admin API tokens name no session; there is nothing a
		// self-scoped revoke could end for them.
		apiError(w, http.StatusUnauthorized, notSessionTokenMsg)
		return
	}
	id := claims.Session
	changed, err := a.store.Sessions().SetStatusIfChanged(r.Context(), id, store.SessionRevoked)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.fail(w, r, http.StatusInternalServerError, "revoke failed", err)
		return
	}
	// The kill-switch trio (revocation row, control event, per-target push)
	// fires ONLY on the actual row transition. tokens.Verify never consults
	// the denylist, so the just-revoked session's token stays valid for its
	// whole TTL and can replay this unprivileged route freely; ungated, one
	// logout would be an authenticated write-amplification primitive against
	// the outbox. A replay (or a token whose row is gone, ErrNotFound above)
	// is the idempotent 200 the CLI contract promises: the target state
	// already holds, nothing re-fires. The row (not the denylist) is the
	// signal, because a restarted or peer pod's denylist is rebuilt FROM the
	// rows and would answer wrongly here.
	if changed {
		_, _ = a.store.Revocations().Create(r.Context(), store.Revocation{
			Kind: store.RevokeSession, TargetID: id, Reason: "revoked by session owner (logout)",
		})
		a.denylist.revokeSession(id)
		a.subjects.drop(id)
		a.pushRevocation("session", id)
		a.emitEvent(r, "straza.revocation.session", map[string]any{"session": id})
		// Rev 21 chained end record: the verified token names both the
		// session and its owner, and the replay guard above keeps a re-sent
		// logout from re-emitting.
		a.emitAuthnSessionEnd(r.Context(), r, "revoked-self", authnFields{
			UserID: claims.Subject, Session: id,
			Reason: "revoked by session owner (logout)",
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "session": id})
}

// bulkRevokeMaxExplicit caps the explicit-set face of the bulk revoke; the
// user face expands server-side and has no cap. Matches the spec's
// reference-producer chunk size (rev 17), so one explicit call is one event.
const bulkRevokeMaxExplicit = 1000

// handleSessionsBulkRevoke serves POST /v1/admin/sessions/revoke: the
// fleet-scale stand-down. One call revokes a SET of sessions
// (named explicitly, or every ACTIVE session of a user), emitting ONE
// straza.revocation.sessions control event per 1000-id chunk (rev 17) plus
// ONE admin audit event, instead of per-session control traffic that queued
// ahead of later control events. Semantics per id match the single revoke
// exactly: stand-down, not lockout (locking is the user-lock API).
func (a *App) handleSessionsBulkRevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sessions []string `json:"sessions"`
		User     string   `json:"user"`
		Reason   string   `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if (len(req.Sessions) == 0) == (req.User == "") {
		apiError(w, http.StatusBadRequest, "exactly one of sessions[] or user is required")
		return
	}
	if len(req.Sessions) > bulkRevokeMaxExplicit {
		apiError(w, http.StatusBadRequest,
			fmt.Sprintf("sessions[] capped at %d per call; use the user face or chunk the set", bulkRevokeMaxExplicit))
		return
	}
	if req.Reason == "" {
		req.Reason = "bulk stand-down by admin"
	}

	ids := req.Sessions
	if req.User != "" {
		userID := req.User
		if u, err := a.store.Users().GetByUsername(r.Context(), req.User); err == nil {
			userID = u.ID
		} else if !errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, http.StatusInternalServerError, "user lookup failed", err)
			return
		}
		sessions, err := a.store.Sessions().ListByUser(r.Context(), userID)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "session listing failed", err)
			return
		}
		ids = ids[:0]
		for _, s := range sessions {
			if s.Status == store.SessionActive {
				ids = append(ids, s.ID)
			}
		}
	}

	revoked := make([]string, 0, len(ids))
	for _, id := range ids {
		// A stand-down starts no session write once the handler's context has
		// ended, so a store that stopped answering costs one write's limit,
		// not one per session. The write in flight at the bound runs on
		// afterCommit's context and completes, and the bound's 503 says to
		// read the sessions back and run the stand-down again.
		if r.Context().Err() != nil {
			break
		}
		// Only a real transition counts, fires, and rides the plural event,
		// which is what the contract always said ("unknown/already-dead ids
		// are skipped; the response counts what was actually revoked") and
		// what keeps a replayed set from re-amplifying the outbox.
		wctx, cancel := afterCommit(r.Context())
		if changed, err := a.store.Sessions().SetStatusIfChanged(wctx, id, store.SessionRevoked); err != nil || !changed {
			cancel()
			continue // unknown or already-dead id: the rest of the set still lands
		}
		_, _ = a.store.Revocations().Create(wctx, store.Revocation{
			Kind: store.RevokeSession, TargetID: id, Reason: req.Reason,
		})
		cancel()
		a.denylist.revokeSession(id)
		a.subjects.drop(id)
		// The push rule is per-target (spec §5): cheap core-NATS, no outbox.
		a.pushRevocation("session", id)
		revoked = append(revoked, id)
	}
	for chunk := revoked; len(chunk) > 0; {
		n := min(len(chunk), bulkRevokeMaxExplicit)
		ectx, cancel := afterCommit(r.Context())
		a.emitEventCtx(ectx, "straza.revocation.sessions", map[string]any{
			"sessions": chunk[:n], "reason": req.Reason,
		})
		cancel()
		chunk = chunk[n:]
	}
	a.emitEvent(r, "straza.audit.admin", map[string]any{
		"action": "sessions.bulk-revoke", "count": len(revoked),
		"target": req.User, "reason": req.Reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{"revoked": len(revoked)})
}
