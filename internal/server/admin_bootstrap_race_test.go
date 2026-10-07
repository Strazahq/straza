package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/identity"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// errLostRace is what the store answers the replica whose insert reaches a
// unique index second.
var errLostRace = fmt.Errorf("%w: ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)", store.ErrConflict)

// adoptLine opens the log line of a replica that uses a peer's row.
const adoptLine = "another replica created the row at the same moment"

// lostRaceStore wraps a real store and makes named boot writes lose a race:
// the peer's work runs first on the real store, then the write answers
// errLostRace. A nil peer leaves no row behind the conflict.
type lostRaceStore struct {
	store.Store
	mu    sync.Mutex
	races map[string]func()
}

// lose reports whether the write named key loses its race, after running
// the peer's work. Each named write loses once.
func (s *lostRaceStore) lose(key string) bool {
	s.mu.Lock()
	peer, ok := s.races[key]
	delete(s.races, key)
	s.mu.Unlock()
	if ok && peer != nil {
		peer()
	}
	return ok
}

func (s *lostRaceStore) Users() store.UserRepo { return lostRaceUsers{s.Store.Users(), s} }
func (s *lostRaceStore) Roles() store.RoleRepo { return lostRaceRoles{s.Store.Roles(), s} }

type lostRaceUsers struct {
	store.UserRepo
	s *lostRaceStore
}

func (r lostRaceUsers) Create(ctx context.Context, u store.User) (store.User, error) {
	if r.s.lose("user " + u.Username) {
		return store.User{}, errLostRace
	}
	return r.UserRepo.Create(ctx, u)
}

type lostRaceRoles struct {
	store.RoleRepo
	s *lostRaceStore
}

func (r lostRaceRoles) Create(ctx context.Context, role store.Role) (store.Role, error) {
	if r.s.lose("role " + role.Name) {
		return store.Role{}, errLostRace
	}
	return r.RoleRepo.Create(ctx, role)
}

func (r lostRaceRoles) Assign(ctx context.Context, as store.RoleAssignment) (store.RoleAssignment, error) {
	if r.s.lose("assign") {
		return store.RoleAssignment{}, errLostRace
	}
	return r.RoleRepo.Assign(ctx, as)
}

// bootStore opens a migrated SQLite store, the store a first boot finds.
func bootStore(t *testing.T) store.Store {
	t.Helper()
	cfg := config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(t.TempDir(), "straza.db")}}
	storetest.SeedSQLite(t, cfg.SQLitePath())
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

// bootApp is the part of an App that the boot's ensure steps read.
func bootApp(st store.Store, log *slog.Logger) *App {
	return &App{store: st, log: log, resolver: identity.NewResolver(st)}
}

// assertBreakGlassOnce fails unless the store holds one break-glass row
// holding one assignment, of straza-admin, and answers the row.
func assertBreakGlassOnce(t *testing.T, st store.Store) store.User {
	t.Helper()
	ctx := context.Background()
	all, err := st.Users().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.User
	for _, u := range all {
		if u.Username == BreakGlassUsername {
			rows = append(rows, u)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("break-glass rows = %d, want 1", len(rows))
	}
	role, err := st.Roles().GetByName(ctx, AdminRole)
	if err != nil {
		t.Fatalf("the role %s: %v", AdminRole, err)
	}
	held, err := st.Roles().ListAssignments(ctx, store.SubjectUser, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].RoleID != role.ID {
		t.Fatalf("break-glass holds %+v, want one assignment of %s (%s)", held, AdminRole, role.ID)
	}
	return rows[0]
}

// assertAdoptLines fails unless the capture holds want adoption lines, none
// of them carrying a password.
func assertAdoptLines(t *testing.T, logs string, want int) {
	t.Helper()
	n := 0
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, adoptLine) {
			continue
		}
		n++
		if !strings.Contains(line, "level=INFO") || strings.Contains(line, "password") {
			t.Errorf("adoption line = %q, want an INFO line without a password", line)
		}
	}
	if n != want {
		t.Errorf("adoption lines = %d, want %d:\n%s", n, want, logs)
	}
}

// TestBreakGlassAdoptsPeerRow pins the break-glass boot step when another
// replica wins a write in the same instant: a row the peer wrote is read
// back and used, the password is printed only by the replica that created
// the admin, and a conflict with no row behind it fails the boot.
func TestBreakGlassAdoptsPeerRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peerEnsure := func(t *testing.T, st store.Store) {
		if err := bootApp(st, slog.New(slog.DiscardHandler)).ensureBreakGlass(ctx); err != nil {
			t.Fatalf("the peer's boot: %v", err)
		}
	}
	peerRole := func(t *testing.T, st store.Store) {
		if _, err := st.Roles().Create(ctx, store.Role{Name: AdminRole, Description: "Straza administration", Plane: store.RolePlaneControl}); err != nil {
			t.Fatalf("the peer's role: %v", err)
		}
	}
	peerAssign := func(t *testing.T, st store.Store) {
		u, err := st.Users().GetByUsername(ctx, BreakGlassUsername)
		if err != nil {
			t.Fatal(err)
		}
		role, err := st.Roles().GetByName(ctx, AdminRole)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID}); err != nil {
			t.Fatalf("the peer's assignment: %v", err)
		}
	}
	cases := []struct {
		name    string
		lose    string                             // the write that loses its race; empty loses none
		peer    func(t *testing.T, st store.Store) // the peer's work; nil leaves no row
		wantErr bool
		created int // "break-glass admin created" lines from this replica
		adopted int
	}{
		{name: "a fresh store gets the admin, the role and the assignment", created: 1},
		{name: "the peer created the admin first", lose: "user " + BreakGlassUsername, peer: peerEnsure, adopted: 1},
		{name: "a conflict with no admin behind it fails the boot", lose: "user " + BreakGlassUsername, wantErr: true},
		{name: "the peer created straza-admin first", lose: "role " + AdminRole, peer: peerRole, created: 1, adopted: 1},
		{name: "a conflict with no straza-admin behind it fails the boot", lose: "role " + AdminRole, wantErr: true},
		{name: "the peer assigned the same pair first", lose: "assign", peer: peerAssign, created: 1, adopted: 1},
		{name: "a conflict with no assignment behind it fails the boot", lose: "assign", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := bootStore(t)
			races := map[string]func(){}
			if tc.lose != "" {
				races[tc.lose] = nil
				if tc.peer != nil {
					races[tc.lose] = func() { tc.peer(t, raw) }
				}
			}
			logger, logs := captureLogger()
			err := bootApp(&lostRaceStore{Store: raw, races: races}, logger).ensureBreakGlass(ctx)
			if tc.wantErr {
				if !errors.Is(err, store.ErrConflict) {
					t.Fatalf("ensureBreakGlass = %v, want the conflict", err)
				}
				assertAdoptLines(t, logs.String(), 0)
				return
			}
			if err != nil {
				t.Fatalf("ensureBreakGlass = %v, want the boot to continue", err)
			}
			assertBreakGlassOnce(t, raw)
			if n := strings.Count(logs.String(), "break-glass admin created"); n != tc.created {
				t.Errorf("password lines = %d, want %d", n, tc.created)
			}
			assertAdoptLines(t, logs.String(), tc.adopted)
		})
	}
}

// TestProductRolesAdoptPeerRow pins the reserved-role boot step when
// another replica creates a role in the same instant: the peer's role is
// read back and used without a second announcement, and a conflict with no
// role behind it fails the boot.
func TestProductRolesAdoptPeerRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reserved := []string{EnrollMobileRole, EnrollBrowserRole, MCPAdminRole, DraftConfigRole}
	cases := []struct {
		name    string
		lose    string
		peer    bool // the peer created the role before the conflict
		wantErr bool
	}{
		{name: "a fresh store gets the four reserved roles"},
		{name: "the peer created a reserved role first", lose: MCPAdminRole, peer: true},
		{name: "a conflict with no role behind it fails the boot", lose: MCPAdminRole, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := bootStore(t)
			races := map[string]func(){}
			if tc.lose != "" {
				races["role "+tc.lose] = nil
				if tc.peer {
					races["role "+tc.lose] = func() {
						if _, err := raw.Roles().Create(ctx, store.Role{Name: tc.lose, Description: MCPAdminRoleDescription, Plane: store.RolePlaneControl}); err != nil {
							t.Fatalf("the peer's role: %v", err)
						}
					}
				}
			}
			logger, logs := captureLogger()
			app := bootApp(&lostRaceStore{Store: raw, races: races}, logger)
			err := app.ensureProductRoles(ctx)
			if tc.wantErr {
				if !errors.Is(err, store.ErrConflict) {
					t.Fatalf("ensureProductRoles = %v, want the conflict", err)
				}
				assertAdoptLines(t, logs.String(), 0)
				return
			}
			if err != nil {
				t.Fatalf("ensureProductRoles = %v, want the boot to continue", err)
			}
			announced := map[string]int{}
			for _, ev := range identityUpdatedEvents(t, app) {
				if id, ok := ev["id"].(string); ok {
					announced[id]++
				}
			}
			roles, err := raw.Roles().List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range reserved {
				var ids []string
				for _, r := range roles {
					if r.Name == name {
						ids = append(ids, r.ID)
					}
				}
				if len(ids) != 1 {
					t.Fatalf("roles named %s = %d, want 1", name, len(ids))
				}
				want := 1
				if tc.peer && name == tc.lose {
					want = 0 // the peer that created it announces it
				}
				if announced[ids[0]] != want {
					t.Errorf("%s announced %d times by this replica, want %d", name, announced[ids[0]], want)
				}
			}
			want := 0
			if tc.peer {
				want = 1
			}
			assertAdoptLines(t, logs.String(), want)
		})
	}
}

// TestBootRowsTwoReplicas starts two replicas' boot steps on one database
// at the same instant, step by step as build runs them, and pins that both
// replicas boot and the database ends with one break-glass admin holding one
// straza-admin assignment, each reserved role once, and one admin role for a
// server whose role the boot re-mints. The Postgres case runs the replicas
// on two connection pools, as two processes do.
func TestBootRowsTwoReplicas(t *testing.T) {
	t.Parallel()
	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		st := bootStore(t)
		bootTwoReplicas(t, st, st)
	})
	t.Run("postgres", func(t *testing.T) {
		t.Parallel()
		dsn := freshPostgresDSN(t)
		open := func() store.Store {
			st, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverPostgres, DSN: dsn}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		}
		a, b := open(), open()
		if err := a.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		bootTwoReplicas(t, a, b)
	})
}

// bootTwoReplicas runs each boot ensure step on both stores at once and
// checks the rows they leave.
func bootTwoReplicas(t *testing.T, stA, stB store.Store) {
	t.Helper()
	ctx := context.Background()
	jira, err := stA.Apps().Create(ctx, store.App{Name: "jira", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	if err := stA.Roles().Delete(ctx, jira.AdminRoleID); err != nil {
		t.Fatal(err)
	}
	logger, logs := captureLogger()
	replicas := []*App{bootApp(stA, logger), bootApp(stB, logger)}
	steps := []struct {
		name string
		run  func(*App, context.Context) error
	}{
		{"break-glass admin", (*App).ensureBreakGlass},
		{"product roles", (*App).ensureProductRoles},
		{"server admin roles", (*App).ensureAppAdminRoles},
	}
	for _, step := range steps {
		start := make(chan struct{})
		errs := make([]error, len(replicas))
		var wg sync.WaitGroup
		for i, a := range replicas {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = step.run(a, ctx)
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("replica %d, %s: %v", i, step.name, err)
			}
		}
	}
	assertBreakGlassOnce(t, stA)
	if n := strings.Count(logs.String(), "break-glass admin created"); n != 1 {
		t.Errorf("password lines = %d, want 1 from the replica that created the admin", n)
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, adoptLine) {
			t.Logf("adopted: %s", line[strings.Index(line, "row="):])
		}
	}
	roles, err := stA.Roles().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]int{}
	for _, r := range roles {
		named[r.Name]++
	}
	for _, name := range []string{AdminRole, EnrollMobileRole, EnrollBrowserRole, MCPAdminRole, DraftConfigRole, "mcp-admin-jira"} {
		if named[name] != 1 {
			t.Errorf("roles named %s = %d, want 1", name, named[name])
		}
	}
	if named["mcp-admin-jira-2"] != 0 {
		t.Errorf("the boot minted a second admin role for jira: %v", named)
	}
	row, err := stA.Apps().GetByID(ctx, jira.ID)
	if err != nil {
		t.Fatal(err)
	}
	if role, err := stA.Roles().GetByID(ctx, row.AdminRoleID); err != nil || role.Name != "mcp-admin-jira" {
		t.Errorf("jira's admin role = %+v, %v; want mcp-admin-jira", role, err)
	}
}
