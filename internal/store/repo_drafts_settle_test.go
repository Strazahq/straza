package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

var draftUpgrade = DraftActor{Name: "strazad", Via: "upgrade", Client: "strazad"}

const (
	guardPublished = "guard as published"
	guardSaved     = "guard as saved"
	// The saved edit changed the priority its text declares, so the row
	// holds the saved priority until the conversion settles it.
	publishedPriority = 3
	savedPriority     = 5
)

// savedEdit stores the live set guard whose row holds a saved edit over the
// text it was published with, as a store before the conversion holds it,
// and answers the settle the conversion makes of what PolicyState read: the
// row back to the published text, and a new slot draft holding the edit.
func savedEdit(t *testing.T, s Store) PolicySettle {
	t.Helper()
	ctx := context.Background()
	activate(t, s, "s1")
	row, err := s.Policies().Create(ctx, PolicySet{Name: "guard", Priority: savedPriority, YAMLSource: guardSaved, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Drafts().PolicyState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return PolicySettle{Generation: st.Mark.Generation, Snapshot: st.Snapshot.ID, Name: "guard", Was: &row, Status: "active",
		Text: guardPublished, Priority: publishedPriority,
		Draft: &DraftRow{Door: "api", Slot: "policy:guard", Note: "Saved before the upgrade to drafts: this edit was stored and never published."},
		Item: DraftItemRow{Kind: kindPolicySet, Name: "guard", Op: opPut, Doc: guardSaved,
			Base:   FingerprintPolicySet(PolicySet{Name: "guard", Status: "active", YAMLSource: guardPublished}),
			BaseOp: opPut, BaseDoc: guardPublished},
		Rev: DraftRevisionRow{Author: draftUpgrade, Digest: "digest-saved"}}
}

func mustSettle(t *testing.T, s Store, settle PolicySettle) {
	t.Helper()
	if ok, err := s.Drafts().SettlePolicy(context.Background(), settle); !ok || err != nil {
		t.Fatalf("SettlePolicy answered %v, %v; want it to settle the set", ok, err)
	}
}

// TestSettlePolicyMovesASavedEditIntoANewSlotDraft pins the saved-edit rule with no open
// slot draft: the row goes back to the published text with its hash, and
// a new slot draft of the upgrade holds the saved edit, stamped on the
// published text. A second run finds the row settled and writes nothing.
func TestSettlePolicyMovesASavedEditIntoANewSlotDraft(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		settle := savedEdit(t, s)
		mustSettle(t, s, settle)
		row := wantSet(t, s, "guard", "active", guardPublished, sha256Hex(guardPublished))
		if row.Priority != publishedPriority {
			t.Errorf("the settled row has priority %d; want %d, the published text's", row.Priority, publishedPriority)
		}
		d, items, err := s.Drafts().BySlot(ctx, "policy:guard")
		if err != nil || d.Door != "api" || d.Proposer.Name != "strazad" || d.Proposer.Via != "upgrade" || d.Note != settle.Draft.Note {
			t.Fatalf("the slot draft is %+v, %v; want one of door api proposed by the upgrade with the note", d, err)
		}
		if want := []DraftItemRow{{Seq: 1, Kind: kindPolicySet, Name: "guard", Op: opPut, Doc: guardSaved, Base: settle.Item.Base,
			BaseOp: opPut, BaseDoc: guardPublished}}; !equalItems(items, want) {
			t.Errorf("the slot draft holds %+v; want %+v", items, want)
		}

		again := settle
		again.Was = &row
		before := dbDump(t, s)
		if ok, err := s.Drafts().SettlePolicy(ctx, again); ok || err != nil {
			t.Errorf("a second run answered %v, %v; want false with nothing to settle", ok, err)
		}
		sameDump(t, before, dbDump(t, s))
	})
}

func equalItems(got, want []DraftItemRow) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestSettlePolicyRevisesTheOpenSlotDraft pins the saved-edit rule with an open slot
// draft: a newer saved text, as an old replica writes it, becomes the next
// revision of that draft, and the item keeps the base it was stamped with.
func TestSettlePolicyRevisesTheOpenSlotDraft(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		settle := savedEdit(t, s)
		slot := createDraft(t, s, DraftRow{Door: "api", Slot: "policy:guard"}, draftUpgrade, settle.Item)
		newer := *settle.Was
		newer.YAMLSource = "guard saved again"
		newer, err := s.Policies().Update(ctx, newer)
		if err != nil {
			t.Fatal(err)
		}
		stamped := settle.Item.Base
		settle.Was, settle.Draft, settle.Slot, settle.SlotRevision = &newer, nil, slot.ID, 1
		settle.Item = DraftItemRow{Kind: kindPolicySet, Name: "guard", Op: opPut, Doc: "guard saved again",
			Base: "a base the revision must not take", BaseOp: opPut, BaseDoc: "not the published text"}
		mustSettle(t, s, settle)
		wantSet(t, s, "guard", "active", guardPublished, sha256Hex(guardPublished))
		d, items := getDraft(t, s, slot.ID)
		if d.Revision != 2 || len(items) != 1 || items[0].Doc != "guard saved again" || items[0].BaseDoc != guardPublished ||
			items[0].Base != stamped {
			t.Errorf("the slot draft is at revision %d holding %+v; want revision 2 with the newer text on the base it had", d.Revision, items)
		}
		revs, err := s.Drafts().Revisions(ctx, slot.ID)
		if err != nil || len(revs) != 2 || revs[1].Author.Via != "upgrade" || revs[1].Digest != "digest-saved" {
			t.Errorf("the revisions are %+v, %v; want a second one by the upgrade", revs, err)
		}
	})
}

// TestSettlePolicyPutsBackThePublishedPriority pins that a saved edit's
// priority never stays in the row: a row that holds the published text and
// status with the priority of an edit saved and discarded since takes the
// priority the published text declares, and no draft opens.
func TestSettlePolicyPutsBackThePublishedPriority(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		settle := savedEdit(t, s)
		row := *settle.Was
		row.YAMLSource = guardPublished
		row, err := s.Policies().Update(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		settle.Was, settle.Draft = &row, nil
		mustSettle(t, s, settle)
		if row := wantSet(t, s, "guard", "active", guardPublished, sha256Hex(guardPublished)); row.Priority != publishedPriority {
			t.Errorf("the settled row has priority %d; want %d, the published text's", row.Priority, publishedPriority)
		}
		if _, _, err := s.Drafts().BySlot(ctx, "policy:guard"); !errors.Is(err, ErrNotFound) {
			t.Errorf("the slot draft reads %v; want none opened", err)
		}
	})
}

// TestSettlePolicyInsertsAMissingRow pins the missing-row rule: a set the snapshot
// carries with no row gets a row, on and with the published text, and an
// insert that meets a row of that name writes nothing.
func TestSettlePolicyInsertsAMissingRow(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		activate(t, s, "s1")
		settle := PolicySettle{Snapshot: "s1", Name: "guard", Status: "active", Text: guardPublished, Priority: 7}
		mustSettle(t, s, settle)
		if row := wantSet(t, s, "guard", "active", guardPublished, sha256Hex(guardPublished)); row.Priority != 7 {
			t.Errorf("the inserted row has priority %d; want 7", row.Priority)
		}
		before := dbDump(t, s)
		if ok, err := s.Drafts().SettlePolicy(context.Background(), settle); ok || err != nil {
			t.Errorf("an insert meeting a row of the name answered %v, %v; want false", ok, err)
		}
		sameDump(t, before, dbDump(t, s))
	})
}

// TestSettlePolicyWritesNothingWhenLiveStateMoved moves what the
// conversion read before its settle runs, and asserts that the settle
// answers false and that not one row changed, so a text a publish placed
// in the snapshot is never filed as a saved edit and reverted.
func TestSettlePolicyWritesNothingWhenLiveStateMoved(t *testing.T) {
	revising := func(t *testing.T, s Store) PolicySettle {
		settle := savedEdit(t, s)
		slot := createDraft(t, s, DraftRow{Door: "api", Slot: "policy:guard"}, draftUpgrade, settle.Item)
		settle.Draft, settle.Slot, settle.SlotRevision = nil, slot.ID, 1
		return settle
	}
	cases := []struct {
		name    string
		prepare func(t *testing.T, s Store) PolicySettle
		move    func(t *testing.T, s Store, settle PolicySettle)
	}{
		{
			name:    "a publish of another text of the set landed after the read",
			prepare: savedEdit,
			move: func(t *testing.T, s Store, _ PolicySettle) {
				mustPublish(t, s, planOf(t, s, setPut("guard", "guard as published again", 5)))
			},
		},
		{
			// The row still holds what the conversion read, so only the
			// generation tells that the saved text is live now, and a settle
			// would revert it and file it as a saved edit.
			name:    "a publish made the saved text live after the read",
			prepare: savedEdit,
			move: func(t *testing.T, s Store, _ PolicySettle) {
				plan := planOf(t, s, setPut("guard", guardSaved, 5))
				plan.Snapshot = &Snapshot{ID: "s2", SignerKeyID: "k1", Blob: []byte("the saved text, published")}
				mustPublish(t, s, plan)
			},
		},
		{
			// A replica of the release before drafts activates a snapshot
			// without moving the generation or the row, and the text it
			// published would be filed as a saved edit and reverted.
			name:    "an old replica activated another snapshot after the read",
			prepare: savedEdit,
			move: func(t *testing.T, s Store, _ PolicySettle) {
				activate(t, s, "s2")
			},
		},
		{
			name:    "an old replica saved again after the read",
			prepare: savedEdit,
			move: func(t *testing.T, s Store, settle PolicySettle) {
				row := *settle.Was
				row.YAMLSource = "guard saved again"
				if _, err := s.Policies().Update(context.Background(), row); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "another saved edit took the slot",
			prepare: savedEdit,
			move: func(t *testing.T, s Store, settle PolicySettle) {
				createDraft(t, s, DraftRow{Door: "api", Slot: "policy:guard"}, draftAlice, settle.Item)
			},
		},
		{
			name:    "the slot draft moved on",
			prepare: revising,
			move: func(t *testing.T, s Store, settle PolicySettle) {
				if _, err := s.Drafts().Revise(context.Background(), settle.Slot, DraftRevise{From: 1, Items: []DraftItemRow{settle.Item},
					Rev: DraftRevisionRow{Author: draftAlice, Door: "console", Digest: "d2"}}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "the row holds the published text already",
			prepare: func(t *testing.T, s Store) PolicySettle {
				settle := savedEdit(t, s)
				row := *settle.Was
				row.YAMLSource, row.Priority = guardPublished, publishedPriority
				row, err := s.Policies().Update(context.Background(), row)
				if err != nil {
					t.Fatal(err)
				}
				settle.Was = &row
				return settle
			},
			move: func(*testing.T, Store, PolicySettle) {},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				settle := tc.prepare(t, s)
				tc.move(t, s, settle)
				before := dbDump(t, s)
				if ok, err := s.Drafts().SettlePolicy(context.Background(), settle); ok || err != nil {
					t.Errorf("SettlePolicy answered %v, %v; want false", ok, err)
				}
				sameDump(t, before, dbDump(t, s))
			})
		})
	}
}

// TestSettlePolicyComparesTheActiveSnapshot pins the snapshot half of the
// settle's check with no snapshot active: a settle that read none lands,
// and one that read a snapshot writes nothing.
func TestSettlePolicyComparesTheActiveSnapshot(t *testing.T) {
	cases := []struct {
		name     string
		snapshot string
		lands    bool
	}{
		{"the settle read no snapshot", "", true},
		{"the settle read a snapshot that is no longer active", "s1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				settle := PolicySettle{Snapshot: tc.snapshot, Name: "guard", Status: "draft", Text: guardPublished, Priority: 7}
				if ok, err := s.Drafts().SettlePolicy(context.Background(), settle); ok != tc.lands || err != nil {
					t.Errorf("SettlePolicy answered %v, %v; want %v", ok, err, tc.lands)
				}
			})
		})
	}
}

// TestSettlePolicyWaitsForThePublishLockOnPostgres pins that the settle
// checks the generation under the publish lock: a publish that holds the
// lock and moves the generation while the settle waits is seen once it
// commits, and the settle writes nothing.
func TestSettlePolicyWaitsForThePublishLockOnPostgres(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		if sq.d != dialectPostgres {
			t.Skip("sqlite's one connection runs no settle beside a publish")
		}
		ctx := context.Background()
		settle := savedEdit(t, s)
		holder, err := sq.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		for _, q := range []string{`SELECT pg_advisory_xact_lock(74218502)`,
			`UPDATE config_generation SET generation = generation + 1 WHERE id = 1`} {
			if _, err := holder.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		type answer struct {
			ok  bool
			err error
		}
		settled := make(chan answer, 1)
		go func() {
			ok, err := s.Drafts().SettlePolicy(ctx, settle)
			settled <- answer{ok, err}
		}()
		waitForLockWait(t, s, `l.locktype = 'advisory'`)
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		if got := <-settled; got.ok || got.err != nil {
			t.Errorf("the settle that waited answered %v, %v; want false", got.ok, got.err)
		}
		wantSet(t, s, "guard", "active", guardSaved, "")
		if _, _, err := s.Drafts().BySlot(ctx, "policy:guard"); !errors.Is(err, ErrNotFound) {
			t.Errorf("the slot draft reads %v; want none opened", err)
		}
	})
}

// markOf reads the policy mark and fails the test on an error.
func markOf(t *testing.T, s Store) PolicyMark {
	t.Helper()
	m, err := s.Drafts().PolicyMark(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// newestRowTime answers the newest updated_at of policy_sets, the mark a
// clock would give.
func newestRowTime(t *testing.T, s Store) time.Time {
	t.Helper()
	var newest scanTimePtr
	if err := s.(*sqlStore).queryRow(context.Background(), `SELECT MAX(updated_at) FROM policy_sets`).Scan(&newest); err != nil {
		t.Fatal(err)
	}
	return *newest.t
}

// TestPolicyMarkMovesWithEveryRowChange pins the cheap test of the
// conversion without clocks: any change to a row moves the mark. A save
// stamped older than the newest row, as a replica whose clock runs behind
// writes it, moves the mark though the newest time stays where it was.
func TestPolicyMarkMovesWithEveryRowChange(t *testing.T) {
	noon, eleven := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC), time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		change func(t *testing.T, s Store)
		moves  bool
	}{
		{"nothing changed", func(*testing.T, Store) {}, false},
		{"a save stamped older than the newest row", func(t *testing.T, s Store) {
			before := newestRowTime(t, s)
			execSQL(t, s, `UPDATE policy_sets SET yaml_source = 'beta v2', updated_at = $1 WHERE name = 'beta'`,
				s.(*sqlStore).tArg(eleven.Add(30*time.Minute)))
			if after := newestRowTime(t, s); !after.Equal(before) {
				t.Fatalf("the newest time moved from %v to %v, so the case tests nothing", before, after)
			}
		}, true},
		{"a status flip that kept its time", func(t *testing.T, s Store) {
			execSQL(t, s, `UPDATE policy_sets SET status = 'active' WHERE name = 'beta'`)
		}, true},
		{"a text of another length that kept its time", func(t *testing.T, s Store) {
			execSQL(t, s, `UPDATE policy_sets SET yaml_source = 'beta v1, longer' WHERE name = 'beta'`)
		}, true},
		{"a row removed", func(t *testing.T, s Store) {
			execSQL(t, s, `DELETE FROM policy_sets WHERE name = 'beta'`)
		}, true},
		{"a row replaced by another of the same status, time and length", func(t *testing.T, s Store) {
			execSQL(t, s, `DELETE FROM policy_sets WHERE name = 'beta'`)
			execSQL(t, s, `INSERT INTO policy_sets (id, name, priority, yaml_source, compiled_hash, status, created_at, updated_at)
				VALUES ($1, 'betb', 0, 'betb v1', '', 'draft', $2, $3)`, newID(), s.(*sqlStore).tArg(eleven), s.(*sqlStore).tArg(eleven))
		}, true},
		{"a row added with an old time", func(t *testing.T, s Store) {
			execSQL(t, s, `INSERT INTO policy_sets (id, name, priority, yaml_source, compiled_hash, status, created_at, updated_at)
				VALUES ($1, 'gamma', 0, 'gamma v1', '', 'draft', $2, $3)`, newID(), s.(*sqlStore).tArg(eleven), s.(*sqlStore).tArg(eleven))
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				seedPolicy(t, s, "alpha", "active", "alpha v1")
				seedPolicy(t, s, "beta", "draft", "beta v1")
				for name, at := range map[string]time.Time{"alpha": noon, "beta": eleven} {
					execSQL(t, s, `UPDATE policy_sets SET updated_at = $1 WHERE name = $2`, s.(*sqlStore).tArg(at), name)
				}
				before := markOf(t, s)
				tc.change(t, s)
				after := markOf(t, s)
				if moved := after != before; moved != tc.moves || after.Generation != before.Generation {
					t.Errorf("the mark went from %+v to %+v; want it moved %v at the same generation", before, after, tc.moves)
				}
			})
		})
	}
}

// TestPolicyMarkOfAStoreWithoutSets pins that a store with no set has a
// mark that holds still and is never the zero mark, so a caller that
// starts from the zero mark reads the store at least once, and that a
// publish moves the generation of the mark.
func TestPolicyMarkOfAStoreWithoutSets(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		first := markOf(t, s)
		if first == (PolicyMark{}) || first.Digest == "" || markOf(t, s) != first {
			t.Errorf("the mark of a store without sets is %+v, then %+v; want one that holds still and is not zero", first, markOf(t, s))
		}
		mustPublish(t, s, planOf(t, s, setPut("other", "other v1", 0)))
		if m := markOf(t, s); m.Generation != 1 || m.Digest == first.Digest {
			t.Errorf("the mark after a publish of a new set is %+v; want generation 1 and another digest", m)
		}
	})
}

// TestPolicyStateReadsTheMarkTheSnapshotAndEveryRow pins what the
// conversion reads: the mark, the active snapshot with its blob, zero when
// none is active, and every row by name.
func TestPolicyStateReadsTheMarkTheSnapshotAndEveryRow(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		st, err := s.Drafts().PolicyState(ctx)
		if err != nil || st.Snapshot.ID != "" || st.Rows != nil || st.Mark != markOf(t, s) {
			t.Fatalf("the state of an empty store is %+v, %v; want no snapshot, no rows and the store's mark", st, err)
		}
		activate(t, s, "s1")
		seedPolicy(t, s, "beta", "active", "beta v1")
		seedPolicy(t, s, "alpha", "draft", "alpha v1")
		st, err = s.Drafts().PolicyState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		mark, _ := s.Drafts().PolicyMark(ctx)
		if st.Snapshot.ID != "s1" || string(st.Snapshot.Blob) != "blob s1" || st.Mark != mark ||
			len(st.Rows) != 2 || st.Rows[0].Name != "alpha" || st.Rows[1].YAMLSource != "beta v1" {
			t.Errorf("the state is %+v; want snapshot s1 with its blob, the mark %+v and the rows alpha and beta", st, mark)
		}
	})
}

// TestPolicyStateReadsOneMoment pins the one read of the conversion on
// Postgres: a transaction locks the snapshots table, so PolicyState waits
// after its mark, and then saves a row, activates another snapshot and
// moves the generation. PolicyState must answer the moment before that
// commit in every part.
func TestPolicyStateReadsOneMoment(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		if sq.d != dialectPostgres {
			t.Skip("sqlite's one connection runs no transaction beside another")
		}
		ctx := context.Background()
		activate(t, s, "s1")
		if _, err := s.Snapshots().Create(ctx, Snapshot{ID: "s2", SignerKeyID: "k1", Blob: []byte("blob s2")}); err != nil {
			t.Fatal(err)
		}
		seedPolicy(t, s, "guard", "active", "guard v1")
		mark := markOf(t, s)
		holder, err := sq.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		if _, err := holder.ExecContext(ctx, `LOCK TABLE snapshots IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		type answer struct {
			st  PolicyState
			err error
		}
		read := make(chan answer, 1)
		go func() {
			st, err := s.Drafts().PolicyState(ctx)
			read <- answer{st, err}
		}()
		waitForLockWait(t, s, `l.relation = 'snapshots'::regclass`)
		for _, q := range []string{`UPDATE policy_sets SET yaml_source = 'guard v2', updated_at = now() WHERE name = 'guard'`,
			`UPDATE snapshots SET active = (id = 's2')`, `UPDATE config_generation SET generation = generation + 1 WHERE id = 1`} {
			if _, err := holder.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		got := <-read
		if got.err != nil {
			t.Fatal(got.err)
		}
		if st := got.st; st.Mark != mark || st.Snapshot.ID != "s1" || len(st.Rows) != 1 || st.Rows[0].YAMLSource != "guard v1" {
			t.Errorf("the read that waited answered the mark %+v, snapshot %s, rows %+v; want the mark %+v, s1 and guard v1",
				st.Mark, st.Snapshot.ID, st.Rows, mark)
		}
		st, err := s.Drafts().PolicyState(ctx)
		if err != nil || st.Mark.Generation != 1 || st.Snapshot.ID != "s2" || st.Rows[0].YAMLSource != "guard v2" {
			t.Errorf("the next read answered %+v, %v; want generation 1, s2 and guard v2", st, err)
		}
	})
}
