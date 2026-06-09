#!/usr/bin/env bash
#
# deploy/scripts/down.sh — stop the integrated stack
#
# Usage:
#   ./deploy/scripts/down.sh            # stop, keep volumes
#   ./deploy/scripts/down.sh --volumes  # stop + remove named volumes
#   ./deploy/scripts/down.sh --images   # also remove built images
#
# Volume names (data NOT lost on default `down`):
#   psa_pgdata         — Postgres data
#   psa_redis_data     — Redis snapshots + AOF
#   psa_ollama_models  — pulled LLM weights
#   psa_reports        — /reports from the swarm container
#   psa_home           — /home/pentester (license, installation_id)
#   psa_nuclei         — nuclei template cache

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
COMPOSE_FILE="$ROOT/deploy/docker-compose.yml"
FULL_COMPOSE="$ROOT/deploy/docker-compose.full.yml"

VOLUMES=false
IMAGES=false

for arg in "$@"; do
  case "$arg" in
    --volumes) VOLUMES=true ;;
    --images)  IMAGES=true ;;
    -h|--help)
      sed -n '2,17p' "$0"
      exit 0
      ;;
    *) echo "unknown arg: $arg" >&2; exit 1 ;;
  esac
done

# Build the same compose flag set as up.sh so we tear down what
# we brought up. The "full" profile attaches the observability
# compose; tear it down first to release the shared network.
COMPOSE_FLAGS=(--env-file "$ROOT/.env")
if docker compose -f "$FULL_COMPOSE" --env-file "$ROOT/.env" ps --services 2>/dev/null | grep -q .; then
  COMPOSE_FLAGS+=(-f "$COMPOSE_FILE" -f "$FULL_COMPOSE")
else
  COMPOSE_FLAGS+=(-f "$COMPOSE_FILE")
fi

echo "▶ Stopping Pentest Swarm AI ..."
docker compose "${COMPOSE_FLAGS[@]}" down

if $VOLUMES; then
  echo "▶ Removing named volumes ..."
  docker volume rm -f \
    psa_pgdata psa_redis_data psa_ollama_models \
    psa_reports psa_home psa_nuclei || true
fi

if $IMAGES; then
  echo "▶ Removing built images ..."
  docker compose "${COMPOSE_FLAGS[@]}" down --rmi local || true
fi

echo "✅ Stack stopped"
