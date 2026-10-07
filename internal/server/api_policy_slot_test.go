package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// driftProbeV3 is a third text of drift-probe, with another priority and
// description and a match role V1 and V2 do not name.
const driftProbeV3 = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: drift-probe
  description: The third text of the probe.
spec:
  priority: 130
  match:
    roles: [dev, probe-readers]
  rules:
    - id: no-rm
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *"]
      effect: deny
      reason: "Straza: v3"
`

// policyRig is an admin session over a fresh App for the policy route
// tests, with the calls they make.
type policyRig struct {
	app  *App
	base string
	tok  string
	user store.User
}

// newPolicyRig boots an App, optionally on a wrapped store, and signs in
// its admin.
func newPolicyRig(t *testing.T, preRun ...func(*App)) *policyRig {
	t.Helper()
	app, base := testAppPreRun(t, preRun)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	return &policyRig{app: app, base: base, tok: tok, user: user}
}

// put saves text through PUT /v1/admin/policies and answers the status
// and the decoded body.
func (p *policyRig) put(t *testing.T, text string) (int, map[string]any) {
	t.Helper()
	code, body, _ := adminBytes(t, "PUT", p.base+"/v1/admin/policies", p.tok, "application/yaml", []byte(text))
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("PUT answered %d %q", code, body)
	}
	return code, out
}

// mustPut saves text and fails the test unless it answers want.
func (p *policyRig) mustPut(t *testing.T, text string, want int) map[string]any {
	t.Helper()
	code, out := p.put(t, text)
	if code != want {
		t.Fatalf("PUT = %d %v; want %d", code, out, want)
	}
	return out
}

// activate asks for status on the set name and answers the status and the
// decoded body.
func (p *policyRig) activate(t *testing.T, name, status string) (int, map[string]any) {
	t.Helper()
	code, body, _ := adminBytes(t, "POST", p.base+"/v1/admin/policies/"+name+"/activate", p.tok, "application/json", []byte(`{"status":"`+status+`"}`))
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("activate answered %d %q", code, body)
	}
	return code, out
}

// mustActivate asks for status on name and fails the test unless it
// answers 200.
func (p *policyRig) mustActivate(t *testing.T, name, status string) map[string]any {
	t.Helper()
	code, out := p.activate(t, name, status)
	if code != http.StatusOK {
		t.Fatalf("activate %s %s = %d %v", name, status, code, out)
	}
	return out
}

// live puts text as a new set and turns it on.
func (p *policyRig) live(t *testing.T, name, text string) {
	t.Helper()
	p.mustPut(t, text, http.StatusCreated)
	p.mustActivate(t, name, "active")
}

// generation reads the config generation.
func (p *policyRig) generation(t *testing.T) int64 {
	t.Helper()
	g, err := p.app.store.Drafts().Generation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// events answers the admin records written since the first n.
func (p *policyRig) events(t *testing.T, n int) []map[string]any {
	t.Helper()
	return adminAuditEvents(t, p.app)[n:]
}

// actions answers the actions of records.
func actions(records []map[string]any) []string {
	out := make([]string, len(records))
	for i, rec := range records {
		out[i], _ = rec["action"].(string)
	}
	return out
}

// sameActions reports whether records hold exactly the actions want, in
// order.
func sameActions(records []map[string]any, want ...string) bool {
	return strings.Join(actions(records), ",") == strings.Join(want, ",")
}

// answerOf is a policy route's answer as JSON reads it back.
func answerOf(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// fakeSecretText is drift-probe with a GitHub-token-shaped value in a
// comment, which the secret scan refuses.
var fakeSecretText = driftProbeV2 + "# break-glass token: " + "gh" + "p_" + strings.Repeat("0a1b2c3d4e5f", 3) + "\n"

// TestPolicyApplyOnALiveSetWritesItsSavedEdit pins the save of a live set:
// today's 200 body, the set's slot draft with the caller as author
// and the published text as its stamped base, draft.create then
// draft.update, and no policy.update, while the row, the snapshot and the
// config generation stay where they were.
func TestPolicyApplyOnALiveSetWritesItsSavedEdit(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	p.live(t, "drift-probe", driftProbeV1)
	row := rowOf(t, p.app, "drift-probe")
	gen, snap, n := p.generation(t), p.app.snapshots.Current().ID, len(adminAuditEvents(t, p.app))

	got := p.mustPut(t, driftProbeV3, http.StatusOK)

	if want := answerOf(t, policyPayload{ID: row.ID, Name: "drift-probe", Priority: 130, Status: "active"}); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the save answered %v; want %v", got, want)
	}
	d, items := savedEditOf(t, p.app, "drift-probe")
	wantItem := store.DraftItemRow{Seq: 1, Kind: "PolicySet", Name: "drift-probe", Op: "put", Doc: driftProbeV3,
		Base: store.FingerprintPolicySet(row), BaseOp: "put", BaseDoc: driftProbeV1}
	if d.Door != "api" || d.Proposer.ID != p.user.ID || d.CheckedRevision != 0 || len(items) != 1 || items[0] != wantItem {
		t.Errorf("the saved edit is %+v holding %+v; want door api by kim, unchecked, holding %+v", d, items, wantItem)
	}
	recs := p.events(t, n)
	if !sameActions(recs, "draft.create") || recs[0]["draft"] != strconv.FormatInt(d.ID, 10) || recs[0]["door"] != "api" ||
		recs[0]["revision"] != float64(1) || recs[0]["actor"] != "kim" {
		t.Errorf("the save recorded %v; want one draft.create of draft %d at revision 1 by kim", recs, d.ID)
	}
	if after := rowOf(t, p.app, "drift-probe"); after.YAMLSource != driftProbeV1 || !after.UpdatedAt.Equal(row.UpdatedAt) {
		t.Errorf("the save wrote the row: %q", after.YAMLSource)
	}
	if p.generation(t) != gen || p.app.snapshots.Current().ID != snap || probeReason(t, p.app) != "Straza: v1" {
		t.Error("the save of a live set published")
	}

	p.mustPut(t, driftProbeV2, http.StatusOK)
	d, items = savedEditOf(t, p.app, "drift-probe")
	if d.Revision != 2 || len(items) != 1 || items[0].Doc != driftProbeV2 || items[0].Base != wantItem.Base {
		t.Errorf("the second save left revision %d holding %+v; want revision 2 with V2 on the first base", d.Revision, items)
	}
	recs = p.events(t, n)
	if !sameActions(recs, "draft.create", "draft.update") || recs[1]["revision"] != float64(2) {
		t.Errorf("the saves recorded %v; want draft.create then draft.update at revision 2", recs)
	}
}

// TestPolicyApplyOfThePublishedTextDiscardsTheSavedEdit pins a save equal
// to the published text: it discards the open saved edit with its reason
// and one draft.discard, and with no saved edit it writes nothing at all.
func TestPolicyApplyOfThePublishedTextDiscardsTheSavedEdit(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	p.live(t, "drift-probe", driftProbeV1)
	n := len(adminAuditEvents(t, p.app))
	dump := storeDump(t, p.app)
	p.mustPut(t, driftProbeV1, http.StatusOK)
	if after := storeDump(t, p.app); after != dump || len(p.events(t, n)) != 0 {
		t.Errorf("a save of the published text with no saved edit wrote %v", p.events(t, n))
	}

	p.mustPut(t, driftProbeV2, http.StatusOK)
	d, _ := savedEditOf(t, p.app, "drift-probe")
	got := p.mustPut(t, driftProbeV1, http.StatusOK)
	if got["status"] != "active" || got["priority"] != float64(120) {
		t.Errorf("the save answered %v", got)
	}
	closed, _, err := p.app.store.Drafts().Get(context.Background(), d.ID)
	if err != nil || closed.State != "discarded" || closed.DecidedReason != "the saved text equals the published text" || closed.DecidedBy.ID != p.user.ID {
		t.Errorf("the saved edit is %+v (%v); want discarded by kim with the reason", closed, err)
	}
	recs := p.events(t, n)
	if !sameActions(recs, "draft.create", "draft.discard") || recs[1]["reason"] != "the saved text equals the published text" || recs[1]["closedBy"] != nil {
		t.Errorf("the saves recorded %v; want draft.create, then draft.discard with the reason and no closedBy", recs)
	}
	noSavedEdit(t, p.app, "drift-probe")
}

// TestPolicyApplyOverAStaleSavedEditOpensANewOne pins the stale-base race: a
// saved edit whose base another publish replaced is discarded with its
// reason at the next save, which opens a new saved edit stamped on the
// published text.
func TestPolicyApplyOverAStaleSavedEditOpensANewOne(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	p.live(t, "drift-probe", driftProbeV1)
	p.mustPut(t, driftProbeV2, http.StatusOK)
	stale, _ := savedEditOf(t, p.app, "drift-probe")
	publishText(t, p.app, "drift-probe", driftProbeV3)
	n := len(adminAuditEvents(t, p.app))

	p.mustPut(t, driftProbeRefused, http.StatusOK)

	closed, _, err := p.app.store.Drafts().Get(context.Background(), stale.ID)
	if err != nil || closed.State != "discarded" || closed.DecidedReason != "the published text changed after this edit was saved" {
		t.Errorf("the stale saved edit is %+v (%v); want discarded with the reason", closed, err)
	}
	d, items := savedEditOf(t, p.app, "drift-probe")
	if d.ID == stale.ID || len(items) != 1 || items[0].Doc != driftProbeRefused || items[0].BaseDoc != driftProbeV3 ||
		items[0].Base != publishedFingerprint("drift-probe", driftProbeV3) {
		t.Errorf("the new saved edit is %+v holding %+v; want a new draft on the published V3", d, items)
	}
	if recs := p.events(t, n); !sameActions(recs, "draft.discard", "draft.create") ||
		recs[0]["reason"] != "the published text changed after this edit was saved" {
		t.Errorf("the save recorded %v; want draft.discard with the reason, then draft.create", recs)
	}
}

// TestPolicyApplyRefusesASecret pins the secret scan on PUT: a text holding a
// secret-shaped value answers 400 with the scan's sentence and fix, and
// stores nothing, for a new set and for the save of a live one.
func TestPolicyApplyRefusesASecret(t *testing.T) {
	t.Parallel()
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("live %v", live), func(t *testing.T) {
			t.Parallel()
			p := newPolicyRig(t)
			if live {
				p.live(t, "drift-probe", driftProbeV1)
			}
			gen, dump := p.generation(t), storeDump(t, p.app)
			code, body, _ := adminBytes(t, "PUT", p.base+"/v1/admin/policies", p.tok, "application/yaml", []byte(fakeSecretText))
			if code != http.StatusBadRequest || !strings.Contains(string(body), "A policy set never needs a secret, in a rule or in a comment.") {
				t.Errorf("the save of a secret = %d %s; want 400 with the scan's words", code, body)
			}
			if p.generation(t) != gen || storeDump(t, p.app) != dump {
				t.Error("a refused save stored something")
			}
		})
	}
}

// TestPolicyApplyPublishesANewOrOffSet pins PUT on a set that is not on: a
// new name publishes the set off with 201, a new text of a set that is off
// publishes it with 200, the records carry the draft and no draft.* record
// is written, and the stored text sent again writes nothing.
func TestPolicyApplyPublishesANewOrOffSet(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	gen, n := p.generation(t), len(adminAuditEvents(t, p.app))

	got := p.mustPut(t, driftProbeV1, http.StatusCreated)
	row := rowOf(t, p.app, "drift-probe")
	if want := answerOf(t, policyPayload{ID: row.ID, Name: "drift-probe", Priority: 120, Status: "draft"}); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the create answered %v; want %v", got, want)
	}
	recs := p.events(t, n)
	if !sameActions(recs, "policy.create") || recs[0]["draft"] == nil || recs[0]["id"] != row.ID || p.generation(t) != gen+1 {
		t.Errorf("the create recorded %v at generation %d; want one policy.create naming its draft, generation %d", recs, p.generation(t), gen+1)
	}
	d, _, err := p.app.store.Drafts().Get(context.Background(), mustAtoi(t, recs[0]["draft"]))
	if err != nil || d.State != "published" || d.Door != "api" || d.Proposer.ID != p.user.ID {
		t.Errorf("the one-item draft is %+v (%v); want published through door api by kim", d, err)
	}

	dump := storeDump(t, p.app)
	if got := p.mustPut(t, driftProbeV1, http.StatusOK); got["id"] != row.ID || got["status"] != "draft" {
		t.Errorf("the unchanged save answered %v", got)
	}
	if storeDump(t, p.app) != dump || p.generation(t) != gen+1 || len(p.events(t, n)) != 1 {
		t.Errorf("the save of the stored text of a set that is off wrote something: %v", p.events(t, n))
	}

	p.mustPut(t, driftProbeV2, http.StatusOK)
	if after := rowOf(t, p.app, "drift-probe"); after.YAMLSource != driftProbeV2 || after.Status != "draft" {
		t.Errorf("the row is %s with %q; want off with V2", after.Status, after.YAMLSource)
	}
	if recs := p.events(t, n); !sameActions(recs, "policy.create", "policy.update") || recs[1]["draft"] == nil {
		t.Errorf("the save recorded %v; want policy.update naming its draft", recs)
	}
}

// mustAtoi reads a draft id a record names.
func mustAtoi(t *testing.T, v any) int64 {
	t.Helper()
	s, _ := v.(string)
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("the draft id %v: %v", v, err)
	}
	return id
}

// TestPolicyActivatePublishesTheSavedEdit pins activate over a saved edit:
// the gates run first on its text in today's words and a refusal leaves
// it open, then the saved edit is published as it is, the row and the
// snapshot take its text, and the records name that draft, draft.publish
// with no acknowledgment among them.
func TestPolicyActivatePublishesTheSavedEdit(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	p.live(t, "drift-probe", driftProbeV1)
	p.mustPut(t, driftProbeRefused, http.StatusOK)
	gen := p.generation(t)
	code, out := p.activate(t, "drift-probe", "active")
	if msg, _ := out["error"].(string); code != http.StatusBadRequest || !strings.Contains(msg, "approve.roles names") || !strings.Contains(msg, "no-such-approvers") {
		t.Fatalf("the refused saved edit = %d %v; want 400 naming the approver pool", code, out)
	}
	d, _ := savedEditOf(t, p.app, "drift-probe")
	if p.generation(t) != gen || probeReason(t, p.app) != "Straza: v1" {
		t.Error("a refused activate published")
	}

	p.mustPut(t, driftProbeV2, http.StatusOK)
	n := len(adminAuditEvents(t, p.app))
	got := p.mustActivate(t, "drift-probe", "active")

	act, err := p.app.store.Snapshots().GetActive(context.Background())
	if err != nil || got["status"] != "active" || got["snapshot"] != act.ID || got["changed"] != true || len(got) != 3 {
		t.Errorf("the activate answered %v; want the status, the active snapshot %s and changed true", got, act.ID)
	}
	published, _, err := p.app.store.Drafts().Get(context.Background(), d.ID)
	if err != nil || published.State != "published" || published.Revision != 2 {
		t.Errorf("the saved edit is %+v (%v); want published at revision 2", published, err)
	}
	noSavedEdit(t, p.app, "drift-probe")
	if row := rowOf(t, p.app, "drift-probe"); row.YAMLSource != driftProbeV2 || row.Status != "active" || probeReason(t, p.app) != "Straza: v2" {
		t.Errorf("after the activate the row holds %q and the probe decides %q; want V2", row.YAMLSource, probeReason(t, p.app))
	}
	id := strconv.FormatInt(d.ID, 10)
	recs := p.events(t, n)
	if !sameActions(recs, "policy.update", "policy.activate", "draft.publish") {
		t.Fatalf("the activate recorded %v; want policy.update, policy.activate and draft.publish", actions(recs))
	}
	for _, rec := range recs {
		if rec["draft"] != id || rec["actor"] != "kim" {
			t.Errorf("%v does not name the saved edit %s and kim", rec, id)
		}
	}
	if pub := recs[2]; fmt.Sprint(pub["acknowledged"]) != "[]" || fmt.Sprint(pub["typed"]) != "[]" || pub["revision"] != float64(2) {
		t.Errorf("draft.publish is %v; want revision 2 with no acknowledgment", pub)
	}
}

// staleSentence is the 409 of an activate whose saved edit went stale.
const staleSentence = "The saved edit of drift-probe was made on a text that another publish has replaced since, so activating it would undo that change. Open the set, save your text again, then activate it."

// TestPolicyActivateOfAStaleSavedEdit pins draft.stale on the saved edit:
// activate answers its pinned 409 and publishes nothing.
func TestPolicyActivateOfAStaleSavedEdit(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	p.live(t, "drift-probe", driftProbeV1)
	p.mustPut(t, driftProbeV2, http.StatusOK)
	publishText(t, p.app, "drift-probe", driftProbeV3)
	gen := p.generation(t)
	code, out := p.activate(t, "drift-probe", "active")
	if code != http.StatusConflict || out["error"] != staleSentence {
		t.Errorf("the stale activate = %d %v; want 409 %q", code, out, staleSentence)
	}
	if p.generation(t) != gen || rowOf(t, p.app, "drift-probe").YAMLSource != driftProbeV3 {
		t.Error("the stale activate published")
	}
}

// TestPolicyActivateAgainWritesNothing pins the no-op rule: a set already in the
// asked state with no saved edit answers 200 with the current snapshot and
// writes nothing, and no gate runs, even one that would refuse now.
func TestPolicyActivateAgainWritesNothing(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	ctx := context.Background()
	deciders, err := p.app.store.Roles().Create(ctx, store.Role{Name: "k30-deciders", Kind: store.RoleKindApprover, Plane: store.RolePlaneAccess})
	if err != nil {
		t.Fatal(err)
	}
	held := strings.Replace(driftProbeRefused, "no-such-approvers", "k30-deciders", 1)
	p.live(t, "drift-probe", held)
	p.mustPut(t, driftOther, http.StatusCreated)
	deciders.Kind = store.RoleKindApplication
	if _, err := p.app.store.Roles().Update(ctx, deciders); err != nil {
		t.Fatal(err)
	}
	gen, n, dump := p.generation(t), len(adminAuditEvents(t, p.app)), storeDump(t, p.app)
	for _, tc := range []struct{ name, status string }{{"drift-probe", "active"}, {"drift-other", "draft"}} {
		got := p.mustActivate(t, tc.name, tc.status)
		if got["status"] != tc.status || got["snapshot"] != p.app.snapshots.Current().ID {
			t.Errorf("activate %s %s answered %v; want the status and the running snapshot", tc.name, tc.status, got)
		}
	}
	if p.generation(t) != gen || len(p.events(t, n)) != 0 || storeDump(t, p.app) != dump {
		t.Errorf("a repeated activate wrote %v", p.events(t, n))
	}
}

// TestPolicyActivateSaysWhetherItChanged pins the changed field of the
// activate answer. A call that publishes answers true with the new
// snapshot. A call that finds the set already in the asked state with no
// saved edit answers false with the snapshot that was running before it.
func TestPolicyActivateSaysWhetherItChanged(t *testing.T) {
	t.Parallel()
	stored := func(t *testing.T, p *policyRig) { p.mustPut(t, driftProbeV1, http.StatusCreated) }
	live := func(t *testing.T, p *policyRig) { p.live(t, "drift-probe", driftProbeV1) }
	edited := func(t *testing.T, p *policyRig) {
		p.live(t, "drift-probe", driftProbeV1)
		p.mustPut(t, driftProbeV2, http.StatusOK)
	}
	tests := []struct {
		name    string
		setup   func(*testing.T, *policyRig)
		status  string
		changed bool
	}{
		{"a first activate of a set that is off", stored, "active", true},
		{"a repeated activate of a live set", live, "active", false},
		{"an activate that publishes the saved edit", edited, "active", true},
		{"a first deactivate of a live set", live, "draft", true},
		{"a repeated deactivate of a set that is off", stored, "draft", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPolicyRig(t)
			tc.setup(t, p)
			before := p.app.snapshots.Current().ID
			got := p.mustActivate(t, "drift-probe", tc.status)
			after := p.app.snapshots.Current().ID
			if got["changed"] != tc.changed {
				t.Errorf("the %s answered %v; want changed %v", tc.status, got, tc.changed)
			}
			if got["snapshot"] != after || (after != before) != tc.changed {
				t.Errorf("the %s answered %v, and the running snapshot went from %s to %s", tc.status, got, before, after)
			}
		})
	}
}

// TestPolicyTurnOffClosesTheSavedEdit pins turning a set off: the set is
// stored off with the saved edit's text, the saved edit is discarded with
// the saved-edit close reason and one draft.discard naming the publish that
// closed it,
// and policy.deactivate is written.
func TestPolicyTurnOffClosesTheSavedEdit(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	p.live(t, "drift-probe", driftProbeV1)
	p.mustPut(t, driftProbeV2, http.StatusOK)
	d, _ := savedEditOf(t, p.app, "drift-probe")
	n := len(adminAuditEvents(t, p.app))

	if got := p.mustActivate(t, "drift-probe", "draft"); got["status"] != "draft" || got["snapshot"] != p.app.snapshots.Current().ID {
		t.Errorf("the turn-off answered %v", got)
	}

	if row := rowOf(t, p.app, "drift-probe"); row.Status != "draft" || row.YAMLSource != driftProbeV2 {
		t.Errorf("the row is %s with %q; want off with the saved text", row.Status, row.YAMLSource)
	}
	const reason = "drift-probe was turned off, and it keeps this edit's text while it is off"
	closed, _, err := p.app.store.Drafts().Get(context.Background(), d.ID)
	if err != nil || closed.State != "discarded" || closed.DecidedReason != reason {
		t.Errorf("the saved edit is %+v (%v); want discarded with the saved-edit close reason", closed, err)
	}
	recs := p.events(t, n)
	if !sameActions(recs, "policy.update", "policy.deactivate", "draft.discard") {
		t.Fatalf("the turn-off recorded %v", actions(recs))
	}
	if disc := recs[2]; disc["draft"] != strconv.FormatInt(d.ID, 10) || disc["reason"] != reason || disc["closedBy"] != recs[1]["draft"] {
		t.Errorf("draft.discard is %v; want the saved edit, the reason and the publish that closed it", disc)
	}
	if got := probeReason(t, p.app); got == "Straza: v1" || got == "Straza: v2" {
		t.Errorf("after the turn-off the probe still decides %q", got)
	}
}

// TestPolicyActivateAnOffSetPublishesItsStoredText pins activate of a set
// that is off with no saved edit: its stored text is published and
// policy.activate names the draft.
func TestPolicyActivateAnOffSetPublishesItsStoredText(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	p.mustPut(t, driftProbeV1, http.StatusCreated)
	gen, n := p.generation(t), len(adminAuditEvents(t, p.app))
	got := p.mustActivate(t, "drift-probe", "active")
	if got["snapshot"] != p.app.snapshots.Current().ID || probeReason(t, p.app) != "Straza: v1" || p.generation(t) != gen+1 {
		t.Errorf("the activate answered %v and the probe decides %q", got, probeReason(t, p.app))
	}
	if recs := p.events(t, n); !sameActions(recs, "policy.activate") || recs[0]["draft"] == nil || recs[0]["snapshot"] != got["snapshot"] {
		t.Errorf("the activate recorded %v; want one policy.activate naming its draft and the snapshot", recs)
	}
}

// TestPolicyDeleteRemovesAnOffSet pins DELETE as a one-item publish: a set
// that is off goes with 204 and policy.delete naming its draft, a live set
// and a set the snapshot carries while its row says off answer today's
// 409s, and a saved edit left open on an off set is closed.
func TestPolicyDeleteRemovesAnOffSet(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	ctx := context.Background()
	del := func(name string) (int, string) {
		code, body, _ := adminBytes(t, "DELETE", p.base+"/v1/admin/policies/"+name, p.tok, "", nil)
		return code, string(body)
	}
	p.live(t, "drift-probe", driftProbeV1)
	if code, body := del("drift-probe"); code != http.StatusConflict || !strings.Contains(body, "the policy set is live. Turn it off first, then delete") {
		t.Errorf("delete of a live set = %d %s", code, body)
	}
	row := rowOf(t, p.app, "drift-probe")
	row.Status = "draft"
	if _, err := p.app.store.Policies().Update(ctx, row); err != nil {
		t.Fatal(err)
	}
	const published = "the policy set drift-probe is still published and deciding, although its stored status says draft. Turn it off first, then delete"
	if code, body := del("drift-probe"); code != http.StatusConflict || !strings.Contains(body, published) {
		t.Errorf("delete of a published set stored off = %d %s", code, body)
	}

	p.mustPut(t, driftOther, http.StatusCreated)
	orphan, err := p.app.store.Drafts().Create(ctx, store.DraftRow{Door: "api", Slot: "policy:drift-other"},
		[]store.DraftItemRow{{Kind: "PolicySet", Name: "drift-other", Op: "put", Doc: driftOther}},
		store.DraftRevisionRow{Author: store.DraftActor{ID: p.user.ID, Name: "kim", Via: laneLogin, Client: clientLogin}, Digest: "d"})
	if err != nil {
		t.Fatal(err)
	}
	n := len(adminAuditEvents(t, p.app))
	if code, body := del("drift-other"); code != http.StatusNoContent || body != "" {
		t.Fatalf("delete of an off set = %d %q", code, body)
	}
	if _, err := p.app.store.Policies().GetByName(ctx, "drift-other"); err == nil {
		t.Error("the deleted set still has its row")
	}
	recs := p.events(t, n)
	if !sameActions(recs, "policy.delete", "draft.discard") || recs[0]["draft"] == nil || recs[1]["reason"] != "drift-other was deleted" {
		t.Errorf("the delete recorded %v; want policy.delete naming its draft and the orphan's draft.discard", recs)
	}
	if closed, _, err := p.app.store.Drafts().Get(ctx, orphan.ID); err != nil || closed.State != "discarded" {
		t.Errorf("the orphan saved edit is %+v (%v); want discarded", closed, err)
	}
}

// closedSentence is the 409 of an activate whose saved edit another
// replica closed while it ran, with the reason reason.
func closedSentence(reason string) string {
	return "The saved edit of drift-probe was closed while it was being activated: " + reason +
		". Nothing was published. Read the set with strazactl policy show drift-probe, save your text again if you still need it, then activate it."
}

// TestPolicyActivateWhenTheSavedEditMovedMeanwhile pins what an activate
// answers when another replica moves the saved edit it read before its
// commit: closed, and closed with a new saved edit opened in its place,
// answer the closed edit's 409 with its reason, a new revision answers the
// 409 that names it, and published answers the no-change 200, which says
// this call
// changed nothing. Only the published case publishes anything.
func TestPolicyActivateWhenTheSavedEditMovedMeanwhile(t *testing.T) {
	t.Parallel()
	other := store.DraftActor{Name: "lee", Via: laneLogin, Client: clientLogin}
	cases := []struct {
		name string
		move func(ctx context.Context, t *testing.T, s store.Store, app *App, d store.DraftRow, items []store.DraftItemRow) error
		code int
		want string
	}{
		{name: "closed", code: http.StatusConflict, want: closedSentence("draft 99 published another text of drift-probe"),
			move: func(ctx context.Context, _ *testing.T, s store.Store, _ *App, d store.DraftRow, _ []store.DraftItemRow) error {
				_, err := s.Drafts().Close(ctx, d.ID, 0, "discarded", other, "draft 99 published another text of drift-probe", time.Now())
				return err
			}},
		{name: "closed and a new one opened", code: http.StatusConflict, want: closedSentence("lee discarded it"),
			move: func(ctx context.Context, _ *testing.T, s store.Store, _ *App, d store.DraftRow, _ []store.DraftItemRow) error {
				if _, err := s.Drafts().Close(ctx, d.ID, 0, "discarded", other, "lee discarded it", time.Now()); err != nil {
					return err
				}
				_, err := s.Drafts().Create(ctx, store.DraftRow{Door: "api", Slot: "policy:drift-probe"},
					[]store.DraftItemRow{{Kind: "PolicySet", Name: "drift-probe", Op: "put", Doc: driftProbeV3,
						Base: publishedFingerprint("drift-probe", driftProbeV1), BaseOp: "put", BaseDoc: driftProbeV1}},
					store.DraftRevisionRow{Author: other, Door: "api", Digest: "d"})
				return err
			}},
		{name: "revised", code: http.StatusConflict, want: "The saved edit of drift-probe was saved again while it was being activated, and it is now at revision 2. " +
			"Nothing was published. Read the set with strazactl policy show drift-probe, check the text, then activate it again.",
			move: func(ctx context.Context, _ *testing.T, s store.Store, _ *App, d store.DraftRow, _ []store.DraftItemRow) error {
				_, err := s.Drafts().Revise(ctx, d.ID, store.DraftRevise{From: d.Revision,
					Items: []store.DraftItemRow{{Kind: "PolicySet", Name: "drift-probe", Op: "put", Doc: driftProbeV3}},
					Rev:   store.DraftRevisionRow{Author: other, Door: "api", Digest: "d2"}})
				return err
			}},
		{name: "published", code: http.StatusOK,
			move: func(_ context.Context, t *testing.T, _ store.Store, app *App, d store.DraftRow, items []store.DraftItemRow) error {
				publishSaved(t, app, d, items)
				return nil
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &publishHook{}
			p := newPolicyRig(t, func(a *App) { h.Store, a.store = a.store, h })
			p.live(t, "drift-probe", driftProbeV1)
			p.mustPut(t, driftProbeV2, http.StatusOK)
			d, items := savedEditOf(t, p.app, "drift-probe")
			h.arm(func(ctx context.Context, n int) error {
				if n != 1 {
					return nil
				}
				return tc.move(ctx, t, h.Store, p.app, d, items)
			}, nil)
			code, out := p.activate(t, "drift-probe", "active")
			h.disarm()
			act, err := p.app.store.Snapshots().GetActive(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case code != tc.code:
				t.Errorf("the activate = %d %v; want %d", code, out, tc.code)
			case tc.code == http.StatusOK && (out["snapshot"] != act.ID || out["changed"] != false):
				t.Errorf("the activate answered %v; want the active snapshot %s and changed false", out, act.ID)
			case tc.code != http.StatusOK && out["error"] != tc.want:
				t.Errorf("the activate answered %q; want %q", out["error"], tc.want)
			}
			if tc.code != http.StatusOK {
				if row := rowOf(t, p.app, "drift-probe"); row.YAMLSource != driftProbeV1 || publishedText(t, p.app, "drift-probe") != driftProbeV1 {
					t.Errorf("a refused activate published %q", row.YAMLSource)
				}
			}
		})
	}
}

// saveFaults wraps a store so a test can fail one DraftRepo method of the
// save path, and run a function once before the next Create, standing in
// for another replica that writes between two reads of this one.
type saveFaults struct {
	store.Store
	fail   atomic.Pointer[string]
	before atomic.Pointer[func()]
}

func (s *saveFaults) Drafts() store.DraftRepo { return saveFaultsRepo{s.Store.Drafts(), s} }

type saveFaultsRepo struct {
	store.DraftRepo
	s *saveFaults
}

// failing answers the armed failure when method is the one armed.
func (r saveFaultsRepo) failing(method string) error {
	if m := r.s.fail.Load(); m != nil && *m == method {
		return errors.New("injected: the database refused the statement")
	}
	return nil
}

func (r saveFaultsRepo) BySlot(ctx context.Context, slot string) (store.DraftRow, []store.DraftItemRow, error) {
	if err := r.failing("BySlot"); err != nil {
		return store.DraftRow{}, nil, err
	}
	return r.DraftRepo.BySlot(ctx, slot)
}

func (r saveFaultsRepo) Create(ctx context.Context, d store.DraftRow, items []store.DraftItemRow, rev store.DraftRevisionRow) (store.DraftRow, error) {
	if before := r.s.before.Swap(nil); before != nil {
		(*before)()
	}
	if err := r.failing("Create"); err != nil {
		return store.DraftRow{}, err
	}
	return r.DraftRepo.Create(ctx, d, items, rev)
}

func (r saveFaultsRepo) Revise(ctx context.Context, id int64, rv store.DraftRevise) (store.DraftRow, error) {
	if err := r.failing("Revise"); err != nil {
		return store.DraftRow{}, err
	}
	return r.DraftRepo.Revise(ctx, id, rv)
}

func (r saveFaultsRepo) Close(ctx context.Context, id int64, revision int, state string, by store.DraftActor, reason string, at time.Time) (bool, error) {
	if err := r.failing("Close"); err != nil {
		return false, err
	}
	return r.DraftRepo.Close(ctx, id, revision, state, by, reason, at)
}

// TestPolicyApplyAnswersAFailedSave pins the words of a save that fails
// on the store: a failed read of the saved edit answers "lookup failed",
// and a failed write of it answers "update failed", as the save of an
// existing set answered before drafts.
func TestPolicyApplyAnswersAFailedSave(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, method string
		open         bool   // a saved edit is open before the save
		text         string // the text saved
		want         string
	}{
		{"the read of the saved edit", "BySlot", false, driftProbeV2, "lookup failed"},
		{"the write of a new saved edit", "Create", false, driftProbeV2, "update failed"},
		{"the write of a new revision", "Revise", true, driftProbeV3, "update failed"},
		{"the discard of a saved edit", "Close", true, driftProbeV1, "update failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &saveFaults{}
			p := newPolicyRig(t, func(a *App) { f.Store, a.store = a.store, f })
			p.live(t, "drift-probe", driftProbeV1)
			if tc.open {
				p.mustPut(t, driftProbeV2, http.StatusOK)
			}
			dump := storeDump(t, p.app)
			method := tc.method
			f.fail.Store(&method)
			code, out := p.put(t, tc.text)
			f.fail.Store(nil)
			if code != http.StatusInternalServerError || out["error"] != tc.want || out["correlation_id"] == nil {
				t.Errorf("the save = %d %v; want 500 %q with a correlation id", code, out, tc.want)
			}
			if storeDump(t, p.app) != dump {
				t.Error("the failed save stored something")
			}
		})
	}
}

// TestPolicyApplyAgainReadsLiveState pins a save that runs again after
// another replica's write: the rerun reads the published text again, so a
// saved edit another replica opened on its own fresh publish between the
// two runs is kept and takes this save as its next revision, never
// discarded as stale by the base the first run read.
func TestPolicyApplyAgainReadsLiveState(t *testing.T) {
	t.Parallel()
	f := &saveFaults{}
	p := newPolicyRig(t, func(a *App) { f.Store, a.store = a.store, f })
	p.live(t, "drift-probe", driftProbeV1)
	lee := store.DraftActor{Name: "lee", Via: laneLogin, Client: clientLogin}
	var leeDraft store.DraftRow
	before := func() {
		publishText(t, p.app, "drift-probe", driftProbeV3)
		var err error
		leeDraft, err = f.Store.Drafts().Create(context.Background(), store.DraftRow{Door: "api", Slot: "policy:drift-probe"},
			[]store.DraftItemRow{{Kind: "PolicySet", Name: "drift-probe", Op: "put", Doc: driftProbeRefused,
				Base: publishedFingerprint("drift-probe", driftProbeV3), BaseOp: "put", BaseDoc: driftProbeV3}},
			store.DraftRevisionRow{Author: lee, Door: "api", Digest: "d"})
		if err != nil {
			t.Error(err)
		}
	}
	f.before.Store(&before)
	n := len(adminAuditEvents(t, p.app))

	p.mustPut(t, driftProbeV2, http.StatusOK)

	d, items := savedEditOf(t, p.app, "drift-probe")
	if d.ID != leeDraft.ID || d.Revision != 2 || len(items) != 1 || items[0].Doc != driftProbeV2 ||
		items[0].Base != publishedFingerprint("drift-probe", driftProbeV3) {
		t.Errorf("the saved edit is %+v holding %+v; want lee's draft %d at revision 2 with this save, on the base of V3", d, items, leeDraft.ID)
	}
	var saves []map[string]any
	for _, rec := range p.events(t, n) {
		if action, _ := rec["action"].(string); strings.HasPrefix(action, "draft.") {
			saves = append(saves, rec)
		}
	}
	if !sameActions(saves, "draft.update") {
		t.Errorf("the save recorded %v; want one draft.update and no discard", actions(saves))
	}
}

// TestPolicyActivateOfARiskySavedEditNamesItsDraft pins the second person
// on a saved edit: under admin.secondPerson an activate of a saved
// edit whose verdict holds a risk answers 409 naming the draft that another
// administrator publishes, since the edit is a draft already, and publishes
// nothing.
func TestPolicyActivateOfARiskySavedEditNamesItsDraft(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t, func(a *App) { a.cfg.Admin.SecondPerson = true })
	p.live(t, "drift-probe", driftProbeV1)
	recording := strings.Replace(driftProbeV2, "  rules:\n", "  capture:\n    conversations: true\n    mode: redact\n  rules:\n", 1)
	p.mustPut(t, recording, http.StatusOK)
	d, _ := savedEditOf(t, p.app, "drift-probe")
	gen := p.generation(t)

	code, out := p.activate(t, "drift-probe", "active")

	id := strconv.FormatInt(d.ID, 10)
	want := "This deployment needs a second person to publish a change that widens access (admin.secondPerson), and the saved edit of drift-probe is draft " +
		id + ". Ask another administrator to review and publish draft " + id + ", with strazactl drafts publish " + id + " or on the console under Drafts."
	if code != http.StatusConflict || out["error"] != want {
		t.Errorf("the risky activate = %d %v; want 409 %q", code, out, want)
	}
	if again, _ := savedEditOf(t, p.app, "drift-probe"); again.ID != d.ID || p.generation(t) != gen {
		t.Error("the refused activate published or closed the saved edit")
	}
}

// publishSaved publishes the saved edit d, whose items are items, as the
// publish route of another replica would.
func publishSaved(t *testing.T, app *App, d store.DraftRow, items []store.DraftItemRow) {
	t.Helper()
	ctx := context.Background()
	draft := draftOf(d, items, []drafts.Principal{principalOf(d.Proposer)})
	w, in, err := app.draftWorld(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	dp, err := planFor(w, in, draft, items)
	if err != nil {
		t.Fatal(err)
	}
	dp.plan.DraftID, dp.plan.Revision, dp.plan.Publisher, dp.plan.Acks = d.ID, d.Revision, store.DraftActor{Name: "lee", Via: laneLogin}, "{}"
	if _, end, err := app.commit(ctx, w, &dp); end != commitLanded || err != nil {
		t.Fatalf("publish the saved edit: %v, %v", end, err)
	}
}

// TestPolicyReadsAnswerTheSavedEdit pins the drift reads: while a set has an open
// saved edit, GET answers its text, priority, summary and time with drift
// true, the list answers the same fields and filters on them, and once it
// is published the drift goes.
func TestPolicyReadsAnswerTheSavedEdit(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	p.live(t, "drift-probe", driftProbeV1)
	row := rowOf(t, p.app, "drift-probe")
	var got policyPayload
	if code := adminReq(t, "GET", p.base+"/v1/admin/policies/drift-probe", p.tok, nil, &got); code != http.StatusOK || got.Drift || got.YAML != driftProbeV1 {
		t.Fatalf("GET before a save = %d %+v", code, got)
	}

	p.mustPut(t, driftProbeV3, http.StatusOK)
	d, _ := savedEditOf(t, p.app, "drift-probe")
	stamp := d.UpdatedAt.UTC().Format(time.RFC3339)
	got = policyPayload{}
	adminReq(t, "GET", p.base+"/v1/admin/policies/drift-probe", p.tok, nil, &got)
	if got.ID != row.ID || got.Status != "active" || got.YAML != driftProbeV3 || got.Priority != 130 || !got.Drift || got.UpdatedAt != stamp ||
		got.Summary == nil || got.Summary.Description != "The third text of the probe." {
		t.Errorf("GET with a saved edit = %+v; want V3's text, priority, summary and time with drift", got)
	}
	var list policyListResponse
	for _, query := range []string{"", "?q=third+text", "?role=probe-readers"} {
		list = policyListResponse{}
		if code := adminReq(t, "GET", p.base+"/v1/admin/policies"+query, p.tok, nil, &list); code != http.StatusOK {
			t.Fatalf("list %s = %d", query, code)
		}
		var item *policyPayload
		for i := range list.Items {
			if list.Items[i].Name == "drift-probe" {
				item = &list.Items[i]
			}
		}
		if item == nil || item.Priority != 130 || !item.Drift || item.UpdatedAt != stamp || item.Summary == nil || item.Summary.Rules != 1 {
			t.Errorf("list %s answers %+v; want drift-probe with V3's priority, time and summary and drift", query, item)
		}
	}

	p.mustActivate(t, "drift-probe", "active")
	got = policyPayload{}
	adminReq(t, "GET", p.base+"/v1/admin/policies/drift-probe", p.tok, nil, &got)
	if got.Drift || got.YAML != driftProbeV3 {
		t.Errorf("GET after the activate = %+v; want V3 with no drift", got)
	}
}
