package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// The answers of the key lanes of enroll and check-in.
const (
	keyNotEnrolledMsg   = "device not enrolled for this key"
	oneCredentialMsg    = "a check-in carries one credential: a token in the body or a DPoP proof, not both"
	counterWithTokenMsg = "a check-in with a counter is a key check-in, so it carries no token in the body. " +
		"Send a token without a counter, or device_id and counter with a DPoP proof."
	keyReusedMsg = "Straza stopped trusting this machine because its key was also used from a copy of it, " +
		"such as a cloned VM or a restored backup. Run straza enroll to enroll this machine again, " +
		"and tell your Straza admin if nobody copied it."
	keyOnlyDeviceMsg = "This machine checks in with its own key, so a sign-in token or device credential " +
		"cannot start a session for it. Check in from the Straza client on that machine."
	staleKeyMsg = "This session was started with a key this machine no longer uses, because the machine " +
		"was enrolled again. Start a new session, and the Straza client checks in with the new key."
	signInPassedReason = "the last sign-in on this machine is older than governance.signInMaxAge"
	custodyRefusedMsg  = "Straza refused this machine because its key is kept in your own account instead of by " +
		"the Straza background service, and your Straza admin requires the service. Install Straza from your " +
		"software portal, then run `straza enroll`."
	custodyBelowReason = "key custody is below governance.minCustody"
)

// custodyInfo says who holds an enrolled key on the machine: kind account
// is a holder under its own OS account, kind user a key in the person's own
// account. Platform and version name the holder build.
type custodyInfo struct {
	Kind     string `json:"kind"`
	Platform string `json:"platform,omitempty"`
	Version  string `json:"version,omitempty"`
}

// storedCustody reads a device row's stored custody, nil when the row holds
// none or it does not parse.
func storedCustody(stored string) *custodyInfo {
	var c custodyInfo
	if stored == "" || json.Unmarshal([]byte(stored), &c) != nil {
		return nil
	}
	return &c
}

// custodyKind is the kind of a device row's stored custody, "" when the row
// holds none, which ranks below every setting.
func custodyKind(stored string) string {
	if c := storedCustody(stored); c != nil {
		return c.Kind
	}
	return ""
}

// custodyRefused reports whether the custody kind ranks below
// governance.minCustody. An unset setting refuses nothing, as
// minAttestation's does, because config validation sets it on every boot.
func (a *App) custodyRefused(kind string) bool {
	minimum := a.cfg.Governance.MinCustody
	return minimum != "" && config.CustodyRank(kind) < config.CustodyRank(minimum)
}

// keyEnrollResponse answers a key enrollment. It carries no device token,
// because the key is the machine's credential.
type keyEnrollResponse struct {
	UserID          string `json:"user_id"`
	Username        string `json:"username"`
	DeviceID        string `json:"device_id"`
	KeyThumbprint   string `json:"key_thumbprint"`
	SignInExpiresIn int    `json:"sign_in_expires_in"`
}

// loadProofVerifier loads the DPoP nonce key every replica shares, creating
// it on first boot, and builds the proof verifier from it.
func (a *App) loadProofVerifier(ctx context.Context, st store.Store) error {
	key, err := authn.LoadOrCreateNonceKey(ctx, st.Settings())
	if err != nil {
		// The store answered the migrations just before, so the likely cause
		// is a malformed key row, and the error says what to do about it.
		return fmt.Errorf("strazad did not start because the DPoP nonce key could not be loaded: %w", err)
	}
	a.proofs = authn.NewProofVerifier(key)
	return nil
}

// checkProof verifies the DPoP proof of r, a request that carries no access
// token, with any key, and with ctr equal to counter on a check-in. A
// missing or stale nonce is answered here with the nonce challenge and
// reports answered true. Any other failure returns the refusal's reason in
// the verifier's words for the caller to answer. The proof id is left
// unspent: the caller spends it with spendProof before it acts.
func (a *App) checkProof(w http.ResponseWriter, r *http.Request, counter int64, now time.Time) (p authn.Proof, answered bool, reason string) {
	proofs := r.Header.Values(dpop.HeaderProof)
	switch {
	case len(proofs) == 0:
		return authn.Proof{}, false, "the request carries no DPoP proof"
	case len(proofs) > 1:
		return authn.Proof{}, false, "more than one DPoP proof"
	}
	p, err := a.proofs.Verify(proofs[0], authn.ProofCheck{
		Method: r.Method, Path: r.URL.Path, Origins: a.proofOrigins(r),
		Counter: counter, CallerSpends: true,
	}, now)
	var refused *authn.ProofError
	switch {
	case errors.Is(err, authn.ErrStaleNonce):
		a.answerNonce(w)
		return authn.Proof{}, true, ""
	case errors.As(err, &refused):
		return authn.Proof{}, false, refused.Reason
	case err != nil:
		return authn.Proof{}, false, "the proof did not verify"
	}
	return p, false, ""
}

// spendProof records p's proof id as spent in the store every replica
// shares, and reports false when it was spent before, which means the proof
// was replayed.
func (a *App) spendProof(ctx context.Context, p authn.Proof, now time.Time) (bool, error) {
	return a.store.Devices().SpendProof(ctx, p.Thumbprint, p.JTI, now.Add(authn.SpentProofLifetime))
}

// answerStoreOutage answers 503 to a key lane whose store write or read
// failed. The writes and reads it covers fail only when the store does, so
// it does not ask the store whether it is down.
func (a *App) answerStoreOutage(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Retry-After", loginOutageRetryAfter)
	a.fail(w, r, http.StatusServiceUnavailable, loginOutageBody, fmt.Errorf("key lane: %w", err))
}

// keyEnrollFieldsError checks the fields a key enrollment adds to the body
// and answers the 400 sentence, or "" when they hold.
func keyEnrollFieldsError(req enrollRequest, kind string) string {
	switch {
	case kind != store.DeviceClientKit:
		return "a key enrollment is for the Straza client only, so client_kind must be kit or left out"
	case req.Custody.Kind != "account" && req.Custody.Kind != "user":
		return "custody.kind must be account or user"
	case len(req.Custody.Platform) > 64 || len(req.Custody.Version) > 64:
		return "custody.platform and custody.version must each be at most 64 bytes"
	case req.MachineBinding == "" || len(req.MachineBinding) > 128:
		return "machine_binding is required and must be at most 128 bytes"
	case req.Counter != 0:
		return "counter must be 0 on a key enrollment, because a new key has not checked in yet"
	}
	return ""
}

// enrollProof checks the key-enrollment fields of req and the DPoP proof of
// r, whose jwk is the key being enrolled. On a refusal it answers the request
// and reports false. A refused proof writes no audit record, because no actor
// is known before the ID token is verified.
func (a *App) enrollProof(w http.ResponseWriter, r *http.Request, req enrollRequest, kind string) (authn.Proof, bool) {
	if msg := keyEnrollFieldsError(req, kind); msg != "" {
		apiError(w, http.StatusBadRequest, msg)
		return authn.Proof{}, false
	}
	p, answered, reason := a.checkProof(w, r, 0, time.Now())
	if answered {
		return authn.Proof{}, false
	}
	if reason != "" {
		refuseEnrollProof(w, reason)
		return authn.Proof{}, false
	}
	return p, true
}

// refuseEnrollProof answers 401 to a key enrollment whose proof failed for
// reason.
func refuseEnrollProof(w http.ResponseWriter, reason string) {
	w.Header().Set("WWW-Authenticate", `DPoP error="invalid_dpop_proof"`)
	apiError(w, http.StatusUnauthorized, "the DPoP proof was not accepted: "+reason+
		". Sign a fresh proof with the key you are enrolling.")
}

// enrollKeyDevice binds the proof key to a device row of u and answers the
// key enrollment, whose ID token said f about its sign-in. The proof is
// spent before any judgment, so a replayed request writes no second record.
// Only a person enrolls a machine key, its sign-in must pass judgeSignIn, its
// custody must rank at governance.minCustody or above, and every enrollment
// brings a key none of u's devices holds. It reuses only a keyed kit row with
// the same fingerprint, so no enrollment converts a bearer row, and a
// re-enrollment replaces the key, the custody, the binding and the counter
// and restarts the sign-in deadline from the new sign-in. Each enrollment
// writes one device.enrolled record.
func (a *App) enrollKeyDevice(w http.ResponseWriter, r *http.Request, req enrollRequest, u store.User, p authn.Proof, f signInFacts) {
	now := time.Now()
	fresh, err := a.spendProof(r.Context(), p, now)
	if err != nil {
		a.answerStoreOutage(w, r, err)
		return
	}
	if !fresh {
		refuseEnrollProof(w, "jti was already used")
		return
	}
	if !personUser(u) {
		a.emitAuthnLogin(r, "failure", authnFields{Via: "device-key", User: u.Username, UserID: u.ID,
			Reason: "only a person can enroll a machine key"})
		apiError(w, http.StatusForbidden, fmt.Sprintf(
			"the user %s is %s, and only a person can enroll a machine key. An AI agent or a service account checks in with its own client credentials instead.",
			u.Username, userTypeWord(u)))
		return
	}
	lane, sentence, reason := a.judgeSignIn(f, p.Thumbprint, now)
	if sentence != "" {
		a.emitAuthnLogin(r, "failure", authnFields{Via: "device-key", User: u.Username, UserID: u.ID, Reason: reason})
		apiError(w, http.StatusForbidden, sentence)
		return
	}
	if a.custodyRefused(req.Custody.Kind) {
		a.emitAuthnLogin(r, "failure", authnFields{Via: "device-key", User: u.Username, UserID: u.ID,
			Reason: custodyBelowReason})
		apiError(w, http.StatusForbidden, custodyRefusedMsg)
		return
	}
	devices, err := a.store.Devices().ListByUser(r.Context(), u.ID)
	if err != nil {
		a.answerStoreOutage(w, r, err)
		return
	}
	var device store.Device
	for _, d := range devices {
		if d.KeyThumbprint == p.Thumbprint {
			// A key enrolled again would carry the counter of its first
			// enrollment, which a client that starts over at 1 cannot know.
			apiError(w, http.StatusConflict, "This key is already enrolled for you. "+
				"Make a new key and enroll again, because each enrollment needs a key of its own.")
			return
		}
		if req.Device.Fingerprint != "" && d.KeyThumbprint != "" && d.Fingerprint == req.Device.Fingerprint &&
			d.ClientKind == store.DeviceClientKit && device.ID == "" {
			device = d
		}
	}
	custody, _ := json.Marshal(keyCustodyRow{custodyInfo: req.Custody, SignIn: lane}) // four strings always encode
	signedAt := signedInAt(f.LoginFacts, now)
	device.KeyJWK, device.KeyThumbprint = p.JWK, p.Thumbprint
	device.Custody, device.MachineBinding = string(custody), req.MachineBinding
	device.KeyCounter, device.SignedInAt = 0, signedAt
	if device.ID != "" {
		device, err = a.store.Devices().Update(r.Context(), device)
	} else {
		device.UserID, device.Name, device.Platform = u.ID, req.Device.Name, req.Device.Platform
		device.Fingerprint, device.ClientKind = req.Device.Fingerprint, store.DeviceClientKit
		device, err = a.store.Devices().Create(r.Context(), device)
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "could not register device", err)
		return
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "enroll", "user": u.ID, "device": device.ID,
	})
	a.auditDeviceEnrolled(r, device, lane, req.Custody.Kind, f.AuthTime)
	left := max(0, a.cfg.EffectiveSignInMaxAge()-now.Sub(signedAt))
	writeJSON(w, http.StatusOK, keyEnrollResponse{
		UserID: u.ID, Username: u.Username, DeviceID: device.ID, KeyThumbprint: device.KeyThumbprint,
		SignInExpiresIn: int(left.Seconds()),
	})
}

// The refusal of an agent's check-in whose proof key is not the key its ID
// token is stamped for.
const (
	proofNotStampReason = "the DPoP proof key is not the key the ID token was issued for"
	proofNotStampMsg    = "The key proof does not come from the key this agent's ID token was issued for. " +
		"Check in from the machine that holds the agent's registered key."
)

// checkinNHIProof verifies the DPoP proof of the NHI u's id-token check-in
// and answers the proof key's thumbprint for the session's cnf. The proof is
// checked as the key lane checks it, with a nonce and with no ath and no ctr.
// When the ID token is stamped for a key, stamp, the proof key must be that
// key. The proof is spent once it passes. On a refusal it answers the
// request and reports false: the nonce challenge with no record, a failed,
// replayed or other-key proof with 401 and one login failure via id-token,
// and a store outage with 503.
func (a *App) checkinNHIProof(w http.ResponseWriter, r *http.Request, u store.User, harness harnessInfo, stamp string) (string, bool) {
	refuseWith := func(msg, reason string) (string, bool) {
		w.Header().Set("WWW-Authenticate", `DPoP error="invalid_dpop_proof"`)
		a.emitAuthnLogin(r, "failure", authnFields{Via: "id-token", User: u.Username, UserID: u.ID,
			Harness: harnessLabel(harness), Reason: reason})
		apiError(w, http.StatusUnauthorized, msg)
		return "", false
	}
	refuse := func(why, reason string) (string, bool) {
		return refuseWith("the DPoP proof was not accepted: "+why+". Sign a fresh proof with this agent's key.", reason)
	}
	now := time.Now()
	p, answered, why := a.checkProof(w, r, 0, now)
	switch {
	case answered:
		return "", false
	case why != "":
		return refuse(why, "the DPoP proof did not verify")
	case stamp != "" && p.Thumbprint != stamp:
		return refuseWith(proofNotStampMsg, proofNotStampReason)
	}
	fresh, err := a.spendProof(r.Context(), p, now)
	switch {
	case err != nil:
		a.answerStoreOutage(w, r, err)
		return "", false
	case !fresh:
		return refuse("jti was already used", "the DPoP proof was replayed")
	}
	return p.Thumbprint, true
}

// checkinKey is the key lane of check-in: a body with device_id and counter
// and no token, and a DPoP proof from the device's enrolled key. It checks
// the nonce, the proof, the device row and its key, the denylist, the user,
// the sign-in deadline and the counter, in that order. On a refusal it
// answers the request and reports false. Every 401 and 403 writes one login
// failure, except the key-reuse refusal, whose identity record is the
// evidence, and a store outage answers 503. On true the caller runs the
// shared gates for u and d.
func (a *App) checkinKey(w http.ResponseWriter, r *http.Request, req checkinRequest) (store.User, store.Device, bool) {
	none := func() (store.User, store.Device, bool) { return store.User{}, store.Device{}, false }
	refuse := func(code int, msg string, f authnFields) (store.User, store.Device, bool) {
		f.Via, f.Harness = "device-key", harnessLabel(req.Harness)
		a.emitAuthnLogin(r, "failure", f)
		apiError(w, code, msg)
		return none()
	}
	switch {
	case req.DeviceID == "":
		apiError(w, http.StatusBadRequest, "device_id is required on a key check-in")
		return none()
	case req.Counter < 1:
		apiError(w, http.StatusBadRequest, "counter must be above 0 on a key check-in, and above the last counter this machine sent")
		return none()
	}
	now := time.Now()
	p, answered, reason := a.checkProof(w, r, req.Counter, now)
	if answered {
		return none()
	}
	if reason != "" {
		w.Header().Set("WWW-Authenticate", `DPoP error="invalid_dpop_proof"`)
		return refuse(http.StatusUnauthorized, "the DPoP proof was not accepted: "+reason+
			". Sign a fresh proof with the key this machine enrolled.",
			authnFields{Reason: "the DPoP proof did not verify"})
	}
	d, err := a.store.Devices().GetByID(r.Context(), req.DeviceID)
	if err != nil && a.answerOutage(w, r, "key checkin", err) {
		return none()
	}
	if err != nil || d.Status != "active" || d.KeyThumbprint == "" || d.KeyThumbprint != p.Thumbprint {
		return refuse(http.StatusForbidden, keyNotEnrolledMsg, authnFields{Reason: keyNotEnrolledMsg})
	}
	// The proof is spent before anything acts on it, so a replay on any
	// replica can neither move the counter nor reach the reuse path.
	fresh, err := a.spendProof(r.Context(), p, now)
	if err != nil {
		a.answerStoreOutage(w, r, err)
		return none()
	}
	if !fresh {
		w.Header().Set("WWW-Authenticate", `DPoP error="invalid_dpop_proof"`)
		return refuse(http.StatusUnauthorized, "the DPoP proof was not accepted: jti was already used. "+
			"Sign a fresh proof with the key this machine enrolled.",
			authnFields{UserID: d.UserID, Reason: "the DPoP proof was replayed"})
	}
	if a.denylist.userBlocked(d.UserID) || a.denylist.deviceBlocked(d.ID) {
		return refuse(http.StatusForbidden, revokedIdentityMsg, authnFields{UserID: d.UserID, Reason: "device or user revoked"})
	}
	u, err := a.store.Users().GetByID(r.Context(), d.UserID)
	if err != nil && a.answerOutage(w, r, "key checkin", err) {
		return none()
	}
	if err != nil || u.Status != store.UserActive {
		return refuse(http.StatusForbidden, "user is disabled. Contact your administrator",
			authnFields{User: u.Username, UserID: d.UserID, Reason: "user is disabled"})
	}
	if msg, passed := a.signInPassed(d, now); passed {
		return refuse(http.StatusForbidden, msg, authnFields{User: u.Username, UserID: u.ID, Reason: signInPassedReason})
	}
	// A failed counter write or re-read below is an outage, and it never
	// reaches the reuse path, which deletes the device.
	outage := func(err error) (store.User, store.Device, bool) {
		a.answerStoreOutage(w, r, err)
		return none()
	}
	if req.Counter > d.KeyCounter {
		moved, err := a.store.Devices().AdvanceCounter(r.Context(), d.ID, d.KeyThumbprint, req.Counter)
		if err != nil {
			return outage(err)
		}
		if moved {
			return u, d, true
		}
		// The row changed after the read: it was deleted, enrolled to another
		// key, or raised to this counter or past it by another check-in. Only
		// the last is key reuse, so the row is read again to tell them apart.
		cur, err := a.store.Devices().GetByID(r.Context(), d.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return outage(err)
		}
		if err != nil || cur.KeyThumbprint != d.KeyThumbprint || cur.KeyCounter < req.Counter {
			return refuse(http.StatusForbidden, keyNotEnrolledMsg,
				authnFields{User: u.Username, UserID: u.ID, Reason: keyNotEnrolledMsg})
		}
		d = cur
	}
	if !a.revokeReusedKey(r, d, req.Counter) {
		return refuse(http.StatusForbidden, keyNotEnrolledMsg,
			authnFields{User: u.Username, UserID: u.ID, Reason: keyNotEnrolledMsg})
	}
	apiError(w, http.StatusForbidden, keyReusedMsg)
	return none()
}

// revokeReusedKey handles a key check-in whose counter sent is at or below
// the last one accepted for d, which means a copy of the key checked in too.
// It revokes the device the way an admin revoke does and writes one
// device.key.reused record. It reports false, with no record, when another
// revoke deleted the row first. A delete the store fails is logged at Error
// and the record is still written, because the reuse happened.
func (a *App) revokeReusedKey(r *http.Request, d store.Device, sent int64) bool {
	err := a.revokeDevice(r, d.ID, "device key reused")
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		a.log.Error("key reuse: the device could not be revoked, so it stays enrolled. Revoke it with strazactl devices revoke",
			"user", d.UserID, "device", d.ID, "err", err)
	}
	a.emitEvent(r, "straza.audit.identity", map[string]any{
		"action": "device.key.reused", "user": d.UserID, "device": d.ID,
		"counter": sent, "storedCounter": d.KeyCounter,
		"reason": "check-in counter at or below the last one accepted",
	})
	return true
}

// keyDeviceRefusal judges the device row d of a check-in against its
// credential lane via and the cnf of the session it mints, "" for a bearer
// session. A keyed device opens sessions from its key only, a key-bound
// session ends once the machine enrolled a new key, once its custody ranks
// below governance.minCustody or once its sign-in lane ranks below
// governance.minSignIn, and it ends at the sign-in deadline even inside its
// lifetime. A bearer session and a row without a key pass here, and
// deviceClientRefusal judges them. It answers the status, the sentence and
// the audit reason, a zero status when the device passes.
func (a *App) keyDeviceRefusal(d store.Device, via, cnf string, now time.Time) (int, string, string) {
	switch {
	case d.KeyThumbprint != "" && (via == "id-token" || via == "device-token"):
		return http.StatusForbidden, keyOnlyDeviceMsg, "the device checks in with its key"
	case cnf == "" || d.KeyThumbprint == "":
		return 0, "", ""
	case d.KeyThumbprint != cnf:
		return http.StatusUnauthorized, staleKeyMsg, "the session's key is no longer the device's key"
	case a.custodyRefused(custodyKind(d.Custody)):
		return http.StatusForbidden, custodyRefusedMsg, custodyBelowReason
	case a.signInRefused(storedSignIn(d.Custody)):
		return http.StatusForbidden, codeMachineMsg, signInBelowReason
	}
	if msg, passed := a.signInPassed(d, now); passed {
		return http.StatusForbidden, msg, signInPassedReason
	}
	return 0, "", ""
}

// signInPassed reports whether the sign-in deadline of the keyed device d
// has passed at now, with the sentence that refuses it. A row with no
// sign-in time fails closed as past it.
func (a *App) signInPassed(d store.Device, now time.Time) (string, bool) {
	maxAge := a.cfg.EffectiveSignInMaxAge()
	msg := "Your last sign-in on this machine was more than " + periodWords(maxAge) +
		" ago, and your Straza admin requires a new one. Run straza enroll and sign in with your work account."
	return msg, d.SignedInAt.IsZero() || now.Sub(d.SignedInAt) > maxAge
}

// periodWords renders d in whole days when it is whole days, else in whole
// hours when it is whole hours, else in whole minutes, rounded down.
func periodWords(d time.Duration) string {
	unit, n := "minute", int64(d/time.Minute)
	switch {
	case d%(24*time.Hour) == 0:
		unit, n = "day", int64(d/(24*time.Hour))
	case d%time.Hour == 0:
		unit, n = "hour", int64(d/time.Hour)
	}
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
