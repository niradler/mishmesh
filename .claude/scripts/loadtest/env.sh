export MSYS_NO_PATHCONV=1
: "${W5_ROOT:?Set W5_ROOT to an absolute scratch directory before sourcing env.sh}"
export W5_ROOT
export W5_BIN="$W5_ROOT/bin"
export W5_OUT="$W5_ROOT/out"
export COMPOSE_PROJECT_NAME=w5
LTDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && (cygpath -m "$PWD" 2>/dev/null || pwd))"
dc() { timeout 300 docker compose -f "$LTDIR/compose.yml" "$@"; }
ltx() { timeout "${LT_TIMEOUT:-300}" docker compose -f "$LTDIR/compose.yml" exec -T lt /bin/w5/lt "$@"; }
