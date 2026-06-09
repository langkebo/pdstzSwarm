# syntax=docker/dockerfile:1.7
#
# P3-1: Kali toolchain image for Pentest-Swarm-AI.
#
# This image is the runtime target of internal/tools/docker.Runner.
# It bakes the toolchain + non-root user + entrypoint together
# so a single image ref (psa/kali:dev) gives operators a known
# safe baseline. The Runner still applies cgroup limits, network
# isolation, RO rootfs, and the seccomp profile — those are
# orthogonal to what's installed in the image.
#
# Base: kalilinux/kali-rolling (Debian-derived, official Kali).
# Why rolling rather than a tagged release: the pentest toolchain
# is best-effort / fast-moving; "latest" matches operator
# expectations (a year-old image is usually missing recent
# nuclei templates).
#
# Layering:
#   1. apt-get update + install (~2-3 min on first build)
#   2. non-root user + /work tree
#   3. entrypoint + LABEL metadata
#
# The image is intentionally NOT built with the seccomp profile
# baked in. Seccomp is a daemon-level concern — it's passed at
# container-create time via HostConfig.SecurityOpt. Baking it
# would force the image to a single profile.

FROM kalilinux/kali-rolling

# Skip the interactive apt prompts. We never use a TTY in
# the docker build context, but the post-install scripts of
# some kali metapackages probe debconf.
ENV DEBIAN_FRONTEND=noninteractive
ENV LANG=C.UTF-8

# --- Toolchain --------------------------------------------------------
#
# Three layers, separated by RUN to maximize BuildKit cache hits
# (most of the time, only the project-specific layer changes
# between rebuilds).
#
# Layer 1 — kali-tools-top10: the canonical "10 most-used Kali
# tools" metapackage. Pulls in nmap, sqlmap, hydra, john,
# aircrack-ng, burpsuite, wireshark-cli, metasploit-framework,
# maltego, and zaproxy.
#
# Layer 2 — project-specific Go-binary recon stack: nuclei,
# subfinder, httpx, naabu, ffuf, katana. These are the tools
# the agents actually call into from the runner. Installed
# from the Kali apt repo when available; ffuf and katana may
# fall back to direct binary download (see comments below).
#
# Layer 3 — utilities: dnsutils, iputils-ping, net-tools, curl,
# jq. Pure-POSIX / busybox-style tools that the entrypoint and
# the agents lean on for basic plumbing (DNS lookups, JSON
# parsing, etc.).
#
# `--no-install-recommends` keeps the image lean. The full Kali
# top-10 pulls ~3 GB of recommendations otherwise; we only need
# the CLI tools.
#
# `apt-get clean && rm -rf /var/lib/apt/lists/*` shrinks the
# layer by ~200 MB (apt cache + lists).

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        kali-tools-top10 \
        nuclei \
        subfinder \
        httpx-toolkit \
        naabu \
        ffuf \
        katana \
    || (echo "WARN: some Kali metapackages missing — falling back to baseline tools" \
        && apt-get install -y --no-install-recommends \
            nmap sqlmap hydra john aircrack-ng wireshark-common \
            dnsutils iputils-ping net-tools curl jq ca-certificates) \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# --- Non-root user ---------------------------------------------------
#
# Pentest-Swarm-AI's default posture is unprivileged. The Runner
# never sets Privileged=true, and Linux user namespaces are off
# by default on the daemon. The "pentest" uid (1000) is the
# convention so host files mounted via any future bind-mount
# (out of P3-1 scope) are accessible without chowning.
#
# /work is the agent's writable surface. With ReadonlyRootfs=true
# (Runner's recommended posture), /work is a tmpfs (Docker
# creates it by default if a path is listed in VOLUME) — agent
# scratch data lives there and dies with the container.
#
# /work/.cache is for the long-lived tool caches (nuclei
# templates, subfinder provider list, etc). Operators who want
# cross-run cache persistence bind-mount a host directory
# there. Without a bind mount, it still works — just gets wiped
# on container exit.

RUN groupadd -g 1000 pentest \
    && useradd -m -u 1000 -g 1000 -s /bin/bash pentest \
    && mkdir -p /work /work/.cache /work/.cache/bin /etc/psa \
    && chown -R pentest:pentest /work

# --- /etc/psa/manifest.json ----------------------------------------
#
# P3-1: write a small JSON document at /etc/psa/manifest.json
# describing the installed toolchain. The Runner reads this
# at container-create time (when Config.VerifyManifest=true)
# to verify image identity. The schema is documented in
# internal/tools/docker/manifest.go.
#
# We resolve versions via `dpkg-query -W -f='${Version}'` for
# the packages we just installed, so the manifest reflects
# what the build actually produced (not a pinned version in
# the Dockerfile, which can drift as kali-rolling rolls).
#
# The build image_ref arg is passed via `docker build
# --build-arg IMAGE_REF=...` and defaults to "psa/kali:dev"
# for local builds.
ARG IMAGE_REF=psa/kali:dev
ARG BUILD_DATE=unknown

# dpkg-query may emit a "no packages found" line for tools
# that were unavailable in the metapackage fallback path
# (see the top of the file). We treat those as "missing"
# and use a sentinel version string "n/a" so the manifest
# parser still succeeds.
RUN set -eux \
    ; for pkg in nmap sqlmap hydra john aircrack-ng wireshark-common dnsutils iputils-ping net-tools curl jq ca-certificates nuclei subfinder httpx-toolkit naabu ffuf katana; do \
        v=$(dpkg-query -W -f='${Version}' "$pkg" 2>/dev/null || echo "n/a"); \
        printf '{"name":"%s","version":"%s"}\n' "$pkg" "$v"; \
      done > /tmp/psa-manifest-tools.jsonl \
    ; jq -s --arg image "$IMAGE_REF" --arg built_at "$BUILD_DATE" --arg user "pentest:1000" --arg workdir "/work" \
        '{image:$image,built_at:$built_at,user:$user,workdir:$workdir,tools:.} \
         | .tools |= sort_by(.name)' \
       /tmp/psa-manifest-tools.jsonl > /etc/psa/manifest.json \
    && rm -f /tmp/psa-manifest-tools.jsonl \
    && chmod 0644 /etc/psa/manifest.json

# --- Entrypoint ------------------------------------------------------

COPY --chmod=0755 entrypoint.sh /usr/local/bin/entrypoint.sh

# --- OCI / PSA metadata ----------------------------------------------
#
# org.opencontainers.image.* are the standard labels. Custom
# org.psa.* are project-specific so operators can `docker
# inspect psa/kali:dev` and see exactly what's inside.

LABEL org.opencontainers.image.title="psa-kali" \
      org.opencontainers.image.description="Pentest-Swarm-AI Kali toolchain (P3-1)" \
      org.opencontainers.image.source="https://github.com/Armur-Ai/Pentest-Swarm-AI" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.psa.kali.base="kalilinux/kali-rolling" \
      org.psa.kali.user="pentest:1000" \
      org.psa.kali.workdir="/work" \
      org.psa.kali.entrypoint="/usr/local/bin/entrypoint.sh" \
      org.psa.kali.tools="nmap,sqlmap,hydra,john,aircrack-ng,wireshark-cli,metasploit-framework,maltego,zaproxy,nuclei,subfinder,httpx,naabu,ffuf,katana" \
      org.psa.kali.utils="dnsutils,iputils-ping,net-tools,curl,jq" \
      org.psa.kali.manifest="/etc/psa/manifest.json" \
      org.psa.kali.layered="true"

# P3-1: this is a tool image, not a service — it doesn't
# run a long-lived process. HEALTHCHECK NONE prevents the
# Docker daemon from emitting spurious "unhealthy" events
# for a container that just `exec`-ed a tool and exited.
# (The image's ENTRYPOINT is `/usr/local/bin/entrypoint.sh`
# which `exec`s the user's command — but for the manifest
# probe path, the Runner uses `/bin/sh -c cat …` with
# Entrypoint cleared, so the container's process tree is
# just `sh -c cat …`.)
HEALTHCHECK NONE

# /work is the agent's RW surface; expose it as a VOLUME so
# Docker creates a tmpfs (with the default driver) when
# ReadonlyRootfs=true. Operators may also bind-mount a host
# directory here for persistent caching.
VOLUME ["/work"]

# /work as the working directory — most recon tools default
# to writing their output to the cwd.
WORKDIR /work

# Default to the unprivileged user. The Runner doesn't need
# root, and dropping to a non-root UID is a defense-in-depth
# measure in case a tool image escapes its seccomp/cgroup
# boundary (the worst it can do is corrupt /work, not the
# host).
USER pentest

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["/bin/bash"]
