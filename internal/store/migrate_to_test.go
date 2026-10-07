package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// withStepsAbove gives sq two migrations above its newest embedded one,
// each of which creates one table named step_<version>, and answers that
// newest embedded version. A test moves the database between them, because
// the embedded migrations are one baseline and hold no step to move across.
// The cleanup moves the database back down and restores the embedded
// migrations, because the Postgres tests share one database.
func withStepsAbove(t *testing.T, sq *sqlStore) uint {
	t.Helper()
	embedded := sq.migrations
	_, base, err := sq.migrationRange()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(embedded, sq.migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for _, e := range entries {
		data, err := fs.ReadFile(embedded, sq.migrationsDir+"/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		files[sq.migrationsDir+"/"+e.Name()] = &fstest.MapFile{Data: data}
	}
	for v := base + 1; v <= base+2; v++ {
		name := fmt.Sprintf("%s/%06d_step", sq.migrationsDir, v)
		files[name+".up.sql"] = &fstest.MapFile{Data: []byte(fmt.Sprintf("CREATE TABLE step_%d (id INTEGER PRIMARY KEY);", v))}
		files[name+".down.sql"] = &fstest.MapFile{Data: []byte(fmt.Sprintf("DROP TABLE step_%d;", v))}
	}
	sq.migrations = files
	t.Cleanup(func() {
		for v, _ := schemaVersion(t, sq); v > base; v-- {
			if _, err := sq.MigrateTo(context.Background(), v-1); err != nil {
				t.Errorf("move the database back down to %d: %v", v-1, err)
				break
			}
		}
		sq.migrations = embedded
	})
	return base
}

// schemaVersion reads the migration version and dirty flag as
// golang-migrate recorded them, 0 for a database with none. It reads the
// row with SQL, because every golang-migrate driver on Postgres holds one
// pooled connection for good, and a test that built one per read would
// empty the pool.
func schemaVersion(t *testing.T, sq *sqlStore) (uint, bool) {
	t.Helper()
	var v int64
	var dirty bool
	err := sq.queryRow(context.Background(), `SELECT version, dirty FROM schema_migrations`).Scan(&v, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return uint(v), dirty
}

// hasStepTable reports whether the table a step above the baseline creates
// exists.
func hasStepTable(t *testing.T, sq *sqlStore, version uint) bool {
	t.Helper()
	_, err := sq.db.ExecContext(context.Background(), fmt.Sprintf("SELECT 1 FROM step_%d", version))
	return err == nil
}

// TestMigrateToMovesUpAnyDistanceAndDownOneStep pins both moves on both
// drivers with two steps above the embedded migrations. A move up crosses
// both steps in one run. A move down runs one down step, records the
// version it ends at, and leaves a database that the release without the
// undone step starts on, which it refused before the move. A second move
// to the same version changes nothing.
func TestMigrateToMovesUpAnyDistanceAndDownOneStep(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		embedded := sq.migrations
		base := withStepsAbove(t, sq)
		withSteps := sq.migrations
		top := base + 2
		// asRelease runs fn as the strazad release whose migrations stop at
		// the embedded ones.
		asRelease := func(fn func() error) error {
			sq.migrations = embedded
			defer func() { sq.migrations = withSteps }()
			return fn()
		}

		if v, err := sq.MigrateTo(ctx, top); err != nil || v != top {
			t.Fatalf("MigrateTo(%d) = %d, %v; want %d", top, v, err, top)
		}
		if !hasStepTable(t, sq, top-1) || !hasStepTable(t, sq, top) {
			t.Fatalf("the move up to %d left a step's table out", top)
		}
		if err := asRelease(func() error { return s.Migrate(ctx) }); err == nil {
			t.Fatalf("the release at %d started on a database at %d, so the test cannot tell a rollback from none", base, top)
		}

		if v, err := sq.MigrateTo(ctx, top-1); err != nil || v != top-1 {
			t.Fatalf("MigrateTo(%d) = %d, %v; want %d", top-1, v, err, top-1)
		}
		if v, dirty := schemaVersion(t, sq); v != top-1 || dirty {
			t.Fatalf("recorded version after the move down = %d dirty=%v, want %d clean", v, dirty, top-1)
		}
		if hasStepTable(t, sq, top) || !hasStepTable(t, sq, top-1) {
			t.Errorf("the move down to %d did not undo exactly the step above it", top-1)
		}
		if v, err := sq.MigrateTo(ctx, top-1); !errors.Is(err, ErrSchemaUnchanged) || v != top-1 {
			t.Errorf("a second MigrateTo(%d) = %d, %v; want %d and ErrSchemaUnchanged", top-1, v, err, top-1)
		}

		if v, err := sq.MigrateTo(ctx, base); err != nil || v != base {
			t.Fatalf("MigrateTo(%d) = %d, %v; want %d", base, v, err, base)
		}
		if hasStepTable(t, sq, top-1) {
			t.Errorf("the move down to %d left the table of step %d", base, top-1)
		}
		if err := asRelease(func() error { return s.Migrate(ctx) }); err != nil {
			t.Fatalf("the release at %d does not start on the database moved down to it: %v", base, err)
		}
	})
}

// TestMigrateToRefuses pins every refusal of MigrateTo on both drivers:
// each one says what failed and what to do, and moves nothing. The database
// sits two steps above the embedded migrations, so that a move of two
// steps down exists to refuse.
func TestMigrateToRefuses(t *testing.T) {
	cases := []struct {
		name string
		to   func(first, head uint) uint
		// prepare leaves the database in the state the case needs, and
		// restore puts it back so that the next case starts clean.
		prepare, restore func(t *testing.T, sq *sqlStore, first, head uint)
		want             []string
	}{
		{
			name: "a version above the newest migration",
			to:   func(_, head uint) uint { return head + 1 },
			want: []string{"this strazad knows migrations up to {head}, so it cannot move the database to {head+1}",
				"Run the command with a strazad release that has migration {head+1}"},
		},
		{
			// A slipped digit, 3 for 38, names no migration this binary has.
			name: "a version below the first migration",
			to:   func(first, _ uint) uint { return first - 1 },
			want: []string{"this strazad has no migration {first-1}. Its migrations run from {first} to {head}, so name one of those"},
		},
		{
			name: "a version more than one below the database's",
			to:   func(_, head uint) uint { return head - 2 },
			want: []string{"the database is at migration {head}, and the command moves it down one migration per run, " +
				"because each down step can drop tables and the rows they hold",
				"Next run strazad migrate --to {head-1}, with the other flags as they were"},
		},
		{
			name:    "a database marked dirty",
			to:      func(_, head uint) uint { return head - 1 },
			prepare: func(t *testing.T, sq *sqlStore, _, _ uint) { setSchemaRow(t, sq, "dirty", true) },
			restore: func(t *testing.T, sq *sqlStore, _, _ uint) { setSchemaRow(t, sq, "dirty", false) },
			want: []string{"the database is marked dirty at migration {head}, because a migration failed part way",
				"Restore the backup taken before the upgrade, or repair the schema by hand, then run the command again"},
		},
		{
			name:    "a database ahead of this binary",
			to:      func(_, head uint) uint { return head - 1 },
			prepare: func(t *testing.T, sq *sqlStore, _, head uint) { setSchemaRow(t, sq, "version", int64(head)+1) },
			restore: func(t *testing.T, sq *sqlStore, _, head uint) { setSchemaRow(t, sq, "version", int64(head)) },
			want: []string{"the database is at migration {head+1}, and this strazad knows migrations up to {head}, so it cannot move it",
				"Run the command with a strazad release that has migration {head+1}"},
		},
		{
			name:    "a database older than the first migration",
			to:      func(first, _ uint) uint { return first },
			prepare: func(t *testing.T, sq *sqlStore, first, _ uint) { setSchemaRow(t, sq, "version", int64(first)-1) },
			restore: func(t *testing.T, sq *sqlStore, _, head uint) { setSchemaRow(t, sq, "version", int64(head)) },
			want: []string{"the database is at migration {first-1}, and this strazad's migrations start at {first}, so it cannot upgrade it",
				"Upgrade the database with an earlier strazad release whose migrations reach {first}, then start this one"},
		},
		{
			name: "a database with no migration applied",
			to:   func(_, head uint) uint { return head - 1 },
			prepare: func(t *testing.T, sq *sqlStore, _, _ uint) {
				if err := sq.migrateDown(); err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T, sq *sqlStore, _, _ uint) {
				if err := sq.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"the database has no Straza migration applied, so there is nothing to move",
				"Give the command the same --config, --profile, --data-dir and --store-dsn that strazad serve runs with"},
		},
	}
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		withStepsAbove(t, sq)
		first, head, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		if v, err := sq.MigrateTo(context.Background(), head); err != nil || v != head {
			t.Fatalf("MigrateTo(%d) = %d, %v; want %d", head, v, err, head)
		}
		words := strings.NewReplacer(
			"{head+1}", strconv.Itoa(int(head)+1), "{head-1}", strconv.Itoa(int(head)-1), "{head}", strconv.Itoa(int(head)),
			"{first-1}", strconv.Itoa(int(first)-1), "{first}", strconv.Itoa(int(first)))
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if tc.prepare != nil {
					tc.prepare(t, sq, first, head)
				}
				if tc.restore != nil {
					t.Cleanup(func() { tc.restore(t, sq, first, head) })
				}
				before, dirtyBefore := schemaVersion(t, sq)
				_, err := sq.MigrateTo(context.Background(), tc.to(first, head))
				if err == nil {
					t.Fatal("MigrateTo moved the database, want a refusal")
				}
				for _, w := range tc.want {
					if w = words.Replace(w); !strings.Contains(err.Error(), w) {
						t.Errorf("refusal %q lacks %q", err, w)
					}
				}
				if after, dirtyAfter := schemaVersion(t, sq); after != before || dirtyAfter != dirtyBefore {
					t.Errorf("the refusal moved the database from %d dirty=%v to %d dirty=%v", before, dirtyBefore, after, dirtyAfter)
				}
			})
		}
	})
}

// TestMigrateRefusesADatabaseBelowTheFirstMigration pins the start-up
// refusal on both drivers: a database older than the first embedded
// migration has no step here that upgrades it, so Migrate says so and
// names the fix, and leaves the recorded version as it was.
func TestMigrateRefusesADatabaseBelowTheFirstMigration(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		first, _, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		setSchemaRow(t, sq, "version", int64(first)-1)
		t.Cleanup(func() { setSchemaRow(t, sq, "version", int64(first)) })
		err = s.Migrate(context.Background())
		if err == nil {
			t.Fatal("Migrate started on a database below the first migration, want a refusal")
		}
		for _, w := range []string{
			fmt.Sprintf("the database is at migration %d, and this strazad's migrations start at %d, so it cannot upgrade it", first-1, first),
			fmt.Sprintf("Upgrade the database with an earlier strazad release whose migrations reach %d, then start this one", first),
		} {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("refusal %q lacks %q", err, w)
			}
		}
		if v, dirty := schemaVersion(t, sq); v != first-1 || dirty {
			t.Errorf("the refusal moved the database to %d dirty=%v, want %d clean", v, dirty, first-1)
		}
	})
}

// setSchemaRow writes one column of golang-migrate's version row.
func setSchemaRow(t *testing.T, sq *sqlStore, column string, v any) {
	t.Helper()
	if _, err := sq.exec(context.Background(), "UPDATE schema_migrations SET "+column+" = $1", v); err != nil {
		t.Fatalf("set schema_migrations.%s: %v", column, err)
	}
}
