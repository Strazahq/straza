package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestAppSetStatusWritesOnlyTheStatus pins the write a replica makes when its
// own instance of a server changes health: the status and updated_at change
// and no other column does, so a replica whose copy of the row is older
// never writes back a manifest, version or source that another replica
// changed. A removed or unknown row answers ErrNotFound and stays as it was.
func TestAppSetStatusWritesOnlyTheStatus(t *testing.T) {
	cases := []struct {
		name string
		// prepare changes the row the way another replica would, after this
		// replica read it, and returns the id this replica writes to.
		prepare func(t *testing.T, s Store, read App) string
		wantErr error
	}{
		{name: "a row another replica changed after this one read it", prepare: func(t *testing.T, s Store, read App) string {
			changed := read
			changed.Version, changed.Manifest, changed.Source = "2.0.0", `{"url":"https://two.example/mcp"}`, AppSourceGitops
			if _, err := s.Apps().Update(context.Background(), changed); err != nil {
				t.Fatal(err)
			}
			return read.ID
		}},
		{name: "a row another replica removed", wantErr: ErrNotFound, prepare: func(t *testing.T, s Store, read App) string {
			if err := s.Apps().SoftDelete(context.Background(), read.ID); err != nil {
				t.Fatal(err)
			}
			return read.ID
		}},
		{name: "an id no row has", wantErr: ErrNotFound, prepare: func(*testing.T, Store, App) string { return "no-such-app" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				ctx := context.Background()
				read, err := s.Apps().Create(ctx, App{Name: "notes", Version: "1.0.0", Manifest: `{"url":"https://one.example/mcp"}`,
					RuntimeKind: "remote", Status: "running", Source: AppSourceAPI})
				if err != nil {
					t.Fatal(err)
				}
				id := tc.prepare(t, s, read)
				rows, err := s.Apps().List(ctx)
				if err != nil {
					t.Fatal(err)
				}

				err = s.Apps().SetStatus(ctx, id, "degraded")
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("SetStatus = %v, want %v", err, tc.wantErr)
				}
				after, err := s.Apps().List(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if tc.wantErr != nil {
					if !reflect.DeepEqual(after, rows) {
						t.Errorf("a refused status write changed the rows:\n got %+v\nwant %+v", after, rows)
					}
					return
				}
				if len(rows) != 1 || len(after) != 1 {
					t.Fatalf("rows before %d, after %d, want 1 each", len(rows), len(after))
				}
				want := rows[0]
				if after[0].UpdatedAt.Before(want.UpdatedAt) {
					t.Errorf("updated_at went back from %s to %s", want.UpdatedAt, after[0].UpdatedAt)
				}
				want.Status, want.UpdatedAt = "degraded", after[0].UpdatedAt
				if !reflect.DeepEqual(after[0], want) {
					t.Errorf("the status write touched another column:\n got %+v\nwant %+v", after[0], want)
				}
			})
		})
	}
}
