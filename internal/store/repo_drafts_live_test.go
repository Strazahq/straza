package store

import (
	"bytes"
	"context"
	"reflect"
	"sort"
	"testing"
	"time"
)

// liveNames answers the names of apps, sorted.
func liveNames(apps []App) []string {
	out := []string{}
	for _, a := range apps {
		out = append(out, a.Name)
	}
	sort.Strings(out)
	return out
}

// TestLiveStateReadsTheConfigEveryReplicaRuns pins what LiveState answers
// on both drivers: the generation, the active snapshot with its blob, the
// live servers only, the access rows of live servers with their names and
// matchers, every role name, the minted admin roles included, and every
// implication edge by name. An access row that grants nothing is left out.
func TestLiveStateReadsTheConfigEveryReplicaRuns(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		github, err := s.Apps().Create(ctx, App{Name: "github", Version: "1.0.0", RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		gone, err := s.Apps().Create(ctx, App{Name: "gone", Version: "1.0.0", RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		roles := map[string]Role{}
		for _, name := range []string{"dev", "tagged-users", "ops", "empty", "odd"} {
			ro, err := s.Roles().Create(ctx, Role{Name: name, Kind: RoleKindBusiness})
			if err != nil {
				t.Fatal(err)
			}
			roles[name] = ro
		}
		for _, edge := range [][2]string{{"dev", "tagged-users"}, {"ops", "dev"}, {"ops", "tagged-users"}} {
			if err := s.Roles().AddImplication(ctx, roles[edge[0]].ID, roles[edge[1]].ID); err != nil {
				t.Fatal(err)
			}
		}
		bind := func(role, app, matcher string) ToolBinding {
			b, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: roles[role].ID, AppID: app, ToolMatcher: matcher})
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		readers := bind("dev", github.ID, `["get_*","list_*"]`)
		everyone := bind("tagged-users", github.ID, `["*"]`)
		bind("ops", gone.ID, `["*"]`)
		bind("empty", github.ID, `[]`)
		bind("odd", github.ID, `{"tools": ["*"]}`)
		if err := s.Apps().SoftDelete(ctx, gone.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Snapshots().Create(ctx, Snapshot{ID: "snap-1", SignerKeyID: "k1", Blob: []byte("blob-1")}); err != nil {
			t.Fatal(err)
		}
		if err := s.Snapshots().SetActive(ctx, "snap-1"); err != nil {
			t.Fatal(err)
		}

		before := time.Now()
		st, err := s.Drafts().LiveState(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if st.Generation != 0 || st.Snapshot.ID != "snap-1" || !bytes.Equal(st.Snapshot.Blob, []byte("blob-1")) || st.Snapshot.SignerKeyID != "k1" {
			t.Errorf("generation %d snapshot %q blob %q signer %q, want 0, snap-1, its blob and k1",
				st.Generation, st.Snapshot.ID, st.Snapshot.Blob, st.Snapshot.SignerKeyID)
		}
		if got := liveNames(st.Apps); !reflect.DeepEqual(got, []string{"github"}) {
			t.Errorf("servers %v, want the live github only", got)
		}
		wantAccess := map[string]LiveAccess{
			readers.ID:  {ID: readers.ID, Role: "dev", App: "github", Matchers: []string{"get_*", "list_*"}},
			everyone.ID: {ID: everyone.ID, Role: "tagged-users", App: "github", Matchers: []string{"*"}},
		}
		if len(st.Access) != len(wantAccess) {
			t.Errorf("access rows %+v, want the two that grant on a live server", st.Access)
		}
		for _, la := range st.Access {
			if !reflect.DeepEqual(la, wantAccess[la.ID]) {
				t.Errorf("access row %+v, want %+v", la, wantAccess[la.ID])
			}
		}
		all, err := s.Roles().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var wantRoles []string
		for _, ro := range all {
			wantRoles = append(wantRoles, ro.Name)
			if st.RoleIDs[ro.Name] != ro.ID {
				t.Errorf("role %s has id %q in the state, want %q", ro.Name, st.RoleIDs[ro.Name], ro.ID)
			}
		}
		sort.Strings(wantRoles)
		gotRoles := append([]string(nil), st.Roles...)
		sort.Strings(gotRoles)
		if len(wantRoles) < 7 || !reflect.DeepEqual(gotRoles, wantRoles) {
			t.Errorf("roles %v, want every role row, the two minted admin roles included: %v", gotRoles, wantRoles)
		}
		wantEdges := map[string][]string{"dev": {"tagged-users"}, "ops": {"dev", "tagged-users"}}
		for role := range st.Implies {
			sort.Strings(st.Implies[role])
		}
		if !reflect.DeepEqual(st.Implies, wantEdges) {
			t.Errorf("implication edges %v, want %v", st.Implies, wantEdges)
		}
		if st.ReadAt.Before(before) || st.ReadAt.After(time.Now()) {
			t.Errorf("read at %v, want the moment of the call", st.ReadAt)
		}
	})
}

// TestLiveStateLeavesTheBlobOutWhenTheCallerRunsIt pins that the blob is
// read only when the active snapshot is not the caller's live one.
func TestLiveStateLeavesTheBlobOutWhenTheCallerRunsIt(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		st, err := s.Drafts().LiveState(ctx, "")
		if err != nil || st.Snapshot.ID != "" {
			t.Fatalf("with no snapshot active LiveState = %+v, %v; want a zero snapshot", st.Snapshot, err)
		}
		if _, err := s.Snapshots().Create(ctx, Snapshot{ID: "snap-1", SignerKeyID: "k1", Blob: []byte("blob-1")}); err != nil {
			t.Fatal(err)
		}
		if err := s.Snapshots().SetActive(ctx, "snap-1"); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			live string
			want []byte
		}{{"snap-1", nil}, {"snap-0", []byte("blob-1")}, {"", []byte("blob-1")}} {
			st, err := s.Drafts().LiveState(ctx, tc.live)
			if err != nil {
				t.Fatal(err)
			}
			if st.Snapshot.ID != "snap-1" || st.Snapshot.Size != 6 || !bytes.Equal(st.Snapshot.Blob, tc.want) {
				t.Errorf("live %q: snapshot %q size %d blob %q, want snap-1 of 6 bytes with blob %q",
					tc.live, st.Snapshot.ID, st.Snapshot.Size, st.Snapshot.Blob, tc.want)
			}
		}
	})
}

// TestLiveStateReadsOneMoment pins the repeatable read on Postgres. Another
// transaction locks the snapshots table, so LiveState waits after its first
// read. That transaction then adds a server, activates a new snapshot and
// moves the generation, and commits. LiveState must answer the state before
// that commit in every part, and the next LiveState the state after it.
// sqlite runs no transaction beside another on its one connection.
func TestLiveStateReadsOneMoment(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		sq := s.(*sqlStore)
		if sq.d != dialectPostgres {
			t.Skip("sqlite's one connection runs no transaction beside another")
		}
		ctx := context.Background()
		for _, id := range []string{"before", "after"} {
			if _, err := s.Snapshots().Create(ctx, Snapshot{ID: id, SignerKeyID: "k1", Blob: []byte(id)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Snapshots().SetActive(ctx, "before"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Apps().Create(ctx, App{Name: "old", Version: "1.0.0", RuntimeKind: "remote"}); err != nil {
			t.Fatal(err)
		}

		tx, err := sq.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `LOCK TABLE snapshots IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		type answer struct {
			st  LiveState
			err error
		}
		read := make(chan answer, 1)
		go func() {
			st, err := s.Drafts().LiveState(ctx, "")
			read <- answer{st, err}
		}()
		deadline := time.Now().Add(10 * time.Second)
		for waiting := 0; waiting == 0; {
			if time.Now().After(deadline) {
				t.Fatal("LiveState never waited on the locked snapshots table")
			}
			if err := sq.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_locks WHERE relation = 'snapshots'::regclass AND NOT granted`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		at := sq.tArg(now())
		for _, stmt := range []struct {
			q    string
			args []any
		}{
			{`INSERT INTO apps (id, name, version, manifest, runtime_kind, status, source, created_at, updated_at)
				VALUES ($1, 'new', '1.0.0', '{}', 'remote', 'pending', 'api', $2, $3)`, []any{newID(), at, at}},
			{`UPDATE snapshots SET active = (id = 'after')`, nil},
			{`UPDATE config_generation SET generation = generation + 1 WHERE id = 1`, nil},
		} {
			if _, err := tx.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		got := <-read
		if got.err != nil {
			t.Fatal(got.err)
		}
		if st := got.st; st.Generation != 0 || st.Snapshot.ID != "before" || !reflect.DeepEqual(liveNames(st.Apps), []string{"old"}) {
			t.Errorf("the read that waited answered generation %d, snapshot %q, servers %v; want 0, before and old, the state it began in",
				st.Generation, st.Snapshot.ID, liveNames(st.Apps))
		}
		st, err := s.Drafts().LiveState(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if st.Generation != 1 || st.Snapshot.ID != "after" || !reflect.DeepEqual(liveNames(st.Apps), []string{"new", "old"}) {
			t.Errorf("the next read answered generation %d, snapshot %q, servers %v; want 1, after, new and old",
				st.Generation, st.Snapshot.ID, liveNames(st.Apps))
		}
	})
}
