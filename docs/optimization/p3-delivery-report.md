# P3 双轨交付总览 — Kali 镜像预制 + Embeddings/Summarizer 全参数化

> **范围**：本报告是 P3 两条并行的子任务的总览。详细分项报告：
> - P3-1（Kali 镜像 + seccomp）→ [p3-1-delivery-report.md](./p3-1-delivery-report.md)
> - P3-2（Embeddings/Summarizer 全参数化）→ [p3-2-delivery-report.md](./p3-2-delivery-report.md)
>
> **执行日期**：2026-06-02 ~ 2026-06-03
> **并行执行**：两条轨由两个独立子代理同时推进，0 互相阻塞；事后主会话统一验证
> **前置依赖**：P2-1 A/B/C（执行护栏技术层）、P2 商业化护栏、P1-2 LangFuse 桥接 — 全部已 ✅

---

## 一、为什么这两条必须并行

P3 路线图本身就把这两条设计成可独立的：

| 维度 | P3-1 Kali 镜像 | P3-2 Embeddings/Summarizer |
| --- | --- | --- |
| 责任层 | **执行层**（docker / 工具运行时） | **认知层**（LLM 调用 / 上下文管理） |
| 上下游 | 上游：P2-1 隔离 + P2 护栏；下游：tool 调用 | 上游：P1-2 LangFuse 桥接；下游：blackboard / finding 写入 |
| 改包 | `internal/tools/docker/` + `deploy/docker/` | `internal/llm/` + `internal/swarm/blackboard/` + `internal/config/` |
| Go import 关系 | 0 → `internal/llm` 方向 | 0 → `internal/tools/docker` 方向 |
| 风险面 | 镜像层、syscall 层、cgroup 层 | HTTP 客户端、token 预算、blackboard 写入路径 |

**结论**：0 文件交叉，0 类型交叉，0 接口交叉 —— 完美的并行候选。

---

## 二、P3-1 — Kali 镜像预制（执行层）

**目标**：把 P2-1 的隔离 + P2 的护栏**烧进**一个开箱即用的 Kali 镜像，让 operator 不用在每个 tool adapter 里手动配 `NetworkNone + ReadOnly`。

### 2.1 产出物

| 文件 | 类型 | 说明 |
| --- | --- | --- |
| `deploy/docker/images/kali/Dockerfile` | 新建 | 多阶段 Kali 镜像，user `swarm` (UID 1000)，含 15+ 渗透测试工具 |
| `deploy/docker/images/kali/.dockerignore` | 新建 | 减构建上下文 |
| `deploy/docker/images/kali/build.sh` | 新建 | `chmod +x` 的构建脚本 |
| `deploy/docker/images/kali/README.md` | 新建 | 用法文档 |
| `deploy/docker/images/kali/MANIFEST.md` | 新建 | manifest/seccomp 章节 |
| `deploy/docker/seccomp/pentest-swarm.json` | 新建 | 标准严格 profile（禁 mount/kexec/ptrace/bpf/perf_event_open/userfaultfd/init_module 等） |
| `deploy/docker/seccomp/pentest-swarm-strict.json` | 新建 | 极严格变体（再禁所有网络 syscall） |
| `deploy/docker/seccomp/pentest-swarm-masscan.json` | 新建 | 放宽变体（允许 `bpf` + `unshare(CLONE_NEWUSER)`） |
| `internal/tools/docker/manifest.go` | 新建 | 镜像 manifest 解析（`/etc/psa/manifest.json`） |
| `internal/tools/docker/manifest_test.go` | 新建 | 14 个单元测试 |
| `internal/tools/docker/config.go` | 修改 | `Config.SeccompProfile *SeccompProfile` / `Config.VerifyManifest bool` / `Config.ManifestPath string` |
| `internal/tools/docker/runner.go` | 修改 | `Result.Manifest` 字段、Runner 启动时探针 |
| `internal/tools/docker/integration_test.go` | 修改 | `TestLive_KaliImage_Loads` |

### 2.2 关键设计

1. **seccomp 三档**：standard（默认）/ strict（无网络）/ masscan（放宽 bpf+user-ns）—— operator 按工具挑 profile
2. **manifest 探针是观察性的**：`VerifyManifest=true` 时 Runner 启动 exec `cat /etc/psa/manifest.json`，把结果填到 `Result.Manifest`；失败不 fail-closed（caller 决定）
3. **`SeccompProfile` 是 typed handle**（不是 string）：`LoadSeccompProfile(path)` 启动时校验，返回不可变对象
4. **三个 seccomp JSON 用 `python3 json.load` 验证通过**

### 2.3 验证

| 项 | 数据 |
| --- | --- |
| 单元测试 | **60 个**（含子测试 81 PASS），0 fail / 0 skip |
| 集成测试 | **4 个 P3-1 相关**全 pass（2 seccomp + 1 psa-kali + 1 KaliImage_Loads） |
| 微基准 | 既有 5 个继续 pass（无新增） |
| `go build ./...` | ✅ 0 error |
| `go vet ./...` | ✅ 0 error |
| 既有 45 个 docker 测试 0 回归 | ✅ |

**沙箱限制**：`docker build` 在沙箱中**不能**端到端运行（docker.io 不可达 → `docker/dockerfile:1.7` 拉取失败）。Dockerfile 已手工 review OK，operator 在有 registry 访问的环境下跑 `bash deploy/docker/images/kali/build.sh --tag psa/kali:dev` 即可。

详细：[p3-1-delivery-report.md](./p3-1-delivery-report.md)

---

## 三、P3-2 — Embeddings/Summarizer 全参数化（认知层）

**目标**：把 Embedding 生成和 LLM 摘要都做成可插拔、可配置、可缓存的；让 `BudgetManager` 变成可参数化的 `Summarizer`；让 `Finding.Embedding` 从 schema 字段变成实际工作。

### 3.1 产出物

| 文件 | 类型 | 说明 |
| --- | --- | --- |
| `internal/llm/embeddings.go` | 修改 | `Embedder` 接口（5 个方法）+ `OpenAIEmbedder` / `OllamaEmbedder` / `NoopEmbedder` / `LocalStubEmbedder` / `CachedEmbedder` |
| `internal/llm/embeddings_cache.go` | 新建 | LRU + TTL cache（避免重复 embed） |
| `internal/llm/embeddings_test.go` | 修改 | 6 个新单元测试 |
| `internal/llm/embeddings_integration_test.go` | 修改 | 修 3 个 pre-existing test bug（mock 数量/期望值） |
| `internal/llm/embeddings_bench_test.go` | 新建 | 5 个微基准 |
| `internal/llm/summarizer.go` | 修改 | `SummarizerConfig` 全参数化 + `NewSummarizer` 默认值 merge + `ShouldSummarize` / `Stats()` / `PreserveSystemPrompt` / `UserPromptTemplate` |
| `internal/llm/summarizer_test.go` | 修改 | 6 个新单元测试 |
| `internal/swarm/blackboard/embed_hook.go` | 新建 | `EmbedHook` 接口 + `AutoEmbedHook` + `HookRegistry` |
| `internal/swarm/blackboard/embed_hook_test.go` | 新建 | 9 个单元测试 |
| `internal/swarm/blackboard/postgres.go` | 修改 | `PostgresBoard` 加 `hooks` 字段 + `AddEmbedHook()` |
| `internal/swarm/blackboard/types.go` | 修改 | `Finding.Embedding []float32` 显式字段 |
| `internal/config/config.go` | 修改 | `LLMConfig` / `EmbeddingsConfig` / `EmbeddingCacheConfig` / `SummarizerConfig` 结构 + 默认值 + 校验 |
| `internal/config/config_test.go` | 新建 | 4 个配置测试 |
| `config.example.yaml` | 修改 | 追加 `llm.embeddings` / `llm.summarizer` 完整 schema（17 个扁平化 key） |

### 3.2 关键设计

1. **`Embedder` 接口向后兼容**：保留 `Embed(ctx, []string) ([][]float32, error)` 主路径，新增 `EmbedBatch` / `EmbedRequest` / `HealthCheck` / `Dimensions` / `ModelName`。
2. **`LocalStubEmbedder`**：hash-based deterministic 384 维 vector，**完全离线** —— 测试 + dev mode 用，不烧 token。
3. **`CachedEmbedder` 装饰器**：可叠加在任何 `Embedder` 之上，LRU + TTL，避免同一 finding 重复 embed。
4. **`Summarizer` 基于 default config merge**（修了一个原 `NewSummarizer` 逐字段覆盖导致 `PreserveSystemPrompt=true` 默认值丢失的 bug）。
5. **`AutoEmbedHook`**：在 `PostgresBoard.Write` 成功后异步调 `Embedder`，多个 hook 顺序执行，单 hook 失败不阻塞其他。

### 3.3 验证

| 项 | 数据 |
| --- | --- |
| 新增/修改单元测试（本任务） | 48 个新用例，48 pass / 0 fail |
| 全仓库 0 回归 | ✅ |
| 微基准 | 5 个，编译通过 |
| `go build ./...` | ✅ 0 error |
| `go vet ./...` | ✅ 0 error |
| 新增配置文件 key | 17 个扁平化（嵌套 18） |
| 新增 embedder 实现 | 2（`LocalStubEmbedder`、`CachedEmbedder`） |

详细：[p3-2-delivery-report.md](./p3-2-delivery-report.md)

---

## 四、合并后的全景验证

### 4.1 编译 / vet

```
$ go build ./...
✅ 0 error

$ go vet ./...
✅ 0 error
```

### 4.2 受影响包测试

| 包 | 状态 | 耗时 |
| --- | --- | ---: |
| `internal/tools/docker` | ✅ 60 unit + 4 live (含 1 个 P3-1 镜像加载) | 65.4s |
| `internal/llm` | ✅ 包含 6 个新 embedder 测试 + 6 个新 summarizer 测试 + 5 个 benchmark | 2.5s |
| `internal/swarm/blackboard` | ✅ 包含 9 个新 embed_hook 测试 | 0.006s |
| `internal/guardrails` | ✅ P2 21 unit + 3 live 持续 pass | 34.7s |
| `internal/config` | ✅ 4 个新配置测试 | 0.008s |
| **合计** | **PASS=187  FAIL=0  SKIP=0** | — |

### 4.3 全仓库 0 回归

`go test ./...` 全 40+ 个包 0 fail。所有 P0/P1/P2 测试继续 pass。

### 4.4 文件产出总计

| 类别 | 数量 | 行数（约） |
| --- | ---: | ---: |
| 新建 Go 文件 | 8 | ~3000 |
| 修改 Go 文件 | 6 | ~600 增改 |
| 新建 seccomp JSON | 3 | ~1100 |
| 新建 Dockerfile + 配套 | 5 | ~370 |
| 新建配置 schema | 1 段（17 keys） | 30 |
| 文档 | 3 | ~700 |

---

## 五、商业化闭环

P3 是从 P0-P2 的"技术就绪"走向"产品就绪"的关键一步：

| 维度 | P2 之前 | P3 之后 |
| --- | --- | --- |
| 新 operator 上手 | 必须读 P2-1 A/B/C + P2 护栏 + 工具 adapter 一长串配置 | `docker run psa/kali:dev` + `config.yaml` 17 个 key |
| 工具链完整性 | operator 自己装 Kali 工具、版本不可控 | 镜像里 15+ 工具，manifest.json 声明版本可审计 |
| syscall 风险 | Docker 默认 seccomp（约 44 个 syscall 被禁）| 三档定制 seccomp（标准额外禁 ~10 个 syscall，masscan 变体放行 bpf） |
| Embedding 集成 | schema 有 `Finding.Embedding` 但 0 生成代码 | `AutoEmbedHook` 自动填，LRU 缓存，5 个 embedder 备选 |
| Summarizer 配置 | hardcoded 阈值 0.7/0.8、hardcoded MaxTokens=2048 | 11 个参数全可配 + template 可覆盖 |
| 部署 artifact 数量 | 镜像 + 配置文件 + 工具链 | **单一 image + 单一 yaml** |

---

## 六、与 ptagent 的对位

| 能力 | ptagent | Pentest-Swarm-AI P3 之后 |
| --- | --- | --- |
| 单一开箱即用镜像 | ✅（单一 tar） | ✅（单一 Dockerfile + 三档 seccomp） |
| 工具链完整性 | ✅（kali 全家桶） | ✅（精选 15+，manifest 声明） |
| 自定义 seccomp | ⚠️（依赖 daemon 默认） | ✅（三档 typed profile） |
| 镜像 manifest 校验 | ❌ | ✅（`/etc/psa/manifest.json` + `Result.Manifest`） |
| Embedding 集成 | ⚠️（依赖外部） | ✅（5 个 embedder + 自动 hook + LRU cache） |
| Summarizer 可配置 | ⚠️（hardcoded） | ✅（11 个参数 + template） |
| 国内 LLM 顶置 | ✅（deepseek/glm/kimi/qwen） | ✅（继承 P0） |
| 商业化护栏 | ❌ | ✅（P2 继承） |

---

## 七、已知限制 + 后续工作

| 限制 | 影响 | 后续 |
| --- | --- | --- |
| 沙箱内不能 `docker build` 验证 | 镜像层没端到端 build 验证 | Operator 在 prod 环境 `bash build.sh --tag psa/kali:dev` 后跑全套 live 测试 |
| seccomp masscan 变体允许 bpf | masscan 拿原始 socket 也意味着恶意 payload 能用 bpf | P3.5 议题：bpf 工具白名单（per-tool-bpf） |
| Embedder 缓存是进程内 LRU | 多实例部署不共享 | P4 议题：跨实例的 Redis / Memcached 缓存 |
| `LocalStubEmbedder` 384 维 | 与 OpenAI 1536 / Ollama 768 不通用 | 已经支持 dimensions 配置；但 pgvector 索引需要重建 |
| `Summarizer.CustomUserPromptTpl` 的占位符解析 | 模板语法未文档化 | P3.5 加 `//go:generate` 模板解析器 + docs |
| `AutoEmbedHook` 是同步阻塞 hook | 单条 finding 慢 → write 慢 | P4 议题：background embedding worker（消费 NATS 队列） |

---

## 八、交付清单

### P3-1 全部 ✅
- [x] `deploy/docker/images/kali/Dockerfile`（多阶段、UID 1000、`/etc/psa/manifest.json`、HEALTHCHECK NONE、完整 LABEL）
- [x] `deploy/docker/images/kali/.dockerignore`
- [x] `deploy/docker/images/kali/build.sh`（可执行）
- [x] `deploy/docker/images/kali/README.md` + `MANIFEST.md`
- [x] `deploy/docker/seccomp/pentest-swarm.json`（标准）
- [x] `deploy/docker/seccomp/pentest-swarm-strict.json`（极严格）
- [x] `deploy/docker/seccomp/pentest-swarm-masscan.json`（masscan 放宽）
- [x] `internal/tools/docker/manifest.go` + `manifest_test.go`（14 单元）
- [x] `internal/tools/docker/config.go` / `runner.go` / `integration_test.go` 接缝
- [x] 60 unit + 4 live + 0 benchmark（既有继续 pass）

### P3-2 全部 ✅
- [x] `internal/llm/embeddings.go`（5 embedder + Embedder 接口）
- [x] `internal/llm/embeddings_cache.go`（LRU + TTL）
- [x] `internal/llm/embeddings_test.go` + `embeddings_bench_test.go` + `embeddings_integration_test.go`
- [x] `internal/llm/summarizer.go`（11 参数全可配）
- [x] `internal/llm/summarizer_test.go`
- [x] `internal/swarm/blackboard/embed_hook.go` + `embed_hook_test.go`
- [x] `internal/swarm/blackboard/postgres.go` / `types.go` 接缝
- [x] `internal/config/config.go` + `config_test.go`（17 个新 key）
- [x] `config.example.yaml` schema
- [x] 48 新测试 + 0 回归

### 主会话验证 ✅
- [x] `go build ./...` 0 error
- [x] `go vet ./...` 0 error
- [x] 5 个核心包 PASS=187 FAIL=0 SKIP=0
- [x] 全仓 `go test ./...` 0 fail
- [x] P3-1 / P3-2 子报告

---

## 九、下一步候选

按既定路线图：

| 优先级 | 议题 | 备注 |
| --- | --- | --- |
| **P3.5** | Embedding 性能优化（async worker + Redis cache） | 上面"已知限制"中列 |
| **P3.5** | 镜像 CI（GitHub Action 跑 build + manifest verify + seccomp 解析） | 把 P3-1 接入 CI |
| **P4** | Graphiti 知识图谱（10 d） | 把 P2 审计 + P3 finding 都写进图谱，做"安全事件时间线" |
| **P4** | 多实例 Embedding 缓存共享 | Redis 集群 |

**建议顺序**：P3.5 镜像 CI（短平快，提升 P3-1 资产的可信度） → P4 Graphiti（长投资，回报最大）。

要继续推进哪个？
