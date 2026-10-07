package manager

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/store"
)

// TestDockerRunArgsSandbox asserts the sandbox defaults: read-only
// rootfs, all caps dropped, no-new-privileges, no network, env by name only
// (secret stays out of argv), `--rm -i` for stdio.
func TestDockerRunArgsSandbox(t *testing.T) {
	args := dockerRunArgs(OCISpec{
		Image:   "ghcr.io/example/mcp:1.0",
		Sandbox: "default",
		Env:     []EnvVar{{Name: "GITHUB_TOKEN", Value: "secret-should-not-appear"}},
	}, "", "example-app", "/tmp/example/cid")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"run --rm -i", "--read-only", "--cap-drop ALL",
		"--security-opt no-new-privileges", "--network none",
		"-e GITHUB_TOKEN", "ghcr.io/example/mcp:1.0",
		"--cidfile /tmp/example/cid", "--label straza.oci=1", "--label straza.app=example-app",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
	// The secret VALUE must never be in the argv, only the name.
	if strings.Contains(joined, "secret-should-not-appear") {
		t.Fatal("secret value leaked into docker argv")
	}
	// Image must be the final arg (nothing after it is a container command).
	if args[len(args)-1] != "ghcr.io/example/mcp:1.0" {
		t.Errorf("image is not the final arg: %v", args)
	}
}

// TestDockerRunArgsInjectedCredential: the broker-injected credential must be
// imported into the container by name (`-e NAME`, value only in the docker
// CLI process environment, never argv), not only the manifest env entries,
// or the container never sees the secret.
func TestDockerRunArgsInjectedCredential(t *testing.T) {
	args := dockerRunArgs(OCISpec{Image: "img", Sandbox: "default"}, "GITHUB_TOKEN", "img-app", "/tmp/cid")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-e GITHUB_TOKEN") {
		t.Fatalf("injected credential not imported into the container: %s", joined)
	}
	if args[len(args)-1] != "img" {
		t.Errorf("image is not the final arg: %v", args)
	}
}

func TestDockerRunArgsSandboxNone(t *testing.T) {
	args := dockerRunArgs(OCISpec{Image: "img", Sandbox: "none"}, "", "img-app", "/tmp/cid")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--read-only") || strings.Contains(joined, "--network none") {
		t.Errorf("sandbox=none must not add hardening flags: %s", joined)
	}
	if !strings.Contains(joined, "run --rm -i") {
		t.Errorf("stdio flags always present: %s", joined)
	}
}

// fakeSecrets is a SecretSource whose secret can be set mid-test (the
// strazactl `apps secret set` seam without a broker).
type fakeSecrets struct {
	mu    sync.Mutex
	byApp map[string]*Secret
}

func (f *fakeSecrets) set(appID string, s *Secret) {
	f.mu.Lock()
	f.byApp[appID] = s
	f.mu.Unlock()
}

func (f *fakeSecrets) AppSecret(appID string) *Secret {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byApp[appID]
}

func (f *fakeSecrets) ForRoles(appID string, _ []string, _ string) *Secret { return f.AppSecret(appID) }

func (f *fakeSecrets) ForUser(appID, _ string) UserCredential {
	s := f.AppSecret(appID)
	return UserCredential{Secret: s, Present: s != nil}
}

// TestSecretUpdatedStartsPendingOCIApp: a credentialed oci app parks in
// StatusPending, and setting its secret must start it, the same gate the
// command runtime has. No Docker needed: DefaultDockerBin is swapped
// for this test binary, which ignores the docker argv and serves MCP over
// stdio when STRAZA_MCP_HELPER=1 is in its (injected) process environment.
func TestSecretUpdatedStartsPendingOCIApp(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old := DefaultDockerBin
	DefaultDockerBin = exe
	t.Cleanup(func() { DefaultDockerBin = old })

	secrets := &fakeSecrets{byApp: map[string]*Secret{}}
	sink := &eventSink{}
	mgr := New(Options{Store: testStore(t), Emit: sink.emit, Secrets: secrets, HealthInterval: time.Hour})
	t.Cleanup(mgr.stopAll)

	m := Manifest{
		APIVersion: APIVersion,
		Kind:       "App",
		Metadata:   Metadata{Name: "ocisecret"},
		Server:     map[string]any{"name": "straza.test/ocisecret", "version": "1.0.0"},
		Straza: Extensions{
			Runtime: RuntimeSpec{Kind: RuntimeOCI, OCI: &OCISpec{
				Image:   "straza.test/fake-image",
				Sandbox: "none",
				Env:     []EnvVar{{Name: "STRAZA_MCP_HELPER", Value: "1"}},
			}},
			Credential: &CredentialSpec{Kind: CredentialStatic, Inject: &InjectSpec{As: InjectEnv, Name: "GITHUB_TOKEN"}},
		},
	}
	raw, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("oci manifest invalid: %v", err)
	}

	ctx := context.Background()
	row, err := mgr.Install(ctx, parsed, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "ocisecret", StatusPending)

	secrets.set(row.ID, &Secret{Value: "tok-123"})
	mgr.SecretUpdated(ctx, row.ID)
	waitStatus(t, mgr, "ocisecret", StatusRunning)
}

// TestOCIRuntimeLive runs a real container (Docker-gated; CI has Docker). It
// exercises an MCP server image over stdio through the oci runtime. Skipped
// when docker is unavailable, so it never blocks the local/no-Docker matrix.
func TestOCIRuntimeLive(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
	// The public everything server speaks MCP over stdio and is small.
	const image = "docker.io/mcp/everything"
	if err := exec.Command("docker", "pull", image).Run(); err != nil {
		t.Skipf("cannot pull %s: %v", image, err)
	}

	rt, err := NewOCIRuntime("everything", OCISpec{Image: image, Sandbox: "default"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt.ConnectTimeout = 30 * time.Second
	rt.BackoffBase = 200 * time.Millisecond
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Stop)

	deadline := time.Now().Add(30 * time.Second)
	for !rt.Ready() {
		if time.Now().After(deadline) {
			t.Fatalf("oci runtime never became ready; logs:\n%s", strings.Join(rt.Logs(0), "\n"))
		}
		time.Sleep(100 * time.Millisecond)
	}
	tools, err := rt.Tools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) == 0 {
		t.Error("expected a non-empty tool inventory from the container")
	}

	// Stop kills the container: nothing carrying this app's label survives.
	rt.Stop()
	out, err := exec.Command("docker", "ps", "-q", "--filter", "label=straza.app=everything").Output()
	if err != nil {
		t.Fatal(err)
	}
	if ids := strings.TrimSpace(string(out)); ids != "" {
		t.Fatalf("containers left after Stop: %s", ids)
	}
}
