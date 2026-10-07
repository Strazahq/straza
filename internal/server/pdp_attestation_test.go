package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestDecideEnforcesAttestationMinimum pins that /v1/decide refuses a token
// below governance.minAttestation the way the gateway does, so the check-in
// exemption of the human clients is no decide bypass: the answer is an
// audited deny, and a managed kit session still gets policy's answer.
func TestDecideEnforcesAttestationMinimum(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Governance.MinAttestation = config.AttestationManaged
	})
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	checkin := func(harness string, managed bool, hashes map[string]string) (string, string) {
		t.Helper()
		code, body := postJSON(t, base+"/v1/checkin", map[string]any{
			"id_token": idToken,
			"harness":  map[string]string{"name": harness, "version": "2.1.0"},
			"attestation": map[string]any{
				"managed": managed, "platform": "linux/amd64", "hashes": hashes,
			},
		})
		if code != http.StatusOK {
			t.Fatalf("%s checkin = %d %v", harness, code, body)
		}
		return body["session_token"].(string), body["session_id"].(string)
	}
	event := map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "ls"}

	// The console is exempt at check-in and holds a token below the minimum.
	consoleTok, consoleSes := checkin("console", false, map[string]string{"self": "sha256:aa11"})
	code, dec := decide(t, base, consoleTok, event)
	if code != http.StatusOK || dec["effect"] != "deny" || dec["ruleId"] != "attestation" {
		t.Fatalf("console decide = %d %v, want an attestation deny", code, dec)
	}
	reason, _ := dec["reason"].(string)
	for _, want := range []string{`"advisory"`, `"managed"`, "`straza install --managed <harness>`"} {
		if !strings.Contains(reason, want) {
			t.Errorf("deny reason %q must say %s", reason, want)
		}
	}

	// The chain shows the refusal once, for this session and person.
	deadline := time.Now().Add(15 * time.Second)
	var refusals int
	for time.Now().Before(deadline) {
		pending, _ := app.store.Outbox().ListUnpublished(context.Background(), 200)
		refusals = 0
		for _, e := range pending {
			if e.Subject == "straza.audit.tool" && strings.Contains(e.CE, `"ruleId":"attestation"`) {
				refusals++
				for _, want := range []string{`"session":"` + consoleSes + `"`, `"user":"` + kim.ID + `"`, `"effect":"deny"`, `"reason":"Straza: attestation level`} {
					if !strings.Contains(e.CE, want) {
						t.Errorf("audit record %s must carry %s", e.CE, want)
					}
				}
			}
		}
		if refusals >= 1 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if refusals != 1 {
		t.Fatalf("attestation refusals audited = %d, want exactly 1", refusals)
	}

	// A managed kit session is at the minimum and gets policy's answer.
	ctx := context.Background()
	for artifact, h := range map[string]string{"self": "sha256:aa11", "hooks.claude-code": "sha256:cc33"} {
		row := store.AttestationHash{Artifact: artifact, Hash: h, Platform: "linux/amd64"}
		if artifact != "self" {
			row.Harness = "claude-code"
		}
		if _, err := app.store.AttestationHashes().Create(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	kitTok, _ := checkin("claude-code", true, map[string]string{"self": "sha256:aa11", "hooks.claude-code": "sha256:cc33"})
	if code, dec := decide(t, base, kitTok, event); code != http.StatusOK || dec["ruleId"] == "attestation" {
		t.Fatalf("managed kit decide = %d %v, want policy's answer", code, dec)
	}
}
