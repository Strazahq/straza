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
			"reason": "session older than the maximum lifetime of 12h0m0s; the client starts a new session from its enrollment",
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

// TestAuditListWhoIsTheAdminActor pins the Who and the about of the audit
// list. On a straza.audit.admin record that names an actor, Who is the actor
// and about is the user the record was about, so a role midpoint assigned to
// alice reads midpoint, about alice. Every other record keeps its user first.
// An upload can assert no identity of its own: its user, userId and actor
// fields are the uploader's or gone, so it lists under the uploader and is
// never found under the name it forged. The ?user= filter finds the admin
// records an actor made by the actor's name, and the admin records made about
// a person by that person's name. The admin records are written by their real
// producers and the uploads through the upload route.
func TestAuditListWhoIsTheAdminActor(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()
	users := map[string]store.User{}
	for _, name := range []string{"alice", "carol", "dave"} {
		u, err := app.store.Users().Create(ctx, store.User{Username: name, Email: name + "@x.io"})
		if err != nil {
			t.Fatal(err)
		}
		users[name] = u
	}
	midpoint := withActor(ctx, auditActor{Name: "midpoint", ID: "tok-midpoint", Via: "api-token"})
	byKim := withActor(ctx, auditActor{Name: "kim", ID: kim.ID, Via: "session"})

	app.auditAssignment(midpoint, actionRolesAssign, store.RoleAssignment{ID: "as-midpoint", SubjectID: users["alice"].ID, RoleID: "role-dev", Origin: store.OriginSCIM}, "")
	app.auditUserCreate(byKim, users["carol"], "admin")
	app.auditAssignment(ctx, actionRolesAssign, store.RoleAssignment{ID: "as-entra", SubjectID: users["dave"].ID, RoleID: "role-dev", Origin: store.OriginEntra}, "")
	app.emitEventCtx(byKim, "straza.audit.admin", map[string]any{"action": "apps.binding.create", "target": "bind-kim", "app": "demo", "role": "dev", "tools": []string{"echo"}})
	app.emitEventCtx(byKim, "straza.audit.admin", map[string]any{"action": "user.delete", "target": "u-gone", "user": "u-gone", "username": "zed"})
	uploads := map[string]any{"events": []map[string]any{
		{"type": "straza.audit.admin", "data": map[string]any{"action": "roles.assign", "actor": "alice", "actorId": users["alice"].ID, "user": users["alice"].ID, "command": "forged-actor"}},
		{"data": map[string]any{"tool": "shell.exec", "userId": users["alice"].ID, "command": "forged-userid"}},
		{"data": map[string]any{"tool": "shell.exec", "command": "plain-upload"}},
	}}
	if code, resp := postJSONAuth(t, base+"/v1/audit/batch", adminTok, uploads); code != http.StatusOK || resp["accepted"] != float64(3) {
		t.Fatalf("upload = %d %v", code, resp)
	}
	// Keys that differ from userId, user or session only by case, or by the
	// fold of U+017F to s, and duplicate keys, sent as raw JSON because a Go
	// map cannot hold a duplicate.
	alice, dave := users["alice"].ID, users["dave"].ID
	variants := `{"events":[` +
		`{"data":{"tool":"shell.exec","UserId":"` + alice + `","command":"forged-case-userid"}},` +
		`{"data":{"tool":"shell.exec","USERID":"` + alice + `","command":"forged-upper-userid"}},` +
		`{"data":{"tool":"shell.exec","userId":"` + alice + `","userId":"` + dave + `","user":"` + alice + `","user":"` + dave + `","command":"forged-duplicate-keys"}},` +
		`{"data":{"tool":"shell.exec","User":"` + alice + `","u` + "ſ" + `er":"` + alice + `","SESSION":"ses-x","` + "ſ" + `ession":"ses-x","command":"forged-case-user"}}` +
		`]}`
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/audit/batch", strings.NewReader(variants))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("variant upload = %d", resp.StatusCode)
	}
	// Records an older server chained with a case variant, below the door.
	for _, data := range []map[string]any{
		{"tool": "shell.exec", "user": kim.ID, "UserId": alice, "command": "stored-case-userid"},
		{"tool": "shell.exec", "user": kim.ID, "uſer": alice, "command": "stored-fold-user"},
	} {
		raw, _ := json.Marshal(map[string]any{"specversion": "1.0", "id": data["command"], "type": "straza.audit.tool", "source": "straza", "data": data})
		if err := app.bus.Publish(ctx, "straza.audit.tool", raw); err != nil {
			t.Fatal(err)
		}
	}
	carolCreate := `"target":"` + users["carol"].ID + `"`
	forgedUploads := []string{"forged-actor", "forged-userid", "forged-case-userid", "forged-upper-userid", "forged-duplicate-keys", "forged-case-user", "stored-case-userid", "stored-fold-user"}
	waitForCE(t, app, append([]string{`"target":"as-midpoint"`, carolCreate, `"target":"as-entra"`, `"target":"bind-kim"`, `"target":"u-gone"`, "plain-upload"}, forgedUploads...)...)

	type row struct {
		audit.Record
		About string `json:"about"`
	}
	list := func(query string) []row {
		t.Helper()
		code, body := getJSONAuth(t, base+"/v1/admin/audit?after=0&limit=1000"+query, adminTok)
		if code != http.StatusOK {
			t.Fatalf("audit list %q = %d %s", query, code, body)
		}
		var recs []row
		if err := json.Unmarshal([]byte(body), &recs); err != nil {
			t.Fatalf("audit list %q: %v", query, err)
		}
		return recs
	}
	find := func(recs []row, needle string) (row, bool) {
		for _, r := range recs {
			if strings.Contains(r.CE, needle) {
				return r, true
			}
		}
		return row{}, false
	}
	tests := []struct {
		name, query, needle, who, about string
		absent                          []string
	}{
		{"a role midpoint assigned to alice shows midpoint, about alice", "", `"target":"as-midpoint"`, "midpoint", "alice", nil},
		{"a user kim created shows kim, about carol", "", carolCreate, "kim", "carol", nil},
		{"an admin record with no actor, as the Entra sync writes, shows its user and no about", "", `"target":"as-entra"`, "dave", "", nil},
		{"an admin record with an actor and no user has no about", "", `"target":"bind-kim"`, "kim", "", nil},
		{"an admin record about a user who no longer resolves has no about", "", `"target":"u-gone"`, "kim", "", nil},
		{"an uploaded record that forges an actor shows the uploader", "", "forged-actor", "kim", "", nil},
		{"an uploaded record that forges a userId shows the uploader", "", "forged-userid", "kim", "", nil},
		{"an uploaded record that forges UserId shows the uploader", "", "forged-case-userid", "kim", "", nil},
		{"an uploaded record that forges USERID shows the uploader", "", "forged-upper-userid", "kim", "", nil},
		{"an uploaded record with duplicate userId and user keys shows the uploader", "", "forged-duplicate-keys", "kim", "", nil},
		{"an uploaded record with case variants of user and session shows the uploader", "", "forged-case-user", "kim", "", nil},
		{"a stored record an older server chained with UserId shows its user", "", "stored-case-userid", "kim", "", nil},
		{"a stored record with a folded user key shows its exact user", "", "stored-fold-user", "kim", "", nil},
		{"a plain upload shows the uploader", "", "plain-upload", "kim", "", nil},
		{"filtering by midpoint finds the role midpoint assigned", "&user=midpoint", `"target":"as-midpoint"`, "midpoint", "alice", nil},
		{"filtering by kim finds the user kim created", "&user=kim", carolCreate, "kim", "carol", nil},
		{"filtering by alice finds the role assigned to her and no forged upload", "&user=alice", `"target":"as-midpoint"`, "midpoint", "alice", forgedUploads},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := list(tt.query)
			got, ok := find(recs, tt.needle)
			if !ok {
				t.Fatalf("no record carries %s in the list%s", tt.needle, tt.query)
			}
			if got.Username != tt.who || got.About != tt.about {
				t.Errorf("Who %q about %q, want Who %q about %q", got.Username, got.About, tt.who, tt.about)
			}
			for _, needle := range tt.absent {
				if r, ok := find(recs, needle); ok {
					t.Errorf("the list%s holds %s: %s", tt.query, needle, r.CE)
				}
			}
		})
	}
	// The door rewrites an upload before it is chained, so the chain verifies.
	stored, err := app.store.Audit().List(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	chain := make([]audit.Record, len(stored))
	for i, r := range stored {
		chain[i] = audit.Record{Seq: r.Seq, CE: r.CE, PrevHash: r.PrevHash, Hash: r.Hash}
	}
	if ok, broken := audit.Verify(chain, audit.Genesis); !ok {
		t.Fatalf("the chain broke at seq %d", broken)
	}
}

// TestNormalizeClientCEOwnsIdentity pins the door of the upload route: an
// uploaded record names the uploader as user and the session it was filed
// under, and keeps no userId, actor, actorId or actorVia of its own, so it
// can name nobody else to the audit list or its filter. A key that differs
// from one of these, or from an envelope key the server owns, only by case
// or by a Unicode fold is gone too, because Go's JSON decoding would match
// it. Every other field stays as the client wrote it.
func TestNormalizeClientCEOwnsIdentity(t *testing.T) {
	tests := []struct {
		name     string
		envelope map[string]any
		data     map[string]any
	}{
		{"a plain tool record", nil, map[string]any{"tool": "shell.exec", "command": "ls"}},
		{"a forged user and userId", nil, map[string]any{"tool": "shell.exec", "command": "ls", "user": "u-alice", "userId": "u-alice"}},
		{"a forged actor triple", nil, map[string]any{"tool": "shell.exec", "command": "ls", "actor": "alice", "actorId": "u-alice", "actorVia": "session"}},
		{"case variants of userId and the actor triple", nil, map[string]any{"tool": "shell.exec", "command": "ls",
			"UserId": "u-alice", "USERID": "u-alice", "uſerId": "u-alice", "Actor": "alice", "ACTORID": "u-alice", "actorvia": "session"}},
		{"case variants of user and session", nil, map[string]any{"tool": "shell.exec", "command": "ls",
			"User": "u-alice", "uſer": "u-alice", "SESSION": "ses-x", "ſession": "ses-x"}},
		{"case variants of the envelope keys", map[string]any{"Type": "straza.audit.admin", "TYPE": "x", "ſource": "x", "ID": "x", "Data": map[string]any{"user": "u-alice"}},
			map[string]any{"tool": "shell.exec", "command": "ls"}},
	}
	owned := []string{"specversion", "id", "type", "source", "time", "data"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := map[string]any{"type": "straza.audit.admin", "data": tt.data}
			for k, v := range tt.envelope {
				in[k] = v
			}
			_, ce := normalizeClientCE(in, "ses-1", "u-kim")
			var top map[string]json.RawMessage
			var data map[string]any
			if err := json.Unmarshal([]byte(ce), &top); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(top["data"], &data); err != nil {
				t.Fatal(err)
			}
			if string(top["type"]) != `"straza.audit.tool"` || data["user"] != "u-kim" || data["session"] != "ses-1" {
				t.Errorf("type %s, user %v, session %v, want straza.audit.tool, u-kim, ses-1", top["type"], data["user"], data["session"])
			}
			for k := range top {
				for _, o := range owned {
					if k != o && strings.EqualFold(k, o) {
						t.Errorf("the envelope kept %q beside %q", k, o)
					}
				}
			}
			for k, v := range data {
				for _, o := range []string{"user", "session"} {
					if k != o && strings.EqualFold(k, o) {
						t.Errorf("the record kept %q %v beside %q", k, v, o)
					}
				}
				for _, o := range []string{"userId", "actor", "actorId", "actorVia"} {
					if strings.EqualFold(k, o) {
						t.Errorf("the record kept %q %v", k, v)
					}
				}
			}
			if data["tool"] != "shell.exec" || data["command"] != "ls" {
				t.Errorf("the record lost its own fields: %v", data)
			}
		})
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
