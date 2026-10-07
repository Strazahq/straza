package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestCheckinCarriesTypology pins the typology plumbing (spec/policyset
// revision 9): the user row's typology reaches BOTH the server's cached
// subject (gateway decisions) and the checkin response (the client mirrors
// it so local hook decisions match identity-scoped sets: one engine, no
// drift).
func TestCheckinCarriesTypology(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()

	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	u, err := app.store.Users().Create(ctx, store.User{
		Username: "atlas", PasswordHash: hash,
		UserType: store.UserTypeAgent, AgencyMode: store.AgencyAutonomous, SwarmID: "scan-fleet-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	idt := loginDeviceFlow(t, base, "atlas", "hunter2!")
	var enrolled struct {
		DeviceToken string `json:"device_token"`
	}
	code := adminReq(t, "POST", base+"/v1/enroll", "", map[string]any{
		"id_token": idt,
		"device":   map[string]string{"name": "ty-dev", "platform": "linux", "fingerprint": "ty-fp"},
	}, &enrolled)
	if code != http.StatusOK || enrolled.DeviceToken == "" {
		t.Fatalf("enroll = %d", code)
	}
	var resp struct {
		SessionID  string `json:"session_id"`
		UserType   string `json:"user_type"`
		AgencyMode string `json:"agency_mode"`
		SwarmID    string `json:"swarm_id"`
	}
	code = adminReq(t, "POST", base+"/v1/checkin", "", map[string]any{
		"device_token": enrolled.DeviceToken,
		"harness":      map[string]string{"name": "claude-code", "version": "2.1"},
	}, &resp)
	if code != http.StatusOK {
		t.Fatalf("checkin = %d", code)
	}
	if resp.UserType != "agent" || resp.AgencyMode != "autonomous" || resp.SwarmID != "scan-fleet-1" {
		t.Fatalf("checkin response typology = %+v", resp)
	}
	sub, ok := app.subjects.get(resp.SessionID)
	if !ok {
		t.Fatal("no cached subject for the session")
	}
	if sub.UserType != "agent" || sub.AgencyMode != "autonomous" || sub.SwarmID != "scan-fleet-1" {
		t.Fatalf("cached subject typology = %+v", sub)
	}

	// The simulate user lane resolves typology too: an identity-scoped draft
	// must hit the stored user exactly like the live checkin subject would.
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminBearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	draft := `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: sim-identity }
spec:
  match:
    identity: { agencyMode: [autonomous] }
  rules:
    - id: autonomous-no-shell
      tools: [shell.exec]
      effect: deny
      reason: "Straza: autonomous agents get no raw shell"
`
	var sim struct {
		Draft *struct {
			Effect string `json:"effect"`
			RuleID string `json:"ruleId"`
		} `json:"draft"`
	}
	code = adminReq(t, "POST", base+"/v1/admin/policies/simulate", adminBearer, map[string]any{
		"event":   map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "ls"},
		"subject": map[string]any{"user": u.ID},
		"draft":   draft,
	}, &sim)
	if code != http.StatusOK || sim.Draft == nil {
		t.Fatalf("simulate = %d %+v", code, sim)
	}
	if sim.Draft.Effect != "deny" || sim.Draft.RuleID != "autonomous-no-shell" {
		t.Fatalf("simulate draft = %+v, want the identity-scoped deny", sim.Draft)
	}
}
