package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/store"
)

// TestRemoveDeletesAccessAndSecrets pins what app removal takes along: the
// app's access rows and every credential row go, the per-user OAuth grant
// included, so a server installed again under the same name resolves no
// old grant; the names of the roles that held access come back sorted, and
// removing an unknown app answers not found.
func TestRemoveDeletesAccessAndSecrets(t *testing.T) {
	mgr, _ := testManager(t)
	ctx := context.Background()
	st := mgr.opts.Store

	row, err := mgr.Install(ctx, helperManifest(t, "rmapp", nil), store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "rmapp", StatusRunning)
	ops, err := st.Roles().Create(ctx, store.Role{Name: "ops", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.Role{ops, dev} {
		if _, err := st.ToolBindings().Create(ctx, store.ToolBinding{RoleID: r.ID, AppID: row.ID, ToolMatcher: `["*"]`}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []store.Credential{
		{AppID: row.ID, Scope: store.CredScopeApp, OwnerID: row.ID, Kind: store.CredStatic, EncPayload: []byte{1}},
		{AppID: row.ID, Scope: store.CredScopeRole, OwnerID: dev.ID, Kind: store.CredStatic, EncPayload: []byte{2}},
		{AppID: row.ID, Scope: store.CredScopeUser, OwnerID: "u1", Kind: store.CredOAuth, EncPayload: []byte{3}},
	} {
		if _, err := st.Credentials().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	roles, err := mgr.Remove(ctx, "rmapp")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(roles, ",") != "dev,ops" {
		t.Errorf("removed roles = %v, want [dev ops]", roles)
	}
	if bs, err := st.ToolBindings().List(ctx); err != nil || len(bs) != 0 {
		t.Errorf("bindings after remove = %v err=%v, want none", bs, err)
	}
	creds, err := st.Credentials().ListByApp(ctx, row.ID)
	if err != nil || len(creds) != 0 {
		t.Errorf("credentials after remove = %+v err=%v, want none, the user grant included", creds, err)
	}
	if _, err := mgr.Remove(ctx, "rmapp"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second remove err = %v, want not found", err)
	}
	if _, err := mgr.Remove(ctx, "never-installed"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("remove unknown err = %v, want not found", err)
	}
}

// TestCheckProvider pins the install refusal for an oauth manifest whose
// provider is not configured: only kind oauth is checked, the sentence
// names the provider, the config key and the configured alternatives. A
// manifest with agents client_credentials also needs the provider's
// clientCredentials block, which is the operator's word that the provider
// trusts this deployment's client assertion keys.
func TestCheckProvider(t *testing.T) {
	oauth := func(provider string) Manifest {
		return Manifest{Straza: Extensions{
			Runtime:    RuntimeSpec{Kind: RuntimeRemote, Remote: &RemoteSpec{URL: "http://x/mcp"}},
			Credential: &CredentialSpec{Kind: CredentialOAuth, OAuth: &OAuthSpec{Provider: provider}, Inject: &InjectSpec{As: InjectHeader, Name: "Authorization"}},
		}}
	}
	agentClients := func(provider string) Manifest {
		mf := oauth(provider)
		mf.Metadata.Name = "midpoint"
		mf.Straza.Credential.Agents = AgentsClientCredentials
		return mf
	}
	cases := []struct {
		name      string
		providers []string
		withBlock []string
		mf        Manifest
		wantErr   string
	}{
		{name: "client credentials with the block passes", providers: []string{"keycloak"}, withBlock: []string{"keycloak"}, mf: agentClients("keycloak")},
		{name: "client credentials without the block", providers: []string{"keycloak", "okta"}, withBlock: []string{"okta"}, mf: agentClients("keycloak"),
			wantErr: "server midpoint sets credential.agents to client_credentials, and the provider keycloak has no clientCredentials settings. Add oauth.providers.keycloak.clientCredentials.assertionAudience to strazad's config, or set credential.agents to own, sponsor or shared."},
		{name: "client credentials against an unknown provider", providers: []string{"keycloak"}, withBlock: []string{"keycloak"}, mf: agentClients("okta"),
			wantErr: `credential.oauth.provider "okta" is not configured on this server. Add oauth.providers.okta to the strazad config, or pick one of: keycloak.`},
		{name: "another agents value needs no block", providers: []string{"keycloak"}, mf: oauth("keycloak")},
		{name: "kind none passes", providers: nil, mf: Manifest{}},
		{name: "kind static passes", providers: nil, mf: Manifest{Straza: Extensions{Credential: &CredentialSpec{Kind: CredentialStatic}}}},
		{name: "configured provider passes", providers: []string{"keycloak", "github"}, mf: oauth("github")},
		{name: "unknown provider with alternatives", providers: []string{"keycloak"}, mf: oauth("github"),
			wantErr: `credential.oauth.provider "github" is not configured on this server. Add oauth.providers.github to the strazad config, or pick one of: keycloak.`},
		{name: "unknown provider with none configured", providers: nil, mf: oauth("github"),
			wantErr: `credential.oauth.provider "github" is not configured on this server. Add oauth.providers.github to the strazad config.`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Options{Store: testStore(t), OAuthProviders: tc.providers, ClientCredentialsProviders: tc.withBlock})
			err := m.CheckProvider(tc.mf)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Errorf("err = %v\nwant %s", err, tc.wantErr)
			}
		})
	}
}

// TestOCIDegradedWithoutDocker: an oci app on a host without docker settles
// degraded with the sentence that names the fix, instead of the raw exec
// error.
func TestOCIDegradedWithoutDocker(t *testing.T) {
	old := DefaultDockerBin
	DefaultDockerBin = filepath.Join(t.TempDir(), "no-such-docker")
	t.Cleanup(func() { DefaultDockerBin = old })
	mgr, _ := testManager(t)
	ctx := context.Background()
	raw, err := yaml.Marshal(Manifest{
		APIVersion: APIVersion, Kind: "App", Metadata: Metadata{Name: "ocinodocker"},
		Server: map[string]any{"name": "straza.test/ocinodocker", "version": "1.0.0"},
		Straza: Extensions{Runtime: RuntimeSpec{Kind: RuntimeOCI, OCI: &OCISpec{Image: "img", Sandbox: "none"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mf, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Install(ctx, mf, store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	v := waitStatus(t, mgr, "ocinodocker", StatusDegraded)
	if v.Detail != ociNoDockerDetail {
		t.Errorf("detail = %q, want the no-docker sentence", v.Detail)
	}
}

// fakeDocker re-executes the test binary as a Docker stand-in and returns
// its argument log. The DOCKER_ prefix reaches the child through the OCI
// runtime's existing environment allowlist.
func fakeDocker(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	t.Setenv("DOCKER_FAKE_LOG", log)
	old := DefaultDockerBin
	DefaultDockerBin = exe
	t.Cleanup(func() { DefaultDockerBin = old })
	return log
}

// TestBootSweepKillsLabeledContainers: a manager that loads an oci app at
// boot first kills every container carrying the straza.oci label, what a
// strazad that died mid-flight left behind; a boot without an oci app
// never asks docker.
func TestBootSweepKillsLabeledContainers(t *testing.T) {
	log := fakeDocker(t)
	st := testStore(t)
	ctx := context.Background()
	mgr := New(Options{Store: st, HealthInterval: time.Hour})
	t.Cleanup(mgr.stopAll)
	if err := mgr.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("a boot without an oci app asked docker: %v", err)
	}
	if _, err := st.Apps().Create(ctx, store.App{Name: "ocirow", RuntimeKind: RuntimeOCI, Status: StatusStopped, Manifest: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Load(ctx); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "ps -q --filter label=straza.oci=1") || !strings.Contains(string(argv), "kill cid-left-over") {
		t.Errorf("boot sweep argv:\n%s", argv)
	}
}

// TestOCIRuntimeStopKillsContainer drives the oci runtime with the fake
// docker: Stop must kill the container by the id from the cidfile, the run
// argv must carry the app label, and the temp dir must go.
func TestOCIRuntimeStopKillsContainer(t *testing.T) {
	log := fakeDocker(t)

	rt, err := NewOCIRuntime("fakeapp", OCISpec{Image: "img", Sandbox: "none"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Stop)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "fake container ready", rt.Ready)
	rt.Stop()

	argv, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "kill cid-fake-0001") {
		t.Errorf("Stop did not kill the container by its id; docker argv:\n%s", argv)
	}
	if !strings.Contains(string(argv), "--label straza.app=fakeapp") {
		t.Errorf("run argv lacks the app label:\n%s", argv)
	}
	if _, err := os.Stat(rt.dir); !os.IsNotExist(err) {
		t.Errorf("cidfile dir %s survived Stop: %v", rt.dir, err)
	}
	rt.Stop() // idempotent
}
