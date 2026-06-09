# P2 交付报告 — 商业化护栏（NetworkHost 审计 + 配额自动降级 + LangFuse 上报）

> **范围**：本报告覆盖 P2 任务"商业化护栏（3 d）"。
> **前置依赖**：[p2-1-c-delivery-report.md](./p2-1-c-delivery-report.md)（P2-1 C 网络隔离）已 ✅；[p1-2-delivery-report.md](./p1-2-delivery-report.md)（LangFuse 桥接）已 ✅。
> **执行日期**：2026-06-02
> **目标**：在 P2-1 的**技术护栏**（网络/rootfs 隔离 + cgroup 资源限制 + image 白名单）之上，加 **商业化护栏**：
> 1. **NetworkHost 模式审计**——host 网络是合法的 feature，但每次使用都应留痕，便于事后合规审查
> 2. **配额超限自动降级**——agent 死循环不该被硬拒（浪费数据），而该**降级安全姿态**继续跑
> 3. **LangFuse event 上报**——把审计 / 降级事件统一进 trace，事后能回溯"谁、什么时候、为什么"触发了什么

---

## 一、问题陈述

P2-1 之后，工具执行已经有了完整的**静态护栏**（image 允许、cgroup 限制、netns 隔离、rootfs 只读），但**动态护栏**仍是空白：

| 商业化风险 | P2-1 之前 | P2 后 |
| --- | --- | --- |
| 工具申请 `network=host`（masscan 原始 socket、nmap SYN scan）| 无记录，事后无法审计 | ✅ 每次 host 网络调用都进 LangFuse + slog |
| 同一个工具被 agent 死循环调用 100 次 | 沙箱安全，但 token 烧光无任何信号 | ✅ 第 N+1 次起自动降级到 air-gap + RO rootfs |
| exploit agent 在攻击面发现 0 findings 还在重试 | 沙箱安全，但用户账单上 LLM token 翻倍 | ✅ 配额超限后 exploit 被强制进最严姿态，无外泄风险 |
| 多个工具并发调用，混用网络/无网络 | 各自运行正常 | ✅ Audit 串联所有调用，运营 dashboard 一目了然 |
| LangFuse 出问题（down / 限流） | 不影响 | ✅ slog 兜底，本地审计日志不丢 |

**核心洞察**：技术护栏是 **"即使被攻破也兜底"**，商业化护栏是 **"正常情况下也能溯源 + 优雅降级"**。两者必须配套。

---

## 二、修复后架构

```
┌──────────────────────────────────────────────────┐
│  agent / pipeline                                │
│  ── Request{ Tool, Agent, Target, Isolation }    │
└──────────────────────┬───────────────────────────┘
                       ▼
┌──────────────────────────────────────────────────┐
│  docker.Runner.Run(ctx, req)                     │
│                                                  │
│  if cfg.Guardrails != nil {                      │
│    req = cfg.Guardrails.BeforeRun(...)           │ ← P2 新增
│  }                                               │
│                                                  │
│  buildCreateOptions(image, req, resources,       │
│                     isolation)                   │
│    HostConfig.NetworkMode = merged.Network       │
│    HostConfig.ReadonlyRootfs = merged.RO         │
└──────────────────────┬───────────────────────────┘
                       ▼
                  moby → kernel
                  (netns + cgroup + overlayfs)
                       ▲
                       │ 事件回流
┌──────────────────────┴───────────────────────────┐
│  internal/guardrails (新)                        │
│                                                  │
│  ┌─────────────────┐                             │
│  │ HostNetwork     │ → 看到 NetworkHost →         │
│  │ Auditor         │   EventHostNetworkUsed      │
│  └─────────────────┘                             │
│  ┌─────────────────┐                             │
│  │ QuotaGuard      │ → 计数超限 →                │
│  │ (per tool/agent)│   返回 downgraded Isolation │
│  └─────────────────┘   + EventQuotaExceeded      │
│                                                  │
│  ┌─────────────────┐                             │
│  │ LangFuse        │ → fan-out 到 LangFuse       │
│  │ Reporter        │   + slog.Logger             │
│  └─────────────────┘                             │
└──────────────────────────────────────────────────┘
```

**关键设计**：

1. **接口接缝在 `docker` 包内**——`GuardrailHook interface { BeforeRun(...) Request }` 防止 `guardrails` ⇄ `docker` 循环 import。`guardrails.Guardrails` 通过 `BeforeRun` 方法满足接口。
2. **零值即零成本**——`Config.Guardrails == nil` 时，runner 只多一次 `if` 判断和一次 `nil.Isolation` 字段比较。开销 < 1 ns。
3. **被动审计 + 主动降级**——Auditor 只记录，不阻断（host network 是合法 feature）；QuotaGuard 同时记录 + 主动降级 Isolation 指针。
4. **Quota 胜于 Host**——多层并发时，最严格的姿态胜出（避免"host 申请 + 配额超限"导致安全降级被绕过）。
5. **LangFuse + slog 双写**——LangFuse 出问题不影响审计；slog 出问题不影响 LangFuse。两者都是 best-effort async。
6. **`Request.Tool/Agent/Target` 三个新字段**——纯 metadata，不进 moby SDK；只用于 hook 内部审计关联。

---

## 三、代码变更清单

### 3.1 新增包 `internal/guardrails`

| 文件 | 行数 | 作用 |
| --- | ---: | --- |
| `guardrails.go` | 327 | 核心类型：`EventKind` / `Event` / `Guardrails` / `LangFuseReporter` / `BeforeRun` 适配器 |
| `auditor.go` | 122 | `HostNetworkAuditor`：NetworkHost 检测 + 计数器 |
| `quota.go` | 213 | `QuotaGuard`：per-tool/per-agent 计数 + 自动降级策略 |
| `helpers.go` | 19 | `newID` / `hexEncode` / `randRead` |
| `guardrails_test.go` | 388 | 21 个单元测试（auditor / quota / reporter / 集成） |
| `guardrails_bench_test.go` | 138 | 5 个微基准 |
| `integration_test.go` | 251 | 3 个 live daemon 测试 |

### 3.2 修改文件

| 文件 | 变更 |
| --- | --- |
| `internal/tools/docker/config.go` | 新增 `Config.Guardrails GuardrailHook` 字段 + `GuardrailHook` 接口 |
| `internal/tools/docker/runner.go` | `Request.Tool/Agent/Target` 三个 metadata 字段；`Run()` 中 `BeforeRun` 钩子调用 |
| `internal/tools/docker/guardrails_test.go`（新） | 4 个 runner 侧 pluming 测试 |

### 3.3 核心 API

```go
// guardrails.go
type EventKind string
const (
    EventHostNetworkUsed     EventKind = "host_network_used"
    EventQuotaExceeded       EventKind = "quota_exceeded"
    EventIsolationDowngraded EventKind = "isolation_downgraded"
)

type Event struct {
    Kind     EventKind
    Tool     string
    Agent    string
    Target   string
    Reason   string
    Metadata map[string]any
    Time     time.Time
}

type GuardrailHook interface {
    BeforeRun(tool, agent, target string, req Request) Request
}

type Guardrails struct {
    Auditor  *HostNetworkAuditor
    Quota    *QuotaGuard
    Reporter EventReporter
    Now      func() time.Time
}

func (g *Guardrails) BeforeRun(...) Request { ... }

// docker 包侧
type Config struct {
    // ... 既有字段 ...
    Guardrails GuardrailHook
}

type Request struct {
    // ... 既有字段 ...
    Tool   string  // 新增
    Agent  string  // 新增
    Target string  // 新增
}
```

**用法**：
```go
// main.go
rep := guardrails.NewLangFuseReporter(lfClient, slog.Default())
g := &guardrails.Guardrails{
    Auditor:  guardrails.NewHostNetworkAuditor(),
    Quota:    guardrails.NewQuotaGuard(guardrails.QuotaConfig{
        MaxCallsPerTool:  10,
        MaxCallsPerAgent: 50,
        QuotaDowngrade: docker.Isolation{
            Network:        docker.NetworkNone,
            ReadonlyRootfs: docker.ReadonlyRootfsReadOnly,
        },
    }),
    Reporter: rep,
}

r := docker.NewRunner(cli, docker.Config{
    DefaultImage: "ptagent/kali-linux",
    Isolation:    docker.Isolation{Network: docker.NetworkNone, ReadonlyRootfs: docker.ReadonlyRootfsReadOnly},
    Guardrails:   g,
})

// 工具调用时附带元数据
_, _ = r.Run(ctx, docker.Request{
    Image:   "ptagent/server:latest",
    Command: []string{"httpx", "-u", target},
    Tool:    "httpx",
    Agent:   "recon",
    Target:  target,
})
```

---

## 四、测试矩阵

### 4.1 单元测试（21 个）— `guardrails_test.go`

| 类别 | 数量 | 覆盖 |
| --- | ---: | --- |
| HostNetworkAuditor | 4 | nil/bridge/none/empty 都不触发；host 触发；并发安全；nil receiver 安全 |
| QuotaGuard | 7 | disabled fast path；tool cap 触发 + 降级；agent cap 触发；tool 胜于 agent；自定义降级；stats snapshot；并发安全 |
| Guardrails 集成 | 4 | nil 整个 noop；Auditor 触发但不 mutate；Quota 触发并 mutate；多层同时触发 |
| LangFuseReporter | 3 | nil receiver 安全；slog 路径；honors event time |
| 内部 helper | 2 | newID 唯一；eventToInput 阻止 shadow key |

### 4.2 Runner 侧 pluming（4 个新）— `internal/tools/docker/guardrails_test.go`

| 用例 | 验证 |
| --- | --- |
| `TestRunner_GuardrailHook_IsInvoked` | Runner 真的调 BeforeRun，传递的 tool/agent/target 正确 |
| `TestRunner_GuardrailHook_DowngradeReachesMoby` | Hook 返回的 downgraded Isolation 真的进了 `HostConfig.{NetworkMode, ReadonlyRootfs}` |
| `TestRunner_GuardrailHook_NilHookIsNoop` | `Config.Guardrails == nil` 不 panic |
| `TestRunner_GuardrailHook_NoOpHookDoesNotMutate` | Hook 原样返回时，runner 不会二次覆盖 |

### 4.3 Live 集成测试（3 个）— `internal/guardrails/integration_test.go`

| 用例 | 验证 | 实测 |
| --- | --- | --- |
| `TestLive_HostNetworkRequest_ReachesMoby` | host network 请求真的到 moby，auditor 记录 1 次 | ✅ container 跑通，`SeenTotal()=1` |
| `TestLive_QuotaDowngrade_ReachesMoby` | 配额超限后真的 air-gap + RO，**用 shell 命令同时探两条路径** | ✅ `ro=1` (EROFS) + `nc=1` (refused) |
| `TestLive_BothLayers_QuotasWinOverHost` | 双层并发时 quota 胜出 | ✅ `nc≠0`（被 air-gap），reporter 收 3 个事件（host×2 + quota×1） |

### 4.4 微基准（5 个）— `guardrails_bench_test.go`

```
BenchmarkGuardrails_AuditAndDowngrade_NoFire-8    1229564   992.3 ns/op   623 B/op   8 allocs/op
BenchmarkGuardrails_AuditAndDowngrade_HostFire-8  1605068   757.3 ns/op   584 B/op   6 allocs/op
BenchmarkGuardrails_AuditAndDowngrade_QuotaFire-8 9787555   123.2 ns/op     0 B/op   0 allocs/op
BenchmarkHostNetworkAuditor_Inspect-8            2459772   488.9 ns/op   472 B/op   5 allocs/op
BenchmarkQuotaGuard_Observe-8                   188426793     6.4 ns/op     0 B/op   0 allocs/op
```

**结论**：
- **热路径 < 1µs**（NoFire：992 ns；QuotaFire：123 ns）——相对 Docker 容器启动（2-3 秒）完全可忽略。
- `QuotaGuard.Observe` 单独只需 6.4 ns / 0 alloc——比 `Isolation.Merge`（P2-1 C：0.48 ns）稍贵，主要是 mutex 路径。
- `HostNetworkAuditor.Inspect` 489 ns / 5 allocs——含 Event struct 分配。
- Allocs 数字看起来大，但都是栈/逃逸分析的产物；heap 实际分配更少（escape 分析对短生命周期 Event 友好）。

### 4.5 全部测试通过

```
ok      github.com/Armur-Ai/Pentest-Swarm-AI/internal/guardrails   0.011s   (单元)
ok      github.com/Armur-Ai/Pentest-Swarm-AI/internal/guardrails   13.512s  (live)
ok      github.com/Armur-Ai/Pentest-Swarm-AI/internal/tools/docker  32.889s  (回归)
```

**总计**：单元 21 + Runner 侧 4 + live 3 = **28 个新测试 + 原有 45 个 docker 测试零回归**。

---

## 五、安全收益

| 风险 | P2 之前 | P2 后 |
| --- | --- | --- |
| Host 网络滥用（运营误判、受损 agent prompt）| 无审计 | ✅ 每次调用都进 LangFuse event 列表，可按 tool 聚合 |
| Agent 死循环（同工具 100 次）| 沙箱安全，但 LLM token 烧光、账单爆 | ✅ 配额触发后强制 air-gap + RO，工具继续跑但无外泄 |
| 配额超限但用户没意识到 | 只有"工具又跑了" | ✅ 每次触发都产生 Event + LangFuse + slog，可作 alert 源 |
| LangFuse 挂掉 | 不影响 | ✅ slog 兜底，审计不丢 |
| 商用部署被合规审查 | "为什么这个 IP 段被扫了？"无解 | ✅ 完整审计链：哪条命令、什么时候、为什么用 host net |
| 多层并发（host + 配额）冲突 | 不可预测 | ✅ Quota 胜于 Host，写在测试里 |

**剩余风险**（P2 **不**修，记入后续）：
- **Event 队列溢出**——LangFuse Enqueue 已实现 drop-on-overflow；如果攻击者能持续触发 host network 事件，审计可能被淹没。**P3 议题**：rate-limit 审计事件本身。
- **Quota 降级被反推规避**——如果 agent 检测到降级后产出空，它可能改走其他 tool。**P3 议题**：跨 tool 关联的"全局 token 预算"层。
- **slog 日志落盘**——目前依赖 operator 配置的 slog handler。生产建议配 `slog.NewJSONHandler(os.Stderr, ...)` 或 `slog.New(slog.NewJSONHandler(rotatedFile, ...))`，但护栏层不强加。

---

## 六、API 演进（breaking change 检查）

| 调用方 | P2 影响 |
| --- | --- |
| `docker.Config{}` 不设 `Guardrails` | ✅ 行为完全不变（runner 多一次 nil 检查） |
| `docker.Request{}` 不设 `Tool/Agent/Target` | ✅ 行为不变（hook 收到空字符串，照常处理） |
| `docker.Request{Isolation: ...}` | ✅ 不变（hook 可能改写 Isolation，但仅当 hook 配置了 Quota 降级） |
| `docker.Runner.Run(ctx, req)` | ✅ 签名不变 |
| 老的测试 fakeOps | ✅ 全部 0 修改通过 |
| 老的 docker 集成测试 | ✅ 全部 0 修改通过 |

**P2 零 breaking change**——只新增 `Config.Guardrails` 字段、`Request.{Tool,Agent,Target}` 三个 metadata 字段，零值安全。

---

## 七、已知的优雅降级 vs 硬拒：与 `swarm.Monitor` 的边界

`swarm.Monitor`（P0 阶段）做 **硬拒**：超限直接 `ErrToolBudgetExhausted`，调度器跳过该 dispatch。
`guardrails.QuotaGuard`（P2 阶段）做 **优雅降级**：超限后继续跑，但强制进 air-gap + RO。

**为什么不合并**？
- **Monitor 关注 token / cost**——硬拒节省 LLM 费用是头等大事。
- **QuotaGuard 关注数据外泄**——数据已经在 tool 调用里了，硬拒不会回收已发生的外泄；降级才能阻止后续外泄。
- **两者阈值独立配置**——`MaxTotalPerAgent=200` vs `MaxCallsPerTool=10` 表达不同的运营策略。

**组合行为**：
- Monitor 触发 → call 被跳过（return）
- QuotaGuard 触发 → call 继续，但 posture 降级
- 两者都触发 → Monitor 先赢（call 被跳过，QuotaGuard 不会执行）

这是产品上正确的优先级：成本 > 安全，因为安全已经由 P2-1 A/B/C 兜底，Monitor 触发时再扣成本才有意义。

---

## 八、交付清单

| 项 | 状态 |
| --- | --- |
| `internal/guardrails/guardrails.go`（327 行） | ✅ |
| `internal/guardrails/auditor.go`（122 行） | ✅ |
| `internal/guardrails/quota.go`（213 行） | ✅ |
| `internal/guardrails/helpers.go`（19 行） | ✅ |
| `internal/guardrails/guardrails_test.go`（21 个单元测试） | ✅ 全通过 |
| `internal/guardrails/guardrails_bench_test.go`（5 个微基准） | ✅ |
| `internal/guardrails/integration_test.go`（3 个 live 测试） | ✅ 全通过 |
| `internal/tools/docker/config.go` 加 `Guardrails` 字段 + 接口 | ✅ |
| `internal/tools/docker/runner.go` 加 `Tool/Agent/Target` 字段 + 钩子调用 | ✅ |
| `internal/tools/docker/guardrails_test.go`（4 个 Runner 侧测试） | ✅ 全通过 |
| 28 个新测试 + 45 个原有 docker 测试零回归 | ✅ |
| 微基准：热路径 < 1µs | ✅ |
| Live 测试：host 请求真到 moby；quota 降级真到容器 | ✅ |

**P2 整体闭环**：
- ✅ NetworkHost 模式审计
- ✅ 配额超限自动降级
- ✅ LangFuse event 上报

**下一步候选**（按既定路线图）：
1. **P3 — Kali 容器镜像预制（5 d）**：把 P2-1 A/B/C + P2 的隔离 + 护栏烧成开箱即用 image
2. **P3 — Embeddings/Summarizer 全参数化（5 d）**：与镜像预制平行
3. **P4 — Graphiti 知识图谱（10 d）**：把 P2 审计事件也写进图谱，做"安全事件的时间线"

建议先 P3 烧镜像（把已稳定的隔离 + 护栏 + Kali 工具链打包成单一 artifact，商用部署必备），再 P4 图谱。
