package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/store"
)

// whoami calls the tagged server through a manager and answers the tag of the
// upstream it reached.
func whoami(ctx context.Context, m *Manager) (string, error) {
	res, err := m.Call(ctx, "tagged", "whoami", nil, nil)
	if err != nil {
		return "", err
	}
	return textOf(res), nil
}

// instanceOf answers the instance a manager runs under a name, nil for none.
func instanceOf(m *Manager, name string) *instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byName[name]
}

// converge runs the manager's part of a config apply on m, as the server's
// apply calls it: the rows read now, Stale and StopNamed as of that read,
// then StartMissing with nothing announced.
func converge(ctx context.Context, m *Manager) error {
	since := time.Now()
	rows, err := m.opts.Store.Apps().List(ctx)
	if err != nil {
		return err
	}
	stale, err := m.Stale(ctx, rows, since)
	if err != nil {
		return err
	}
	if err := m.StopNamed(ctx, stale, since); err != nil {
		return err
	}
	m.StartMissing(ctx, rows, nil)
	return nil
}

// TestConvergeFollowsAnotherReplica walks one server through its life on
// replica a. After each change, the three calls on replica b make b run
// what the store holds: the address a installed last, no instance while the
// server is paused or removed, and the pause as a recorded it. Replica b
// emits nothing, so no change comes back to a as a second one.
func TestConvergeFollowsAnotherReplica(t *testing.T) {
	a, b, bEmits := twoReplicas(t)
	ctx := context.Background()
	one, two := newTaggedUpstream(t, "one"), newTaggedUpstream(t, "two")
	install := func(up *taggedUpstream) func() error {
		return func() error {
			_, err := a.Install(ctx, remoteManifest(t, "tagged", up.URL), store.AppSourceAPI)
			return err
		}
	}
	steps := []struct {
		name       string
		change     func() error // made through replica a
		wantTag    string       // what b's instance answers, empty when b runs none
		wantPaused bool
	}{
		{name: "installed", change: install(one), wantTag: "one"},
		{name: "moved to another address", change: install(two), wantTag: "two"},
		{name: "disabled", change: func() error { _, err := a.Disable(ctx, "tagged"); return err }, wantPaused: true},
		{name: "enabled", change: func() error { _, err := a.Enable(ctx, "tagged"); return err }, wantTag: "two"},
		{name: "removed", change: func() error { _, err := a.Remove(ctx, "tagged"); return err }},
	}
	for _, step := range steps {
		if err := step.change(); err != nil {
			t.Fatalf("%s through a: %v", step.name, err)
		}
		if err := converge(ctx, b); err != nil {
			t.Fatalf("%s: converge on b: %v", step.name, err)
		}
		got, err := whoami(ctx, b)
		switch {
		case step.wantTag == "" && err == nil:
			t.Errorf("%s: b still runs the server, and it answered %q", step.name, got)
		case step.wantTag != "" && (err != nil || got != step.wantTag):
			t.Errorf("%s: b answered %q, %v, want %q", step.name, got, err, step.wantTag)
		}
		if b.IsPaused("tagged") != step.wantPaused {
			t.Errorf("%s: paused on b = %v, want %v", step.name, b.IsPaused("tagged"), step.wantPaused)
		}
	}
	bEmits.mu.Lock()
	defer bEmits.mu.Unlock()
	if len(bEmits.events) != 0 {
		t.Errorf("b emitted %v, want nothing", bEmits.events)
	}
}

// TestConvergeRunsOnlyLiveRows: a replica runs a server only while its row
// is live, not stopped, not paused and readable. A replica with no instance
// starts none for any other row, and one that runs the server stops it once
// the row turns into any of them. A server that stays live keeps its
// instance, also when the store renders the same manifest anew, as Postgres
// renders JSONB in its own key order and spacing.
func TestConvergeRunsOnlyLiveRows(t *testing.T) {
	cases := []struct {
		name string
		// turn leaves the row the way another replica would.
		turn       func(t *testing.T, st store.Store, row store.App)
		wantRun    bool
		wantPaused bool
	}{
		{name: "a live row", wantRun: true, turn: func(*testing.T, store.Store, store.App) {}},
		{name: "a live row whose manifest the store renders anew", wantRun: true, turn: func(t *testing.T, st store.Store, row store.App) {
			var doc any
			if err := json.Unmarshal([]byte(row.Manifest), &doc); err != nil {
				t.Fatal(err)
			}
			rendered, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			row.Manifest = string(rendered)
			if _, err := st.Apps().Update(context.Background(), row); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a stopped row", turn: func(t *testing.T, st store.Store, row store.App) {
			if err := st.Apps().SetStatus(context.Background(), row.ID, StatusStopped); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a paused row", wantPaused: true, turn: func(t *testing.T, st store.Store, _ store.App) {
			if err := st.Settings().Set(context.Background(), pausedSettingsKey, `["tagged"]`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a row whose manifest cannot be read", turn: func(t *testing.T, st store.Store, row store.App) {
			row.Manifest = `{"straza":`
			if _, err := st.Apps().Update(context.Background(), row); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a removed row", turn: func(t *testing.T, st store.Store, row store.App) {
			if err := st.Apps().SoftDelete(context.Background(), row.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			running := New(Options{Store: st, HealthInterval: time.Hour, AllowLoopbackUpstreams: true})
			fresh := New(Options{Store: st, HealthInterval: time.Hour, AllowLoopbackUpstreams: true})
			t.Cleanup(running.stopAll)
			t.Cleanup(fresh.stopAll)
			up := newTaggedUpstream(t, "one")
			manifest, err := remoteManifest(t, "tagged", up.URL).JSON()
			if err != nil {
				t.Fatal(err)
			}
			row, err := st.Apps().Create(ctx, store.App{Name: "tagged", Version: "1.0.0", Manifest: manifest,
				RuntimeKind: RuntimeRemote, Status: StatusRunning, Source: store.AppSourceAPI})
			if err != nil {
				t.Fatal(err)
			}
			if err := converge(ctx, running); err != nil {
				t.Fatal(err)
			}
			started := instanceOf(running, "tagged")
			if started == nil {
				t.Fatal("a live row did not start")
			}

			tc.turn(t, st, row)
			for _, m := range []*Manager{running, fresh} {
				if err := converge(ctx, m); err != nil {
					t.Fatal(err)
				}
				if got := instanceOf(m, "tagged") != nil; got != tc.wantRun {
					t.Errorf("runs the server = %v, want %v", got, tc.wantRun)
				}
				if m.IsPaused("tagged") != tc.wantPaused {
					t.Errorf("paused = %v, want %v", m.IsPaused("tagged"), tc.wantPaused)
				}
			}
			if tc.wantRun && instanceOf(running, "tagged") != started {
				t.Error("a server that stayed live was started again")
			}
		})
	}
}

// TestConvergeStartsARowOnce: the three calls run together, as two events
// from another replica can make them run, start a new row once, and a run
// with nothing to change keeps the instance that serves.
func TestConvergeStartsARowOnce(t *testing.T) {
	a, b, _ := twoReplicas(t)
	ctx := context.Background()
	up := newTaggedUpstream(t, "one")
	if _, err := a.Install(ctx, remoteManifest(t, "tagged", up.URL), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	before := up.sessions.Load()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := converge(ctx, b); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := up.sessions.Load() - before; got != 1 {
		t.Fatalf("eight reconciles together opened %d sessions for one new row, want 1", got)
	}
	inst := instanceOf(b, "tagged")
	if err := converge(ctx, b); err != nil {
		t.Fatal(err)
	}
	if instanceOf(b, "tagged") != inst || up.sessions.Load()-before != 1 {
		t.Error("a reconcile with nothing to change started the server again")
	}
}

// TestConvergeLeavesServingInstancesAlone: calls through an instance whose
// row did not change keep succeeding while reconciles run beside them. Run
// it with -race.
func TestConvergeLeavesServingInstancesAlone(t *testing.T) {
	a, b, _ := twoReplicas(t)
	ctx := context.Background()
	up := newTaggedUpstream(t, "one")
	if _, err := a.Install(ctx, remoteManifest(t, "tagged", up.URL), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	if err := converge(ctx, b); err != nil {
		t.Fatal(err)
	}
	inst, before := instanceOf(b, "tagged"), up.sessions.Load()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 20 {
			if err := converge(ctx, b); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 50 {
			if got, err := whoami(ctx, b); err != nil || got != "one" {
				t.Errorf("a call during reconciles answered %q, %v", got, err)
				return
			}
		}
	}()
	wg.Wait()
	if instanceOf(b, "tagged") != inst || up.sessions.Load() != before {
		t.Error("the reconciles replaced an instance whose row did not change")
	}
}

// TestConvergeStartsAServerWhoseSecretArrived: a command or oci server that
// replica b parked for want of its secret starts once the secret set through
// replica a is in b's broker cache and b reconciles, as SecretUpdated starts
// it on a. A server whose secret is still missing stays parked as it was.
func TestConvergeStartsAServerWhoseSecretArrived(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The oci case runs this test binary as docker, which serves the helper
	// MCP server when STRAZA_MCP_HELPER=1 is in its environment.
	old := DefaultDockerBin
	DefaultDockerBin = exe
	t.Cleanup(func() { DefaultDockerBin = old })
	cases := []struct {
		name    string
		runtime string
		arrived bool
	}{
		{name: "a command server whose secret arrived", runtime: RuntimeCommand, arrived: true},
		{name: "an oci server whose secret arrived", runtime: RuntimeOCI, arrived: true},
		{name: "a command server whose secret is still missing", runtime: RuntimeCommand},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			aSecrets, bSecrets := &fakeSecrets{byApp: map[string]*Secret{}}, &fakeSecrets{byApp: map[string]*Secret{}}
			a := New(Options{Store: st, Secrets: aSecrets, HealthInterval: time.Hour, AllowLoopbackUpstreams: true})
			b := New(Options{Store: st, Secrets: bSecrets, HealthInterval: time.Hour, AllowLoopbackUpstreams: true})
			t.Cleanup(a.stopAll)
			t.Cleanup(b.stopAll)
			row, err := a.Install(ctx, parkedManifest(t, tc.runtime), store.AppSourceAPI)
			if err != nil {
				t.Fatal(err)
			}
			if err := converge(ctx, b); err != nil {
				t.Fatal(err)
			}
			waitStatus(t, b, "parked", StatusPending)
			parked := instanceOf(b, "parked")

			secret := &Secret{ID: "cred-1", Value: "tok-arrived"}
			if tc.arrived {
				aSecrets.set(row.ID, secret)
				a.SecretUpdated(ctx, row.ID)
				bSecrets.set(row.ID, secret)
			}
			if err := converge(ctx, b); err != nil {
				t.Fatal(err)
			}
			if !tc.arrived {
				if instanceOf(b, "parked") != parked {
					t.Error("a server whose secret is still missing was started again")
				}
				return
			}
			waitStatus(t, b, "parked", StatusRunning)
			res, err := b.Call(ctx, "parked", "env", mustJSON(t, map[string]string{"name": "TOKEN"}), secret)
			if err != nil {
				t.Fatal(err)
			}
			if got := textOf(res); got != "tok-arrived" {
				t.Errorf("the server on b reads TOKEN = %q, want the secret set through a", got)
			}
		})
	}
}

// parkedManifest is the test helper MCP server, run as a command or in a
// container, with a static credential injected as the env var TOKEN, so it
// parks until its secret is set.
func parkedManifest(t *testing.T, runtime string) Manifest {
	t.Helper()
	spec := helperSpec(t)
	m := Manifest{
		APIVersion: APIVersion, Kind: "App", Metadata: Metadata{Name: "parked"},
		Server: map[string]any{"name": "straza.test/parked", "version": "1.0.0"},
		Straza: Extensions{
			Runtime:    RuntimeSpec{Kind: RuntimeCommand, Command: &spec},
			Credential: &CredentialSpec{Kind: CredentialStatic, Inject: &InjectSpec{As: InjectEnv, Name: "TOKEN"}},
		},
	}
	if runtime == RuntimeOCI {
		m.Straza.Runtime = RuntimeSpec{Kind: RuntimeOCI, OCI: &OCISpec{Image: "straza.test/fake-image", Sandbox: "none",
			Env: []EnvVar{{Name: "STRAZA_MCP_HELPER", Value: "1"}}}}
	}
	raw, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("parked manifest invalid: %v", err)
	}
	return parsed
}

// TestStaleChangesNothingWhenThePauseSetCannotBeRead: when the pause set
// cannot be read, Stale says so and names nothing, so the replica's servers
// stay as they were, and the next run that can read it stops the server
// another replica removed.
func TestStaleChangesNothingWhenThePauseSetCannotBeRead(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := New(Options{Store: st, HealthInterval: time.Hour})
	f := &failReadStore{Store: st}
	b := New(Options{Store: f, HealthInterval: time.Hour})
	t.Cleanup(a.stopAll)
	t.Cleanup(b.stopAll)
	up := newTaggedUpstream(t, "one")
	if _, err := a.Install(ctx, remoteManifest(t, "tagged", up.URL), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	if err := converge(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Remove(ctx, "tagged"); err != nil {
		t.Fatal(err)
	}

	f.failSettings = true
	rows, err := st.Apps().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names, err := b.Stale(ctx, rows, time.Now())
	if err == nil || !strings.Contains(err.Error(), "manager: load paused set") || len(names) != 0 {
		t.Fatalf("Stale = %v, %v; want no names and the pause set's read error", names, err)
	}
	if instanceOf(b, "tagged") == nil {
		t.Fatal("a run that could not read the pause set stopped a server")
	}
	f.failSettings = false
	if err := converge(ctx, b); err != nil {
		t.Fatal(err)
	}
	if instanceOf(b, "tagged") != nil {
		t.Error("the next run left the removed server running")
	}
}

// failReadStore wraps a store and fails the settings read of the pause set.
type failReadStore struct {
	store.Store
	failSettings bool
}

func (f *failReadStore) Settings() store.SettingsRepo {
	return failGetSettings{f.Store.Settings(), f.failSettings}
}

type failGetSettings struct {
	store.SettingsRepo
	fail bool
}

func (f failGetSettings) Get(ctx context.Context, key string) (string, error) {
	if f.fail {
		return "", errors.New("store unavailable")
	}
	return f.SettingsRepo.Get(ctx, key)
}

// taggedRow stores a live row for the tagged server at url, as another
// replica's install leaves it, and answers it.
func taggedRow(t *testing.T, st store.Store, url string) store.App {
	t.Helper()
	manifest, err := remoteManifest(t, "tagged", url).JSON()
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.Apps().Create(context.Background(), store.App{Name: "tagged", Version: "1.0.0", Manifest: manifest,
		RuntimeKind: RuntimeRemote, Status: StatusRunning, Source: store.AppSourceAPI})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// moveTo stores the tagged server at url, as another replica's change
// leaves the row.
func moveTo(t *testing.T, st store.Store, row store.App, url string) {
	t.Helper()
	manifest, err := remoteManifest(t, "tagged", url).JSON()
	if err != nil {
		t.Fatal(err)
	}
	row.Manifest = manifest
	if _, err := st.Apps().Update(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

// TestStaleNamesInstancesTheRowsNoLongerMatch pins Stale's judgment: it
// names the running server when its row went, stopped, paused, moved to
// another address or turned unreadable, and not while the row stays as the
// instance started from it, also when the store renders the manifest anew.
// It stops nothing, and it leaves this replica's copy of the pause set to
// StartMissing, which holds the manager's lock.
func TestStaleNamesInstancesTheRowsNoLongerMatch(t *testing.T) {
	cases := []struct {
		name      string
		turn      func(t *testing.T, st store.Store, row store.App, other string)
		wantStale bool
	}{
		{name: "a live row", turn: func(*testing.T, store.Store, store.App, string) {}},
		{name: "a live row the store renders anew", turn: func(t *testing.T, st store.Store, row store.App, _ string) {
			var doc any
			if err := json.Unmarshal([]byte(row.Manifest), &doc); err != nil {
				t.Fatal(err)
			}
			rendered, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			row.Manifest = string(rendered)
			if _, err := st.Apps().Update(context.Background(), row); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a row moved to another address", wantStale: true, turn: func(t *testing.T, st store.Store, row store.App, other string) {
			moveTo(t, st, row, other)
		}},
		{name: "a stopped row", wantStale: true, turn: func(t *testing.T, st store.Store, row store.App, _ string) {
			if err := st.Apps().SetStatus(context.Background(), row.ID, StatusStopped); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a paused row", wantStale: true, turn: func(t *testing.T, st store.Store, _ store.App, _ string) {
			if err := st.Settings().Set(context.Background(), pausedSettingsKey, `["tagged"]`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a removed row", wantStale: true, turn: func(t *testing.T, st store.Store, row store.App, _ string) {
			if err := st.Apps().SoftDelete(context.Background(), row.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a row whose manifest cannot be read", wantStale: true, turn: func(t *testing.T, st store.Store, row store.App, _ string) {
			row.Manifest = `{"straza":`
			if _, err := st.Apps().Update(context.Background(), row); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			sink := &eventSink{}
			m := New(Options{Store: st, Emit: sink.emit, HealthInterval: time.Hour})
			t.Cleanup(m.stopAll)
			one, two := newTaggedUpstream(t, "one"), newTaggedUpstream(t, "two")
			row := taggedRow(t, st, one.URL)
			if errs := m.StartMissing(ctx, []store.App{row}, nil); len(errs) != 0 {
				t.Fatal(errs)
			}
			started := instanceOf(m, "tagged")

			tc.turn(t, st, row, two.URL)
			rows, err := st.Apps().List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			names, err := m.Stale(ctx, rows, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if got := len(names) == 1 && names[0] == "tagged"; got != tc.wantStale || (!tc.wantStale && len(names) != 0) {
				t.Errorf("Stale named %v, want tagged named %v", names, tc.wantStale)
			}
			if instanceOf(m, "tagged") != started {
				t.Error("Stale stopped or replaced the instance, want it left for StopNamed")
			}
			if m.IsPaused("tagged") {
				t.Error("Stale took the stored pause set as this replica's, want it left to StartMissing")
			}
			sink.mu.Lock()
			defer sink.mu.Unlock()
			if len(sink.events) != 0 {
				t.Errorf("Stale or the quiet start emitted %v, want nothing", sink.events)
			}
		})
	}
}

// TestStaleLeavesAnInstanceMadeAfterItsRows pins the since rule: an
// install on this replica after the rows were read makes an instance the
// rows do not describe yet, and Stale leaves it for the next apply instead
// of stopping it. The same rows judged as of a later moment name it.
func TestStaleLeavesAnInstanceMadeAfterItsRows(t *testing.T) {
	ctx := context.Background()
	m, _ := testManager(t)
	one, two := newTaggedUpstream(t, "one"), newTaggedUpstream(t, "two")
	if _, err := m.Install(ctx, remoteManifest(t, "tagged", one.URL), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	since := time.Now()
	rows, err := m.opts.Store.Apps().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(ctx, remoteManifest(t, "tagged", two.URL), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}

	if names, err := m.Stale(ctx, rows, since); err != nil || len(names) != 0 {
		t.Errorf("Stale over rows read before the install = %v, %v; want the new instance left alone", names, err)
	}
	if names, err := m.Stale(ctx, rows, time.Now()); err != nil || len(names) != 1 || names[0] != "tagged" {
		t.Errorf("Stale over the same rows as of now = %v, %v; want tagged, whose instance runs another address", names, err)
	}
}

// TestStopNamedStopsWithoutWriting pins StopNamed: a named instance whose
// row still differs stops, a named instance whose row matches it keeps
// serving, a name with no instance is skipped, no row is written, no event
// is emitted, and the catalogs are told once.
func TestStopNamedStopsWithoutWriting(t *testing.T) {
	ctx := context.Background()
	m, sink := testManager(t)
	for _, name := range []string{"tagged", "other"} {
		if _, err := m.Install(ctx, remoteManifest(t, name, newTaggedUpstream(t, name).URL), store.AppSourceAPI); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, m, name, StatusRunning)
	}
	before, err := m.opts.Store.Apps().GetByName(ctx, "tagged")
	if err != nil {
		t.Fatal(err)
	}
	moveTo(t, m.opts.Store, before, newTaggedUpstream(t, "two").URL)
	if before, err = m.opts.Store.Apps().GetByName(ctx, "tagged"); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	sink.events = nil
	sink.mu.Unlock()
	changes := 0
	m.OnChange(func() { changes++ })

	if err := m.StopNamed(ctx, []string{"tagged", "other", "absent"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if instanceOf(m, "tagged") != nil || instanceOf(m, "other") == nil {
		t.Errorf("tagged runs %v, other runs %v; want tagged stopped and other serving",
			instanceOf(m, "tagged") != nil, instanceOf(m, "other") != nil)
	}
	after, err := m.opts.Store.Apps().GetByName(ctx, "tagged")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("row after StopNamed = status %s at %v, want %s at %v untouched", after.Status, after.UpdatedAt, before.Status, before.UpdatedAt)
	}
	sink.mu.Lock()
	events := len(sink.events)
	sink.mu.Unlock()
	if events != 0 || changes != 1 {
		t.Errorf("StopNamed emitted %d events and told the catalogs %d times, want none and once", events, changes)
	}
	told := changes
	if err := m.StopNamed(ctx, []string{"absent"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if changes != told {
		t.Error("a StopNamed that stopped nothing told the catalogs")
	}
}

// TestStopNamedStopsOnlyWhatIsStillStale pins the second look StopNamed
// takes under the lock. Replica a moves the server to two, and replica b's
// Stale names its instance. Before b's StopNamed runs, a local install on b
// replaces the instance, or a moves the row back to what b's instance runs,
// or a local install lands and a then changes the row again. StopNamed
// leaves each of those instances alone, so b keeps serving instead of
// serving nothing until the next change, and the apply of a later change
// judges a younger instance. A row that still differs is stopped.
func TestStopNamedStopsOnlyWhatIsStillStale(t *testing.T) {
	cases := []struct {
		name string
		// between runs after Stale named the instance and before StopNamed.
		between func(t *testing.T, a, b *Manager, one, three string)
		wantTag string
	}{
		{name: "a row that still differs", between: func(*testing.T, *Manager, *Manager, string, string) {}, wantTag: "two"},
		{name: "a local install after the read", wantTag: "three", between: func(t *testing.T, _, b *Manager, _, three string) {
			if _, err := b.Install(context.Background(), remoteManifest(t, "tagged", three), store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a row moved back to what the instance runs", wantTag: "one", between: func(t *testing.T, a, _ *Manager, one, _ string) {
			if _, err := a.Install(context.Background(), remoteManifest(t, "tagged", one), store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}
		}},
		// The row no longer matches the local install's instance either, and
		// the instance is younger than the read, so the apply that moved the
		// row judges it, not this one.
		{name: "a local install and another change after the read", wantTag: "three", between: func(t *testing.T, a, b *Manager, one, three string) {
			if _, err := b.Install(context.Background(), remoteManifest(t, "tagged", three), store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Install(context.Background(), remoteManifest(t, "tagged", one), store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b, _ := twoReplicas(t)
			ctx := context.Background()
			one, two, three := newTaggedUpstream(t, "one"), newTaggedUpstream(t, "two"), newTaggedUpstream(t, "three")
			if _, err := a.Install(ctx, remoteManifest(t, "tagged", one.URL), store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}
			if err := converge(ctx, b); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Install(ctx, remoteManifest(t, "tagged", two.URL), store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}
			since := time.Now()
			rows, err := b.opts.Store.Apps().List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			names, err := b.Stale(ctx, rows, since)
			if err != nil || len(names) != 1 {
				t.Fatalf("Stale named %v, %v; want tagged", names, err)
			}

			tc.between(t, a, b, one.URL, three.URL)
			if err := b.StopNamed(ctx, names, since); err != nil {
				t.Fatal(err)
			}
			if errs := b.StartMissing(ctx, rows, nil); len(errs) != 0 {
				t.Fatal(errs)
			}
			if tc.wantTag == "two" {
				// The row differs, so StopNamed stopped the instance, and
				// StartMissing started the row as read.
				if got, err := whoami(ctx, b); err != nil || got != "two" {
					t.Errorf("b answers %q, %v; want two", got, err)
				}
				return
			}
			if got, err := whoami(ctx, b); err != nil || got != tc.wantTag {
				t.Errorf("b answers %q, %v; want %q", got, err, tc.wantTag)
			}
		})
	}
}

// TestStaleAndStopNamedRefuseAZeroSince pins that a lost read time fails
// closed: Stale and StopNamed answer an error and judge nothing, where
// the zero time would have left every instance alone as younger than it.
func TestStaleAndStopNamedRefuseAZeroSince(t *testing.T) {
	ctx := context.Background()
	m, _ := testManager(t)
	up := newTaggedUpstream(t, "one")
	if _, err := m.Install(ctx, remoteManifest(t, "tagged", up.URL), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	row, err := m.opts.Store.Apps().GetByName(ctx, "tagged")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.opts.Store.Apps().SoftDelete(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	const want = "manager: no server was judged or stopped, because the caller gave no time for its read of the rows. " +
		"It is a defect in strazad: report it with this line"
	if names, err := m.Stale(ctx, nil, time.Time{}); err == nil || err.Error() != want || len(names) != 0 {
		t.Errorf("Stale with a zero since = %v, %v; want no names and %q", names, err, want)
	}
	if err := m.StopNamed(ctx, []string{"tagged"}, time.Time{}); err == nil || err.Error() != want {
		t.Errorf("StopNamed with a zero since = %v, want %q", err, want)
	}
	if instanceOf(m, "tagged") == nil {
		t.Error("StopNamed with a zero since stopped the server, want the refusal only")
	}
	if names, err := m.Stale(ctx, nil, time.Now()); err != nil || len(names) != 1 {
		t.Errorf("positive control: Stale as of now = %v, %v; want the removed server", names, err)
	}
}

// TestStartMissingStartsOnlyWhatTheRowStillSays pins StartMissing's second
// read: a row as the caller read it starts, and a row that went, stopped,
// paused or took another manifest after that read does not, because the
// apply that moved it starts it. A stopped row and a row with an instance
// start nothing either.
func TestStartMissingStartsOnlyWhatTheRowStillSays(t *testing.T) {
	cases := []struct {
		name string
		// turn changes the row after the caller read it, or the row the
		// caller read.
		turn       func(t *testing.T, m *Manager, row *store.App, other string)
		wantTag    string
		wantPaused bool
	}{
		{name: "a row as read", wantTag: "one", turn: func(*testing.T, *Manager, *store.App, string) {}},
		{name: "a row removed after the read", turn: func(t *testing.T, m *Manager, row *store.App, _ string) {
			if err := m.opts.Store.Apps().SoftDelete(context.Background(), row.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a row stopped after the read", turn: func(t *testing.T, m *Manager, row *store.App, _ string) {
			if err := m.opts.Store.Apps().SetStatus(context.Background(), row.ID, StatusStopped); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a row paused after the read", wantPaused: true, turn: func(t *testing.T, m *Manager, _ *store.App, _ string) {
			if err := m.opts.Store.Settings().Set(context.Background(), pausedSettingsKey, `["tagged"]`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a row moved after the read", turn: func(t *testing.T, m *Manager, row *store.App, other string) {
			moveTo(t, m.opts.Store, *row, other)
		}},
		{name: "a row read as stopped", turn: func(_ *testing.T, _ *Manager, row *store.App, _ string) {
			row.Status = StatusStopped
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, _ := testManager(t)
			one, two := newTaggedUpstream(t, "one"), newTaggedUpstream(t, "two")
			row := taggedRow(t, m.opts.Store, one.URL)
			tc.turn(t, m, &row, two.URL)

			if errs := m.StartMissing(ctx, []store.App{row}, nil); len(errs) != 0 {
				t.Fatal(errs)
			}
			got, err := whoami(ctx, m)
			switch {
			case tc.wantTag == "" && err == nil:
				t.Errorf("StartMissing started the server, and it answered %q", got)
			case tc.wantTag != "" && (err != nil || got != tc.wantTag):
				t.Errorf("the started server answered %q, %v; want %q", got, err, tc.wantTag)
			}
			if m.IsPaused("tagged") != tc.wantPaused {
				t.Errorf("paused = %v, want %v", m.IsPaused("tagged"), tc.wantPaused)
			}
			if tc.wantTag == "" {
				return
			}
			inst, before := instanceOf(m, "tagged"), one.sessions.Load()
			if errs := m.StartMissing(ctx, []store.App{row}, nil); len(errs) != 0 || instanceOf(m, "tagged") != inst || one.sessions.Load() != before {
				t.Error("a second StartMissing started a server that has an instance")
			}
		})
	}
}

// TestStartMissingReadsTheRowAgainUnderTheLock pins that the second read
// happens once StartMissing holds the manager's lock: a removal that lands
// while it waits for the lock is not started by it.
func TestStartMissingReadsTheRowAgainUnderTheLock(t *testing.T) {
	ctx := context.Background()
	m, _ := testManager(t)
	up := newTaggedUpstream(t, "one")
	row := taggedRow(t, m.opts.Store, up.URL)

	m.adminMu.Lock()
	done := make(chan map[string]error, 1)
	go func() { done <- m.StartMissing(ctx, []store.App{row}, nil) }()
	time.Sleep(50 * time.Millisecond)
	if err := m.opts.Store.Apps().SoftDelete(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	m.adminMu.Unlock()
	if errs := <-done; len(errs) != 0 {
		t.Fatal(errs)
	}
	if instanceOf(m, "tagged") != nil || up.sessions.Load() != 0 {
		t.Error("StartMissing started a server removed while it waited for the lock")
	}
}

// TestStartMissingAnnounces pins what announce changes: an announced
// server emits straza.apps.deployed once it runs and answers its start
// error, and a server not announced emits nothing and has its start error
// logged with the sentence that says what to do.
func TestStartMissingAnnounces(t *testing.T) {
	cases := []struct {
		name     string
		announce map[string]bool
		wantEmit bool
	}{
		{name: "announced", announce: map[string]bool{"tagged": true, "broken": true}, wantEmit: true},
		{name: "not announced"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			sink := &eventSink{}
			var logs bytes.Buffer
			m := New(Options{Store: st, Emit: sink.emit, Log: slog.New(slog.NewTextHandler(&logs, nil)), HealthInterval: time.Hour, AllowLoopbackUpstreams: true})
			t.Cleanup(m.stopAll)
			tagged := taggedRow(t, st, newTaggedUpstream(t, "one").URL)
			// The stored manifest names a runtime this binary does not have,
			// the one start that fails before it returns.
			broken, err := st.Apps().Create(ctx, store.App{Name: "broken", Version: "1.0.0", RuntimeKind: RuntimeCommand, Status: StatusRunning,
				Manifest: `{"apiVersion":"straza.dev/v1beta1","kind":"App","metadata":{"name":"broken"},"straza":{"runtime":{"kind":"bogus"}}}`})
			if err != nil {
				t.Fatal(err)
			}

			errs := m.StartMissing(ctx, []store.App{broken, tagged}, tc.announce)
			if _, emitted := sink.find("straza.apps.deployed"); emitted != tc.wantEmit {
				t.Errorf("straza.apps.deployed emitted = %v, want %v", emitted, tc.wantEmit)
			}
			const failed = "manager: a server changed on another replica did not start on this one. " +
				"Its health reason says why, and a restart of this replica tries again"
			if tc.announce != nil {
				if err := errs["broken"]; err == nil || !strings.Contains(err.Error(), `unknown runtime kind "bogus"`) || len(errs) != 1 {
					t.Errorf("answer %v, want the start error of broken only", errs)
				}
				if strings.Contains(logs.String(), failed) {
					t.Errorf("an announced start error was logged too:\n%s", logs.String())
				}
				return
			}
			if len(errs) != 0 {
				t.Errorf("answer %v, want none for servers not announced", errs)
			}
			if got := logs.String(); !strings.Contains(got, failed) || !strings.Contains(got, "app=broken") {
				t.Errorf("log lacks the start error of broken:\n%s", got)
			}
		})
	}
}

// TestStaleDoesNotWaitForTheManagersLock pins that Stale only judges: while
// a start holds the manager's lock across its probe, Stale answers the
// servers whose rows moved at once, because the caller holds its config
// lock and StopNamed looks again under the manager's lock before it stops.
func TestStaleDoesNotWaitForTheManagersLock(t *testing.T) {
	ctx := context.Background()
	m, _ := testManager(t)
	row := taggedRow(t, m.opts.Store, newTaggedUpstream(t, "one").URL)
	if errs := m.StartMissing(ctx, []store.App{row}, nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	moveTo(t, m.opts.Store, row, newTaggedUpstream(t, "two").URL)
	rows, err := m.opts.Store.Apps().List(ctx)
	if err != nil {
		t.Fatal(err)
	}

	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	done := make(chan []string, 1)
	go func() {
		names, err := m.Stale(ctx, rows, time.Now())
		if err != nil {
			t.Error(err)
		}
		done <- names
	}()
	select {
	case names := <-done:
		if len(names) != 1 || names[0] != "tagged" {
			t.Errorf("Stale named %v, want tagged", names)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stale waited for the manager's lock")
	}
}

// TestStartMissingTakesThePauseSetAsThisReplicas pins where the stored
// pause set becomes this replica's now that Stale only judges: StartMissing
// reads it under the manager's lock, also when it has nothing to start, so
// a pause recorded through another replica holds here for the next verb.
func TestStartMissingTakesThePauseSetAsThisReplicas(t *testing.T) {
	ctx := context.Background()
	m, _ := testManager(t)
	row := taggedRow(t, m.opts.Store, newTaggedUpstream(t, "one").URL)
	if errs := m.StartMissing(ctx, []store.App{row}, nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	if err := m.opts.Store.Settings().Set(ctx, pausedSettingsKey, `["elsewhere"]`); err != nil {
		t.Fatal(err)
	}
	if errs := m.StartMissing(ctx, []store.App{row}, nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	if !m.IsPaused("elsewhere") {
		t.Error("StartMissing left the pause recorded through another replica out of this replica's set")
	}
}

// TestSecretUpdatedStartsTheRowAsStoredNow pins where the restart after a
// secret comes from: the server's row read again under the manager's lock,
// never the parked instance's old copy. A row as the instance started from
// it starts with the secret, and a row that moved to another manifest, went,
// stopped or paused leaves the parked instance for the apply that judges it.
func TestSecretUpdatedStartsTheRowAsStoredNow(t *testing.T) {
	cases := []struct {
		name string
		// turn changes the row after the instance parked, as another
		// replica's publish or verb would.
		turn      func(t *testing.T, st store.Store, row store.App)
		wantStart bool
	}{
		{name: "a row as the instance started from it", wantStart: true, turn: func(*testing.T, store.Store, store.App) {}},
		{name: "a row moved to another manifest", turn: func(t *testing.T, st store.Store, row store.App) {
			mf := parkedManifest(t, RuntimeCommand)
			mf.Straza.Runtime.Command.Env = append(mf.Straza.Runtime.Command.Env, EnvVar{Name: "MOVED", Value: "1"})
			raw, err := mf.JSON()
			if err != nil {
				t.Fatal(err)
			}
			row.Manifest = raw
			if _, err := st.Apps().Update(context.Background(), row); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a removed row", turn: func(t *testing.T, st store.Store, row store.App) {
			if err := st.Apps().SoftDelete(context.Background(), row.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a stopped row", turn: func(t *testing.T, st store.Store, row store.App) {
			if err := st.Apps().SetStatus(context.Background(), row.ID, StatusStopped); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a paused row", turn: func(t *testing.T, st store.Store, _ store.App) {
			if err := st.Settings().Set(context.Background(), pausedSettingsKey, `["parked"]`); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			secrets := &fakeSecrets{byApp: map[string]*Secret{}}
			m := New(Options{Store: st, Secrets: secrets, HealthInterval: time.Hour})
			t.Cleanup(m.stopAll)
			row, err := m.Install(ctx, parkedManifest(t, RuntimeCommand), store.AppSourceAPI)
			if err != nil {
				t.Fatal(err)
			}
			waitStatus(t, m, "parked", StatusPending)
			parked := instanceOf(m, "parked")

			tc.turn(t, st, row)
			secrets.set(row.ID, &Secret{ID: "cred-1", Value: "tok-arrived"})
			m.SecretUpdated(ctx, row.ID)
			if tc.wantStart {
				waitStatus(t, m, "parked", StatusRunning)
				return
			}
			if inst := instanceOf(m, "parked"); inst != parked {
				t.Error("SecretUpdated replaced the parked instance, want it left for the apply that judges the row")
			}
		})
	}
}
