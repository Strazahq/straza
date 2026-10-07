package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// driftProbeRefused is drift-probe with a hold whose approver pool names a
// role that does not exist: it parses, so a save stores it, and activation
// refuses it.
const driftProbeRefused = `apiVersion: straza.dev/v1beta1
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
      reason: "Straza: refused version"
    - id: hold-push
      tools: [shell.exec]
      command:
        allowPatterns: ["git push *"]
      effect: allow
      mode: approve
      approve:
        roles: [no-such-approvers]
        timeoutSeconds: 300
      reason: "Straza: push waits"
`

// TestPolicyActivateChangesOnlyThatSet pins that activating or turning off
// one set publishes exactly that set. Text saved over another live set never
// reaches the snapshot through someone else's publish, a refused activation
// changes nothing, and activating a set that is on already writes nothing
// and names the running snapshot.
func TestPolicyActivateChangesOnlyThatSet(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	put := func(yaml string, want int) {
		t.Helper()
		if code, body, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(yaml)); code != want {
			t.Fatalf("apply = %d (%s), want %d", code, body, want)
		}
	}
	setStatus := func(name, status string) (int, string) {
		t.Helper()
		code, body, _ := adminBytes(t, "POST", base+"/v1/admin/policies/"+name+"/activate", tok, "application/json", []byte(`{"status":"`+status+`"}`))
		return code, string(body)
	}
	activations := func(name string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, data := range adminAuditEvents(t, app) {
			if data["action"] == "policy.activate" && data["name"] == name {
				out = append(out, data)
			}
		}
		return out
	}

	put(driftProbeV1, http.StatusCreated)
	put(driftOther, http.StatusCreated)
	for _, name := range []string{"drift-probe", "drift-other"} {
		if code, body := setStatus(name, "active"); code != http.StatusOK {
			t.Fatalf("activate %s = %d (%s)", name, code, body)
		}
	}

	// A save over the live probe is stored and does not decide.
	put(driftProbeV2, http.StatusOK)
	if got := probeReason(t, app); got != "Straza: v1" {
		t.Fatalf("after the save the probe decides with %q, want the published v1", got)
	}

	// Activating the other set again leaves the saved v2 out, writes no
	// record and names the snapshot running.
	before := len(activations("drift-other"))
	code, body := setStatus("drift-other", "active")
	if code != http.StatusOK || !strings.Contains(body, app.snapshots.Current().ID) {
		t.Fatalf("re-activate drift-other = %d (%s), want 200 naming the running snapshot", code, body)
	}
	if recs := activations("drift-other"); len(recs) != before {
		t.Fatalf("activating drift-other again wrote %d records, want none", len(recs)-before)
	}
	if got := probeReason(t, app); got != "Straza: v1" {
		t.Fatalf("publishing drift-other published the probe's saved edits: it decides with %q", got)
	}
	if d := driftOf(t, base, tok, "drift-probe"); d == nil || !*d {
		t.Fatal("the probe's saved edits no longer read as drift after another set's publish")
	}

	// A refused activation changes nothing: the snapshot, the deciding text
	// and the drift stay, and no activation record is written.
	put(driftProbeRefused, http.StatusOK)
	runningID := app.snapshots.Current().ID
	probeActivations := len(activations("drift-probe"))
	code, body = setStatus("drift-probe", "active")
	if code != http.StatusBadRequest || !strings.Contains(body, "approve.roles names") || !strings.Contains(body, "no-such-approvers") {
		t.Fatalf("activating the refused text = %d (%s), want 400 naming the approver pool", code, body)
	}
	if app.snapshots.Current().ID != runningID {
		t.Error("a refused activation swapped the snapshot")
	}
	if got := probeReason(t, app); got != "Straza: v1" {
		t.Errorf("after the refusal the probe decides with %q, want v1", got)
	}
	if d := driftOf(t, base, tok, "drift-probe"); d == nil || !*d {
		t.Error("after the refusal the probe's drift is no longer claimed")
	}
	if n := len(activations("drift-probe")); n != probeActivations {
		t.Errorf("a refused activation wrote %d activation records", n-probeActivations)
	}

	// The refused text stays stored and never reaches the snapshot through
	// the other set turning off and on.
	for _, status := range []string{"draft", "active"} {
		if code, body := setStatus("drift-other", status); code != http.StatusOK {
			t.Fatalf("setting drift-other %s = %d (%s)", status, code, body)
		}
		if got := probeReason(t, app); got != "Straza: v1" {
			t.Fatalf("after drift-other went %s the probe decides with %q, want v1", status, got)
		}
	}

	// Publishing the probe itself runs its checks on what it stores and, when
	// they pass, makes that text decide and clears the drift.
	put(driftProbeV2, http.StatusOK)
	if code, body := setStatus("drift-probe", "active"); code != http.StatusOK {
		t.Fatalf("activate the probe's v2 = %d (%s)", code, body)
	}
	if got := probeReason(t, app); got != "Straza: v2" {
		t.Errorf("after its own publish the probe decides with %q, want v2", got)
	}
	if d := driftOf(t, base, tok, "drift-probe"); d != nil {
		t.Errorf("after its own publish the probe's drift = %v, want absent", *d)
	}
}

// TestPolicyActivateThatFailsWritesNothing pins a failed publish of an
// activate: the set decides as before, its stored status stays, no record
// is written and the running snapshot stays, and the same command again
// makes the change. The status commits with the snapshot, so no answer
// says a change decides while its status was not recorded.
func TestPolicyActivateThatFailsWritesNothing(t *testing.T) {
	cases := []struct {
		name    string
		live    bool   // the probe is published before the call
		status  string // the status the call asks for
		decides bool   // the probe decides before and after the failed call
		stored  string // the probe's stored status before and after the failed call
	}{
		{name: "an activation", status: "active", decides: false, stored: "draft"},
		{name: "a turn-off", live: true, status: "draft", decides: true, stored: "active"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &publishHook{}
			p := newPolicyRig(t, func(a *App) { h.Store, a.store = a.store, h })
			p.mustPut(t, driftProbeV1, http.StatusCreated)
			if tc.live {
				p.mustActivate(t, "drift-probe", "active")
			}
			action := "policy.activate"
			if tc.status == "draft" {
				action = "policy.deactivate"
			}
			n, snap := len(adminAuditEvents(t, p.app)), p.app.snapshots.Current().ID

			h.arm(func(context.Context, int) error { return errors.New("injected: the transaction was lost") }, nil)
			code, out := p.activate(t, "drift-probe", tc.status)
			h.disarm()

			if msg, _ := out["error"].(string); code != http.StatusInternalServerError || msg != directUnconfirmedRefusal {
				t.Fatalf("the call = %d %v, want 500 %q", code, out, directUnconfirmedRefusal)
			}
			if got := probeReason(t, p.app) == "Straza: v1"; got != tc.decides {
				t.Errorf("the probe decides = %v after the call, want %v", got, tc.decides)
			}
			if row := rowOf(t, p.app, "drift-probe"); row.Status != tc.stored {
				t.Errorf("stored status = %s after the call, want %s", row.Status, tc.stored)
			}
			if recs := p.events(t, n); len(recs) != 0 || p.app.snapshots.Current().ID != snap {
				t.Errorf("the failed call wrote %v and runs snapshot %s, want nothing and %s", recs, p.app.snapshots.Current().ID, snap)
			}
			p.mustActivate(t, "drift-probe", tc.status)
			if row := rowOf(t, p.app, "drift-probe"); row.Status != tc.status || !sameActions(p.events(t, n), action) {
				t.Errorf("after running it again the stored status = %s and the records %v, want %s and one %s", row.Status, actions(p.events(t, n)), tc.status, action)
			}
		})
	}
}

// TestPolicyDeleteRefusesAPublishedSet pins that delete asks the published
// snapshot, not only the stored status. A replica of the release before
// drafts can leave a set stored as a draft while the snapshot still carries
// it, and deleting it then would leave a set nobody can see deciding every
// call it matches. Delete refuses that set with 409 until it is turned
// off, and a set that was never published is deleted as before.
func TestPolicyDeleteRefusesAPublishedSet(t *testing.T) {
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	if code, body, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(driftProbeV1)); code != http.StatusCreated {
		t.Fatalf("apply the probe = %d (%s)", code, body)
	}
	setStatus := func(status string) (int, string) {
		code, body, _ := adminBytes(t, "POST", base+"/v1/admin/policies/drift-probe/activate", tok, "application/json", []byte(`{"status":"`+status+`"}`))
		return code, string(body)
	}
	del := func() (int, string) {
		code, body, _ := adminBytes(t, "DELETE", base+"/v1/admin/policies/drift-probe", tok, "", nil)
		return code, string(body)
	}

	if code, body := setStatus("active"); code != http.StatusOK {
		t.Fatalf("activate the probe = %d (%s)", code, body)
	}
	row := rowOf(t, app, "drift-probe")
	row.Status = "draft"
	if _, err := app.store.Policies().Update(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if got := probeReason(t, app); got != "Straza: v1" {
		t.Fatalf("the probe decides with %q, want it published as v1", got)
	}

	const want = "the policy set drift-probe is still published and deciding, although its stored status says draft. Turn it off first, then delete"
	if code, body := del(); code != http.StatusConflict || !strings.Contains(body, want) {
		t.Fatalf("delete of a published draft = %d (%s), want 409 saying %q", code, body, want)
	}
	if _, err := app.store.Policies().GetByName(context.Background(), "drift-probe"); err != nil {
		t.Fatalf("the refused delete removed the set: %v", err)
	}

	if code, body := setStatus("draft"); code != http.StatusOK {
		t.Fatalf("turn the probe off = %d (%s)", code, body)
	}
	if code, body := del(); code != http.StatusNoContent {
		t.Fatalf("delete after turning it off = %d (%s), want 204", code, body)
	}
}
