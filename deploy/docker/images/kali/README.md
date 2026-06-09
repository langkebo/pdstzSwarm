# psa-kali

> Pentest-Swarm-AI's runtime image for the `docker.Runner`.
> Built on top of `kalilinux/kali-rolling`.

This directory contains the Dockerfile, entrypoint, and
build tooling for the `psa/kali` image. The image is the
default `Docker.Runner` target — every tool call lands in
a fresh container based on this image (or a pinned
variant) with the cgroup / network / seccomp / manifest
guardrails applied at create time.

See `MANIFEST.md` for the full tool inventory. See
`../../seccomp/pentest-swarm.json` for the seccomp
profile applied at runtime. See
`docs/optimization/p3-1-delivery-report.md` for the
P3-1 security rationale.

## Quick start

Build and inspect:

```bash
# Build (default tag: psa/kali:dev).
./build.sh

# Custom tag.
./build.sh --tag psa/kali:1.0.0

# Build, then push to a registry.
./build.sh --tag registry.example.com/psa/kali:1.0.0 --push

# Inspect the installed toolchain.
docker run --rm psa/kali:dev nmap --version
docker run --rm psa/kali:dev sqlmap --version

# Inspect the manifest.
docker run --rm --entrypoint /bin/sh psa/kali:dev -c \
    'cat /etc/psa/manifest.json | jq .'
```

Run a tool via `docker.Runner` with the standard
seccomp profile:

```go
profile, _ := docker.LoadSeccompProfile(
    "deploy/docker/seccomp/pentest-swarm.json")
r := docker.NewRunner(cli, docker.Config{
    DefaultImage:   "psa/kali:dev",
    SeccompProfile: profile,
    Isolation: docker.Isolation{
        Network:        docker.NetworkNone,
        ReadonlyRootfs: docker.ReadonlyRootfsReadOnly,
    },
    VerifyManifest: true,
})
res, _ := r.Run(ctx, docker.Request{
    Image:   "psa/kali:dev",
    Command: []string{"nmap", "-sV", "10.0.0.0/24"},
})
// res.Manifest is non-nil and matches the image we built.
```

## File layout

| File | Purpose |
| --- | --- |
| `Dockerfile` | Multi-stage build recipe |
| `entrypoint.sh` | `ENTRYPOINT` script (timestamp + exec) |
| `MANIFEST.md` | Tool inventory (humans) |
| `README.md` | This file |
| `build.sh` | `docker build` wrapper with manifest verify |
| `.dockerignore` | Build context exclusions |

## Seccomp

The image itself doesn't bake in a seccomp profile —
seccomp is a daemon-level concern, passed at
container-create time via `HostConfig.SecurityOpt`.
Bake it in the Runner, not the image.

The standard P3-1 profile is at
`deploy/docker/seccomp/pentest-swarm.json`. Two
variants ship alongside it:

- `pentest-swarm-strict.json` — blocks network syscalls
  entirely (socket, bind, connect, etc.). Offline-only
  analysis. Many tools will fail.
- `pentest-swarm-masscan.json` — allows `bpf` and
  `unshare(CLONE_NEWUSER)` for masscan-style scanners.
  Audit any tool that runs under this profile.

## Manifest verify

Each build writes `/etc/psa/manifest.json` with the
resolved version of every installed tool. The
`docker.Runner` reads this on every call (when
`Config.VerifyManifest = true`) and records the parsed
result on `Result.Manifest`. Use this to:

- Detect image-substitution attacks (a hostile mirror
  returns a different image under the same tag; the
  manifest won't match).
- Pin tool versions per-call (a tools agent can
  fail-closed if `Result.Manifest.Tools[i].Version`
  is below a floor).
- Audit (the manifest becomes the "what was actually
  installed" record for a given run).

`build.sh` runs a `python3 -c json.load` over the
manifest after the build to catch Dockerfile typos
in the `dpkg-query` loop.

## Build args

| Arg | Default | Purpose |
| --- | --- | --- |
| `IMAGE_REF` | `psa/kali:dev` | Baked into `manifest.image` |
| `BUILD_DATE` | `unknown` | Baked into `manifest.built_at` |

The `build.sh` wrapper sets these automatically from
the `--tag` flag and the current UTC time.

## What it does NOT include

- `kali-linux-everything` (30 GB, way too much).
- `gdb` (blocked by the seccomp profile — `ptrace`
  is denied).
- `metasploit-framework`'s postgres database
  (the agents run msf CLI, not the daemon).
- GUI tools (wireshark-qt, burpsuite GUI,
  maltego GUI). The CLI variants are installed.

See `MANIFEST.md` for the full list and rationale.
