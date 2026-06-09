# psa-kali: Tool Manifest

> Pentest-Swarm-AI's runtime image for the `docker.Runner`.
> Built on top of `kalilinux/kali-rolling`.
> See `Dockerfile` for the build recipe and `../../seccomp/pentest-swarm.json`
> for the seccomp profile applied at container-create time.

## Layered install (matches Dockerfile RUN statements)

### Layer 1 — `kali-tools-top10` (the canonical 10)

The `kali-tools-top10` metapackage pulls in the ten most-used
Kali tools as a single apt install. The list is curated by
OffSec and matches what `kali-linux-default` would suggest.

| Tool | Purpose | Metapackage Member |
| --- | --- | --- |
| **nmap** | Port scanning, service fingerprinting, NSE scripts | yes |
| **sqlmap** | Automated SQL injection detection & takeover | yes |
| **hydra** | Online brute-force (SSH, FTP, HTTP-POST, etc.) | yes |
| **john** | Offline password cracking (CPU) | yes |
| **aircrack-ng** | WiFi capture + WEP/WPA-PSK cracking | yes |
| **burpsuite** | Web proxy / scanner (community edition) | yes |
| **wireshark** | Packet capture / dissection (CLI: `tshark`) | yes |
| **metasploit-framework** | Exploit framework + payloads | yes |
| **maltego** | OSINT / link analysis (GUI; CLI under `maltego-cli` if installed) | yes |
| **zaproxy** | OWASP ZAP web scanner | yes |

### Layer 2 — Project-specific recon stack

These are the tools the agents actually call from
`internal/tools/docker.Runner`. They're installed alongside
`kali-tools-top10` because the agents' first phase is
recon — nuclei templates, subfinder, httpx, naabu, ffuf,
katana are the workhorses.

| Tool | Purpose | Notes |
| --- | --- | --- |
| **nuclei** | Template-based vulnerability scanner (ProjectDiscovery) | Pulls templates into `/work/.cache/nuclei-templates` on first run. |
| **subfinder** | Passive subdomain enumeration | API-key-aware; falls back to free sources without keys. |
| **httpx** | HTTP probing & tech fingerprinting | Used by recon agent to filter live hosts. |
| **naabu** | Fast port scanner (ProjectDiscovery) | SYN scan requires `NetworkHost` (P2 audit captures this). |
| **ffuf** | Web fuzzer (directory / parameter / VHost) | ProjectDiscovery's de-facto dirbuster. |
| **katana** | Web crawler / spider (ProjectDiscovery) | Output feeds httpx. |

### Layer 3 — Utilities

Pure-POSIX / busybox plumbing that the entrypoint, the
agents, and the seccomp live tests lean on.

| Tool | Purpose |
| --- | --- |
| **dnsutils** | `dig`, `nslookup`, `host` (DNS lookups) |
| **iputils-ping** | `ping` (ICMP probes; often blocked in `NetworkNone` mode, but useful for host self-test) |
| **net-tools** | `ifconfig`, `netstat`, `route` (legacy; superseded by `iproute2` on newer images but still expected by some scripts) |
| **curl** | HTTP client (used by the seccomp live test to fetch and parse output) |
| **jq** | JSON parser (used in the LangFuse reporter + various wrapper scripts) |

## Runtime User

| Property | Value |
| --- | --- |
| Username | `pentest` |
| UID / GID | 1000 / 1000 |
| Home | `/home/pentest` |
| Shell | `/bin/bash` |
| Working dir | `/work` |

`USER pentest` is set in the Dockerfile. The Runner does NOT
escalate to root. The seccomp profile additionally blocks
`setuid` / `setgid` for defense in depth, but we don't rely
on that — the non-root UID is the primary boundary.

## Filesystem Layout

```
/
├── work/                       # agent RW surface (VOLUME — tmpfs by default)
│   └── .cache/                 # persistent tool cache
│       ├── bin/                # on PATH (per-user binaries)
│       ├── nuclei-templates/   # populated by `nuclei -update-templates`
│       └── ...                 # anything the tool wants to keep
├── usr/
│   ├── bin/                    # system tools (nmap, sqlmap, …)
│   └── local/bin/
│       └── entrypoint.sh       # P3-1 entrypoint
└── ...
```

The root filesystem is **read-only** when the Runner uses
`Isolation{ReadonlyRootfs: ReadonlyRootfsReadOnly}`. `/work`
is the only writable surface and it's a tmpfs (or a host
bind-mount, if the operator provides one). All tool state
goes to `/work` or to stdout.

## Entrypoint

`/usr/local/bin/entrypoint.sh` (executable, owned by root).
Stamps every stdout/stderr line with an ISO-8601 timestamp
and `exec`s the user command as PID 1.

## What is **not** installed (intentional)

| Excluded | Why |
| --- | --- |
| `kali-linux-everything` | ~30 GB. The agents use ~20 tools, not 600. |
| `gdb` | Blocked by the seccomp profile (`ptrace` is denied). See "Known Limitations" in `docs/optimization/p3-1-delivery-report.md`. |
| `setuid` binaries | The image runs as `pentest` (uid 1000) — setuid is pointless and adds a 0-day surface. |
| `metasploit-framework` **database** (postgres) | The Runner doesn't run msfconsole in daemon mode; msf CLI is fine without the DB. |
| `wireshark` GUI | The agents run headless. `tshark` (CLI) is installed. |
| `burpsuite` GUI | The agents drive `zaproxy` programmatically; Burp is included for ad-hoc operator use only. |

## Versioning

The image inherits rolling Kali's release cadence — there is
no frozen tag. Operators who need reproducibility should pin
the seccomp profile (which IS versioned in the repo) rather
than the image. Tools are version-stable at any given pull,
but a `docker pull` 6 months later can shift minor versions.

## /etc/psa/manifest.json

Each build writes a small JSON document at
`/etc/psa/manifest.json` describing the installed tools and
their resolved versions. The schema (from
`internal/tools/docker/manifest.go`) is:

```json
{
  "image": "psa/kali:dev",
  "built_at": "2026-06-03T00:00:00Z",
  "user": "pentest:1000",
  "workdir": "/work",
  "tools": [
    {"name": "nmap", "version": "7.94+git20230802"},
    {"name": "sqlmap", "version": "1.7.11"}
  ]
}
```

When `Config.VerifyManifest = true`, the Runner probes this
file (via a one-shot `cat` in a throwaway second container)
and records the parsed result on `Result.Manifest`. The probe
adds < 200ms on a cached image. See
`docs/optimization/p3-1-delivery-report.md` for the security
rationale (image-substitution detection).

To inspect manually:

```bash
docker run --rm psa/kali:dev cat /etc/psa/manifest.json | jq .
```

## Seccomp

Build with `--security-opt seccomp=/path/to/pentest-swarm.json`
in the container create options. The P3-1 standard profile
(`deploy/docker/seccomp/pentest-swarm.json`) blocks mount,
kexec_load, ptrace, bpf, perf_event_open, init_module, etc.
The Runner attaches the profile automatically when
`Config.SeccompProfile` is set:

```go
profile, _ := docker.LoadSeccompProfile(
    "deploy/docker/seccomp/pentest-swarm.json")
r := docker.NewRunner(cli, docker.Config{
    SeccompProfile: profile,
    Isolation: docker.Isolation{
        Network:        docker.NetworkNone,
        ReadonlyRootfs: docker.ReadonlyRootfsReadOnly,
    },
})
```

## See also

- `Dockerfile` — build recipe
- `entrypoint.sh` — runtime entrypoint
- `../../seccomp/pentest-swarm.json` — seccomp profile
- `../../../../internal/tools/docker/seccomp.go` — Go-side loader
- `../../../../docs/optimization/p3-1-delivery-report.md` — P3-1 delivery report
