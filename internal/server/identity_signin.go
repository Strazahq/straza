package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// The bounds of the sign-in judgments of a person's key enrollment.
const (
	// signInFreshFor is how old a browser sign-in may be when the
	// enrollment it was made for arrives.
	signInFreshFor = 10 * time.Minute
	// signInClockAhead is how far ahead of strazad's clock an identity
	// provider's auth_time may run.
	signInClockAhead = time.Minute
	// nonceRandomBytes is the least random part of a browser sign-in's nonce.
	nonceRandomBytes = 16
	// signInTimeLayout renders a sign-in time in a refusal sentence.
	signInTimeLayout = "2006-01-02 15:04:05 UTC"
	// keyEnrollAudience is the one client a built-in ID token that enrolls a
	// person's machine key was minted for. strazactl and the console sign in
	// as clients of their own.
	keyEnrollAudience = "straza"
)

// The audit reasons of the sign-in judgments and of the id-token lane on
// enterprise.
const (
	otherProgramReason = "the sign-in was made for another Straza program"
	otherKeyReason     = "the sign-in was made for another key"
	noAuthTimeReason   = "the sign-in carries no auth_time"
	staleSignInReason  = "the sign-in is older than 10 minutes"
	aheadSignInReason  = "the sign-in time is ahead of the server clock"
	signInBelowReason  = "the sign-in lane is below governance.minSignIn"
	personDoorReason   = "a person's ID token alone does not start a session on enterprise"
	agentProofReason   = "an agent check-in without a key proof is refused on enterprise"
)

// The answers of the sign-in judgments and of the id-token lane on
// enterprise. browserTail also tells a person whose Straza is too old to
// open a browser to update it.
const (
	otherProgramMsg = "Straza refused this sign-in because it was made for another Straza program. " +
		"Run `straza enroll` on this machine and finish the sign-in it starts."
	otherKeyMsg = "Straza refused this sign-in because it was made for a different enrollment. " +
		"Run `straza enroll` again and finish the sign-in in the browser it opens."
	noAuthTimeMsg = "Straza refused this sign-in because the identity provider sent no auth_time claim, " +
		"so Straza cannot tell that you signed in just now. Ask IT to add the auth_time claim to the ID token " +
		"of the Straza app, then run `straza enroll` again."
	staleSignInMsg = "Straza refused this sign-in because your identity provider says you last signed in at %s, " +
		"more than 10 minutes ago. Run `straza enroll` again and sign in when the browser asks."
	aheadSignInMsg = "Straza refused this sign-in because your identity provider says you signed in at %s, " +
		"which is ahead of the Straza server's clock. Ask your Straza admin to check the clocks of the Straza " +
		"server and the identity provider, then run `straza enroll` again."
	pastDeadlineMsg = "Straza refused this sign-in because your identity provider says you last signed in at %s, " +
		"longer ago than your Straza admin allows. Sign out at your identity provider, then run `straza enroll` " +
		"again and sign in."
	browserTail = "Run `straza enroll` in a desktop session on this machine, and update Straza first if it " +
		"does not open a browser."
	codeLaneMsg = "Straza refused this sign-in because your Straza admin requires a browser sign-in, and a " +
		"sign-in code cannot show which machine it was approved for. " + browserTail
	codeMachineMsg = "Straza refused this machine because it was enrolled with a sign-in code, and your Straza " +
		"admin now requires a browser sign-in. " + browserTail
	personDoorMsg = "Straza refused this session because a sign-in token alone no longer starts a session on " +
		"this server. Install Straza on this machine and run `straza enroll`."
	agentProofMsg = "Straza refused this session because the user %s is %s, and this server accepts its " +
		"check-in only with a key proof. Install a current Straza on the machine it runs on, with `straza service` " +
		"running, and enroll it there with `straza enroll --headless`."
)

// signInFacts is what a person's key enrollment judges about its ID token:
// the facts the token carries, and the audience the built-in issuer minted
// it for, "" for an external provider's token, whose audience is the one
// oidc.clientId every Straza program shares.
type signInFacts struct {
	authn.LoginFacts
	audience string
}

// signInLane names the lane of a person's key enrollment from the nonce of
// its ID token and the thumbprint of the key being enrolled. No nonce is
// lane code, because a device code sign-in carries none. A nonce of the form
// <thumbprint>.<random>, whose thumbprint is the key's and whose random part
// is unpadded base64url of at least 16 bytes, is lane browser. Any other
// nonce reports false, because it was made for another key or another client.
func signInLane(nonce, thumbprint string) (string, bool) {
	if nonce == "" {
		return config.SignInCode, true
	}
	jkt, random, ok := strings.Cut(nonce, ".")
	if !ok || jkt != thumbprint {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(random)
	if err != nil || len(raw) < nonceRandomBytes {
		return "", false
	}
	return config.SignInBrowser, true
}

// signInRefused reports whether the sign-in lane ranks below
// governance.minSignIn. An unset setting refuses nothing, as minCustody's
// does, because config validation sets it on every boot.
func (a *App) signInRefused(lane string) bool {
	minimum := a.cfg.Governance.MinSignIn
	return minimum != "" && config.SignInRank(lane) < config.SignInRank(minimum)
}

// judgeSignIn judges the sign-in facts f of a person's enrollment of the key
// whose thumbprint is thumbprint at now, in this order: a built-in token was
// minted for the straza client, the nonce names the key, a browser sign-in
// carries an auth_time at most 10 minutes old and at most a minute ahead,
// the lane ranks at governance.minSignIn or above, and the sign-in is younger
// than governance.signInMaxAge, so no enrollment is past its deadline when
// it lands. An auth_time of 0 or below reads as a sign-in in 1970 or before,
// so it is stale on both lanes. It answers the lane, or the 403 sentence and
// the audit reason of the first judgment that fails.
func (a *App) judgeSignIn(f signInFacts, thumbprint string, now time.Time) (lane, sentence, reason string) {
	if f.audience != "" && f.audience != keyEnrollAudience {
		return "", otherProgramMsg, otherProgramReason
	}
	lane, ok := signInLane(f.Nonce, thumbprint)
	if !ok {
		return "", otherKeyMsg, otherKeyReason
	}
	if lane == config.SignInBrowser {
		at := f.AuthTime.UTC().Format(signInTimeLayout)
		switch {
		case f.AuthTime.IsZero():
			return "", noAuthTimeMsg, noAuthTimeReason
		case f.AuthTime.Sub(now) > signInClockAhead:
			return "", fmt.Sprintf(aheadSignInMsg, at), aheadSignInReason
		case now.Sub(f.AuthTime) > signInFreshFor:
			return "", fmt.Sprintf(staleSignInMsg, at), staleSignInReason
		}
	}
	if a.signInRefused(lane) {
		return "", codeLaneMsg, signInBelowReason
	}
	if at := signedInAt(f.LoginFacts, now); now.Sub(at) >= a.cfg.EffectiveSignInMaxAge() {
		return "", fmt.Sprintf(pastDeadlineMsg, at.Format(signInTimeLayout)), signInPassedReason
	}
	return lane, "", ""
}

// signedInAt is the sign-in time a key enrollment records, from which the
// sign-in deadline counts: the provider's auth_time when it is present and
// not more than a minute ahead of now, and now otherwise.
func signedInAt(f authn.LoginFacts, now time.Time) time.Time {
	if !f.AuthTime.IsZero() && f.AuthTime.Sub(now) <= signInClockAhead {
		return f.AuthTime.UTC()
	}
	return now.UTC()
}

// keyCustodyRow is a keyed device row's custody column: the custody the
// client reported and sign_in, the lane strazad judged at the enrollment.
// The request's custodyInfo has no sign_in, so a client cannot claim a lane.
type keyCustodyRow struct {
	custodyInfo
	SignIn string `json:"sign_in"`
}

// storedSignIn is the sign-in lane a device row's stored custody records,
// code when it records none, because every key enrollment before strazad
// recorded the lane signed in with a code.
func storedSignIn(stored string) string {
	var row keyCustodyRow
	if json.Unmarshal([]byte(stored), &row) != nil || row.SignIn == "" {
		return config.SignInCode
	}
	return row.SignIn
}

// auditDeviceEnrolled chains the device.enrolled record of a person's key
// enrollment of device d: the lane strazad judged, the custody kind the
// client reported, and the provider's auth_time when the ID token carried
// one.
func (a *App) auditDeviceEnrolled(r *http.Request, d store.Device, lane, custody string, authTime time.Time) {
	data := map[string]any{
		"action": "device.enrolled", "user": d.UserID, "device": d.ID, "signIn": lane, "custody": custody,
	}
	if !authTime.IsZero() {
		data["authTime"] = authTime.UTC().Format(time.RFC3339)
	}
	a.emitEvent(r, "straza.audit.identity", data)
}

// idTokenDoorRefusal judges an id-token check-in of u under the harness name
// harness, where proof reports a DPoP header, and answers the 403 sentence
// and the audit reason, or "" when it passes. Only enterprise refuses. A
// person's token under a name other than the human clients' would start a
// session outside the Straza service from a token an agent can take from the
// person's browser, and an agent or a service account must bind its session
// to a key. Standalone's built-in issuer asks for the password at every
// sign-in, so it keeps both lanes.
func (a *App) idTokenDoorRefusal(u store.User, harness string, proof bool) (string, string) {
	if a.cfg.Profile != config.ProfileEnterprise {
		return "", ""
	}
	switch {
	case personUser(u) && !adminHarnesses[harness]:
		return personDoorMsg, personDoorReason
	case !personUser(u) && !proof:
		return fmt.Sprintf(agentProofMsg, u.Username, userTypeWord(u)), agentProofReason
	}
	return "", ""
}
