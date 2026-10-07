package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// checkerAgent is the author of the drafts the checker tests store as the
// straza-app door writes them.
var checkerAgent = store.DraftActor{ID: "agent-1", Name: "bot", Agent: true, Via: laneSession, Client: "claude-code"}

// storeDoorDraft stores an unchecked straza-app draft of documents that
// expires at expires and answers its row.
func storeDoorDraft(t *testing.T, app *App, expires time.Time, documents ...string) store.DraftRow {
	t.Helper()
	items, fs := drafts.ParseBundle(documents)
	if len(fs) > 0 {
		t.Fatalf("the documents do not read: %+v", fs)
	}
	row, err := app.store.Drafts().Create(context.Background(), store.DraftRow{Door: string(drafts.DoorAgent), ExpiresAt: &expires},
		rowsOf(items), store.DraftRevisionRow{Author: checkerAgent, Digest: revisionDigest(items)})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// draftRow reads the draft id with its items.
func draftRow(t *testing.T, app *App, id int64) (store.DraftRow, []store.DraftItemRow) {
	t.Helper()
	row, items, err := app.store.Drafts().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row, items
}

// TestCheckerChecksUncheckedDrafts pins the checker's unchecked sweep: a
// draft of the straza-app door and a saved edit written by PUT are both
// stamped by one tick, the door's draft stores the verdict an agent reads
// under its own id, each stamp writes one draft.check with no actor, and a
// second tick writes none.
func TestCheckerChecksUncheckedDrafts(t *testing.T) {
	t.Parallel()
	p := newPolicyRig(t)
	ctx := context.Background()
	door := storeDoorDraft(t, p.app, time.Now().Add(time.Hour), draftApp("weather", "https://weather.example/mcp", "The weather."))
	p.live(t, "drift-probe", driftProbeV3)
	p.mustPut(t, driftProbeV3+"\n# saved, not published\n", http.StatusOK)
	saved, _, err := p.app.store.Drafts().BySlot(ctx, "policy:drift-probe")
	if err != nil {
		t.Fatalf("the saved edit: %v", err)
	}
	if saved.CheckedRevision == saved.Revision {
		t.Fatalf("the saved edit was checked at its save, which this test assumes it is not")
	}

	for range 2 {
		if err := p.app.checkDrafts(ctx); err != nil {
			t.Fatal(err)
		}
	}

	row, items := draftRow(t, p.app, door.ID)
	if row.CheckedRevision != row.Revision || row.CheckedAt == nil || row.CheckedSnapshot == "" {
		t.Errorf("the door's draft after the tick = checked %d of %d at %v", row.CheckedRevision, row.Revision, row.CheckedAt)
	}
	for _, it := range items {
		if it.BaseOp == "" {
			t.Errorf("the item %s/%s has no base after the tick", it.Kind, it.Name)
		}
	}
	var av drafts.AgentVerdict
	if err := json.Unmarshal([]byte(row.AgentVerdict), &av); err != nil || av.Draft != strconv.FormatInt(door.ID, 10) || !av.Checked {
		t.Errorf("the stored agent verdict = %q (%v)", row.AgentVerdict, err)
	}
	edit, _ := draftRow(t, p.app, saved.ID)
	if edit.CheckedRevision != edit.Revision || edit.AgentVerdict != "" {
		t.Errorf("the saved edit after the tick = checked %d of %d, agent verdict %q", edit.CheckedRevision, edit.Revision, edit.AgentVerdict)
	}
	for _, id := range []int64{door.ID, saved.ID} {
		recs := draftRecords(t, p.app, "draft.check", strconv.FormatInt(id, 10))
		if len(recs) != 1 {
			t.Errorf("draft %d has %d draft.check records after two ticks, want 1", id, len(recs))
			continue
		}
		for _, field := range []string{"actor", "actorId", "actorVia"} {
			if _, ok := recs[0][field]; ok {
				t.Errorf("draft %d's draft.check names %s", id, field)
			}
		}
	}
}

// expiryHook wraps a store so a test can revise a draft between the
// expiry sweep's list and its close.
type expiryHook struct {
	store.Store
	between atomic.Pointer[func()]
}

func (h *expiryHook) Drafts() store.DraftRepo { return expiryHookRepo{h.Store.Drafts(), h} }

type expiryHookRepo struct {
	store.DraftRepo
	h *expiryHook
}

func (r expiryHookRepo) ListExpirable(ctx context.Context, now time.Time, limit int) ([]store.DraftRow, error) {
	rows, err := r.DraftRepo.ListExpirable(ctx, now, limit)
	if f := r.h.between.Swap(nil); f != nil {
		(*f)()
	}
	return rows, err
}

// TestCheckerExpiresDrafts pins the checker's expiry sweep: a
// straza-app draft past its expiry closes as expired with one draft.expire
// and no actor, a draft before its expiry stays open, and a draft revised
// between the list and the close stays open at its new revision.
func TestCheckerExpiresDrafts(t *testing.T) {
	t.Parallel()
	h := &expiryHook{}
	app, _ := testAppPreRun(t, []func(*App){func(a *App) { h.Store, a.store = a.store, h }})
	ctx := context.Background()
	doc := draftApp("weather", "https://weather.example/mcp", "The weather.")
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	gone := storeDoorDraft(t, app, past, doc)
	kept := storeDoorDraft(t, app, future, doc)
	moved := storeDoorDraft(t, app, past, doc)
	revise := func() {
		items, _ := drafts.ParseBundle([]string{doc})
		later := time.Now().Add(draftExpiry)
		if _, err := app.store.Drafts().Revise(ctx, moved.ID, store.DraftRevise{From: 1, Items: rowsOf(items), ExpiresAt: &later,
			Rev: store.DraftRevisionRow{Author: checkerAgent, Door: string(drafts.DoorAgent)}}); err != nil {
			t.Error(err)
		}
	}
	h.between.Store(&revise)

	for range 2 {
		if err := app.checkDrafts(ctx); err != nil {
			t.Fatal(err)
		}
	}

	if row, _ := draftRow(t, app, gone.ID); row.State != "expired" || row.DecidedAt == nil || row.DecidedBy != (store.DraftActor{}) {
		t.Errorf("the draft past its expiry = %s decided %v by %+v", row.State, row.DecidedAt, row.DecidedBy)
	}
	if row, _ := draftRow(t, app, kept.ID); row.State != "open" {
		t.Errorf("the draft before its expiry is %s", row.State)
	}
	if row, _ := draftRow(t, app, moved.ID); row.State != "open" || row.Revision != 2 {
		t.Errorf("the draft revised after the list is %s at revision %d", row.State, row.Revision)
	}
	recs := draftRecords(t, app, "draft.expire", strconv.FormatInt(gone.ID, 10))
	if len(recs) != 1 || recs[0]["revision"] != float64(1) {
		t.Fatalf("draft.expire records = %v, want one at revision 1", recs)
	}
	for _, field := range []string{"actor", "actorId", "actorVia"} {
		if _, ok := recs[0][field]; ok {
			t.Errorf("draft.expire names %s", field)
		}
	}
	if recs := draftRecords(t, app, "draft.expire", strconv.FormatInt(moved.ID, 10)); len(recs) != 0 {
		t.Errorf("the revised draft has draft.expire records %v", recs)
	}
}
