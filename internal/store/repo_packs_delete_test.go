package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPacksDeleteRefusesWhileBound pins the delete guard: the bound check is
// part of the delete statement, so a pack a role is still bound to affects
// no row and keeps its binding, the same pack deletes once unbound, and a
// second delete or an unknown id answers ErrNotFound.
func TestPacksDeleteRefusesWhileBound(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		dev, err := s.Roles().Create(ctx, Role{Name: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		pack, err := s.Packs().Create(ctx, KnowledgePack{Name: "golang-style", Version: "1", Content: "use gofmt", Checksum: "c1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Packs().Bind(ctx, dev.ID, pack.ID); err != nil {
			t.Fatal(err)
		}
		steps := []struct {
			name         string
			before       func() error
			id           string
			wantErr      error
			wantPack     bool
			wantBindings int
		}{
			{"a bound pack stays with its binding", nil, pack.ID, ErrNotFound, true, 1},
			{"an unknown id affects nothing", nil, "nope", ErrNotFound, true, 1},
			{"the unbound pack deletes", func() error { return s.Packs().Unbind(ctx, dev.ID, pack.ID) }, pack.ID, nil, false, 0},
			{"a second delete answers ErrNotFound", nil, pack.ID, ErrNotFound, false, 0},
		}
		for _, tc := range steps {
			if tc.before != nil {
				if err := tc.before(); err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
			}
			if err := s.Packs().Delete(ctx, tc.id); !errors.Is(err, tc.wantErr) {
				t.Fatalf("%s: Delete = %v, want %v", tc.name, err, tc.wantErr)
			}
			_, err := s.Packs().GetByID(ctx, pack.ID)
			if present := err == nil; present != tc.wantPack {
				t.Errorf("%s: pack present = %v (%v), want %v", tc.name, present, err, tc.wantPack)
			}
			bindings, err := s.Packs().ListBindings(ctx)
			if err != nil || len(bindings) != tc.wantBindings {
				t.Errorf("%s: bindings = %d (%v), want %d", tc.name, len(bindings), err, tc.wantBindings)
			}
		}
	})
}

// TestPacksDeleteWaitsForABindInFlight holds a bind open across the delete:
// the bind has passed its foreign key check, which locks the pack row on
// Postgres, and has not committed when the delete starts. Once the bind
// commits, the delete must see it, affect no row and leave the binding.
func TestPacksDeleteWaitsForABindInFlight(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		ctx := context.Background()
		dev, err := s.Roles().Create(ctx, Role{Name: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		pack, err := s.Packs().Create(ctx, KnowledgePack{Name: "golang-style", Version: "1", Content: "use gofmt", Checksum: "c1"})
		if err != nil {
			t.Fatal(err)
		}
		bind, err := sq.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bind.ExecContext(ctx, sq.q(`INSERT INTO pack_bindings (role_id, pack_id) VALUES ($1, $2)`), dev.ID, pack.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- s.Packs().Delete(ctx, pack.ID) }()
		if sq.d == dialectPostgres {
			// The commit must land while the delete waits on the row lock,
			// so poll until the server shows it waiting.
			waiting := false
			for i := 0; i < 250 && !waiting; i++ {
				var n int
				if err := sq.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity
					WHERE wait_event_type = 'Lock' AND query LIKE '%knowledge_packs%' AND pid <> pg_backend_pid()`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				waiting = n > 0
				time.Sleep(20 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("the delete never waited on the bind's row lock")
			}
		} else {
			time.Sleep(300 * time.Millisecond)
		}
		if err := bind.Commit(); err != nil {
			t.Fatalf("bind commit: %v", err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("Delete = %v, want ErrNotFound once the bind committed", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the delete never returned")
		}
		if _, err := s.Packs().GetByID(ctx, pack.ID); err != nil {
			t.Errorf("the pack is gone: %v", err)
		}
		if bindings, err := s.Packs().ListBindings(ctx); err != nil || len(bindings) != 1 {
			t.Errorf("bindings = %d (%v), want the one that committed", len(bindings), err)
		}
	})
}
