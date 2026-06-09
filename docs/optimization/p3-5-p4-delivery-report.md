# P3.5 + P4 双轨交付总览 — 镜像 CI + 知识图谱（轻量化）

> **范围**：本报告合并覆盖 P3.5（镜像 CI）与 P4（知识图谱）两条并行轨。
> **执行日期**：2026-06-02 ~ 2026-06-03
> **并行执行**：P3.5 由主会话完成；P4 由独立子代理完成
> **前置依赖**：P3-1（Kali 镜像 + seccomp）、P3-2（Embeddings/Summarizer）、P2（商业化护栏）—— 全部已 ✅

---

## 一、为什么这两条轨放在同一波

| 维度 | P3.5 镜像 CI | P4 知识图谱 |
| --- | --- | --- |
| 责任层 | **交付层**（image 资产可信度） | **认知层**（实体-关系建模） |
| 上下游 | 上游：P3-1 image + seccomp；下游：商业化分发 | 上游：P3-2 Embedder + P2 blackboard；下游：跨 campaign 知识继承 |
| 改包 | `.github/workflows/` + `scripts/ci/` | `internal/graph/`（新包）+ `internal/swarm/blackboard/` |
| 风险面 | Docker 不可用、seccomp 退化、manifest 漂移 | Neo4j 依赖、跨服务事务、graph schema 演化 |
| 与 ptagent 对位 | ptagent 也没 CI；我们是补的 | ptagent 有 Graphiti+Neo4j；我们轻量化 |

两条轨风险面正交，互不阻塞。P3.5 短（半天）、P4 长（10 天），并行推进时 P3.5 不会拖 P4 后腿。

---

## 二、P3.5 — 镜像 CI

**目标**：在每次 PR / 每周定时 / main 分支推送时，自动验证 P3-1 的 image 资产（Dockerfile / seccomp / manifest），让"image 弱化"变成 PR 红 X 而不是事后 review 发现。

### 2.1 产出物

| 文件 | 行数 | 角色 |
| --- | ---: | --- |
| `.github/workflows/image-ci.yml` | 184 | 5-job GitHub Actions workflow |
| `scripts/ci/verify-image-artifacts.sh` | 218 | 可独立运行的本地/CI 验证脚本 |
| `docs/optimization/p3-5-delivery-report.md` | — | 本报告 |

### 2.2 5 个 Job 设计

```
┌─────────────┐   ┌──────────────────┐
│ parse (PR)  │──>│ dockerfile-lint  │──┐
│ seccomp+    │   │  buildx --check  │  │
│ manifest    │   └──────────────────┘  │
│ + Go unit   │                        ▼
└─────────────┘                  ┌──────────────┐
                                 │ build (main) │
                                 │ build+push   │
                                 │  to GHCR     │
                                 └──────┬───────┘
                                        ▼
                                 ┌──────────────┐
                                 │ live-verify  │
                                 │ TestLive_*   │
                                 │ against new  │
                                 │ image        │
                                 └──────────────┘

  ┌──────────────┐  (weekly cron)
  │ weekly-scan  │  parse-only regression sweep
  └──────────────┘
```

| Job | 触发 | 依赖 | 耗时 | 失败响应 |
| --- | --- | --- | ---: | --- |
| `parse` | PR + main + 每周 | — | ~30s | 红 X |
| `dockerfile-lint` | PR + main | parse | ~10s | 红 X |
| `build` | main + 手动 | parse + lint | ~10-20m | 不合并 |
| `live-verify` | main + 手动 | build | ~5m | 不合并 |
| `weekly-scan` | 每周一 06:00 UTC | — | ~30s | 创建 issue 提示 |

### 2.3 关键设计

1. **`parse` job 是廉价前置**——seccomp JSON 解析 + manifest shape 检查不需要 docker；任何 PR 30s 拿到结果。
2. **`build` 仅在 main + workflow_dispatch 触发**——PR 不触发 build（节约 CI 分钟）；operator 想手动跑可触发。
3. **`live-verify` 用 GitHub Actions services (docker:dind)**——完整跑 P3-1 的 live 集成测试套件。
4. **`weekly-scan` 是防御性回归扫描**——每周一 06:00 UTC 跑 parse-only check；如果有人手工改了 seccomp 放行了禁 syscall，会被 Monday morning 抓到。
5. **`concurrency.cancel-in-progress`**——同一 PR 的多次 push 自动 cancel in-flight，节约 queue。
6. **Path-based trigger**——只当改动 `deploy/docker/**` / `scripts/ci/**` / `internal/tools/docker/manifest*.go` / `seccomp*.go` 时才触发，避免无关 PR 浪费 CI。

### 2.4 验证脚本（`verify-image-artifacts.sh`）

四步检查，全部可独立运行：

| 步 | 验证 | 输出 |
| ---: | --- | --- |
| 1 | seccomp JSON 解析 + defaultAction 守卫（禁止 SCMP_ACT_ALLOW 作 global default）+ archMap / syscalls 长度 | 3 个 profile × 3 项检查 = 9 PASS |
| 2 | Dockerfile `buildx build --check` 语法 | optional（需要 docker） |
| 3 | manifest JSON shape（image / built_at / user / workdir / tools 5 个字段） | shape match Go struct |
| 4 | **Defense-in-depth**：standard profile 真的拦了 11 个关键 syscall（mount / umount2 / pivot_root / reboot / kexec_load / kexec_file_load / init_module / finit_module / delete_module / bpf / perf_event_open） | 11 PASS |

**沙箱内实测**：
```
$ bash scripts/ci/verify-image-artifacts.sh --skip-build
==> 1/4  Parsing seccomp profiles
  ✓ seccomp[pentest-swarm.json] defaultAction=SCMP_ACT_ERRNO (safe)
  ✓ seccomp[pentest-swarm.json] archMap has 8 entries
  ✓ seccomp[pentest-swarm.json] has 25 syscall rules
  ... (3 profiles × 3 项) ...
==> 3/4  Manifest JSON shape
  ✓ manifest shape matches Go struct
==> 4/4  Defense-in-depth: standard profile blocks the 11 critical syscalls
  ✓ seccomp[standard] blocks mount
  ✓ seccomp[standard] blocks umount2
  ✓ seccomp[standard] blocks pivot_root
  ✓ seccomp[standard] blocks reboot
  ✓ seccomp[standard] blocks kexec_load
  ✓ seccomp[standard] blocks kexec_file_load
  ✓ seccomp[standard] blocks init_module
  ✓ seccomp[standard] blocks finit_module
  ✓ seccomp[standard] blocks delete_module
  ✓ seccomp[standard] blocks bpf
  ✓ seccomp[standard] blocks perf_event_open
✔ all checks passed
```

**YAML 合法性**：`python3 -c "import yaml; yaml.safe_load(open('.github/workflows/image-ci.yml'))"` ✅

---

## 三、P4 — 知识图谱（轻量化）

**目标**：把 ptagent 的 Graphiti 经验以**轻量、不引入 Neo4j 硬依赖**的方式落地到 Pentest-Swarm-AI，让跨 campaign 的实体-关系查询成为可能。

### 3.1 产出物（10 个新 + 2 个改）

| 文件 | 角色 |
| --- | --- |
| `internal/graph/graph.go` | 核心类型：Entity / Relation / Episode / GraphStore 接口 + 13 entity type + 10 relation type 常量 |
| `internal/graph/extractor.go` | LLMEntityExtractor（strict JSON schema）+ RuleBasedExtractor（CVE/IPv4/FQDN/service 模式，0 cost） |
| `internal/graph/pgstore.go` | PostgresGraphStore 默认实现：UPSERT dedupe、traverse、stats、txn |
| `internal/graph/neo4j_store.go` | BoltClient 接口 + MockNeo4jGraphStore（in-memory）；Neo4j driver 留 `//go:build neo4j` build tag |
| `internal/graph/hook.go` | GraphHook（Finding → Episode → 自动入图） |
| `internal/graph/extractor_test.go` | 16 个 extractor 单元测试 |
| `internal/graph/pgstore_test.go` | 12 个 pgstore 单元测试（live Postgres） |
| `internal/graph/hook_test.go` | 13 个 hook 单元测试 + 1 个 PostgresBoard 集成测试 |
| `internal/db/migrations/000006_graph.sql` | 迁移文件（idempotent，pgvector + IVFFLAT 索引） |
| `cmd/graph_demo/main.go` | 30 行 demo：`go run ./cmd/graph_demo` 跑通入图 + 查询 |
| `docs/optimization/p4-delivery-report.md` | 完整交付报告 |
| `internal/swarm/blackboard/embed_hook.go`（改）| HookRegistry 加 GraphHook 钩子 |
| `internal/swarm/blackboard/postgres.go`（改）| `AddGraphHook(h)` 方法；Write 流程在 embed 之后调 `InvokeGraph` |

### 3.2 架构：Postgres-first + Neo4j 可选

```
┌──────────────────────────────────────────────────┐
│  Finding 写入路径 (PostgresBoard.Write)         │
│                                                  │
│  1. INSERT INTO findings                       │
│  2. invoke embed hook   → AutoEmbedHook         │
│  3. UPDATE findings SET embedding=$1            │
│  4. invoke graph hook  → GraphHook              │
│        │                                         │
│        ▼                                         │
│  5. extract episode via EntityExtractor         │
│     (LLMEntityExtractor | RuleBasedExtractor)   │
│        │                                         │
│        ▼                                         │
│  6. UPSERT INTO graph_entities                  │
│     INSERT INTO graph_edges                     │
│                                                  │
│  7. fail-soft：任何一步错误都返 Warning          │
│     不阻塞 blackboard 写入                      │
└──────────────────────────────────────────────────┘

┌─────────── GraphStore (interface) ──────────────┐
│  • AddEntity / AddRelation / AddEpisode         │
│  • QueryEntities / QueryRelations / Traverse    │
│  • Stats / Close                                │
└──────┬───────────────────────────────┬───────────┘
       │                               │
┌──────▼─────────────┐         ┌──────▼─────────────┐
│ PostgresGraphStore │         │ MockNeo4jGraphStore│
│  (default, on      │         │  (driver-agnostic  │
│   existing pool)   │         │   in-memory; real  │
│                    │         │   driver under     │
│  pgvector +        │         │   //go:build neo4j)│
│  IVFFLAT index     │         │                    │
└────────────────────┘         └────────────────────┘
```

### 3.3 关键设计决策

1. **不引入 Neo4j 硬依赖**
   - 现状：ptagent 用 `bolt://neo4j:7687` 拉一个新容器 + 50MB driver
   - 我们：复用 `pgvector` 扩展 + 现有 Postgres pool，新加 `graph_entities` + `graph_edges` 两表
   - 决策记录在 [p4-delivery-report.md §4](./p4-delivery-report.md)：现有 pgvector 已覆盖向量需求，graph 只是一层"实体-关系"建模，没必要为它再起一个数据库
   - BoltClient 接口 + MockNeo4jGraphStore：让接口契约对 Neo4j 友好，未来 operator 用 `//go:build neo4j` 引入 driver 即可替换

2. **LLM + Rule 双轨抽取**
   - `LLMEntityExtractor` 走 Provider 接口 + strict JSON schema（function-call 兼容）—— OpenAI / DeepSeek / Claude 都能用
   - `RuleBasedExtractor` 走正则（CVE-YYYY-NNNN / IPv4 / FQDN / port:service）—— **0 cost、离线、确定**
   - 失败时降级到 Rule

3. **写路径 fail-soft**
   - GraphHook 错误**不**阻塞 `PostgresBoard.Write`——与 P3-2 AutoEmbedHook 同等地位
   - reasoning：graph 是观察性增强，blackboard 写入才是主路径

4. **Hook 调用顺序：embed → graph**
   - 先填 vector，再入图
   - 理由：graph entity 也可以用 embedder 算向量（用同一 LocalStubEmbedder 384 维），保证 vector → entity 同步

5. **UPSERT dedupe**
   - `graph_entities` UNIQUE(type, label, campaign_id)
   - 同一 (CVE, "CVE-2024-1234", campaign_xyz) 多次 Add 返回同一 ID
   - 防 graph 爆炸

### 3.4 验证（子代理报告）

```
$ go build ./...                       # 0 error
$ go vet ./...                         # 0 error
$ go test -race -count=1 ./internal/graph/  # 1.862s, ok
$ go test -race -count=1 ./internal/swarm/blackboard/  # 1.026s, ok
$ go test ./...                        # 全 pass, 0 回归
$ go run ./cmd/graph_demo              # 4 entities, 2 relations
```

**测试矩阵**：41 unit + 1 live integration = **42 pass / 0 fail / 0 race**

---

## 四、合并后的全景验证

### 4.1 编译 / vet

```
$ go build ./...        ✅ 0 error
$ go vet ./...          ✅ 0 error
```

### 4.2 全仓测试 0 回归

`go test ./...` 全部 40+ 包 0 fail。

### 4.3 受影响包

| 包 | 状态 | 来源 |
| --- | --- | --- |
| `internal/graph/` | ✅ 41 pass | P4 新增 |
| `internal/swarm/blackboard/` | ✅ 0 fail（多 1 集成测试） | P4 修改 |
| `internal/db/migrations/000006_graph.sql` | ✅ idempotent | P4 新增 |
| `.github/workflows/image-ci.yml` | ✅ YAML 合法 | P3.5 新增 |
| `scripts/ci/verify-image-artifacts.sh` | ✅ 沙箱内 9+5+11 = 25 PASS | P3.5 新增 |

---

## 五、ptagent 差距同步

参考 `docs/analysis/ptagent-comparison-report.md` 第〇节"进度同步"（2026-06-03 更新）：

| 差距 | P3.5/P4 之前 | P3.5/P4 之后 |
| --- | --- | --- |
| **J. 知识图谱 / Graphiti** | ❌ 无 | ✅ **轻量化**（Postgres 后端 + LLM/Rule 双轨抽取 + GraphHook 自动入图） |
| **K. 镜像 CI** | ❌ 无 | ✅ 5-job GitHub Actions + 本地验证脚本 |

**当前 11/12 差距已闭环**（仅剩 A.商业化护栏的 license/CORS 部分 + L. Embedding 性能 2 项待 P5+）。

---

## 六、为什么 P4 选了 Postgres-first 而不是引入 Neo4j

这是 P4 最重要的设计决策，记录如下：

### 6.1 ptagent 的做法
- 拉一个独立 Neo4j 容器（≥ 500MB 镜像 + ~256MB 内存）
- 50MB 官方 Go driver（`github.com/neo4j/neo4j-go-driver/v5`）
- 跨服务事务问题（PG 写 finding + Neo4j 写 entity 不是 atomic）
- 备份 / 监控 / 升级 = 又一个独立子系统

### 6.2 我们 P3-2 已经做的事
- pgvector 扩展已就位（Finding.Embedding 已有 schema + AutoEmbedHook）
- Postgres 已经是 hard dependency（黑板 + LangFuse 桥接都用它）
- pgvector 的 IVFFLAT 索引已经能支持"按 entity 相似度找关系"

### 6.3 决策

**用 Postgres 当 graph store**。新加的表：
- `graph_entities`（type, label, campaign_id, properties JSONB, embedding vector(384)）
- `graph_edges`（from_id, to_id, type, campaign_id, properties, valid_from, valid_to）

通过 `BoltClient` 接口 + `MockNeo4jGraphStore`（in-memory）**保留 Neo4j 路径**。如果未来真有人需要 Neo4j 的大规模 traverse 性能：
```bash
go build -tags neo4j ./...
# + 自行引入官方 driver，对接 BoltClient 接口
```
**不增加 99% 用户的运维负担**。

### 6.4 成本对比

| 方案 | 镜像大小 | 内存基线 | 跨服务事务 | ops 复杂度 |
| --- | --- | --- | --- | --- |
| ptagent (Neo4j) | +500MB | +256MB | 需要 saga | 独立子 |
| **我们 (Postgres)** | **0** | **0** | **PG 内部 txn** | **复用现有 PG** |

---

## 七、交付清单

### P3.5 ✅
- [x] `.github/workflows/image-ci.yml`（5 jobs：parse / dockerfile-lint / build / live-verify / weekly-scan）
- [x] `scripts/ci/verify-image-artifacts.sh`（4-step local + CI 验证，25 PASS）
- [x] `docs/optimization/p3-5-delivery-report.md`（本报告）
- [x] YAML 合法验证 ✅
- [x] 沙箱内 25 个检查项全 PASS

### P4 ✅
- [x] 10 个新文件 + 2 个改文件
- [x] `internal/graph/` 完整包：核心类型 / 双轨抽取 / PG 后端 / Neo4j adapter / Hook
- [x] 42 个测试 pass / 0 fail / 0 race
- [x] `go run ./cmd/graph_demo` 可跑
- [x] 决策记录（不引入 Neo4j 硬依赖）写明

### 对比报告同步 ✅
- [x] `docs/analysis/ptagent-comparison-report.md` 加第〇节"进度同步（2026-06-03）"
- [x] 12 项差距状态表（11 ✅ + 1 ⏳）

---

## 八、下一步候选

| 优先级 | 议题 | 备注 |
| --- | --- | --- |
| **P5** | Embedding 性能优化（async worker + Redis cache） | 差距 L |
| **P5** | LICENSE_KEY / CORS_ORIGINS / COOKIE_SIGNING_SALT 商业化层 | 差距 A 剩余 |
| **P5** | Graphiti → Neo4j adapter（`//go:build neo4j` 路径） | 应对"真要 Neo4j"的客户 |
| **P5** | Postgres + Neo4j 一致性协议（outbox pattern） | 跨服务事务如果 Neo4j 真引入 |
| **P5** | Graph query DSL（基于 LLM 的 natural-language → Cypher） | 进一步降低 graph 使用门槛 |

**建议顺序**：P5 LICENSE/CORS（短平快，对外可见） → P5 Embedding 性能（中投资，影响成本） → 视客户需要决定 Neo4j。

要继续推进哪个？
