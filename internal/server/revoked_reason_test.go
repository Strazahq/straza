package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// TestRevokedRefusalNamesTheNextStep pins what each denylist hit tells the
// client on the three session-token routes. A session or token hit ends
// only that session, so the refusal says a new session needs no new
// enrollment. A user or device hit judges the identity, so it sends the
// person to an administrator. Every status stays what the kit keys on:
// decide answers a 200 deny, the gateway and the audit batch route a 403.
// Decide also writes exactly one decision record per revoked call.
func TestRevokedRefusalNamesTheNextStep(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)

	const (
		sessionMsg       = "Straza: this session has been revoked. Start a new session: its check-in uses this device's existing enrollment, so re-enrolling is not needed"
		identityMsg      = "Straza: this device or user has been revoked. Contact your administrator"
		sessionAuditMsg  = "Straza: this session has been revoked, so its audit events are refused and stay in the spool. Start a new session: its check-in uses this device's existing enrollment, and the spool uploads under it"
		identityAuditMsg = "Straza: this device or user has been revoked, so its audit events are refused. Contact your administrator"
	)
	// The user hit runs last: it blocks every later session of kim.
	hits := []struct {
		name               string
		revoke             func(authn.Claims)
		wantMsg, wantAudit string
	}{
		{"session", func(c authn.Claims) { app.denylist.revokeSession(c.Session) }, sessionMsg, sessionAuditMsg},
		{"token", func(c authn.Claims) {
			app.denylist.mu.Lock()
			app.denylist.jtis[c.JTI] = true
			app.denylist.mu.Unlock()
		}, sessionMsg, sessionAuditMsg},
		{"device", func(c authn.Claims) { app.denylist.RevokeDevice(c.Device) }, identityMsg, identityAuditMsg},
		{"user", func(c authn.Claims) { app.denylist.revokeUser(c.Subject) }, identityMsg, identityAuditMsg},
	}
	ls := map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "ls"}
	batch := map[string]any{"events": []map[string]any{
		{"data": map[string]any{"event": "tool.pre", "tool": "shell.exec", "command": "spooled"}},
	}}
	revokedSessions := map[string]string{} // session id -> the reason decide gave
	for _, hit := range hits {
		t.Run(hit.name, func(t *testing.T) {
			_, deviceToken := mintDevice(t, app, kim.ID, store.DeviceClientKit, "sha256:revoked-"+hit.name)
			code, body := checkinDeviceAs(t, base, deviceToken, "claude-code", "2.1.0")
			if code != http.StatusOK {
				t.Fatalf("device checkin = %d %v", code, body)
			}
			token := body["session_token"].(string)
			claims, err := app.tokens.Verify(token)
			if err != nil || claims.Device == "" || claims.JTI == "" {
				t.Fatalf("claims = %+v, %v: the table needs a device and a token id", claims, err)
			}
			// Positive control: the same token passes all three routes first.
			if code, dec := decide(t, base, token, ls); code != http.StatusOK || dec["effect"] != "allow" {
				t.Fatalf("decide before the revoke = %d %v", code, dec)
			}
			if code, _, raw := mcpCall(t, base, token, "ping", nil); code != http.StatusOK {
				t.Fatalf("gateway before the revoke = %d %s", code, raw)
			}
			if code, resp := postJSONAuth(t, base+"/v1/audit/batch", token, batch); code != http.StatusOK {
				t.Fatalf("audit batch before the revoke = %d %v", code, resp)
			}

			hit.revoke(claims)

			code, dec := decide(t, base, token, ls)
			if code != http.StatusOK || dec["effect"] != "deny" || dec["ruleId"] != "revoked" || dec["reason"] != hit.wantMsg {
				t.Errorf("decide = %d %v, want 200 deny revoked %q", code, dec, hit.wantMsg)
			}
			revokedSessions[claims.Session] = hit.wantMsg
			code, gw, raw := mcpCall(t, base, token, "ping", nil)
			if code != http.StatusForbidden || gw["error"] != hit.wantMsg {
				t.Errorf("gateway = %d %s, want 403 %q", code, raw, hit.wantMsg)
			}
			code, resp := postJSONAuth(t, base+"/v1/audit/batch", token, batch)
			if code != http.StatusForbidden || resp["error"] != hit.wantAudit {
				t.Errorf("audit batch = %d %v, want 403 %q", code, resp, hit.wantAudit)
			}
		})
	}

	// A decision from another user is spooled after every revoked one, and
	// the spool drains in order, so once it lands the count is final.
	lee, err := app.store.Users().Create(context.Background(), store.User{Username: "lee"})
	if err != nil {
		t.Fatal(err)
	}
	_, leeDevice := mintDevice(t, app, lee.ID, store.DeviceClientKit, "sha256:revoked-marker")
	code, body := checkinDeviceAs(t, base, leeDevice, "claude-code", "2.1.0")
	if code != http.StatusOK {
		t.Fatalf("marker checkin = %d %v", code, body)
	}
	markerSession := body["session_id"].(string)
	if code, dec := decide(t, base, body["session_token"].(string), ls); code != http.StatusOK {
		t.Fatalf("marker decide = %d %v", code, dec)
	}
	records := decisionRecordsUntil(t, app, markerSession)
	for session, reason := range revokedSessions {
		var got []map[string]any
		for _, d := range records {
			if d["session"] == session && d["ruleId"] == "revoked" {
				got = append(got, d)
			}
		}
		if len(got) != 1 {
			t.Errorf("session %s: %d revoked decision records, want exactly 1: %v", session, len(got), got)
			continue
		}
		if d := got[0]; d["user"] != kim.ID || d["effect"] != "deny" || d["reason"] != reason {
			t.Errorf("session %s: record %v, want user %s, a deny and the reason %q", session, d, kim.ID, reason)
		}
	}
}

// decisionRecordsUntil waits until a straza.audit.tool record of the marker
// session reaches the outbox and returns the data of every such record.
func decisionRecordsUntil(t *testing.T, app *App, markerSession string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		recent, err := app.store.Outbox().ListRecent(context.Background(), 500)
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]any
		seen := false
		for _, e := range recent {
			if e.Subject != "straza.audit.tool" {
				continue
			}
			var ce struct {
				Data map[string]any `json:"data"`
			}
			if json.Unmarshal([]byte(e.CE), &ce) != nil {
				continue
			}
			out = append(out, ce.Data)
			seen = seen || ce.Data["session"] == markerSession
		}
		if seen {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("the marker decision of session %s never reached the outbox", markerSession)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
