package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestStatusCarriesAttestation pins that the service's status names the
// attestation level strazad gave each session and when it judged it, at the
// check-in that started the session, so straza doctor can tell managed
// wiring strazad still accepts from wiring it refuses. The fake strazad
// answers every check-in with none.
func TestStatusCarriesAttestation(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	ses, err := r.start(alice, "w1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	st := r.s.Status(alice)
	if len(st.Sessions) != 1 || st.Sessions[0].Attestation != "none" ||
		st.Sessions[0].AttestedAt.IsZero() || !st.Sessions[0].AttestedAt.Equal(ses.IssuedAt) {
		t.Fatalf("sessions = %+v, want one that attests none since its check-in at %v", st.Sessions, ses.IssuedAt)
	}
	raw, err := json.Marshal(st.Sessions[0])
	if err != nil || !strings.Contains(string(raw), `"attestation":"none"`) || !strings.Contains(string(raw), `"attested_at":`) {
		t.Fatalf("status JSON = %s %v, want the attestation fields", raw, err)
	}
	// A session with no level, as a service from before the fields sends,
	// leaves both out, so the answer is the one an older client knows.
	raw, err = json.Marshal(localapi.SessionStatus{SessionID: "s1"})
	if err != nil || strings.Contains(string(raw), "attest") {
		t.Fatalf("status JSON = %s %v, want no attestation fields when they are empty", raw, err)
	}
}
