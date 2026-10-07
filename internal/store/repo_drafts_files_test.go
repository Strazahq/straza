package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// draftFile is strazad's own account on the apps directory door.
var draftFile = DraftActor{Name: "strazad", Via: "file", Client: "strazad"}

// TestDraftBySourceAnswersEveryState pins BySource: every draft proposed
// from one file, whatever its state, newest first with who decided it, and
// nothing for another file or for an empty source.
func TestDraftBySourceAnswersEveryState(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		const path = "/etc/straza/apps/github.yaml"
		first := createDraft(t, s, DraftRow{Door: "apps-directory", Source: path, SourceHash: "h1"}, draftFile,
			draftItem("App", "github", "put", "kind: App"))
		discarded := createDraft(t, s, DraftRow{Door: "apps-directory", Source: path, SourceHash: "h2"}, draftFile,
			draftItem("App", "github", "put", "kind: App"))
		if ok, err := s.Drafts().Close(ctx, discarded.ID, 0, "discarded", draftFile, "a newer revision of the file replaced it", draftCheckAt); err != nil || !ok {
			t.Fatalf("discard: %v, %v", ok, err)
		}
		published := createDraft(t, s, DraftRow{Door: "apps-directory", Source: path, SourceHash: "h3"}, draftFile,
			draftItem("App", "github", "put", "kind: App"))
		markPublished(t, s, published.ID, draftCheckAt.Add(time.Minute))
		createDraft(t, s, DraftRow{Door: "apps-directory", Source: "/etc/straza/apps/jira.yaml", SourceHash: "h1"}, draftFile,
			draftItem("App", "jira", "put", "kind: App"))
		createDraft(t, s, DraftRow{}, draftAlice, draftItem("App", "github", "put", "kind: App"))

		rows, err := s.Drafts().BySource(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := draftIDs(rows), []int64{published.ID, discarded.ID, first.ID}; !reflect.DeepEqual(got, want) {
			t.Fatalf("BySource answers %v, want %v, newest first", got, want)
		}
		if rows[0].State != "published" || rows[1].State != "discarded" || rows[2].State != "open" {
			t.Errorf("states = %s, %s, %s, want published, discarded, open", rows[0].State, rows[1].State, rows[2].State)
		}
		if rows[1].DecidedBy != draftFile || rows[1].SourceHash != "h2" {
			t.Errorf("the discarded draft reads %+v by %+v, want hash h2 decided by the file door", rows[1], rows[1].DecidedBy)
		}
		none, err := s.Drafts().BySource(ctx, "")
		if err != nil || len(none) != 0 {
			t.Errorf("BySource of an empty source = %v, %v, want none", draftIDs(none), err)
		}
	})
}

// TestDraftFileDecisionsAnswersTheDecisionsThatMoveALink pins what the file
// link reads: every published or discarded draft of the apps directory
// that holds an App item, and every published draft of any door that
// removes a server, oldest decision first, each with its door, who decided
// it and its App items without their documents. An open file draft, a
// file draft with no items, a console put and a discarded console removal
// are left out.
func TestDraftFileDecisionsAnswersTheDecisionsThatMoveALink(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		const path = "/etc/straza/apps/github.yaml"
		at := func(minutes int) time.Time { return draftCheckAt.Add(time.Duration(minutes) * time.Minute) }

		// Created first and decided last, so the order is the decision's.
		removed := createDraft(t, s, DraftRow{Door: "api"}, draftAlice, draftItem("App", "jira", "remove", ""))
		markPublished(t, s, removed.ID, at(30))

		dropped := createDraft(t, s, DraftRow{Door: "apps-directory", Source: path, SourceHash: "h1"}, draftFile,
			draftItem("App", "github", "put", "kind: App"))
		if ok, err := s.Drafts().Close(ctx, dropped.ID, 0, "discarded", draftFile, "the file is gone", at(10)); err != nil || !ok {
			t.Fatalf("discard: %v, %v", ok, err)
		}
		linked := createDraft(t, s, DraftRow{Door: "apps-directory", Source: path, SourceHash: "h2"}, draftFile,
			draftItem("App", "github", "put", "kind: App"), draftItem("Role", "github-readers", "put", "kind: Role"))
		markPublished(t, s, linked.ID, at(20))

		console := createDraft(t, s, DraftRow{}, draftAlice, draftItem("App", "github", "put", "kind: App"))
		markPublished(t, s, console.ID, at(5))
		createDraft(t, s, DraftRow{Door: "apps-directory", Source: path, SourceHash: "h3"}, draftFile,
			draftItem("App", "github", "put", "kind: App"))
		refused := createDraft(t, s, DraftRow{Door: "apps-directory", Source: path, SourceHash: "h4", Refusal: "manifest: parse"}, draftFile)
		if ok, err := s.Drafts().Close(ctx, refused.ID, 0, "discarded", draftFile, "the file is gone", at(11)); err != nil || !ok {
			t.Fatalf("discard: %v, %v", ok, err)
		}
		kept := createDraft(t, s, DraftRow{}, draftAlice, draftItem("App", "github", "remove", ""))
		if ok, err := s.Drafts().Close(ctx, kept.ID, 0, "discarded", draftAlice, "", at(12)); err != nil || !ok {
			t.Fatalf("discard: %v, %v", ok, err)
		}

		got, err := s.Drafts().FileDecisions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("FileDecisions answers %d decisions, want 3: %+v", len(got), got)
		}
		want := []FileDecision{
			{DraftID: dropped.ID, Door: "apps-directory", State: "discarded", Source: path, DecidedAt: at(10), DecidedBy: draftFile,
				Items: []DraftItemRow{{Seq: 1, Kind: "App", Name: "github", Op: "put"}}},
			{DraftID: linked.ID, Door: "apps-directory", State: "published", Source: path, DecidedAt: at(20),
				DecidedBy: DraftActor{ID: draftAlice.ID, Name: draftAlice.Name}, Items: []DraftItemRow{{Seq: 1, Kind: "App", Name: "github", Op: "put"}}},
			{DraftID: removed.ID, Door: "api", State: "published", DecidedAt: at(30),
				DecidedBy: DraftActor{ID: draftAlice.ID, Name: draftAlice.Name}, Items: []DraftItemRow{{Seq: 1, Kind: "App", Name: "jira", Op: "remove"}}},
		}
		for i := range want {
			if !got[i].DecidedAt.Equal(want[i].DecidedAt) {
				t.Errorf("decision %d was decided at %v, want %v", i, got[i].DecidedAt, want[i].DecidedAt)
			}
			got[i].DecidedAt = want[i].DecidedAt
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("decision %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	})
}
