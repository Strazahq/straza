package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/authn"
)

// TestApproverInactiveRecordSurvivesACancelledRequest pins that the
// user_inactive record of the signed phone lane reaches the outbox when the
// phone's connection is gone before strazad writes it, as every other
// straza.audit.authn record does, and that the token is then marked, so
// the next refused poll writes none.
func TestApproverInactiveRecordSurvivesACancelledRequest(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, _, _ := testAppFaultLog(t, log)
	kim := seedIdentity(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	claims := authn.ApproverClaims{Subject: kim.ID, Device: "apd_cancelled", JTI: "jti-cancelled"}
	// The outbox, not the chain: the record is in it when the call returns.
	locked := func() int {
		n := 0
		for _, d := range outboxDataFor(t, app, "straza.audit.authn") {
			if d["via"] == approverTokenVia && d["reason"] == "user is locked" {
				n++
			}
		}
		return n
	}

	app.auditApproverInactive(httptest.NewRequest("GET", "/v1/approver/pending", nil).WithContext(ctx), kim, claims, nil)
	if n := locked(); n != 1 {
		t.Fatalf("user is locked records after a cancelled request = %d, want 1", n)
	}
	if strings.Contains(buf.String(), "the user_inactive refusal record could not be written") {
		t.Errorf("a cancelled request logged the failed-write Warn line:\n%s", buf.String())
	}
	app.auditApproverInactive(httptest.NewRequest("GET", "/v1/approver/pending", nil), kim, claims, nil)
	if n := locked(); n != 1 {
		t.Errorf("user is locked records after the next refused poll = %d, want still 1", n)
	}
}
