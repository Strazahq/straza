package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// Client identifiers accepted as ID-token audiences (the client_id used in
// the device flow). "console" is the embedded web console.
var allowedAudiences = []string{"straza", "strazactl", "console"} // "straza" (the enforcement kit / headless NHI audience), "strazactl", and the embedded console.

// handleIdPDiscovery is the login-discovery document: it tells
// clients (straza enroll, strazactl login, the console) which OIDC
// issuer to run the RFC 8628 device flow against. Enterprise: the external
// IdP (interactive login happens at the IdP, strazad only verifies the
// resulting ID token). Standalone: the built-in issuer, even when
// an external issuer is additionally configured for server-side federation;
// deterministic beats clever (the bootstrap admin has no IdP
// account). An empty client_id means "use your own client id" (the
// built-in issuer accepts per-client audiences). Public by design: issuer
// URL and client id are discovery metadata, not secrets.
func (a *App) handleIdPDiscovery(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Profile == config.ProfileEnterprise {
		if a.cfg.OIDC.Issuer == "" {
			a.fail(w, r, http.StatusServiceUnavailable,
				"no login issuer configured. Set oidc.issuer (the enterprise profile has no built-in issuer)", nil)
			return
		}
		// nhi_issuer names where NHIs authenticate (the narrow built-in
		// mount): humans go to the IdP above, agents come
		// here. Additive: old kits ignore the unknown field.
		writeJSON(w, http.StatusOK, map[string]string{
			"issuer": a.cfg.OIDC.Issuer, "client_id": a.cfg.OIDC.ClientID,
			"nhi_issuer": a.cfg.Server.PublicURL,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"issuer": a.cfg.Server.PublicURL, "client_id": "",
		"nhi_issuer": a.cfg.Server.PublicURL,
	})
}

type attestationPayload struct {
	Managed bool `json:"managed"`
	// Platform is the client's GOOS/GOARCH; it selects platform-scoped rows
	// in the expected-hash registry (spec/attestation v1beta1).
	Platform string            `json:"platform,omitempty"`
	Hashes   map[string]string `json:"hashes"`
}

type harnessInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type checkinRequest struct {
	// Exactly one of IDToken (fresh login), DeviceToken (enrolled device
	// starting a session), or SessionToken (refresh).
	IDToken      string             `json:"id_token,omitempty"`
	DeviceToken  string             `json:"device_token,omitempty"`
	SessionToken string             `json:"session_token,omitempty"`
	DeviceID     string             `json:"device_id,omitempty"`
	Harness      harnessInfo        `json:"harness"`
	Attestation  attestationPayload `json:"attestation"`
	// Client is the straza build making the check-in (spec/attestation
	// v1beta1): recorded on the session, never verified.
	Client clientInfo `json:"client"`
}

type clientInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type packPayload struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Content  string `json:"content"`
	Checksum string `json:"checksum"`
}

type checkinResponse struct {
	SessionID    string        `json:"session_id"`
	SessionToken string        `json:"session_token"`
	ExpiresIn    int           `json:"expires_in"`
	Attestation  string        `json:"attestation"`
	User         string        `json:"user"`
	Roles        []string      `json:"roles"`
	SnapshotID   string        `json:"snapshot_id"`
	Packs        []packPayload `json:"knowledge_packs"`
	// Identity typology (spec/policyset revision 9): the client mirrors these
	// onto its cached session so the LOCAL hook-lane subject matches
	// identity-scoped sets exactly like the server does (one engine, no
	// drift). Additive; older clients ignore them.
	UserType   string `json:"user_type,omitempty"`
	AgencyMode string `json:"agency_mode,omitempty"`
	SwarmID    string `json:"swarm_id,omitempty"`
	// AdminGrants is the session's admin-plane standing ("full" or the
	// canonical "area:verb,…" union from admin.roleAreas); the console
	// hides inaccessible areas from it. Omitted for sessions with no admin
	// standing, so an agent checkin never carries an admin-shaped field.
	// Display only; requireAdmin re-derives authority per request.
	AdminGrants string `json:"admin_grants,omitempty"`
	// AdminServers counts the servers whose admin role the session holds,
	// so the console opens the MCP servers area for a server admin who has
	// no area grant. Omitted when zero. Display only, like AdminGrants.
	AdminServers int `json:"admin_servers,omitempty"`
	// DeviceToken is a renewed enroll credential (see
	// renewDeviceCredential). Additive; older clients ignore it.
	DeviceToken          string `json:"device_token,omitempty"`
	DeviceTokenExpiresIn int    `json:"device_token_expires_in,omitempty"`
}

// handleCheckin starts or refreshes a session after enrollment:
// verifies identity, computes the attestation level, resolves roles, mints a
// session token, and returns the role-bound knowledge packs.
func (a *App) handleCheckin(w http.ResponseWriter, r *http.Request) {
	var req checkinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}

	var (
		u         store.User
		sessionID string
		dc        authn.DeviceClaims // device-token lane only; the renewal reads it
	)
	// Device binding and attestation are session-scoped: fixed at
	// session start, never re-derived from a refresh payload. A refresh
	// carries no device_id, and trusting its attestation would let every
	// 30 s daemon tick silently downgrade (or upgrade) the minted claims.
	deviceID := req.DeviceID
	attLevel := ""
	// gateHarness feeds the admin-CLI exemption; on refresh the session
	// row is authoritative so a blocked agent cannot re-label itself.
	gateHarness := req.Harness.Name
	// via names the credential lane for the authn events (rev 21); 401/403
	// refusals emit, 400s and outages do not (not authentication outcomes).
	via := ""
	switch {
	case req.SessionToken != "":
		via = "session-token"
		var ses store.Session
		var ok bool
		if u, ses, ok = a.refreshSession(w, r, req); !ok {
			return
		}
		sessionID = ses.ID
		deviceID = ses.DeviceID
		attLevel = ses.AttestationLevel
		gateHarness = ses.HarnessName
	case req.DeviceToken != "":
		via = "device-token"
		// The enroll credential starts sessions long after the login
		// token died. Local verify first, then the in-memory denylist (the
		// push-fed kill switch bites before any store read), then user and
		// (below, via deviceID) device status from the store (control plane).
		var err error
		dc, err = a.tokens.VerifyDeviceToken(req.DeviceToken)
		if err != nil {
			a.emitAuthnLogin(r, "failure", authnFields{Via: via,
				Harness: harnessLabel(req.Harness), Reason: "device credential rejected"})
			apiError(w, http.StatusUnauthorized, "device credential rejected. Run `straza enroll` again")
			return
		}
		if a.denylist.userBlocked(dc.Subject) || a.denylist.deviceBlocked(dc.Device) {
			a.emitAuthnLogin(r, "failure", authnFields{Via: via,
				UserID: dc.Subject, Harness: harnessLabel(req.Harness),
				Reason: "device or user revoked"})
			apiError(w, http.StatusForbidden, revokedIdentityMsg)
			return
		}
		u, err = a.store.Users().GetByID(r.Context(), dc.Subject)
		if err != nil {
			if a.answerOutage(w, r, "device checkin", err) {
				return // a store blip must not read as "re-enroll"
			}
			a.emitAuthnLogin(r, "failure", authnFields{Via: via,
				UserID: dc.Subject, Harness: harnessLabel(req.Harness),
				Reason: "device credential rejected"})
			apiError(w, http.StatusUnauthorized, "device credential rejected. Run `straza enroll` again")
			return
		}
		if u.Status != store.UserActive {
			a.emitAuthnLogin(r, "failure", authnFields{Via: via,
				User: u.Username, UserID: u.ID, Harness: harnessLabel(req.Harness),
				Reason: "user is disabled"})
			apiError(w, http.StatusForbidden, "user is disabled. Contact your administrator")
			return
		}
		// The token's device binding is authoritative; a client-supplied
		// device_id cannot re-point it.
		deviceID = dc.Device
	case req.IDToken != "":
		via = "id-token"
		var err error
		u, err = a.verifyLogin(r, req.IDToken)
		var refused *loginRefusal
		if errors.As(err, &refused) {
			a.refuseLogin(w, r, refused, harnessLabel(req.Harness))
			return
		}
		if err != nil {
			if a.answerOutage(w, r, "session exchange", err) {
				return // Postgres down must not read as "login not accepted"
			}
			a.log.Warn("session exchange: login not accepted", "err", err)
			a.emitAuthnLogin(r, "failure", authnFields{Via: via,
				Harness: harnessLabel(req.Harness), Reason: "login not accepted"})
			apiError(w, http.StatusUnauthorized, "login not accepted")
			return
		}
	default:
		apiError(w, http.StatusBadRequest, "id_token, device_token, or session_token is required")
		return
	}

	// The human clients' harness names open the admin plane and the decide
	// routes, so they are for people only, whatever credential is presented.
	if adminHarnesses[gateHarness] && !personUser(u) {
		a.emitAuthnLogin(r, "failure", authnFields{Via: via,
			User: u.Username, UserID: u.ID, Harness: harnessLabel(req.Harness),
			Reason: "only a person can open a human client session"})
		apiError(w, http.StatusForbidden, fmt.Sprintf(
			"the user %s is %s, and only a person can open a %s session. Sign in as yourself from your own terminal or browser",
			u.Username, userTypeWord(u), gateHarness))
		return
	}

	if deviceID != "" {
		d, err := a.store.Devices().GetByID(r.Context(), deviceID)
		if err != nil && a.answerOutage(w, r, "device checkin", err) {
			return // a 403 here is terminal for the daemon; an outage is not
		}
		if err != nil || d.UserID != u.ID || d.Status != "active" {
			a.emitAuthnLogin(r, "failure", authnFields{Via: via,
				User: u.Username, UserID: u.ID, Harness: harnessLabel(req.Harness),
				Reason: "device not enrolled for this user"})
			apiError(w, http.StatusForbidden, "device not enrolled for this user")
			return
		}
		// Judged before the attestation exemption below, which a human
		// client's name would otherwise grant to a kit credential.
		if sentence, reason := deviceClientRefusal(d, gateHarness); sentence != "" {
			a.emitAuthnLogin(r, "failure", authnFields{Via: via,
				User: u.Username, UserID: u.ID, Harness: harnessLabel(req.Harness), Reason: reason})
			apiError(w, http.StatusForbidden, sentence)
			return
		}
	}

	if attLevel == "" {
		// Managed claims are verified against the expected-hash registry.
		// Registry unavailable = unknown state = fail closed,
		// never a silent downgrade to an issued-anyway token.
		var expected []store.AttestationHash
		if req.Attestation.Managed && len(req.Attestation.Hashes) > 0 {
			var err error
			expected, err = a.store.AttestationHashes().List(r.Context())
			if err != nil {
				a.fail(w, r, http.StatusInternalServerError, "attestation registry unavailable. Retry or contact your administrator", err)
				return
			}
		}
		attLevel = verifyAttestation(req.Attestation, req.Harness.Name, expected)
	}

	// Attestation gate: below the profile's minimum attestation level → no
	// session, no token (the enterprise default minimum is "managed"). The
	// admin CLI is exempt so operators can always log in to bootstrap the registry;
	// its surface is role-gated, and an unattested token is still barred from
	// the data plane because the gateway enforces the same minimum from token
	// claims (gatewayAuth).
	if minAtt := a.cfg.Governance.MinAttestation; minAtt != "" && !adminHarnesses[gateHarness] &&
		config.AttestationRank(attLevel) < config.AttestationRank(minAtt) {
		// Rev 21: this refusal rides the hash-chained authn subject.
		a.emitAuthnLogin(r, "failure", authnFields{Via: via,
			User: u.Username, UserID: u.ID, Harness: harnessLabel(req.Harness),
			Reason: fmt.Sprintf("attestation level %q is below the required level %q", attLevel, minAtt)})
		apiError(w, http.StatusForbidden, fmt.Sprintf(
			"attestation level %q is below the required level %q. Reinstall with `straza install --managed <harness>` or contact your administrator",
			attLevel, minAtt))
		return
	}
	hashesJSON, _ := json.Marshal(req.Attestation.Hashes)

	if sessionID == "" {
		ses, err := a.store.Sessions().Create(r.Context(), store.Session{
			UserID: u.ID, DeviceID: deviceID,
			HarnessName: req.Harness.Name, HarnessVersion: req.Harness.Version,
			ClientVersion:    req.Client.Version,
			AttestationLevel: attLevel, AttestationHashes: string(hashesJSON),
		})
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "could not create session", err)
			return
		}
		sessionID = ses.ID
		a.emitEvent(r, "straza.identity.updated", map[string]any{
			"action": "session.start", "user": u.ID, "session": sessionID,
			"harness": req.Harness.Name, "attestation": attLevel,
		})
		// Interactive login success (rev 21): the id-token lane only. A
		// device-token session start is a daemon resuming its standing (the
		// session.start above witnesses it), and refresh successes are
		// deliberately unlogged (spec: a 30 s tick per session is noise, not
		// authn information).
		if via == "id-token" {
			a.emitAuthnLogin(r, "success", authnFields{Via: via,
				User: u.Username, UserID: u.ID, Session: sessionID,
				Harness: harnessLabel(req.Harness)})
		}
	} else {
		_ = a.store.Sessions().Touch(r.Context(), sessionID, time.Now())
	}

	// Cache the resolved subject for the DB-free /v1/decide path. The
	// cached subject ends at the next window edge of these roles, so a role
	// that starts or ends mid-session sends the next request back to check
	// in, and settleRoles caches it only for roles whose config this replica
	// applied (drafts_apply.go).
	facts := sessionFacts{person: personUser(u), harness: req.Harness.Name}
	settled, refusal, err := a.settleRoles(r.Context(), sessionID, u.ID, facts, func(roles []store.Role) policy.Subject {
		names := make([]string, len(roles))
		for i, role := range roles {
			names[i] = role.Name
		}
		sub := policy.Subject{
			User:        u.Username,
			Roles:       names,
			Attestation: attLevel,
			// Honest false until a device-certificate factor exists (no
			// platform CA, no issuance, no verification; a device ID proves
			// enrollment, NOT cert possession). Both PEPs agree on false now:
			// agentguard's subject says the same, so `require: {deviceCert}`
			// denies identically everywhere instead of silently passing every
			// enrolled session here.
			DeviceCert: false,
			Harness:    joinHarness(req.Harness.Name, req.Harness.Version),
			UserType:   u.UserType,
			AgencyMode: u.AgencyMode,
			SwarmID:    u.SwarmID,
		}
		sub.Sponsor, sub.SponsorID = a.resolveSponsor(r.Context(), u)
		return sub
	})
	if refusal != "" {
		w.Header().Set("Retry-After", loginOutageRetryAfter)
		a.fail(w, r, http.StatusServiceUnavailable, refusal, err)
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "role resolution failed", err)
		return
	}
	roles, newSubject := settled.roles, settled.subject
	roleIDs := make([]string, len(roles))
	for i, role := range roles {
		roleIDs[i] = role.ID
	}
	roleNames := newSubject.Roles
	// The prior subject was captured before the new one replaced it: a
	// refresh check-in that re-resolves different roles (an IdM change reached
	// the PDP mid session) must nudge THIS session's live MCP stream to
	// re-list, because the gateway's per-session catalog overlay is keyed on
	// the subject digest. The first check-in (no prior) never nudges: there
	// is nothing stale to drop. A sponsor change nudges too, so a re-sponsored
	// agent stops riding the old sponsor's connection at its next refresh.
	prevSubject, hadPrev := settled.prev, settled.hadPrev
	if hadPrev && a.gateway != nil &&
		(!sameStringSet(prevSubject.Roles, newSubject.Roles) ||
			prevSubject.UserType != newSubject.UserType ||
			prevSubject.AgencyMode != newSubject.AgencyMode ||
			prevSubject.SwarmID != newSubject.SwarmID ||
			prevSubject.SponsorID != newSubject.SponsorID) {
		a.gateway.dropOverlay(sessionID)
		a.gateway.streams.notifySession(sessionID, listChangedNotification)
	}

	snapshotID := a.snapshots.Current().ID

	token, claims, err := a.tokens.Mint(authn.Claims{
		Subject: u.ID, Session: sessionID, Device: deviceID,
		Harness:     joinHarness(req.Harness.Name, req.Harness.Version),
		Attestation: attLevel, RolesHash: authn.RolesHash(roleIDs), Snapshot: snapshotID,
	})
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not mint session token", err)
		return
	}

	packs, err := a.store.Packs().ForRoles(r.Context(), roleIDs)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not load knowledge packs", err)
		return
	}
	packOut := make([]packPayload, len(packs))
	for i, p := range packs {
		packOut[i] = packPayload{Name: p.Name, Version: p.Version, Content: p.Content, Checksum: p.Checksum}
	}

	administered, err := a.store.Apps().ListByAdminRoles(r.Context(), roleIDs)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "server lookup failed", err)
		return
	}

	var renewed string
	var renewedTTL time.Duration
	if via == "device-token" {
		// Credential renewal: every gate above passed (denylist, user and device
		// status, attestation minimum, session minted), so this device is
		// still enrolled and in good standing.
		renewed, renewedTTL, err = a.renewDeviceCredential(r, dc, u.ID, deviceID)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "could not renew device credential", err)
			return
		}
	}

	writeJSON(w, http.StatusOK, checkinResponse{
		SessionID:            sessionID,
		SessionToken:         token,
		ExpiresIn:            int(time.Until(claims.Expiry).Seconds()),
		Attestation:          attLevel,
		User:                 u.Username,
		Roles:                roleNames,
		SnapshotID:           snapshotID,
		Packs:                packOut,
		UserType:             u.UserType,
		AgencyMode:           u.AgencyMode,
		SwarmID:              u.SwarmID,
		AdminGrants:          a.adminGrantsForRoles(roles),
		AdminServers:         len(administered),
		DeviceToken:          renewed,
		DeviceTokenExpiresIn: int(renewedTTL.Seconds()),
	})
}

// emitEvent inserts an event into the transactional outbox, which the
// relay publishes to NATS.
func (a *App) emitEvent(r *http.Request, subject string, data map[string]any) {
	a.emitEventCtx(r.Context(), subject, data)
}

// emitEventCtx is the context-only variant, for callers without a request
// (snapshot service, background workers). The CE source carries the pod's
// unique instance id so consumers can tell their own events from other
// pods' (the convergence consumer skips self-events; in-process handlers
// already applied the change). The chained records of straza.audit.admin,
// straza.audit.authn and straza.audit.identity and the straza.identity.*
// announcements are written even when ctx is cancelled, because the change
// they record has already happened, and each insert waits at most
// auditInsertTimeout for the store.
func (a *App) emitEventCtx(ctx context.Context, subject string, data map[string]any) {
	switch {
	case subject == "straza.audit.admin", subject == "straza.audit.authn", subject == "straza.audit.identity",
		strings.HasPrefix(subject, "straza.identity."):
		// A client that disconnects after the store write must not take
		// the record with it, and a store that stops answering must not
		// hold the handler for ever. The actor is a context value, which
		// survives.
		var cancel context.CancelFunc
		ctx, cancel = afterCommit(ctx)
		defer cancel()
	}
	// Admin mutations are attributed to the authenticated principal
	// requireAdmin stored in the context: cloudEvent is the one seam that
	// stamps it, for every emit site and for the records of a publish.
	ev, err := a.cloudEvent(ctx, subject, data)
	if err != nil {
		return
	}
	if _, err := a.store.Outbox().Insert(ctx, ev); err != nil {
		a.log.Warn("outbox insert failed", "subject", subject, "err", err)
	}
}

// afterCommit answers the context one write that follows a committed change
// runs on: ctx's values without its cancel or its deadline, so a client that
// left or a write that ran past its bound cannot cut it, and at most
// auditInsertTimeout for the store.
func afterCommit(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), auditInsertTimeout)
}

func apiError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
