package manager

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// Docker backend for the oci runtime. The container speaks MCP over
// stdio (registry `transport: stdio` for oci packages), so the supervised
// CommandRuntime machinery (spawn, restart-with-backoff, log ring) is
// reused with `docker run -i` as the process.
//
// Sandbox defaults: read-only rootfs, all capabilities dropped, no new
// privileges, no host mounts (none are ever added), and no network. These
// are applied when oci.sandbox=default. sandbox=none runs the image with
// docker's own defaults (the operator's explicit choice).

// DefaultDockerBin is the docker CLI used by the oci backend.
var DefaultDockerBin = "docker"

// ociNoDockerDetail is the degraded detail of an oci app on a host without
// docker: what failed, why, and what to do next.
const ociNoDockerDetail = "this server cannot run containers (no docker on the strazad host). Use the remote or command runtime, or run strazad where docker is installed."

// ociNoDockerRemoteDetail is ociNoDockerDetail on a manager that refuses
// command servers, so it names the remote runtime only.
const ociNoDockerRemoteDetail = "this server cannot run containers (no docker on the strazad host). Use the remote runtime, or run strazad where docker is installed."

// Container labels: every container this runtime starts carries both, so a
// boot sweep can find what a dead strazad left behind and a test can prove
// nothing outlived Stop.
const (
	labelOCI = "straza.oci=1"
	labelApp = "straza.app="
)

// dockerKillTimeout bounds every docker kill and docker ps, so a dead
// daemon cannot hang Stop or the boot sweep.
const dockerKillTimeout = 5 * time.Second

// DockerOnPath reports whether the docker CLI is on PATH.
func DockerOnPath() bool {
	_, err := exec.LookPath(DefaultDockerBin)
	return err == nil
}

// OCIRuntime is the docker-backed runtime: a CommandRuntime around docker
// run plus the container bookkeeping a docker client's exit does not do.
// The container id lands in a per-instance cidfile and the container
// carries the straza labels, so Stop kills the container by id and no
// container outlives its runtime.
type OCIRuntime struct {
	*CommandRuntime
	dir     string // per-instance temp dir holding the cidfile
	cidfile string
}

// NewOCIRuntime builds a Docker-backed runtime for an oci manifest. injectEnv
// is the broker-resolved credential rendered to an env var (nil = none). Its
// value is passed through the docker process environment (via `-e NAME`), not
// the argv, so it never appears in `ps` or logs.
func NewOCIRuntime(name string, spec OCISpec, injectEnv *EnvVar, ring *Ring) (*OCIRuntime, error) {
	dir, err := os.MkdirTemp("", "straza-oci-")
	if err != nil {
		return nil, fmt.Errorf("manager: oci runtime %s: create the cidfile directory: %w", name, err)
	}
	cidfile := filepath.Join(dir, "cid")
	// Process environment for the docker CLI: fixed manifest env values that
	// we reference by name, plus the injected secret.
	procEnv := append([]EnvVar{}, spec.Env...)
	injectName := ""
	if injectEnv != nil {
		procEnv = append(procEnv, *injectEnv)
		injectName = injectEnv.Name
	}
	cmdSpec := CommandSpec{Exec: DefaultDockerBin, Args: dockerRunArgs(spec, injectName, name, cidfile), Env: procEnv}
	rt := &OCIRuntime{CommandRuntime: NewCommandRuntime(name, cmdSpec, nil, ring), dir: dir, cidfile: cidfile}
	// docker refuses to start when the cidfile exists, so each restart
	// clears the previous one.
	rt.preSpawn = func() { _ = os.Remove(cidfile) }
	// The docker CLI reads DOCKER_HOST, DOCKER_CONTEXT, DOCKER_CONFIG,
	// DOCKER_CERT_PATH and DOCKER_TLS_VERIFY from its own environment, so a
	// rootless or remote daemon needs them passed through. The container
	// never sees the CLI's environment.
	rt.envPrefixes = []string{"DOCKER_"}
	return rt, nil
}

// Stop kills the container, then closes the client session and the
// supervisor, then kills again in case a restart wrote a new id in between,
// and removes the cidfile directory. Idempotent.
func (r *OCIRuntime) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	r.killContainer()
	r.CommandRuntime.Stop()
	r.killContainer()
	_ = os.RemoveAll(r.dir)
}

// killContainer kills the container named by the cidfile, when there is one.
func (r *OCIRuntime) killContainer() {
	raw, err := os.ReadFile(r.cidfile)
	if err != nil {
		return
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerKillTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, DefaultDockerBin, "kill", id).CombinedOutput(); err != nil { // #nosec G204 -- the id came from docker's own cidfile
		r.ring.Append(fmt.Sprintf("docker kill %s: %v %s", id, err, strings.TrimSpace(string(out))))
	}
}

// sweepContainers kills every container carrying the straza.oci label: what
// a strazad that died mid-flight left running.
func sweepContainers(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, dockerKillTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, DefaultDockerBin, "ps", "-q", "--filter", "label="+labelOCI).Output()
	if err != nil {
		return err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	return exec.CommandContext(ctx, DefaultDockerBin, append([]string{"kill"}, ids...)...).Run() // #nosec G204 -- ids come from docker ps
}

// hasOCIApp reports whether any persisted app runs in a container.
func hasOCIApp(apps []store.App) bool {
	for _, a := range apps {
		if a.RuntimeKind == RuntimeOCI {
			return true
		}
	}
	return false
}

// dockerRunArgs builds the `docker run` argument vector. Env vars (manifest
// entries and the injected credential) are passed by NAME only (`-e NAME`),
// so docker imports the value from the CLI process environment into the
// container and the secret stays out of argv. The cidfile and the labels
// are what Stop and the boot sweep find the container by.
func dockerRunArgs(spec OCISpec, injectName, appName, cidfile string) []string {
	args := []string{"run", "--rm", "-i", "--cidfile", cidfile, "--label", labelOCI, "--label", labelApp + appName}
	if spec.Sandbox != "none" {
		args = append(args,
			"--read-only",
			"--cap-drop", "ALL",
			"--security-opt", "no-new-privileges",
			"--pids-limit", strconv.Itoa(256),
			"--network", "none", // the manifest has no network allowlist
		)
	}
	for _, e := range spec.Env {
		args = append(args, "-e", e.Name)
	}
	if injectName != "" {
		args = append(args, "-e", injectName)
	}
	args = append(args, spec.Image)
	return args
}
