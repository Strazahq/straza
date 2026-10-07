package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// App status values (FSM). Legality lives in the DB CHECK constraint; these
// constants keep call sites typo-safe.
const (
	StatusPending  = "pending"
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusDegraded = "degraded"
	StatusStopped  = "stopped"
	StatusFailed   = "failed"
)

// SecretSource resolves broker credentials from in-memory state only: it is
// called on gateway request paths, where DB reads are forbidden. The
// credential broker implements it.
type SecretSource interface {
	// AppSecret is the app-level credential used for inventory and health.
	AppSecret(appID string) *Secret
	// ForRoles resolves the credential for a calling session's role names.
	// grantingRole names the role whose access row admitted the call, so a
	// per-role secret on that role wins over the other held roles.
	ForRoles(appID string, roles []string, grantingRole string) *Secret
	// ForUser resolves one user's own row on a caller-kind app: their OAuth
	// grant or their pasted token.
	ForUser(appID, userID string) UserCredential
}

// UserCredential is one caller's own credential row as the broker holds it
// in memory. Secret is nil when no row exists, the row expired or it cannot
// be opened; Present tells the first case from the other two so a resolver
// can deny an expired row instead of falling through to another source.
type UserCredential struct {
	Secret      *Secret
	Present     bool
	ExpiresAt   *time.Time
	AllowAgents bool // the owner lets their sponsored agents use the row
}

// ClientTokenRequest names whose client token is wanted. ClientID is the
// username of the verified session, because it becomes the client the
// provider authenticates, and no other input may fill it.
type ClientTokenRequest struct {
	Provider string // the provider name of the server's manifest
	Server   string // the server's name, for the sentences
	ServerID string
	UserID   string
	ClientID string
	Session  string
}

// ClientTokens gets the upstream token of an agent's own client at the
// server's OAuth provider. It answers from memory or asks the provider within
// its own limit, and every failure is the sentence the agent reads.
type ClientTokens interface {
	Token(ctx context.Context, r ClientTokenRequest) (string, error)
}

// Credential sources, recorded on every gateway call: the caller's own row,
// the sponsor's row under the sponsor's opt-in, the server's shared row, or
// the token of the agent's own client at the provider.
const (
	SourceOwn               = "own"
	SourceSponsor           = "sponsor"
	SourceShared            = "shared"
	SourceClientCredentials = "client_credentials"
)

// Caller is what the resolver knows about the calling session.
type Caller struct {
	UserID       string
	User         string // username, for the deny texts
	Roles        []string
	GrantingRole string // the role whose access row admitted the call
	Agent        bool   // userType agent; only agents ever fall back
	Sponsor      string // the sponsor's username, for the deny text
	SponsorID    string // the sponsor's user id, empty when none
	Session      string // the verified session id
}

// Resolved is the credential a call runs on and where it came from. Owner
// is the sponsor's user id when Source is sponsor and empty otherwise.
type Resolved struct {
	Secret *Secret
	Source string
	Owner  string
}

// Options configures a Manager.
type Options struct {
	Store store.Store
	// Emit publishes a CloudEvent through the transactional outbox.
	Emit func(ctx context.Context, subject string, data map[string]any)
	Log  *slog.Logger
	// Secrets is the credential broker; nil means no credentials resolve.
	Secrets SecretSource
	// HealthInterval paces the health/drift loop (default 20 s).
	HealthInterval time.Duration
	// RingSize caps per-app log rings (default 256 lines).
	RingSize int
	// OAuthProviders names the oauth providers configured on this server;
	// an oauth-kind manifest naming any other provider is refused by
	// CheckProvider before it installs.
	OAuthProviders []string
	// ConnectPageURL is the URL of a person's credentials page, named by
	// the deny sentences that ask a person to connect. Empty names the
	// page without a URL.
	ConnectPageURL string
	// ClientCredentialsProviders names the oauth providers whose config
	// carries a clientCredentials block; a manifest with agents
	// client_credentials against any other provider is refused by
	// CheckProvider.
	ClientCredentialsProviders []string
	// ClientTokens serves agents client_credentials; nil refuses those calls.
	ClientTokens ClientTokens
	// AllowLoopbackUpstreams lets a remote runtime dial a loopback address:
	// apps.allowLoopbackUpstreams, true under the standalone profile and
	// false under enterprise.
	AllowLoopbackUpstreams bool
	// RefuseCommand keeps every command server from starting: CheckRuntime
	// refuses its manifest, and one already stored is registered degraded
	// and never spawned. strazad sets it under the enterprise profile, with
	// no config key, because the child would share strazad's user, files and
	// network.
	RefuseCommand bool
}

// commandRefusedDetail is the degraded detail of a command server on a
// manager that refuses command servers: what failed, why, and what to do.
const commandRefusedDetail = "this server does not start command servers under the enterprise profile, because the process would run inside Straza with access to its keys and database. Run it as its own service or pod and switch it to the remote runtime."

// CommandRefusal is the sentence that refuses the command server name under
// the enterprise profile. The drafts check answers the same words, split
// into its sentence and its fix.
func CommandRefusal(name string) string {
	return name + " runs as a command, which this server refuses under the enterprise profile, because the process would run inside Straza with access to its keys and database. " +
		"Run the server as its own service or pod and add it as a remote server over HTTP."
}

// CheckRuntime refuses a command manifest when the manager refuses command
// servers, with CommandRefusal. Every other manifest passes.
func (m *Manager) CheckRuntime(mf Manifest) error {
	if !m.opts.RefuseCommand || mf.Straza.Runtime.Kind != RuntimeCommand {
		return nil
	}
	return errors.New(CommandRefusal(mf.Metadata.Name))
}

// Runtimes lists the runtimes this manager starts, for /version: remote,
// command unless it refuses command servers, and oci when docker is on the
// PATH.
func (m *Manager) Runtimes() []string {
	out := []string{RuntimeRemote}
	if !m.opts.RefuseCommand {
		out = append(out, RuntimeCommand)
	}
	if DockerOnPath() {
		out = append(out, RuntimeOCI)
	}
	return out
}

// CheckProvider refuses an oauth-kind manifest whose provider is not
// configured on this server, and one with agents client_credentials whose
// provider has no clientCredentials block, each with the sentence the install
// lane answers and a draft of the apps directory stores. Every other
// credential kind passes.
func (m *Manager) CheckProvider(mf Manifest) error {
	if mf.CredentialKind() != CredentialOAuth {
		return nil
	}
	name := oauthProviderOf(mf)
	known := append([]string{}, m.opts.OAuthProviders...)
	sort.Strings(known)
	for _, p := range known {
		if p != name {
			continue
		}
		if mf.AgentsSource() != AgentsClientCredentials || slices.Contains(m.opts.ClientCredentialsProviders, name) {
			return nil
		}
		return errors.New(fmt.Sprintf("server %s sets credential.agents to client_credentials, and the provider %s has no clientCredentials settings. "+
			"Add oauth.providers.%s.clientCredentials.assertionAudience to strazad's config, or set credential.agents to own, sponsor or shared",
			mf.Metadata.Name, name, name) + ".")
	}
	msg := fmt.Sprintf("credential.oauth.provider %q is not configured on this server. Add oauth.providers.%s to the strazad config", name, name)
	if len(known) > 0 {
		msg += ", or pick one of: " + strings.Join(known, ", ")
	}
	return errors.New(msg + ".")
}

// Manager owns MCP app lifecycle: install/remove pipelines, the runtime
// registry, tool-inventory caching with drift detection, and health checks.
// All state the gateway needs at request time lives in memory here.
type Manager struct {
	opts Options

	mu     sync.RWMutex
	byName map[string]*instance
	change []func() // catalog invalidation subscribers (gateway)
	// paused is the admin-pause overlay: app names an operator has disabled.
	// It is loaded from the settings KV at Boot and updated persist-first by
	// Disable/Enable. The boot loop and the starts of an apply consult it so a
	// paused app is never resurrected. Guarded by mu.
	paused map[string]bool
	// adminMu serializes the admin verbs (Install, Remove, Disable, Enable),
	// SecretUpdated, StopNamed, StartMissing and the load of the paused set,
	// so none interleaves with another's read-modify-write of a row, an
	// instance or the paused set. It is taken before mu and never while mu is
	// held.
	adminMu sync.Mutex
}

// instance is one managed app. app is the row it started from and is never
// written after, so it reads without mu.
type instance struct {
	mu       sync.Mutex
	app      store.App
	manifest Manifest
	runtime  Runtime
	ring     *Ring
	status   string
	detail   string
	deployed bool // apps.deployed emitted
	dead     bool // replaced/removed: late runtime callbacks must not write

	// registered is when register made this the instance of its name.
	registered time.Time

	inventory []*mcp.Tool
	invHash   string
	invFilled bool

	// views are the views served beside inventory, which invHash covers
	// with it. viewCache is the last read of the views, for the set of links
	// in viewLinks, made at viewsRead, so a probe reads them again only when
	// needed (refreshViews).
	views     []View
	viewCache viewRead
	viewLinks string
	viewsRead time.Time

	// Health observability: lastProbe is when the manager last completed
	// a health evaluation (inventory probe or remote ping), and lastHealthy is
	// when a probe last settled the app running. Zero means never. In-memory
	// only: the admin surface reads them off the view, never the store.
	lastProbe   time.Time
	lastHealthy time.Time

	// statusSince is when the status word last changed; a new detail under
	// the same word keeps it, so the admin surface can say "degraded for
	// 12 minutes" across a crash loop. Zero = never set.
	statusSince time.Time
}

// AppView is a read-only snapshot of one managed app for the gateway and
// admin surface.
type AppView struct {
	ID       string
	Name     string
	Version  string
	Runtime  string
	Status   string
	Detail   string
	Source   string
	Tools    []*mcp.Tool // cached upstream inventory ∩ manifest exposure
	Manifest Manifest
	// Offered lists, sorted, the name of every tool the upstream server
	// itself lists, before the manifest's exposure list cuts it down to
	// Tools. Empty until the first inventory probe, so an exposure picker
	// can offer a tool the manifest leaves out.
	Offered []string

	// ViewsOn mirrors the manifest's straza.exposure.views switch. Views
	// holds, sorted by URI, the views the listed tools link, and is empty
	// while the switch is off.
	ViewsOn bool
	Views   []View

	// Paused reports the admin-pause overlay: an operator disabled this
	// app, so neither boot nor the GitOps watcher will resurrect it. A live app
	// is never paused (Disable stops it), so this is only ever true on the
	// at-rest view returned by Disable.
	Paused bool

	// LastProbe is the time of the last completed health evaluation;
	// LastHealthy the last time a probe settled the app running. Zero = never.
	LastProbe   time.Time
	LastHealthy time.Time

	// StatusSince is when the status word last changed. Zero = never set.
	StatusSince time.Time
}

// New builds a Manager.
func New(opts Options) *Manager {
	if opts.HealthInterval <= 0 {
		opts.HealthInterval = 20 * time.Second
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Manager{opts: opts, byName: map[string]*instance{}, paused: map[string]bool{}}
}

// OnChange registers a catalog-invalidation callback, fired whenever app
// visibility may have changed (deployed/removed/drift).
func (m *Manager) OnChange(fn func()) {
	m.mu.Lock()
	m.change = append(m.change, fn)
	m.mu.Unlock()
}

func (m *Manager) fireChange() {
	m.mu.RLock()
	subs := append([]func(){}, m.change...)
	m.mu.RUnlock()
	for _, fn := range subs {
		fn()
	}
}

// Load restores persisted apps at boot (control plane): every non-stopped app
// gets its runtime started again. Deployed events are not re-emitted.
func (m *Manager) Load(ctx context.Context) error {
	// Restore the admin-pause overlay before starting anything so a paused app
	// is never resurrected at boot (fail-closed: an unreadable set aborts the
	// boot rather than risk starting a disabled app).
	if err := m.loadPaused(ctx); err != nil {
		return err
	}
	apps, err := m.opts.Store.Apps().List(ctx)
	if err != nil {
		return fmt.Errorf("manager: load apps: %w", err)
	}
	// A strazad that died mid-flight leaves its containers running, since
	// docker does not stop a container when its client exits. Every container
	// this manager starts carries the straza.oci label, so one sweep at boot
	// kills the leftovers before the apps start again. The sweep runs only on
	// a host that has docker and an oci app to run.
	if hasOCIApp(apps) && DockerOnPath() {
		if err := sweepContainers(ctx); err != nil {
			m.opts.Log.Warn("manager: container sweep at boot failed", "err", err)
		}
	}
	for _, a := range apps {
		if a.Status == StatusStopped || m.IsPaused(a.Name) {
			continue
		}
		mf, err := FromJSON(a.Manifest)
		if err != nil {
			m.opts.Log.Error("manager: stored manifest unreadable", "app", a.Name, "err", err)
			continue
		}
		if err := m.startInstance(ctx, a, mf, true); err != nil {
			m.opts.Log.Error("manager: restart failed", "app", a.Name, "err", err)
		}
	}
	return nil
}

// Run drives the health/drift loop until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(m.opts.HealthInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-t.C:
			m.HealthCheck(ctx)
		}
	}
}

// Install parses nothing; the manifest is already validated. It upserts the
// app row (same name = update), builds and starts the runtime, and reports
// the app running once the upstream serves (apps.deployed event). A paused
// app takes the new manifest and stays stopped until Enable. A new or
// revived row first clears any pause left under its name. The install path
// is control plane: DB writes are expected here.
func (m *Manager) Install(ctx context.Context, mf Manifest, source string) (store.App, error) {
	ctx = asked(ctx)
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	manifestJSON, err := mf.JSON()
	if err != nil {
		return store.App{}, err
	}

	name := mf.Metadata.Name
	row, err := m.opts.Store.Apps().GetByName(ctx, name)
	switch {
	case err == nil:
		row.Version = mf.ServerVersion()
		row.Manifest = manifestJSON
		row.RuntimeKind = mf.Straza.Runtime.Kind
		row.Source = source
		if m.IsPaused(name) {
			row.Status = StatusStopped
			return m.opts.Store.Apps().Update(ctx, row)
		}
		// Reinstall/update: stop the old runtime before swapping.
		m.stopInstance(name)
		row.Status = StatusStarting
		row, err = m.opts.Store.Apps().Update(ctx, row)
	case errors.Is(err, store.ErrNotFound):
		// A server installed under a removed name is a new server, so a
		// pause left under that name must not hold it. Only a confirmed
		// absence reaches here: a failed read never clears a pause.
		if err = m.setPausedLocked(ctx, name, false); err != nil {
			return store.App{}, err
		}
		fresh := store.App{
			Name:        name,
			Version:     mf.ServerVersion(),
			Manifest:    manifestJSON,
			RuntimeKind: mf.Straza.Runtime.Kind,
			Status:      StatusStarting,
			Source:      source,
		}
		row, err = m.opts.Store.Apps().Create(ctx, fresh)
		if errors.Is(err, store.ErrConflict) {
			// A removed app keeps its soft-deleted row under the same name,
			// so installing that name again through this path revives the
			// row with its old id. The drafts publish, the production path,
			// deletes the row and inserts the server under a fresh id instead.
			row, err = m.opts.Store.Apps().Revive(ctx, fresh)
		}
	}
	if err != nil {
		return store.App{}, err
	}

	if err := m.startInstance(ctx, row, mf, false); err != nil {
		return row, err
	}
	return row, nil
}

// startInstance registers and starts the runtime for an app row.
func (m *Manager) startInstance(ctx context.Context, row store.App, mf Manifest, booted bool) error {
	inst := &instance{
		app:      row,
		manifest: mf,
		ring:     NewRing(m.opts.RingSize),
		status:   StatusStarting,
		deployed: booted,
	}

	switch mf.Straza.Runtime.Kind {
	case RuntimeCommand:
		// Every start path reaches this switch, so a command server stored
		// before the profile refused it is never spawned either.
		if m.opts.RefuseCommand {
			m.register(inst)
			m.setStatus(ctx, inst, StatusDegraded, commandRefusedDetail)
			return nil
		}
		spec := *mf.Straza.Runtime.Command
		if extra, ok := m.commandEnvSecret(inst); ok {
			spec.Env = append(append([]EnvVar{}, spec.Env...), extra)
		} else if mf.CredentialKind() != CredentialNone {
			// Fail closed: never spawn a credentialed app uncredentialed.
			m.register(inst)
			m.setStatus(ctx, inst, StatusPending, "waiting for a credential (strazactl apps secret set)")
			return nil
		}
		rt := NewCommandRuntime(row.Name, spec, nil, inst.ring)
		rt.Views = mf.viewsOn()
		rt.OnState = func(up bool, detail string) { m.onRuntimeState(inst, up, detail) }
		inst.runtime = rt
	case RuntimeRemote:
		var inject *InjectSpec
		if mf.CredentialKind() != CredentialNone {
			inject = mf.Straza.Credential.Inject
		}
		rrt := NewRemoteRuntime(row.Name, *mf.Straza.Runtime.Remote, inject, inst.ring)
		rrt.PerUser = mf.CallerKind()
		rrt.AllowLoopback = m.opts.AllowLoopbackUpstreams
		rrt.Views = mf.viewsOn()
		inst.runtime = rrt
	case RuntimeOCI:
		if !DockerOnPath() {
			detail := ociNoDockerDetail
			if m.opts.RefuseCommand {
				detail = ociNoDockerRemoteDetail
			}
			m.register(inst)
			m.setStatus(ctx, inst, StatusDegraded, detail)
			return nil
		}
		var injectEnv *EnvVar
		if e, ok := m.commandEnvSecret(inst); ok {
			injectEnv = &e
		} else if mf.CredentialKind() != CredentialNone {
			m.register(inst)
			m.setStatus(ctx, inst, StatusPending, "waiting for a credential (strazactl apps secret set)")
			return nil
		}
		rt, err := NewOCIRuntime(row.Name, *mf.Straza.Runtime.OCI, injectEnv, inst.ring)
		if err != nil {
			m.register(inst)
			m.setStatus(ctx, inst, StatusFailed, err.Error())
			return err
		}
		rt.Views = mf.viewsOn()
		rt.OnState = func(up bool, detail string) { m.onRuntimeState(inst, up, detail) }
		inst.runtime = rt
	default:
		return fmt.Errorf("manager: unknown runtime kind %q", mf.Straza.Runtime.Kind)
	}

	m.register(inst)
	if err := inst.runtime.Start(context.WithoutCancel(ctx)); err != nil {
		m.setStatus(ctx, inst, StatusFailed, err.Error())
		return err
	}
	// Remote runtimes have no supervisor: probe immediately so the drop-file
	// demo reaches running without waiting for the health tick.
	if mf.Straza.Runtime.Kind == RuntimeRemote {
		m.probe(ctx, inst)
	}
	return nil
}

// register installs the instance in the registry, replacing any previous
// instance of the same name. The old runtime is stopped OUTSIDE the registry
// lock: its supervisor goroutine may be inside a callback that acquires it.
func (m *Manager) register(inst *instance) {
	inst.registered = time.Now()
	m.mu.Lock()
	old := m.byName[inst.app.Name]
	m.byName[inst.app.Name] = inst
	m.mu.Unlock()
	retire(old)
}

// retire marks an instance dead (so late callbacks become no-ops) and stops
// its runtime.
func retire(inst *instance) {
	if inst == nil {
		return
	}
	inst.mu.Lock()
	inst.dead = true
	inst.mu.Unlock()
	if inst.runtime != nil {
		inst.runtime.Stop()
	}
}

// stop retires a live instance and marks its row stopped (the admin pause).
// The row stays. store.ErrNotFound when the app is not live.
func (m *Manager) stop(ctx context.Context, name string) error {
	m.mu.Lock()
	inst, ok := m.byName[name]
	if ok {
		delete(m.byName, name)
	}
	m.mu.Unlock()
	if !ok {
		return store.ErrNotFound
	}
	retire(inst)
	inst.mu.Lock()
	inst.status = StatusStopped
	row := inst.app
	inst.mu.Unlock()

	if err := m.opts.Store.Apps().SetStatus(ctx, row.ID, StatusStopped); err != nil {
		return err
	}
	m.emit(ctx, "straza.apps.removed", map[string]any{"app": row.ID, "name": row.Name})
	m.fireChange()
	return nil
}

// Remove stops an app and removes it: the row is soft-deleted, its access
// rows and every credential row are deleted, the per-user OAuth grants
// included, the admin pause under its name is cleared, and
// straza.apps.removed is emitted. A server installed again under the same
// name therefore starts with no grants and no pause, and every user
// connects afresh. It returns the sorted names of the roles whose access
// rows went, and store.ErrNotFound when no such app exists. A publish that
// adds the same name again deletes the row and inserts a fresh id.
func (m *Manager) Remove(ctx context.Context, name string) ([]string, error) {
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	live := true
	if err := m.stop(ctx, name); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		live = false
	}
	row, err := m.opts.Store.Apps().GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	roles, err := m.dropBindings(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	creds, err := m.opts.Store.Credentials().ListByApp(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range creds {
		if err := m.opts.Store.Credentials().Delete(ctx, c.ID); err != nil {
			return nil, err
		}
	}
	if err := m.opts.Store.Apps().SoftDelete(ctx, row.ID); err != nil {
		return nil, err
	}
	// A server installed again under this name is a new server, so the
	// pause goes with the row. A failed clear is logged, not returned: the
	// removal already happened and must still reach its audit record, and a
	// pause left behind only holds a server stopped.
	if err := m.setPausedLocked(ctx, name, false); err != nil {
		m.opts.Log.Warn("manager: app removed, but its pause could not be cleared. The next install of the name or the next start of strazad clears it", "app", name, "err", err)
	}
	if !live {
		m.emit(ctx, "straza.apps.removed", map[string]any{"app": row.ID, "name": row.Name})
		m.fireChange()
	}
	return roles, nil
}

// dropBindings deletes every access row of an app and returns the sorted
// names of the roles that held one.
func (m *Manager) dropBindings(ctx context.Context, appID string) ([]string, error) {
	bindings, err := m.opts.Store.ToolBindings().List(ctx)
	if err != nil {
		return nil, err
	}
	var roles []string
	for _, b := range bindings {
		if b.AppID != appID {
			continue
		}
		if err := m.opts.Store.ToolBindings().Delete(ctx, b.ID); err != nil {
			return nil, err
		}
		if role, err := m.opts.Store.Roles().GetByID(ctx, b.RoleID); err == nil {
			roles = append(roles, role.Name)
		}
	}
	sort.Strings(roles)
	return roles, nil
}

// stopInstance stops and forgets an instance by name (no events).
func (m *Manager) stopInstance(name string) {
	m.mu.Lock()
	inst := m.byName[name]
	delete(m.byName, name)
	m.mu.Unlock()
	retire(inst)
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	insts := make([]*instance, 0, len(m.byName))
	for _, i := range m.byName {
		insts = append(insts, i)
	}
	m.byName = map[string]*instance{}
	m.mu.Unlock()
	for _, i := range insts {
		retire(i)
	}
}

// onRuntimeState maps command-runtime supervision onto the FSM.
func (m *Manager) onRuntimeState(inst *instance, up bool, detail string) {
	ctx := context.Background()
	if !up {
		m.setStatus(ctx, inst, StatusDegraded, detail)
		return
	}
	m.probe(ctx, inst)
}

// stampProbe records that a health evaluation for this instance completed.
func (inst *instance) stampProbe() {
	inst.mu.Lock()
	inst.lastProbe = time.Now()
	inst.mu.Unlock()
}

// probe refreshes inventory and settles running/degraded for one instance.
func (m *Manager) probe(ctx context.Context, inst *instance) {
	start := time.Now()
	inst.stampProbe()
	secret := m.appSecret(inst)
	tools, err := inst.runtime.Tools(ctx, secret)
	if err != nil {
		m.failed(ctx, inst, start, "manager: the tool inventory probe failed", err)
		return
	}
	m.acceptInventory(ctx, inst, tools, m.refreshViews(ctx, inst, tools, secret))
}

// askedKey marks the context of a check that a request asked for.
type askedKey struct{}

// asked marks ctx as a request's, so that a check on it whose context ends
// says the request ended, and a probe on it reads the server's views again.
// The health loop, the boot and a replica's converge never mark theirs, and
// a stop of strazad cancels them.
func asked(ctx context.Context) context.Context {
	return context.WithValue(ctx, askedKey{}, true)
}

// failed records a failed health check or probe of inst that began at start
// as degraded, and logs line with the reason when the reason is new. When
// the check's context ended and err is only that context's error, err says
// nothing about the server. A check whose time ran out or whose request
// ended then says so and what to do next, and one that strazad's own stop
// cancelled records nothing.
func (m *Manager) failed(ctx context.Context, inst *instance, start time.Time, line string, err error) {
	reason := err.Error()
	if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		why := "the time given to it ran out"
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
		case ctx.Value(askedKey{}) != nil:
			why = "the request that asked for it ended"
		default:
			return
		}
		reason = fmt.Sprintf("the health check of the MCP server %s ended after %s with no answer from the server, "+
			"because %s while Straza was still waiting on the server or on another connection attempt to it. "+
			"An administrator checks that the server runs and answers at the address in its manifest, then runs strazactl apps recheck %s again",
			inst.app.Name, time.Since(start).Round(100*time.Millisecond), why, inst.app.Name)
	}
	if m.setStatus(ctx, inst, StatusDegraded, reason) {
		m.opts.Log.Warn(line, "app", inst.app.Name, "err", shown(reason))
	}
}

// acceptInventory caches the upstream tool list and detects drift, the
// rug-pull guard: a changed inventory degrades the app and emits
// straza.apps.drift; the catalog rebuilds from the new list so vanished
// tools fail closed immediately). views are the served views, which the
// drift hash covers with the tools' links while the views switch is on.
func (m *Manager) acceptInventory(ctx context.Context, inst *instance, tools []*mcp.Tool, views []View) {
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	hash := inventoryHash(tools, views, inst.manifest.viewsOn())

	inst.mu.Lock()
	if inst.dead {
		inst.mu.Unlock()
		return
	}
	first := !inst.invFilled
	prev, prevViews := inst.inventory, inst.views
	prevHash := inst.invHash
	inst.inventory, inst.views = tools, views
	inst.invHash = hash
	inst.invFilled = true
	inst.mu.Unlock()

	if first || prevHash == hash {
		if first {
			m.fireChange()
		}
		m.settleRunning(ctx, inst)
		return
	}

	added, removed := diffTools(prev, tools)
	changed := changedViews(prevViews, views)
	links := inst.manifest.viewsOn() && linksChanged(prev, tools)
	m.opts.Log.Warn("manager: tool inventory drift", "app", inst.app.Name, "added", added, "removed", removed, "views", changed)
	m.setStatus(ctx, inst, StatusDegraded, driftDetail(added, removed, changed, links))
	m.emit(ctx, "straza.apps.drift", map[string]any{
		"app": inst.app.ID, "name": inst.app.Name, "added": added, "removed": removed,
	})
	m.fireChange()
}

// settleRunning flips an instance to running and emits apps.deployed once.
// The event goes out before the status flip so "status running" implies the
// deployment was announced.
func (m *Manager) settleRunning(ctx context.Context, inst *instance) {
	inst.mu.Lock()
	emitDeploy := !inst.deployed && !inst.dead
	inst.deployed = true
	inst.lastHealthy = time.Now()
	inst.mu.Unlock()
	if emitDeploy {
		m.emit(ctx, "straza.apps.deployed", map[string]any{
			"app": inst.app.ID, "name": inst.app.Name, "version": inst.app.Version,
			"runtime": inst.app.RuntimeKind, "source": inst.app.Source,
		})
		m.fireChange()
	}
	m.setStatus(ctx, inst, StatusRunning, "")
}

// HealthCheck probes every instance once (driven by Run's ticker; callable
// directly in tests and after secret updates).
func (m *Manager) HealthCheck(ctx context.Context) {
	for _, inst := range m.instances() {
		m.checkInstance(ctx, inst)
	}
}

// checkInstance applies the health policy for one instance. Supervised stdio
// runtimes are probed only while a session is live (their supervisor already
// reports up/down transitions); remote runtimes get a liveness ping first.
func (m *Manager) checkInstance(ctx context.Context, inst *instance) {
	switch inst.manifest.Straza.Runtime.Kind {
	case RuntimeCommand, RuntimeOCI:
		if inst.runtime != nil && inst.runtime.Ready() {
			m.probe(ctx, inst)
		}
	case RuntimeRemote:
		start := time.Now()
		if err := inst.runtime.Ping(ctx, m.appSecret(inst)); err != nil {
			inst.stampProbe()
			m.failed(ctx, inst, start, "manager: the liveness ping failed", err)
			return
		}
		m.probe(ctx, inst)
	}
}

// HealthCheckOne runs one on-demand health evaluation for a single app (the
// admin recheck endpoint) with the same per-runtime semantics as the periodic
// loop, then returns the refreshed view. ok=false when the app is not managed
// (stopped or unknown).
func (m *Manager) HealthCheckOne(ctx context.Context, name string) (AppView, bool) {
	m.mu.RLock()
	inst, ok := m.byName[name]
	m.mu.RUnlock()
	if !ok {
		return AppView{}, false
	}
	m.checkInstance(asked(ctx), inst)
	return inst.view(), true
}

// SecretUpdated re-resolves credentials for one app: pending credentialed
// supervised apps (command and oci both park in StatusPending until a secret
// arrives) get their (re)start from the row as stored now, others a fresh
// probe.
func (m *Manager) SecretUpdated(ctx context.Context, appID string) {
	ctx = asked(ctx)
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	for _, inst := range m.instances() {
		if inst.app.ID != appID {
			continue
		}
		inst.mu.Lock()
		status := inst.status
		inst.mu.Unlock()
		supervised := inst.app.RuntimeKind == RuntimeCommand || inst.app.RuntimeKind == RuntimeOCI
		if supervised && (status == StatusPending || inst.runtime == nil) {
			m.restartParked(ctx, inst.app)
			continue
		}
		m.probe(ctx, inst)
	}
}

func (m *Manager) instances() []*instance {
	m.mu.RLock()
	out := make([]*instance, 0, len(m.byName))
	for _, i := range m.byName {
		out = append(out, i)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].app.Name < out[j].app.Name })
	return out
}

// View returns a snapshot of one app (in-memory; request-path safe).
func (m *Manager) View(name string) (AppView, bool) {
	m.mu.RLock()
	inst, ok := m.byName[name]
	m.mu.RUnlock()
	if !ok {
		return AppView{}, false
	}
	return inst.view(), true
}

// RPS returns the app's manifest rate limit (0 = unlimited) without building
// a full AppView; the gateway asks on every tools/call (hot path). The
// manifest is immutable per instance (reinstall swaps the instance, same as
// Call's lock-free manifest read), so this is a field read after the
// registry lookup.
func (m *Manager) RPS(name string) float64 {
	m.mu.RLock()
	inst, ok := m.byName[name]
	m.mu.RUnlock()
	if !ok || inst.manifest.Straza.Limits == nil {
		return 0
	}
	return inst.manifest.Straza.Limits.RPS
}

// UpstreamTimeout returns the app's manifest per-call ceiling
// (limits.timeoutSeconds; 0 = none configured, use the server-wide default).
// Same lock-free manifest read as RPS; the gateway asks per tools/call.
func (m *Manager) UpstreamTimeout(name string) time.Duration {
	m.mu.RLock()
	inst, ok := m.byName[name]
	m.mu.RUnlock()
	if !ok || inst.manifest.Straza.Limits == nil {
		return 0
	}
	return time.Duration(inst.manifest.Straza.Limits.TimeoutSeconds) * time.Second
}

// Views lists snapshots of all managed apps.
func (m *Manager) Views() []AppView {
	insts := m.instances()
	out := make([]AppView, 0, len(insts))
	for _, i := range insts {
		out = append(out, i.view())
	}
	return out
}

// Credential resolves the credential a call to appName runs on, from
// in-memory broker state (request path: no DB), and says where it came
// from. A kind none app resolves to nothing. A static app resolves the
// shared rows: the granting role's row, then a held role's, then the
// server's own. A caller-kind app (oauth or token) resolves the caller's
// own row; an own row that exists but expired or cannot be opened denies
// and never falls through. A human with no own row denies. An agent with no
// own row follows the manifest's credential.agents value: own denies,
// sponsor uses the sponsor's row when the sponsor allowed agents on it,
// shared uses the static rows, client_credentials asks ClientTokens for the
// token of the agent's own client, within that lane's limit under ctx. A
// command or oci runtime never resolves an own or sponsor row or a client
// token, whatever the manifest says, because one process is one identity.
// Every deny names the fix.
func (m *Manager) Credential(ctx context.Context, appName string, c Caller) (Resolved, error) {
	m.mu.RLock()
	inst, ok := m.byName[appName]
	m.mu.RUnlock()
	if !ok || inst.runtime == nil {
		return Resolved{}, notRunning(appName)
	}
	kind := inst.manifest.CredentialKind()
	if kind == CredentialNone {
		return Resolved{}, nil
	}
	if m.opts.Secrets == nil {
		return Resolved{}, m.missingCredential(inst, kind)
	}
	if !inst.manifest.CallerKind() {
		return m.sharedCredential(inst, c)
	}
	if rk := inst.manifest.Straza.Runtime.Kind; rk != RuntimeRemote {
		return Resolved{}, fmt.Errorf("app %s runs as one %s process and cannot carry a credential per caller. Its manifest must use kind static", inst.app.Name, rk)
	}
	own := m.opts.Secrets.ForUser(inst.app.ID, c.UserID)
	if own.Secret != nil {
		return Resolved{Secret: own.Secret, Source: SourceOwn}, nil
	}
	if own.Present {
		return Resolved{}, m.unusableOwnRow(inst, "", own)
	}
	if !c.Agent {
		return Resolved{}, m.missingCredential(inst, kind)
	}
	switch inst.manifest.AgentsSource() {
	case AgentsSponsor:
		return m.sponsorCredential(inst, c)
	case AgentsShared:
		res, err := m.sharedCredential(inst, c)
		if err != nil {
			if inst.manifest.CredentialKind() == CredentialOAuth {
				return Resolved{}, fmt.Errorf("%s and no shared secret is set for the server. An administrator sets %s", lack(inst, c), sharedFix(inst))
			}
			return Resolved{}, fmt.Errorf("%s and no shared secret is set for the server. %s, or %s", lack(inst, c), m.agentFix(inst, c), sharedFix(inst))
		}
		return res, nil
	case AgentsClientCredentials:
		return m.clientCredential(ctx, inst, c)
	default:
		return Resolved{}, fmt.Errorf("%s. %s", lack(inst, c), m.agentFix(inst, c))
	}
}

// ProbeWith checks that an app's upstream accepts a credential, for the
// paste-time test of a caller's token, without pooling the session. Only a
// remote runtime can take a credential per caller, so any other runtime
// answers an error naming that. The probe's error comes back as shown
// writes it, because the person who pasted the token reads it.
func (m *Manager) ProbeWith(ctx context.Context, appName string, secret *Secret) error {
	m.mu.RLock()
	inst, ok := m.byName[appName]
	m.mu.RUnlock()
	if !ok || inst.runtime == nil {
		return notRunning(appName)
	}
	rrt, ok := inst.runtime.(*RemoteRuntime)
	if !ok {
		return fmt.Errorf("app %s runs as one process and cannot take a credential per caller", appName)
	}
	return shownErr(rrt.Probe(ctx, secret))
}

// clientCredential resolves an agent's call onto the token of its own client
// at the server's provider. The client id is the username of the verified
// session and nothing else, because whoever chooses it chooses the identity
// the upstream sees. The secret id is stable per server and agent, so the
// upstream session pool closes the old session when the token is renewed.
func (m *Manager) clientCredential(ctx context.Context, inst *instance, c Caller) (Resolved, error) {
	if m.opts.ClientTokens == nil {
		return Resolved{}, fmt.Errorf("agent %s could not get a %s token, because this strazad started without its client credentials lane. An administrator reads the strazad log from its start", c.User, inst.app.Name)
	}
	token, err := m.opts.ClientTokens.Token(ctx, ClientTokenRequest{
		Provider: oauthProviderOf(inst.manifest), Server: inst.app.Name, ServerID: inst.app.ID,
		UserID: c.UserID, ClientID: c.User, Session: c.Session,
	})
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Secret: &Secret{ID: "client_credentials:" + inst.app.ID + ":" + c.UserID, Value: token}, Source: SourceClientCredentials}, nil
}

// sharedCredential resolves the static rows for the caller's roles.
func (m *Manager) sharedCredential(inst *instance, c Caller) (Resolved, error) {
	s := m.opts.Secrets.ForRoles(inst.app.ID, c.Roles, c.GrantingRole)
	if s == nil {
		return Resolved{}, m.missingCredential(inst, CredentialStatic)
	}
	return Resolved{Secret: s, Source: SourceShared}, nil
}

// sponsorCredential resolves an agent's call onto its sponsor's own row,
// which needs a sponsor, a usable row and the sponsor's opt-in.
func (m *Manager) sponsorCredential(inst *instance, c Caller) (Resolved, error) {
	if c.SponsorID == "" {
		return Resolved{}, fmt.Errorf("%s and no sponsor whose connection it could use. %s", lack(inst, c), m.agentFix(inst, c))
	}
	sp := m.opts.Secrets.ForUser(inst.app.ID, c.SponsorID)
	switch {
	case sp.Secret != nil && sp.AllowAgents:
		return Resolved{Secret: sp.Secret, Source: SourceSponsor, Owner: c.SponsorID}, nil
	case sp.Secret == nil && sp.Present:
		return Resolved{}, m.unusableOwnRow(inst, c.Sponsor, sp)
	case sp.Present:
		return Resolved{}, fmt.Errorf("%s, and its sponsor %s has not allowed agents on their %s connection. %s allows it on %s%s",
			lack(inst, c), c.Sponsor, inst.app.Name, c.Sponsor, m.pageRef("their"), ownFix(inst, c))
	default:
		return Resolved{}, fmt.Errorf("%s, and its sponsor %s has no %s connection either. %s connects on %s and allows agents there%s",
			lack(inst, c), c.Sponsor, inst.app.Name, c.Sponsor, m.pageRef("their"), ownFix(inst, c))
	}
}

// lack opens an agent's deny with what the agent is missing: a token on a
// token app, a sign-in of its own on an oauth app.
func lack(inst *instance, c Caller) string {
	if inst.manifest.CredentialKind() == CredentialOAuth {
		return fmt.Sprintf("agent %s has no %s sign-in of its own", c.User, inst.app.Name)
	}
	return fmt.Sprintf("agent %s has no token for app %s", c.User, inst.app.Name)
}

// ownFix is the clause for how an agent gets a row of its own beside the
// sponsor lane. On a token app an administrator pastes for it. On an oauth
// app there is no such way, so the clause is empty: a sign-in is stored only
// for the user of the session that finishes it in a browser, and no browser
// is signed in to Straza as an agent.
func ownFix(inst *instance, c Caller) string {
	if inst.manifest.CredentialKind() == CredentialOAuth {
		return ""
	}
	return fmt.Sprintf(", or an administrator sets the agent's own with strazactl connect %s --user %s", inst.app.Name, c.User)
}

// sharedFix is the clause for the shared secret an agent on agents shared
// could fall back to.
func sharedFix(inst *instance) string {
	return "a shared one with strazactl apps secret set " + inst.app.Name + ", which asks for the value at a hidden prompt"
}

// pageRef names the credentials page in a deny sentence, after the given
// possessive (your or their), with its URL when the server knows one.
func (m *Manager) pageRef(whose string) string {
	if m.opts.ConnectPageURL == "" {
		return whose + " credentials page"
	}
	return whose + " credentials page at " + m.opts.ConnectPageURL
}

// agentFix is the sentence naming how an agent with no row of its own gets
// a credential. On an oauth app an agent cannot sign in, so an administrator
// routes it by the server's credential.agents value; on a token app its
// sponsor pastes on their credentials page or an administrator pastes with
// strazactl.
func (m *Manager) agentFix(inst *instance, c Caller) string {
	if inst.manifest.CredentialKind() == CredentialOAuth {
		ways := "shared or client_credentials"
		if c.SponsorID != "" {
			ways = fmt.Sprintf("sponsor so it runs on %s's sign-in once %s allows it, or to shared or client_credentials", c.Sponsor, c.Sponsor)
		}
		return fmt.Sprintf("An agent cannot sign in through a browser. An administrator sets credential.agents on %s to %s", inst.app.Name, ways)
	}
	admin := fmt.Sprintf("strazactl connect %s --user %s", inst.app.Name, c.User)
	if c.SponsorID == "" {
		return "An administrator sets one with " + admin
	}
	return fmt.Sprintf("Its sponsor %s sets one on %s, or an administrator sets it with %s", c.Sponsor, m.pageRef("their"), admin)
}

// unusableOwnRow is the deny for a row that exists and cannot serve the
// call: expired by the stored date, or sealed under a key the server cannot
// open. sponsor is empty when the row is the caller's own and the sponsor's
// username when an agent rides that row, so the fix names who pastes.
func (m *Manager) unusableOwnRow(inst *instance, sponsor string, row UserCredential) error {
	whose, page := "your", m.pageRef("your")
	noun, again, back := "token", "Paste a new one", "Paste it again"
	if sponsor != "" {
		whose, page = sponsor+"'s", m.pageRef("their")
		again, back = sponsor+" pastes a new one", sponsor+" pastes it again"
	}
	if inst.manifest.CredentialKind() == CredentialOAuth {
		noun, again = "sign-in", "Sign in again"
		if sponsor != "" {
			again = sponsor + " signs in again"
		}
		back = again
	}
	if row.ExpiresAt != nil && !time.Now().Before(*row.ExpiresAt) {
		return fmt.Errorf("%s %s for app %s expired on %s (as it was recorded). %s on %s",
			whose, noun, inst.app.Name, row.ExpiresAt.UTC().Format("2006-01-02"), again, page)
	}
	return fmt.Errorf("%s %s for app %s cannot be opened by this server. %s on %s", whose, noun, inst.app.Name, back, page)
}

// Call invokes one upstream tool with the credential Credential resolved
// (nil for a kind none app). It fails closed: a credentialed app is never
// called with a nil secret. The runtime's error comes back as shown writes
// it, because the gateway answers it to the AI agent.
func (m *Manager) Call(ctx context.Context, appName, tool string, args json.RawMessage, secret *Secret) (*mcp.CallToolResult, error) {
	m.mu.RLock()
	inst, ok := m.byName[appName]
	m.mu.RUnlock()
	if !ok || inst.runtime == nil {
		return nil, notRunning(appName)
	}
	if kind := inst.manifest.CredentialKind(); kind != CredentialNone && secret == nil {
		return nil, m.missingCredential(inst, kind)
	}
	res, err := inst.runtime.Call(ctx, CallInput{Tool: tool, Args: args, Secret: secret})
	return res, shownErr(err)
}

// missingCredential is the actionable error for a credentialed app whose
// credential did not resolve for a caller with no row of their own.
func (m *Manager) missingCredential(inst *instance, kind string) error {
	switch kind {
	case CredentialOAuth:
		return fmt.Errorf("MCP server %s needs your own %s sign-in and you have not connected. Run straza connect %s, or sign in on %s",
			inst.app.Name, oauthProviderOf(inst.manifest), inst.app.Name, m.pageRef("your"))
	case CredentialToken:
		return fmt.Errorf("MCP server %s needs your own token and none is stored for you. Run straza connect %s, or paste one on %s",
			inst.app.Name, inst.app.Name, m.pageRef("your"))
	}
	return fmt.Errorf("app %s requires a credential and none is stored for the server or bound to your roles. An administrator sets one with strazactl apps secret set %s, which asks for the value at a hidden prompt",
		inst.app.Name, inst.app.Name)
}

// oauthProviderOf names the provider for actionable deny reasons; validation
// guarantees it is set on oauth-kind manifests.
func oauthProviderOf(m Manifest) string {
	if m.Straza.Credential != nil && m.Straza.Credential.OAuth != nil {
		return m.Straza.Credential.OAuth.Provider
	}
	return "oauth"
}

// Logs returns recent runtime log lines for an app.
func (m *Manager) Logs(name string, n int) ([]string, bool) {
	m.mu.RLock()
	inst, ok := m.byName[name]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return inst.ring.Last(n), true
}

// LogEntries returns up to n recent runtime log lines with their receive
// times, oldest first; ok is false when no instance carries that name.
func (m *Manager) LogEntries(name string, n int) ([]Entry, bool) {
	m.mu.RLock()
	inst, ok := m.byName[name]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return inst.ring.Entries(n), true
}

func (inst *instance) view() AppView {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	on := inst.manifest.viewsOn()
	exposed := servedTools(filterTools(inst.inventory, inst.manifest.ExposedTools()), inst.views, on)
	return AppView{
		ID: inst.app.ID, Name: inst.app.Name, Version: inst.app.Version,
		Runtime: inst.app.RuntimeKind, Status: inst.status, Detail: inst.detail,
		Source: inst.app.Source, Tools: exposed, Manifest: inst.manifest,
		Offered: inventoryNames(inst.inventory), LastProbe: inst.lastProbe,
		LastHealthy: inst.lastHealthy, StatusSince: inst.statusSince,
		ViewsOn: on, Views: slices.Clone(inst.views),
	}
}

func (m *Manager) appSecret(inst *instance) *Secret {
	if m.opts.Secrets == nil {
		return nil
	}
	return m.opts.Secrets.AppSecret(inst.app.ID)
}

// commandEnvSecret renders the spawn-time env injection for command apps.
func (m *Manager) commandEnvSecret(inst *instance) (EnvVar, bool) {
	mf := inst.manifest
	if mf.CredentialKind() == CredentialNone || mf.Straza.Credential.Inject == nil {
		return EnvVar{}, false
	}
	secret := m.appSecret(inst)
	if secret == nil {
		return EnvVar{}, false
	}
	in := mf.Straza.Credential.Inject
	return EnvVar{Name: in.Name, Value: strings.ReplaceAll(in.Template, SecretPlaceholder, secret.Value)}, true
}

// setStatus records an FSM transition (memory + the row's status column)
// when it changes and reports whether it changed, so a caller can log the
// probe that wrote the reason once per transition instead of once per tick.
// Dead (replaced or removed) instances are ignored so late supervisor
// callbacks cannot clobber their successor's state. The reason is kept as
// shown writes it, because every app answer, the recheck record and the
// drafts check read it.
func (m *Manager) setStatus(ctx context.Context, inst *instance, status, detail string) bool {
	detail = shown(detail)
	inst.mu.Lock()
	if inst.dead || (inst.status == status && inst.detail == detail) {
		inst.mu.Unlock()
		return false
	}
	if inst.status != status {
		inst.statusSince = time.Now()
	}
	inst.status = status
	inst.detail = detail
	inst.mu.Unlock()

	inst.ring.Append(fmt.Sprintf("status → %s %s", status, detail))
	if err := m.opts.Store.Apps().SetStatus(ctx, inst.app.ID, status); err != nil {
		m.opts.Log.Warn("manager: status persist failed", "app", inst.app.Name, "err", err)
	}
	return true
}

func (m *Manager) emit(ctx context.Context, subject string, data map[string]any) {
	if m.opts.Emit != nil {
		m.opts.Emit(ctx, subject, data)
	}
}

// inventoryHash fingerprints a sorted tool list (names, descriptions, and
// schemas) for drift detection. With views on, it also covers each tool's
// view links and each served view's address, document and _meta, each
// appended only when present, so the hash of a server with no links is the
// hash of its tools alone.
func inventoryHash(tools []*mcp.Tool, views []View, on bool) string {
	h := sha256.New()
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema)
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", t.Name, t.Description, schema)
		if !on {
			continue
		}
		for _, k := range []string{uiKey, flatLinkKey} {
			if v, ok := t.Meta[k]; ok {
				b, _ := json.Marshal(v)
				fmt.Fprintf(h, "%s\x00%s\x00", k, b)
			}
		}
	}
	for _, v := range views {
		sum := sha256.Sum256([]byte(v.Text))
		fmt.Fprintf(h, "view\x00%s\x00%x\x00", v.URI, sum)
		if len(v.Meta) > 0 {
			b, _ := json.Marshal(v.Meta)
			fmt.Fprintf(h, "%s\x00", b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func diffTools(prev, next []*mcp.Tool) (added, removed []string) {
	added, removed = []string{}, []string{}
	old := map[string]bool{}
	for _, t := range prev {
		old[t.Name] = true
	}
	seen := map[string]bool{}
	for _, t := range next {
		seen[t.Name] = true
		if !old[t.Name] {
			added = append(added, t.Name)
		}
	}
	for _, t := range prev {
		if !seen[t.Name] {
			removed = append(removed, t.Name)
		}
	}
	return added, removed
}

// inventoryNames lists the names of a tool list, which acceptInventory
// keeps sorted by name.
func inventoryNames(tools []*mcp.Tool) []string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}

// filterTools applies the manifest exposure cap (glob list).
func filterTools(tools []*mcp.Tool, globs []string) []*mcp.Tool {
	out := make([]*mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if MatchAnyGlob(globs, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// MatchAnyGlob reports whether name matches any pattern. Patterns are simple
// name globs: `*` matches any run of characters; everything else is literal.
func MatchAnyGlob(patterns []string, name string) bool {
	for _, p := range patterns {
		if matchGlob(p, name) {
			return true
		}
	}
	return false
}

var globCache sync.Map // pattern → *regexp.Regexp

func matchGlob(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	if re, ok := globCache.Load(pattern); ok {
		return re.(*regexp.Regexp).MatchString(name)
	}
	parts := strings.Split(pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re := regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
	globCache.Store(pattern, re)
	return re.MatchString(name)
}
