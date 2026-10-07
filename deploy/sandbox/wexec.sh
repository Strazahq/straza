#!/straza/real/sh
# `wexec CMD [ARGS...]`: ergonomic alias for `straza exec -- CMD ...`.
# The straza binary has no argv[0] dispatch, so the deploy layer provides
# the alias with a root-owned two-line script.
exec /opt/straza/bin/straza exec -- "$@"
