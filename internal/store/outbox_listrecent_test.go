package store

import (
	"context"
	"testing"
)

// TestOutboxListRecentSurvivesPublish pins the observation seam: a row must
// stay visible through ListRecent after the relay marks it published,
// because asserting an emit via ListUnpublished races the relay.
// Newest-first ordering is part of the contract.
func TestOutboxListRecentSurvivesPublish(t *testing.T) {
	forEachStore(t, testOutboxListRecentSurvivesPublish)
}

func testOutboxListRecentSurvivesPublish(t *testing.T, s Store) {
	ctx := context.Background()

	first, err := s.Outbox().Insert(ctx, OutboxEvent{Subject: "straza.audit.admin", CE: `{"data":{"action":"first"}}`})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Outbox().Insert(ctx, OutboxEvent{Subject: "straza.audit.admin", CE: `{"data":{"action":"second"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Outbox().MarkPublished(ctx, []string{first.ID}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Outbox().ListRecent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]OutboxEvent{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	got, ok := byID[first.ID]
	if !ok {
		t.Fatalf("published row vanished from ListRecent (the seam this method exists for)")
	}
	if !got.Published {
		t.Errorf("published row reads Published=false")
	}
	if _, ok := byID[second.ID]; !ok {
		t.Fatalf("unpublished row missing from ListRecent")
	}
	// Newest first: second was inserted after first.
	var iFirst, iSecond int
	for i, r := range rows {
		if r.ID == first.ID {
			iFirst = i
		}
		if r.ID == second.ID {
			iSecond = i
		}
	}
	if iSecond > iFirst {
		t.Errorf("ordering not newest-first: second at %d, first at %d", iSecond, iFirst)
	}
}
