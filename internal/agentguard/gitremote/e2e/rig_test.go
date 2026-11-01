package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/gitremote"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/agentguard/service"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/server"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// window is the Claude Code session id the rig's git runs carry.
const window = "e2e-window-1"

// rig is one machine: the stand-in git host, the fake resource server, a
// real strazad behind a proxy that answers the git grant routes, a
// user-mode service with kim enrolled and one window open, and a PATH that
// makes this test binary git-remote-straza.
type rig struct {
	t         *testing.T
	root      string
	home      string // STRAZA_HOME of the user-mode service
	bin       string // holds the git-remote-straza symlink
	gitHome   string // HOME of every git run
	hosts     string // the host file with protocol.straza.allow
	hostsBare string // the same rewrite without protocol.straza.allow
	upstream  string // http://127.0.0.1:<port>, the stand-in git host
	repos     string // bare repos the resource server serves, by owner/name.git
	public    string // bare repos only the git host serves
	grants    *grantServer
	rs        *resourceServer
	logs      *syncBuffer

	mu      sync.Mutex
	outputs []string
}

// syncBuffer is a bytes.Buffer safe for the service's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if testing.Short() {
		t.Skip("boots a real strazad and runs real git")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the user-mode service and the helper's git route are Linux-only in this version")
	}
	requireGit(t)
	root := t.TempDir()
	r := &rig{t: t, root: root, home: filepath.Join(root, "straza"), bin: filepath.Join(root, "bin"),
		gitHome: filepath.Join(root, "githome"), repos: filepath.Join(root, "repos"), public: filepath.Join(root, "public"),
		logs: &syncBuffer{}}
	for _, d := range []string{r.home, r.bin, r.gitHome, r.repos, r.public} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("STRAZA_SYSTEM", filepath.Join(root, "system"))
	t.Setenv("STRAZA_HOME", r.home)
	if socket, _, _ := localapi.Paths(r.home); agentguard.NewServiceClient().Socket() != socket {
		t.Skip("this machine runs a system or managed Straza service, which the helper would call instead of the rig's")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(r.bin, gitremote.Name)); err != nil {
		t.Fatal(err)
	}

	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		repo, tail, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), ".git/")
		svc := tail
		if tail == "info/refs" {
			svc = req.URL.Query().Get("service")
		}
		serveGit(w, req, filepath.Join(r.public, repo+".git"), svc, tail == "info/refs", req.Body)
	}))
	t.Cleanup(host.Close)
	r.upstream = host.URL

	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x47}, ed25519.SeedSize))
	r.rs = &resourceServer{pub: key.Public().(ed25519.PublicKey), repos: r.repos, jtis: map[string]bool{}, spent: map[string]bool{}}
	rs := httptest.NewServer(r.rs)
	t.Cleanup(rs.Close)
	r.rs.base = rs.URL

	var strazad atomic.Pointer[httputil.ReverseProxy]
	r.grants = &grantServer{key: key, aud: rs.URL, upstream: r.upstream, holdFor: 7 * time.Second, held: map[string]time.Time{},
		repos: map[string]string{"acme/pay-api": "acme/pay-api", "acme/super": "acme/super"}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/grants/resolve", r.grants.resolve)
	mux.HandleFunc("POST /v1/grants", r.grants.grant)
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) { strazad.Load().ServeHTTP(w, req) })
	front := httptest.NewServer(mux)
	t.Cleanup(front.Close)
	r.grants.iss = front.URL
	strazad.Store(startStrazad(t, front.URL))
	r.startService()
	r.writeHosts()
	return r
}

// requireGit skips the test without git 2.31 or later, the first git that
// reads GIT_CONFIG_COUNT.
func requireGit(t *testing.T) {
	t.Helper()
	out, err := exec.Command("git", "version").Output()
	if err != nil {
		t.Skipf("git is not installed: %v", err)
	}
	m := regexp.MustCompile(`git version (\d+)\.(\d+)`).FindSubmatch(out)
	if m == nil {
		t.Skipf("cannot read the git version from %q", out)
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	if major < 2 || major == 2 && minor < 31 {
		t.Skipf("git %s.%s predates GIT_CONFIG_COUNT", m[1], m[2])
	}
}

// startStrazad boots a real standalone strazad whose public URL is the
// rig's proxy, seeds kim with the dev role, and returns the proxy to it.
func startStrazad(t *testing.T, public string) *httputil.ReverseProxy {
	t.Helper()
	dir := t.TempDir()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	cfg := config.Config{
		Profile: config.ProfileStandalone, DataDir: dir,
		Server: config.Server{Listen: addr, PublicURL: public},
		Log:    config.Log{Level: "error", Format: "json"},
		Store:  config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events: config.Events{Embedded: true},
		Governance: config.Governance{OfflineGraceTTL: 15 * time.Minute, LocalToolDefault: config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop},
	}
	storetest.SeedSQLite(t, cfg.SQLitePath())
	ctx, cancel := context.WithCancel(context.Background())
	app, err := server.New(ctx, cfg, logging.New(cfg.Log, &bytes.Buffer{}))
	if err != nil {
		cancel()
		t.Fatalf("server.New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("strazad shutdown timed out")
		}
	})
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	bg := context.Background()
	hash, _ := storetest.PasswordHash("hunter2!")
	kim, err := st.Users().Create(bg, store.User{Username: "kim", Email: "kim@x.io", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := st.Roles().Create(bg, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if _, err := st.Roles().Assign(bg, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: kim.ID, RoleID: dev.ID}); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("http://" + addr)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/version")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return rp
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("strazad at %s never answered /version: %v", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// startService runs the user-mode service in this process, enrolls kim by
// device code and opens the rig's window.
func (r *rig) startService() {
	t := r.t
	socket, state, _ := localapi.Paths(r.home)
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- service.Run(ctx, service.Options{Socket: socket, StateDir: state, UserMode: true,
			Log: slog.New(slog.NewJSONHandler(r.logs, nil)), Ready: func() { close(ready) }})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("the service did not stop")
		}
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("the service stopped before it listened: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the service never listened")
	}
	c := localapi.NewClient(r.home)
	ectx, ecancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer ecancel()
	res, err := c.Enroll(ectx, localapi.EnrollRequest{ServerURL: r.grants.iss}, func(ev localapi.EnrollEvent) {
		if ev.Event == localapi.EnrollCode {
			resp, err := http.PostForm(r.grants.iss+"/oidc/device", url.Values{"user_code": {ev.UserCode},
				"username": {"kim"}, "password": {"hunter2!"}})
			if err == nil {
				_ = resp.Body.Close()
			}
		}
	})
	if err != nil || res.Username != "kim" {
		t.Fatalf("enroll: %+v, %v", res, err)
	}
	if _, err := c.StartSession(ectx, localapi.SessionRequest{Harness: localapi.Harness{Name: "claude-code",
		Version: "2.1.0", Session: window}}); err != nil {
		t.Fatalf("start the window: %v", err)
	}
}

// writeHosts writes the two host files in the form `straza service`
// writes, for the stand-in git host.
func (r *rig) writeHosts() {
	rewrite := "[url \"straza::" + r.upstream + "/\"]\n\tinsteadOf = " + r.upstream + "/\n"
	r.hosts, r.hostsBare = filepath.Join(r.root, "hosts.conf"), filepath.Join(r.root, "hosts-bare.conf")
	for file, body := range map[string]string{r.hosts: rewrite + "[protocol \"straza\"]\n\tallow = always\n", r.hostsBare: rewrite} {
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
}

// env is the environment of a git run inside an agent window, with hosts
// as the included host file.
func (r *rig) env(hosts string) []string {
	return []string{
		"PATH=" + r.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + r.gitHome, "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=include.path", "GIT_CONFIG_VALUE_0=" + hosts,
		"STRAZA_HOME=" + r.home, "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=" + window,
		"GIT_AUTHOR_NAME=kim", "GIT_AUTHOR_EMAIL=kim@x.io", "GIT_COMMITTER_NAME=kim", "GIT_COMMITTER_EMAIL=kim@x.io",
	}
}

// gitResult is one finished git run.
type gitResult struct {
	stdout, stderr string
	err            error
	took           time.Duration
	firstWait      time.Duration // when the first Straza wait line reached stderr, 0 for none
}

// git runs git in dir with env and a deadline, keeps its output for the
// secrets row, and notes when the first wait line arrived on stderr.
func (r *rig) git(dir string, env []string, deadline time.Duration, args ...string) gitResult {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir, cmd.Env = dir, env
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	pipe, err := cmd.StderrPipe()
	if err != nil {
		r.t.Fatal(err)
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	var res gitResult
	var stderr strings.Builder
	sc := bufio.NewScanner(pipe)
	for sc.Scan() {
		line := sc.Text()
		if res.firstWait == 0 && strings.Contains(line, "waiting for an approver") {
			res.firstWait = time.Since(start)
		}
		stderr.WriteString(line + "\n")
	}
	res.err = cmd.Wait()
	res.took = time.Since(start)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.err = errors.New("git ran past its deadline of " + deadline.String())
	}
	res.stdout, res.stderr = stdout.String(), stderr.String()
	r.mu.Lock()
	r.outputs = append(r.outputs, res.stdout, res.stderr)
	r.mu.Unlock()
	return res
}

// plain runs git with no Straza wiring, for seeding the repos, and fails
// the test on an error.
func (r *rig) plain(dir string, args ...string) string {
	r.t.Helper()
	out, err := r.plainCmd(dir, args...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// plainCmd is the git command plain runs.
func (r *rig) plainCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + r.gitHome, "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=seed", "GIT_AUTHOR_EMAIL=seed@x.io", "GIT_COMMITTER_NAME=seed", "GIT_COMMITTER_EMAIL=seed@x.io"}
	return cmd
}

// bare makes the bare repo under base/<repo>.git with main as its head and
// partial clone allowed, and returns its path.
func (r *rig) bare(base, repo string) string {
	dir := filepath.Join(base, repo+".git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		r.t.Fatal(err)
	}
	r.plain(base, "init", "-q", "--bare", "--initial-branch=main", dir)
	r.plain(dir, "config", "uploadpack.allowFilter", "true")
	r.plain(dir, "config", "uploadpack.allowAnySHA1InWant", "true")
	return dir
}

// commit makes a commit in the work tree dir that writes name with body,
// pushes it to the bare repo at dst as main when dst is set, and returns
// its id.
func (r *rig) commit(dir, name, body, dst string) string {
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
	r.plain(dir, "add", name)
	r.plain(dir, "commit", "-q", "-m", "write "+name)
	if dst != "" {
		r.plain(dir, "push", "-q", dst, "HEAD:refs/heads/main")
	}
	return r.plain(dir, "rev-parse", "HEAD")
}

// seed makes the rig's repos: acme/pay-api and acme/super, whose lib
// submodule is pay-api through the git host's URL, behind the resource
// server, and acme/public-lib on the git host alone. It returns pay-api's
// seed work tree.
func (r *rig) seed() string {
	pay := r.bare(r.repos, "acme/pay-api")
	work := filepath.Join(r.root, "seed-pay")
	r.plain(r.root, "init", "-q", "--initial-branch=main", work)
	head := r.commit(work, "README", "pay-api\n", pay)

	super := r.bare(r.repos, "acme/super")
	sw := filepath.Join(r.root, "seed-super")
	r.plain(r.root, "init", "-q", "--initial-branch=main", sw)
	gitmodules := "[submodule \"lib\"]\n\tpath = lib\n\turl = " + r.upstream + "/acme/pay-api.git\n"
	if err := os.WriteFile(filepath.Join(sw, ".gitmodules"), []byte(gitmodules), 0o600); err != nil {
		r.t.Fatal(err)
	}
	r.plain(sw, "add", ".gitmodules")
	r.plain(sw, "update-index", "--add", "--cacheinfo", "160000,"+head+",lib")
	r.commit(sw, "SUPER", "super\n", super)

	pub := r.bare(r.public, "acme/public-lib")
	pw := filepath.Join(r.root, "seed-public")
	r.plain(r.root, "init", "-q", "--initial-branch=main", pw)
	r.commit(pw, "LIB", "public\n", pub)
	return work
}
