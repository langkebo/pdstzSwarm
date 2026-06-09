#!/usr/bin/env bash
#
# deploy/scripts/up.sh — start the integrated stack
#
# Usage:
#   ./deploy/scripts/up.sh                # bring up core + ollama
#   ./deploy/scripts/up.sh --build        # rebuild images before up
#   ./deploy/scripts/up.sh --profile=full # + Prometheus + Grafana
#   ./deploy/scripts/up.sh --profile=no-llm  # no Ollama (use external)
#
# Side effects:
#   - Reads ../../.env (or creates a minimal one from .env.example
#     if missing).
#   - Calls `docker compose -f deploy/docker-compose.yml up` with
#     the right profile flags.
#   - Waits for the API healthcheck to report healthy before
#     returning. Exits non-zero on timeout so a CI run can fail
#     cleanly.

set -euo pipefail

# ── Resolve project root relative to this script ───────────────────
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
COMPOSE_FILE="$ROOT/deploy/docker-compose.yml"
FULL_COMPOSE="$ROOT/deploy/docker-compose.full.yml"

# ── Defaults ───────────────────────────────────────────────────────
PROFILE="default"
BUILD=""
WAIT_TIMEOUT=120   # seconds

# ── Args ───────────────────────────────────────────────────────────
for arg in "$@"; do
  case "$arg" in
    --build) BUILD="--build" ;;
    --profile=*) PROFILE="${arg#*=}" ;;
    --wait=*)  WAIT_TIMEOUT="${arg#*=}" ;;
    -h|--help)
      sed -n '2,21p' "$0"
      exit 0
      ;;
    *) echo "unknown arg: $arg" >&2; exit 1 ;;
  esac
done

# ── Sanity checks ──────────────────────────────────────────────────
if ! command -v docker >/dev/null 2>&1; then
  echo "❌ docker not found in PATH" >&2
  exit 1
fi

# ── .env bootstrap ────────────────────────────────────────────────
if [[ ! -f "$ROOT/.env" ]]; then
  if [[ -f "$ROOT/.env.example" ]]; then
    cp "$ROOT/.env.example" "$ROOT/.env"
    echo "📝 Created $ROOT/.env from .env.example — fill in ORCHESTRATOR_API_KEY before continuing"
  else
    echo "❌ Neither $ROOT/.env nor $ROOT/.env.example found" >&2
    exit 1
  fi
fi

# ── Compose flags ─────────────────────────────────────────────────
#
# `docker compose --env-file` loads the file into compose's
# interpolation scope (used by `${VAR:-default}`), AS WELL
# AS into the service's environment (via env_file in the
# service block). The two scopes are different — without
# --env-file here, ${VAR:-...} interpolation would see only
# the shell env, and your .env values would not land in the
# resolved config.
COMPOSE_FLAGS=(-f "$COMPOSE_FILE" --env-file "$ROOT/.env")
case "$PROFILE" in
  default|llm) ;;                          # both default and llm include ollama
  no-llm|api-only)
    # Override the default ollama profile activation by NOT including it.
    # We do this by adding the `no-llm` profile flag and removing
    # the `default` profile.
    COMPOSE_FLAGS+=(--profile no-llm)
    # Note: the compose file declares `profiles: [llm, default]`
    # on ollama. With --profile no-llm AND --profile llm both
    # present, ollama still starts. The trick is to NOT pass
    # --profile llm here. The default profile is implicit only
    # when the service has no `profiles:` list. Since ollama
    # has a profiles list, we need to either include it
    # explicitly or omit it. So: just don't pass --profile llm.
    ;;
  full)
    # Stack + observability plane.
    COMPOSE_FLAGS=(-f "$COMPOSE_FILE" -f "$FULL_COMPOSE")
    ;;
  *)
    echo "unknown profile: $PROFILE" >&2
    exit 1
    ;;
esac

# ── Up ─────────────────────────────────────────────────────────────
echo "▶ Starting Pentest Swarm AI (profile=$PROFILE, build=${BUILD:-no}) ..."
docker compose "${COMPOSE_FLAGS[@]}" up -d $BUILD

# ── Wait for healthz ──────────────────────────────────────────────
echo -n "⏳ Waiting for API healthz "
for ((i=0; i<WAIT_TIMEOUT; i+=2)); do
  if curl -fsS http://localhost:8081/healthz >/dev/null 2>&1; then
    echo
    echo "✅ Stack up. Open http://localhost:8081"
    echo
    echo "Useful endpoints:"
    echo "  - API:    http://localhost:8081/api/v1/health"
    echo "  - Web UI: http://localhost:8081"
    echo "  - /metrics: http://localhost:8081/metrics"
    echo "  - /readyz:  http://localhost:8081/readyz"
    echo "  - Postgres: localhost:5432 (POSTGRES_USER / POSTGRES_PASSWORD)"
    echo "  - Redis:    localhost:6379"
    echo "  - Ollama:   http://localhost:11434"
    exit 0
  fi
  echo -n "."
  sleep 2
done

echo
echo "❌ API healthz did not respond within ${WAIT_TIMEOUT}s" >&2
echo "   Tail of swarm logs:" >&2
docker compose "${COMPOSE_FLAGS[@]}" logs --tail=50 pentestswarm >&2 || true
exit 1
