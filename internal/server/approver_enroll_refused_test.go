package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// wantAdminEnrollRefusal is the 403 sentence of the admin enroll-token
// route, formatted with the self-service page's address.
const wantAdminEnrollRefusal = "Straza enrolls an approver phone or browser only for the person who is signed in and mints the code, so this admin route creates no enroll code for anyone. " +
	"The person signs in to the self-service page at %s and chooses Add a phone, which needs the role straza-enroll-mobile, " +
	"or Enable this browser, which needs the role straza-enroll-browser. Assign those roles in your identity manager, or under Roles in the console when no identity manager feeds Straza."

// TestAdminEnrollTokenRefusesEveryCaller pins that POST
// /v1/admin/approvers/enroll-token mints an enroll token for nobody. An
// admin API token with the approvals area and a straza-admin console
// session each read the 403 sentence that names the self-service page,
// whether the body names another user, the caller, no user or an unknown
// user, or does not parse. Each such request chains exactly one
// enroll-token.refused record, which names the user only when the body
// names one that exists. A token whose scope lacks the approvals area keeps
// the admin plane's refusal and chains no record. The self route stays the
// one mint: it binds the token to the caller whatever user the body names,
// and the store then holds that one enroll token and no other.
func TestAdminEnrollTokenRefusesEveryCaller(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	lin := mkHuman(t, app, "lin", EnrollMobileRole)
	session, _ := checkinToken(t, app, base)
	tokenID, token := mintConnectToken(t, base, session, "iga-pull", "approvals:write")
	_, auditor := mintConnectToken(t, base, session, "auditor", "audit:read")

	reason := "only the person enrolls their own approver phone or browser"
	record := func(actor, actorID, via string, named ...string) map[string]any {
		m := map[string]any{"action": "enroll-token.refused", "reason": reason,
			"actor": actor, "actorId": actorID, "actorVia": via}
		if len(named) == 2 {
			m["target"], m["user"], m["username"] = named[0], named[0], named[1]
		}
		return m
	}
	byToken := func(named ...string) map[string]any { return record("iga-pull", tokenID, "api-token", named...) }
	byKim := func(named ...string) map[string]any { return record("kim", kim.ID, "session", named...) }
	refusal := fmt.Sprintf(wantAdminEnrollRefusal, base+"/self-service/")

	rows := []struct {
		name   string
		bearer string
		body   string
		// wantError is the exact sentence of the 403 body.
		wantError string
		// want is the exact data of the one record the call chains, actor
		// fields included, or nil when the call must chain no record.
		want map[string]any
	}{
		{"an admin API token naming another user", token, `{"username":"lin"}`, refusal, byToken(lin.ID, "lin")},
		{"an admin API token naming an unknown user id", token, `{"user_id":"u-ghost"}`, refusal, byToken()},
		{"a console session naming another user", session, `{"user_id":"` + lin.ID + `"}`, refusal, byKim(lin.ID, "lin")},
		{"a console session naming itself", session, `{"username":"kim"}`, refusal, byKim(kim.ID, "kim")},
		{"a body naming no user", session, `{}`, refusal, byKim()},
		{"a body naming an unknown username", session, `{"username":"ghost"}`, refusal, byKim()},
		{"a malformed body", session, `{"username":"lin"`, refusal, byKim()},
		{"a token without the approvals area", auditor, `{"username":"lin"}`, "token lacks scope approvals:write", nil},
	}
	for _, row := range rows {
		before := len(adminAuditEvents(t, app))
		code, raw, _ := adminBytes(t, http.MethodPost, base+"/v1/admin/approvers/enroll-token", row.bearer, "application/json", []byte(row.body))
		var resp map[string]any
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("%s: the answer is not JSON: %v (%s)", row.name, err, raw)
		}
		if code != http.StatusForbidden || resp["error"] != row.wantError {
			t.Errorf("%s: %d %q, want 403 %q", row.name, code, resp["error"], row.wantError)
		}
		if _, minted := resp["enroll_token"]; minted {
			t.Errorf("%s: the refusal carries an enroll token: %v", row.name, resp)
		}
		events := adminAuditEvents(t, app)
		if row.want == nil {
			if len(events) != before {
				t.Errorf("%s: chained %d admin records, want none: %v", row.name, len(events)-before, events[before:])
			}
			continue
		}
		if len(events) != before+1 {
			t.Errorf("%s: chained %d admin records, want exactly one: %v", row.name, len(events)-before, events[before:])
			continue
		}
		gotJSON, _ := json.Marshal(events[len(events)-1])
		wantJSON, _ := json.Marshal(row.want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s: record\n got %s\nwant %s", row.name, gotJSON, wantJSON)
		}
	}

	// The self route ignores the user fields of its body and mints for the
	// caller, and its record names the caller.
	before := len(adminAuditEvents(t, app))
	var mint selfMintResponse
	if code := adminReq(t, http.MethodPost, base+"/v1/approvals/self/enroll-token", session,
		map[string]any{"channel": "mobile", "user_id": lin.ID, "username": "lin"}, &mint); code != http.StatusOK {
		t.Fatalf("self mint = %d", code)
	}
	if mint.User.ID != kim.ID || mint.EnrollToken == "" {
		t.Errorf("self mint bound to %q, want the caller %q: %+v", mint.User.ID, kim.ID, mint)
	}
	events := adminAuditEvents(t, app)
	want := map[string]any{"action": "enroll-token.create", "target": kim.ID, "user": kim.ID, "username": "kim",
		"channel": "mobile", "self": true, "actor": "kim", "actorId": kim.ID, "actorVia": "session"}
	if gotJSON, wantJSON := mustJSON(events[len(events)-1]), mustJSON(want); len(events) != before+1 || string(gotJSON) != string(wantJSON) {
		t.Errorf("self mint chained %d records, want one\n got %s\nwant %s", len(events)-before, gotJSON, wantJSON)
	}

	// The store holds the self route's token and nothing the refusals made.
	stored, err := app.store.Approvers().PurgeEnrollTokensBefore(context.Background(), time.Now().Add(24*time.Hour))
	if err != nil || stored != 1 {
		t.Errorf("stored enroll tokens = %d (%v), want the self route's one", stored, err)
	}
}
