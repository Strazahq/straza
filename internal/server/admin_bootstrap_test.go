package server

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestDraftConfigRoleAtBoot pins that a fresh boot holds the
// drafting role on the control plane with its description, every product
// role it created is announced once by straza.identity.updated with its
// id, and a later run creates and announces nothing.
func TestDraftConfigRoleAtBoot(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	role, err := app.store.Roles().GetByName(ctx, DraftConfigRole)
	if err != nil {
		t.Fatalf("the drafting role after boot: %v", err)
	}
	if role.Plane != store.RolePlaneControl || role.Description != DraftConfigRoleDescription {
		t.Errorf("the drafting role = plane %q, description %q", role.Plane, role.Description)
	}
	announced := func() map[string]int {
		n := map[string]int{}
		for _, ev := range identityUpdatedEvents(t, app) {
			if id, ok := ev["id"].(string); ok && len(ev) == 1 {
				n[id]++
			}
		}
		return n
	}
	before := announced()
	for _, name := range []string{EnrollMobileRole, EnrollBrowserRole, MCPAdminRole, DraftConfigRole} {
		r, err := app.store.Roles().GetByName(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if before[r.ID] != 1 {
			t.Errorf("the product role %s was announced %d times at boot, want once", name, before[r.ID])
		}
	}
	if err := app.ensureProductRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if after := announced(); !maps.Equal(after, before) {
		t.Errorf("a second run announced roles again: %v, before %v", after, before)
	}
}

// repairLine opens the Info line of a boot that assigns straza-admin to a
// break-glass row it found without the role in force.
const repairLine = "the break-glass admin held no straza-admin assignment"

// repairStores opens n handles on one migrated store of the dialect: SQLite
// shares one handle, as one process does, and Postgres opens a pool per
// replica on a fresh database, as separate processes do.
func repairStores(t *testing.T, postgres bool, n int) []store.Store {
	t.Helper()
	stores := make([]store.Store, n)
	if !postgres {
		st := bootStore(t)
		for i := range stores {
			stores[i] = st
		}
		return stores
	}
	dsn := freshPostgresDSN(t)
	for i := range stores {
		st, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverPostgres, DSN: dsn}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		stores[i] = st
	}
	if err := stores[0].Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return stores
}

// repairLines counts the repair lines in a capture and fails on one that is
// not at Info or that carries a password attribute or the stored hash.
func repairLines(t *testing.T, logs, hash string) int {
	t.Helper()
	n := 0
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, repairLine) {
			continue
		}
		n++
		if !strings.Contains(line, "level=INFO") || strings.Contains(line, "password=") || strings.Contains(line, hash) {
			t.Errorf("repair line = %q, want an INFO line that carries no password", line)
		}
	}
	return n
}

// announced counts the straza.identity.updated events that name id.
func announced(t *testing.T, st store.Store, id string) int {
	t.Helper()
	n := 0
	for _, ev := range identityUpdatedEvents(t, bootApp(st, slog.New(slog.DiscardHandler))) {
		if ev["id"] == id {
			n++
		}
	}
	return n
}

// breakGlassHash is the stored hash of the break-glass rows the repair
// tests seed; no boot may print it.
const breakGlassHash = "stored-hash-of-nobody"

// TestBreakGlassRepairsMissingAdminRole pins the boot step on a break-glass
// row found without a straza-admin assignment in force, on SQLite and on
// Postgres. The boot assigns the role, creating it when it is missing and
// replacing an assignment whose window ended or has not begun, logs one Info
// line without a password and announces the user once. A row that holds the
// role in force boots with no change, no line and no event. When another
// replica writes the role or the assignment first, the boot uses that row,
// and a conflict with no row behind it fails the boot. The fresh store is
// TestBreakGlassAdoptsPeerRow's first row.
func TestBreakGlassRepairsMissingAdminRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	hour := time.Hour
	cases := []struct {
		name                      string
		role                      bool          // straza-admin exists before the boot
		held                      bool          // break-glass holds it before the boot
		from                      time.Duration // the held assignment's window from now; zero is no bound
		to                        time.Duration
		lose                      string // the boot write that loses its race to a peer
		peer                      bool   // the peer wrote the row before the conflict
		wantErr                   bool
		repaired, adopted, events int
	}{
		{name: "a row without the role or its assignment gets both", repaired: 1, events: 1},
		{name: "a row without its assignment gets straza-admin", role: true, repaired: 1, events: 1},
		{name: "a row that holds straza-admin in force is left as it is", role: true, held: true},
		{name: "an assignment whose window ended is replaced", role: true, held: true, to: -hour, repaired: 1, events: 1},
		{name: "an assignment that starts tomorrow is replaced", role: true, held: true, from: 24 * hour, repaired: 1, events: 1},
		{name: "a peer that assigns the pair first is used", role: true, lose: "assign", peer: true, adopted: 1},
		{name: "a peer that creates straza-admin first is used", lose: "role " + AdminRole, peer: true, repaired: 1, adopted: 1, events: 1},
		{name: "a conflict with no assignment behind it fails the boot", role: true, lose: "assign", wantErr: true},
	}
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					raw := repairStores(t, dialect == "postgres", 1)[0]
					bg, err := raw.Users().Create(ctx, store.User{Username: BreakGlassUsername, Display: "Break-glass admin", PasswordHash: breakGlassHash})
					if err != nil {
						t.Fatal(err)
					}
					var held store.RoleAssignment
					if tc.role {
						role, err := raw.Roles().Create(ctx, store.Role{Name: AdminRole, Description: "Straza administration", Plane: store.RolePlaneControl})
						if err != nil {
							t.Fatal(err)
						}
						if tc.held {
							as := store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: bg.ID, RoleID: role.ID}
							from, to := time.Now().Add(tc.from).UTC(), time.Now().Add(tc.to).UTC()
							if tc.from != 0 {
								as.ValidFrom = &from
							}
							if tc.to != 0 {
								as.ValidTo = &to
							}
							if held, err = raw.Roles().Assign(ctx, as); err != nil {
								t.Fatal(err)
							}
						}
					}
					races := map[string]func(){}
					if tc.lose != "" {
						races[tc.lose] = nil
						if tc.peer {
							races[tc.lose] = func() { peerWrite(t, raw, tc.lose, bg.ID) }
						}
					}
					logger, logs := captureLogger()
					err = bootApp(&lostRaceStore{Store: raw, races: races}, logger).ensureBreakGlass(ctx)
					if tc.wantErr {
						if !errors.Is(err, store.ErrConflict) {
							t.Fatalf("ensureBreakGlass = %v, want the conflict", err)
						}
						if n := repairLines(t, logs.String(), breakGlassHash); n != 0 {
							t.Errorf("a failed boot logged %d repair lines", n)
						}
						return
					}
					if err != nil {
						t.Fatalf("ensureBreakGlass = %v, want the boot to continue", err)
					}
					if got := assertBreakGlassOnce(t, raw); got.ID != bg.ID {
						t.Errorf("the boot replaced the break-glass row %s with %s", bg.ID, got.ID)
					}
					now, err := raw.Roles().ListAssignments(ctx, store.SubjectUser, bg.ID)
					if err != nil {
						t.Fatal(err)
					}
					if now[0].ValidFrom != nil || now[0].ValidTo != nil {
						t.Errorf("the assignment after the boot has a window %v to %v, want none", now[0].ValidFrom, now[0].ValidTo)
					}
					if kept := now[0].ID == held.ID; tc.held && kept != (tc.repaired == 0) {
						t.Errorf("the held assignment %s kept = %v, want kept only when it was in force", held.ID, kept)
					}
					if n := repairLines(t, logs.String(), breakGlassHash); n != tc.repaired {
						t.Errorf("repair lines = %d, want %d:\n%s", n, tc.repaired, logs.String())
					}
					if n := strings.Count(logs.String(), adoptLine); n != tc.adopted {
						t.Errorf("adoption lines = %d, want %d:\n%s", n, tc.adopted, logs.String())
					}
					if n := announced(t, raw, bg.ID); n != tc.events {
						t.Errorf("straza.identity.updated events naming break-glass = %d, want %d", n, tc.events)
					}
					if strings.Contains(logs.String(), "break-glass admin created") {
						t.Errorf("a boot on an existing row printed a new password:\n%s", logs.String())
					}
				})
			}
		})
	}
}

// peerWrite is another replica's write that wins the race named key: the
// straza-admin role, or its assignment to the break-glass row bgID.
func peerWrite(t *testing.T, st store.Store, key, bgID string) {
	ctx := context.Background()
	if key != "assign" {
		if _, err := st.Roles().Create(ctx, store.Role{Name: AdminRole, Description: "Straza administration", Plane: store.RolePlaneControl}); err != nil {
			t.Errorf("the peer's role: %v", err)
		}
		return
	}
	role, err := st.Roles().GetByName(ctx, AdminRole)
	if err == nil {
		_, err = st.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: bgID, RoleID: role.ID})
	}
	if err != nil {
		t.Errorf("the peer's assignment: %v", err)
	}
}

// TestBreakGlassRepairTwoReplicas starts two replicas' repair of one
// break-glass row at the same instant, round after round, on warm handles,
// until a round raced: one replica's write of the role or the assignment
// lost to the other's. Every round must boot both replicas and end with one
// assignment, one repair line and one announcement. A dialect on which no
// round of twenty raced fails, so the test cannot pass without the race.
func TestBreakGlassRepairTwoReplicas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			stores := repairStores(t, dialect == "postgres", 2)
			bg, err := stores[0].Users().Create(ctx, store.User{Username: BreakGlassUsername, Display: "Break-glass admin", PasswordHash: breakGlassHash})
			if err != nil {
				t.Fatal(err)
			}
			const rounds = 20
			for round := 1; round <= rounds; round++ {
				held, err := stores[0].Roles().ListAssignments(ctx, store.SubjectUser, bg.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, as := range held {
					if err := stores[0].Roles().Unassign(ctx, as.ID); err != nil {
						t.Fatal(err)
					}
				}
				for _, st := range stores {
					if _, err := st.Users().GetByUsername(ctx, BreakGlassUsername); err != nil {
						t.Fatal(err) // warms each handle, so neither replica starts by dialling
					}
				}
				before := announced(t, stores[0], bg.ID)
				logger, logs := captureLogger()
				start := make(chan struct{})
				errs := make([]error, len(stores))
				var wg sync.WaitGroup
				for i, st := range stores {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						errs[i] = bootApp(st, logger).ensureBreakGlass(ctx)
					}()
				}
				close(start)
				wg.Wait()
				for i, err := range errs {
					if err != nil {
						t.Fatalf("round %d, replica %d: ensureBreakGlass = %v, want the boot to continue", round, i, err)
					}
				}
				assertBreakGlassOnce(t, stores[0])
				if n := repairLines(t, logs.String(), breakGlassHash); n != 1 {
					t.Errorf("round %d: repair lines = %d, want 1:\n%s", round, n, logs.String())
				}
				if n := announced(t, stores[0], bg.ID) - before; n != 1 {
					t.Errorf("round %d: %d announcements of break-glass, want 1", round, n)
				}
				if strings.Contains(logs.String(), adoptLine) {
					t.Logf("round %d raced: %s", round, logs.String()[strings.Index(logs.String(), "row="):])
					return
				}
			}
			t.Fatalf("no round of %d raced on %s, so the test proved nothing about two replicas", rounds, dialect)
		})
	}
}
