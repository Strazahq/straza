package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// count reports how many captured events carry the given subject.
func (s *eventSink) count(subject string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.subject == subject {
			n++
		}
	}
	return n
}

// pausedSet reads the persisted admin-pause overlay from the settings KV.
func pausedSet(t *testing.T, mgr *Manager) []string {
	t.Helper()
	raw, err := mgr.opts.Store.Settings().Get(context.Background(), pausedSettingsKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		t.Fatal(err)
	}
	return names
}

// TestDisableStopsPersistsEmits: Disable on a live app persists the pause,
// stops it via the Remove path (status=stopped + straza.apps.removed), and the
// returned view reports Paused.
func TestDisableStopsPersistsEmits(t *testing.T) {
	mgr, sink := testManager(t)
	ctx := context.Background()

	row, err := mgr.Install(ctx, helperManifest(t, "dis1", nil), store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "dis1", StatusRunning)

	view, err := mgr.Disable(ctx, "dis1")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Paused || view.Status != StatusStopped {
		t.Errorf("disable view = paused:%v status:%s (want paused stopped)", view.Paused, view.Status)
	}
	if !mgr.IsPaused("dis1") {
		t.Error("IsPaused false after disable")
	}
	if got := pausedSet(t, mgr); len(got) != 1 || got[0] != "dis1" {
		t.Errorf("persisted paused set = %v", got)
	}
	if _, ok := mgr.View("dis1"); ok {
		t.Error("disabled app still live in registry")
	}
	if _, ok := sink.find("straza.apps.removed"); !ok {
		t.Error("no straza.apps.removed event")
	}
	stored, err := mgr.opts.Store.Apps().GetByID(ctx, row.ID)
	if err != nil || stored.Status != StatusStopped {
		t.Errorf("stored status = %q err=%v", stored.Status, err)
	}
}

// TestDisableAlreadyStoppedStillPauses: disabling an app that is already
// stopped/de-adopted records the pause and succeeds idempotently (no error,
// no second removed event needed).
func TestDisableAlreadyStoppedStillPauses(t *testing.T) {
	mgr, sink := testManager(t)
	ctx := context.Background()

	if _, err := mgr.Install(ctx, helperManifest(t, "dis2", nil), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "dis2", StatusRunning)

	// A stopped app: row kept, not paused yet.
	if err := mgr.stop(ctx, "dis2"); err != nil {
		t.Fatal(err)
	}
	if mgr.IsPaused("dis2") {
		t.Fatal("app paused before Disable")
	}
	removedBefore := sink.count("straza.apps.removed")

	view, err := mgr.Disable(ctx, "dis2")
	if err != nil {
		t.Fatalf("Disable on already-stopped app: %v", err)
	}
	if !view.Paused || view.Status != StatusStopped {
		t.Errorf("view = paused:%v status:%s", view.Paused, view.Status)
	}
	if !mgr.IsPaused("dis2") {
		t.Error("IsPaused false after pausing a stopped app")
	}
	if got := sink.count("straza.apps.removed"); got != removedBefore {
		t.Errorf("removed events %d -> %d (pausing a stopped app must not re-Remove)", removedBefore, got)
	}

	// Missing app → ErrNotFound.
	if _, err := mgr.Disable(ctx, "no-such-app"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Disable(missing) err = %v (want ErrNotFound)", err)
	}
}

// TestEnableRestartsFromPersistedManifest: Enable clears the pause and brings
// the app back from its stored manifest with the same exposure, re-emitting
// deployed.
func TestEnableRestartsFromPersistedManifest(t *testing.T) {
	mgr, sink := testManager(t)
	ctx := context.Background()

	if _, err := mgr.Install(ctx, helperManifest(t, "en1", []string{"echo", "env"}), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "en1", StatusRunning)
	if _, err := mgr.Disable(ctx, "en1"); err != nil {
		t.Fatal(err)
	}
	deployedBefore := sink.count("straza.apps.deployed")

	if _, err := mgr.Enable(ctx, "en1"); err != nil {
		t.Fatal(err)
	}
	view := waitStatus(t, mgr, "en1", StatusRunning)
	if mgr.IsPaused("en1") {
		t.Error("still paused after Enable")
	}
	if got := pausedSet(t, mgr); len(got) != 0 {
		t.Errorf("paused set not cleared: %v", got)
	}
	names := strings.Join(toolNames(view), ",")
	if !strings.Contains(names, "echo") || !strings.Contains(names, "env") || strings.Contains(names, "die") {
		t.Errorf("exposure not restored from persisted manifest: %s", names)
	}
	if got := sink.count("straza.apps.deployed"); got <= deployedBefore {
		t.Errorf("Enable did not re-emit deployed (%d -> %d)", deployedBefore, got)
	}

	// Missing app → ErrNotFound.
	if _, err := mgr.Enable(ctx, "no-such-app"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Enable(missing) err = %v (want ErrNotFound)", err)
	}
}

// TestEnableIdempotentOnRunning: Enable on a live, non-paused app is a no-op
// success: no restart, no duplicate deployed event.
func TestEnableIdempotentOnRunning(t *testing.T) {
	mgr, sink := testManager(t)
	ctx := context.Background()

	if _, err := mgr.Install(ctx, helperManifest(t, "en2", nil), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "en2", StatusRunning)
	deployed := sink.count("straza.apps.deployed")

	view, err := mgr.Enable(ctx, "en2")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != StatusRunning {
		t.Errorf("idempotent Enable status = %s", view.Status)
	}
	if got := sink.count("straza.apps.deployed"); got != deployed {
		t.Errorf("idempotent Enable re-emitted deployed (%d -> %d)", deployed, got)
	}
}

// TestEnableCorruptManifestErrors: a stored manifest that no longer parses
// yields an actionable error, not a panic.
func TestEnableCorruptManifestErrors(t *testing.T) {
	mgr, _ := testManager(t)
	ctx := context.Background()

	if _, err := mgr.opts.Store.Apps().Create(ctx, store.App{
		Name:        "corrupt",
		Version:     "1.0.0",
		Manifest:    "{ this is not json",
		RuntimeKind: RuntimeCommand,
		Status:      StatusStopped,
		Source:      store.AppSourceAPI,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := mgr.Enable(ctx, "corrupt")
	if err == nil {
		t.Fatal("Enable on corrupt manifest succeeded")
	}
	if !strings.Contains(err.Error(), "reinstall") {
		t.Errorf("error not actionable: %v", err)
	}
}

// TestPauseSurvivesRestart: the pause is durable: a new Manager over the same
// store sees IsPaused after Load, and the boot loop leaves the app down.
func TestPauseSurvivesRestart(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	mgr1 := New(Options{Store: st, Emit: (&eventSink{}).emit, HealthInterval: time.Hour})
	if _, err := mgr1.Install(ctx, helperManifest(t, "durable", nil), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr1, "durable", StatusRunning)
	if _, err := mgr1.Disable(ctx, "durable"); err != nil {
		t.Fatal(err)
	}
	mgr1.stopAll()

	// Simulated restart: a brand-new Manager over the same store.
	mgr2 := New(Options{Store: st, Emit: (&eventSink{}).emit, HealthInterval: time.Hour})
	t.Cleanup(mgr2.stopAll)
	if err := mgr2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if !mgr2.IsPaused("durable") {
		t.Error("pause did not survive restart")
	}
	if _, ok := mgr2.View("durable"); ok {
		t.Error("boot resurrected a paused app")
	}
}

// failSettingsStore wraps a store and can be told to fail settings writes,
// exercising the persist-first abort in Disable.
type failSettingsStore struct {
	store.Store
	fail bool
}

func (f *failSettingsStore) Settings() store.SettingsRepo {
	return failSettings{f.Store.Settings(), &f.fail}
}

type failSettings struct {
	store.SettingsRepo
	fail *bool
}

func (f failSettings) Set(ctx context.Context, k, v string) error {
	if *f.fail {
		return errors.New("settings store unavailable")
	}
	return f.SettingsRepo.Set(ctx, k, v)
}

// TestDisableAbortsOnSettingsWriteFailure: if the pause cannot be persisted,
// Disable must not stop the app (a restart would silently resurrect it) and
// must not mutate the in-memory overlay.
func TestDisableAbortsOnSettingsWriteFailure(t *testing.T) {
	fs := &failSettingsStore{Store: testStore(t)}
	sink := &eventSink{}
	mgr := New(Options{Store: fs, Emit: sink.emit, HealthInterval: time.Hour})
	t.Cleanup(mgr.stopAll)
	ctx := context.Background()

	if _, err := mgr.Install(ctx, helperManifest(t, "wf", nil), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "wf", StatusRunning)

	fs.fail = true
	if _, err := mgr.Disable(ctx, "wf"); err == nil {
		t.Fatal("Disable succeeded despite a settings write failure")
	}
	if mgr.IsPaused("wf") {
		t.Error("overlay mutated despite a failed persist")
	}
	if v, ok := mgr.View("wf"); !ok || v.Status != StatusRunning {
		t.Errorf("app not left running: ok=%v status=%v", ok, v)
	}
	if sink.count("straza.apps.removed") != 0 {
		t.Error("removed event emitted despite an aborted disable")
	}
}

// TestInstallOverPausedAppStaysStopped: a change to a paused server stores
// the new manifest, version and source, and leaves the server stopped and
// paused; Enable then starts it with the manifest the change stored.
func TestInstallOverPausedAppStaysStopped(t *testing.T) {
	mgr, sink := testManager(t)
	ctx := context.Background()

	if _, err := mgr.Install(ctx, helperManifest(t, "held", []string{"echo"}), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "held", StatusRunning)
	if _, err := mgr.Disable(ctx, "held"); err != nil {
		t.Fatal(err)
	}
	deployed := sink.count("straza.apps.deployed")

	next := helperManifest(t, "held", []string{"echo", "env"})
	next.Server["version"] = "2.0.0"
	row, err := mgr.Install(ctx, next, store.AppSourceGitops)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusStopped || row.Version != "2.0.0" || row.Source != store.AppSourceGitops {
		t.Errorf("row = status %s version %s source %s, want stopped 2.0.0 gitops", row.Status, row.Version, row.Source)
	}
	if _, live := mgr.View("held"); live {
		t.Fatal("a change started a paused server")
	}
	if !mgr.IsPaused("held") {
		t.Error("a change cleared the pause")
	}
	stored, err := mgr.opts.Store.Apps().GetByName(ctx, "held")
	if err != nil {
		t.Fatal(err)
	}
	mf, err := FromJSON(stored.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(mf.ExposedTools(), ","); got != "echo,env" || stored.Status != StatusStopped {
		t.Errorf("stored row = exposure %s status %s, want echo,env stopped", got, stored.Status)
	}
	if got := sink.count("straza.apps.deployed"); got != deployed {
		t.Errorf("deployed events %d -> %d, want none for a paused change", deployed, got)
	}

	if _, err := mgr.Enable(ctx, "held"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "the stored exposure", func() bool {
		v, ok := mgr.View("held")
		return ok && v.Status == StatusRunning && strings.Join(toolNames(v), ",") == "echo,env"
	})
	if v, _ := mgr.View("held"); v.Version != "2.0.0" {
		t.Errorf("enabled version = %s, want 2.0.0", v.Version)
	}
}

// TestRemoveClearsPause: removing a paused server clears its pause, so a
// server installed later under the same name starts and reads not paused.
func TestRemoveClearsPause(t *testing.T) {
	mgr, _ := testManager(t)
	ctx := context.Background()

	if _, err := mgr.Install(ctx, helperManifest(t, "gone", nil), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "gone", StatusRunning)
	if _, err := mgr.Disable(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Remove(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if mgr.IsPaused("gone") {
		t.Error("the pause outlived the removal")
	}
	if got := pausedSet(t, mgr); len(got) != 0 {
		t.Errorf("persisted paused set = %v, want empty", got)
	}
	if _, err := mgr.Install(ctx, helperManifest(t, "gone", nil), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "gone", StatusRunning)
	if mgr.IsPaused("gone") {
		t.Error("the new server reads paused")
	}
}

// TestInstallClearsStalePause: a pause left behind under a name, by a
// removal that predates the clearing, does not hold the next server of that
// name, whether the install creates a row or revives the removed one.
func TestInstallClearsStalePause(t *testing.T) {
	cases := []struct {
		name   string
		revive bool
	}{
		{name: "a new name", revive: false},
		{name: "a revived name", revive: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _ := testManager(t)
			ctx := context.Background()
			firstID := ""
			if tc.revive {
				row, err := mgr.Install(ctx, helperManifest(t, "stale", nil), store.AppSourceAPI)
				if err != nil {
					t.Fatal(err)
				}
				firstID = row.ID
				waitStatus(t, mgr, "stale", StatusRunning)
				if _, err := mgr.Remove(ctx, "stale"); err != nil {
					t.Fatal(err)
				}
			}
			mgr.adminMu.Lock()
			err := mgr.setPausedLocked(ctx, "stale", true)
			mgr.adminMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}

			row, err := mgr.Install(ctx, helperManifest(t, "stale", nil), store.AppSourceAPI)
			if err != nil {
				t.Fatal(err)
			}
			waitStatus(t, mgr, "stale", StatusRunning)
			if mgr.IsPaused("stale") {
				t.Error("the stale pause holds the new server")
			}
			if got := pausedSet(t, mgr); len(got) != 0 {
				t.Errorf("persisted paused set = %v, want empty", got)
			}
			if tc.revive && row.ID != firstID {
				t.Errorf("row id = %s, want the revived %s", row.ID, firstID)
			}
		})
	}
}

// failGetStore wraps a store and fails the row read of the named apps with
// an error other than not found.
type failGetStore struct {
	store.Store
	fail map[string]bool
}

func (f *failGetStore) Apps() store.AppRepo { return failGetApps{f.Store.Apps(), f.fail} }

type failGetApps struct {
	store.AppRepo
	fail map[string]bool
}

func (f failGetApps) GetByName(ctx context.Context, name string) (store.App, error) {
	if f.fail[name] {
		return store.App{}, errors.New("store unavailable")
	}
	return f.AppRepo.GetByName(ctx, name)
}

// TestLoadDropsStalePauses: a pause belongs to a live server, so the load
// drops a paused name with no live row, persists the trimmed set and logs
// one line per dropped name; a paused live row keeps its pause, and a name
// the store cannot answer for keeps its pause.
func TestLoadDropsStalePauses(t *testing.T) {
	cases := []struct {
		name          string
		rows          []string // names with a live, stopped row
		failGet       []string // names whose row read fails
		persisted     []string
		wantPaused    []string
		wantPersisted []string
		wantDropped   []string
	}{
		{name: "a stale name is dropped and persisted", persisted: []string{"gone"},
			wantPaused: nil, wantPersisted: []string{}, wantDropped: []string{"gone"}},
		{name: "a live paused name stays", rows: []string{"held"}, persisted: []string{"held"},
			wantPaused: []string{"held"}, wantPersisted: []string{"held"}},
		{name: "a store error keeps the name", failGet: []string{"flaky"}, persisted: []string{"flaky"},
			wantPaused: []string{"flaky"}, wantPersisted: []string{"flaky"}},
		{name: "a mixed set keeps the live name", rows: []string{"held"}, persisted: []string{"gone", "held"},
			wantPaused: []string{"held"}, wantPersisted: []string{"held"}, wantDropped: []string{"gone"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := &failGetStore{Store: testStore(t), fail: map[string]bool{}}
			for _, n := range tc.failGet {
				st.fail[n] = true
			}
			for _, n := range tc.rows {
				if _, err := st.Apps().Create(ctx, store.App{Name: n, Version: "1.0.0", Manifest: "{}",
					RuntimeKind: RuntimeCommand, Status: StatusStopped, Source: store.AppSourceAPI}); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Settings().Set(ctx, pausedSettingsKey, string(mustJSON(t, tc.persisted))); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			mgr := New(Options{Store: st, Log: slog.New(slog.NewTextHandler(&logs, nil)), HealthInterval: time.Hour})
			t.Cleanup(mgr.stopAll)

			if err := mgr.Load(ctx); err != nil {
				t.Fatal(err)
			}
			for _, n := range tc.persisted {
				if got, want := mgr.IsPaused(n), slices.Contains(tc.wantPaused, n); got != want {
					t.Errorf("IsPaused(%s) = %v, want %v", n, got, want)
				}
			}
			if got := pausedSet(t, mgr); strings.Join(got, ",") != strings.Join(tc.wantPersisted, ",") {
				t.Errorf("persisted paused set = %v, want %v", got, tc.wantPersisted)
			}
			for _, n := range tc.wantDropped {
				if line := "manager: dropped the pause of " + n + ", since no server with that name is installed"; !strings.Contains(logs.String(), line) {
					t.Errorf("log lacks %q:\n%s", line, logs.String())
				}
			}
			if got := strings.Count(logs.String(), "dropped the pause"); got != len(tc.wantDropped) {
				t.Errorf("drop lines = %d, want %d:\n%s", got, len(tc.wantDropped), logs.String())
			}
		})
	}
}

// TestAdminVerbsSerializeWithInstall: an install that races Disable or
// Enable never loses its manifest. Whichever runs first, the stored
// manifest is the installed one and the pause ends as the verb left it.
// Run it with -race.
func TestAdminVerbsSerializeWithInstall(t *testing.T) {
	cases := []struct {
		name string
		// pausedFirst pauses the server before the race.
		pausedFirst bool
		verb        func(m *Manager, ctx context.Context, name string) (AppView, error)
		wantPaused  bool
	}{
		{name: "disable", verb: (*Manager).Disable, wantPaused: true},
		{name: "enable", pausedFirst: true, verb: (*Manager).Enable, wantPaused: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _ := testManager(t)
			ctx := context.Background()
			for i := range 25 {
				name := "race-" + strconv.Itoa(i)
				if _, err := mgr.Install(ctx, remoteManifest(t, name, "http://127.0.0.1:1/mcp"), store.AppSourceAPI); err != nil {
					t.Fatal(err)
				}
				if tc.pausedFirst {
					if _, err := mgr.Disable(ctx, name); err != nil {
						t.Fatal(err)
					}
				}
				next := remoteManifest(t, name, "http://127.0.0.1:2/mcp")
				next.Server["version"] = "2.0.0"

				var wg sync.WaitGroup
				errs := make([]error, 2)
				wg.Add(2)
				go func() { defer wg.Done(); _, errs[0] = tc.verb(mgr, ctx, name) }()
				go func() { defer wg.Done(); _, errs[1] = mgr.Install(ctx, next, store.AppSourceAPI) }()
				wg.Wait()
				if errs[0] != nil || errs[1] != nil {
					t.Fatalf("%s: verb err %v, install err %v", name, errs[0], errs[1])
				}

				row, err := mgr.opts.Store.Apps().GetByName(ctx, name)
				if err != nil {
					t.Fatal(err)
				}
				if row.Version != "2.0.0" {
					t.Errorf("%s: stored version %s, want the installed 2.0.0", name, row.Version)
				}
				if mgr.IsPaused(name) != tc.wantPaused {
					t.Errorf("%s: paused %v, want %v", name, mgr.IsPaused(name), tc.wantPaused)
				}
				v, live := mgr.View(name)
				if live == tc.wantPaused || (live && v.Version != "2.0.0") {
					t.Errorf("%s: live %v version %q, want live %v running 2.0.0", name, live, v.Version, !tc.wantPaused)
				}
			}
		})
	}
}

// TestRemoveKeepsGoingWhenThePauseCannotBeCleared: a removal whose pause
// cannot be cleared still removes the app and says in the log what clears
// the pause.
func TestRemoveKeepsGoingWhenThePauseCannotBeCleared(t *testing.T) {
	fs := &failSettingsStore{Store: testStore(t)}
	var logs bytes.Buffer
	mgr := New(Options{Store: fs, Emit: (&eventSink{}).emit, Log: slog.New(slog.NewTextHandler(&logs, nil)), HealthInterval: time.Hour})
	t.Cleanup(mgr.stopAll)
	ctx := context.Background()

	if _, err := mgr.Install(ctx, remoteManifest(t, "stuck", "http://127.0.0.1:1/mcp"), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Disable(ctx, "stuck"); err != nil {
		t.Fatal(err)
	}
	fs.fail = true
	if _, err := mgr.Remove(ctx, "stuck"); err != nil {
		t.Fatalf("Remove = %v, want the removal to go through", err)
	}
	if _, err := mgr.opts.Store.Apps().GetByName(ctx, "stuck"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("row after remove err = %v, want not found", err)
	}
	want := "manager: app removed, but its pause could not be cleared. The next install of the name or the next start of strazad clears it"
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log lacks %q:\n%s", want, logs.String())
	}
}

// publishAfterRead wraps a store so that the next read of a row by name is
// followed by write, as a publish that lands between a verb's read and its
// own write would be.
type publishAfterRead struct {
	store.Store
	write func()
}

func (p *publishAfterRead) Apps() store.AppRepo { return publishAfterReadApps{p.Store.Apps(), p} }

type publishAfterReadApps struct {
	store.AppRepo
	p *publishAfterRead
}

func (a publishAfterReadApps) GetByName(ctx context.Context, name string) (store.App, error) {
	row, err := a.AppRepo.GetByName(ctx, name)
	if write := a.p.write; write != nil {
		a.p.write = nil
		write()
	}
	return row, err
}

// TestEnableWritesOnlyTheStatus pins that Enable writes the row's status
// and nothing else: a manifest and version that a publish stored after
// Enable read the row stay in the row, where writing the row back would
// put the old ones there again.
func TestEnableWritesOnlyTheStatus(t *testing.T) {
	ctx := context.Background()
	st := &publishAfterRead{Store: testStore(t)}
	mgr := New(Options{Store: st, Emit: (&eventSink{}).emit, HealthInterval: time.Hour})
	t.Cleanup(mgr.stopAll)
	one, two := newTaggedUpstream(t, "one"), newTaggedUpstream(t, "two")
	if _, err := mgr.Install(ctx, remoteManifest(t, "tagged", one.URL), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Disable(ctx, "tagged"); err != nil {
		t.Fatal(err)
	}
	published := remoteManifest(t, "tagged", two.URL)
	published.Server["version"] = "2.0.0"
	st.write = func() {
		row, err := st.Store.Apps().GetByName(ctx, "tagged")
		if err != nil {
			t.Fatal(err)
		}
		if row.Manifest, err = published.JSON(); err != nil {
			t.Fatal(err)
		}
		row.Version = "2.0.0"
		if _, err := st.Store.Apps().Update(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := mgr.Enable(ctx, "tagged"); err != nil {
		t.Fatal(err)
	}
	if st.write != nil {
		t.Fatal("Enable read no row by name, so the test published nothing in between")
	}
	row, err := st.Store.Apps().GetByName(ctx, "tagged")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := FromJSON(row.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Straza.Runtime.Remote.URL; got != two.URL || row.Version != "2.0.0" {
		t.Errorf("stored address %s version %s, want the published %s version 2.0.0 kept", got, row.Version, two.URL)
	}
	if row.Status == StatusStopped {
		t.Errorf("stored status %s, want the status Enable wrote", row.Status)
	}
}
