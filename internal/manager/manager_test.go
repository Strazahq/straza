package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// eventSink captures emitted CloudEvents for assertions.
type eventSink struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	subject string
	data    map[string]any
}

func (s *eventSink) emit(_ context.Context, subject string, data map[string]any) {
	s.mu.Lock()
	s.events = append(s.events, capturedEvent{subject, data})
	s.mu.Unlock()
}

func (s *eventSink) find(subject string) (capturedEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.subject == subject {
			return e, true
		}
	}
	return capturedEvent{}, false
}

func testStore(t *testing.T) store.Store {
	t.Helper()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: t.TempDir(),
		Store:   config.Store{Driver: config.DriverSQLite},
	}
	storetest.SeedSQLite(t, cfg.SQLitePath())
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func testManager(t *testing.T) (*Manager, *eventSink) {
	t.Helper()
	sink := &eventSink{}
	mgr := New(Options{
		AllowLoopbackUpstreams: true,
		Store:                  testStore(t),
		Emit:                   sink.emit,
		HealthInterval:         time.Hour, // health checks are driven manually in tests
	})
	t.Cleanup(mgr.stopAll)
	return mgr, sink
}

// helperManifest builds a validated command-runtime manifest that runs the
// test-binary MCP helper.
func helperManifest(t *testing.T, name string, exposure []string, extraEnv ...EnvVar) Manifest {
	t.Helper()
	spec := helperSpec(t, extraEnv...)
	m := Manifest{
		APIVersion: APIVersion,
		Kind:       "App",
		Metadata:   Metadata{Name: name},
		Server:     map[string]any{"name": "straza.test/" + name, "version": "1.0.0"},
		Straza: Extensions{
			Runtime: RuntimeSpec{Kind: RuntimeCommand, Command: &spec},
		},
	}
	if exposure != nil {
		m.Straza.Exposure = &ExposureSpec{Tools: exposure}
	}
	// Round-trip through Parse so tests exercise the same validation and
	// defaulting as production manifests.
	raw, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("helper manifest invalid: %v", err)
	}
	return parsed
}

func waitStatus(t *testing.T, mgr *Manager, app, want string) AppView {
	t.Helper()
	var last AppView
	waitFor(t, 15*time.Second, "app "+app+" status "+want, func() bool {
		v, ok := mgr.View(app)
		last = v
		return ok && v.Status == want
	})
	return last
}

func toolNames(v AppView) []string {
	names := make([]string, 0, len(v.Tools))
	for _, tl := range v.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// TestManagerInstallLifecycle covers the install pipeline: install
// → running, deployed event, exposure cap applied, logs served, remove →
// stopped + removed event.
func TestManagerInstallLifecycle(t *testing.T) {
	mgr, sink := testManager(t)
	ctx := context.Background()

	mf := helperManifest(t, "helperapp", []string{"echo", "env"})
	row, err := mgr.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}

	view := waitStatus(t, mgr, "helperapp", StatusRunning)
	names := strings.Join(toolNames(view), ",")
	if !strings.Contains(names, "echo") || strings.Contains(names, "die") {
		t.Errorf("exposure cap not applied: tools = %s", names)
	}

	ev, ok := sink.find("straza.apps.deployed")
	if !ok {
		t.Fatal("no apps.deployed event")
	}
	if ev.data["name"] != "helperapp" || ev.data["app"] != row.ID {
		t.Errorf("deployed event data = %v", ev.data)
	}

	logs, ok := mgr.Logs("helperapp", 0)
	if !ok || !strings.Contains(strings.Join(logs, "\n"), "helper started") {
		t.Errorf("logs = %v", logs)
	}

	res, err := mgr.Call(ctx, "helperapp", "echo", mustJSON(t, map[string]string{"text": "hi"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if textOf(res) != "echo: hi" {
		t.Errorf("call = %q", textOf(res))
	}

	if _, err := mgr.Remove(ctx, "helperapp"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.View("helperapp"); ok {
		t.Error("removed app still visible")
	}
	if _, ok := sink.find("straza.apps.removed"); !ok {
		t.Error("no apps.removed event")
	}
	// The row is gone from every read (soft-deleted), and installing the
	// same name again revives it under its old id.
	if _, err := mgr.opts.Store.Apps().GetByID(ctx, row.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("removed row still readable: err=%v", err)
	}
	if rows, err := mgr.opts.Store.Apps().List(ctx); err != nil || len(rows) != 0 {
		t.Errorf("apps list after remove = %v err=%v, want empty", rows, err)
	}
	again, err := mgr.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatalf("reinstall after remove: %v", err)
	}
	if again.ID != row.ID {
		t.Errorf("reinstall row id = %s, want the revived %s", again.ID, row.ID)
	}
	waitStatus(t, mgr, "helperapp", StatusRunning)
}

// TestManagerDriftDetection pins that an upstream inventory change
// flips the app to degraded and emits straza.apps.drift.
func TestManagerDriftDetection(t *testing.T) {
	mgr, sink := testManager(t)
	ctx := context.Background()

	toggle := filepath.Join(t.TempDir(), "extra-tool")
	mf := helperManifest(t, "driftapp", nil, EnvVar{Name: "STRAZA_HELPER_EXTRA_TOOL_FILE", Value: toggle})
	if _, err := mgr.Install(ctx, mf, store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	view := waitStatus(t, mgr, "driftapp", StatusRunning)
	if strings.Contains(strings.Join(toolNames(view), ","), "delete_everything") {
		t.Fatal("extra tool present before toggle")
	}

	// Upstream "releases a new version": the restarted child serves an extra
	// tool.
	if err := os.WriteFile(toggle, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Call(ctx, "driftapp", "die", nil, nil); err != nil {
		t.Logf("die call: %v", err)
	}

	waitFor(t, 15*time.Second, "drift event", func() bool {
		_, ok := sink.find("straza.apps.drift")
		return ok
	})
	ev, _ := sink.find("straza.apps.drift")
	added, _ := ev.data["added"].([]string)
	if len(added) != 1 || added[0] != "delete_everything" {
		t.Errorf("drift added = %v", ev.data["added"])
	}
	v, _ := mgr.View("driftapp")
	if v.Status != StatusDegraded {
		t.Errorf("status after drift = %s (want degraded)", v.Status)
	}
}

// TestWatcherGitOps pins the apps directory watcher: a dropped
// file is proposed within 3 s, a file that does not parse is proposed with
// no name, and a removed file is handed to gone. That nothing runs before a
// publish is pinned by the server's TestFileDoorRunsNothingBeforeAPublish.
func TestWatcherGitOps(t *testing.T) {
	ctx := context.Background()

	dir := t.TempDir()
	r := &recorder{}
	w := r.watch(dir)
	w.interval = 100 * time.Millisecond
	wctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go w.Run(wctx)

	path := filepath.Join(dir, "gitopsapp.app.yaml")
	start := time.Now()
	if err := os.WriteFile(path, []byte(fileText("gitopsapp", "https://one.example/mcp")), 0o600); err != nil {
		t.Fatal(err)
	}
	// A sweep may read the file while it is being written, so the test
	// waits for the revision that names the server.
	waitFor(t, 5*time.Second, "the dropped file's proposal", func() bool {
		p, _ := r.take()
		return slices.ContainsFunc(p, func(f File) bool { return f.Name == "gitopsapp" })
	})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("drop-to-draft took %s (> 3s AC)", elapsed)
	}

	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("kind: Nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var gone []goneCall
	var broken []File
	waitFor(t, 5*time.Second, "the removed file's gone call and the broken file's proposal", func() bool {
		p, g := r.take()
		broken, gone = append(broken, p...), append(gone, g...)
		return len(gone) > 0 && slices.ContainsFunc(broken, func(f File) bool { return string(f.Raw) == "kind: Nope" })
	})
	if gone[0] != (goneCall{path, "gitopsapp"}) || broken[len(broken)-1].Name != "" {
		t.Errorf("gone %v and proposed %+v, want the removed file and the broken one with no name", gone, broken)
	}
}

func TestManagerReinstallSwapsRuntime(t *testing.T) {
	mgr, _ := testManager(t)
	ctx := context.Background()

	if _, err := mgr.Install(ctx, helperManifest(t, "swap", nil), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "swap", StatusRunning)

	// Reinstall with a narrower exposure; same app row, new runtime.
	if _, err := mgr.Install(ctx, helperManifest(t, "swap", []string{"echo"}), store.AppSourceGitops); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "swap", StatusRunning)
	waitFor(t, 5*time.Second, "narrowed exposure", func() bool {
		v, ok := mgr.View("swap")
		return ok && len(v.Tools) == 1 && v.Tools[0].Name == "echo"
	})

	apps, err := mgr.opts.Store.Apps().List(ctx)
	if err != nil || len(apps) != 1 {
		t.Fatalf("apps rows = %d err=%v (reinstall must reuse the row)", len(apps), err)
	}
	if apps[0].Source != store.AppSourceGitops {
		t.Errorf("source = %s", apps[0].Source)
	}
}

// remoteManifest builds a validated remote-runtime manifest (health tests use
// unreachable URLs to drive the degraded lane).
func remoteManifest(t *testing.T, name, url string) Manifest {
	t.Helper()
	raw := "apiVersion: straza.dev/v1beta1\n" +
		"kind: App\n" +
		"metadata: {name: " + name + "}\n" +
		"server: {name: straza.test/" + name + ", version: \"1.0.0\"}\n" +
		"straza:\n" +
		"  runtime:\n" +
		"    kind: remote\n" +
		"    remote: {url: " + url + "}\n"
	mf, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("remote manifest invalid: %v", err)
	}
	return mf
}

// TestHealthTimestamps pins the health observability contract: probes stamp
// LastProbe, settling running stamps LastHealthy, HealthCheckOne re-evaluates
// on demand, and an app that was never healthy keeps a zero LastHealthy.
func TestHealthTimestamps(t *testing.T) {
	mgr, _ := testManager(t)
	ctx := context.Background()

	if _, err := mgr.Install(ctx, helperManifest(t, "stamps", nil), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	first := waitStatus(t, mgr, "stamps", StatusRunning)
	if first.LastProbe.IsZero() || first.LastHealthy.IsZero() {
		t.Fatalf("running app missing stamps: probe=%v healthy=%v", first.LastProbe, first.LastHealthy)
	}
	if first.LastHealthy.Before(first.LastProbe) {
		t.Errorf("LastHealthy %v before LastProbe %v", first.LastHealthy, first.LastProbe)
	}

	// On-demand recheck re-probes a live supervised app and advances the stamp.
	second, ok := mgr.HealthCheckOne(ctx, "stamps")
	if !ok {
		t.Fatal("HealthCheckOne: managed app reported not managed")
	}
	if !second.LastProbe.After(first.LastProbe) {
		t.Errorf("recheck did not advance LastProbe: %v -> %v", first.LastProbe, second.LastProbe)
	}
	if second.Status != StatusRunning {
		t.Errorf("recheck status = %s", second.Status)
	}

	if _, ok := mgr.HealthCheckOne(ctx, "no-such-app"); ok {
		t.Error("HealthCheckOne invented an unmanaged app")
	}

	// A remote app that never answered: probed but never healthy.
	if _, err := mgr.Install(ctx, remoteManifest(t, "deadremote", "http://127.0.0.1:1/mcp"), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	dead := waitStatus(t, mgr, "deadremote", StatusDegraded)
	if dead.LastProbe.IsZero() {
		t.Error("degraded probe left LastProbe zero")
	}
	if !dead.LastHealthy.IsZero() {
		t.Errorf("never-healthy app has LastHealthy %v", dead.LastHealthy)
	}
	redead, ok := mgr.HealthCheckOne(ctx, "deadremote")
	if !ok || !redead.LastProbe.After(dead.LastProbe) {
		t.Errorf("recheck on dead remote: ok=%v probe %v -> %v", ok, dead.LastProbe, redead.LastProbe)
	}
	if !redead.LastHealthy.IsZero() {
		t.Errorf("dead remote gained LastHealthy %v", redead.LastHealthy)
	}
}

// TestViewOffersEveryUpstreamTool: the view reports every tool the upstream
// server lists, whatever the manifest exposes, so a picker can offer a tool
// the exposure list leaves out.
func TestViewOffersEveryUpstreamTool(t *testing.T) {
	mgr, _ := testManager(t)
	ctx := context.Background()

	if _, err := mgr.Install(ctx, helperManifest(t, "offers", []string{"echo"}), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	view := waitStatus(t, mgr, "offers", StatusRunning)
	if got := strings.Join(toolNames(view), ","); got != "echo" {
		t.Errorf("tools = %s, want the exposed echo", got)
	}
	if got := strings.Join(view.Offered, ","); got != "die,echo,env" {
		t.Errorf("offered = %s, want every tool the helper serves, sorted", got)
	}
}
