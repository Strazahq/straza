package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/audit"
)

// sentinelCE builds a straza.audit.sentinel CloudEvent as the audit sentinel
// publishes them: data carries {session, user, detector, severity, reason,
// evidence[], window?}.
func sentinelCE(id, session, user, detector, severity, reason string) string {
	ce := map[string]any{
		"specversion": "1.0",
		"id":          id,
		"type":        "straza.audit.sentinel",
		"source":      "sentinel",
		"time":        "2026-07-17T10:00:00Z",
		"data": map[string]any{
			"session":  session,
			"user":     user,
			"detector": detector,
			"severity": severity,
			"reason":   reason,
			"evidence": []string{"seq:11", "seq:12"},
			"window":   "60s",
		},
	}
	raw, _ := json.Marshal(ce)
	return string(raw)
}

// TestAuditListSentinel: sentinel verdicts published on the audit stream land
// in the hash chain and come back through /v1/admin/audit, and the ?type= /
// ?session= filters isolate them for the console's sentinel surface. The
// verdicts here are fixtures for the emitter contract.
func TestAuditListSentinel(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	// One ordinary decision, then two verdicts of different severity on two
	// different sessions.
	decide(t, base, adminTok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "echo hi"})
	verdicts := []string{
		sentinelCE("sent-ce-1", "ses-sent-1", user.ID, "denyBurst", "warn", "6 denials in 60s"),
		sentinelCE("sent-ce-2", "ses-sent-2", user.ID, "writeThenExecute", "critical", "wrote /tmp/x.sh then executed it"),
	}
	for _, ce := range verdicts {
		if err := app.bus.Publish(context.Background(), "straza.audit.sentinel", []byte(ce)); err != nil {
			t.Fatal(err)
		}
	}
	waitForAudit(t, app, 3)

	list := func(query string) []audit.Record {
		t.Helper()
		code, body := getJSONAuth(t, base+"/v1/admin/audit?after=0&limit=1000"+query, adminTok)
		if code != http.StatusOK {
			t.Fatalf("audit list %q = %d %s", query, code, body)
		}
		var recs []audit.Record
		if err := json.Unmarshal([]byte(body), &recs); err != nil {
			t.Fatalf("audit list %q: %v", query, err)
		}
		return recs
	}

	// Unfiltered: the sentinel type is returned alongside ordinary audit.
	all := list("")
	if n := countType(all, "straza.audit.sentinel"); n != 2 {
		t.Fatalf("unfiltered list has %d sentinel records, want 2", n)
	}

	// ?type= isolates verdicts (what the console's sentinel surfaces fetch).
	sent := list("&type=straza.audit.sentinel")
	if len(sent) != 2 || countType(sent, "straza.audit.sentinel") != 2 {
		t.Fatalf("type filter returned %d records (%d sentinel), want 2", len(sent), countType(sent, "straza.audit.sentinel"))
	}
	for _, r := range sent {
		if !strings.Contains(r.CE, `"severity"`) || !strings.Contains(r.CE, `"detector"`) {
			t.Errorf("sentinel CE missing contract fields: %s", r.CE)
		}
		if r.Username != "kim" {
			t.Errorf("sentinel record not enriched with username: %q", r.Username)
		}
	}

	// ?session= narrows to one session's verdicts.
	one := list("&type=straza.audit.sentinel&session=ses-sent-2")
	if len(one) != 1 {
		t.Fatalf("session filter returned %d records, want 1", len(one))
	}
	if !strings.Contains(one[0].CE, "writeThenExecute") || !strings.Contains(one[0].CE, `"critical"`) {
		t.Errorf("session filter returned the wrong verdict: %s", one[0].CE)
	}

	// The chain covers verdicts too: the judge is judged by the same hashes.
	recs := list("")
	if ok, broken := audit.Verify(recs, audit.Genesis); !ok {
		t.Fatalf("chain with sentinel verdicts fails verify at seq %d", broken)
	}

	// A client must NOT be able to mint verdicts through the batch endpoint:
	// the type is coerced to straza.audit.tool (forgery guard).
	code, resp := postJSONAuth(t, base+"/v1/audit/batch", adminTok, map[string]any{
		"events": []map[string]any{{
			"type": "straza.audit.sentinel",
			"data": map[string]any{"detector": "denyBurst", "severity": "critical", "reason": "forged", "marker": "forged-verdict"},
		}},
	})
	if code != http.StatusOK || resp["accepted"].(float64) != 1 {
		t.Fatalf("batch = %d %v", code, resp)
	}
	// Wait on the marker, not a record count: the chain also carries authn
	// events (rev 21) whose number this test must not care about.
	waitForCE(t, app, "forged-verdict")
	for _, r := range list("&type=straza.audit.sentinel") {
		if strings.Contains(r.CE, "forged-verdict") {
			t.Fatalf("client-submitted CE kept the sentinel type (forgery hole): %s", r.CE)
		}
	}
	found := false
	for _, r := range list("&type=straza.audit.tool") {
		if strings.Contains(r.CE, "forged-verdict") {
			found = true
		}
	}
	if !found {
		t.Error("client batch event with sentinel type was not coerced to straza.audit.tool")
	}
}

func countType(recs []audit.Record, want string) int {
	n := 0
	for _, r := range recs {
		if ceMeta(r.CE).ceType == want {
			n++
		}
	}
	return n
}

// TestAuditListTypeFilterPrefix: the type filter is a prefix match, so the
// console's coarse chips (straza.audit, straza.revocation, …) work too.
func TestAuditListTypeFilterPrefix(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	decide(t, base, adminTok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "echo hi"})
	if err := app.bus.Publish(context.Background(), "straza.audit.sentinel",
		[]byte(sentinelCE("sent-ce-p", "ses-p", user.ID, "denyBurst", "info", "probe"))); err != nil {
		t.Fatal(err)
	}
	waitForAudit(t, app, 2)

	code, body := getJSONAuth(t, base+"/v1/admin/audit?after=0&limit=1000&type=straza.audit", adminTok)
	if code != http.StatusOK {
		t.Fatalf("audit list = %d %s", code, body)
	}
	var recs []audit.Record
	if err := json.Unmarshal([]byte(body), &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs) < 2 {
		t.Fatalf("prefix filter straza.audit returned %d records, want >= 2", len(recs))
	}
	for _, r := range recs {
		ceType := ceMeta(r.CE).ceType
		if !strings.HasPrefix(ceType, "straza.audit") {
			t.Errorf("prefix filter leaked type %q", ceType)
		}
	}
	// An unmatched filter returns an empty page, not an error.
	code, body = getJSONAuth(t, base+"/v1/admin/audit?after=0&limit=1000&type=straza.nothing", adminTok)
	if code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("unmatched type filter = %d %s, want 200 []", code, body)
	}
}
