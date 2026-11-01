package server

import (
	"testing"
)

// TestSubjectDeviceCertHonestFalse pins the honest-false posture: the
// device-certificate second factor is NOT implemented (no platform CA, no
// issuance, no verification), so the server-built policy subject must
// carry deviceCert=false even for a session with an enrolled device. A
// device ID proves enrollment, not certificate possession. Reporting true
// would make `require: {deviceCert: true}` pass for every enrolled session
// on the gateway lane while the hook lane (agentguard state.go) says false,
// so the same rule would allow on one PEP and deny on the other.
func TestSubjectDeviceCertHonestFalse(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	h := newKeyHolder(t, base)
	h.enroll(idToken, "sha256:fp-devicecert")
	_, sessionID := h.mustCheckin()

	sub, ok := app.subjects.get(sessionID)
	if !ok {
		t.Fatal("subject cache missed the fresh session")
	}
	if sub.DeviceCert {
		t.Error("subject DeviceCert = true for a plain enrolled device: no certificate was ever issued or verified; the honest value is false until D6 ships")
	}
}
