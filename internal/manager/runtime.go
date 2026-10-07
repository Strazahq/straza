package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/version"
)

// Runtime is the seam every app runtime implements. The gateway calls
// Tools/Call/Ping; the manager drives Start/Stop and maps readiness onto the
// app FSM. Implementations MUST fail closed: any call while the upstream is
// unreachable returns an error, never blocks forever.
type Runtime interface {
	// Start begins serving (spawning/supervising as the kind requires) and
	// returns without waiting for the upstream to become ready; readiness is
	// reported through the OnState callback and Ready.
	Start(ctx context.Context) error
	// Stop terminates the runtime and releases resources. Idempotent.
	Stop()
	// Ready reports whether a call made now could reach the upstream.
	Ready() bool
	// Tools fetches the live upstream tool inventory. secret carries the
	// broker-resolved app-level credential for runtimes that authenticate
	// per request (remote); spawn-injected runtimes ignore it.
	Tools(ctx context.Context, secret *Secret) ([]*mcp.Tool, error)
	// Call invokes one upstream tool.
	Call(ctx context.Context, in CallInput) (*mcp.CallToolResult, error)
	// Resources lists the upstream's resources and ReadResource reads one,
	// with the same credential Tools takes, for the views a tool links.
	Resources(ctx context.Context, secret *Secret) ([]*mcp.Resource, error)
	ReadResource(ctx context.Context, secret *Secret, uri string) (*mcp.ReadResourceResult, error)
	// Ping checks upstream liveness.
	Ping(ctx context.Context, secret *Secret) error
	// Logs returns up to n recent runtime log lines, oldest first.
	Logs(n int) []string
}

// Secret is a broker-resolved credential. The plaintext exists only in
// gateway/runtime memory and in the upstream request, never in any
// client-visible payload or log.
type Secret struct {
	// ID identifies the credential row; safe to use as a pool key.
	ID string
	// Value is the decrypted secret material.
	Value string
}

// CallInput is one governed upstream tool invocation.
type CallInput struct {
	// Tool is the upstream (un-namespaced) tool name.
	Tool string
	// Args is the raw JSON arguments object, passed through verbatim.
	Args json.RawMessage
	// Secret is the broker-resolved credential for this call (nil = none).
	Secret *Secret
}

// ErrNotReady is returned by calls while the upstream is down (fail
// closed). The supervisor keeps reconnecting in the background.
var ErrNotReady = errors.New("the MCP server is not running")

// notRunning refuses a call to the MCP server name while it has no live
// connection, wrapping ErrNotReady. An agent reads it after the gateway's
// "upstream call failed: ", so it names the server and where an
// administrator reads the reason.
func notRunning(name string) error {
	return fmt.Errorf("%s was not called because %w. An administrator reads the reason with strazactl apps show %s", name, ErrNotReady, name)
}

func clientImpl() *mcp.Implementation {
	return &mcp.Implementation{Name: "straza-gateway", Version: version.Version}
}

// newClient builds the upstream MCP client. With views off its initialize
// carries the SDK's default capabilities, roots with listChanged. With views
// on it carries the same plus the MCP Apps extension, so the server links
// its tools to views. Setting capabilities replaces the SDK's default, which
// is why roots is spelled out.
func newClient(views bool) *mcp.Client {
	if !views {
		return mcp.NewClient(clientImpl(), nil)
	}
	caps := &mcp.ClientCapabilities{RootsV2: &mcp.RootCapabilities{ListChanged: true}}
	caps.AddExtension(viewsExtension, map[string]any{"mimeTypes": []string{ViewMIMEType}})
	return mcp.NewClient(clientImpl(), &mcp.ClientOptions{Capabilities: caps})
}

// CommandRuntime runs a child process speaking MCP over stdio, with
// crash supervision and exponential backoff. The child's environment is
// PATH, HOME and LANG from strazad's own, the parent names under
// envPrefixes, the manifest env and the broker-resolved credential, and
// nothing else, so a secret strazad reads from its environment never
// reaches the child.
type CommandRuntime struct {
	// ConnectTimeout bounds each connect handshake (default 15 s).
	ConnectTimeout time.Duration
	// BackoffBase/BackoffMax shape the restart backoff (default 250 ms / 30 s).
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// OnState, when set, is called with readiness transitions.
	OnState func(up bool, detail string)
	// Views makes the upstream initialize advertise the MCP Apps extension
	// (straza.exposure.views). Set it before Start.
	Views bool

	name     string
	spec     CommandSpec
	extraEnv []EnvVar
	ring     *Ring
	// preSpawn runs before every spawn of the child; the oci runtime uses
	// it to clear the cidfile docker refuses to overwrite.
	preSpawn func()
	// envPrefixes names the parent environment prefixes the child also
	// receives; the oci runtime sets DOCKER_ for the docker CLI.
	envPrefixes []string

	sess     atomic.Pointer[mcp.ClientSession]
	restarts atomic.Int64
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewCommandRuntime builds a command runtime from a manifest spec. extraEnv
// carries broker-resolved injections.
func NewCommandRuntime(name string, spec CommandSpec, extraEnv []EnvVar, ring *Ring) *CommandRuntime {
	if ring == nil {
		ring = NewRing(256)
	}
	return &CommandRuntime{
		ConnectTimeout: 15 * time.Second,
		BackoffBase:    250 * time.Millisecond,
		BackoffMax:     30 * time.Second,
		name:           name,
		spec:           spec,
		extraEnv:       extraEnv,
		ring:           ring,
	}
}

// Restarts reports how many reconnect attempts followed the initial connect.
func (r *CommandRuntime) Restarts() int64 { return r.restarts.Load() }

// Start launches the supervision loop, which restarts with backoff.
func (r *CommandRuntime) Start(ctx context.Context) error {
	if r.done != nil {
		return fmt.Errorf("manager: app %s already started", r.name)
	}
	ctx, r.cancel = context.WithCancel(ctx)
	r.done = make(chan struct{})
	go r.supervise(ctx)
	return nil
}

func (r *CommandRuntime) supervise(ctx context.Context) {
	defer close(r.done)
	backoff := r.BackoffBase
	for {
		if ctx.Err() != nil {
			return
		}
		sess, err := r.connect(ctx)
		if err != nil {
			r.state(false, fmt.Sprintf("connect failed: %v", err))
			r.restarts.Add(1)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, r.BackoffMax)
			continue
		}
		r.sess.Store(sess)
		r.state(true, "connected")
		up := time.Now()

		err = sess.Wait() // returns when the child dies or the session closes
		r.sess.Store(nil)
		if ctx.Err() != nil {
			_ = sess.Close()
			return
		}
		r.state(false, fmt.Sprintf("upstream exited: %v", err))
		r.restarts.Add(1)
		// A session that stayed healthy for a while earns a fresh backoff;
		// a crash loop keeps growing it.
		if time.Since(up) > 10*r.BackoffBase {
			backoff = r.BackoffBase
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, r.BackoffMax)
	}
}

func (r *CommandRuntime) connect(ctx context.Context) (*mcp.ClientSession, error) {
	if r.preSpawn != nil {
		r.preSpawn()
	}
	cmd := exec.Command(r.spec.Exec, r.spec.Args...) // #nosec G204 -- the manifest is operator-supplied config
	cmd.Dir = r.spec.Workdir
	cmd.Env = childEnv(os.Environ(), r.envPrefixes, r.spec.Env, r.extraEnv)
	cmd.Stderr = r.ring

	cctx, cancel := context.WithTimeout(ctx, r.ConnectTimeout)
	defer cancel()
	// The session outlives cctx: CommandTransport connections live on the
	// process pipes, and the SDK detaches session lifetime from the connect
	// context.
	return newClient(r.Views).Connect(cctx, &mcp.CommandTransport{Command: cmd}, nil)
}

// childEnv builds the environment a child receives from the parent entries:
// PATH, HOME and LANG when the parent sets them, every parent entry whose
// name starts with one of prefixes, then manifest and extra in that order.
// os/exec keeps the last value of a repeated name, so a later entry wins.
func childEnv(parent []string, prefixes []string, manifest, extra []EnvVar) []string {
	var out []string
	for _, name := range []string{"PATH", "HOME", "LANG"} {
		for _, kv := range parent {
			if strings.HasPrefix(kv, name+"=") {
				out = append(out, kv)
				break
			}
		}
	}
	for _, kv := range parent {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				out = append(out, kv)
				break
			}
		}
	}
	for _, e := range manifest {
		out = append(out, e.Name+"="+e.Value)
	}
	for _, e := range extra {
		out = append(out, e.Name+"="+e.Value)
	}
	return out
}

// Stop cancels supervision and closes the child (SDK: stdin close, then
// terminate).
func (r *CommandRuntime) Stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	if s := r.sess.Swap(nil); s != nil {
		_ = s.Close()
	}
	<-r.done
}

// Ready reports whether a session is live.
func (r *CommandRuntime) Ready() bool { return r.sess.Load() != nil }

func (r *CommandRuntime) session() (*mcp.ClientSession, error) {
	if s := r.sess.Load(); s != nil {
		return s, nil
	}
	return nil, notRunning(r.name)
}

// Tools lists the live upstream inventory. The secret is ignored: command
// runtimes receive their credential as spawn-time environment.
func (r *CommandRuntime) Tools(ctx context.Context, _ *Secret) ([]*mcp.Tool, error) {
	s, err := r.session()
	if err != nil {
		return nil, err
	}
	return listTools(ctx, s)
}

// Resources lists the upstream's resources. The secret is ignored, as Tools
// ignores it.
func (r *CommandRuntime) Resources(ctx context.Context, _ *Secret) ([]*mcp.Resource, error) {
	s, err := r.session()
	if err != nil {
		return nil, err
	}
	return listResources(ctx, s)
}

// ReadResource reads one upstream resource. The secret is ignored, as Tools
// ignores it.
func (r *CommandRuntime) ReadResource(ctx context.Context, _ *Secret, uri string) (*mcp.ReadResourceResult, error) {
	s, err := r.session()
	if err != nil {
		return nil, err
	}
	return s.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
}

// Call invokes an upstream tool. in.Secret is ignored (spawn-time env
// injection).
func (r *CommandRuntime) Call(ctx context.Context, in CallInput) (*mcp.CallToolResult, error) {
	s, err := r.session()
	if err != nil {
		return nil, err
	}
	return callTool(ctx, s, in.Tool, in.Args)
}

// Ping checks child liveness over the session.
func (r *CommandRuntime) Ping(ctx context.Context, _ *Secret) error {
	s, err := r.session()
	if err != nil {
		return err
	}
	return s.Ping(ctx, nil)
}

// Logs returns recent child stderr lines.
func (r *CommandRuntime) Logs(n int) []string { return r.ring.Last(n) }

func (r *CommandRuntime) state(up bool, detail string) {
	if r.OnState != nil {
		r.OnState(up, detail)
	}
}

// listTools collects the full (paginated) tool list of a session.
func listTools(ctx context.Context, s *mcp.ClientSession) ([]*mcp.Tool, error) {
	var out []*mcp.Tool
	for t, err := range s.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// listResources collects the full (paginated) resource list of a session.
func listResources(ctx context.Context, s *mcp.ClientSession) ([]*mcp.Resource, error) {
	var out []*mcp.Resource
	for r, err := range s.Resources(ctx, nil) {
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// callTool invokes one tool passing raw JSON arguments through verbatim.
func callTool(ctx context.Context, s *mcp.ClientSession, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	return s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: json.RawMessage(orEmptyArgs(args))})
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
