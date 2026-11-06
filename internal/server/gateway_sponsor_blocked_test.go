package server

import (
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestGatewayBlockedSponsorCredential pins that an agent never runs on the
// own row of a sponsor whom Straza has locked, disabled or deactivated: not
// on the session it held before, which still names the sponsor, and not on
// a session it starts after. Each refusal writes exactly one deny record
// for the agent with the refusal as its reason. An unlock gives the row back.
func TestGatewayBlockedSponsorCredential(t *testing.T) {
	t.Parallel()
	const (
		blocked = "agent joe runs on the tokapp connection of its sponsor alice, and alice is locked or disabled in Straza, so the call is refused. " +
			"An administrator gives joe a credential of its own or a sponsor who is active, or unlocks or enables alice"
		noSponsor = "agent joe has no token for app tokapp and no sponsor whose connection it could use. " +
			"An administrator sets one with strazactl connect tokapp --user joe"
		noConnection = "agent joe has no token for app tokapp, and its sponsor alice has no tokapp connection either. " +
			"alice connects on their credentials page at " + testConnectPage + " and allows agents there, " +
			"or an administrator sets the agent's own with strazactl connect tokapp --user joe"
	)
	lock := func(t *testing.T, base, kimTok string, alice store.User) {
		t.Helper()
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/users/"+alice.ID+"/lock", kimTok, map[string]any{"reason": "suspected compromise"}, nil); code != http.StatusOK {
			t.Fatalf("lock = %d", code)
		}
	}
	cases := []struct {
		name string
		act  func(t *testing.T, base, kimTok string, alice store.User)
		// live and fresh are the refusals of joe's session from before the
		// act and of one he starts after it; empty runs on alice's row.
		live, fresh string
	}{
		{"lock", lock, blocked, blocked},
		{"disable", func(t *testing.T, base, kimTok string, alice store.User) {
			t.Helper()
			if code := adminReq(t, http.MethodPatch, base+"/v1/admin/users/"+alice.ID, kimTok, map[string]any{"status": "disabled"}, nil); code != http.StatusOK {
				t.Fatalf("disable = %d", code)
			}
		}, blocked, noSponsor},
		{"scim deactivation", func(t *testing.T, base, _ string, alice store.User) {
			t.Helper()
			token := mintProvisioningToken(t, base, loginDeviceFlow(t, base, "kim", "hunter2!"))
			scimPatch(t, base, token, alice.ID, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
				"Operations":[{"op":"replace","path":"active","value":false}]}`)
		}, noConnection, noSponsor},
		{"unlock after a lock", func(t *testing.T, base, kimTok string, alice store.User) {
			t.Helper()
			lock(t, base, kimTok, alice)
			if code := adminReq(t, http.MethodPost, base+"/v1/admin/users/"+alice.ID+"/unlock", kimTok, nil, nil); code != http.StatusOK {
				t.Fatalf("unlock = %d", code)
			}
		}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, base, _ := testAppCounting(t)
			up := startGatewayUpstream(t)
			alice := seedGatewayUser(t, app, "alice", "dev")
			joe := seedAgent(t, app, "joe", "alice", "dev")
			seedGatewayUser(t, app, "kim", AdminRole)
			installTokenApp(t, app, up, "tokapp", "sponsor")
			catRecompile(t, app)
			aliceTok := sessionToken(t, base, "alice")
			kimTok, _ := checkinTokenAs(t, base, "console")
			liveTok, liveSID := agentCheckin(t, app, base, "joe")
			if code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", aliceTok, map[string]any{"token": "tok-alice-sponsor"}, nil); code != http.StatusOK {
				t.Fatalf("paste = %d", code)
			}
			if code := adminReq(t, http.MethodPatch, base+"/v1/connect/tokapp", aliceTok, map[string]any{"allow_agents": true}, nil); code != http.StatusOK {
				t.Fatalf("opt-in = %d", code)
			}
			echoCall(t, base, liveTok, "tokapp")
			sponsorRecord := func(sid string) func(map[string]any) bool {
				return func(d map[string]any) bool {
					return d["user"] == joe.ID && d["session"] == sid && d["effect"] == "allow" &&
						d["credentialSource"] == "sponsor" && d["credentialOwner"] == alice.ID
				}
			}
			awaitMCPRecords(t, app, 1, sponsorRecord(liveSID))

			tc.act(t, base, kimTok, alice)
			freshTok, freshSID := agentCheckin(t, app, base, "joe")
			for _, s := range []struct {
				label, tok, sid, want string
				ran                   int
			}{
				{"live", liveTok, liveSID, tc.live, 2},
				{"fresh", freshTok, freshSID, tc.fresh, 1},
			} {
				_, body, raw := mcpCall(t, base, s.tok, "tools/call", map[string]any{
					"name": "tokapp__echo", "arguments": map[string]string{"text": "hi"},
				})
				if s.want == "" {
					if body["error"] != nil {
						t.Fatalf("%s session after %s = %s, want the call to run on alice's row", s.label, tc.name, raw)
					}
					awaitMCPRecords(t, app, s.ran, sponsorRecord(s.sid))
					continue
				}
				rpcErr, _ := body["error"].(map[string]any)
				if rpcErr["message"] != "Straza: the call was not sent to the MCP server tokapp: "+s.want+"." {
					t.Errorf("%s session after %s = %s, want the refusal %q", s.label, tc.name, raw, s.want)
				}
				awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
					return d["user"] == joe.ID && d["session"] == s.sid && d["effect"] == "deny" &&
						d["reason"] == s.want && d["toolName"] == "echo" && d["credentialSource"] == nil
				})
			}
		})
	}
}
