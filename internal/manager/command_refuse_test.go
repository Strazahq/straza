package manager

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/store"
)

// TestCheckRuntime: a manager that refuses command servers refuses a command
// manifest with the sentence that names the fix and passes every other
// runtime, and a manager that does not refuse them passes all of them.
func TestCheckRuntime(t *testing.T) {
	of := func(name, kind string) Manifest {
		return Manifest{Metadata: Metadata{Name: name}, Straza: Extensions{Runtime: RuntimeSpec{Kind: kind}}}
	}
	const refused = "files runs as a command, which this server refuses under the enterprise profile, because the process would run inside Straza with access to its keys and database. " +
		"Run the server as its own service or pod and add it as a remote server over HTTP."
	cases := []struct {
		name    string
		refuse  bool
		mf      Manifest
		wantErr string
	}{
		{name: "enterprise refuses a command server", refuse: true, mf: of("files", RuntimeCommand), wantErr: refused},
		{name: "enterprise passes a remote server", refuse: true, mf: of("jira", RuntimeRemote)},
		{name: "enterprise passes an oci server", refuse: true, mf: of("box", RuntimeOCI)},
		{name: "standalone passes a command server", refuse: false, mf: of("files", RuntimeCommand)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := New(Options{Store: testStore(t), RefuseCommand: tc.refuse}).CheckRuntime(tc.mf)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Errorf("err = %v\nwant %s", err, tc.wantErr)
			}
		})
	}
}

// TestCommandRefusedNeverStarts: on a manager that refuses command servers,
// every start path registers a command server degraded with the sentence
// that names the fix and never spawns its process, including a server a
// standalone manager stored before the profile changed.
func TestCommandRefusedNeverStarts(t *testing.T) {
	refusing := func(t *testing.T, st store.Store) *Manager {
		mgr := New(Options{Store: st, Emit: (&eventSink{}).emit, HealthInterval: time.Hour, RefuseCommand: true})
		t.Cleanup(mgr.stopAll)
		return mgr
	}
	install := func(t *testing.T, ctx context.Context, mgr *Manager) {
		if _, err := mgr.Install(ctx, helperManifest(t, "files", nil), store.AppSourceAPI); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		start func(t *testing.T, ctx context.Context, st store.Store) *Manager
	}{
		{name: "install", start: func(t *testing.T, ctx context.Context, st store.Store) *Manager {
			mgr := refusing(t, st)
			install(t, ctx, mgr)
			return mgr
		}},
		{name: "boot over a server stored before the profile changed", start: func(t *testing.T, ctx context.Context, st store.Store) *Manager {
			before := New(Options{Store: st, Emit: (&eventSink{}).emit, HealthInterval: time.Hour})
			install(t, ctx, before)
			waitStatus(t, before, "files", StatusRunning)
			before.stopAll()
			mgr := refusing(t, st)
			if err := mgr.Load(ctx); err != nil {
				t.Fatal(err)
			}
			return mgr
		}},
		{name: "start missing, the path a publish takes", start: func(t *testing.T, ctx context.Context, st store.Store) *Manager {
			before := New(Options{Store: st, Emit: (&eventSink{}).emit, HealthInterval: time.Hour})
			install(t, ctx, before)
			before.stopAll()
			mgr := refusing(t, st)
			rows, err := st.Apps().List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if errs := mgr.StartMissing(ctx, rows, nil); len(errs) != 0 {
				t.Fatal(errs)
			}
			return mgr
		}},
		{name: "enable", start: func(t *testing.T, ctx context.Context, st store.Store) *Manager {
			mgr := refusing(t, st)
			install(t, ctx, mgr)
			if _, err := mgr.Disable(ctx, "files"); err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.Enable(ctx, "files"); err != nil {
				t.Fatal(err)
			}
			return mgr
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mgr := tc.start(t, ctx, testStore(t))
			v := waitStatus(t, mgr, "files", StatusDegraded)
			if v.Detail != commandRefusedDetail {
				t.Errorf("detail = %q, want the refusal sentence", v.Detail)
			}
			mgr.mu.RLock()
			inst := mgr.byName["files"]
			mgr.mu.RUnlock()
			if inst == nil || inst.runtime != nil {
				t.Fatal("files has a runtime, so its process was started")
			}
		})
	}
}

// TestOCINoDockerNamesRemoteOnlyWhenCommandRefused: an oci server on a host
// without docker, on a manager that refuses command servers, settles
// degraded with the sentence that names the remote runtime only.
func TestOCINoDockerNamesRemoteOnlyWhenCommandRefused(t *testing.T) {
	old := DefaultDockerBin
	DefaultDockerBin = filepath.Join(t.TempDir(), "no-such-docker")
	t.Cleanup(func() { DefaultDockerBin = old })
	mgr := New(Options{Store: testStore(t), Emit: (&eventSink{}).emit, HealthInterval: time.Hour, RefuseCommand: true})
	t.Cleanup(mgr.stopAll)
	raw, err := yaml.Marshal(Manifest{
		APIVersion: APIVersion, Kind: "App", Metadata: Metadata{Name: "box"},
		Server: map[string]any{"name": "straza.test/box", "version": "1.0.0"},
		Straza: Extensions{Runtime: RuntimeSpec{Kind: RuntimeOCI, OCI: &OCISpec{Image: "img", Sandbox: "none"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mf, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Install(context.Background(), mf, store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	if v := waitStatus(t, mgr, "box", StatusDegraded); v.Detail != ociNoDockerRemoteDetail {
		t.Errorf("detail = %q, want the sentence that names the remote runtime only", v.Detail)
	}
}
