# P5+ 观测 (Observability) — Operations Guide

> **Status**: P5+ 观测 stage, signed off 2026-06-03.
> **Owner**: `@pentestswarm/observability`
> **Surfaces touched**: backend (Go), `deploy/`, `docs/`

This document explains how to wire the Pentest-Swarm-AI process
metrics into an existing Prometheus / Grafana / pg_exporter
deployment, and what to expect when you scrape
`http://pentestswarm:8080/metrics`.

---

## 1. What's exposed

The server exposes three endpoints, **outside** the `/api/v1`
prefix because they're scraped by infrastructure, not the
front-end:

| Path | Format | Auth | Purpose |
| --- | --- | --- | --- |
| `GET /metrics` | Prometheus 0.0.4 text | none | Scrape target |
| `GET /healthz` | JSON | none | Liveness probe |
| `GET /readyz` | JSON | none | Readiness probe (200 iff orchestrator provider is configured) |

The 200 / 503 split on `/healthz` and `/readyz` is the standard
Kubernetes probe contract.

---

## 2. Metric inventory

The zero-dependency registry in `internal/observability/metrics/`
exports the metric types below. The application bundle
`internal/observability/appmetrics` registers the canonical
process / HTTP / WS / LLM / tool metrics on top.

### 2.1 Process (auto-registered)

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `process_start_time_seconds` | gauge | — | Unix epoch of process start |
| `process_uptime_seconds` | gauge | — | Now − start_time, refreshed on each scrape |

### 2.2 HTTP layer (request-timing middleware)

| Metric | Type | Labels |
| --- | --- | --- |
| `psa_http_requests_total` | counter | `method`, `path`, `status` |
| `psa_http_request_duration_seconds` | histogram | `method`, `path` |
| `psa_http_in_flight_requests` | gauge | — |

- `path` is the Fiber **route pattern** (e.g. `/api/v1/campaigns/:id`),
  not the raw URL — cardinality is bounded by the route table.
- `status` is bucketed: `1xx` / `2xx` / `3xx` / `4xx` / `5xx`. A
  request to an unmatched route is labelled `<unmatched>`.

### 2.3 WebSocket layer

| Metric | Type | Labels |
| --- | --- | --- |
| `psa_ws_active_connections` | gauge | `campaign_id` |
| `psa_ws_messages_total` | counter | `kind` (`event` / `finding`) |
| `psa_ws_broadcast_errors_total` | counter | `campaign_id`, `kind` |

### 2.4 Campaign / finding

| Metric | Type | Labels |
| --- | --- | --- |
| `psa_campaigns_created_total` | counter | — |
| `psa_campaigns_by_state` | gauge | `state` |
| `psa_findings_emitted_total` | counter | `severity` |
| `psa_findings_by_severity_total` | counter | `severity` |

The by-state gauge is **re-swept from the source of truth on every
`GET /api/v1/stats` call** — i.e. a missed state transition
self-heals on the next scrape instead of accumulating drift.

### 2.5 LLM (decorator on the engine's `llm.Provider`)

| Metric | Type | Labels |
| --- | --- | --- |
| `psa_llm_calls_total` | counter | `provider`, `model` |
| `psa_llm_errors_total` | counter | `provider`, `model` |
| `psa_llm_tokens_in_total` | counter | `provider`, `model` |
| `psa_llm_tokens_out_total` | counter | `provider`, `model` |
| `psa_llm_call_duration_seconds` | histogram | `provider`, `model` |

`provider` is derived heuristically from the model name prefix
(`claude-3-5-sonnet` → `claude`, `gpt-4o` → `gpt`,
`qwen-plus` → `qwen`, etc.). When in doubt, set the model name
explicitly in your LLM preset.

### 2.6 Tool execution (placeholders)

| Metric | Type | Labels |
| --- | --- | --- |
| `psa_tool_calls_total` | counter | `tool_name` |
| `psa_tool_errors_total` | counter | `tool_name` |
| `psa_tool_call_duration_seconds` | histogram | `tool_name` |

> **Status**: registered, but no call sites yet. The next
> iteration of `internal/tools/` will wrap every tool
> execution in a metrics call; the metric is already exported
> so a Prometheus query that expects the series doesn't 404.

---

## 3. Quick start

### 3.1 Local sandbox (zero infra)

```bash
go run ./cmd/pentestswarm serve --port 8080
# in another shell:
curl -s http://localhost:8080/metrics | head -30
curl -s http://localhost:8080/healthz
curl -s http://localhost:8080/readyz
```

You should see the auto-registered process metrics
(`process_start_time_seconds`, `process_uptime_seconds`)
immediately, even before any traffic. As soon as you make an
HTTP call, the corresponding `psa_http_*` line appears.

### 3.2 Full stack (Prometheus + pg_exporter + Grafana)

The `deploy/docker-compose.observability.yml` file brings up
Prometheus, pg_exporter, and Grafana on a shared
`psa_observability` network. Pre-requisite: the main app
compose is already running on `deploy/docker-compose.yml`.

```bash
# Terminal 1 — main app
docker compose -f deploy/docker-compose.yml up -d

# Terminal 2 — observability
docker compose -f deploy/docker-compose.observability.yml up -d

# Open
open http://localhost:9090   # Prometheus
open http://localhost:3000   # Grafana (admin / admin)
```

The Prometheus job list (Targets page) shows three green
targets: `pentestswarm:8080`, `pg_exporter:9187`, and
`prometheus:9090`.

The Grafana dashboard `Swarm` (auto-provisioned) shows four
panels: HTTP rate, WS rate, Findings by severity, LLM call rate.

### 3.3 Existing Prometheus (add this scrape job)

Append to your existing `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: pentestswarm
    metrics_path: /metrics
    static_configs:
      - targets: ["pentestswarm.internal:8080"]
        labels:
          service: psa-app
```

Reload Prometheus (`curl -X POST prometheus:9090/-/reload`) and
the new targets appear within 15 s.

---

## 4. Why zero-dependency?

We deliberately did not pull in `prometheus/client_golang` for
this stage. The reason: ptagent's `pg_exporter` sidecar is the
*only* observability infra the comparison requires, and adding
a 200+ KiB dependency for ~500 lines of text-format serialiser
felt like overkill.

The custom registry exports the standard Prometheus 0.0.4 text
format, so it is wire-compatible with:

- vanilla Prometheus (`prom/prometheus:v2.x`)
- Grafana Agent / Mimir / VictoriaMetrics
- Datadog Agent (Prometheus check)
- New Relic (Prometheus integration)
- CloudWatch Container Insights via OTel collector

The 4 metric types we expose — counter, gauge, histogram, and
the auto-registered process gauges — cover ~95% of what SREs
need from a long-running process. Anything fancier
(summaries, exemplars) is an additive change to the registry
and a follow-up P-stage.

---

## 5. Future work (out of P5+ 观测 scope)

- **Exemplars** on `psa_llm_call_duration_seconds` — would
  attach a LangFuse / OTel trace id to each LLM call. Requires
  openmetrics text format (v0.0.4 → v1.0.0) which is a
  breaking Prometheus change.
- **Build info** in the `psa_build_info` gauge — already
  registered, but not populated. A 5-line `main.go` patch
  would set the version + git SHA at build time.
- **Trace ratio** — currently every LLM call is wrapped in
  LangFuse AND metrics, which double-counts the latency
  histogram. A `trace_ratio` sample in the histogram would let
  an SRE compare observed latency with and without tracing.

---

## 6. Tests

```
$ go test ./internal/observability/...
ok    internal/observability
ok    internal/observability/appmetrics
ok    internal/observability/langfuse
```

Coverage breakdown:

- `internal/observability`: 6 tests (counter, gauge, histogram,
  duplicate-register, /metrics endpoint, /healthz, /readyz,
  AllOf composition)
- `internal/observability/appmetrics`: 4 tests (Complete
  records all, error path, latency histogram, Stream,
  Wrap interface)
- `internal/api/ws`: 3 metric-hook tests added (metric-free
  no-op, broadcast-without-subscribers, active-connections
  gauge)
- `internal/api`: 2 middleware tests (records request, nil-
  bundle no-op) + status-class boundary table
