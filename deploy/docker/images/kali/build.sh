#!/usr/bin/env bash
#
# P3-1: build script for the psa-kali image.
#
# Usage:
#   ./build.sh                      # build psa/kali:dev (default tag)
#   ./build.sh --tag psa/kali:1.0.0 # custom tag
#   ./build.sh --no-cache           # pass --no-cache to docker build
#   ./build.sh --push               # also push the resulting image
#
# Exit codes:
#   0 = build succeeded + manifest verify OK
#   1 = build failed or manifest verify failed
#
# The script is intentionally minimal — it does not
# pull base images (the daemon caches them), does not
# install jq / docker (the operator's responsibility),
# and does not manage multi-arch builds (use
# buildx for that — the script can be wrapped
# trivially).

set -euo pipefail

# --- argument parsing ------------------------------------------------

TAG="psa/kali:dev"
PUSH=0
EXTRA_BUILD_ARGS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tag)
            TAG="$2"
            shift 2
            ;;
        --no-cache)
            EXTRA_BUILD_ARGS+=(--no-cache)
            shift
            ;;
        --push)
            PUSH=1
            shift
            ;;
        --build-arg)
            EXTRA_BUILD_ARGS+=(--build-arg "$2")
            shift 2
            ;;
        -h|--help)
            grep -E "^#( |$)" "$0" | sed 's/^# \?//'
            exit 0
            ;;
        *)
            echo "build.sh: unknown flag: $1" >&2
            exit 2
            ;;
    esac
done

# --- preflight -------------------------------------------------------

if ! command -v docker >/dev/null 2>&1; then
    echo "build.sh: docker not on PATH" >&2
    exit 1
fi
if ! docker info >/dev/null 2>&1; then
    echo "build.sh: docker daemon not reachable (or no permission)" >&2
    exit 1
fi

# Resolve the script's directory so we can pin the
# build context regardless of cwd.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "build.sh: context = $SCRIPT_DIR"
echo "build.sh: tag     = $TAG"

# --- build -----------------------------------------------------------
#
# The IMAGE_REF build arg is what the Dockerfile bakes
# into /etc/psa/manifest.json. Without it, the manifest
# would have image="psa/kali:dev" even for a --tagged
# psa/kali:1.0.0 build — a subtle bug that would
# confuse VerifyManifest on the operator's host.

BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "build.sh: building (BUILD_DATE=$BUILD_DATE)…"
docker build \
    --tag "$TAG" \
    --build-arg IMAGE_REF="$TAG" \
    --build-arg BUILD_DATE="$BUILD_DATE" \
    "${EXTRA_BUILD_ARGS[@]}" \
    "$SCRIPT_DIR"

# --- post-build manifest verify -------------------------------------
#
# We re-read the manifest out of the freshly-built
# image and confirm it's valid JSON. This catches
# Dockerfile typos in the dpkg-query loop without
# needing a real Runner — a `docker run` + `cat` is
# enough.

echo "build.sh: verifying /etc/psa/manifest.json in $TAG…"
if ! docker run --rm \
        --entrypoint /bin/sh \
        "$TAG" \
        -c 'cat /etc/psa/manifest.json' \
        | python3 -c "import sys, json; json.load(sys.stdin); print('  manifest JSON: OK')" \
    ; then
    echo "build.sh: manifest verify FAILED for $TAG" >&2
    exit 1
fi

# --- optional push ---------------------------------------------------

if [[ "$PUSH" -eq 1 ]]; then
    echo "build.sh: pushing $TAG…"
    docker push "$TAG"
fi

echo "build.sh: $TAG built and verified"
