package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// forEachStore runs fn as a subtest against every available driver: sqlite
// always (temp file), postgres when STRAZA_TEST_POSTGRES_DSN is set (the CI
// test job provides a service container). The sqlite file starts as a copy
// of the migrated template from seedSQLite.
func forEachStore(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Helper()
	forEachStoreFrom(t, false, fn)
}

// forEachFreshStore is forEachStore with the sqlite file migrated from
// empty, for a test that proves the migrations themselves.
func forEachFreshStore(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Helper()
	forEachStoreFrom(t, true, fn)
}

// forEachStoreFrom runs the driver subtests. When fresh is false the sqlite
// file is seeded from the migrated template before Open.
func forEachStoreFrom(t *testing.T, fresh bool, fn func(t *testing.T, s Store)) {
	t.Helper()

	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.db")
		if !fresh {
			seedSQLite(t, path)
		}
		cfg := config.Config{Store: config.Store{
			Driver: config.DriverSQLite,
			DSN:    path,
		}}
		s, err := Open(cfg)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		fn(t, s)
	})

	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("STRAZA_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("STRAZA_TEST_POSTGRES_DSN not set")
		}
		s, err := Open(config.Config{Store: config.Store{Driver: config.DriverPostgres, DSN: dsn}})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		// Fresh schema per test: down (tolerates empty) then up. Postgres
		// tests must not run in parallel; they share one database.
		sq := s.(*sqlStore)
		if err := sq.migrateDown(); err != nil {
			t.Fatalf("migrateDown: %v", err)
		}
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		fn(t, s)
	})
}

var (
	sqliteTemplateOnce  sync.Once
	sqliteTemplateBytes []byte
	sqliteTemplateErr   error
)

// seedSQLite writes a sqlite database already migrated to the current schema
// to path, so the Migrate after Open has nothing to apply. Under the race
// detector migrating an empty file costs seconds per test and the copy costs
// a fraction of one. The template is migrated once per test binary through
// Open and Migrate. The storetest package does the same for other packages,
// but it imports this one, so the store tests keep their own copy.
func seedSQLite(t *testing.T, path string) {
	t.Helper()
	sqliteTemplateOnce.Do(func() { sqliteTemplateBytes, sqliteTemplateErr = buildSQLiteTemplate() })
	if sqliteTemplateErr != nil {
		t.Fatalf("migrated sqlite template: %v", sqliteTemplateErr)
	}
	if err := os.WriteFile(path, sqliteTemplateBytes, 0o600); err != nil {
		t.Fatal(err)
	}
}

// buildSQLiteTemplate migrates a fresh sqlite file and returns its bytes.
// Close checkpoints the write-ahead log into the main file; a log left
// behind would mean a partial copy, so it refuses one.
func buildSQLiteTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "straza-store-template-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "template.db")
	s, err := Open(config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: path}})
	if err != nil {
		return nil, err
	}
	if err := s.Migrate(context.Background()); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.Close(); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return nil, fmt.Errorf("write-ahead log of %d bytes left after close", fi.Size())
	}
	return os.ReadFile(path)
}

func TestMigrateUpDownUp(t *testing.T) {
	forEachFreshStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("second Migrate: %v", err)
		}
		sq := s.(*sqlStore)
		if err := sq.migrateDown(); err != nil {
			t.Fatalf("migrateDown: %v", err)
		}
		if _, err := s.Users().GetByID(ctx, "x"); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("expected missing-table error after down, got %v", err)
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate after down: %v", err)
		}
		if _, err := s.Users().GetByID(ctx, "x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound after re-up, got %v", err)
		}
	})
}

func TestUsersCRUD(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u, err := s.Users().Create(ctx, User{Username: "kim", Email: "kim@x.io", Display: "Kim", Attrs: `{"team":"sec"}`})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if u.ID == "" || u.Status != UserActive || u.Origin != OriginLocal {
			t.Errorf("defaults not applied: %+v", u)
		}

		if _, err := s.Users().Create(ctx, User{Username: "kim"}); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate username: want ErrConflict, got %v", err)
		}

		got, err := s.Users().GetByUsername(ctx, "kim")
		if err != nil || got.ID != u.ID || got.Attrs == "" {
			t.Fatalf("GetByUsername = %+v, %v", got, err)
		}
		if !got.CreatedAt.Equal(u.CreatedAt) {
			t.Errorf("created_at round-trip: got %v want %v", got.CreatedAt, u.CreatedAt)
		}

		u.Email = "kim@corp.io"
		u.ExternalID = "scim-123"
		if _, err := s.Users().Update(ctx, u); err != nil {
			t.Fatalf("Update: %v", err)
		}
		byExt, err := s.Users().GetByExternalID(ctx, "scim-123")
		if err != nil || byExt.Email != "kim@corp.io" {
			t.Fatalf("GetByExternalID = %+v, %v", byExt, err)
		}

		lee, err := s.Users().Create(ctx, User{Username: "lee"})
		if err != nil {
			t.Fatal(err)
		}
		users, err := s.Users().List(ctx)
		if err != nil || len(users) != 2 {
			t.Fatalf("List = %d users, %v", len(users), err)
		}

		batch, err := s.Users().GetByIDs(ctx, []string{u.ID, lee.ID, "no-such-id"})
		if err != nil || len(batch) != 2 {
			t.Fatalf("GetByIDs = %d users, %v (missing ids must just be absent)", len(batch), err)
		}
		if empty, err := s.Users().GetByIDs(ctx, nil); err != nil || len(empty) != 0 {
			t.Errorf("GetByIDs(nil) = %v, %v, want empty", empty, err)
		}

		if _, err := s.Users().SoftDelete(ctx, u.ID); err != nil {
			t.Fatalf("SoftDelete: %v", err)
		}
		if _, err := s.Users().GetByID(ctx, u.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("soft-deleted user still visible: %v", err)
		}
		if batch, _ := s.Users().GetByIDs(ctx, []string{u.ID}); len(batch) != 0 {
			t.Errorf("soft-deleted user visible via GetByIDs: %v", batch)
		}
		users, _ = s.Users().List(ctx)
		if len(users) != 1 {
			t.Errorf("List after soft-delete = %d, want 1", len(users))
		}
		if _, err := s.Users().SoftDelete(ctx, u.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("double soft-delete: want ErrNotFound, got %v", err)
		}
	})
}

func TestRolesImplicationsAssignments(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		dev, err := s.Roles().Create(ctx, Role{Name: "dev"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		reader, _ := s.Roles().Create(ctx, Role{Name: "reader", Kind: RoleKindApplication})

		if err := s.Roles().AddImplication(ctx, dev.ID, reader.ID); err != nil {
			t.Fatalf("AddImplication: %v", err)
		}
		imps, err := s.Roles().ListImplications(ctx)
		if err != nil || len(imps) != 1 || imps[0].RoleID != dev.ID || imps[0].ImpliesRoleID != reader.ID {
			t.Fatalf("ListImplications = %v, %v", imps, err)
		}

		u, _ := s.Users().Create(ctx, User{Username: "kim"})
		from := now().Add(-time.Hour)
		to := now().Add(time.Hour)
		a, err := s.Roles().Assign(ctx, RoleAssignment{
			SubjectKind: SubjectUser, SubjectID: u.ID, RoleID: dev.ID,
			ValidFrom: &from, ValidTo: &to,
		})
		if err != nil {
			t.Fatalf("Assign: %v", err)
		}
		// Duplicate assignment of the same role to the same subject conflicts.
		if _, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: u.ID, RoleID: dev.ID}); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate assignment: want ErrConflict, got %v", err)
		}

		list, err := s.Roles().ListAssignments(ctx, SubjectUser, u.ID)
		if err != nil || len(list) != 1 {
			t.Fatalf("ListAssignments = %v, %v", list, err)
		}
		if list[0].ValidFrom == nil || !list[0].ValidFrom.Equal(from) {
			t.Errorf("valid_from round-trip: %v want %v", list[0].ValidFrom, from)
		}
		if list[0].ValidTo == nil || !list[0].ValidTo.Equal(to) {
			t.Errorf("valid_to round-trip: %v want %v", list[0].ValidTo, to)
		}

		// Open-ended assignment (nil window) round-trips as nil.
		u2, _ := s.Users().Create(ctx, User{Username: "window-open"})
		ga, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: u2.ID, RoleID: reader.ID})
		if err != nil {
			t.Fatal(err)
		}
		all, err := s.Roles().ListAllAssignments(ctx)
		if err != nil || len(all) != 2 {
			t.Fatalf("ListAllAssignments = %d, %v", len(all), err)
		}
		for _, x := range all {
			if x.ID == ga.ID && (x.ValidFrom != nil || x.ValidTo != nil) {
				t.Errorf("open-ended window should stay nil: %+v", x)
			}
		}

		if err := s.Roles().Unassign(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Roles().RemoveImplication(ctx, dev.ID, reader.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Roles().Delete(ctx, dev.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Roles().GetByName(ctx, "dev"); !errors.Is(err, ErrNotFound) {
			t.Errorf("deleted role still visible: %v", err)
		}
	})
}

// revokeEvent is the outbox event a revoke test writes for the ended ids.
func revokeEvent(ended []string) (OutboxEvent, error) {
	ce, err := json.Marshal(map[string]any{"data": map[string]any{"sessions": ended}})
	return OutboxEvent{Subject: "straza.revocation.sessions", CE: string(ce)}, err
}

// outboxCount counts the outbox rows on subject.
func outboxCount(t *testing.T, s Store, subject string) int {
	t.Helper()
	rows, err := s.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range rows {
		if e.Subject == subject {
			n++
		}
	}
	return n
}

// revocationCount counts the revocation rows of kind for target.
func revocationCount(t *testing.T, s Store, kind, target string) int {
	t.Helper()
	rows, err := s.Revocations().ListByTarget(context.Background(), kind, target)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// tableAway renames table away while fn runs, so every write to it inside
// fn fails, and then renames it back.
func tableAway(t *testing.T, s Store, table string, fn func()) {
	t.Helper()
	db := s.(*sqlStore).db
	if _, err := db.Exec(`ALTER TABLE ` + table + ` RENAME TO ` + table + `_away`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`ALTER TABLE ` + table + `_away RENAME TO ` + table); err != nil {
			t.Fatal(err)
		}
	}()
	fn()
}

// TestSessionsRevoke pins the session revoke transaction on both drivers.
// An active or closed session ends with one revocation row and one event, a
// replay and an unknown id write nothing, a mixed set of 1000 ids ends each
// active session once in the order given, and a write the database refuses
// leaves the session active with no row and no event, so a rerun ends it.
func TestSessionsRevoke(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u, err := s.Users().Create(ctx, User{Username: "kim"})
		if err != nil {
			t.Fatal(err)
		}
		newSession := func(status string) string {
			t.Helper()
			ses, err := s.Sessions().Create(ctx, Session{UserID: u.ID, HarnessName: "claude-code"})
			if err != nil {
				t.Fatal(err)
			}
			if status != SessionActive {
				if err := s.Sessions().SetStatus(ctx, ses.ID, status); err != nil {
					t.Fatal(err)
				}
			}
			return ses.ID
		}
		var calls [][]string
		event := func(ended []string) (OutboxEvent, error) {
			calls = append(calls, slices.Clone(ended))
			return revokeEvent(ended)
		}
		statusOf := func(id string) string {
			t.Helper()
			ses, err := s.Sessions().GetByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			return ses.Status
		}

		active, closed := newSession(SessionActive), newSession(SessionClosed)
		var mixed, mixedActive, mixedRevoked []string
		for i := range 300 {
			id := newSession(SessionActive)
			mixed, mixedActive = append(mixed, id, fmt.Sprintf("unknown-%d", i)), append(mixedActive, id)
		}
		for range 100 {
			id := newSession(SessionRevoked)
			mixed, mixedRevoked = append(mixed, id), append(mixedRevoked, id)
		}
		mixed = append(mixed, mixedActive[0])
		for i := len(mixed); i < 1000; i++ {
			mixed = append(mixed, fmt.Sprintf("unknown-%d", i))
		}

		steps := []struct {
			name string
			ids  []string
			want []string
		}{
			{"an active session ends", []string{active}, []string{active}},
			{"a replay ends nothing", []string{active}, nil},
			{"an unknown id ends nothing", []string{"no-such-session"}, nil},
			{"a closed session ends as revoked", []string{closed}, []string{closed}},
			{"a mixed set of 1000 ids ends each active session once, in order", mixed, mixedActive},
		}
		for _, tc := range steps {
			calls = nil
			before := outboxCount(t, s, "straza.revocation.sessions")
			got, err := s.Sessions().Revoke(ctx, tc.ids, "l4 test", event)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("%s: Revoke = %d ids (%v), want %d", tc.name, len(got), err, len(tc.want))
			}
			wantEvents := 0
			if len(tc.want) > 0 {
				wantEvents = 1
				if len(calls) != 1 || !slices.Equal(calls[0], tc.want) {
					t.Errorf("%s: the event was built %d times, want once from the ended ids", tc.name, len(calls))
				}
			} else if len(calls) != 0 {
				t.Errorf("%s: the event was built %d times with nothing ended, want never", tc.name, len(calls))
			}
			if n := outboxCount(t, s, "straza.revocation.sessions") - before; n != wantEvents {
				t.Errorf("%s: outbox rows written = %d, want %d", tc.name, n, wantEvents)
			}
		}
		for _, id := range []string{active, closed} {
			if st, n := statusOf(id), revocationCount(t, s, RevokeSession, id); st != SessionRevoked || n != 1 {
				t.Errorf("session %s: status %q with %d revocation rows, want revoked with exactly 1", id, st, n)
			}
		}
		rows, err := s.Revocations().ListByTargets(ctx, RevokeSession, append(slices.Clone(mixedActive), mixedRevoked...))
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range mixedActive {
			if rv := rows[id]; len(rv) != 1 || rv[0].Reason != "l4 test" || rv[0].Origin != RevocationOriginAdmin || statusOf(id) != SessionRevoked {
				t.Fatalf("mixed active session %s: rows %+v, want one of reason l4 test and origin admin, and the row revoked", id, rv)
			}
		}
		for _, id := range mixedRevoked {
			if len(rows[id]) != 0 {
				t.Fatalf("already revoked session %s got %d revocation rows, want none", id, len(rows[id]))
			}
		}

		failures := []struct {
			name  string
			table string
			event func([]string) (OutboxEvent, error)
		}{
			{"the event cannot be built", "", func([]string) (OutboxEvent, error) { return OutboxEvent{}, errors.New("no event") }},
			{"the revocations table is gone", "revocations", revokeEvent},
			{"the outbox table is gone", "events_outbox", revokeEvent},
		}
		for _, tc := range failures {
			id := newSession(SessionActive)
			before := outboxCount(t, s, "straza.revocation.sessions")
			var got []string
			var err error
			run := func() { got, err = s.Sessions().Revoke(ctx, []string{id}, "l4 test", tc.event) }
			if tc.table == "" {
				run()
			} else {
				tableAway(t, s, tc.table, run)
			}
			if err == nil || got != nil {
				t.Fatalf("%s: Revoke = %v (%v), want an error and no ids", tc.name, got, err)
			}
			st, n, ev := statusOf(id), revocationCount(t, s, RevokeSession, id), outboxCount(t, s, "straza.revocation.sessions")-before
			if st != SessionActive || n != 0 || ev != 0 {
				t.Errorf("%s: status %q, %d revocation rows, %d outbox rows, want active with none", tc.name, st, n, ev)
			}
			if got, err := s.Sessions().Revoke(ctx, []string{id}, "l4 test", revokeEvent); err != nil || !slices.Equal(got, []string{id}) {
				t.Errorf("%s: the rerun = %v (%v), want it to end the session", tc.name, got, err)
			}
		}
	})
}

// TestDevicesRevoke pins the device revoke transaction on both drivers. An
// enrolled device is deleted with one revocation row and one event, a gone
// device answers ErrNotFound and writes nothing, and a write the database
// refuses leaves the device enrolled with no row and no event.
func TestDevicesRevoke(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u, err := s.Users().Create(ctx, User{Username: "kim"})
		if err != nil {
			t.Fatal(err)
		}
		const subject = "straza.revocation.device"
		ev := OutboxEvent{Subject: subject, CE: `{"data":{"device":"l4"}}`}
		cases := []struct{ name, table string }{
			{"the revocations table is gone", "revocations"},
			{"the outbox table is gone", "events_outbox"},
			{"a healthy store", ""},
		}
		for i, tc := range cases {
			d, err := s.Devices().Create(ctx, Device{UserID: u.ID, Name: "box", Fingerprint: fmt.Sprintf("fp-l4-%d", i)})
			if err != nil {
				t.Fatal(err)
			}
			before := outboxCount(t, s, subject)
			run := func() { err = s.Devices().Revoke(ctx, d.ID, "l4 test", ev) }
			if tc.table == "" {
				run()
			} else {
				tableAway(t, s, tc.table, run)
			}
			_, getErr := s.Devices().GetByID(ctx, d.ID)
			rows, events := revocationCount(t, s, RevokeDevice, d.ID), outboxCount(t, s, subject)-before
			if tc.table != "" {
				if err == nil || getErr != nil || rows != 0 || events != 0 {
					t.Errorf("%s: Revoke = %v, device lookup %v, %d rows, %d events, want an error with the device enrolled and nothing written",
						tc.name, err, getErr, rows, events)
				}
				continue
			}
			if err != nil || !errors.Is(getErr, ErrNotFound) || rows != 1 || events != 1 {
				t.Fatalf("%s: Revoke = %v, device lookup %v, %d rows, %d events, want the device gone with one row and one event",
					tc.name, err, getErr, rows, events)
			}
			if err := s.Devices().Revoke(ctx, d.ID, "l4 test", ev); !errors.Is(err, ErrNotFound) {
				t.Errorf("a gone device = %v, want ErrNotFound", err)
			}
			if rows, events := revocationCount(t, s, RevokeDevice, d.ID), outboxCount(t, s, subject)-before; rows != 1 || events != 1 {
				t.Errorf("a gone device wrote: %d rows and %d events in all, want still 1 and 1", rows, events)
			}
		}
	})
}

func TestDevicesAndSessions(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u, _ := s.Users().Create(ctx, User{Username: "kim"})

		d, err := s.Devices().Create(ctx, Device{UserID: u.ID, Name: "laptop", Fingerprint: "fp1", Platform: "windows"})
		if err != nil {
			t.Fatalf("Devices.Create: %v", err)
		}
		devs, err := s.Devices().ListByUser(ctx, u.ID)
		if err != nil || len(devs) != 1 {
			t.Fatalf("ListByUser = %v, %v", devs, err)
		}
		d.Status = "disabled"
		if _, err := s.Devices().Update(ctx, d); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Devices().GetByID(ctx, d.ID)
		if got.Status != "disabled" {
			t.Errorf("device status = %q", got.Status)
		}
		if got.ClientKind != "" {
			t.Errorf("client kind of a row created without one = %q, want empty", got.ClientKind)
		}
		d.ClientKind = DeviceClientHuman
		if _, err := s.Devices().Update(ctx, d); err != nil {
			t.Fatal(err)
		}
		if got, _ = s.Devices().GetByID(ctx, d.ID); got.ClientKind != DeviceClientHuman {
			t.Errorf("client kind after the update = %q, want human", got.ClientKind)
		}
		kit, err := s.Devices().Create(ctx, Device{UserID: u.ID, Name: "agent-box", Fingerprint: "fp2", ClientKind: DeviceClientKit})
		if err != nil {
			t.Fatalf("Devices.Create with a client kind: %v", err)
		}
		if got, _ = s.Devices().GetByID(ctx, kit.ID); got.ClientKind != DeviceClientKit {
			t.Errorf("client kind = %q, want kit", got.ClientKind)
		}
		if err := s.Devices().Revoke(ctx, kit.ID, "test cleanup", OutboxEvent{Subject: "straza.revocation.device", CE: "{}"}); err != nil {
			t.Fatal(err)
		}

		ses, err := s.Sessions().Create(ctx, Session{
			UserID: u.ID, DeviceID: d.ID,
			HarnessName: "claude-code", HarnessVersion: "2.1",
			AttestationLevel: AttestationAdvisory, AttestationHashes: `{"self":"abc"}`,
		})
		if err != nil {
			t.Fatalf("Sessions.Create: %v", err)
		}
		active, err := s.Sessions().List(ctx, SessionActive)
		if err != nil || len(active) != 1 {
			t.Fatalf("List(active) = %v, %v", active, err)
		}

		later := now().Add(time.Minute)
		if err := s.Sessions().Touch(ctx, ses.ID, later); err != nil {
			t.Fatal(err)
		}
		got2, _ := s.Sessions().GetByID(ctx, ses.ID)
		if !got2.LastSeen.Equal(later) {
			t.Errorf("last_seen = %v, want %v", got2.LastSeen, later)
		}

		if err := s.Sessions().SetStatus(ctx, ses.ID, SessionRevoked); err != nil {
			t.Fatal(err)
		}
		if got3, _ := s.Sessions().GetByID(ctx, ses.ID); got3.Status != SessionRevoked {
			t.Errorf("status = %q", got3.Status)
		}
		if byUser, _ := s.Sessions().ListByUser(ctx, u.ID); len(byUser) != 1 {
			t.Errorf("ListByUser = %d", len(byUser))
		}

		// CloseIdle: sessions whose token expired long ago close in bulk;
		// live and already-revoked ones are untouched.
		stale, err := s.Sessions().Create(ctx, Session{UserID: u.ID, HarnessName: "claude-code"})
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := s.Sessions().Create(ctx, Session{UserID: u.ID, HarnessName: "strazactl"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Sessions().Touch(ctx, stale.ID, now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		closed, err := s.Sessions().CloseIdle(ctx, now().Add(-10*time.Minute))
		if err != nil || len(closed) != 1 {
			t.Fatalf("CloseIdle = %v, %v; want 1 pair", closed, err)
		}
		// The janitor emits per-session authn events from these pairs, so the
		// RETURNING projection must carry both the session and its owner.
		if closed[0].ID != stale.ID || closed[0].UserID != u.ID {
			t.Errorf("CloseIdle pair = %+v, want {%s %s}", closed[0], stale.ID, u.ID)
		}
		if got, _ := s.Sessions().GetByID(ctx, stale.ID); got.Status != SessionClosed {
			t.Errorf("stale session status = %q, want closed", got.Status)
		}
		if got, _ := s.Sessions().GetByID(ctx, fresh.ID); got.Status != SessionActive {
			t.Errorf("fresh session status = %q, want active", got.Status)
		}
		if got, _ := s.Sessions().GetByID(ctx, ses.ID); got.Status != SessionRevoked {
			t.Errorf("revoked session status = %q, want revoked (janitor must not rewrite it)", got.Status)
		}
		if again, err := s.Sessions().CloseIdle(ctx, now().Add(-10*time.Minute)); err != nil || len(again) != 0 {
			t.Errorf("second CloseIdle = %v, %v; want none", again, err)
		}

		if err := s.Devices().Revoke(ctx, d.ID, "test cleanup", OutboxEvent{Subject: "straza.revocation.device", CE: "{}"}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestSessionsCloseStartedBefore pins the absolute-lifetime close: a session
// started before the cutoff closes and comes back as an (id, user_id) pair
// even when it was seen a moment ago, a younger one stays active, and a
// revoked one keeps its status.
func TestSessionsCloseStartedBefore(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u, err := s.Users().Create(ctx, User{Username: "lia", Email: "lia@x.io"})
		if err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name       string
			startedAgo time.Duration
			status     string
			wantClosed bool
		}{
			{"old and still refreshing", 13 * time.Hour, SessionActive, true},
			{"young", time.Minute, SessionActive, false},
			{"old but revoked", 13 * time.Hour, SessionRevoked, false},
		}
		ids := map[string]string{}
		sq := s.(*sqlStore)
		for _, tc := range cases {
			ses, err := s.Sessions().Create(ctx, Session{UserID: u.ID, HarnessName: "claude-code", Status: tc.status})
			if err != nil {
				t.Fatal(err)
			}
			// Create stamps started_at with the store clock, so the age is
			// written behind the repo's back, exactly as time would.
			if _, err := sq.exec(ctx, `UPDATE sessions SET started_at = $1 WHERE id = $2`,
				sq.tArg(now().Add(-tc.startedAgo)), ses.ID); err != nil {
				t.Fatal(err)
			}
			ids[tc.name] = ses.ID
		}
		closed, err := s.Sessions().CloseStartedBefore(ctx, now().Add(-12*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(closed) != 1 || closed[0].ID != ids["old and still refreshing"] || closed[0].UserID != u.ID {
			t.Fatalf("CloseStartedBefore = %+v, want the one old active session as {%s %s}",
				closed, ids["old and still refreshing"], u.ID)
		}
		for _, tc := range cases {
			got, err := s.Sessions().GetByID(ctx, ids[tc.name])
			if err != nil {
				t.Fatal(err)
			}
			want := tc.status
			if tc.wantClosed {
				want = SessionClosed
			}
			if got.Status != want {
				t.Errorf("%s: status = %q, want %q", tc.name, got.Status, want)
			}
		}
		if again, err := s.Sessions().CloseStartedBefore(ctx, now().Add(-12*time.Hour)); err != nil || len(again) != 0 {
			t.Errorf("second CloseStartedBefore = %v, %v; want none", again, err)
		}
	})
}

func TestPacksAndBindings(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		r1, _ := s.Roles().Create(ctx, Role{Name: "dev"})
		r2, _ := s.Roles().Create(ctx, Role{Name: "ops"})

		p1, err := s.Packs().Create(ctx, KnowledgePack{Name: "golang-style", Version: "1", Content: "use gofmt", Checksum: "c1"})
		if err != nil {
			t.Fatalf("Packs.Create: %v", err)
		}
		p2, _ := s.Packs().Create(ctx, KnowledgePack{Name: "infra-runbook", Version: "1", Content: "restart with care", Checksum: "c2"})

		if err := s.Packs().Bind(ctx, r1.ID, p1.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Packs().Bind(ctx, r2.ID, p1.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Packs().Bind(ctx, r2.ID, p2.ID); err != nil {
			t.Fatal(err)
		}

		// p1 bound via both roles must appear once (DISTINCT).
		packs, err := s.Packs().ForRoles(ctx, []string{r1.ID, r2.ID})
		if err != nil || len(packs) != 2 {
			t.Fatalf("ForRoles = %d packs, %v", len(packs), err)
		}
		packs, _ = s.Packs().ForRoles(ctx, []string{r1.ID})
		if len(packs) != 1 || packs[0].Name != "golang-style" {
			t.Errorf("ForRoles(r1) = %v", packs)
		}
		if packs, _ := s.Packs().ForRoles(ctx, nil); packs != nil {
			t.Errorf("ForRoles(nil) = %v, want nil", packs)
		}

		p1.Content = "use gofmt always"
		if _, err := s.Packs().Update(ctx, p1); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Packs().GetByName(ctx, "golang-style"); got.Content != "use gofmt always" {
			t.Errorf("content = %q", got.Content)
		}

		if err := s.Packs().Unbind(ctx, r2.ID, p1.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Packs().Unbind(ctx, r2.ID, p2.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Packs().Delete(ctx, p2.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Packs().GetByID(ctx, p2.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("deleted pack still visible: %v", err)
		}
	})
}

// TestUsersSoftDeleteEndsAssignments pins the store half of the user delete:
// the soft delete and the removal of the subject's assignment rows are one
// transaction that returns the removed rows, a subject without grants gets
// an empty slice, other subjects keep their rows, and a missing or already
// deleted id answers ErrNotFound with every row intact.
func TestUsersSoftDeleteEndsAssignments(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		dev, err := s.Roles().Create(ctx, Role{Name: "dev"})
		if err != nil {
			t.Fatalf("Create role: %v", err)
		}
		ops, err := s.Roles().Create(ctx, Role{Name: "ops"})
		if err != nil {
			t.Fatalf("Create role: %v", err)
		}
		keeper, err := s.Users().Create(ctx, User{Username: "keeper"})
		if err != nil {
			t.Fatalf("Create user: %v", err)
		}
		kept, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: keeper.ID, RoleID: dev.ID})
		if err != nil {
			t.Fatalf("Assign: %v", err)
		}

		cases := []struct {
			name     string
			username string
			roles    []Role
		}{
			{"two grants", "doomed-two", []Role{dev, ops}},
			{"zero grants", "doomed-zero", nil},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				u, err := s.Users().Create(ctx, User{Username: tc.username})
				if err != nil {
					t.Fatalf("Create user: %v", err)
				}
				want := map[string]bool{}
				for _, role := range tc.roles {
					a, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: u.ID, RoleID: role.ID})
					if err != nil {
						t.Fatalf("Assign: %v", err)
					}
					want[a.ID] = true
				}

				removed, err := s.Users().SoftDelete(ctx, u.ID)
				if err != nil {
					t.Fatalf("SoftDelete: %v", err)
				}
				if removed == nil || len(removed) != len(tc.roles) {
					t.Fatalf("SoftDelete returned %v, want %d rows in a non-nil slice", removed, len(tc.roles))
				}
				for _, a := range removed {
					if !want[a.ID] || a.SubjectID != u.ID {
						t.Errorf("removed row %+v is not one of the subject's grants", a)
					}
				}
				if _, err := s.Users().GetByID(ctx, u.ID); !errors.Is(err, ErrNotFound) {
					t.Errorf("soft-deleted user still visible: %v", err)
				}
				if list, err := s.Roles().ListAssignments(ctx, SubjectUser, u.ID); err != nil || len(list) != 0 {
					t.Errorf("ListAssignments after delete = %v, %v, want none", list, err)
				}
				all, err := s.Roles().ListAllAssignments(ctx)
				if err != nil {
					t.Fatalf("ListAllAssignments: %v", err)
				}
				for _, a := range all {
					if a.SubjectID == u.ID {
						t.Errorf("deleted subject still listed: %+v", a)
					}
				}
				counts, err := s.Roles().AssignmentCountsByRole(ctx)
				if err != nil || counts[dev.ID] != 1 || counts[ops.ID] != 0 {
					t.Errorf("AssignmentCountsByRole = %v, %v, want dev 1 and ops 0", counts, err)
				}
				if _, err := s.Users().SoftDelete(ctx, u.ID); !errors.Is(err, ErrNotFound) {
					t.Errorf("double soft-delete: want ErrNotFound, got %v", err)
				}
			})
		}

		if _, err := s.Users().SoftDelete(ctx, "no-such-user"); !errors.Is(err, ErrNotFound) {
			t.Errorf("SoftDelete of a missing id: want ErrNotFound, got %v", err)
		}
		list, err := s.Roles().ListAssignments(ctx, SubjectUser, keeper.ID)
		if err != nil || len(list) != 1 || list[0].ID != kept.ID {
			t.Fatalf("the other subject's row must survive: %v, %v", list, err)
		}
	})
}
