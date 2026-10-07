package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// waitForCE polls the audit chain until every want string appears in some
// record's CE (one record per want, matched independently), or fails.
func waitForCE(t *testing.T, app *App, wants ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		recs, err := app.store.Audit().List(context.Background(), 0, 1000)
		if err == nil {
			missing := ""
			for _, w := range wants {
				found := false
				for _, r := range recs {
					if strings.Contains(r.CE, w) {
						found = true
						break
					}
				}
				if !found {
					missing = w
					break
				}
			}
			if missing == "" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("audit chain never carried %q (%d records)", missing, len(recs))
			}
		} else if time.Now().After(deadline) {
			t.Fatalf("audit list: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// TestAuditToolCarriesSetName pins the revision-20 minimum on the hook-PDP
// writer: a rule-decided CE names the deciding SET, and a profile-default
// decision carries setName "" exactly like its empty ruleId (empty means no
// rule decided; the field is always present from revision 20).
func TestAuditToolCarriesSetName(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", adminTok, "application/yaml", []byte(rmPolicy)); code != http.StatusCreated {
		t.Fatalf("apply = %d %s", code, b)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/block-rm/activate", adminTok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}

	token, _ := checkinToken(t, app, base)
	if code, dec := decide(t, base, token, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x"}); code != http.StatusOK || dec["ruleId"] != "no-rm-rf" {
		t.Fatalf("rule-decided decide = %d %v", code, dec)
	}
	// The decide RESPONSE omits an empty ruleId (Decision json omitempty);
	// the audit CE is the surface where the empty field must be PRESENT.
	if code, dec := decide(t, base, token, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "ls"}); code != http.StatusOK || (dec["ruleId"] != nil && dec["ruleId"] != "") {
		t.Fatalf("default decide = %d %v, want no rule", code, dec)
	}

	// The rule-decided CE names the set; the default CE carries the empty
	// field (present, not omitted): the same honesty contract as ruleId.
	waitForCE(t, app, `"command":"rm -rf /tmp/x"`, `"command":"ls"`)
	recs, err := app.store.Audit().List(context.Background(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var ruleCE, defCE string
	for _, r := range recs {
		if strings.Contains(r.CE, `"command":"rm -rf /tmp/x"`) {
			ruleCE = r.CE
		}
		if strings.Contains(r.CE, `"command":"ls"`) {
			defCE = r.CE
		}
	}
	if !strings.Contains(ruleCE, `"setName":"block-rm"`) {
		t.Errorf("rule-decided CE misses setName block-rm:\n%s", ruleCE)
	}
	if !strings.Contains(defCE, `"setName":""`) {
		t.Errorf("default-decision CE misses the empty setName:\n%s", defCE)
	}
}

// TestAuditBatchPreservesSetName pins the client lane: a spooled CE's
// data.setName survives /v1/audit/batch normalization (the server rebinds
// session/user and must not strip the revision-20 field). Pin, not a
// red-proof: normalization already passes unknown data fields through.
func TestAuditBatchPreservesSetName(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	token, _ := checkinToken(t, app, base)

	batch := map[string]any{
		"events": []map[string]any{
			{"data": map[string]any{
				"event": "tool.pre", "tool": "shell.exec", "command": "spooled-setname",
				"effect": "deny", "ruleId": "r-spool", "setName": "spool-set", "snapshot": "snap-x",
			}},
		},
	}
	if code, resp := postJSONAuth(t, base+"/v1/audit/batch", token, batch); code != http.StatusOK {
		t.Fatalf("batch = %d %v", code, resp)
	}
	waitForCE(t, app, `"command":"spooled-setname"`)
	recs, err := app.store.Audit().List(context.Background(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if strings.Contains(r.CE, `"command":"spooled-setname"`) {
			if !strings.Contains(r.CE, `"setName":"spool-set"`) {
				t.Errorf("client CE lost setName through normalization:\n%s", r.CE)
			}
			return
		}
	}
	t.Fatal("spooled CE never reached the chain")
}

// TestAuditToolHoldDenyCarriesSetName pins the same field on the held lane: a
// call a rule holds for approval is answered with a deny, and that record must
// name the set that decided it, not an empty set name.
func TestAuditToolHoldDenyCarriesSetName(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app)
	ctx := context.Background()

	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "pool-gate", Status: "active",
		YAMLSource: strings.Replace(approvePoolYAML, "[POOL]", "[sec-approvers]", 1),
	}); err != nil {
		t.Fatalf("create the hold policy: %v", err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatalf("recompile: %v", err)
	}

	token, _ := checkinToken(t, app, base)
	if code, dec := decide(t, base, token, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "echo held-by-the-set",
	}); code != http.StatusOK || dec["effect"] != "deny" {
		t.Fatalf("held decide = %d %v, want the hold deny", code, dec)
	}

	waitForCE(t, app, `"command":"echo held-by-the-set"`)
	recs, err := app.store.Audit().List(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if strings.Contains(r.CE, `"command":"echo held-by-the-set"`) {
			if !strings.Contains(r.CE, `"setName":"pool-gate"`) {
				t.Errorf("hold deny CE misses setName pool-gate:\n%s", r.CE)
			}
			return
		}
	}
	t.Fatal("the hold deny never reached the chain")
}
