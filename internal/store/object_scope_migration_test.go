package store

import (
	"context"
	"database/sql"
	"testing"
)

// TestObjectScopeMigration pins migration 39 on both drivers. The move up
// adds object_scope and backfills {"every":true} on exactly the bindings
// whose server's stored manifest pins an object type, so a pinned server's
// roles keep today's reach. The move down drops the column and keeps every
// binding.
func TestObjectScopeMigration(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		_, head, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		if head < 39 {
			t.Fatalf("newest migration = %d, want 39 or later", head)
		}
		// down moves the database to 38, one migration per run.
		down := func() {
			t.Helper()
			for v := head; v >= 39; v-- {
				if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
					t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
				}
			}
		}
		manifests := map[string]string{
			"pinned":     `{"straza":{"objects":{"type":"repo","plural":"repos","list":{"tool":"repo_list"}}}}`,
			"unpinned":   `{"straza":{"runtime":{"kind":"remote"}}}`,
			"empty-type": `{"straza":{"objects":{"type":""}}}`,
			"no-straza":  `{}`,
		}
		roleOf, appOf := map[string]string{}, map[string]string{}
		for name, mf := range manifests {
			app, err := s.Apps().Create(ctx, App{Name: name, RuntimeKind: "remote", Manifest: mf})
			if err != nil {
				t.Fatal(err)
			}
			ro, err := s.Roles().Create(ctx, Role{Name: name + "-read", Kind: RoleKindApplication})
			if err != nil {
				t.Fatal(err)
			}
			roleOf[name], appOf[name] = ro.ID, app.ID
		}
		down()
		for name := range manifests {
			if _, err := sq.exec(ctx, `INSERT INTO tool_bindings (id, role_id, app_id, tool_matcher, effect, created_at)
				VALUES ($1, $2, $3, $4, 'allow', $5)`, "b-"+name, roleOf[name], appOf[name], `["*"]`, sq.tArg(now())); err != nil {
				t.Fatal(err)
			}
		}

		if v, err := sq.MigrateTo(ctx, head); err != nil || v != head {
			t.Fatalf("MigrateTo(%d) = %d, %v", head, v, err)
		}
		for name := range manifests {
			var got sql.NullString
			if err := sq.queryRow(ctx, `SELECT object_scope FROM tool_bindings WHERE id = $1`, "b-"+name).Scan(&got); err != nil {
				t.Fatal(err)
			}
			want := sql.NullString{}
			if name == "pinned" {
				want = sql.NullString{String: `{"every":true}`, Valid: true}
			}
			if got != want {
				t.Errorf("object_scope of the %s server's binding = %+v, want %+v", name, got, want)
			}
		}
		rows, err := s.ToolBindings().ListByRole(ctx, roleOf["pinned"])
		if err != nil || len(rows) != 1 || rows[0].Scope == nil || !rows[0].Scope.Every {
			t.Errorf("the pinned server's binding reads %+v, %v; want covers every", rows, err)
		}

		down()
		if _, err := sq.exec(ctx, `SELECT object_scope FROM tool_bindings`); err == nil {
			t.Error("object_scope survived the move down")
		}
		var n int
		if err := sq.queryRow(ctx, `SELECT COUNT(*) FROM tool_bindings`).Scan(&n); err != nil || n != len(manifests) {
			t.Errorf("bindings after the move down = %d, %v; want %d", n, err, len(manifests))
		}
		if v, err := sq.MigrateTo(ctx, head); err != nil || v != head {
			t.Fatalf("MigrateTo(%d) back up = %d, %v", head, v, err)
		}
	})
}
