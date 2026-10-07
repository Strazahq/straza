#!/straza/real/sh
# Straza Tier-3 sandbox profile: the governed shell. Installed as /bin/sh AND
# /bin/bash (deploy/sandbox/Dockerfile), so every shell-string execution route
# in libc and CPython (system(3), popen(3), os.system, subprocess shell=True)
# lands here and becomes a canonical shell.exec decision BEFORE anything runs;
# shebang scripts land here too, with the script path as $1.
#
# The real POSIX shell (dash) is stashed at /straza/real/sh, root-owned. It
# must stay agent-executable so allowed commands can run. That is the
# documented residual: execve()ing it directly yields an ungoverned shell,
# bounded by the same minimal executable set.

# Root passthrough keeps build layers (RUN is /bin/sh -c as root on a writable
# rootfs) session-free: -w is an access(2) check, true only for root on a
# writable filesystem. The runtime rootfs is read-only, so root routes here too.
if [ -w /straza/real/sh ]; then
  exec /straza/real/sh "$@"
fi
exec /opt/straza/bin/straza exec -- /straza/real/sh "$@"
