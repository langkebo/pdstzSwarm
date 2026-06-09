#!/usr/bin/env bash
#
# P3-1: psa-kali container entrypoint.
#
# Responsibilities:
#   1. Make /work/.cache/bin available on PATH so any tool that
#      wants to install per-user binaries (pip --user, go install,
#      etc.) can drop them somewhere persistent.
#   2. Stamp every stdout / stderr line with an ISO-8601 timestamp
#      for audit purposes. Pentest runs are long (nmap on a /16 can
#      take hours), and the original timestamps from `docker logs`
#      are coarse-grained.
#   3. exec the user-supplied command as PID 1. We use exec so the
#      user process receives SIGTERM directly on `docker stop`
#      rather than going through a shell intermediary.
#
# Note on PID 1 + signal handling: bash itself doesn't forward
# signals cleanly when it's PID 1 (it's a known bash limitation;
# dash and busybox sh do better). For tools that need clean
# SIGTERM handling, callers should pass the tool binary directly
# as the command (no /bin/sh -c wrapper). The runner does this
# for the live tests.

set -euo pipefail

# Prepend the persistent cache bin dir to PATH. We do this BEFORE
# exec so the user command sees the right PATH.
export PATH="/work/.cache/bin:${PATH}"

# Line-prefixed logging. We re-route through ts() so every byte
# the user command writes gets a timestamp. The format is the
# same one Docker's own logs -t flag uses, so operators can
# grep on either.
ts() {
    # Read from stdin, write to stdout with a prefix.
    # awk is in busybox + the core Kali image; POSIX-portable.
    awk '{ printf("%s %s\n", strftime("%Y-%m-%dT%H:%M:%S%z", systime()), $0); fflush() }'
}

# If we got nothing to run, just drop into a shell so the
# container is usable for ad-hoc debugging.
if [ "$#" -eq 0 ]; then
    set -- /bin/bash
fi

# exec so the user command replaces this script as PID 1. The
# `2>&1` merge + pipe to ts is wrapped in `exec` so the shell
# doesn't sit between the user process and the daemon.
exec 2>&1
exec "$@" | ts
