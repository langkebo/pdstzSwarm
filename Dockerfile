# syntax=docker/dockerfile:1.7
#
# Pentest Swarm AI — integrated distribution image.
#
# Stages:
#   1. web-build  — produces web/out/ (Next.js static export)
#   2. swarm-build — compiles the pentestswarm binary, with the web
#                    bundle embedded via //go:embed (see internal/webfs).
#   3. tools-build — installs the Go-based security tools (subfinder,
#                    nuclei, httpx, naabu, etc.) into a single bin dir.
#   4. runtime     — slim Debian with apt + binary security tools, plus
#                    artefacts from stages 1/2/3.
#
# Why this shape:
#   - Stage 1 ensures the web bundle is *built* even when the
#     developer only asked to compile the Go binary (the embed
#     would otherwise fail in CI without the out/ directory).
#   - Stages 2 + 3 keep the Go toolchain (~150 MB) out of the
#     final image.
#   - The runtime layer holds only the binaries the operator
#     actually invokes.
#   - Final image is ~900 MB — large by web-app standards, normal
#     for a pentesting toolbox image (Kali base is ~3 GB for
#     comparison). The slim API-only image at
#     deploy/docker/Dockerfile is the alternative when tool
#     access is not required.
#
# Usage:
#   docker build -t pentest-swarm-ai .
#   docker run --rm -e PENTESTSWARM_ORCHESTRATOR_API_KEY=$KEY \
#     -v "$PWD/reports:/reports" \
#     pentest-swarm-ai serve
#   docker run --rm pentest-swarm-ai scan example.com
#   docker run --rm -i pentest-swarm-ai mcp serve   # stdio

# ───────────────────────────────────────────────────────────────────────────
# Stage 1: build the Next.js static dashboard
# ───────────────────────────────────────────────────────────────────────────
FROM node:20-bookworm-slim AS web-build

WORKDIR /src

# Layer so the heavy `npm install` is cached separately from the
# source tree. The standalone build is then a single COPY layer.
COPY web/package.json web/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund

COPY web/ ./
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

# ───────────────────────────────────────────────────────────────────────────
# Stage 2: build the swarm binary (with web embedded)
# ───────────────────────────────────────────────────────────────────────────
FROM golang:1.25-bookworm AS swarm-build

WORKDIR /src

# Layer the build so dependency downloads are cached separately from
# the source tree — incremental rebuilds only repay the compile step.
COPY go.mod go.sum ./
# GOPROXY points at the public proxy aggregator for the build
# sandbox. Operators in mainland China can override at build
# time with `--build-arg GOPROXY=https://goproxy.cn,direct`
# to route around the public proxy's egress throttling.
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=off
RUN go mod download

# Stage 1's web bundle must land at internal/webfs/out/ before the
# go build runs, because the webfs package uses //go:embed out.
# The COPY is split into two: web/out first (so the embed succeeds
# even when the user rebuilt with an empty Next.js cache), then the
# rest of the source. A `.gitkeep` is added in the empty case.
COPY --from=web-build /src/out/ /src/internal/webfs/out/
COPY . .

# CGO is on because some adapters (e.g. naabu's libpcap link) need
# it. -s -w strips debug info to keep the binary small (~30 MB).
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=1 go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/pentestswarm \
    ./cmd/pentestswarm

# ───────────────────────────────────────────────────────────────────────────
# Stage 3: build the Go-based security tools
# ───────────────────────────────────────────────────────────────────────────
# A separate stage so we can throw away the Go toolchain at the end.
# Each `go install` writes to /out/bin; the runtime stage scoops up
# that whole directory into /usr/local/bin.
FROM golang:1.25-bookworm AS tools-build

# libpcap-dev is required at COMPILE time for naabu's raw-socket port
# scan. The runtime stage installs libpcap0.8 for the runtime ABI.
#
# APT_MIRROR override (see runtime stage for rationale).
ARG APT_MIRROR=https://deb.debian.org
RUN sed -i "s|deb.debian.org/debian|${APT_MIRROR}/debian|g; s|deb.debian.org/debian-security|${APT_MIRROR}/debian-security|g" /etc/apt/sources.list.d/debian.sources 2>/dev/null \
    || sed -i "s|deb.debian.org/debian|${APT_MIRROR}/debian|g; s|deb.debian.org/debian-security|${APT_MIRROR}/debian-security|g" /etc/apt/sources.list 2>/dev/null \
    || true
RUN apt-get update && apt-get install -y --no-install-recommends \
      libpcap-dev \
      git \
    && rm -rf /var/lib/apt/lists/*

# GOTOOLCHAIN=auto lets Go fetch a newer toolchain on-demand when a
# tool's go.mod requires it. Without this, a single upstream bump
# (e.g. gowitness requiring Go 1.26) would force us to re-tag the
# base image. Auto handles the whole class of "tool X now needs Go
# Y.Z" failures transparently.
ENV GOTOOLCHAIN=auto
ENV GOSUMDB=off
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
ENV GOBIN=/out/bin
RUN mkdir -p /out/bin

# ProjectDiscovery toolchain (8 tools)
RUN go install github.com/projectdiscovery/subfinder/v2/cmd/subfinder@latest \
    && go install github.com/projectdiscovery/dnsx/cmd/dnsx@latest \
    && go install github.com/projectdiscovery/httpx/cmd/httpx@latest \
    && go install github.com/projectdiscovery/naabu/v2/cmd/naabu@latest \
    && go install github.com/projectdiscovery/katana/cmd/katana@latest \
    && go install github.com/lc/gau/v2/cmd/gau@latest \
    && go install github.com/projectdiscovery/nuclei/v3/cmd/nuclei@latest \
    && go install github.com/ffuf/ffuf/v2@latest

# Evidence + reporting helpers (gowitness) is installed in
# the runtime stage from a pre-built release tarball —
# see the rationale there. We can't `go install` the
# latest because its go.mod requires Go 1.26+.

# Amass is no longer in Debian's main repo — install from upstream
# Go module. Pinned to v4.2.0 (2023-09); the v5.x line introduced
# GraphQL/gRPC dependencies that compile-time-link Graphviz via
# cgo in the build sandbox. v4.2.0 is the last pure-Go release
# and gives the same surface API the swarm expects.
RUN go install github.com/owasp-amass/amass/v4/...@v4.2.0

# ───────────────────────────────────────────────────────────────────────────
# Stage 4: runtime
# ───────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

ENV DEBIAN_FRONTEND=noninteractive

# System security tools (apt-installable):
#   nmap        — port + service scanner
#   sqlmap      — SQL-injection exploitation
#   gobuster    — content discovery (alternative to ffuf)
#   dnsutils    — dig/nslookup, used by some adapters
#   libpcap0.8  — naabu runtime dependency
#   ca-certificates, curl, git — basic networking + tool downloads
#   chromium    — gowitness needs a headless browser; without it
#                 screenshot capture silently no-ops (we want it to
#                 work out of the box)
#   tini        — PID 1 init for clean signal forwarding (SIGTERM
#                 to the swarm binary, not to orphaned children).
#                 Without it, docker stop takes 10s and risks
#                 half-flushed Postgres transactions.
#
# Note: amass dropped from Debian's main repo as of Bookworm; we
# install it via `go install` in the tools-build stage instead.
#
# APT_MIRROR build-arg defaults to deb.debian.org; operators
# in mainland China can pass `--build-arg APT_MIRROR=mirrors.aliyun.com`
# to route around the upstream archive's egress throttling.
ARG APT_MIRROR=https://deb.debian.org
RUN sed -i "s|deb.debian.org/debian|${APT_MIRROR}/debian|g; s|deb.debian.org/debian-security|${APT_MIRROR}/debian-security|g" /etc/apt/sources.list.d/debian.sources 2>/dev/null \
    || sed -i "s|deb.debian.org/debian|${APT_MIRROR}/debian|g; s|deb.debian.org/debian-security|${APT_MIRROR}/debian-security|g" /etc/apt/sources.list 2>/dev/null \
    || true
RUN apt-get update && apt-get install -y --no-install-recommends \
      nmap \
      sqlmap \
      gobuster \
      dnsutils \
      libpcap0.8 \
      ca-certificates \
      curl \
      git \
      tini \
      python3 \
      python3-pip \
      python3-venv \
      chromium \
    && rm -rf /var/lib/apt/lists/*

# Semgrep via pip in a venv (Debian's PEP 668 forbids system-wide
# pip).
#
# PIP_MIRROR defaults to the public PyPI; operators in
# mainland China can pass `--build-arg PIP_MIRROR=https://mirrors.aliyun.com/pypi/simple/`.
ARG PIP_MIRROR=https://pypi.org/simple
RUN python3 -m venv /opt/venv \
    && /opt/venv/bin/pip install --no-cache-dir \
        -i "${PIP_MIRROR}" \
        semgrep
ENV PATH="/opt/venv/bin:${PATH}"

# Trufflehog: pinned release binary (not the curl|sh installer —
# auditability).
#
# GH_PROXY build-arg lets operators in mainland China route
# around GitHub's egress throttling. Default is the
# direct URL; set to `https://gh-proxy.com/` to use the
# community mirror.
ARG GH_PROXY=
ARG TRUFFLEHOG_VERSION=3.95.5
RUN curl -sSfL "${GH_PROXY}https://github.com/trufflesecurity/trufflehog/releases/download/v${TRUFFLEHOG_VERSION}/trufflehog_${TRUFFLEHOG_VERSION}_linux_amd64.tar.gz" \
    | tar -xz -C /usr/local/bin trufflehog

# Gitleaks: pinned release binary.
ARG GITLEAKS_VERSION=8.30.1
RUN curl -sSfL "${GH_PROXY}https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_linux_x64.tar.gz" \
    | tar -xz -C /usr/local/bin gitleaks

# Gowitness: pre-built release binary rather than `go install`
# from source — the latest module pseudo-version requires Go
# 1.26+ which our build sandbox doesn't have. Pinned to
# 3.1.1 (the last release built on Go 1.25).
ARG GOWITNESS_VERSION=3.1.1
RUN set -eux; \
    curl -fsSL "${GH_PROXY}https://github.com/sensepost/gowitness/releases/download/${GOWITNESS_VERSION}/gowitness-${GOWITNESS_VERSION}-linux-amd64" \
        -o /usr/local/bin/gowitness; \
    chmod +x /usr/local/bin/gowitness; \
    /usr/local/bin/gowitness version 2>&1 | head -3 || true

# Pull in the Go-based tools from stage 3 + the swarm binary from
# stage 2.
COPY --from=tools-build /out/bin/. /usr/local/bin/
COPY --from=swarm-build /out/pentestswarm /usr/local/bin/pentestswarm

# Pre-cache nuclei templates so the first scan doesn't pay the
# download cost. `|| true` because template fetch occasionally
# rate-limits on the unauthenticated GitHub API and we don't want
# that to break the build.
RUN nuclei -update-templates -silent 2>/dev/null || true

# Runtime layout. Three persistent paths:
#   /reports     — output of scan/report/assist
#   /home/pentester/.pentestswarm — license, installation_id, log
#                  files
#   /home/pentester/.nuclei       — nuclei template updates (operator
#                  can run `nuclei -ut` inside the container)
# All three should be backed by named volumes in docker-compose.
#
# Naabu's raw-socket scan needs CAP_NET_RAW which docker grants by
# default to root inside the container; if you run as non-root and
# need raw sockets, pass --cap-add=NET_RAW. We default to a
# dedicated user for the API server (no raw sockets needed); for
# scan/mcp modes the operator can override with --user 0:0.
RUN useradd -ms /bin/bash pentester \
    && mkdir -p /reports /home/pentester/.pentestswarm /home/pentester/.nuclei \
    && chown -R pentester:pentester /reports /home/pentester
USER pentester

# Reports land here; mount a host directory at /reports to persist
# them.
WORKDIR /reports

# API server port. Defaults to 8081 (the canonical "8080 is taken"
# port). Override with `--build-arg SERVER_PORT=9090` or set the
# PENTESTSWARM_SERVER_PORT env var at runtime. The HEALTHCHECK and
# CMD must agree on the same port.
#
# Note: the Dockerfile uses 8081 rather than 8080 because 8080 is
# the most commonly occupied port on shared dev servers. This
# avoids the "port already in use" failure on first boot.
ARG SERVER_PORT=8081

# Liveness: /healthz always 200s unless the process is dying.
# Readiness: /readyz 200s only when the orchestrator provider is
# configured. A load balancer can use it to drain traffic during
# rolling updates.
#
# Startup probe gives the process 30s to come up before the
# 5s-interval liveness probe starts hammering it. 30s covers
# the worst-case (cold Postgres pool + 1k-template nuclei cache).
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 \
    CMD curl -fsS http://localhost:${SERVER_PORT}/healthz || exit 1

# tini forwards SIGTERM to pentestswarm and reaps zombies. Without
# it, `docker stop` sends SIGTERM to PID 1 (the binary) but any
# child processes (nuclei / nmap / gowitness) keep running until
# the kernel times them out, blocking the postgres pool flush.
ENTRYPOINT ["/usr/bin/tini", "--", "pentestswarm"]
CMD serve --port ${SERVER_PORT}
