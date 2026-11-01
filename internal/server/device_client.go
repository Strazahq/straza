package server

import (
	"fmt"
	"net/http"

	"github.com/strazahq/straza/internal/store"
)

// The answer and the audit reason that retire the Straza client's bearer
// device credential: its enrollments and its rows without a key are refused.
const (
	oldClientMsg = "This Straza client is older than the server's sign-in rules, which now need the Straza " +
		"background service on this machine. Update Straza from your software portal, then run `straza enroll`."
	bearerRetiredReason = "the client's bearer device credential is retired"
)

// The harness name strazactl checked in under, and the answer and the audit
// reason that refuse its sign-in, its device credential and its sessions.
// strazactl runs on an admin API token instead.
const (
	strazactlHarness   = "strazactl"
	strazactlSignInMsg = "strazactl no longer signs in, because its sign-in kept an administrator credential in a file " +
		"that any program running as you could copy. Mint an admin API token in the console under Settings, API tokens, " +
		"and run strazactl with STRAZA_API_TOKEN set to it."
	strazactlSignInReason = "strazactl no longer signs in"
)

// refuseStrazactl answers 403 with strazactlSignInMsg and writes one login
// failure record with the fields f names and the reason
// strazactlSignInReason.
func (a *App) refuseStrazactl(w http.ResponseWriter, r *http.Request, f authnFields) {
	f.Reason = strazactlSignInReason
	a.emitAuthnLogin(r, "failure", f)
	apiError(w, http.StatusForbidden, strazactlSignInMsg)
}

// deviceClientKind maps the client_kind an enrol request carries to the
// stored kind. Missing means the enforcement kit, as kit builds that predate
// the field send nothing; ok is false for any other value.
func deviceClientKind(v string) (kind string, ok bool) {
	switch v {
	case "":
		return store.DeviceClientKit, true
	case store.DeviceClientKit, store.DeviceClientHuman:
		return v, true
	}
	return "", false
}

// personUser reports whether the user is a person rather than an agent or a
// service account. It reads both non-human signals through userKind, so a
// user created with attrs.kind nhi and no user_type is not a person. Only a
// user with neither signal counts as one.
func personUser(u store.User) bool {
	return userKind(u) == "human"
}

// userTypeWord names a non-person user's type for a sentence.
func userTypeWord(u store.User) string {
	if u.UserType == store.UserTypeService {
		return "a service account"
	}
	return "an agent"
}

// humanClientHint says how a person gets a credential for the named human
// client.
func humanClientHint(harness string) string {
	if harness == "self-service" {
		return "Sign in to the self-service page in your browser"
	}
	return "Sign in to the console in your browser"
}

// deviceClientRefusal judges a device credential against the harness name a
// check-in declared. A credential strazactl enrolled, client kind human,
// opens no session at all, because strazactl no longer signs in. A
// credential the kit enrolled, or one from before the kind was recorded,
// opens no session under a human client's name. A kit row or a row with no
// kind that holds no key opens no session at all, because the client's
// bearer device credential is retired. It answers the 403 sentence and the
// audit reason, both empty when the credential and the harness agree.
func deviceClientRefusal(d store.Device, harness string) (sentence, reason string) {
	human := adminHarnesses[harness]
	switch {
	case d.ClientKind == store.DeviceClientHuman:
		return strazactlSignInMsg, strazactlSignInReason
	case d.ClientKind == store.DeviceClientKit && human:
		return fmt.Sprintf("this device credential belongs to the enforcement kit and cannot open a %s session. %s", harness, humanClientHint(harness)),
			"device credential is bound to the enforcement kit"
	case d.ClientKind == "" && human:
		return fmt.Sprintf("this device credential was issued before Straza recorded which client enrolled it, so it cannot open a %s session. %s", harness, humanClientHint(harness)),
			"device credential carries no client kind"
	case d.KeyThumbprint == "":
		return oldClientMsg, bearerRetiredReason
	}
	return "", ""
}
