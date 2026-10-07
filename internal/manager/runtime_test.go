package manager

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func startCommandRuntime(t *testing.T, spec CommandSpec) *CommandRuntime {
	t.Helper()
	r := NewCommandRuntime("helper", spec, nil, nil)
	r.ConnectTimeout = 10 * time.Second
	r.BackoffBase = 50 * time.Millisecond
	r.BackoffMax = time.Second
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	waitFor(t, 10*time.Second, "runtime ready", r.Ready)
	return r
}

func TestCommandRuntimeServesTools(t *testing.T) {
	r := startCommandRuntime(t, helperSpec(t))
	ctx := context.Background()

	tools, err := r.Tools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["echo"] || !names["die"] {
		t.Fatalf("tool inventory missing expected tools: %v", names)
	}

	res, err := r.Call(ctx, CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "hi"})})
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(res); got != "echo: hi" {
		t.Errorf("echo = %q", got)
	}
	if err := r.Ping(ctx, nil); err != nil {
		t.Errorf("ping: %v", err)
	}
}

// TestCommandRuntimeRestartsAfterCrash pins crash supervision: kill child →
// auto-restart with backoff, and the log ring shows both process generations.
func TestCommandRuntimeRestartsAfterCrash(t *testing.T) {
	r := startCommandRuntime(t, helperSpec(t))
	ctx := context.Background()

	if _, err := r.Call(ctx, CallInput{Tool: "die"}); err != nil {
		// The child may exit before the response flushes; either way it dies.
		t.Logf("die call returned error (acceptable): %v", err)
	}
	waitFor(t, 10*time.Second, "runtime down", func() bool { return !r.Ready() || r.Restarts() > 0 })
	waitFor(t, 10*time.Second, "runtime restarted", func() bool { return r.Ready() && r.Restarts() >= 1 })

	res, err := r.Call(ctx, CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "back"})})
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(res); got != "echo: back" {
		t.Errorf("echo after restart = %q", got)
	}

	logs := strings.Join(r.Logs(0), "\n")
	if strings.Count(logs, "helper started") < 2 {
		t.Errorf("log ring should show two process generations:\n%s", logs)
	}
}

// TestCommandRuntimeBackoffBoundsCrashLoop asserts the supervisor retries an
// always-crashing child with growing backoff instead of spinning.
func TestCommandRuntimeBackoffBoundsCrashLoop(t *testing.T) {
	r := NewCommandRuntime("crasher", helperSpec(t, EnvVar{Name: "STRAZA_HELPER_CRASH", Value: "1"}), nil, nil)
	r.ConnectTimeout = 5 * time.Second
	r.BackoffBase = 50 * time.Millisecond
	r.BackoffMax = 400 * time.Millisecond
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)

	time.Sleep(1200 * time.Millisecond)
	attempts := r.Restarts()
	if attempts < 2 {
		t.Errorf("expected the supervisor to keep retrying, got %d attempts", attempts)
	}
	if attempts > 20 {
		t.Errorf("backoff not applied: %d attempts in 1.2s", attempts)
	}
	if r.Ready() {
		t.Error("crash-looping runtime must not report ready")
	}

	if _, err := r.Call(context.Background(), CallInput{Tool: "echo"}); err == nil {
		t.Error("calls while down must fail closed")
	}
}

func TestCommandRuntimeStopIsIdempotent(t *testing.T) {
	r := startCommandRuntime(t, helperSpec(t))
	r.Stop()
	r.Stop()
	if r.Ready() {
		t.Error("stopped runtime reports ready")
	}
}

func TestRingWriterSplitsLines(t *testing.T) {
	ring := NewRing(4)
	_, _ = ring.Write([]byte("one\ntwo\r\npart"))
	_, _ = ring.Write([]byte("ial\n"))
	got := ring.Last(0)
	want := []string{"one", "two", "partial"}
	if len(got) != len(want) {
		t.Fatalf("lines = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
	for i := 0; i < 10; i++ {
		ring.Append("x")
	}
	if n := len(ring.Last(0)); n != 4 {
		t.Errorf("ring should cap at 4 lines, got %d", n)
	}
}

// TestChildEnvAllowlist pins the environment a child receives: PATH, HOME
// and LANG from the parent when set, the parent names under a requested
// prefix, then the manifest env and the injected credential in that order.
func TestChildEnvAllowlist(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"STRAZA_STORE_DSN=postgres://straza:sekret@db/straza",
		"HOME=/home/straza",
		"STRAZA_ADMIN_TOKEN=wat_sekret",
		"LANG=C.UTF-8",
		"DOCKER_HOST=unix:///run/user/1000/docker.sock",
	}
	tests := []struct {
		name     string
		parent   []string
		prefixes []string
		manifest []EnvVar
		extra    []EnvVar
		want     []string
	}{
		{
			name:   "no prefixes keeps exactly PATH, HOME and LANG",
			parent: parent,
			want:   []string{"PATH=/usr/bin", "HOME=/home/straza", "LANG=C.UTF-8"},
		},
		{
			name:     "the DOCKER_ prefix adds DOCKER_HOST and still drops the STRAZA_ names",
			parent:   parent,
			prefixes: []string{"DOCKER_"},
			want:     []string{"PATH=/usr/bin", "HOME=/home/straza", "LANG=C.UTF-8", "DOCKER_HOST=unix:///run/user/1000/docker.sock"},
		},
		{
			name:   "an unset HOME produces no HOME entry",
			parent: []string{"PATH=/usr/bin", "STRAZA_STORE_DSN=postgres://straza:sekret@db/straza", "LANG=C.UTF-8"},
			want:   []string{"PATH=/usr/bin", "LANG=C.UTF-8"},
		},
		{
			name:     "manifest and extra entries follow the parent entries in order",
			parent:   parent,
			manifest: []EnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}},
			extra:    []EnvVar{{Name: "C", Value: "3"}},
			want:     []string{"PATH=/usr/bin", "HOME=/home/straza", "LANG=C.UTF-8", "A=1", "B=2", "C=3"},
		},
		{
			name:     "a manifest entry with a parent name comes later so it wins",
			parent:   parent,
			manifest: []EnvVar{{Name: "HOME", Value: "/srv/app"}},
			want:     []string{"PATH=/usr/bin", "HOME=/home/straza", "LANG=C.UTF-8", "HOME=/srv/app"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := childEnv(tc.parent, tc.prefixes, tc.manifest, tc.extra)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("childEnv = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCommandRuntimeChildEnvIsAllowlisted spawns the helper while strazad
// secret faces are set in the parent and proves the child sees none of them,
// keeps PATH and receives the manifest env.
func TestCommandRuntimeChildEnvIsAllowlisted(t *testing.T) {
	t.Setenv("STRAZA_STORE_DSN", "postgres://straza:sekret@db/straza")
	t.Setenv("STRAZA_ADMIN_TOKEN", "wat_sekret")
	r := startCommandRuntime(t, helperSpec(t, EnvVar{Name: "STRAZA_LANE_MARK", Value: "present"}))
	ctx := context.Background()
	childGetenv := func(name string) string {
		t.Helper()
		res, err := r.Call(ctx, CallInput{Tool: "env", Args: mustJSON(t, map[string]string{"name": name})})
		if err != nil {
			t.Fatalf("env %s: %v", name, err)
		}
		return textOf(res)
	}
	for _, tc := range []struct{ name, want string }{
		{"STRAZA_STORE_DSN", ""},
		{"STRAZA_ADMIN_TOKEN", ""},
		{"STRAZA_LANE_MARK", "present"},
	} {
		if got := childGetenv(tc.name); got != tc.want {
			t.Errorf("child %s = %q, want %q", tc.name, got, tc.want)
		}
	}
	if childGetenv("PATH") == "" {
		t.Error("child PATH is empty")
	}
}
