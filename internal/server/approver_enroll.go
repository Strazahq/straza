package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// --- enrolment ---

type approverEnrollRequest struct {
	EnrollToken string `json:"enroll_token"`
	Device      struct {
		Name             string `json:"name"`
		Platform         string `json:"platform"`
		KeyAlg           string `json:"key_alg"`
		PublicKey        string `json:"public_key"`
		KeySecurityLevel string `json:"key_security_level"`
		Attestation      struct {
			Kind string `json:"kind"`
			Blob string `json:"blob"`
		} `json:"attestation"`
	} `json:"device"`
}

// handleApproverEnroll serves POST /v1/approver/enroll (no auth): it consumes
// the one-time enroll token, registers the P-256 device, and mints the 30-day
// use=approver device token.
func (a *App) handleApproverEnroll(w http.ResponseWriter, r *http.Request) {
	var req approverEnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	res, err := a.approval.Enroll(r.Context(), approval.EnrollInput{
		EnrollToken: req.EnrollToken, Name: req.Device.Name, Platform: req.Device.Platform,
		KeyAlg: req.Device.KeyAlg, PublicKeyB64: req.Device.PublicKey,
		KeySecurityLevel: req.Device.KeySecurityLevel,
		AttestationKind:  req.Device.Attestation.Kind, AttestationBlob: req.Device.Attestation.Blob,
	})
	if err != nil {
		switch {
		case errors.Is(err, approval.ErrBadKey):
			apiError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, approval.ErrEnrollTokenInvalid):
			apiError(w, http.StatusUnauthorized, err.Error())
		default:
			a.fail(w, r, http.StatusInternalServerError, "enrollment failed", err)
		}
		return
	}
	// The device row exists from here, so its record is written before the
	// credential mint that can still fail.
	a.auditApproverEnroll(r.Context(), res, req.Device.Platform, req.Device.Name)
	ttl := authn.DefaultApproverTokenTTL
	token, err := a.tokens.MintApproverToken(res.UserID, res.DeviceID, ttl)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not mint approver credential", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "approver-enroll", "user": res.UserID, "device": res.DeviceID,
	})
	resp := map[string]any{
		"approver_device_id": res.DeviceID,
		"device_token":       token,
		"expires_in":         int(ttl.Seconds()),
		"project":            a.projectRef(),
	}
	// BYO-Firebase: the enroll response is the
	// server→app copy of the deployment's public Firebase app config, the same
	// object the QR carries, minted fresh here so the app persists enroll-time
	// truth. Unconfigured ⇒ the key is absent entirely (the app's signal that
	// UnifiedPush/ntfy stays the lane), never null or an empty object.
	if fcm := a.enrollFCM(); fcm != nil {
		resp["fcm"] = fcm
	}
	// WebPush lane (openapi 0.40.0): the deployment's VAPID public key, what
	// the app hands its UnifiedPush distributor at REGISTER (spec 3) or a
	// browser passes as applicationServerKey. Same absent-key contract as fcm:
	// unconfigured ⇒ no webpush key at all (the app's signal that only the
	// legacy keyless lane exists), never null or an empty object. Shares
	// enrollWebpush with the QR payload (0.42.0) so the two surfaces cannot
	// disagree.
	if wp := a.enrollWebpush(); wp != nil {
		resp["webpush"] = wp
	}
	writeJSON(w, http.StatusCreated, resp)
}

// --- device-key-signed token refresh ---
//
// Both routes carry NO bearer: an EXPIRED token must not block its own
// refresh. The device re-authenticates by signing a fresh server challenge with
// the same hardware key it enrolled (no new trust anchor; the server already
// holds the SPKI). The refresh mints the SAME 30-day use=approver token (no
// scope change), so a natural day-30 expiry costs a signature, not a QR re-scan.

type approverRefreshChallengeRequest struct {
	DeviceID string `json:"approver_device_id"`
}

// handleApproverRefreshChallenge serves POST /v1/approver/refresh/challenge (no
// auth): it mints a fresh single-use challenge for a device-key-signed refresh.
// An unknown/revoked device is 404: there is nothing to retire, and the inert
// challenge is useless without the private key.
func (a *App) handleApproverRefreshChallenge(w http.ResponseWriter, r *http.Request) {
	var req approverRefreshChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	challenge, err := a.approval.RefreshChallenge(r.Context(), req.DeviceID)
	if errors.Is(err, approval.ErrStoreUnavailable) {
		a.answerServiceOutageCode(w, r, "approver refresh challenge", err)
		return
	}
	if err != nil {
		apiError(w, http.StatusNotFound, "no such approver device")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge":  challenge,
		"expires_in": int(approval.ChallengeTTL.Seconds()),
	})
}

type approverRefreshRequest struct {
	DeviceID  string `json:"approver_device_id"`
	Challenge string `json:"challenge"`
	Signature string `json:"signature"`
}

// handleApproverRefresh serves POST /v1/approver/refresh (no auth): it verifies
// the device-key signature over the refresh challenge and, on success, mints a
// fresh 30-day use=approver token. The mint stays here (a.tokens), mirroring the
// enroll/decide split. Failures map to a machine-readable 401 code so the app
// branches correctly: device_revoked ⇒ destroy the key; token_invalid /
// user_inactive ⇒ do not (a refresh cannot fix them, but the key is untouched).
func (a *App) handleApproverRefresh(w http.ResponseWriter, r *http.Request) {
	var req approverRefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	userID, deviceID, err := a.approval.RefreshSigned(r.Context(), approval.RefreshInput{
		DeviceID: req.DeviceID, Challenge: req.Challenge, SignatureB64: req.Signature,
	})
	if err != nil {
		if errors.Is(err, approval.ErrStoreUnavailable) {
			a.answerServiceOutageCode(w, r, "approver refresh", err)
			return
		}
		if status, code, mapped := approverRefreshError(err); mapped {
			apiErrorCode(w, status, code, err.Error())
			return
		}
		a.fail(w, r, http.StatusInternalServerError, "refresh failed", err)
		return
	}
	ttl := authn.DefaultApproverTokenTTL
	token, err := a.tokens.MintApproverToken(userID, deviceID, ttl)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not mint approver credential", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_token": token,
		"expires_in":   int(ttl.Seconds()),
	})
}

// approverRefreshError maps a RefreshSigned failure to its 401 status and
// machine-readable code. A device row gone ⇒ device_revoked (the app deletes its
// key); an inactive/missing bound user ⇒ user_inactive; anything unverifiable
// (bad signature, stale/consumed/missing challenge, unparseable stored key) ⇒
// token_invalid. mapped=false means an unexpected (store) error → 500.
func approverRefreshError(err error) (status int, code string, mapped bool) {
	switch {
	case errors.Is(err, approval.ErrDeviceRevoked):
		return http.StatusUnauthorized, codeDeviceRevoked, true
	case errors.Is(err, approval.ErrUserInactive):
		return http.StatusUnauthorized, codeUserInactive, true
	case errors.Is(err, approval.ErrBadSignature),
		errors.Is(err, approval.ErrChallengeInvalid),
		errors.Is(err, approval.ErrBadKey):
		return http.StatusUnauthorized, codeTokenInvalid, true
	default:
		return 0, "", false
	}
}

// --- admin: enroll-token mint (device revocation: approver_devices.go) ---

type approverEnrollTokenRequest struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}

// phoneEnrollGuardMessage reports why a phone-destined enroll-token mint
// must be refused, or "" when the QR stands a chance. Two refusal cases,
// both on facts known at mint time:
//  1. automint DERIVED a loopback advertise URL as its no-LAN last resort
//     (an operator-CONFIGURED loopback is the deliberate tunnel lane, ssh
//     tunnel plus adb reverse, where the phone really does dial 127.0.0.1)
//     while the listener accepts non-loopback traffic: the QR scans fine
//     and dials nothing on a phone.
//  2. an auto-minted pair does not name the advertised host: the app pins
//     the SPKI but still validates hostname against the pinned leaf
//     (anchored trust, fail closed), so the scan would end in a TLS
//     refusal. Bring-your-own pairs are the operator's choice.
//
// Only tier 1 is judged: the ingress and publicUrl tiers name hosts the
// operator chose for reachability already.
func (a *App) phoneEnrollGuardMessage() string {
	at := a.cfg.Server.ApproverTLS
	if at.Listen == "" {
		return ""
	}
	servers, tier := a.enrollServersTier()
	if tier != enrollTierApproverTLS || len(servers) == 0 {
		return ""
	}
	u, err := url.Parse(servers[0])
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if a.approverURLDerived && isLoopbackHost(host) {
		if bindHost, _, err := net.SplitHostPort(at.Listen); err == nil && !isLoopbackHost(bindHost) {
			return fmt.Sprintf("strazad found no network address to advertise, so this QR would name %s, a loopback address a phone cannot dial. Set server.approverTLS.publicUrl (env STRAZA_APPROVER_TLS_PUBLIC_URL) to the https URL the phone should dial, restart strazad, and mint again", servers[0])
		}
	}
	if c := a.approverCert; host != "" && c != nil && c.AutoMinted && c.Leaf != nil && c.Leaf.VerifyHostname(host) != nil {
		return fmt.Sprintf("the auto-minted approver TLS certificate does not name %s, the address this QR advertises, so the phone app would refuse the TLS handshake right after scanning. Delete the approver-tls directory under the server data dir and restart strazad to mint a certificate that names it; enrolled approver devices must then re-enroll", host)
	}
	return ""
}

// isLoopbackHost reports whether host can only ever reach this machine.
// The empty host ("" from a ":8443" bind) means every interface, not
// loopback.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// handleApproverEnrollToken serves POST /v1/admin/approvers/enroll-token
// (requireAdmin): it mints a one-time enroll token for a user and returns
// everything the console/CLI needs to render the enroll QR without
// re-deriving anything: the server base list, the TLS SPKI pin (only when the
// listener that minted it is the one the base list names), and the exact
// compact `qr_payload` string. This is the "Add mobile approver" surface, so
// a mint whose QR no phone could dial is refused (phoneEnrollGuardMessage).
func (a *App) handleApproverEnrollToken(w http.ResponseWriter, r *http.Request) {
	var req approverEnrollTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	if msg := a.phoneEnrollGuardMessage(); msg != "" {
		apiError(w, http.StatusConflict, msg)
		return
	}
	var (
		u   store.User
		err error
	)
	switch {
	case req.UserID != "":
		u, err = a.store.Users().GetByID(r.Context(), req.UserID)
	case req.Username != "":
		u, err = a.store.Users().GetByUsername(r.Context(), req.Username)
	default:
		apiError(w, http.StatusBadRequest, "user_id or username is required")
		return
	}
	if err != nil {
		apiError(w, http.StatusNotFound, "no such user")
		return
	}
	resp, err := a.enrollTokenEnvelope(r.Context(), u, "")
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not mint enroll token", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// enrollTokenEnvelope mints a one-time enroll token for u and assembles the
// response envelope both mint surfaces (admin and self-service) serve, so
// the two cannot disagree. The QR the console/page encodes verbatim
// (the approver app's enroll payload): v/servers/token, plus the SPKI pin ONLY
// when the tier that produced the servers list is the endpoint that minted
// that pin (enrollPin); an unconditional pin here is a time bomb behind an
// ingress. Plus the BYO-Firebase public app config when the deployment
// carries one (enrollFCM) and the WebPush VAPID advert when that lane is
// configured (enrollWebpush). `qr` is the structured twin of `qr_payload`
// (same bytes, parsed). channel scopes what the token may enroll: the admin
// lane passes "" (unscoped), the self lane the caller's authorized channel.
// Every mint chains one enroll-token.create record with the actor ctx holds.
func (a *App) enrollTokenEnvelope(ctx context.Context, u store.User, channel string) (map[string]any, error) {
	grant, err := a.approval.MintEnrollToken(ctx, u.ID, channel)
	if err != nil {
		return nil, err
	}
	a.auditEnrollTokenCreate(ctx, u, channel)
	servers, tier := a.enrollServersTier()
	pin := a.enrollPin(tier)
	payload := qrPayload{V: 1, Servers: servers, Token: grant.Token, Pin: pin, Project: a.projectRef(), FCM: a.enrollFCM(), Webpush: a.enrollWebpush()}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp := map[string]any{
		"enroll_token": grant.Token,
		"expires_in":   int(approval.EnrollTokenTTL.Seconds()),
		"user":         map[string]string{"id": u.ID, "username": u.Username},
		"servers":      servers,
		"project":      payload.Project,
		"qr":           payload,
		"qr_payload":   string(payloadJSON),
	}
	// Same value, same rule: the console renders this as the enrolment's pin
	// status, so it must never advertise a pin the QR (correctly) withholds;
	// an operator who hand-copies it would brick the phone just as thoroughly.
	if pin != "" {
		resp["tls_spki_pin"] = pin
	}
	return resp, nil
}

// --- self-service: a logged-in user enrolls their own browser/phone ---

// selfEnrollCooldown rate-limits the self mint per user. Enroll tokens are
// one-time and short-lived, so rapid minting buys an attacker only churn;
// the cooldown bounds that churn without a config knob. var, not const:
// tests shorten it.
var selfEnrollCooldown = 10 * time.Second

// selfEnrollCheck reports whether userID is still inside the cooldown window
// and, when limited, how many whole seconds remain (>=1, for Retry-After).
// Check only, no side effect: a refused mint (bad channel, NHI, missing role)
// must not burn the caller's window. Expired entries are pruned lazily under
// the same lock; the map is bounded by users actively minting inside one
// window.
func (a *App) selfEnrollCheck(userID string) (retryAfter int, limited bool) {
	a.selfEnrollMu.Lock()
	defer a.selfEnrollMu.Unlock()
	now := time.Now()
	for id, at := range a.selfEnrollLast {
		if now.Sub(at) >= selfEnrollCooldown {
			delete(a.selfEnrollLast, id)
		}
	}
	if at, ok := a.selfEnrollLast[userID]; ok {
		remain := selfEnrollCooldown - now.Sub(at)
		secs := int(remain.Seconds()) + 1
		return secs, true
	}
	return 0, false
}

// selfEnrollRecord starts userID's cooldown window; called only when a mint
// is actually happening. Check and record are separate calls, so two
// concurrent gate-passers can both mint once: benign, the cooldown is
// anti-churn, not a security boundary (the token itself is one-time).
func (a *App) selfEnrollRecord(userID string) {
	a.selfEnrollMu.Lock()
	defer a.selfEnrollMu.Unlock()
	if a.selfEnrollLast == nil {
		a.selfEnrollLast = make(map[string]time.Time)
	}
	a.selfEnrollLast[userID] = time.Now()
}

// channelsForRoles answers which channels u may self-enroll given the
// already-resolved roles: empty for NHIs and role-less users, both for
// straza-admin, sorted (browser before mobile). ONE source of truth: the
// mint gate (via selfEnrollEligibility) and GET /v1/self both derive from
// it, so the page's offers and the mint's verdicts can never disagree, and
// a caller that already holds the roles slice resolves exactly once.
func channelsForRoles(u store.User, roles []store.Role) []string {
	channels := make([]string, 0, 2)
	if userKind(u) == "nhi" {
		return channels
	}
	mobile, browser := false, false
	for _, role := range roles {
		switch role.Name {
		case AdminRole:
			mobile, browser = true, true
		case EnrollMobileRole:
			mobile = true
		case EnrollBrowserRole:
			browser = true
		}
	}
	if browser {
		channels = append(channels, approval.EnrollChannelBrowser)
	}
	if mobile {
		channels = append(channels, approval.EnrollChannelMobile)
	}
	return channels
}

// selfEnrollEligibility is the resolve-then-derive wrapper the mint gate
// uses. The NHI early return skips a pointless role resolution.
func (a *App) selfEnrollEligibility(ctx context.Context, u store.User) ([]string, error) {
	if userKind(u) == "nhi" {
		return make([]string, 0, 2), nil
	}
	roles, err := a.resolver.ResolveRoles(ctx, u.ID, time.Now())
	if err != nil {
		return nil, err
	}
	return channelsForRoles(u, roles), nil
}

// handleSelfEnrollToken serves POST /v1/approvals/self/enroll-token
// (requirePersonClient: a person's session or login token, a coding-harness
// session and wat_ refused, user verified active). The logged-in user mints a one-time approver enroll
// token for THEMSELF; the body carries ONLY the channel, identity fields in
// it are ignored, self-scoping is by construction (mint-for-others stays the
// admin surface). The gate: humans only,
// holding the channel's straza-enroll-* role or straza-admin; the minted
// token is channel-stamped and consume enforces the scope. Lives on the main
// listener only: the dedicated approver listener keeps serving device-authed
// /v1/approver/* exclusively.
func (a *App) handleSelfEnrollToken(w http.ResponseWriter, r *http.Request, userID string) {
	var req struct {
		Channel string `json:"channel"`
	}
	// Plain decode: unknown fields (a user_id, a username) stay ignored.
	_ = json.NewDecoder(r.Body).Decode(&req)
	var need string
	switch req.Channel {
	case approval.EnrollChannelMobile:
		need = EnrollMobileRole
		// Phone-destined: a dud QR is refused here exactly as on the admin
		// surface. The browser channel stays open, because a browser on this
		// machine dials loopback just fine.
		if msg := a.phoneEnrollGuardMessage(); msg != "" {
			apiError(w, http.StatusConflict, msg)
			return
		}
	case approval.EnrollChannelBrowser:
		need = EnrollBrowserRole
	default:
		apiError(w, http.StatusBadRequest, "channel must be mobile or browser")
		return
	}
	if retry, limited := a.selfEnrollCheck(userID); limited {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		apiError(w, http.StatusTooManyRequests, "enroll-token cooldown active; retry shortly")
		return
	}
	u, err := a.store.Users().GetByID(r.Context(), userID)
	if err != nil {
		apiError(w, http.StatusUnauthorized, "user no longer exists")
		return
	}
	if userKind(u) == "nhi" {
		apiError(w, http.StatusForbidden, "self enrollment is for a person; agent and service identities cannot enroll an approver phone or browser")
		return
	}
	channels, err := a.selfEnrollEligibility(r.Context(), u)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "role resolution failed", err)
		return
	}
	allowed := false
	for _, ch := range channels {
		if ch == req.Channel {
			allowed = true
			break
		}
	}
	if !allowed {
		apiError(w, http.StatusForbidden, "requires role "+need+" or "+AdminRole+"; enrollment roles are assigned in your identity manager")
		return
	}
	a.selfEnrollRecord(userID)
	resp, err := a.enrollTokenEnvelope(r.Context(), u, req.Channel)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not mint enroll token", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "approver-self-enroll-token", "user": u.ID,
	})
	writeJSON(w, http.StatusOK, resp)
}
