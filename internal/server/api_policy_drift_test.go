package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// Two versions of one small set: v2 changes only the reason, so applying it
// over the active v1 is exactly the "saved but the running snapshot does not
// carry it" state the drift field exists to expose.
const driftProbeV1 = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: drift-probe
spec:
  priority: 120
  match:
    roles: [dev]
  rules:
    - id: no-rm
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *"]
      effect: deny
      reason: "Straza: v1"
`

const driftProbeV2 = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: drift-probe
spec:
  priority: 120
  match:
    roles: [dev]
  rules:
    - id: no-rm
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *"]
      effect: deny
      reason: "Straza: v2"
`

const driftOther = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: drift-other
spec:
  priority: 110
  match:
    roles: [ops]
  rules:
    - id: no-mkfs
      tools: [shell.exec]
      command:
        denyPatterns: ["mkfs*"]
      effect: deny
      reason: "Straza: no"
`

// driftOf lists the policies and returns the drift field of one row as a
// tri-state: nil = field absent (no claim), else the sent bool. The payload
// contract is omitempty, so absent and false are the same wire byte; the
// pointer keeps the test honest about "no field" vs "false".
func driftOf(t *testing.T, base, tok, name string) *bool {
	t.Helper()
	code, body, _ := adminBytes(t, "GET", base+"/v1/admin/policies", tok, "", nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	var env struct {
		Items []struct {
			Name  string `json:"name"`
			Drift *bool  `json:"drift"`
		} `json:"items"`
	}
	if err := jsonUnmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	for _, r := range env.Items {
		if r.Name == name {
			return r.Drift
		}
	}
	t.Fatalf("row %q not listed", name)
	return nil
}

// probeReason evaluates rm -rf for a dev session against the running snapshot
// and returns the reason, which names the drift-probe version that decides.
func probeReason(t *testing.T, app *App) string {
	t.Helper()
	cur := app.snapshots.Current()
	if cur == nil {
		t.Fatal("no running snapshot")
	}
	return cur.Engine.Evaluate(
		policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "rm -rf /tmp/x"},
		policy.Subject{User: "probe", Roles: []string{"dev"}},
	).Reason
}

// TestPolicyListDriftLifecycle pins the drift contract: compiled_hash
// is the sha256 of the text this set was last published with, and the list
// claims drift exactly while the set has an open saved edit. Publishing or
// turning off another set changes nothing here, because a publish swaps one
// set into the running snapshot and leaves every other set's published text
// in place.
func TestPolicyListDriftLifecycle(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	if code, body, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(driftProbeV1)); code != http.StatusCreated {
		t.Fatalf("apply drift-probe = %d (%s)", code, body)
	}
	// (a) A draft makes no drift claim.
	if d := driftOf(t, base, tok, "drift-probe"); d != nil {
		t.Fatalf("draft drift = %v, want absent", *d)
	}

	if code, body, _ := adminBytes(t, "POST", base+"/v1/admin/policies/drift-probe/activate", tok, "application/json", []byte(`{"status":"active"}`)); code != http.StatusOK {
		t.Fatalf("activate = %d (%s)", code, body)
	}
	// (b) Activation stamps the hash of the compiled source and claims no drift.
	if d := driftOf(t, base, tok, "drift-probe"); d != nil {
		t.Fatalf("freshly activated drift = %v, want absent", *d)
	}
	ps, err := app.store.Policies().GetByName(ctx, "drift-probe")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(ps.YAMLSource))
	if want := hex.EncodeToString(sum[:]); ps.CompiledHash != want {
		t.Fatalf("compiled_hash after activate = %q, want sha256 of the stored source %q", ps.CompiledHash, want)
	}

	// (c) Saving over the active set is drift: stored text != compiled text.
	if code, body, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(driftProbeV2)); code != http.StatusOK {
		t.Fatalf("apply v2 over active = %d (%s)", code, body)
	}
	if d := driftOf(t, base, tok, "drift-probe"); d == nil || !*d {
		t.Fatal("saved-over-active drift not claimed, want drift true")
	}

	// (d) Re-activation publishes the stored source: drift clears and v2
	// decides.
	if code, _, _ := adminBytes(t, "POST", base+"/v1/admin/policies/drift-probe/activate", tok, "application/json", []byte(`{"status":"active"}`)); code != http.StatusOK {
		t.Fatal("re-activate failed")
	}
	if d := driftOf(t, base, tok, "drift-probe"); d != nil {
		t.Fatalf("re-activated drift = %v, want absent", *d)
	}
	if got := probeReason(t, app); got != "Straza: v2" {
		t.Fatalf("after re-activation the probe decides with %q, want v2", got)
	}

	// (e) Saved edits stay unpublished while another set is turned off and
	// on: the published v2 keeps deciding and the drift stays claimed.
	if code, _, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(driftOther)); code != http.StatusCreated {
		t.Fatal("apply drift-other failed")
	}
	if code, _, _ := adminBytes(t, "POST", base+"/v1/admin/policies/drift-other/activate", tok, "application/json", []byte(`{"status":"active"}`)); code != http.StatusOK {
		t.Fatal("activate drift-other failed")
	}
	if code, _, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(driftProbeV1)); code != http.StatusOK {
		t.Fatal("apply v1 over active failed")
	}
	if d := driftOf(t, base, tok, "drift-probe"); d == nil || !*d {
		t.Fatal("drift before the other set's publish not claimed")
	}
	for _, status := range []string{"draft", "active"} {
		if code, _, _ := adminBytes(t, "POST", base+"/v1/admin/policies/drift-other/activate", tok, "application/json", []byte(`{"status":"`+status+`"}`)); code != http.StatusOK {
			t.Fatalf("setting drift-other %s failed", status)
		}
		if d := driftOf(t, base, tok, "drift-probe"); d == nil || !*d {
			t.Fatalf("drift after drift-other went %s not claimed, want the saved v1 still unpublished", status)
		}
		if got := probeReason(t, app); got != "Straza: v2" {
			t.Fatalf("after drift-other went %s the probe decides with %q, want the published v2", status, got)
		}
	}
}
