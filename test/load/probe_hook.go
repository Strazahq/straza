package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

const hookPolicy = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: load-block-rm }
spec:
  match: { roles: [dev] }
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "STRAZA: destructive delete blocked for role dev"
`

// probeHook measures the budgeted hook end-to-end overhead (spawn the real
// straza binary, ask straza service to decide against its cached signed
// snapshot, exit) for an allowed command, the common path. Budget: p95 <
// 25 ms. Skipped when no binary path is given (CI builds it first).
func probeHook(ctx context.Context, binPath, dataDir, homeDir string, samples int) Probe {
	if binPath == "" {
		return Probe{Name: "hook-e2e", Budget: "p95 < 25ms (spawn→decide→exit)", Observed: "skipped: -straza not set", Pass: true, Skipped: true}
	}
	if _, err := os.Stat(binPath); err != nil { // #nosec G703 -- operator-supplied path to the binary under test
		return failed("hook-e2e", fmt.Errorf("straza binary: %w", err))
	}

	w, err := bootStrazad(ctx, strazadOpts{dataDir: dataDir})
	if err != nil {
		return failed("hook-e2e", err)
	}
	defer w.stop()

	// One human-shaped user (the enroll device flow logs in with a password).
	hash, err := authn.HashPassword("load-hunter2!")
	if err != nil {
		return failed("hook-e2e", err)
	}
	u, err := w.st.Users().Create(ctx, store.User{Username: "hook-user", Email: "hook@load.test", PasswordHash: hash})
	if err != nil {
		return failed("hook-e2e", err)
	}
	// Application kind: match.roles accepts only the roles that carry tools
	// (PolicySet revision 16), and the store defaults a bare role to business.
	role, err := w.st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		return failed("hook-e2e", err)
	}
	if _, err := w.st.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID}); err != nil {
		return failed("hook-e2e", err)
	}
	adminTok, err := w.adminIDToken(ctx)
	if err != nil {
		return failed("hook-e2e", err)
	}
	if err := w.applyPolicy(adminTok, "load-block-rm", hookPolicy); err != nil {
		return failed("hook-e2e", err)
	}

	// The hook reaches the server only through straza service, run here in
	// user mode: STRAZA_HOME puts its socket and state under homeDir, and
	// the service and every hook child inherit it.
	if err := os.Setenv("STRAZA_HOME", homeDir); err != nil {
		return failed("hook-e2e", err)
	}
	stopService, err := startService(ctx, binPath, homeDir)
	if err != nil {
		return failed("hook-e2e", err)
	}
	defer stopService()
	if err := driveEnroll(ctx, localapi.NewClient(homeDir), w.base, "hook-user", "load-hunter2!"); err != nil {
		return failed("hook-e2e", fmt.Errorf("enroll: %w", err))
	}

	env := append(os.Environ(), "CLAUDECODE=1")
	spawn := func(payload string) (int, string, error) {
		cmd := exec.Command(binPath, "hook", "--harness", "claude-code") // #nosec G204 G702 -- operator-supplied path to the binary under test
		cmd.Stdin = strings.NewReader(payload)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		cmd.Env = env
		err := cmd.Run()
		code := cmd.ProcessState.ExitCode()
		if err != nil && code <= 0 {
			return code, out.String() + errb.String(), err
		}
		return code, out.String() + errb.String(), nil
	}

	// session.start once: checkin + snapshot fetch persisted for the run.
	ss := `{"hook_event_name":"SessionStart","session_id":"load-s1","cwd":"/work"}`
	if code, out, err := spawn(ss); err != nil || code != 0 {
		return failed("hook-e2e", fmt.Errorf("session.start: exit %d err %v: %.200s", code, err, out))
	}

	allowPayload := `{"hook_event_name":"PreToolUse","session_id":"load-s1","cwd":"/work","tool_name":"Bash","tool_input":{"command":"git status --porcelain"}}`
	// Warm the OS cache with a few spawns before sampling.
	for i := 0; i < 5; i++ {
		if code, out, err := spawn(allowPayload); err != nil || code != 0 {
			return failed("hook-e2e", fmt.Errorf("warmup: exit %d err %v: %.200s", code, err, out))
		}
	}
	lat := &latencies{ns: make([]int64, 0, samples)}
	for i := 0; i < samples; i++ {
		t0 := time.Now()
		code, out, err := spawn(allowPayload)
		lat.add(time.Since(t0))
		if err != nil || code != 0 {
			return failed("hook-e2e", fmt.Errorf("sample %d: exit %d err %v: %.200s", i, code, err, out))
		}
	}
	// Sanity: the deny path still denies (exit 2), so the measurement cannot
	// have optimized away enforcement.
	deny := `{"hook_event_name":"PreToolUse","session_id":"load-s1","cwd":"/work","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`
	if code, out, _ := spawn(deny); code != 2 {
		return failed("hook-e2e", fmt.Errorf("rm -rf not denied (exit %d): %.200s", code, out))
	}

	p95 := lat.percentile(95)
	return Probe{
		Name:     "hook-e2e",
		Budget:   "p95 < 25ms (spawn→decide→exit)",
		Observed: fmt.Sprintf("p95 = %s (p50 %s, max %s, n=%d)", p95.Round(100*time.Microsecond), lat.percentile(50).Round(100*time.Microsecond), lat.max().Round(time.Millisecond), samples),
		Value:    float64(p95.Microseconds()) / 1000,
		Limit:    25,
		Pass:     p95 < 25*time.Millisecond,
		Detail:   "allowed shell command against the cached signed snapshot; deny path sanity-checked (exit 2)",
	}
}

// startService runs straza service for homeDir and waits until it answers
// on its socket, a refusal included. The returned func stops it.
func startService(ctx context.Context, binPath, homeDir string) (func(), error) {
	cmd := exec.CommandContext(ctx, binPath, "service") // #nosec G204 G702 -- operator-supplied path to the binary under test
	out := &lockedBuf{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start straza service: %w", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	stop := func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	lc := localapi.NewClient(homeDir)
	for i := 0; i < 400; i++ {
		_, err := lc.Status(ctx)
		var refused *localapi.RefusalError
		if err == nil || errors.As(err, &refused) {
			return stop, nil
		}
		select {
		case <-done:
			return nil, fmt.Errorf("straza service exited before it answered on %s: %.300s", lc.Socket(), out.String())
		case <-time.After(25 * time.Millisecond):
		}
	}
	stop()
	return nil, fmt.Errorf("straza service did not answer on %s within 10s: %.300s", lc.Socket(), out.String())
}

// driveEnroll enrolls the person through the service, approving the device
// code the way a browser user would as soon as the service streams it.
func driveEnroll(ctx context.Context, lc *localapi.Client, base, username, password string) error {
	ectx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var approveErr error
	_, err := lc.Enroll(ectx, localapi.EnrollRequest{ServerURL: base}, func(ev localapi.EnrollEvent) {
		if ev.Event != localapi.EnrollCode || approveErr != nil {
			return
		}
		resp, err := http.PostForm(base+"/oidc/device", url.Values{
			"user_code": {ev.UserCode}, "username": {username}, "password": {password},
		})
		if err != nil {
			approveErr = err
			cancel()
			return
		}
		_ = resp.Body.Close()
	})
	if approveErr != nil {
		return fmt.Errorf("approve the device code: %w", approveErr)
	}
	return err
}

type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
