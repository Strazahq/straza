package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

var (
	draftAlice = DraftActor{ID: "u-alice", Name: "alice", Via: "session", Client: "console"}
	draftBob   = DraftActor{ID: "u-bob", Name: "bob", Via: "api-token", Client: "api-token"}
	draftAgent = DraftActor{ID: "u-joe", Name: "joe-agent", Via: "session", Client: "claude-code", Agent: true,
		SponsorID: "u-alice", SponsorName: "alice"}
	draftCheckAt = time.Date(2026, 9, 24, 10, 0, 0, 123456000, time.UTC)
)

func draftItem(kind, name, op, doc string) DraftItemRow {
	return DraftItemRow{Kind: kind, Name: name, Op: op, Doc: doc}
}

// stampedItem is a put item whose base the caller stamped.
func stampedItem(kind, name, doc, base string) DraftItemRow {
	it := draftItem(kind, name, "put", doc)
	it.Base, it.BaseOp, it.BaseDoc = base, "put", "live "+name
	return it
}

// createDraft stores an open draft through Create, door console unless d
// names one, and fails the test on any error.
func createDraft(t *testing.T, s Store, d DraftRow, by DraftActor, items ...DraftItemRow) DraftRow {
	t.Helper()
	if d.Door == "" {
		d.Door = "console"
	}
	row, err := s.Drafts().Create(context.Background(), d, items, DraftRevisionRow{Author: by, Digest: "digest-1"})
	if err != nil {
		t.Fatalf("create a draft: %v", err)
	}
	return row
}

func getDraft(t *testing.T, s Store, id int64) (DraftRow, []DraftItemRow) {
	t.Helper()
	d, items, err := s.Drafts().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get draft %d: %v", id, err)
	}
	return d, items
}

func draftIDs(rows []DraftRow) []int64 {
	out := []int64{}
	for _, d := range rows {
		out = append(out, d.ID)
	}
	return out
}

// markPublished writes what a publish leaves on a draft, its state and its
// change rows, so that the readers of published drafts are tested on their
// own.
func markPublished(t *testing.T, s Store, id int64, at time.Time, changes ...DraftChangeRow) {
	t.Helper()
	ctx := context.Background()
	sq := s.(*sqlStore)
	if _, err := sq.exec(ctx, `UPDATE drafts SET state = 'published', decided_at = $1, decided_by_id = $2,
		decided_by_name = $3 WHERE id = $4`, sq.tArg(at), draftAlice.ID, draftAlice.Name, id); err != nil {
		t.Fatalf("mark draft %d published: %v", id, err)
	}
	for i, c := range changes {
		if _, err := sq.exec(ctx, `INSERT INTO draft_changes (draft_id, seq, kind, name, implied, before_op, before_doc,
			before_fp, after_op, after_doc, after_fp) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			id, i+1, c.Kind, c.Name, c.Implied, c.BeforeOp, c.BeforeDoc, c.BeforeFP, c.AfterOp, c.AfterDoc, c.AfterFP); err != nil {
			t.Fatalf("record a change of draft %d: %v", id, err)
		}
	}
}

// TestDraftCreateStoresRevisionOne pins what Create stores: an open draft
// at revision 1 whose proposer is the author of revision 1, the items in
// the order given, and the revision row through the draft's door.
func TestDraftCreateStoresRevisionOne(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		expires := time.Date(2026, 10, 8, 10, 0, 0, 123456000, time.UTC)
		d := createDraft(t, s, DraftRow{Door: "straza-app", Note: "Read access to GitHub.", ExpiresAt: &expires}, draftAgent,
			draftItem("Role", "github-readers", "put", "kind: Role"), draftItem("App", "github", "put", "{}"))
		if d.ID <= 0 || d.Revision != 1 || d.State != "open" || d.Door != "straza-app" || d.Proposer != draftAgent {
			t.Fatalf("created %+v, want an id, revision 1, open, door straza-app and the agent as its proposer", d)
		}
		if d.ExpiresAt == nil || !d.ExpiresAt.Equal(expires) || d.Note != "Read access to GitHub." || d.CreatedAt.IsZero() {
			t.Errorf("created %+v, want the expiry, the note and a creation time", d)
		}
		got, items := getDraft(t, s, d.ID)
		if !reflect.DeepEqual(got, d) {
			t.Errorf("Get answers %+v, Create answered %+v", got, d)
		}
		want := []DraftItemRow{
			{Seq: 1, Kind: "Role", Name: "github-readers", Op: "put", Doc: "kind: Role"},
			{Seq: 2, Kind: "App", Name: "github", Op: "put", Doc: "{}"},
		}
		if !reflect.DeepEqual(items, want) {
			t.Errorf("items = %+v, want %+v", items, want)
		}
		revs, err := s.Drafts().Revisions(ctx, d.ID)
		if err != nil || len(revs) != 1 {
			t.Fatalf("revisions = %+v, %v; want one", revs, err)
		}
		if r := revs[0]; r.Revision != 1 || r.Author != draftAgent || r.Door != "straza-app" || r.Digest != "digest-1" ||
			!r.CreatedAt.Equal(d.CreatedAt) {
			t.Errorf("revision 1 = %+v, want the agent through straza-app with digest-1 at the creation time", r)
		}
	})
}

// TestDraftCreateRefusesASecondOpenDraftOfASlotOrSource pins the partial
// unique indexes: one open draft per slot, and one per apps directory path
// and content hash. A closed draft frees its slot.
func TestDraftCreateRefusesASecondOpenDraftOfASlotOrSource(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		tests := []struct {
			name          string
			first, second DraftRow
			conflict      bool
		}{
			{"the same slot", DraftRow{Slot: "working:u-alice"}, DraftRow{Slot: "working:u-alice"}, true},
			{"the same source and hash", DraftRow{Door: "apps-directory", Source: "a.yaml", SourceHash: "h1"},
				DraftRow{Door: "apps-directory", Source: "a.yaml", SourceHash: "h1"}, true},
			{"the same source with another hash", DraftRow{Door: "apps-directory", Source: "b.yaml", SourceHash: "h1"},
				DraftRow{Door: "apps-directory", Source: "b.yaml", SourceHash: "h2"}, false},
			{"another slot", DraftRow{Slot: "policy:a"}, DraftRow{Slot: "policy:b"}, false},
			{"neither slot nor source", DraftRow{}, DraftRow{}, false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				first := createDraft(t, s, tc.first, draftAlice)
				second := tc.second
				if second.Door == "" {
					second.Door = "console"
				}
				_, err := s.Drafts().Create(ctx, second, nil, DraftRevisionRow{Author: draftBob, Digest: "d"})
				if got := errors.Is(err, ErrConflict); got != tc.conflict {
					t.Fatalf("second Create conflicts = %v (%v), want %v", got, err, tc.conflict)
				}
				if !tc.conflict {
					return
				}
				if ok, err := s.Drafts().Close(ctx, first.ID, 0, "discarded", draftAlice, "", draftCheckAt); err != nil || !ok {
					t.Fatalf("close the first draft: %v, %v", ok, err)
				}
				if _, err := s.Drafts().Create(ctx, second, nil, DraftRevisionRow{Author: draftBob, Digest: "d"}); err != nil {
					t.Errorf("Create after the first draft closed: %v, want the slot free", err)
				}
			})
		}
	})
}

// TestDraftCreateRefusesAnObjectNamedTwice pins that a duplicate item is
// an error of its own, never the ErrConflict of a taken slot, and that
// nothing is stored.
func TestDraftCreateRefusesAnObjectNamedTwice(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		_, err := s.Drafts().Create(ctx, DraftRow{Door: "console"},
			[]DraftItemRow{draftItem("App", "demo", "put", "{}"), draftItem("App", "demo", "remove", "")},
			DraftRevisionRow{Author: draftAlice, Digest: "d"})
		if err == nil || errors.Is(err, ErrConflict) {
			t.Fatalf("Create with App/demo twice = %v, want an error that is not ErrConflict", err)
		}
		if rows, err := s.Drafts().List(ctx, DraftFilter{}, 0, 10); err != nil || len(rows) != 0 {
			t.Errorf("drafts after the refusal = %d (%v), want none", len(rows), err)
		}
	})
}

// TestDraftCreateRefusesACheckedDraftWithAnUnstampedItem pins that a draft
// stored as checked has a base for every item, because nothing would stamp
// a missing one later.
func TestDraftCreateRefusesACheckedDraftWithAnUnstampedItem(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		checked := DraftRow{Door: "console", CheckedRevision: 1, CheckedAt: &draftCheckAt, CheckCounts: `{"risks":1}`}
		rev := DraftRevisionRow{Author: draftAlice, Digest: "d"}
		items := []DraftItemRow{stampedItem("App", "demo", "{}", "fp"), draftItem("Role", "demo-readers", "put", "kind: Role")}
		if _, err := s.Drafts().Create(ctx, checked, items, rev); err == nil {
			t.Fatal("Create stored a checked draft whose Role/demo-readers has no base")
		}
		d, err := s.Drafts().Create(ctx, checked, items[:1], rev)
		if err != nil {
			t.Fatalf("Create a checked draft with every item stamped: %v", err)
		}
		if d.CheckedRevision != 1 || d.CheckCounts != `{"risks":1}` || d.CheckedAt == nil || !d.CheckedAt.Equal(draftCheckAt) {
			t.Errorf("checked draft = %+v, want checked at revision 1 with its counts and time", d)
		}
		if rows, err := s.Drafts().List(ctx, DraftFilter{}, 0, 10); err != nil || len(rows) != 1 {
			t.Errorf("drafts = %d (%v), want only the stamped one", len(rows), err)
		}
	})
}

// TestDraftReviseRefusesAMovedRevision pins the compare-and-set: a revise
// from a revision the draft left, or of a draft that is not open, answers
// ErrConflict with the draft as it stands and writes nothing, and an
// unknown id answers ErrNotFound.
func TestDraftReviseRefusesAMovedRevision(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.Drafts()
		d := createDraft(t, s, DraftRow{}, draftAlice, draftItem("App", "demo", "put", "v1"))
		revise := func(from int, doc string) (DraftRow, error) {
			return repo.Revise(ctx, d.ID, DraftRevise{From: from, Items: []DraftItemRow{draftItem("App", "demo", "put", doc)},
				Rev: DraftRevisionRow{Author: draftBob, Door: "strazactl", Digest: doc}})
		}
		if got, err := revise(1, "v2"); err != nil || got.Revision != 2 {
			t.Fatalf("revise from 1 = %+v, %v; want revision 2", got, err)
		}
		cur, err := revise(1, "stale")
		if !errors.Is(err, ErrConflict) || cur.Revision != 2 || cur.State != "open" {
			t.Fatalf("revise from 1 again = %+v, %v; want ErrConflict with the draft at revision 2", cur, err)
		}
		if _, items := getDraft(t, s, d.ID); items[0].Doc != "v2" {
			t.Errorf("the refused revise wrote the item %q, want v2 kept", items[0].Doc)
		}
		if revs, _ := repo.Revisions(ctx, d.ID); len(revs) != 2 {
			t.Errorf("revisions after the refused revise = %d, want 2", len(revs))
		}
		if _, err := repo.Close(ctx, d.ID, 0, "discarded", draftAlice, "", draftCheckAt); err != nil {
			t.Fatal(err)
		}
		if cur, err := revise(2, "late"); !errors.Is(err, ErrConflict) || cur.State != "discarded" {
			t.Errorf("revise of a discarded draft = %+v, %v; want ErrConflict with the draft discarded", cur, err)
		}
		if _, err := repo.Revise(ctx, d.ID+100, DraftRevise{From: 1}); !errors.Is(err, ErrNotFound) {
			t.Errorf("revise of an unknown draft = %v, want ErrNotFound", err)
		}
	})
}

// TestDraftReviseKeepsTheBasesOfStampedItems pins the base rule: an item
// the stored revision held and stamped keeps its base whatever its new
// document says, unless the revise is a rebase. Every other item takes the
// base it is given, a held item never stamped included, and a held item
// keeps the tools a Contact read for it.
func TestDraftReviseKeepsTheBasesOfStampedItems(t *testing.T) {
	stored := []DraftItemRow{
		stampedItem("App", "a", "doc-a", "fp-a"),
		draftItem("App", "b", "put", "doc-b"),
		stampedItem("App", "c", "doc-c", "fp-c"),
	}
	stored[0].Offered = `{"tools":["get_me"]}`
	revised := []DraftItemRow{
		{Kind: "App", Name: "a", Op: "put", Doc: "doc-a2", Base: "given-a", BaseOp: "remove"},
		stampedItem("App", "b", "doc-b2", "given-b"),
		stampedItem("App", "d", "doc-d", "given-d"),
		draftItem("App", "e", "put", "doc-e"),
	}
	tests := []struct {
		name              string
		rebase            bool
		wantBases         []string
		wantAOp, wantADoc string
	}{
		{"a revision", false, []string{"fp-a", "given-b", "given-d", ""}, "put", "live a"},
		{"a rebase", true, []string{"given-a", "given-b", "given-d", ""}, "remove", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				d := createDraft(t, s, DraftRow{}, draftAlice, stored...)
				if _, err := s.Drafts().Revise(context.Background(), d.ID, DraftRevise{From: 1, Items: revised, Rebase: tc.rebase,
					Rev: DraftRevisionRow{Author: draftAlice, Door: "console", Digest: "d2"}}); err != nil {
					t.Fatalf("revise: %v", err)
				}
				_, items := getDraft(t, s, d.ID)
				if len(items) != len(revised) {
					t.Fatalf("items = %+v, want the %d revised ones, App/c dropped", items, len(revised))
				}
				for i, it := range items {
					if it.Seq != i+1 || it.Name != revised[i].Name || it.Doc != revised[i].Doc || it.Base != tc.wantBases[i] {
						t.Errorf("item %d = %+v, want %s with doc %s and base %q", i+1, it, revised[i].Name, revised[i].Doc, tc.wantBases[i])
					}
				}
				if items[0].Offered != stored[0].Offered || items[2].Offered != "" {
					t.Errorf("offered = %q and %q, want App/a's kept and none for the new App/d", items[0].Offered, items[2].Offered)
				}
				if items[0].BaseOp != tc.wantAOp || items[0].BaseDoc != tc.wantADoc {
					t.Errorf("App/a base op and document = %q %q, want %q %q", items[0].BaseOp, items[0].BaseDoc, tc.wantAOp, tc.wantADoc)
				}
			})
		})
	}
}

// TestDraftReviseKeepsNoteAndExpiryUnlessGiven pins that a nil note or
// expiry keeps the stored value and a given one replaces it.
func TestDraftReviseKeepsNoteAndExpiryUnlessGiven(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		first := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		d := createDraft(t, s, DraftRow{Note: "first", ExpiresAt: &first}, draftAlice)
		rev := DraftRevisionRow{Author: draftAlice, Door: "console", Digest: "d"}
		got, err := s.Drafts().Revise(ctx, d.ID, DraftRevise{From: 1, Rev: rev})
		if err != nil || got.Note != "first" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(first) {
			t.Fatalf("revise without note or expiry = %+v, %v; want both kept", got, err)
		}
		note, later := "second", first.Add(14*24*time.Hour)
		got, err = s.Drafts().Revise(ctx, d.ID, DraftRevise{From: 2, Rev: rev, Note: &note, ExpiresAt: &later})
		if err != nil || got.Note != "second" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(later) {
			t.Errorf("revise with a note and an expiry = %+v, %v; want both replaced", got, err)
		}
	})
}

// TestDraftReviseRecordsTheCheckOnlyWhenGiven pins that a revise with a
// check records it against the new revision, and one without leaves the
// new revision unchecked.
func TestDraftReviseRecordsTheCheckOnlyWhenGiven(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.Drafts()
		d := createDraft(t, s, DraftRow{}, draftAlice, stampedItem("App", "demo", "v1", "fp"))
		rev := DraftRevisionRow{Author: draftAlice, Door: "console", Digest: "d"}
		items := []DraftItemRow{draftItem("App", "demo", "put", "v2")}
		check := &DraftCheck{Snapshot: "snap-1", Counts: `{"risks":2}`, AgentVerdict: "{}", At: draftCheckAt}
		got, err := repo.Revise(ctx, d.ID, DraftRevise{From: 1, Items: items, Rev: rev, Checked: check})
		if err != nil {
			t.Fatal(err)
		}
		if got.CheckedRevision != 2 || got.CheckedSnapshot != "snap-1" || got.CheckCounts != `{"risks":2}` ||
			got.AgentVerdict != "{}" || got.CheckedAt == nil || !got.CheckedAt.Equal(draftCheckAt) {
			t.Errorf("revise with a check = %+v, want revision 2 checked with the snapshot, counts, verdict and time", got)
		}
		got, err = repo.Revise(ctx, d.ID, DraftRevise{From: 2, Items: items, Rev: rev})
		if err != nil || got.Revision != 3 || got.CheckedRevision != 2 {
			t.Fatalf("revise without a check = %+v, %v; want revision 3 with the check of revision 2 kept", got, err)
		}
		if rows, err := repo.ListUnchecked(ctx, 10); err != nil || !reflect.DeepEqual(draftIDs(rows), []int64{d.ID}) {
			t.Errorf("unchecked drafts = %v (%v), want the revised one", draftIDs(rows), err)
		}
	})
}

// TestDraftReviseRefusesACheckWithAnUnstampedItem pins that a revise which
// records a check leaves no item without a base, and writes nothing when it
// would.
func TestDraftReviseRefusesACheckWithAnUnstampedItem(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		d := createDraft(t, s, DraftRow{}, draftAlice, stampedItem("App", "demo", "v1", "fp"))
		_, err := s.Drafts().Revise(ctx, d.ID, DraftRevise{From: 1,
			Items:   []DraftItemRow{draftItem("App", "demo", "put", "v2"), draftItem("Role", "new", "put", "kind: Role")},
			Rev:     DraftRevisionRow{Author: draftAlice, Door: "console", Digest: "d"},
			Checked: &DraftCheck{Snapshot: "snap", Counts: "{}", At: draftCheckAt}})
		if err == nil || errors.Is(err, ErrConflict) {
			t.Fatalf("revise with a check and an unstamped item = %v, want an error that is not ErrConflict", err)
		}
		if got, items := getDraft(t, s, d.ID); got.Revision != 1 || len(items) != 1 || items[0].Doc != "v1" {
			t.Errorf("draft after the refusal = %+v with %+v, want revision 1 untouched", got, items)
		}
	})
}

// TestDraftStampFillsOnlyUnstampedItems pins that a stamp writes the base
// of every item without one and the check, and leaves stamped items alone.
func TestDraftStampFillsOnlyUnstampedItems(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		d := createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent,
			stampedItem("App", "a", "doc-a", "fp-a"), draftItem("Role", "b", "put", "doc-b"))
		bases := []DraftItemRow{
			{Kind: "App", Name: "a", Base: "other", BaseOp: "remove"},
			{Kind: "Role", Name: "b", Base: "fp-b", BaseOp: "put", BaseDoc: "live b"},
		}
		c := DraftCheck{Snapshot: "snap-1", Counts: `{"refused":1}`, AgentVerdict: `{"ok":false}`, At: draftCheckAt}
		ok, err := s.Drafts().Stamp(ctx, d.ID, 1, bases, c)
		if err != nil || !ok {
			t.Fatalf("stamp = %v, %v; want it to land", ok, err)
		}
		got, items := getDraft(t, s, d.ID)
		if items[0].Base != "fp-a" || items[0].BaseOp != "put" {
			t.Errorf("stamped App/a = %+v, want its base fp-a kept", items[0])
		}
		if items[1].Base != "fp-b" || items[1].BaseOp != "put" || items[1].BaseDoc != "live b" {
			t.Errorf("unstamped Role/b = %+v, want the given base", items[1])
		}
		if got.CheckedRevision != 1 || got.CheckedSnapshot != "snap-1" || got.CheckCounts != `{"refused":1}` ||
			got.AgentVerdict != `{"ok":false}` || got.CheckedAt == nil || !got.CheckedAt.Equal(draftCheckAt) {
			t.Errorf("draft after the stamp = %+v, want the check of revision 1", got)
		}
	})
}

// TestDraftStampLandsOncePerRevision pins that a stamp reports false and
// writes nothing for a revision already checked, a revision the draft
// left, a draft that is not open, and an unknown draft.
func TestDraftStampLandsOncePerRevision(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.Drafts()
		bases := []DraftItemRow{{Kind: "App", Name: "a", Base: "fp", BaseOp: "put"}}
		fresh := func() DraftRow {
			return createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent, draftItem("App", "a", "put", "doc"))
		}
		checked := fresh()
		if ok, err := repo.Stamp(ctx, checked.ID, 1, bases, DraftCheck{Snapshot: "first", At: draftCheckAt}); err != nil || !ok {
			t.Fatalf("first stamp = %v, %v", ok, err)
		}
		moved := fresh()
		if _, err := repo.Revise(ctx, moved.ID, DraftRevise{From: 1, Items: []DraftItemRow{draftItem("App", "a", "put", "doc2")},
			Rev: DraftRevisionRow{Author: draftAgent, Door: "straza-app", Digest: "d"}}); err != nil {
			t.Fatal(err)
		}
		closed := fresh()
		if _, err := repo.Close(ctx, closed.ID, 0, "expired", DraftActor{}, "", draftCheckAt); err != nil {
			t.Fatal(err)
		}
		tests := []struct {
			name     string
			id       int64
			revision int
		}{
			{"a revision already checked", checked.ID, 1},
			{"a revision the draft left", moved.ID, 1},
			{"a draft that is not open", closed.ID, 1},
			{"an unknown draft", closed.ID + 100, 1},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				ok, err := repo.Stamp(ctx, tc.id, tc.revision, bases, DraftCheck{Snapshot: "second", At: draftCheckAt})
				if err != nil || ok {
					t.Errorf("stamp = %v, %v; want false and no error", ok, err)
				}
			})
		}
		if got, _ := getDraft(t, s, checked.ID); got.CheckedSnapshot != "first" {
			t.Errorf("the checked draft reads snapshot %q, want the first stamp's", got.CheckedSnapshot)
		}
		if got, items := getDraft(t, s, moved.ID); got.CheckedRevision != 0 || items[0].BaseOp != "" {
			t.Errorf("the moved draft reads %+v with %+v, want nothing stamped", got, items[0])
		}
	})
}

// TestDraftStampRefusesAMissingBase pins that a stamp missing the base of an
// item not yet stamped fails and writes nothing, since a check lands once
// per revision and would leave that item without a base for good.
func TestDraftStampRefusesAMissingBase(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		d := createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent,
			draftItem("App", "a", "put", "doc-a"), draftItem("Role", "b", "put", "doc-b"))
		bases := []DraftItemRow{{Kind: "App", Name: "a", Base: "fp-a", BaseOp: "put"}}
		if ok, err := s.Drafts().Stamp(ctx, d.ID, 1, bases, DraftCheck{Snapshot: "snap", At: draftCheckAt}); err == nil || ok {
			t.Fatalf("stamp without Role/b's base = %v, %v; want an error", ok, err)
		}
		if got, items := getDraft(t, s, d.ID); got.CheckedRevision != 0 || items[0].BaseOp != "" {
			t.Errorf("draft after the failed stamp = %+v with %+v, want nothing written", got, items)
		}
	})
}

// TestDraftCountOpenSkipsSlotDrafts pins the drafts that count against a
// proposer's limit: open, theirs and without a slot.
func TestDraftCountOpenSkipsSlotDrafts(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		createDraft(t, s, DraftRow{}, draftAgent)
		createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent)
		createDraft(t, s, DraftRow{Slot: "working:u-joe"}, draftAgent)
		createDraft(t, s, DraftRow{Slot: "policy:guard"}, draftAgent)
		createDraft(t, s, DraftRow{}, draftBob)
		closed := createDraft(t, s, DraftRow{}, draftAgent)
		if _, err := s.Drafts().Close(ctx, closed.ID, 0, "discarded", draftAgent, "", draftCheckAt); err != nil {
			t.Fatal(err)
		}
		if n, err := s.Drafts().CountOpen(ctx, draftAgent.ID); err != nil || n != 2 {
			t.Errorf("CountOpen = %d, %v; want the 2 open drafts without a slot", n, err)
		}
	})
}

// TestDraftCloseEndsADraftOnce pins Close: it ends an open draft at a
// matching revision, or at any revision for 0, records who decided, and
// reports false for a draft already closed or at another revision.
func TestDraftCloseEndsADraftOnce(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.Drafts()
		tests := []struct {
			name     string
			revision int
			state    string
			want     bool
		}{
			{"any revision", 0, "discarded", true},
			{"the draft's revision", 1, "expired", true},
			{"another revision", 2, "discarded", false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				d := createDraft(t, s, DraftRow{}, draftAlice)
				ok, err := repo.Close(ctx, d.ID, tc.revision, tc.state, draftBob, "not needed", draftCheckAt)
				if err != nil || ok != tc.want {
					t.Fatalf("close = %v, %v; want %v", ok, err, tc.want)
				}
				got, _ := getDraft(t, s, d.ID)
				if !tc.want {
					if got.State != "open" {
						t.Errorf("state = %s, want open", got.State)
					}
					return
				}
				if got.State != tc.state || got.DecidedBy != draftBob || got.DecidedReason != "not needed" ||
					got.DecidedAt == nil || !got.DecidedAt.Equal(draftCheckAt) {
					t.Errorf("closed draft = %+v, want %s by bob with the reason and time", got, tc.state)
				}
				if again, err := repo.Close(ctx, d.ID, 0, "discarded", draftAlice, "", draftCheckAt); err != nil || again {
					t.Errorf("second close = %v, %v; want false", again, err)
				}
			})
		}
		d := createDraft(t, s, DraftRow{}, draftAlice)
		if _, err := repo.Close(ctx, d.ID, 0, "published", draftAlice, "", draftCheckAt); err == nil {
			t.Error("Close marked a draft published, which only a publish may do")
		}
	})
}

// TestDraftListFilters pins every filter of List, each alone and two
// together, newest first.
func TestDraftListFilters(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.Drafts()
		console := createDraft(t, s, DraftRow{}, draftAlice, draftItem("App", "github", "put", "{}"))
		agent := createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent, draftItem("Role", "github", "put", "r"))
		file := createDraft(t, s, DraftRow{Door: "apps-directory", Source: "github.yaml", SourceHash: "h"}, draftAlice,
			draftItem("App", "github", "remove", ""))
		working := createDraft(t, s, DraftRow{Slot: "working:u-alice"}, draftAlice, draftItem("PolicySet", "guard", "off", "t"))
		saved := createDraft(t, s, DraftRow{Slot: "policy:guard"}, draftBob, draftItem("PolicySet", "guard", "put", "t"))
		if _, err := repo.Revise(ctx, agent.ID, DraftRevise{From: 1, Items: []DraftItemRow{draftItem("Role", "github", "put", "r2")},
			Rev: DraftRevisionRow{Author: draftBob, Door: "console", Digest: "d"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Close(ctx, file.ID, 0, "discarded", draftAlice, "", draftCheckAt); err != nil {
			t.Fatal(err)
		}
		tests := []struct {
			name string
			f    DraftFilter
			want []int64
		}{
			{"none", DraftFilter{}, []int64{saved.ID, working.ID, file.ID, agent.ID, console.ID}},
			{"state", DraftFilter{State: "discarded"}, []int64{file.ID}},
			{"door", DraftFilter{Door: "straza-app"}, []int64{agent.ID}},
			{"proposer", DraftFilter{ProposerID: "u-bob"}, []int64{saved.ID}},
			{"a later author", DraftFilter{AuthorID: "u-bob"}, []int64{saved.ID, agent.ID}},
			{"source", DraftFilter{Source: "github.yaml"}, []int64{file.ID}},
			{"a slot prefix", DraftFilter{SlotPrefix: "working:"}, []int64{working.ID}},
			{"a whole slot", DraftFilter{SlotPrefix: "policy:guard"}, []int64{saved.ID}},
			{"a slot prefix in another case", DraftFilter{SlotPrefix: "WORKING:"}, []int64{}},
			{"an object", DraftFilter{Object: ObjectRef{Kind: "App", Name: "github"}}, []int64{file.ID, console.ID}},
			{"an object's name", DraftFilter{Object: ObjectRef{Name: "github"}}, []int64{file.ID, agent.ID, console.ID}},
			{"an object's kind", DraftFilter{Object: ObjectRef{Kind: "PolicySet"}}, []int64{saved.ID, working.ID}},
			{"state and author", DraftFilter{State: "open", AuthorID: "u-alice"}, []int64{working.ID, console.ID}},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				rows, err := repo.List(ctx, tc.f, 0, 50)
				if err != nil {
					t.Fatal(err)
				}
				if got := draftIDs(rows); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("List(%+v) = %v, want %v", tc.f, got, tc.want)
				}
			})
		}
		rows, err := repo.List(ctx, DraftFilter{Door: "straza-app"}, 0, 50)
		if err != nil || len(rows) != 1 || rows[0].Proposer != draftAgent {
			t.Errorf("a listed draft's proposer = %+v (%v), want the author of revision 1 with via and client", rows, err)
		}
	})
}

// TestDraftListPagesNewestFirst pins the keyset window: limit drafts with
// an id below before, newest first, and a default page for a limit of 0.
func TestDraftListPagesNewestFirst(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		var ids []int64
		for range 5 {
			ids = append(ids, createDraft(t, s, DraftRow{}, draftAlice).ID)
		}
		page := func(before int64, limit int) []int64 {
			rows, err := s.Drafts().List(ctx, DraftFilter{}, before, limit)
			if err != nil {
				t.Fatal(err)
			}
			return draftIDs(rows)
		}
		if got := page(0, 2); !reflect.DeepEqual(got, []int64{ids[4], ids[3]}) {
			t.Errorf("first page = %v, want the two newest", got)
		}
		if got := page(ids[3], 2); !reflect.DeepEqual(got, []int64{ids[2], ids[1]}) {
			t.Errorf("second page = %v, want the next two", got)
		}
		if got := page(ids[1], 2); !reflect.DeepEqual(got, []int64{ids[0]}) {
			t.Errorf("last page = %v, want the oldest", got)
		}
		if got := page(0, 0); len(got) != 5 {
			t.Errorf("a page of limit 0 = %v, want the default page holding all 5", got)
		}
	})
}

// TestDraftItemsReadsSeveralDrafts pins the batch read: the items of every
// id asked for, by draft id in seq order, and nothing for an unknown id.
func TestDraftItemsReadsSeveralDrafts(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		a := createDraft(t, s, DraftRow{}, draftAlice, draftItem("App", "x", "put", "1"), draftItem("App", "y", "put", "2"))
		b := createDraft(t, s, DraftRow{}, draftAlice, draftItem("Role", "z", "remove", ""))
		createDraft(t, s, DraftRow{}, draftAlice, draftItem("Role", "other", "put", "3"))
		got, err := s.Drafts().Items(context.Background(), []int64{b.ID, a.ID, b.ID + 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || len(got[a.ID]) != 2 || got[a.ID][0].Name != "x" || got[a.ID][1].Seq != 2 ||
			len(got[b.ID]) != 1 || got[b.ID][0].Op != "remove" {
			t.Errorf("Items = %+v, want draft a's two items in order and draft b's one", got)
		}
		if none, err := s.Drafts().Items(context.Background(), nil); err != nil || len(none) != 0 {
			t.Errorf("Items(nil) = %v, %v; want an empty map", none, err)
		}
	})
}

// TestDraftBySlotAnswersTheOpenDraft pins BySlot: the open draft of the
// slot with its items, and ErrNotFound once it closed or for no slot.
func TestDraftBySlotAnswersTheOpenDraft(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		createDraft(t, s, DraftRow{}, draftAlice, draftItem("PolicySet", "guard", "put", "other"))
		d := createDraft(t, s, DraftRow{Slot: "policy:guard"}, draftAlice, draftItem("PolicySet", "guard", "put", "saved"))
		got, items, err := s.Drafts().BySlot(ctx, "policy:guard")
		if err != nil || got.ID != d.ID || len(items) != 1 || items[0].Doc != "saved" {
			t.Fatalf("BySlot = %+v with %+v, %v; want the slot draft with its item", got, items, err)
		}
		if _, err := s.Drafts().Close(ctx, d.ID, 0, "discarded", draftAlice, "", draftCheckAt); err != nil {
			t.Fatal(err)
		}
		for _, slot := range []string{"policy:guard", ""} {
			if _, _, err := s.Drafts().BySlot(ctx, slot); !errors.Is(err, ErrNotFound) {
				t.Errorf("BySlot(%q) = %v, want ErrNotFound", slot, err)
			}
		}
	})
}

// TestDraftListUncheckedOldestFirst pins the checker's input: open drafts
// whose current revision was not checked, oldest first, up to limit.
func TestDraftListUncheckedOldestFirst(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.Drafts()
		older := createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent, draftItem("App", "a", "put", "1"))
		createDraft(t, s, DraftRow{CheckedRevision: 1, CheckedAt: &draftCheckAt}, draftAlice, stampedItem("App", "b", "1", "fp"))
		newer := createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent, draftItem("App", "c", "put", "1"))
		closed := createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent, draftItem("App", "d", "put", "1"))
		if _, err := repo.Close(ctx, closed.ID, 0, "discarded", draftAgent, "", draftCheckAt); err != nil {
			t.Fatal(err)
		}
		if rows, err := repo.ListUnchecked(ctx, 10); err != nil || !reflect.DeepEqual(draftIDs(rows), []int64{older.ID, newer.ID}) {
			t.Errorf("ListUnchecked = %v (%v), want %v", draftIDs(rows), err, []int64{older.ID, newer.ID})
		}
		if rows, err := repo.ListUnchecked(ctx, 1); err != nil || !reflect.DeepEqual(draftIDs(rows), []int64{older.ID}) {
			t.Errorf("ListUnchecked(1) = %v (%v), want the oldest", draftIDs(rows), err)
		}
	})
}

// TestDraftListExpirableAnswersOpenDraftsPastTheirExpiry pins the expiry
// sweep's input: open drafts whose expiry is before now, never a draft
// without one, one not due, or one already closed. An expiry half a second
// after a whole-second now is not due, which sqlite's text times get right
// only with the padded bound.
func TestDraftListExpirableAnswersOpenDraftsPastTheirExpiry(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
		expiring := func(at time.Time) DraftRow {
			return createDraft(t, s, DraftRow{Door: "straza-app", ExpiresAt: &at}, draftAgent)
		}
		due := expiring(now.Add(-time.Hour))
		dueHalfASecondAgo := expiring(now.Add(-500 * time.Millisecond))
		expiring(now.Add(500 * time.Millisecond))
		expiring(now.Add(time.Hour))
		createDraft(t, s, DraftRow{}, draftAlice)
		closed := expiring(now.Add(-time.Hour))
		if _, err := s.Drafts().Close(ctx, closed.ID, 0, "discarded", draftAgent, "", now); err != nil {
			t.Fatal(err)
		}
		want := []int64{due.ID, dueHalfASecondAgo.ID}
		if rows, err := s.Drafts().ListExpirable(ctx, now, 10); err != nil || !reflect.DeepEqual(draftIDs(rows), want) {
			t.Errorf("ListExpirable = %v (%v), want %v", draftIDs(rows), err, want)
		}
	})
}

// TestDraftLatestChangeAnswersTheNewestPublish pins the stale check's
// reader: the published draft that changed the object last, whether its
// change was explicit or implied, and ErrNotFound for an object no publish
// changed.
func TestDraftLatestChangeAnswersTheNewestPublish(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		change := DraftChangeRow{Kind: "Role", Name: "readers", BeforeOp: "put", BeforeDoc: "a", AfterOp: "put", AfterDoc: "b"}
		implied := DraftChangeRow{Kind: "Role", Name: "readers", Implied: true, BeforeOp: "put", AfterOp: "remove"}
		first := createDraft(t, s, DraftRow{}, draftAlice)
		second := createDraft(t, s, DraftRow{}, draftBob)
		open := createDraft(t, s, DraftRow{}, draftAlice, draftItem("Role", "readers", "put", "c"))
		markPublished(t, s, second.ID, draftCheckAt.Add(-time.Hour), change)
		markPublished(t, s, first.ID, draftCheckAt, implied)
		got, err := s.Drafts().LatestChange(ctx, ObjectRef{Kind: "Role", Name: "readers"})
		if err != nil || got.ID != first.ID || got.DecidedAt == nil || !got.DecidedAt.Equal(draftCheckAt) {
			t.Errorf("LatestChange = %+v, %v; want draft %d, published last", got, err, first.ID)
		}
		if got.ID == open.ID {
			t.Error("LatestChange answered an open draft")
		}
		if _, err := s.Drafts().LatestChange(ctx, ObjectRef{Kind: "App", Name: "readers"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("LatestChange of an object no publish changed = %v, want ErrNotFound", err)
		}
	})
}

// TestDraftRevisionsAndChangesReadInOrder pins the two history reads:
// every revision with its author oldest first, and a publish's change rows
// in seq order.
func TestDraftRevisionsAndChangesReadInOrder(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.Drafts()
		d := createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent)
		authors := []DraftActor{draftBob, draftAlice}
		for i, a := range authors {
			if _, err := repo.Revise(ctx, d.ID, DraftRevise{From: i + 1,
				Rev: DraftRevisionRow{Author: a, Door: "console", Digest: "d" + strconv.Itoa(i+2), Mechanical: i == 1}}); err != nil {
				t.Fatal(err)
			}
		}
		revs, err := repo.Revisions(ctx, d.ID)
		if err != nil || len(revs) != 3 {
			t.Fatalf("revisions = %+v, %v; want 3", revs, err)
		}
		for i, want := range []DraftActor{draftAgent, draftBob, draftAlice} {
			if revs[i].Revision != i+1 || revs[i].Author != want || revs[i].Mechanical != (i == 2) {
				t.Errorf("revision %d = %+v, want author %s", i+1, revs[i], want.Name)
			}
		}
		changes := []DraftChangeRow{
			{Seq: 1, Kind: "App", Name: "demo", BeforeOp: "remove", AfterOp: "put", AfterDoc: "{}", AfterFP: "fp1"},
			{Seq: 2, Kind: "Role", Name: "demo-readers", Implied: true, BeforeOp: "put", BeforeDoc: "r", BeforeFP: "fp2", AfterOp: "remove"},
		}
		markPublished(t, s, d.ID, draftCheckAt, changes...)
		if got, err := repo.Changes(ctx, d.ID); err != nil || !reflect.DeepEqual(got, changes) {
			t.Errorf("Changes = %+v, %v; want %+v", got, err, changes)
		}
	})
}

// TestDraftRevisionsOfReadsSeveralDrafts pins the batched history read:
// every revision of each id asked for, by draft id and oldest first, with
// its author, door and digest, and nothing for an unknown id or no id.
func TestDraftRevisionsOfReadsSeveralDrafts(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.Drafts()
		a := createDraft(t, s, DraftRow{}, draftAlice, draftItem("App", "x", "put", "1"))
		b := createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent, draftItem("Role", "z", "remove", ""))
		createDraft(t, s, DraftRow{}, draftBob, draftItem("Role", "other", "put", "3"))
		if _, err := repo.Revise(ctx, b.ID, DraftRevise{From: 1, Items: []DraftItemRow{draftItem("Role", "z", "remove", "")},
			Rev: DraftRevisionRow{Author: draftBob, Door: "console", Digest: "d2", Mechanical: true}}); err != nil {
			t.Fatal(err)
		}
		first := func(by DraftActor, door string) DraftRevisionRow {
			return DraftRevisionRow{Revision: 1, Author: by, Door: door, Digest: "digest-1"}
		}
		for _, tc := range []struct {
			name string
			ids  []int64
			want map[int64][]DraftRevisionRow
		}{
			{"two drafts and an unknown id", []int64{b.ID, a.ID, b.ID + 100}, map[int64][]DraftRevisionRow{
				a.ID: {first(draftAlice, "console")},
				b.ID: {first(draftAgent, "straza-app"), {Revision: 2, Author: draftBob, Door: "console", Digest: "d2", Mechanical: true}},
			}},
			{"one draft", []int64{a.ID}, map[int64][]DraftRevisionRow{a.ID: {first(draftAlice, "console")}}},
			{"no id", nil, map[int64][]DraftRevisionRow{}},
		} {
			got, err := repo.RevisionsOf(ctx, tc.ids)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			for _, revs := range got {
				for i := range revs {
					if revs[i].CreatedAt.IsZero() {
						t.Errorf("%s: revision %d has no time", tc.name, revs[i].Revision)
					}
					revs[i].CreatedAt = time.Time{}
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s: RevisionsOf = %+v, want %+v", tc.name, got, tc.want)
			}
		}
	})
}

// TestDraftPublishKeepsTheCheckItRan pins that the check a plan carries
// becomes the check of the published revision, written by the publish's
// own transaction, and that a plan without one leaves the stored check.
func TestDraftPublishKeepsTheCheckItRan(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ran := &DraftCheck{Snapshot: "snapshot-the-publish-read", Counts: `{"refused":0,"risks":1,"warnings":0,"unchecked":0}`,
			AgentVerdict: `{"refused":[]}`, At: draftCheckAt}
		for i, tc := range []struct {
			name    string
			checked *DraftCheck
		}{
			{"a plan with the check the publish ran", ran},
			{"a plan without one", nil},
		} {
			plan := planOf(t, s, rolePut(RoleConfig{Role: Role{Name: "crew-" + strconv.Itoa(i), Kind: RoleKindBusiness}}))
			plan.Checked = tc.checked
			before, _ := getDraft(t, s, plan.DraftID)
			want := DraftCheck{Snapshot: before.CheckedSnapshot, Counts: before.CheckCounts, AgentVerdict: before.AgentVerdict}
			wantRevision := before.CheckedRevision
			if tc.checked != nil {
				want, wantRevision = *tc.checked, plan.Revision
			}
			mustPublish(t, s, plan)
			d, _ := getDraft(t, s, plan.DraftID)
			got := DraftCheck{Snapshot: d.CheckedSnapshot, Counts: d.CheckCounts, AgentVerdict: d.AgentVerdict}
			if d.CheckedAt != nil {
				got.At = *d.CheckedAt
			}
			if d.State != "published" || d.CheckedRevision != wantRevision || !got.At.Equal(want.At) || got.Snapshot != want.Snapshot ||
				got.Counts != want.Counts || got.AgentVerdict != want.AgentVerdict {
				t.Errorf("%s: draft %d reads %s, checked at revision %d with %+v; want published, revision %d with %+v",
					tc.name, d.ID, d.State, d.CheckedRevision, got, wantRevision, want)
			}
		}
	})
}

// TestDraftGenerationStartsAtZero pins the one row the migration inserts.
func TestDraftGenerationStartsAtZero(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		if g, err := s.Drafts().Generation(context.Background()); err != nil || g != 0 {
			t.Errorf("Generation = %d, %v; want 0", g, err)
		}
	})
}

// TestDraftWritersRaceOnOneRevision is the two-replica proof of the
// compare-and-set writes: eight writers revise, stamp or close one draft at
// one revision at once, and exactly one of them lands. On Postgres the
// writers use two handles, which is two replicas on one database.
func TestDraftWritersRaceOnOneRevision(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		handles := []Store{s, s}
		if s.(*sqlStore).d == dialectPostgres {
			peer, err := Open(config.Config{Store: config.Store{
				Driver: config.DriverPostgres, DSN: os.Getenv("STRAZA_TEST_POSTGRES_DSN"),
			}})
			if err != nil {
				t.Fatalf("open the second replica's handle: %v", err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			handles[1] = peer
		}
		items := []DraftItemRow{draftItem("App", "a", "put", "doc")}
		bases := []DraftItemRow{{Kind: "App", Name: "a", Base: "fp", BaseOp: "put"}}
		writes := []struct {
			name  string
			write func(repo DraftRepo, id int64, i int) (bool, error)
		}{
			{"revise", func(repo DraftRepo, id int64, i int) (bool, error) {
				_, err := repo.Revise(ctx, id, DraftRevise{From: 1, Items: items,
					Rev: DraftRevisionRow{Author: draftAlice, Door: "console", Digest: strconv.Itoa(i)}})
				if errors.Is(err, ErrConflict) {
					return false, nil
				}
				return err == nil, err
			}},
			{"stamp", func(repo DraftRepo, id int64, i int) (bool, error) {
				return repo.Stamp(ctx, id, 1, bases, DraftCheck{Snapshot: strconv.Itoa(i), At: draftCheckAt})
			}},
			{"close", func(repo DraftRepo, id int64, i int) (bool, error) {
				return repo.Close(ctx, id, 1, "discarded", draftAlice, strconv.Itoa(i), draftCheckAt)
			}},
		}
		for _, w := range writes {
			t.Run(w.name, func(t *testing.T) {
				d := createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent, items...)
				const writers = 8
				var wg sync.WaitGroup
				var mu sync.Mutex
				won := 0
				start := make(chan struct{})
				for i := range writers {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						ok, err := w.write(handles[i%2].Drafts(), d.ID, i)
						if err != nil {
							t.Errorf("writer %d: %v", i, err)
						}
						if ok {
							mu.Lock()
							won++
							mu.Unlock()
						}
					}()
				}
				close(start)
				wg.Wait()
				if won != 1 {
					t.Errorf("%d writers landed a %s of revision 1, want exactly 1", won, w.name)
				}
			})
		}
	})
}

// TestDraftCreateRefusesAProposerOtherThanTheAuthor pins that a proposer
// set on the row is the author of revision 1. Any other is refused with
// nothing stored, because a draft stored without its proposer escapes the
// open-draft limit that CountOpen counts.
func TestDraftCreateRefusesAProposerOtherThanTheAuthor(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		items := []DraftItemRow{draftItem("App", "demo", "put", "{}")}
		tests := []struct {
			name             string
			proposer, author DraftActor
			stored           bool
		}{
			{"a proposer and no author", draftAgent, DraftActor{}, false},
			{"a proposer and another author", draftAgent, draftBob, false},
			{"the author as the proposer", draftAgent, draftAgent, true},
			{"the author alone", DraftActor{}, draftAgent, true},
		}
		stored := 0
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				d, err := s.Drafts().Create(ctx, DraftRow{Door: "straza-app", Proposer: tc.proposer}, items,
					DraftRevisionRow{Author: tc.author, Digest: "d"})
				if (err == nil) != tc.stored || errors.Is(err, ErrConflict) {
					t.Fatalf("Create = %+v, %v; want stored %v", d, err, tc.stored)
				}
				if tc.stored {
					stored++
					if d.Proposer != draftAgent {
						t.Errorf("proposer = %+v, want the author of revision 1", d.Proposer)
					}
				}
				if n, err := s.Drafts().CountOpen(ctx, draftAgent.ID); err != nil || n != stored {
					t.Errorf("CountOpen = %d, %v; want %d", n, err, stored)
				}
			})
		}
		if rows, err := s.Drafts().List(ctx, DraftFilter{}, 0, 10); err != nil || len(rows) != stored {
			t.Errorf("drafts = %d (%v), want only the %d stored", len(rows), err, stored)
		}
	})
}

// TestDraftListMatchesAWholeSlotExactly pins that a SlotPrefix naming a
// whole slot lists the drafts of that slot only, decided ones included, so
// the saved edits of set guard never list those of set guard2. A prefix
// that ends in a colon lists every slot of its kind.
func TestDraftListMatchesAWholeSlotExactly(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		older := createDraft(t, s, DraftRow{Slot: "policy:guard"}, draftAlice)
		if _, err := s.Drafts().Close(ctx, older.ID, 0, "discarded", draftAlice, "", draftCheckAt); err != nil {
			t.Fatal(err)
		}
		guard := createDraft(t, s, DraftRow{Slot: "policy:guard"}, draftAlice)
		guard2 := createDraft(t, s, DraftRow{Slot: "policy:guard2"}, draftAlice)
		working := createDraft(t, s, DraftRow{Slot: "working:u-alice"}, draftAlice)
		tests := []struct {
			prefix string
			want   []int64
		}{
			{"policy:guard", []int64{guard.ID, older.ID}},
			{"policy:guard2", []int64{guard2.ID}},
			{"policy:", []int64{guard2.ID, guard.ID, older.ID}},
			{"working:u-alice", []int64{working.ID}},
			{"working:u-ali", []int64{}},
		}
		for _, tc := range tests {
			t.Run(tc.prefix, func(t *testing.T) {
				rows, err := s.Drafts().List(ctx, DraftFilter{SlotPrefix: tc.prefix}, 0, 50)
				if err != nil {
					t.Fatal(err)
				}
				if got := draftIDs(rows); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("List(SlotPrefix %q) = %v, want %v", tc.prefix, got, tc.want)
				}
			})
		}
	})
}

// TestDraftSetOfferedWritesOneItem pins SetOffered: the tool names a
// Contact read land on the one item of the draft at its revision, and
// nothing else moves; a draft past that revision or no longer open answers
// ErrConflict, an unknown draft or item ErrNotFound, and a later revision
// keeps what was stored.
func TestDraftSetOfferedWritesOneItem(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		repo := s.Drafts()
		const offered = `{"digest":"d","tools":["get_me"],"at":"2026-09-25T10:00:00Z"}`
		d := createDraft(t, s, DraftRow{}, draftAlice, stampedItem("App", "github", "doc-g", "fp-g"), stampedItem("Role", "github", "doc-r", "fp-r"))
		before, _ := getDraft(t, s, d.ID)
		if err := repo.SetOffered(ctx, d.ID, 1, ObjectRef{"App", "github"}, offered); err != nil {
			t.Fatalf("SetOffered: %v", err)
		}
		after, items := getDraft(t, s, d.ID)
		if items[0].Offered != offered || items[1].Offered != "" {
			t.Errorf("offered = %q and %q, want the App's set and the Role's untouched", items[0].Offered, items[1].Offered)
		}
		if after.Revision != before.Revision || !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Errorf("the draft row moved: revision %d at %s, was %d at %s", after.Revision, after.UpdatedAt, before.Revision, before.UpdatedAt)
		}
		refusals := []struct {
			name string
			id   int64
			rev  int
			ref  ObjectRef
			want error
		}{
			{"a revision the draft is past", d.ID, 0, ObjectRef{"App", "github"}, ErrConflict},
			{"an unknown draft", d.ID + 100, 1, ObjectRef{"App", "github"}, ErrNotFound},
			{"an item the draft does not hold", d.ID, 1, ObjectRef{"App", "jira"}, ErrNotFound},
		}
		for _, tc := range refusals {
			t.Run(tc.name, func(t *testing.T) {
				if err := repo.SetOffered(ctx, tc.id, tc.rev, tc.ref, `{"tools":[]}`); !errors.Is(err, tc.want) {
					t.Errorf("SetOffered = %v, want %v", err, tc.want)
				}
			})
		}
		if _, err := repo.Revise(ctx, d.ID, DraftRevise{From: 1, Items: items, Rev: DraftRevisionRow{Author: draftAlice, Door: "console", Digest: "d2"}}); err != nil {
			t.Fatal(err)
		}
		if _, items := getDraft(t, s, d.ID); items[0].Offered != offered {
			t.Errorf("offered after a revision = %q, want it kept", items[0].Offered)
		}
		if err := repo.SetOffered(ctx, d.ID, 1, ObjectRef{"App", "github"}, `{"tools":[]}`); !errors.Is(err, ErrConflict) {
			t.Errorf("SetOffered at the old revision = %v, want ErrConflict", err)
		}
		if _, err := repo.Close(ctx, d.ID, 0, "discarded", draftAlice, "", draftCheckAt); err != nil {
			t.Fatal(err)
		}
		if err := repo.SetOffered(ctx, d.ID, 2, ObjectRef{"App", "github"}, `{"tools":[]}`); !errors.Is(err, ErrConflict) {
			t.Errorf("SetOffered on a discarded draft = %v, want ErrConflict", err)
		}
	})
}

// rowsUnknownConn is a sqlite connection whose Exec results cannot count
// the rows they changed. Neither pgx nor modernc fails that count on an
// Exec today, so the fault is made here to test how a caller handles it.
type rowsUnknownConn struct{ driver.Conn }

type rowsUnknownResult struct{ driver.Result }

func (rowsUnknownResult) RowsAffected() (int64, error) {
	return 0, errors.New("the driver cannot count the rows it changed")
}

func (c rowsUnknownConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	res, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return rowsUnknownResult{res}, nil
}

func (c rowsUnknownConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c rowsUnknownConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

// rowsUnknownConnector opens rowsUnknownConn connections to one sqlite file.
type rowsUnknownConnector struct {
	inner driver.Driver
	dsn   string
}

func (c rowsUnknownConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.inner.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return rowsUnknownConn{conn}, nil
}

func (c rowsUnknownConnector) Driver() driver.Driver { return c.inner }

// TestDraftStampRollsBackWhenRowsCannotBeCounted pins that Stamp fails and
// writes nothing when the driver cannot say whether its update landed.
// Committing then would record the check without the bases, a checked
// revision that nothing can stamp or publish.
func TestDraftStampRollsBackWhenRowsCannotBeCounted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	seedSQLite(t, path)
	plain, err := openSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	inner := plain.(*sqlStore).db.Driver()
	_ = plain.Close()
	db := sql.OpenDB(rowsUnknownConnector{inner: inner,
		dsn: "file:" + url.PathEscape(filepath.ToSlash(path)) + "?_pragma=foreign_keys(1)"})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	s := &sqlStore{db: db, d: dialectSQLite}

	d := createDraft(t, s, DraftRow{Door: "straza-app"}, draftAgent, draftItem("App", "a", "put", "doc"))
	ok, err := s.Drafts().Stamp(context.Background(), d.ID, 1,
		[]DraftItemRow{{Kind: "App", Name: "a", Base: "fp", BaseOp: "put"}}, DraftCheck{Snapshot: "snap", At: draftCheckAt})
	if err == nil || ok {
		t.Fatalf("stamp = %v, %v; want an error when the rows cannot be counted", ok, err)
	}
	if got, items := getDraft(t, s, d.ID); got.CheckedRevision != 0 || items[0].BaseOp != "" {
		t.Errorf("draft after the failed stamp = %+v with %+v, want nothing written", got, items)
	}
}
