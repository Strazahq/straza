# Straza Tier-3 "hookless agent" sandbox profile.
#
# The image that turns `straza exec` from advisory into boundary-grade
# (see the Hookless processes page of the docs): the agent's ONLY exec surface
# is the shim. Design, route by route:

#   PATH           /straza/bin only: shim, wexec, the governed shell, and
#                   the agent's own interpreter. Nothing else resolves.
#   /bin/sh, bash   replaced by a root-owned wrapper that routes every shell
#                   string (system(3), popen(3), os.system, subprocess
#                   shell=True, #!/bin/sh shebangs) through `straza exec`.
#   direct execve   the hardening sweep strips group/other READ+execute from
#                   every executable file except a 7-entry allowlist, and the
#                   build FAILS if anything else is left executable. The
#                   dynamic-loader trick (ld.so /path/to/bin) dies with the
#                   same sweep: 0700 root-owned targets are unreadable, so
#                   the loader cannot even map them.

#   write+exec     a runtime property: read_only rootfs + noexec tmpfs for
#                   every writable mount (deploy/sandbox/compose.yaml).
#   setuid          stripped by the sweep; cap_drop ALL + no-new-privileges
#                   at runtime make it moot anyway.

# The HONEST residual (the Hookless processes guide, "What the image leaves
# open"): the agent's own interpreter computes in-process ungoverned
# (governance covers execution and MCP, not thought), and every allowlisted
# binary (python, /straza/real/sh) is directly execve-able by the same uid,
# so per-argument policy is only boundary-grade for what is NOT on the
# allowlist. Keep the allowlist small.

# Build from the repo root (the shim builds from source, same conventions as
# deploy/Dockerfile: CGO off, static, uid 65532):
#   docker build -f deploy/sandbox/Dockerfile -t straza/sandbox-agent .
ARG GO_VERSION=1.26
# The agent runtime this profile hardens around. python:3.X-slim variants work
# unchanged; a non-Python base needs the allowlist and entrypoint.py ported
# (both assume the interpreter at /usr/local/bin/python3).
ARG BASE_IMAGE=python:3.12-slim

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X github.com/strazahq/straza/internal/version.Version=${VERSION} -X github.com/strazahq/straza/internal/version.Commit=${COMMIT}" \
      -o /out/straza ./cmd/straza

FROM ${BASE_IMAGE}

# Non-root agent user, uid 65532 (house convention, distroless "nonroot").
# Login shell is /bin/sh, which this image makes the governed wrapper, so
# even an interactive `docker exec agent sh` session is a Straza decision.
RUN groupadd --gid 65532 agent \
 && useradd --uid 65532 --gid 65532 --create-home --home-dir /home/agent \
      --shell /bin/sh agent

# The shim, root-owned at a fixed path the wrapper scripts reference
# absolutely (never via PATH, so nothing under agent control can shadow it).
COPY --from=build /out/straza /opt/straza/bin/straza
COPY deploy/sandbox/sh-wrapper.sh deploy/sandbox/wexec.sh deploy/sandbox/entrypoint.py /tmp/straza-build/

# ---------------------------------------------------------------------------
# THE HARDENING LAYER. One RUN, deliberately the LAST one: after it, /bin/sh
# is the governed wrapper (root passthrough keeps derived-image RUN steps
# working on the writable build fs, but if you extend the profile, put your
# RUNs before this line or use SHELL ["/straza/real/sh","-c"]).
# ---------------------------------------------------------------------------
RUN set -eux; \
    # the ONE interpreter this profile deliberately keeps agent-executable:
    # it IS the agent's runtime; there is no image where the agent can run
    # and its interpreter cannot (the documented residual).
    PY_REAL="$(readlink -f /usr/local/bin/python3)"; \
    # ---- remove the self-modification levers (package manager, compilers
    # are absent from slim already): pip + ensurepip go entirely. Even
    # in-process `python -m pip` then has nothing to run, and the runtime
    # profile (ro site-packages, egress locked to strazad) could not install
    # anything anyway. apt/dpkg stay on disk but are de-executed by the
    # sweep below (root-only), keeping root debugging possible.
    python3 -m pip uninstall -y pip || true; \
    rm -rf /usr/local/lib/python*/ensurepip /usr/local/bin/pip*; \
    # ---- stash the real POSIX shell where only the shim invokes it ----
    mkdir -p /straza/bin /straza/real /straza/enroll; \
    SH_REAL="$(readlink -f /bin/sh)"; \
    install -m 0755 "$SH_REAL" /straza/real/sh; \
    # ---- the governed shell replaces /bin/sh and /bin/bash ----
    install -m 0755 /tmp/straza-build/sh-wrapper.sh /usr/bin/sh.straza; \
    rm -f /usr/bin/sh /usr/bin/bash; \
    ln -s sh.straza /usr/bin/sh; \
    ln -s sh.straza /usr/bin/bash; \
    if [ ! -L /bin ]; then rm -f /bin/sh /bin/bash; ln -s /usr/bin/sh.straza /bin/sh; ln -s /usr/bin/sh.straza /bin/bash; fi; \
    # ---- the governed bin dir: the agent PATH in its entirety ----
    # each entry is a deliberate decision, documented here:
    #   straza the shim itself (enroll/status/doctor/exec)
    #   wexec       `wexec CMD` == `straza exec -- CMD`
    #   sh          the governed wrapper (so `sh -c ...` by name is governed)
    #   python3/python  the agent's own runtime, allowed by construction
    install -m 0755 /tmp/straza-build/wexec.sh /straza/bin/wexec; \
    install -m 0755 /tmp/straza-build/entrypoint.py /straza/entrypoint; \
    ln -s /opt/straza/bin/straza /straza/bin/straza; \
    ln -s /usr/bin/sh.straza /straza/bin/sh; \
    ln -s "$PY_REAL" /straza/bin/python3; \
    ln -s "$PY_REAL" /straza/bin/python; \
    rm -rf /tmp/straza-build /var/lib/apt/lists/*; \
    # ---- THE SWEEP: de-execute everything on the image except the allowlist,
    # and make PROGRAMS unreadable to the agent too. `-perm /6011` matches any
    # file with a group/other execute bit (or setuid/setgid). Removing execute
    # alone is NOT enough: `ld.so <binary>` maps and runs any ELF the loader
    # can merely READ. But shared libraries (libpython, libc; some ship 0755)
    # must STAY readable or the allowlisted interpreters cannot load them. So
    # split by kind:
    #   programs (not *.so*)  -> go-rx : 0700 root-owned, agent can neither
    #                            exec nor read -> direct-execve AND loader
    #                            route both dead; root keeps them for
    #                            `docker exec -u 0` debugging.
    #   libraries (*.so*)     -> go-x  : keep READ (dlopen/mmap needs read,
    #                            not the file's x bit), drop the exec bit so
    #                            the executable-set assertion below still
    #                            equals the allowlist. Running a library via
    #                            ld.so yields no arbitrary-command primitive.
    # ld-linux (the ELF interpreter of python and dash) is excluded outright.
    LD_REAL="$(find /usr/lib /lib/ -xdev -type f -name 'ld-linux-*' -print -quit)"; \
    SWEEP="-perm /6011 ! -path $PY_REAL ! -path $LD_REAL ! -path /straza/real/sh ! -path /usr/bin/sh.straza ! -path /opt/straza/bin/straza ! -path /straza/bin/wexec ! -path /straza/entrypoint"; \
    find / -xdev -type f $SWEEP ! -name '*.so' ! -name '*.so.*' -exec chmod u-s,g-s,go-rx {} +; \
    find / -xdev -type f $SWEEP \( -name '*.so' -o -name '*.so.*' \) -exec chmod u-s,g-s,go-x {} +; \
    # loader route must be dead: a swept program is unreadable to the agent.
    # /usr/bin/ls is a known base-image binary that MUST have been swept, so
    # assert it is now 0700 root-owned (group/other cannot read to feed ld.so).
    perm="$(stat -c '%a %U' /usr/bin/ls)"; \
    case "$perm" in 700\ root) : ;; *) echo "SWEEP LEAK: /usr/bin/ls is $perm, expected 700 root"; exit 1;; esac; \
    # ---- BUILD-TIME ASSERTION: the executable set IS the allowlist. If a
    # base-image bump ships a stray agent-executable file, the build fails
    # here instead of silently widening the boundary.
    found="$(find / -xdev -type f -perm /0011 | sort)"; \
    expected="$(printf '%s\n' "$PY_REAL" "$LD_REAL" /straza/real/sh /usr/bin/sh.straza /opt/straza/bin/straza /straza/bin/wexec /straza/entrypoint | sort)"; \
    if [ "$found" != "$expected" ]; then \
      echo 'HARDENING FAILED: executable set does not match the allowlist:'; \
      echo "$found"; exit 1; \
    fi; \
    test -z "$(find / -xdev -type f -perm /6000)"; \
    # ---- smoke: the survivors still work ----
    "$PY_REAL" -c 'import shutil; print("python ok")'; \
    /straza/real/sh -c 'echo "real sh ok"'; \
    sh -c 'echo "wrapper root-passthrough ok"'

# The agent sees exactly one directory of executables. HOME and the straza
# home are tmpfs mounts at runtime (compose); the rootfs itself stays ro.
ENV PATH=/straza/bin \
    HOME=/home/agent \
    STRAZA_HOME=/run/straza
USER 65532:65532
WORKDIR /home/agent

# entrypoint.py: verifies the enrollment mount (fails loudly with the remedy),
# stages config+identity into the tmpfs home, then exec()s the agent command.
ENTRYPOINT ["/straza/entrypoint"]
# Default: an interactive governed REPL (compose overrides with your agent).
CMD ["python3"]
