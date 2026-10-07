package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/audit"
	"github.com/strazahq/straza/internal/store"
)

// auditToolCE builds a chain-ready straza.audit CloudEvent fixture carrying
// an optional decision effect (empty = the field is absent, the shape of
// admin/identity records that carry no decision at all).
func auditToolCE(id, ceType, command, effect string) string {
	data := map[string]any{"session": "ses-af-1", "command": command}
	if effect != "" {
		data["effect"] = effect
	}
	ce := map[string]any{
		"specversion": "1.0",
		"id":          id,
		"type":        ceType,
		"source":      "strazad",
		"data":        data,
	}
	raw, _ := json.Marshal(ce)
	return string(raw)
}

// TestAuditListServerSideFilters pins the /v1/admin/audit q= and effect=
// contract: both are applied in the STORE before the limit, so "what was
// denied yesterday" is answerable past the fetched window. q is a
// case-insensitive substring over the raw CE text, effect admits exactly
// allow|deny (else 400) and excludes records carrying no decision effect,
// and both compose with the existing ?type= filter and order=desc.
func TestAuditListServerSideFilters(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	fixtures := []struct{ id, ceType, command, effect string }{
		{"af-ce-1", "straza.audit.tool", "kubectl get pods", "allow"},
		{"af-ce-2", "straza.audit.tool", "rm -rf /tmp/x", "deny"},
		{"af-ce-3", "straza.audit.mcp", "rm -rf via mcp", "deny"},
		{"af-ce-4", "straza.audit.sentinel", "no decision here", ""},
	}
	for _, f := range fixtures {
		ce := auditToolCE(f.id, f.ceType, f.command, f.effect)
		if err := app.bus.Publish(context.Background(), f.ceType, []byte(ce)); err != nil {
			t.Fatal(err)
		}
	}
	waitForAudit(t, app, len(fixtures))

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
	countNeedle := func(recs []audit.Record, needle string) int {
		n := 0
		for _, r := range recs {
			if strings.Contains(r.CE, needle) {
				n++
			}
		}
		return n
	}

	// q reaches every matching record server-side, case-insensitively.
	got := list("&q=RM+-RF")
	if len(got) != 2 || countNeedle(got, "rm -rf") != 2 {
		t.Fatalf("q=RM+-RF returned %d records (%d matching), want 2", len(got), countNeedle(got, "rm -rf"))
	}

	// effect narrows to the decision effect; the no-effect identity record
	// appears under neither value.
	if got := list("&effect=deny"); len(got) != 2 || countNeedle(got, `"effect":"deny"`) != 2 {
		t.Fatalf("effect=deny returned %d records, want the 2 denies", len(got))
	}
	if got := list("&effect=allow"); len(got) != 1 || countNeedle(got, "af-ce-1") != 1 {
		t.Fatalf("effect=allow returned %d records, want exactly af-ce-1", len(got))
	}

	// Anything else is refused, not ignored.
	if code, body := getJSONAuth(t, base+"/v1/admin/audit?effect=bogus", adminTok); code != http.StatusBadRequest {
		t.Fatalf("effect=bogus = %d %s, want 400", code, body)
	}

	// q composes with the existing type filter (type stays a post-fetch
	// narrowing of the q-matched page).
	if got := list("&q=rm+-rf&type=straza.audit.mcp"); len(got) != 1 || countNeedle(got, "af-ce-3") != 1 {
		t.Fatalf("q+type returned %d records, want exactly af-ce-3", len(got))
	}

	// order=desc serves the newest window of matches.
	code, body := getJSONAuth(t, base+"/v1/admin/audit?order=desc&limit=1&effect=deny", adminTok)
	if code != http.StatusOK {
		t.Fatalf("desc+effect = %d %s", code, body)
	}
	var newest []audit.Record
	if err := json.Unmarshal([]byte(body), &newest); err != nil {
		t.Fatal(err)
	}
	if len(newest) != 1 || countNeedle(newest, "af-ce-3") != 1 {
		t.Fatalf("desc limit=1 effect=deny returned %v, want the newest deny af-ce-3", len(newest))
	}
}

// TestAuditListResolvesUserID pins the Who column for records that carry
// the user under data.userId alone, the janitor's session ends (spec/events
// rev 21 and 28): the list resolves the username from that id and the
// ?user= filter finds the record by name and by id. The CE itself stays as
// written, since the chain hash covers its bytes.
func TestAuditListResolvesUserID(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app) // "kim"
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)
	carol, err := app.store.Users().Create(context.Background(),
		store.User{Username: "carol", Email: "carol@x.io"})
	if err != nil {
		t.Fatal(err)
	}
	janitorCE, _ := json.Marshal(map[string]any{
		"specversion": "1.0", "id": "uid-ce-1", "type": "straza.audit.authn", "source": "strazad",
		"data": map[string]any{
			"action": "session.end", "outcome": "lifetime-closed", "userId": carol.ID, "session": "ses-uid-1",
			"reason": "session older than the maximum lifetime of 12h0m0s; the client starts a new session from its device credential",
		},
	})
	if err := app.bus.Publish(context.Background(), "straza.audit.authn", janitorCE); err != nil {
		t.Fatal(err)
	}
	if err := app.bus.Publish(context.Background(), "straza.audit.tool", []byte(auditToolCE("uid-ce-2", "straza.audit.tool", "ls", "allow"))); err != nil {
		t.Fatal(err)
	}
	waitForAudit(t, app, 2)

	list := func(query string) []audit.Record {
		t.Helper()
		code, body := getJSONAuth(t, base+"/v1/admin/audit?after=0&limit=100"+query, adminTok)
		if code != http.StatusOK {
			t.Fatalf("audit list %q = %d %s", query, code, body)
		}
		var recs []audit.Record
		if err := json.Unmarshal([]byte(body), &recs); err != nil {
			t.Fatalf("audit list %q: %v", query, err)
		}
		return recs
	}
	// The admin's own check-in wrote a login record beside the two published
	// here, so the rows are picked by id, not counted.
	seen := false
	for _, r := range list("") {
		if strings.Contains(r.CE, "uid-ce-1") {
			seen = true
			if r.Username != "carol" {
				t.Errorf("janitor record username = %q, want carol", r.Username)
			}
		}
		if strings.Contains(r.CE, "uid-ce-2") && r.Username != "" {
			t.Errorf("tool record without a user resolved to %q, want blank", r.Username)
		}
		if strings.Contains(r.CE, `"user"`) && strings.Contains(r.CE, "uid-ce-1") {
			t.Errorf("the CE was rewritten: %s", r.CE)
		}
	}
	if !seen {
		t.Fatal("the janitor record is missing from the list")
	}
	for _, q := range []string{"&user=carol", "&user=" + carol.ID} {
		got := list(q)
		if len(got) != 1 || !strings.Contains(got[0].CE, "uid-ce-1") || got[0].Username != "carol" {
			t.Errorf("%s returned %d records, want the janitor record named carol", q, len(got))
		}
	}
}

// TestAuditListResolvesDecidedBy pins the read-time enrichment: an
// approval-resolution CE carries data.decidedBy (a user ID); the list
// endpoint resolves it to decidedByUsername on the response envelope (the
// same read-time mechanism as the existing username field; the CE itself is
// never touched because the chain hash covers CE bytes). Records without a
// decidedBy omit the field.
func TestAuditListResolvesDecidedBy(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	requester := seedIdentity(t, app) // "kim"
	grantAdmin(t, app, requester.ID)
	adminTok, _ := checkinToken(t, app, base)

	decider, err := app.store.Users().Create(context.Background(),
		store.User{Username: "alice", Email: "alice@x.io"})
	if err != nil {
		t.Fatal(err)
	}

	approvalCE, _ := json.Marshal(map[string]any{
		"specversion": "1.0", "id": "ap-ce-1", "type": "straza.audit.approval",
		"source": "strazad",
		"data": map[string]any{
			"phase": "resolution", "state": "approved", "approvalId": "ap-1",
			"user": requester.ID, "decidedBy": decider.ID,
			"rule": "dev-echo-approval-showcase", "summary": "shell.exec: date",
			"decidedReason": "expected demo traffic",
		},
	})
	if err := app.bus.Publish(context.Background(), "straza.audit.approval", approvalCE); err != nil {
		t.Fatal(err)
	}
	toolCE := auditToolCE("ap-ce-2", "straza.audit.tool", "git status", "allow")
	if err := app.bus.Publish(context.Background(), "straza.audit.tool", []byte(toolCE)); err != nil {
		t.Fatal(err)
	}
	waitForAudit(t, app, 2)

	code, body := getJSONAuth(t, base+"/v1/admin/audit?after=0&limit=100", adminTok)
	if code != http.StatusOK {
		t.Fatalf("audit list = %d %s", code, body)
	}
	var rows []struct {
		audit.Record
		DecidedByUsername string `json:"decidedByUsername"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatal(err)
	}
	var sawApproval, sawTool bool
	for _, r := range rows {
		switch {
		case strings.Contains(r.CE, "ap-ce-1"):
			sawApproval = true
			if r.DecidedByUsername != "alice" {
				t.Errorf("approval row decidedByUsername = %q, want alice", r.DecidedByUsername)
			}
			if r.Username != "kim" {
				t.Errorf("approval row username = %q, want the requester kim", r.Username)
			}
		case strings.Contains(r.CE, "ap-ce-2"):
			sawTool = true
			if r.DecidedByUsername != "" {
				t.Errorf("tool row decidedByUsername = %q, want omitted", r.DecidedByUsername)
			}
			if !strings.Contains(body, `"decidedByUsername":"alice"`) {
				t.Errorf("raw body lacks decidedByUsername field: %s", body)
			}
		}
	}
	if !sawApproval || !sawTool {
		t.Fatalf("fixtures missing from list (approval=%v tool=%v)", sawApproval, sawTool)
	}
}

// auditUserCE builds a chain-ready CloudEvent fixture attributed to one user
// by data.user, with an optional decision effect.
func auditUserCE(id, ceType, userID, effect string) string {
	data := map[string]any{"user": userID, "session": "ses-au-" + userID}
	if effect != "" {
		data["effect"] = effect
	}
	raw, _ := json.Marshal(map[string]any{"specversion": "1.0", "id": id, "type": ceType, "source": "strazad", "data": data})
	return string(raw)
}

// TestAuditListUserFilterLandsBeforeTheLimit pins the ?user= contract behind
// audit tail --user: the filter is applied in the store before the limit, so
// a quiet user's decision and identity records stay in the newest window
// when busier sessions fill it, whether the value is the id or the username.
func TestAuditListUserFilterLandsBeforeTheLimit(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	before := len(waitForAudit(t, app, 1))

	quiet := []string{
		auditUserCE("au-quiet-1", "straza.audit.tool", user.ID, "deny"),
		auditUserCE("au-quiet-2", "straza.identity.updated", user.ID, ""),
	}
	for _, ce := range quiet {
		if err := app.bus.Publish(context.Background(), "straza.audit.tool", []byte(ce)); err != nil {
			t.Fatal(err)
		}
	}
	const busy = 60
	for i := 0; i < busy; i++ {
		ce := auditUserCE("au-busy-"+strconv.Itoa(i), "straza.audit.tool", "u-busy", "allow")
		if err := app.bus.Publish(context.Background(), "straza.audit.tool", []byte(ce)); err != nil {
			t.Fatal(err)
		}
	}
	waitForAudit(t, app, before+len(quiet)+busy)

	list := func(query string) []audit.Record {
		t.Helper()
		code, body := getJSONAuth(t, base+"/v1/admin/audit?"+query, adminTok)
		if code != http.StatusOK {
			t.Fatalf("audit list %q = %d %s", query, code, body)
		}
		var recs []audit.Record
		if err := json.Unmarshal([]byte(body), &recs); err != nil {
			t.Fatalf("audit list %q: %v", query, err)
		}
		return recs
	}
	has := func(recs []audit.Record, id string) bool {
		for _, r := range recs {
			if strings.Contains(r.CE, `"id":"`+id+`"`) {
				return true
			}
		}
		return false
	}
	tests := []struct {
		name  string
		query string
		want  bool
	}{
		{"newest window without a user filter has lost the quiet rows", "order=desc&limit=50", false},
		{"newest window filtered by username keeps them", "order=desc&limit=50&user=" + user.Username, true},
		{"newest window filtered by id keeps them", "order=desc&limit=50&user=" + user.ID, true},
		{"ascending page filtered by username keeps them", "after=0&limit=1000&user=" + user.Username, true},
		{"an unknown principal matches nothing", "order=desc&limit=50&user=nobody-here", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := list(tt.query)
			for _, id := range []string{"au-quiet-1", "au-quiet-2"} {
				if got := has(recs, id); got != tt.want {
					t.Errorf("%s: %s present = %v, want %v (%d rows)", tt.query, id, got, tt.want, len(recs))
				}
			}
			if strings.Contains(tt.query, "user=") && has(recs, "au-busy-1") {
				t.Errorf("%s: a busy user's row leaked into the filtered page", tt.query)
			}
		})
	}
}
