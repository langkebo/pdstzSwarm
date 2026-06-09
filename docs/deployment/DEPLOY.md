# Pentest Swarm AI — Deployment Guide

> P5+ 一体化部署。本文档覆盖开箱即用的单命令部署、
> 自定义生产配置、镜像瘦身、可观测性接入、和常见排错。

---

## 1. TL;DR — 30 秒启动

```bash
# 1. 准备环境变量
cp .env.example .env
# 编辑 .env，至少填 ORCHESTRATOR_API_KEY 和 POSTGRES_PASSWORD

# 2. 启动
make docker-up           # 等价于 ./deploy/scripts/up.sh

# 3. 打开浏览器
open http://localhost:8081
```

`make docker-up` 完成后，所有服务运行在同一个 `psa_internal` 网络内；外部仅暴露 `8081`（API + Dashboard）。Postgres / Redis / Ollama 全部内网可达，operator 需要时可在 `deploy/docker-compose.yml` 中打开对应的 `ports:` 注释。

---

## 2. 架构：一张图

```
┌──────────────────────── psa_internal (bridge) ────────────────────────┐
│                                                                       │
│  ┌─────────────────┐  ┌──────────┐  ┌──────────┐  ┌──────────────┐  │
│  │  pentestswarm   │  │ postgres │  │  redis   │  │    ollama    │  │
│  │  (single image) │  │ pgvector │  │ 7-alpine │  │  (optional)  │  │
│  │                 │  │  :16     │  │          │  │              │  │
│  │ • API :8080     │──│  :5432   │──│  :6379   │──│  :11434      │  │
│  │ • /healthz      │  └──────────┘  └──────────┘  └──────────────┘  │
│  │ • /readyz       │       │             │              │            │
│  │ • /metrics      │       │             │              │            │
│  │ • /ws           │       ▼             ▼              ▼            │
│  │ • 嵌入 web      │  ┌────────┐    ┌────────┐    ┌──────────┐       │
│  │   /_next/*      │  │ pgdata │    │ redis  │    │ ollama   │       │
│  │                 │  │ vol    │    │ data   │    │ models   │       │
│  │ • nmap/nuclei/  │  └────────┘    └────────┘    └──────────┘       │
│  │   sqlmap/etc.   │                                                │
│  └─────────────────┘                                                │
│         │                                                            │
└─────────┼────────────────────────────────────────────────────────────┘
          │ host:8080 (publish)
          ▼
   ┌──────────────┐
   │  Browser /   │
   │  Caddy /     │
   │  curl / MCP  │
   │  client      │
   └──────────────┘
```

- **单镜像**：Go binary + Next.js bundle + 安全工具（Nuclei / Nmap / Sqlmap / Subfinder / ...）+ Chromium + Trufflehog / Gitleaks 二进制。
- **自动迁移**：`pentestswarm serve` 启动时自动跑 `db.Migrate`，schema 通过 `//go:embed` 内嵌；`schema_migrations` 表去重，并发安全。
- **健康检查**：每个服务都配 `healthcheck:`，`depends_on: condition: service_healthy` 保证启动顺序。

---

## 3. 部署拓扑

### 3.1 集成（生产推荐）

```bash
./deploy/scripts/up.sh
```

- 端口：8080（API + Dashboard）
- 卷：`psa_pgdata` / `psa_redis_data` / `psa_ollama_models` / `psa_reports` / `psa_home` / `psa_nuclei`
- 日志：每个容器最多 3 × 10MB（json-file driver）

### 3.2 集成 + 可观测性

```bash
./deploy/scripts/up.sh --profile=full
```

额外起：
- Prometheus（`:9090`）
- Grafana（`:3000` admin / admin）
- pg_exporter（`:9187`）

`Grafana → Dashboards → Swarm` 直接有现成的请求 / 延迟 / LLM 调用 / Finding 数量面板。

### 3.3 仅 API（无 Ollama）

外部 LLM 已经是云 API 时使用：

```bash
./deploy/scripts/up.sh --profile=no-llm
```

不启 ollama 容器，`ORCHESTRATOR_PROVIDER=claude` 走 Anthropic API。

### 3.4 开发模式

```bash
docker compose -f deploy/docker-compose.dev.yml up -d
go run ./cmd/pentestswarm serve
# 或：docker compose -f deploy/docker-compose.dev.yml --profile web up -d
#      跑 Next.js 热重载前端
```

dev compose 用 `5433/6380` 端口避免和主机已有服务冲突；Ollama 用 `psa-dev-*` 容器名，data 落 tmpfs 不会污染生产卷。

---

## 4. 持久化卷

| 名称                  | 容器路径                              | 内容                              |
| --------------------- | ------------------------------------- | --------------------------------- |
| `psa_pgdata`          | `/var/lib/postgresql/data`            | Postgres + pgvector 数据          |
| `psa_redis_data`      | `/data`                               | Redis snapshot + AOF              |
| `psa_ollama_models`   | `/root/.ollama`                       | 下载的 LLM / embedding 模型       |
| `psa_reports`         | `/reports`                            | 扫描 / 报告 / assist 输出         |
| `psa_home`            | `/home/pentester/.pentestswarm`       | License / installation_id / 日志  |
| `psa_nuclei`          | `/home/pentester/.nuclei`             | Nuclei 模板缓存                   |
| `psa_prometheus_data` | `/prometheus`                         | Prometheus TSDB（full profile）   |
| `psa_grafana_data`    | `/var/lib/grafana`                    | Grafana 配置 / 仪表板（full）     |

**删除数据**：`./deploy/scripts/down.sh --volumes`

**备份**：用 `docker run --rm -v psa_pgdata:/from -v $PWD:/to alpine tar -czf /to/pgdata-$(date +%F).tgz -C /from .` 这种模式挂卷拷出即可。

---

## 5. 环境变量

完整变量表见 [`.env.example`](../.env.example)。`deploy/docker-compose.yml` 用 `${VAR:?msg}` 强制校验：必填变量缺失时 `up` 直接失败。

### 5.1 必填

- `ORCHESTRATOR_API_KEY`：orchestrator LLM（Claude / OpenAI / DeepSeek / ...）的 API key
- `POSTGRES_PASSWORD`：数据库密码

### 5.2 推荐（生产）

- `LICENSE_PUB_KEY`：Ed25519 公钥 hex，启动时校验 license
- `CORS_ORIGINS`：逗号分隔允许的 origin，留空 = legacy `*`
- `REDIS_PASSWORD`：Redis 密码
- `PSA_PUBLIC_BASE_URL`：对外 URL，用于 OAuth 回调和 cookie Secure flag

### 5.3 可选

- `OAUTH_GOOGLE_CLIENT_ID/SECRET`、`OAUTH_GITHUB_CLIENT_ID/SECRET`
- `EMBEDDINGS_PROVIDER`：`noop` / `openai` / `ollama` / `deepseek` / `glm` / `kimi` / `qwen`
- `EMBEDDINGS_ASYNC_ENABLED=true`：开启 P5+ 嵌入异步 worker pool
- `EMBEDDINGS_REDIS_ENABLED=true`：开启 P5+ 跨进程向量缓存

---

## 6. 健康检查

`./deploy/scripts/healthcheck.sh` 汇总每个容器的 `State.Health.Status` 和 API 的 `/healthz` / `/readyz` 状态：

```
SERVICE              HEALTH       RUNNING
-------              ------       -------
psa-swarm            healthy      true
psa-postgres         healthy      true
psa-redis            healthy      true
psa-ollama           healthy      true

API /healthz         up
API /readyz          up
```

脚本支持 `--json` 方便对接监控系统（Prometheus blackbox-exporter / Nagios / 自家健康检查）。

### 6.1 `/healthz` vs `/readyz`

- `/healthz` — **liveness**：进程存活。Docker liveness 探针 / k8s `livenessProbe` 用。返回 200 = 进程没在崩溃。
- `/readyz` — **readiness**：依赖就绪。`cfg.Orchestrator.Provider` 非空才返回 200。K8s `readinessProbe` 用 — 滚动更新时旧副本先标 `unready` 排空流量再重启。
- `/metrics` — Prometheus 文本协议。包含 HTTP 请求 / LLM 调用 / 嵌入缓存 / 黑板写入等指标。

---

## 7. 自定义镜像

### 7.1 添加新工具到 toolbox

编辑根 `Dockerfile` 的 stage 3 (`tools-build`)：

```dockerfile
RUN go install github.com/newtool/cmd/newtool@latest
```

stage 4 (`runtime`) 不需要改 — `COPY --from=tools-build /out/bin/. /usr/local/bin/` 会一并带过去。

### 7.2 减小镜像

如果不需要 Nmap / Nuclei / Chromium 等重型工具，使用 `deploy/docker/Dockerfile`（slim 镜像，仅 Go binary，无工具集）。该镜像：

- 基于 `alpine:3.20`（~50 MB 基础）
- CGO=0，静态链接
- 适合 k8s 横向扩缩，工具在 host 或 sidecar 提供
- **不含 web bundle**（无 webfs）— 这种部署在前面放一个 nginx / Caddy 来服务 Next.js 静态文件

### 7.3 升级 Go 版本

修改 `Dockerfile` 三个 `FROM golang:1.25-bookworm` 行为相同 tag，并同步更新 `go.mod` 的 `go` 指令。

---

## 8. Kubernetes 部署

Helm chart 在 `deploy/helm/autopentest/` 模板目录里（**待填充**）。结构约定：

- Deployment 模板用 `pentest-swarm-ai:latest`
- 持久化用 PVC（Postgres / Redis 用云服务或独立 StatefulSet）
- 反向代理用 Ingress + cert-manager
- Prometheus 通过 `ServiceMonitor` 自动 scrape

实现细节见 [Helm chart README](../helm/autopentest/README.md)（待写）。

---

## 9. 常见排错

### 9.1 `psa-swarm` 一直 restart

```bash
docker compose -f deploy/docker-compose.yml logs --tail=100 pentestswarm
```

- `database ping failed`：Postgres 还没就绪，但 healthcheck 应该挡住；如果是 healthcheck 卡死，看 Postgres 日志。
- `auto-migrate: permission denied`：用 `chown 1000:1000 ./psa_home` 修宿主权限。
- `license: signature mismatch`：`LICENSE_PUB_KEY` 和签发 license 的私钥不匹配。

### 9.2 启动很慢 / Postgres 一直在 starting

首次启动 Postgres 初始化需要 5-10s。`pg_isready` 失败不会让 swarm 启动（`depends_on: service_healthy` 阻塞），但 docker-compose 的 `ps` 状态会一直显示 `(health: starting)`。

如果超过 60s 还没 healthy：

```bash
docker compose -f deploy/docker-compose.yml logs postgres
```

### 9.3 端口冲突

8080 被占用 → 编辑 `deploy/docker-compose.yml` 的 `ports: - "8080:8080"` 第一段。
5432 / 6379 / 11434 同理（默认是注释掉的，按需打开）。

### 9.4 Web 看不到

- 浏览器开 DevTools → Network → 看 `/` 的响应码。
- 200 但白屏：Next.js 的 `NEXT_PUBLIC_API_BASE` 没指对。集成部署里 API 和 web 同源，不需要这个变量。
- 404：Docker 镜像里的 `web/out` 目录为空（stage 1 失败）。`docker run --rm pentest-swarm-ai:latest ls /usr/local/bin` 应有 pentestswarm；用 `make docker-build` 重新构建并查看 stage 1 日志。

### 9.5 数据卷太大

`psa_ollama_models` 是大头（qwen2.5:7b ≈ 5GB，qwen2.5:32b ≈ 20GB）。要省空间：

- 用更小的模型：`ollama pull qwen2.5:1.5b`
- 切换到云 API：`ORCHESTRATOR_PROVIDER=claude` + `EMBEDDINGS_PROVIDER=noop` 整个 ollama 容器都不需要起

### 9.6 升级后旧数据兼容

`internal/db/migrations/*.sql` 是只追加的，新版本部署时 `serve` 启动会自动跑未应用的 migration。**不要**手动改已应用的 migration — `schema_migrations` 表里没记录，校验和会冲突。

---

## 10. 安全清单

生产部署前的检查项：

- [ ] `.env` 不在版本控制里（`.gitignore` 已加）
- [ ] `POSTGRES_PASSWORD` 至少 32 字符
- [ ] `REDIS_PASSWORD` 已设
- [ ] `LICENSE_PUB_KEY` 已配
- [ ] `CORS_ORIGINS` 显式列了生产域名（不要留空 = `*`）
- [ ] `MULTI_TENANT=true` 时数据库已应用 000009_tenants.sql（自动）
- [ ] 8080 前面挂了 TLS 终止（Caddyfile 已给模板）
- [ ] Postgres / Redis / Ollama 端口未对外暴露（compose 里默认就是注释的）
- [ ] `PSA_PUBLIC_BASE_URL` 是 https://
- [ ] License 未过期（`pentestswarm license verify`）

---

## 11. 镜像拓扑对照（vs ptagent）

| 维度             | ptagent                           | Pentest Swarm AI                                      |
| ---------------- | --------------------------------- | ----------------------------------------------------- |
| 镜像数           | 1（Kali 基础，~3 GB）             | 1（Debian 基础，~900 MB；Alpine slim 变体 ~120 MB）   |
| 工具安装         | apt + Kali repo                   | apt + `go install`（精确 pin）                        |
| Web 前端分发     | 内嵌进 Go binary                  | 内嵌进 Go binary（`internal/webfs`）                  |
| 启动顺序控制     | 手动（`docker run` 串行）         | `depends_on: service_healthy`                         |
| 健康检查         | 仅 Postgres                       | Postgres / Redis / Ollama / API                       |
| 数据持久化       | bind mount（散落）                | 命名卷（6 个集中管理）                                |
| 可观测性         | 无                                | Prometheus + Grafana 一键（`--profile=full`）         |
| License 校验     | 无                                | Ed25519 启动期验签（`internal/license`）              |
| CORS 配置        | 全开 `*`                          | 显式 allow-list（`internal/corsmux`）                 |
| 自动迁移         | 手动（README 里写命令）           | `serve` 启动时自动跑 `db.Migrate`                     |
| 反向代理模板     | 无                                | `deploy/Caddyfile`（HTTPS / HSTS / 路径路由）         |

**结论**：ptagent 走"单一大镜像 + 人工运维"路线；Pentest Swarm AI 走"集成 compose + 健康检查 + 持久化 + 可观测"路线，开箱即用度与可复现性都显著更高。

---

## 12. 验收清单

P5+ 一体部署议程 8 项：

- [x] 根 Dockerfile：web 嵌入 + tini + healthcheck + 4 阶段构建
- [x] `internal/webfs`：Next.js bundle `//go:embed` + SPA fallback + Cache-Control
- [x] `internal/api`：webfs handler 通过 `fasthttpadaptor` 挂到 Fiber 根路径
- [x] `cli/migrate`：one-shot 迁移 CLI（`docker compose run --rm migrate`）
- [x] `cli/serve`：启动期自动调用 `db.Migrate`（可 `PENTESTSWARM_SKIP_AUTO_MIGRATE=1` 关闭）
- [x] `deploy/docker-compose.yml`：4 镜像 + depends_on + healthcheck + 6 命名卷 + .env 校验 + log 限制 + cap_add NET_RAW
- [x] `deploy/docker-compose.full.yml`：可观测性 plane（Prometheus / Grafana / pg_exporter）
- [x] `deploy/docker-compose.dev.yml`：开发用数据平面 + 端口偏移 + Next.js hot-reload profile
- [x] `deploy/scripts/{up,down,healthcheck}.sh`：启停 + 健康检查 + JSON 输出
- [x] `deploy/Caddyfile`：HTTPS / HSTS / SSE flush_interval
- [x] `Makefile`：`docker-build / docker-up / docker-up-full / docker-down / docker-health / docker-logs / docker-ps / migrate`
- [x] `.env.example`：25 个变量分类、必填校验、注释说明
- [x] `.dockerignore`：build context 裁剪（web/node_modules / tests/bench 等）
- [x] 单测：`internal/webfs` 2 个用例（`FS()` 探测 + SPA fallback）
- [x] 文档：本文件 + 文档更新（待补）
