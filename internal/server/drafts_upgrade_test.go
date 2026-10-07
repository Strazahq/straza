package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/store"
)

// cvSet is a policy set of the conversion tests. It matches roles, denies
// "cv-<name> *" with reason, and, when pool is not empty, holds "cv-hold *"
// for the approver pool. Every set denies its own command, so a decision
// on "cv-probe x" reads cv-saved alone.
func cvSet(name string, priority int, roles, reason, pool string) string {
	command := strings.TrimPrefix(name, "cv-")
	if name == "cv-saved" {
		command = "probe"
	}
	text := fmt.Sprintf(`apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: %s
spec:
  priority: %d
  match:
    roles: [%s]
  rules:
    - id: deny-%s
      tools: [shell.exec]
      command:
        denyPatterns: ["cv-%s *"]
      effect: deny
      reason: "Straza: %s"
`, name, priority, roles, command, command, reason)
	if pool != "" {
		text += fmt.Sprintf(`    - id: hold
      tools: [shell.exec]
      command:
        allowPatterns: ["cv-hold *"]
      effect: allow
      mode: approve
      approve:
        roles: [%s]
        timeoutSeconds: 300
      reason: "Straza: held"
`, pool)
	}
	return text
}

// The texts of the set with a saved edit: published, saved before the
// upgrade, and saved again by an old replica after it.
var (
	cvPublished = cvSet("cv-saved", 50, "cv-pub", "published", "cv-deciders")
	cvSaved     = cvSet("cv-saved", 60, "cv-pub, cv-saved-role", "saved", "")
	cvSavedNew  = cvSet("cv-saved", 70, "cv-pub", "saved again", "")
)

// activateTexts activates, straight in the store, the successor of the
// active snapshot with the named sets changed, as a publish of the release
// before drafts left it: no generation moves and no row is written. An
// empty text takes the set out.
func activateTexts(t *testing.T, app *App, changes map[string]string) string {
	t.Helper()
	ctx := context.Background()
	act, err := app.store.Snapshots().GetActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	in := map[string][]byte{}
	for name, text := range changes {
		in[name] = nil
		if text != "" {
			in[name] = []byte(text)
		}
	}
	b, err := app.snapshots.Build(ctx, act.ID, in)
	if err != nil {
		t.Fatalf("build the snapshot: %v", err)
	}
	if _, err := app.store.Snapshots().Create(ctx, store.Snapshot{ID: b.ID, SignerKeyID: b.SignerKeyID, Size: int64(len(b.Blob)), Blob: b.Blob}); err != nil && !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	if err := app.store.Snapshots().SetActive(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	return b.ID
}

// putRow stores a policy_sets row as a release before drafts left it.
func putRow(t *testing.T, app *App, name, status, text string, priority int) store.PolicySet {
	t.Helper()
	ps, err := app.store.Policies().Create(context.Background(), store.PolicySet{Name: name, Status: status, YAMLSource: text, Priority: priority})
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// textHash is the hex sha256 of text, the compiled_hash of a row that
// holds it.
func textHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// rowOf reads the row of the set name.
func rowOf(t *testing.T, app *App, name string) store.PolicySet {
	t.Helper()
	ps, err := app.store.Policies().GetByName(context.Background(), name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return ps
}

// convert runs the policy conversion and fails the test on an error.
func convert(t *testing.T, app *App) {
	t.Helper()
	if err := app.convertPolicies(context.Background()); err != nil {
		t.Fatalf("convertPolicies: %v", err)
	}
}

// markNow answers the mark the last settled conversion recorded.
func markNow(app *App) store.PolicyMark {
	app.configMu.Lock()
	defer app.configMu.Unlock()
	return app.policyMark
}

// storeDump is every policy row and every draft with its items and
// revisions, the whole of what the conversion may write.
func storeDump(t *testing.T, app *App) string {
	t.Helper()
	ctx := context.Background()
	rows, err := app.store.Policies().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := fmt.Sprintf("%+v\n", rows)
	list, err := app.store.Drafts().List(ctx, store.DraftFilter{}, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range list {
		_, items, err := app.store.Drafts().Get(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		revs, err := app.store.Drafts().Revisions(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		out += fmt.Sprintf("%+v %+v %+v\n", d, items, revs)
	}
	return out
}

// savedEditOf answers the open slot draft of the set name with its items,
// failing the test when there is none.
func savedEditOf(t *testing.T, app *App, name string) (store.DraftRow, []store.DraftItemRow) {
	t.Helper()
	row, items, err := app.store.Drafts().BySlot(context.Background(), "policy:"+name)
	if err != nil {
		t.Fatalf("the saved edit of %s: %v", name, err)
	}
	return row, items
}

// noSavedEdit fails the test when the set name has an open slot draft.
func noSavedEdit(t *testing.T, app *App, name string) {
	t.Helper()
	if row, _, err := app.store.Drafts().BySlot(context.Background(), "policy:"+name); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the set %s has the saved edit %+v (%v); want none", name, row, err)
	}
}

// publishedFingerprint is the live fingerprint of a set that is on with
// text, as a settled row gives it.
func publishedFingerprint(name, text string) string {
	return store.FingerprintPolicySet(store.PolicySet{Name: name, Status: "active", YAMLSource: text})
}

// seedSavedEdit stores cv-saved as a store before the upgrade holds it: the
// snapshot carries the published text and the row the saved one.
func seedSavedEdit(t *testing.T, app *App) {
	t.Helper()
	putRow(t, app, "cv-saved", "active", cvSaved, 60)
	activateTexts(t, app, map[string]string{"cv-saved": cvPublished})
}

// seedConversion stores six sets on top of
// cv-saved's saved edit: a live set without one, an off set, a row that
// says active while the snapshot lacks it, a set the snapshot carries whose
// row says draft, and a set the snapshot carries with no row. It answers
// the texts by name.
func seedConversion(t *testing.T, app *App) map[string]string {
	t.Helper()
	texts := map[string]string{
		"cv-live":   cvSet("cv-live", 40, "cv-pub", "live", ""),
		"cv-off":    cvSet("cv-off", 30, "cv-pub", "off", ""),
		"cv-stray":  cvSet("cv-stray", 20, "cv-pub", "stray", ""),
		"cv-rowoff": cvSet("cv-rowoff", 25, "cv-pub", "rowoff", ""),
		"cv-norow":  cvSet("cv-norow", 35, "cv-pub", "norow", ""),
	}
	putRow(t, app, "cv-saved", "active", cvSaved, 60)
	putRow(t, app, "cv-live", "active", texts["cv-live"], 40)
	putRow(t, app, "cv-off", "draft", texts["cv-off"], 30)
	putRow(t, app, "cv-stray", "active", texts["cv-stray"], 20)
	putRow(t, app, "cv-rowoff", "draft", texts["cv-rowoff"], 99)
	activateTexts(t, app, map[string]string{"cv-saved": cvPublished, "cv-live": texts["cv-live"],
		"cv-rowoff": texts["cv-rowoff"], "cv-norow": texts["cv-norow"]})
	return texts
}

// seedRoles creates the roles the sets of the conversion tests name.
func seedRoles(t *testing.T, app *App) {
	t.Helper()
	for name, kind := range map[string]string{"cv-pub": store.RoleKindApplication, "cv-saved-role": store.RoleKindApplication,
		"cv-deciders": store.RoleKindApprover} {
		if _, err := app.store.Roles().Create(context.Background(), store.Role{Name: name, Kind: kind, Plane: store.RolePlaneAccess}); err != nil {
			t.Fatal(err)
		}
	}
}

// b079Reads answers what three readers of stored policy text read for
// cv-saved: the reason simulate's draft overlay gives for
// "cv-probe x", whether the SCIM block of cv-saved-role lists cv-saved,
// and whether the role delete guard names cv-saved as a decider set.
func b079Reads(t *testing.T, app *App, base, tok string) (reason string, scim, guard bool) {
	t.Helper()
	ctx := context.Background()
	var out struct {
		Draft struct {
			Reason string `json:"reason"`
		} `json:"draft"`
	}
	body := map[string]any{"event": map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "cv-probe x"},
		"subject": map[string]any{"roles": []string{"cv-pub"}}, "draft": cvSet("cv-other", 10, "cv-pub", "other", "")}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/simulate", tok, body, &out); code != http.StatusOK {
		t.Fatalf("simulate = %d", code)
	}
	role, err := app.store.Roles().GetByName(ctx, "cv-saved-role")
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := app.groupRoleBlock(ctx, role)["policies"].([]string)
	naming, err := app.activeApprovePoolsNaming(ctx, "cv-deciders")
	if err != nil {
		t.Fatal(err)
	}
	return out.Draft.Reason, slices.Contains(listed, "cv-saved"), len(naming) > 0 && strings.Contains(strings.Join(naming, " "), "cv-saved")
}

// TestConversionSettlesEveryRowToTheSnapshot pins the conversion over six
// sets: the saved edit moves into one slot draft of the upgrade, every row
// comes to equal the snapshot with its hash, no audit record is written,
// each set's log line appears once, a second run writes nothing, and the
// readers of stored policy text read the published text afterwards.
func TestConversionSettlesEveryRowToTheSnapshot(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }})
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	seedRoles(t, app)
	texts := seedConversion(t, app)
	if reason, scim, guard := b079Reads(t, app, base, tok); reason != "Straza: saved" || !scim || guard {
		t.Fatalf("before the conversion the readers read %q, SCIM %v, guard %v; want the saved text, so the test tells the two apart", reason, scim, guard)
	}
	records := len(adminAuditEvents(t, app))
	buf.Reset()

	convert(t, app)

	want := map[string]struct {
		status, text string
		priority     int
	}{
		"cv-saved": {"active", cvPublished, 50}, "cv-live": {"active", texts["cv-live"], 40},
		"cv-off": {"draft", texts["cv-off"], 30}, "cv-stray": {"draft", texts["cv-stray"], 20},
		"cv-rowoff": {"active", texts["cv-rowoff"], 25}, "cv-norow": {"active", texts["cv-norow"], 35},
	}
	for name, w := range want {
		row := rowOf(t, app, name)
		if row.Status != w.status || row.YAMLSource != w.text || row.Priority != w.priority {
			t.Errorf("%s is %s with priority %d and text %q; want %s with priority %d and its published text", name, row.Status, row.Priority, row.YAMLSource, w.status, w.priority)
		}
	}
	for _, name := range []string{"cv-saved", "cv-stray", "cv-rowoff", "cv-norow"} {
		if row := rowOf(t, app, name); row.CompiledHash != textHash(row.YAMLSource) {
			t.Errorf("%s written by the conversion has compiled_hash %q; want the sha256 of its text", name, row.CompiledHash)
		}
	}
	d, items := savedEditOf(t, app, "cv-saved")
	wantItem := store.DraftItemRow{Seq: 1, Kind: "PolicySet", Name: "cv-saved", Op: "put", Doc: cvSaved,
		Base: publishedFingerprint("cv-saved", cvPublished), BaseOp: "put", BaseDoc: cvPublished}
	if d.Door != "api" || d.Note != savedEditNote || d.Proposer.Name != "strazad" || d.Proposer.Via != "upgrade" || d.Proposer.Client != "strazad" ||
		d.Revision != 1 || len(items) != 1 || items[0] != wantItem {
		t.Errorf("the saved edit is %+v holding %+v; want door api, the note, the upgrade as proposer and %+v", d, items, wantItem)
	}
	open, err := app.store.Drafts().List(context.Background(), store.DraftFilter{State: "open", SlotPrefix: "policy:"}, 0, 50)
	if err != nil || len(open) != 1 {
		t.Errorf("the open saved edits are %+v (%v); want cv-saved's alone", open, err)
	}
	if n := len(adminAuditEvents(t, app)); n != records {
		t.Errorf("the conversion wrote %d audit records; want none", n-records)
	}
	id := strconv.FormatInt(d.ID, 10)
	lines := []string{
		"moved the saved edit of policy set cv-saved into draft " + id + ", and the set keeps deciding with its published text",
		"set the stored status and priority of policy set cv-stray to match the published snapshot",
		"set the stored status and priority of policy set cv-rowoff to match the published snapshot",
		"inserted the missing row of policy set cv-norow from the published snapshot",
	}
	logged := buf.String()
	for _, line := range lines {
		if n := strings.Count(logged, line); n != 1 {
			t.Errorf("the log holds %q %d times; want once:\n%s", line, n, logged)
		}
	}
	if strings.Contains(logged, "cv-live") || strings.Contains(logged, "cv-off") {
		t.Errorf("the log names a set the conversion had nothing to do for:\n%s", logged)
	}

	before := storeDump(t, app)
	convert(t, app)
	convert(t, app)
	if after := storeDump(t, app); after != before {
		t.Errorf("a second run wrote:\nbefore %s\nafter  %s", before, after)
	}
	if buf.String() != logged {
		t.Errorf("a second run logged:\n%s", strings.TrimPrefix(buf.String(), logged))
	}
	if reason, scim, guard := b079Reads(t, app, base, tok); reason != "Straza: published" || scim || !guard {
		t.Errorf("after the conversion the readers read %q, SCIM %v, guard %v; want the published text", reason, scim, guard)
	}
}

// TestConversionRevisesTheOpenSlotDraft pins that a
// newer saved text an old replica writes into a converted row moves, at the
// next tick, into the open slot draft as its next revision, which keeps the
// base the item was stamped with.
func TestConversionRevisesTheOpenSlotDraft(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, _ := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }})
	seedSavedEdit(t, app)
	convert(t, app)
	row := rowOf(t, app, "cv-saved")
	row.YAMLSource, row.Priority = cvSavedNew, 70
	if _, err := app.store.Policies().Update(context.Background(), row); err != nil {
		t.Fatal(err)
	}

	if err := app.checkDrafts(context.Background()); err != nil {
		t.Fatalf("checkDrafts: %v", err)
	}

	if got := rowOf(t, app, "cv-saved"); got.YAMLSource != cvPublished || got.Priority != 50 {
		t.Errorf("the row holds priority %d and %q; want the published text", got.Priority, got.YAMLSource)
	}
	d, items := savedEditOf(t, app, "cv-saved")
	if d.Revision != 2 || len(items) != 1 || items[0].Doc != cvSavedNew || items[0].Base != publishedFingerprint("cv-saved", cvPublished) {
		t.Errorf("the saved edit is at revision %d holding %+v; want revision 2 with the newer text on the base it had", d.Revision, items)
	}
	revs, err := app.store.Drafts().Revisions(context.Background(), d.ID)
	if err != nil || len(revs) != 2 || revs[1].Author.Via != "upgrade" || revs[1].Door != "api" {
		t.Errorf("the revisions are %+v (%v); want a second one by the upgrade through door api", revs, err)
	}
	want := fmt.Sprintf("moved a newer saved edit of policy set cv-saved into draft %d as revision 2", d.ID)
	if !strings.Contains(buf.String(), want) {
		t.Errorf("the log lacks %q:\n%s", want, buf.String())
	}
}

// settleHook wraps a store so a test can act just before SettlePolicy,
// count the PolicyState reads, and hide the active snapshot from them.
type settleHook struct {
	store.Store
	before     atomic.Pointer[func()]
	states     atomic.Int32
	noSnapshot atomic.Bool
}

func (s *settleHook) Drafts() store.DraftRepo { return settleHookRepo{s.Store.Drafts(), s} }

type settleHookRepo struct {
	store.DraftRepo
	s *settleHook
}

func (r settleHookRepo) SettlePolicy(ctx context.Context, st store.PolicySettle) (bool, error) {
	if before := r.s.before.Swap(nil); before != nil {
		(*before)()
	}
	return r.DraftRepo.SettlePolicy(ctx, st)
}

func (r settleHookRepo) PolicyState(ctx context.Context) (store.PolicyState, error) {
	r.s.states.Add(1)
	st, err := r.DraftRepo.PolicyState(ctx)
	if r.s.noSnapshot.Load() {
		st.Snapshot = store.Snapshot{}
	}
	return st, err
}

// settleHooked boots an App on a settleHook and answers both.
func settleHooked(t *testing.T, log bool) (*App, *settleHook, *syncBuffer) {
	t.Helper()
	h := &settleHook{}
	logger, buf := captureLogger()
	app, _ := testAppPreRun(t, []func(*App){func(a *App) {
		h.Store, a.store = a.store, h
		if log {
			a.log = logger
		}
	}})
	return app, h, buf
}

// publishText publishes text as the set name through a one-item draft of
// the api door, as a replica publishes it, and answers the snapshot.
func publishText(t *testing.T, app *App, name, text string) string {
	t.Helper()
	d := drafts.Draft{Door: drafts.DoorAPI, Authors: []drafts.Principal{{Username: "kim", Via: laneLogin, Client: clientLogin}},
		Items: []drafts.Item{{Kind: drafts.KindPolicySet, Name: name, Op: drafts.OpPut, Doc: text}}}
	w, dp := planOf(t, app, d)
	rows := make([]store.DraftItemRow, len(dp.plan.Items))
	for i, it := range dp.plan.Items {
		rows[i] = store.DraftItemRow{Kind: it.Ref.Kind, Name: it.Ref.Name, Op: it.Op, Doc: it.AfterDoc, Base: it.Base, BaseOp: it.BaseOp, BaseDoc: it.BaseDoc}
	}
	kim := store.DraftActor{Name: "kim", Via: laneLogin, Client: clientLogin}
	dp.plan.New = &store.DraftNew{Row: store.DraftRow{Door: "api"}, Items: rows, Rev: store.DraftRevisionRow{Author: kim, Door: "api", Digest: "d"}}
	dp.plan.Publisher, dp.plan.Acks = kim, "{}"
	res, end, err := app.commit(context.Background(), w, &dp)
	if end != commitLanded || err != nil {
		t.Fatalf("publish %s: %v, %v", name, end, err)
	}
	return res.Snapshot
}

// publishedText answers the text the active snapshot carries for name, ""
// when it carries none.
func publishedText(t *testing.T, app *App, name string) string {
	t.Helper()
	w, err := app.readWorld(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return w.Policies[name].Text
}

// TestConversionYieldsToAPublishBetweenReadAndSettle pins that a publish of
// the live set that lands after the run's read and
// before its settle makes the settle write nothing, so the published text
// is never filed as a saved edit, the mark stays unrecorded, and the next
// tick changes nothing.
func TestConversionYieldsToAPublishBetweenReadAndSettle(t *testing.T) {
	t.Parallel()
	app, h, _ := settleHooked(t, false)
	seedSavedEdit(t, app)
	published := cvSet("cv-saved", 55, "cv-pub", "published again", "")
	mark := markNow(app)
	before := func() { publishText(t, app, "cv-saved", published) }
	h.before.Store(&before)

	convert(t, app)

	if row := rowOf(t, app, "cv-saved"); row.YAMLSource != published || row.Status != "active" {
		t.Errorf("the row is %s with %q; want the text the publish placed", row.Status, row.YAMLSource)
	}
	if got := publishedText(t, app, "cv-saved"); got != published {
		t.Errorf("the snapshot carries %q; want the text the publish placed", got)
	}
	noSavedEdit(t, app, "cv-saved")
	if got := markNow(app); got != mark {
		t.Errorf("the mark moved to %+v after a settle that wrote nothing; want %+v kept", got, mark)
	}
	dump := storeDump(t, app)
	convert(t, app)
	if after := storeDump(t, app); after != dump {
		t.Errorf("the next tick wrote:\nbefore %s\nafter  %s", dump, after)
	}
}

// TestConversionIgnoresAnOldReplicasActivate pins the rolling-upgrade case:
// an old replica activates the saved text after the run's
// read and before its settle, moving neither the generation nor the row.
// The settle writes nothing, so the text now published never lands in a
// slot draft as if it were an edit, and the next tick finds the row settled.
func TestConversionIgnoresAnOldReplicasActivate(t *testing.T) {
	t.Parallel()
	app, h, _ := settleHooked(t, false)
	seedSavedEdit(t, app)
	before := func() {
		activateTexts(t, app, map[string]string{"cv-saved": cvSaved})
		row := rowOf(t, app, "cv-saved")
		row.CompiledHash = textHash(cvSaved)
		if _, err := app.store.Policies().Update(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	h.before.Store(&before)

	convert(t, app)

	if row := rowOf(t, app, "cv-saved"); row.YAMLSource != cvSaved || row.Status != "active" {
		t.Errorf("the row is %s with %q; want the text the old replica published", row.Status, row.YAMLSource)
	}
	noSavedEdit(t, app, "cv-saved")
	dump := storeDump(t, app)
	convert(t, app)
	if after := storeDump(t, app); after != dump {
		t.Errorf("the next tick wrote:\nbefore %s\nafter  %s", dump, after)
	}
	noSavedEdit(t, app, "cv-saved")
}

// TestConversionTickWithAnUnchangedMarkReadsNothingMore pins that a tick
// whose mark did not move since the last settled run
// reads no PolicyState, and a row change makes the next tick read once.
func TestConversionTickWithAnUnchangedMarkReadsNothingMore(t *testing.T) {
	t.Parallel()
	app, h, _ := settleHooked(t, false)
	convert(t, app)
	if n := h.states.Load(); n != 0 {
		t.Fatalf("a tick after the boot's run read PolicyState %d times; want none", n)
	}
	putRow(t, app, "cv-off", "draft", cvSet("cv-off", 30, "cv-pub", "off", ""), 30)
	convert(t, app)
	convert(t, app)
	if n := h.states.Load(); n != 1 {
		t.Errorf("two ticks after one row change read PolicyState %d times; want once", n)
	}
}

// TestConversionWithoutActiveSnapshotSettlesNothing pins that a run whose
// read finds no active snapshot writes nothing and warns, since every set
// would read as off, and records no mark.
func TestConversionWithoutActiveSnapshotSettlesNothing(t *testing.T) {
	t.Parallel()
	app, h, buf := settleHooked(t, true)
	seedSavedEdit(t, app)
	h.noSnapshot.Store(true)
	mark := markNow(app)
	dump := storeDump(t, app)

	convert(t, app)

	if after := storeDump(t, app); after != dump {
		t.Errorf("the run wrote with no active snapshot:\nbefore %s\nafter  %s", dump, after)
	}
	if got := markNow(app); got != mark {
		t.Errorf("the mark moved to %+v; want %+v kept", got, mark)
	}
	if want := "policy conversion: no policy snapshot is active"; !strings.Contains(buf.String(), want) {
		t.Errorf("the log lacks %q:\n%s", want, buf.String())
	}
}

// TestBootConvertsSavedEdits pins the boot run: a replica built over a
// store that holds a saved edit in its row has moved it into the set's
// slot draft before build returns, before its watcher or listener serve.
func TestBootConvertsSavedEdits(t *testing.T) {
	t.Parallel()
	a, _ := testApp(t)
	seedSavedEdit(t, a)
	cfg := a.cfg
	cfg.DataDir, cfg.Server.Listen = t.TempDir(), "127.0.0.1:0"
	cfg.Secrets.KEKFile = a.cfg.KEKFile()
	ctx, cancel := context.WithCancel(context.Background())
	b, err := build(ctx, cfg, logging.New(cfg.Log, io.Discard), keptStore{a.store})
	if err != nil {
		cancel()
		t.Fatalf("build: %v", err)
	}
	if row := rowOf(t, a, "cv-saved"); row.YAMLSource != cvPublished {
		t.Errorf("after the boot the row holds %q; want the published text", row.YAMLSource)
	}
	if _, items := savedEditOf(t, a, "cv-saved"); len(items) != 1 || items[0].Doc != cvSaved {
		t.Errorf("after the boot the saved edit holds %+v; want the saved text", items)
	}
	if markNow(b) == (store.PolicyMark{}) {
		t.Error("the boot run recorded no mark for the checker")
	}
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	cancel()
	<-done
}
