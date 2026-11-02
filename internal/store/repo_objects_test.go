package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

// TestListingScopeMigration pins migration 38 on both drivers: a listing
// credential stores after it, the move down one migration deletes the
// listing rows and keeps every other row, refuses the listing scope again
// and drops the object list tables, and the move back up restores them.
func TestListingScopeMigration(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		_, head, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		if head < 38 {
			t.Fatalf("newest migration = %d, want 38 or later", head)
		}
		app := mustApp(t, s, "objects-demo")
		appRow := mustCred(t, s, Credential{AppID: app.ID, Scope: CredScopeApp, OwnerID: app.ID, Kind: CredStatic})
		mustCred(t, s, Credential{AppID: app.ID, Scope: CredScopeListing, OwnerID: app.ID, Kind: CredStatic})
		if err := s.ObjectLists().Ensure(ctx, app.ID, "repo", "h1", now()); err != nil {
			t.Fatal(err)
		}

		// A move down runs one migration, so the migrations above 38 go first.
		for v := head; v >= 38; v-- {
			if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
				t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
			}
		}
		rows, err := s.Credentials().ListByApp(ctx, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ID != appRow.ID {
			t.Errorf("credentials after the move down = %+v, want only the app row", rows)
		}
		if _, err := s.Credentials().Create(ctx, Credential{AppID: app.ID, Scope: CredScopeListing, OwnerID: app.ID, Kind: CredStatic, EncPayload: []byte("x")}); err == nil {
			t.Error("the scope check took a listing row below migration 38")
		}
		for _, table := range []string{"object_lists", "objects"} {
			if _, err := sq.exec(ctx, "SELECT 1 FROM "+table); err == nil {
				t.Errorf("table %s survived the move down", table)
			}
		}

		if v, err := sq.MigrateTo(ctx, head); err != nil || v != head {
			t.Fatalf("MigrateTo(%d) = %d, %v", head, v, err)
		}
		mustCred(t, s, Credential{AppID: app.ID, Scope: CredScopeListing, OwnerID: app.ID, Kind: CredStatic})
		if err := s.ObjectLists().Ensure(ctx, app.ID, "repo", "h1", now()); err != nil {
			t.Errorf("Ensure after the move back up: %v", err)
		}
	})
}

// TestObjectListLifecycle pins the row of one server through its life: a
// never-listed row, a claim only one owner gets, a first and a changed
// listing with versions, counts, the version each id joined at and the
// ids whose value the listing changed, a failed listing that keeps the
// objects, fencing on the claim and the declaration, a reset for a new
// declaration, the due marks and delete.
func TestObjectListLifecycle(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		ol := s.ObjectLists()
		app := mustApp(t, s, "objects-demo")
		t0 := now()

		if err := ol.Ensure(ctx, app.ID, "repo", "h1", t0); err != nil {
			t.Fatal(err)
		}
		if err := ol.Ensure(ctx, app.ID, "project", "h9", t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		row := mustList(t, ol, app.ID)
		if row.Type != "repo" || row.DeclHash != "h1" || row.Version != 0 || row.Outcome != ObjectsNever ||
			!row.DueAt.Equal(t0) || row.ListedAt != nil || row.AttemptedAt != nil || row.ClaimOwner != "" {
			t.Fatalf("ensured row = %+v", row)
		}

		if _, ok, err := ol.Claim(ctx, app.ID, "pod-a", t0.Add(-time.Second), t0.Add(6*time.Minute)); err != nil || ok {
			t.Fatalf("claim before due = %v, %v; want false", ok, err)
		}
		claimed, ok, err := ol.Claim(ctx, app.ID, "pod-a", t0, t0.Add(6*time.Minute))
		if err != nil || !ok || claimed.ClaimOwner != "pod-a" || claimed.ClaimUntil == nil {
			t.Fatalf("claim when due = %+v, %v, %v", claimed, ok, err)
		}
		if _, ok, err := ol.Claim(ctx, app.ID, "pod-b", t0.Add(time.Minute), t0.Add(7*time.Minute)); err != nil || ok {
			t.Fatalf("second claim under a live claim = %v, %v; want false", ok, err)
		}

		first := []ListedObject{
			{ID: "2", Value: "Strazahq/site", Title: "site", Attributes: `{"area":"web"}`},
			{ID: "1", Value: "Strazahq/api", Title: "api", Attributes: `{"area":["product","core"]}`},
		}
		at1, due1 := t0.Add(time.Second), t0.Add(10*time.Minute)
		if _, err := ol.Replace(ctx, app.ID, "pod-b", "h1", "lh1", first, Listing{Outcome: ObjectsOK, At: at1, Due: due1}); !errors.Is(err, ErrNotFound) {
			t.Errorf("replace under another owner's claim = %v, want ErrNotFound", err)
		}
		row, err = ol.Replace(ctx, app.ID, "pod-a", "h1", "lh1", first, Listing{Outcome: ObjectsOK, At: at1, Due: due1})
		if err != nil {
			t.Fatal(err)
		}
		if row.Version != 1 || row.Count != 2 || row.Joined != 2 || row.Left != 0 || row.ListHash != "lh1" ||
			row.Outcome != ObjectsOK || row.ClaimOwner != "" || row.ClaimUntil != nil ||
			!timeIs(row.ListedAt, at1) || !timeIs(row.AttemptedAt, at1) || !row.DueAt.Equal(due1) || len(row.Renamed) != 0 {
			t.Fatalf("row after the first listing = %+v, want no renamed id from a listing where every id joins", row)
		}
		assertObjects(t, ol, app.ID, []ListedObject{
			{ID: "1", Value: "Strazahq/api", Title: "api", Attributes: `{"area":["product","core"]}`, AddedVersion: 1},
			{ID: "2", Value: "Strazahq/site", Title: "site", Attributes: `{"area":"web"}`, AddedVersion: 1},
		})

		if _, ok, err := ol.Claim(ctx, app.ID, "pod-b", due1, due1.Add(6*time.Minute)); err != nil || !ok {
			t.Fatalf("claim at the next due time = %v, %v", ok, err)
		}
		second := []ListedObject{
			{ID: "1", Value: "Strazahq/api-v2", Title: "api", Attributes: `{}`},
			{ID: "3", Value: "Strazahq/pay", Title: "pay", Attributes: `{"area":"product"}`},
		}
		at2 := due1.Add(time.Second)
		row, err = ol.Replace(ctx, app.ID, "pod-b", "h1", "lh2", second, Listing{Outcome: ObjectsOK, At: at2, Due: at2.Add(10 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if row.Version != 2 || row.Count != 2 || row.Joined != 1 || row.Left != 1 || !slices.Equal(row.Renamed, []string{"1"}) {
			t.Fatalf("row after the changed listing = %+v, want version 2 with one joined, one left and id 1 renamed", row)
		}
		changed := []ListedObject{
			{ID: "1", Value: "Strazahq/api-v2", Title: "api", Attributes: `{}`, AddedVersion: 1},
			{ID: "3", Value: "Strazahq/pay", Title: "pay", Attributes: `{"area":"product"}`, AddedVersion: 2},
		}
		assertObjects(t, ol, app.ID, changed)

		at3 := at2.Add(10 * time.Minute)
		if _, ok, err := ol.Claim(ctx, app.ID, "pod-a", at3, at3.Add(6*time.Minute)); err != nil || !ok {
			t.Fatalf("claim for the failing listing = %v, %v", ok, err)
		}
		if _, err := ol.Settle(ctx, app.ID, "pod-a", "h0", Listing{Outcome: ObjectsFailed, Reason: "r", At: at3, Due: at3}); !errors.Is(err, ErrNotFound) {
			t.Errorf("settle under another declaration = %v, want ErrNotFound", err)
		}
		row, err = ol.Settle(ctx, app.ID, "pod-a", "h1", Listing{Outcome: ObjectsFailed, Reason: "repo_list answered an error.", At: at3, Due: at3.Add(10 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if row.Outcome != ObjectsFailed || row.Reason != "repo_list answered an error." || row.Version != 2 || row.Count != 2 ||
			!timeIs(row.ListedAt, at2) || !timeIs(row.AttemptedAt, at3) || row.ClaimOwner != "" {
			t.Fatalf("row after the failed listing = %+v", row)
		}
		assertObjects(t, ol, app.ID, changed)

		// A claim that outlived its until is free to take.
		at4 := at3.Add(10 * time.Minute)
		if _, ok, _ := ol.Claim(ctx, app.ID, "pod-a", at4, at4.Add(6*time.Minute)); !ok {
			t.Fatal("claim at the fourth due time failed")
		}
		if _, ok, err := ol.Claim(ctx, app.ID, "pod-b", at4.Add(7*time.Minute), at4.Add(13*time.Minute)); err != nil || !ok {
			t.Fatalf("claim after the first one expired = %v, %v", ok, err)
		}
		row, err = ol.Settle(ctx, app.ID, "pod-b", "h1", Listing{Outcome: ObjectsOK, At: at4.Add(7 * time.Minute), Due: at4.Add(17 * time.Minute)})
		if err != nil || row.Outcome != ObjectsOK || row.Reason != "" || row.Version != 2 || !timeIs(row.ListedAt, at4.Add(7*time.Minute)) {
			t.Fatalf("unchanged listing settled %+v, %v", row, err)
		}

		if ok, err := ol.MarkDueIfIdle(ctx, app.ID, at4.Add(8*time.Minute), at4.Add(6*time.Minute)); err != nil || ok {
			t.Errorf("due mark a minute after the last attempt = %v, %v; want false", ok, err)
		}
		if ok, err := ol.MarkDueIfIdle(ctx, app.ID, at4.Add(9*time.Minute), at4.Add(8*time.Minute)); err != nil || !ok {
			t.Errorf("due mark after the minute = %v, %v; want true", ok, err)
		}
		if row := mustList(t, ol, app.ID); !row.DueAt.Equal(at4.Add(9 * time.Minute)) {
			t.Errorf("due at = %v, want the marked time", row.DueAt)
		}
		if err := ol.MarkDue(ctx, app.ID, t0); err != nil || !mustList(t, ol, app.ID).DueAt.Equal(t0) {
			t.Errorf("MarkDue = %v", err)
		}

		if err := ol.Reset(ctx, app.ID, "project", "h2", at4); err != nil {
			t.Fatal(err)
		}
		row = mustList(t, ol, app.ID)
		if row.Type != "project" || row.DeclHash != "h2" || row.Version != 2 || row.Outcome != ObjectsNever ||
			row.Count != 0 || row.ListHash != "" || row.Reason != "" || !row.DueAt.Equal(at4) {
			t.Errorf("row after reset = %+v", row)
		}
		assertObjects(t, ol, app.ID, nil)
		if err := ol.Reset(ctx, app.ID, "project", "h2", at4); !errors.Is(err, ErrNotFound) {
			t.Errorf("a second reset to the same declaration = %v, want ErrNotFound", err)
		}

		if err := ol.Delete(ctx, app.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := ol.Get(ctx, app.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get after delete = %v, want ErrNotFound", err)
		}
		if err := ol.Delete(ctx, app.ID); err != nil {
			t.Errorf("delete of an absent row = %v", err)
		}
		if err := ol.MarkDue(ctx, app.ID, t0); !errors.Is(err, ErrNotFound) {
			t.Errorf("MarkDue of an absent row = %v, want ErrNotFound", err)
		}
		if _, err := ol.MarkDueIfIdle(ctx, app.ID, t0, t0); !errors.Is(err, ErrNotFound) {
			t.Errorf("MarkDueIfIdle of an absent row = %v, want ErrNotFound", err)
		}
	})
}

// TestObjectListReplaceMany pins a listing larger than one insert batch,
// and that the store refuses two objects with one value.
func TestObjectListReplaceMany(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		ol := s.ObjectLists()
		app := mustApp(t, s, "objects-demo")
		t0 := now()
		if err := ol.Ensure(ctx, app.ID, "repo", "h1", t0); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := ol.Claim(ctx, app.ID, "pod-a", t0, t0.Add(time.Minute)); err != nil || !ok {
			t.Fatalf("claim = %v, %v", ok, err)
		}
		dup := []ListedObject{{ID: "1", Value: "same", Attributes: "{}"}, {ID: "2", Value: "same", Attributes: "{}"}}
		if _, err := ol.Replace(ctx, app.ID, "pod-a", "h1", "x", dup, Listing{Outcome: ObjectsOK, At: t0, Due: t0}); err == nil {
			t.Error("the store took two objects with one value")
		}
		if got, err := ol.Objects(ctx, app.ID); err != nil || len(got) != 0 {
			t.Errorf("objects after the refused replace = %d, %v; want none", len(got), err)
		}
		many := make([]ListedObject, 1203)
		for i := range many {
			many[i] = ListedObject{ID: fmt.Sprint(i), Value: fmt.Sprintf("org/repo-%05d", i), Attributes: "{}"}
		}
		row, err := ol.Replace(ctx, app.ID, "pod-a", "h1", "many", many, Listing{Outcome: ObjectsOK, At: t0, Due: t0})
		if err != nil {
			t.Fatal(err)
		}
		got, err := ol.Objects(ctx, app.ID)
		if err != nil || len(got) != len(many) || row.Count != len(many) || got[0].Value != "org/repo-00000" || got[1202].Value != "org/repo-01202" {
			t.Errorf("objects = %d (count %d), %v", len(got), row.Count, err)
		}
	})
}

// TestObjectListReplaceKeepsAnUnchangedID pins that a listing in which one
// id keeps its value while another joins reports no renamed id.
func TestObjectListReplaceKeepsAnUnchangedID(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		ol := s.ObjectLists()
		app := mustApp(t, s, "objects-demo")
		t0 := now()
		if err := ol.Ensure(ctx, app.ID, "repo", "h1", t0); err != nil {
			t.Fatal(err)
		}
		for i, objs := range [][]ListedObject{
			{{ID: "1", Value: "org/api", Attributes: "{}"}},
			{{ID: "1", Value: "org/api", Attributes: "{}"}, {ID: "2", Value: "org/web", Attributes: "{}"}},
		} {
			if _, ok, err := ol.Claim(ctx, app.ID, "pod-a", t0, t0.Add(time.Minute)); err != nil || !ok {
				t.Fatalf("claim for listing %d = %v, %v", i+1, ok, err)
			}
			row, err := ol.Replace(ctx, app.ID, "pod-a", "h1", fmt.Sprint("lh", i), objs, Listing{Outcome: ObjectsOK, At: t0, Due: t0})
			if err != nil {
				t.Fatal(err)
			}
			if len(row.Renamed) != 0 {
				t.Errorf("listing %d renamed %v, want none", i+1, row.Renamed)
			}
		}
	})
}

func mustApp(t *testing.T, s Store, name string) App {
	t.Helper()
	app, err := s.Apps().Create(context.Background(), App{Name: name, RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func mustCred(t *testing.T, s Store, c Credential) Credential {
	t.Helper()
	c.EncPayload = []byte("sealed")
	row, err := s.Credentials().Create(context.Background(), c)
	if err != nil {
		t.Fatalf("create %s credential: %v", c.Scope, err)
	}
	return row
}

func mustList(t *testing.T, ol ObjectListRepo, appID string) ObjectList {
	t.Helper()
	row, err := ol.Get(context.Background(), appID)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func timeIs(got *time.Time, want time.Time) bool { return got != nil && got.Equal(want) }

// assertObjects compares the stored objects with want, the attributes as
// JSON values, because Postgres reformats jsonb text.
func assertObjects(t *testing.T, ol ObjectListRepo, appID string, want []ListedObject) {
	t.Helper()
	got, err := ol.Objects(context.Background(), appID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("objects = %+v, want %+v", got, want)
	}
	for i := range want {
		var ga, wa any
		if err := json.Unmarshal([]byte(got[i].Attributes), &ga); err != nil {
			t.Fatalf("attributes %q: %v", got[i].Attributes, err)
		}
		_ = json.Unmarshal([]byte(want[i].Attributes), &wa)
		g, w := got[i], want[i]
		g.Attributes, w.Attributes = "", ""
		if g != w || !reflect.DeepEqual(ga, wa) {
			t.Errorf("object %d = %+v %v, want %+v %v", i, got[i], ga, want[i], wa)
		}
	}
}
