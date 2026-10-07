package server

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestAuditBatchLogsOncePerSession pins that the Warn lines of refused
// spooled records collapse to one line per named session and reason in an
// upload, carrying the count of records it covers and the first record's
// id, while the answer and straza_audit_refused_total still count records.
func TestAuditBatchLogsOncePerSession(t *testing.T) {
	t.Parallel()
	r := newSpoolRig(t)
	kim := seedIdentity(t, r.app)
	joe := r.user(t, "joe", store.UserTypeHuman)
	kimB := r.session(t, kim.ID, "dev-kim")
	joeA := r.session(t, joe.ID, "dev-joe")
	const n = 5000
	recs := make([]spooled, 0, 2*n+1)
	for i := range n {
		recs = append(recs, spooled{fmt.Sprintf("u5-one-%d", i), fmt.Sprintf("u5-one-%d", i), joeA.ID},
			spooled{fmt.Sprintf("u5-bad-%d", i), fmt.Sprintf("u5-bad-%d", i), "x"})
	}
	recs = append(recs, spooled{"u5-one-own", "u5-one-own", ""})

	code, resp := r.upload(t, kimB, recs...)
	if code != http.StatusOK || resp["accepted"] != float64(1) || resp["refused"] != float64(2*n) {
		t.Fatalf("batch = %d %v, want 200 with accepted 1 and refused %d", code, resp, 2*n)
	}
	if got := r.reads.reads(joeA.ID); got != 1 {
		t.Errorf("session reads of joe's session = %d, want 1", got)
	}
	for _, c := range []struct{ why, first string }{{whyOtherUser, "u5-one-0"}, {whyMalformed, "u5-bad-0"}} {
		if got := warnLines(r.logs, c.why); got != 1 {
			t.Errorf("WARN lines saying %q = %d, want 1", c.why, got)
		}
		if got := warnLines(r.logs, c.why, fmt.Sprintf("count=%d ", n), "record="+c.first+" ", onlyTrace); got != 1 {
			t.Errorf("WARN lines saying %q with count=%d and the first record %s = %d, want 1:\n%s", c.why, n, c.first, got, lastLines(r.logs.String(), 3))
		}
	}
	if got := counterValue(t, r.app, "straza_audit_refused_total"); got != float64(2*n) {
		t.Errorf("straza_audit_refused_total = %v, want %d", got, 2*n)
	}
	waitChainData(t, r.app, "u5-one-own")
}
