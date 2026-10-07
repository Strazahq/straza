package e2ematrix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// run is one whole e2e-matrix run: the built binaries, every timer and
// every report row. Each scenario-and-dialect run boots its own strazad and
// upstream, so the servers live on the scenarioRun, not here.
type run struct {
	root    string
	pkgDir  string
	work    string
	bin     string
	version string
	timers  []timerRow
	hooks   map[string][]time.Duration
	fatal   string
	rows    []resultRow
	logf    func(string, ...any)
}

type timerRow struct {
	name    string
	dialect string
	value   time.Duration
}

type resultRow struct {
	scenario string
	dialect  string
	pass     bool
	detail   string
}

func (r *run) timer(name, dialect string, d time.Duration) {
	r.timers = append(r.timers, timerRow{name: name, dialect: dialect, value: d})
}

// hookTime files one hook's spawn-to-exit time: a session.start under the
// session-start timer, a deciding event (tool.pre, tool.post,
// prompt.submit, or a raw payload) under hook-overhead, and a session.end
// under neither.
func (r *run) hookTime(dialect, kind string, d time.Duration) {
	switch kind {
	case "session.start":
		r.timer("session-start", dialect, d)
	case "session.end":
	default:
		r.hooks[dialect] = append(r.hooks[dialect], d)
	}
}

// stack is one boot: a strazad on a fresh data directory, signed in with
// the IdM token minted, and its own upstream.
type stack struct {
	server *strazad
	up     *upstream
}

// stop ends both processes by process id.
func (st *stack) stop() {
	if st.up != nil {
		st.up.stop()
	}
	if st.server != nil {
		st.server.stop()
	}
}

// boot runs steps 2 to 4 of the README under dir and records the cold-start.
// governance is the scenario's lifetimes for the server, nil for the defaults.
func (r *run) boot(ctx context.Context, dir string, governance map[string]string) (*stack, error) {
	server, err := newStrazad(filepath.Join(r.bin, "strazad"), dir, governance)
	if err != nil {
		return nil, err
	}
	st := &stack{server: server}
	if err := server.start(ctx); err != nil {
		st.stop()
		return nil, fmt.Errorf("boot: %w", err)
	}
	r.timer("cold-start", "-", server.coldStart)
	if err := server.login(ctx); err != nil {
		st.stop()
		return nil, fmt.Errorf("admin sign-in: %w", err)
	}
	if err := server.mintIdM(ctx); err != nil {
		st.stop()
		return nil, fmt.Errorf("IdM token: %w", err)
	}
	up, err := startUpstream()
	if err != nil {
		st.stop()
		return nil, fmt.Errorf("upstream: %w", err)
	}
	st.up = up
	return st, nil
}

// runJourneys is the boot-and-run test body: build, boot for the positive
// control, then one fresh boot per scenario and dialect, and the report.
func runJourneys(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	r := &run{pkgDir: pkgDir, root: filepath.Clean(filepath.Join(pkgDir, "..", "..")),
		work: filepath.Join(pkgDir, ".work"), hooks: map[string][]time.Duration{}, logf: t.Logf}
	r.bin = filepath.Join(r.work, "bin")
	defer func() {
		if err := r.writeReport(filepath.Join(r.work, "report.md")); err != nil {
			t.Errorf("write report: %v", err)
		}
	}()
	fatal := func(format string, v ...any) {
		r.fatal = redact(fmt.Sprintf(format, v...))
		t.Fatal(r.fatal)
	}

	corpus, err := loadCorpus(filepath.Join(pkgDir, "corpus"))
	if err != nil {
		fatal("%v", err)
	}
	corpus, err = selectScenarios(corpus, os.Getenv("STRAZA_E2E_SCENARIOS"))
	if err != nil {
		fatal("%v", err)
	}
	if err := r.build(ctx); err != nil {
		fatal("build: %v", err)
	}

	if union := harnessUnion(corpus); len(union) > 0 {
		dir := t.TempDir()
		st, err := r.boot(ctx, filepath.Join(dir, "strazad"), nil)
		if err != nil {
			fatal("%v", err)
		}
		t.Cleanup(st.stop)
		if err := r.control(ctx, st, filepath.Join(dir, "control"), union); err != nil {
			fatal("positive control: %v", err)
		}
		st.stop()
	}
	for i := range corpus {
		for _, h := range runDialects(&corpus[i]) {
			r.runScenario(ctx, t, &corpus[i], h)
		}
	}
	for _, row := range r.rows {
		if !row.pass {
			t.Errorf("%s [%s]: %s", row.scenario, row.dialect, row.detail)
		}
	}
}

// runDialects lists the per-harness runs a scenario makes: one per listed
// harness, or a single dash run for a scenario that lists none.
func runDialects(sc *scenario) []string {
	if len(sc.Harnesses) == 0 {
		return []string{"-"}
	}
	return sc.Harnesses
}

// selectScenarios keeps the ids STRAZA_E2E_SCENARIOS names, or every one.
func selectScenarios(corpus []scenario, sel string) ([]scenario, error) {
	if strings.TrimSpace(sel) == "" {
		return corpus, nil
	}
	byID := map[string]scenario{}
	var known []string
	for _, sc := range corpus {
		byID[sc.ID] = sc
		known = append(known, sc.ID)
	}
	var out []scenario
	for _, id := range strings.Split(sel, ",") {
		id = strings.TrimSpace(id)
		sc, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("STRAZA_E2E_SCENARIOS names %q, which is not in the corpus (%s)", id, strings.Join(known, ", "))
		}
		out = append(out, sc)
	}
	return out, nil
}

// harnessUnion lists every dialect any scenario runs under.
func harnessUnion(corpus []scenario) []string {
	var out []string
	for _, d := range dialects {
		for _, sc := range corpus {
			if contains(sc.Harnesses, d) {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

// build compiles strazad and straza from the tree into .work/bin the way a
// release is built: CGO_ENABLED=0, trimpath, version and commit from git.
func (r *run) build(ctx context.Context) error {
	version, err := gitOut(ctx, r.root, "describe", "--tags", "--always", "--dirty")
	if err != nil {
		version = "unknown"
	}
	commit, err := gitOut(ctx, r.root, "rev-parse", "--short", "HEAD")
	if err != nil {
		commit = "none"
	}
	r.version = version
	if err := os.MkdirAll(r.bin, 0o750); err != nil {
		return err
	}
	ldflags := "-s -w -X github.com/strazahq/straza/internal/version.Version=" + version +
		" -X github.com/strazahq/straza/internal/version.Commit=" + commit
	for _, name := range []string{"strazad", "straza"} {
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", ldflags,
			"-o", filepath.Join(r.bin, name), "./cmd/"+name) // #nosec G204 -- fixed build line over this tree
		cmd.Dir = r.root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go build ./cmd/%s: %v: %s", name, err, trim(string(out)))
		}
	}
	return nil
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- read-only git queries
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// controlCommand is the shell.exec the control PolicySet denies.
const controlCommand = "straza-e2e-control-probe --run"

// control proves the lane can see a deny before any scenario counts: on the
// first boot, a control NHI holding the e2e-control role, the control
// PolicySet active, and the control command denied through the real straza
// hook on every dialect the corpus uses.
func (r *run) control(ctx context.Context, st *stack, dir string, union []string) error {
	var user struct {
		ID string `json:"id"`
	}
	if err := adminCreate(ctx, st.server, "/v1/admin/users", map[string]any{
		"username": "e2e-control", "display": "e2e-matrix positive control", "kind": "nhi"}, &user); err != nil {
		return err
	}
	var role struct {
		ID string `json:"id"`
	}
	if err := adminCreate(ctx, st.server, "/v1/admin/roles", map[string]any{"name": "e2e-control", "kind": "application"}, &role); err != nil {
		return err
	}
	if err := adminCreate(ctx, st.server, "/v1/admin/assignments", map[string]any{
		"subject_kind": "user", "subject_id": user.ID, "role_id": role.ID}, nil); err != nil {
		return err
	}
	doc, err := os.ReadFile(filepath.Join(r.pkgDir, "control.yaml")) // #nosec G304 -- this package's control PolicySet
	if err != nil {
		return err
	}
	code, body, err := st.server.adminYAML(ctx, http.MethodPut, "/v1/admin/policies", doc)
	if err != nil {
		return err
	}
	if code != http.StatusCreated && code != http.StatusOK {
		return fmt.Errorf("apply control.yaml: status %d: %s", code, trim(string(body)))
	}
	code, body, err = st.server.adminJSON(ctx, http.MethodPost, "/v1/admin/policies/e2e-matrix-control/activate", map[string]any{"status": "active"})
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("activate control PolicySet: status %d: %s", code, trim(string(body)))
	}
	c, err := newClient("e2e-control", identity{Kind: "nhi"}, filepath.Join(r.bin, "straza"), dir, st.server)
	if err != nil {
		return err
	}
	defer c.cleanup()
	if err := c.enroll(ctx); err != nil {
		return err
	}
	for _, d := range union {
		if err := controlDialect(ctx, c, d); err != nil {
			return fmt.Errorf("dialect %s: %w", d, err)
		}
		r.logf("positive control: %s denies the control command", d)
	}
	return nil
}

func controlDialect(ctx context.Context, c *client, dialect string) error {
	start, err := renderPayload(dialect, event{Kind: "session.start"}, "e2e-control-"+dialect)
	if err != nil {
		return err
	}
	o, err := c.hook(ctx, dialect, mustJSON(start))
	if err != nil {
		return err
	}
	if ok, detail := judgeHook(dialect, map[string]any{"decision": "allow"}, o); !ok {
		return fmt.Errorf("session.start: %s", detail)
	}
	probe, err := renderPayload(dialect, event{Kind: "tool.pre", Tool: "shell.exec", Command: controlCommand}, "e2e-control-"+dialect)
	if err != nil {
		return err
	}
	o, err = c.hook(ctx, dialect, mustJSON(probe))
	if err != nil {
		return err
	}
	if ok, detail := judgeHook(dialect, map[string]any{"decision": "deny", "reasonContains": "positive control"}, o); !ok {
		return fmt.Errorf("control command not denied: %s", detail)
	}
	return nil
}

// adminCreate posts to the admin API and requires 201, decoding into out.
func adminCreate(ctx context.Context, server *strazad, path string, body any, out any) error {
	code, resp, err := server.adminJSON(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if code != http.StatusCreated {
		return fmt.Errorf("POST %s: status %d: %s", path, code, trim(redact(string(resp))))
	}
	if out != nil {
		return json.Unmarshal(resp, out)
	}
	return nil
}

// runScenario runs one scenario under one harness on its own freshly
// booted strazad and upstream, with a fresh STRAZA_HOME per identity, then
// records the row for that harness.
func (r *run) runScenario(ctx context.Context, t *testing.T, sc *scenario, harness string) {
	t.Helper()
	r.logf("scenario %s [%s]: %s", sc.ID, harness, sc.Title)
	s := &scenarioRun{r: r, sc: sc, harness: harness, dir: t.TempDir(),
		clients: map[string]*client{}, tokens: map[string]string{}, approvers: map[string]*approverDevice{}}
	defer s.cleanup()
	st, err := r.boot(ctx, filepath.Join(s.dir, "strazad"), sc.Governance)
	if err != nil {
		s.fail("boot", err.Error())
	} else {
		s.stack = st
		s.vars = map[string]string{"server": st.server.URL, "upstream": st.up.URL, "bin": r.bin}
		for name, id := range sc.Identities {
			c, err := newClient(name, id, filepath.Join(r.bin, "straza"), s.dir, st.server)
			if err != nil {
				s.fail("setup", err.Error())
				break
			}
			s.clients[name] = c
		}
	}
	if !s.stopped {
		seq, err := s.stack.server.lastAuditSeq(ctx)
		if err != nil {
			s.fail("setup", err.Error())
		}
		s.auditSeq = seq
	}
	s.lastEnd = time.Now()
	for i := range sc.Steps {
		if s.stopped {
			break
		}
		s.runStep(ctx, &sc.Steps[i])
	}
	r.rows = append(r.rows, resultRow{scenario: sc.ID, dialect: harness, pass: !s.stopped, detail: s.failure})
}

// cleanup stops the identities' daemons, the run's upstream and strazad by
// process id, and removes the identity homes.
func (s *scenarioRun) cleanup() {
	for _, c := range s.clients {
		c.cleanup()
	}
	if s.stack != nil {
		s.stack.stop()
	}
}

// sortedKeys returns a map's keys in order, for stable output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mustJSON encodes a payload the renderers built.
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}
