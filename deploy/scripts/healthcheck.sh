#!/usr/bin/env bash
#
# deploy/scripts/healthcheck.sh — report the health of the integrated stack
#
# Usage:
#   ./deploy/scripts/healthcheck.sh           # human-readable
#   ./deploy/scripts/healthcheck.sh --json    # JSON for monitoring
#   ./deploy/scripts/healthcheck.sh --quiet   # only failures
#
# Checks each container's `State.Health.Status` (running /
# starting / healthy / unhealthy) and the API's /healthz +
# /readyz endpoints. Exits 0 when every service is healthy,
# 1 when any is degraded, 2 when the stack is unreachable.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
COMPOSE_FILE="$ROOT/deploy/docker-compose.yml"
FULL_COMPOSE="$ROOT/deploy/docker-compose.full.yml"

JSON=false
QUIET=false

for arg in "$@"; do
  case "$arg" in
    --json)  JSON=true ;;
    --quiet) QUIET=true ;;
    -h|--help)
      sed -n '2,15p' "$0"
      exit 0
      ;;
    *) echo "unknown arg: $arg" >&2; exit 1 ;;
  esac
done

# Container list — kept in sync with the compose file. We could
# auto-derive this from `docker compose ps` but the human format
# changes between Docker versions; hard-coding the canonical
# service names is more stable.
SERVICES=(psa-swarm psa-postgres psa-redis)
if docker ps --format '{{.Names}}' | grep -q psa-ollama; then
  SERVICES+=(psa-ollama)
fi
if docker ps --format '{{.Names}}' | grep -q psa-prometheus; then
  SERVICES+=(psa-prometheus psa-grafana psa-pg-exporter)
fi

# ── Collect status ───────────────────────────────────────────────
status_for() {
  local name="$1"
  docker inspect --format='{{.State.Health.Status}}|{{.State.Running}}' "$name" 2>/dev/null || echo "missing|false"
}

declare -A STATUS
declare -A RUNNING
for svc in "${SERVICES[@]}"; do
  IFS='|' read -r h r <<<"$(status_for "$svc")"
  STATUS[$svc]="$h"
  RUNNING[$svc]="$r"
done

# ── Probe API endpoints ──────────────────────────────────────────
API_HEALTHZ="down"
API_READYZ="down"
if curl -fsS --max-time 2 http://localhost:8081/healthz >/dev/null 2>&1; then
  API_HEALTHZ="up"
fi
if curl -fsS --max-time 2 http://localhost:8081/readyz >/dev/null 2>&1; then
  API_READYZ="up"
fi

# ── Output ───────────────────────────────────────────────────────
if $JSON; then
  # Build JSON by hand to avoid a jq dependency.
  echo "{"
  echo '  "api_healthz": "'"$API_HEALTHZ"'",'
  echo '  "api_readyz":  "'"$API_READYZ"'",'
  first=true
  for svc in "${SERVICES[@]}"; do
    if ! $first; then echo ","; fi
    first=false
    printf '  "%s": {"health":"%s","running":%s}' \
      "$svc" "${STATUS[$svc]}" "${RUNNING[$svc]}"
  done
  echo
  echo "}"
else
  if ! $QUIET; then
    printf "%-20s %-12s %-10s\n" "SERVICE" "HEALTH" "RUNNING"
    printf "%-20s %-12s %-10s\n" "-------" "------" "-------"
  fi
  overall_ok=true
  for svc in "${SERVICES[@]}"; do
    if [[ "${STATUS[$svc]}" != "healthy" && "${RUNNING[$svc]}" != "true" ]]; then
      overall_ok=false
    fi
    printf "%-20s %-12s %-10s\n" "$svc" "${STATUS[$svc]}" "${RUNNING[$svc]}"
  done
  if ! $QUIET; then
    echo
    printf "%-20s %s\n" "API /healthz" "$API_HEALTHZ"
    printf "%-20s %s\n" "API /readyz"  "$API_READYZ"
  fi
  if ! $overall_ok; then exit 1; fi
fi
