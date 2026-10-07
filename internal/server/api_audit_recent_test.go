package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/audit"
)

// TestAuditListOrderDesc pins the tail contract on /v1/admin/audit:
// order=desc returns the newest window newest-first, the ascending default
// stays byte-compatible for existing clients (verify pages it), and the
// ambiguous or malformed order forms are refused instead of silently
// reinterpreted. Without it, `strazactl audit tail` would read the
// ascending page from seq 0 and show the OLDEST records while its help
// text promises the most recent.
func TestAuditListOrderDesc(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	for _, cmd := range []string{"echo one", "echo two", "echo three"} {
		decide(t, base, adminTok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": cmd})
	}
	stored := waitForAudit(t, app, 3)
	maxSeq := stored[len(stored)-1].Seq

	list := func(query string, wantCode int) []audit.Record {
		t.Helper()
		code, body := getJSONAuth(t, base+"/v1/admin/audit?"+query, adminTok)
		if code != wantCode {
			t.Fatalf("audit list %q = %d %s, want %d", query, code, body, wantCode)
		}
		if wantCode != http.StatusOK {
			return nil
		}
		var recs []audit.Record
		if err := json.Unmarshal([]byte(body), &recs); err != nil {
			t.Fatalf("audit list %q: %v", query, err)
		}
		return recs
	}

	desc := list("order=desc&limit=2", http.StatusOK)
	if len(desc) != 2 {
		t.Fatalf("order=desc&limit=2 returned %d records, want 2", len(desc))
	}
	if desc[0].Seq != maxSeq {
		t.Fatalf("order=desc first seq = %d, want the newest %d", desc[0].Seq, maxSeq)
	}
	if desc[0].Seq <= desc[1].Seq {
		t.Fatalf("order=desc not newest-first: %d then %d", desc[0].Seq, desc[1].Seq)
	}

	asc := list("after=0&limit=1000", http.StatusOK)
	if len(asc) < 3 || asc[0].Seq >= asc[len(asc)-1].Seq {
		t.Fatalf("ascending default changed: %d records, first %d, last %d",
			len(asc), asc[0].Seq, asc[len(asc)-1].Seq)
	}

	list("order=desc&after=1&limit=10", http.StatusBadRequest)
	list("order=sideways&limit=10", http.StatusBadRequest)
}
