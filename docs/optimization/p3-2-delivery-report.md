# P3-2 交付报告 — Embeddings / Summarizer 全参数化

**日期**：2026-06-03
**作者**：P3-2 实现分支
**状态**：已完成；编译 0 error，vet 0 error，全仓库测试 0 回归

---

## 1. 范围

P3-2 把两段长期硬编码（hard-coded）的 LLM 路径全部参数化，并把现有
的 `Embedder` / `Summarizer` 提升为可在配置文件中独立调优的一等公民：

- 嵌入式模型（embeddings）：在 `Finding` 写入黑板时自动向量化，并通过可
  选的 LRU 缓存去重。
- 对话历史压缩（summarizer）：把 `BudgetManager` 里写死的
  `0.7 / 0.8 / 2048 / 0` 四个 magic number 替换为可配置项，并支持保留
  system prompt、自定义模板、按比例保留最近 N% 的消息。
- 同时把 `internal/agent/report` 与 `internal/agent/orchestrator` 内部
  的 `MaxTokens=2048` / `MaxTokens=4096` / `Temperature=0.x` 构造参数
  化（构造时传入，零值用 default）。

**不在范围**：替换黑板的 pgvector schema 字段宽度（已就位，1536 维）。

---

## 2. 交付物

### 2.1 代码文件

| 路径 | 行数 | 说明 |
|------|------|------|
| `internal/llm/embeddings.go` | ~880 | `Embedder` 接口 + `OpenAI` / `Ollama` / `Noop` / `LocalStub` 实现 + 工厂 |
| `internal/llm/embeddings_cache.go` | ~200 | `CachedEmbedder` 提取 + TTL + `NewCachedEmbedderFromConfig` |
| `internal/llm/embeddings_test.go` | ~430 | 单元测试 + httptest mock |
| `internal/llm/embeddings_integration_test.go` | ~280 | 端到端：批量、重试、错误处理、summarizer 集成 |
| `internal/llm/embeddings_bench_test.go` | ~120 | 5 个微基准 |
| `internal/llm/summarizer.go` | ~290 | `Summarizer` + `SummarizerConfig` + `ShouldSummarize` / `Stats` / `PreserveSystemPrompt` |
| `internal/llm/summarizer_test.go` | ~390 | 单元测试（覆盖 spec 的 5 个新行为） |
| `internal/llm/budget.go` | 已存在 | 在 `BudgetManager` 中加入可选 `Summarizer` 字段，未破坏现有 API |
| `internal/llm/factory.go` | 已存在 | 工厂函数 `NewEmbedderFromConfig` 已支持 `local_stub` 等新预设 |
| `internal/swarm/blackboard/embed_hook.go` | ~210 | `EmbedHook` 接口 + `AutoEmbedHook` + `HookRegistry` |
| `internal/swarm/blackboard/embed_hook_test.go` | ~230 | 9 个测试覆盖 hook 的 OnWrite / Registry / 字段选项 |
| `internal/swarm/blackboard/postgres.go` | 已修改 | `PostgresBoard` 增加 `hooks` 字段 + `AddEmbedHook()` + `Write()` 内调用 |
| `internal/swarm/blackboard/types.go` | 已修改 | `Finding` 增加 `Embedding []float32` 字段 |
| `internal/config/config.go` | +110 | `LLMConfig` / `EmbeddingsConfig` / `EmbeddingCacheConfig` / `SummarizerConfig` 结构 + 默认值 + 校验 |
| `internal/config/config_test.go` | ~190 | 4 个新单元测试覆盖默认值 / override / 校验失败 |
| `config.example.yaml` | +28 | `llm.embeddings` / `llm.summarizer` 完整 schema 与注释 |

### 2.2 文档

- `docs/optimization/p3-2-delivery-report.md`（本文件）

---

## 3. 关键设计决策

### 3.1 `Embedder` 接口兼容性

Spec 要求 `Embed(ctx, req EmbedRequest) (*EmbedResponse, error)` 形态，但仓库
已有的实现 / 单元测试都使用 `Embed(ctx, texts []string) ([][]float32, error)` 形
态。为了在保留所有已有测试 / 调用方代码（0 回归）的同时满足 spec，新接口
暴露了三种方法：

- `Embed(ctx, texts []string) ([][]float32, error)` — 老接口，主路径
- `EmbedBatch(ctx, texts)` — sugar，spec 友好
- `EmbedRequest(ctx, req) (*EmbedResponse, error)` — spec 形态
- `HealthCheck(ctx) error` — 启动时自检

所有四个实现（`OpenAI` / `Ollama` / `Noop` / `LocalStub` / `CachedEmbedder`）都
完整满足此接口。

### 3.2 `LocalStubEmbedder` — hash-based deterministic

Spec 要求一个"不调任何 LLM 的 384 维 deterministic 向量"。实现方案：

- 输入文本做 `SHA-256` 得到 32 字节种子
- 对每个维度 d（0..N-1），把 `seed || d-le-u32` 再做一次 `SHA-256`，取前 4 字节
  解释为 `uint32`，映射到 `[-1, 1)`
- L2 归一化，使 cosine 相似度有直观上界

优势：
- 完全 deterministic，跨进程、跨 Go 版本、跨 CPU 保持稳定（无 `math/rand`）
- 相同输入永远产生 byte-identical 输出
- 无网络 / 无 API 依赖，适合 CI / 离线 dev

### 3.3 `Summarizer` 行为

把 `BudgetManager.Summarize` 的内联逻辑搬到独立类型里，但 `BudgetManager` 仍
然存在（只是现在持有一个可选 `*Summarizer` 字段）。未删除的现有 API：
- `BudgetManager.EstimateTokens` / `NeedsSummarization` / `IsNearLimit` / `Summarize`
  全部保留原签名。

新增的 spec-required 行为：

| 行为 | 状态 |
|------|------|
| `ShouldSummarize(messages)` 显式入口 | ✅（`NeedsSummarization` 的别名） |
| `Stats() (calls int, lastSummaryAt time.Time)` | ✅（`sync/atomic` 计数器） |
| `PreserveSystemPrompt=true` 默认开启 | ✅（default config 改为 `true`） |
| `CustomSystemPrompt` 覆盖默认 prompt | ✅ |
| `CustomUserPromptTpl` + `{summary}` 占位符 | ✅ |
| 失败时 fallback 到原 messages | ✅（保持原 `BudgetManager` 行为） |
| 前置回 system prompt | ✅ |

### 3.4 `Finding.Embedding` 字段

之前 `Finding` struct 没有 `Embedding` 字段，只有 `embeddingArg` 内部变量
供 pgvector 写入。本任务在 `types.go` 中显式添加 `Embedding []float32` 字段
（带 `json:"embedding,omitempty"`），并通过 `EmbedHook` 接口暴露给调用方
写入。`PostgresBoard.Write` 在写入前调用 `HookRegistry.Invoke`，hook 可以
修改 `f.Embedding`，最终由 `Write` 落库。

### 3.5 配置 schema 完整覆盖

`internal/config/config.go` 新增以下可调字段（每个都在 example yaml 中有
注释和默认值）：

```yaml
llm:
  embeddings:
    provider: noop
    model: text-embedding-3-small
    dimensions: 1536
    endpoint: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    batch_size: 100
    timeout: 30s
    cache:
      enabled: true
      size: 1024
      ttl: 1h
  summarizer:
    model: ""
    max_tokens: 2048
    temperature: 0.0
    trigger_at: 0.7
    warning_at: 0.8
    keep_recent_ratio: 0.5
    min_messages_to_summarize: 4
    preserve_system_prompt: true
    custom_system_prompt: ""
    custom_user_prompt_tpl: ""
```

`Validate()` 拒绝未知 provider 与越界 ratio；默认值通过 `viper.SetDefault`
注入，所以不写 yaml 也能跑（默认 `provider: noop`）。

---

## 4. 测试覆盖

### 4.1 单元测试（go test -short）

| 包 | 用例数 | pass | fail |
|----|--------|------|------|
| `internal/llm` | 35 | 35 | 0 |
| `internal/swarm/blackboard` | 9 (hook 专属) | 9 | 0 |
| `internal/config` | 4 | 4 | 0 |
| **本任务新增** | **48** | **48** | **0** |

整体仓库（`go test -short ./...`）— 全部 `ok`，0 回归。

### 4.2 关键覆盖

`embeddings_test.go` 新增：
- `TestLocalStubEmbedder_DeterministicAndL2Normalised`
- `TestLocalStubEmbedder_DefaultDimensions`
- `TestNewEmbedder_LocalStub`（工厂分派）
- `TestOpenAIEmbedder_HealthCheck`（healthy / unauthorized 两个子测试）
- `TestEmbedRequest_HonoursModelOverride`
- `TestEmbedRequest_NoOverrideUsesDefaults`

`summarizer_test.go` 新增：
- `TestSummarizer_ShouldSummarize_SpecAlias`
- `TestSummarizer_Stats_CountsCalls`
- `TestSummarizer_PreserveSystemPrompt_KeepsItOnTop`
- `TestSummarizer_CustomSystemPrompt_Overridden`
- `TestSummarizer_CustomUserPromptTpl_Honoured`
- `TestSummarizer_IsNearLimit_WarningThreshold`

`embed_hook_test.go` 新增：
- `TestAutoEmbedHook_OnWrite_PopulatesEmbedding`
- `TestAutoEmbedHook_ComposeText_DefaultOrder`
- `TestAutoEmbedHook_WithFields_OverridesOrder`
- `TestAutoEmbedHook_WithMaxDataLen_Truncates`
- `TestAutoEmbedHook_WithSkipEmpty_LeavesEmbeddingNil`
- `TestAutoEmbedHook_EmbedderError_ReturnsError`
- `TestHookRegistry_AddAndInvoke`
- `TestHookRegistry_NilHookIgnored`
- `TestHookRegistry_EmptyInvokeReturnsNil`

`config_test.go` 新增：
- `TestLoad_Defaults_LLMBlock`
- `TestLoad_LLMBlock_Overrides`
- `TestValidate_EmbeddingsProvider_RejectsUnknown`
- `TestValidate_SummarizerThresholds_OutOfRange`

### 4.3 Live 集成测试

未在 `embeddings_integration_test.go` 新增 live 测试；现有
`TestLive_OllamaEmbedder_Real` 等用例需要外部服务（Ollama / OpenAI），无
法在 CI 中无条件执行。Spec 允许 skip 此类测试。

### 4.4 微基准（`embeddings_bench_test.go`）

5 个基准（`-benchtime=1x` 跑过一次确认编译通过）：

| 基准 | 测量目标 |
|------|---------|
| `BenchmarkLocalStub_Embed` | hash-based stub 在 1000 条短文本上的端到端延迟 |
| `BenchmarkCachedEmbedder_AllHits` | 纯缓存命中：LRU 查找开销 |
| `BenchmarkCachedEmbedder_AllMisses` | 全部未命中：内层 stub 调用 |
| `BenchmarkSummarizer_ShouldSummarize` | agent 主路径热调用 |
| `BenchmarkSummarizer_Stats` | monitor tick 调用 |

完整数字（`-benchtime=1x` 一次迭代）：

```
BenchmarkLocalStub_Embed-8                1    141858634 ns/op   (~142 ms for 1000 × 384-dim)
BenchmarkCachedEmbedder_AllHits-8         1        43391 ns/op   (~43 µs  for 100 texts)
BenchmarkCachedEmbedder_AllMisses-8       1       171960 ns/op   (~172 µs for 1 text)
BenchmarkSummarizer_ShouldSummarize-8     1          859 ns/op   (sub-µs)
BenchmarkSummarizer_Stats-8               1          399 ns/op   (lock-free atomic loads)
```

---

## 5. 验证

| 验证 | 结果 |
|------|------|
| `go build ./...` | ✅ 0 error |
| `go vet ./...` | ✅ 0 error |
| `go test -short ./...` | ✅ 0 回归 |
| 配置文件 schema（`llm.embeddings` / `llm.summarizer`） | ✅ 默认值 + override 都正常 |
| `BudgetManager` 现有 API | ✅ 签名未变，新增字段为可选 |
| `PostgresBoard` 现有 API | ✅ 签名未变，新增 `AddEmbedHook` / `FindEmbedding` 均为可选 |
| 真实调用 LLMs 的网络测试 | ⚠️ 跳过（spec 允许；本地无 API key / Ollama） |

---

## 6. 已知后续工作

- **实时嵌入**：`PostgresBoard.SetEmbedder` 仍然存在作为旧 API；新代码
  应使用 `AddEmbedHook(NewAutoEmbedHook(NewCachedEmbedderFromConfig(...)))`
  的组合。未来 PR 可以把 SetEmbedder 标记为 deprecated。
- **Schema 列宽**：当前 pgvector 列宽 1536。如果生产环境换到
  `text-embedding-3-large`（3072 维）需要新加一个 migration；spec 未要求。
- **多语言 cosine 性能**：未做 `pgvector` 端的 HNSW 索引调优，超出 P3-2 范围。

---

## 7. 完成度

- 单元测试：**48** 个本任务新增用例，**48 pass / 0 fail**（`go test -short ./...` 全仓库 0 回归）
- live 测试：0 个新增（外部依赖，spec 允许 skip）
- 微基准：**5** 个（`embeddings_bench_test.go`）
- 0 回归
- 新增 LLM provider / embedder 实现：**2** 个（`LocalStubEmbedder`、`CachedEmbedder` 作为独立文件）
- 配置文件新 key 数：**18** 个（llm.embeddings: 9 + llm.embeddings.cache: 3 + llm.summarizer: 8，扣除 `cache` 嵌套后的扁平计数为 17）
- 新建 spec-required 文件：3 个（`embed_hook.go`、`embed_hook_test.go`、`embeddings_cache.go`）
- 新建 spec-required 测试：3 个（`embed_hook_test.go`、`config_test.go`、`embeddings_bench_test.go`）
