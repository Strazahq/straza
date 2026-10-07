package server

import (
	"fmt"

	"github.com/strazahq/straza/internal/store"
)

// deviceClientKind maps the client_kind an enrol request carries to the
// stored kind. Missing means the enforcement kit, so kit builds that predate
// the field keep enrolling; ok is false for any other value.
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
	switch harness {
	case "strazactl":
		return "Run strazactl login again from your own terminal"
	case "self-service":
		return "Sign in to the self-service page in your browser"
	}
	return "Sign in to the console in your browser"
}

// deviceClientRefusal judges a device credential against the harness name a
// check-in declared. A credential the kit enrolled, or one from before the
// kind was recorded, opens no session under a human client's name, and a
// credential a human client enrolled opens no session under a coding
// harness. It answers the 403 sentence and the audit reason, both empty when
// the credential and the harness agree.
func deviceClientRefusal(d store.Device, harness string) (sentence, reason string) {
	human := adminHarnesses[harness]
	switch {
	case d.ClientKind == store.DeviceClientHuman && !human:
		return fmt.Sprintf("this device credential belongs to strazactl or the console and cannot start a %s session. Run straza enroll on this machine", harness),
			"device credential is bound to the human clients"
	case d.ClientKind == store.DeviceClientKit && human:
		return fmt.Sprintf("this device credential belongs to the enforcement kit and cannot open a %s session. %s", harness, humanClientHint(harness)),
			"device credential is bound to the enforcement kit"
	case d.ClientKind == "" && human:
		return fmt.Sprintf("this device credential was issued before Straza recorded which client enrolled it, so it cannot open a %s session. %s", harness, humanClientHint(harness)),
			"device credential carries no client kind"
	}
	return "", ""
}
