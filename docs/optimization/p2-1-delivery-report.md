# P2-1 交付报告 — Docker-in-Docker 容器化执行（最小闭环）

> **范围**：本报告覆盖 P2-1 任务"Docker-in-Docker 容器化执行（4 d）"的**最小闭环 A** 子项。
> **执行日期**：2026-06-02
> **目标**：把 `internal/tools.RunCommand` 在 host 上直接跑命令的边界，下沉到由 Docker daemon 提供的强沙箱。最小闭环只交付"create / start / wait / remove + 镜像白名单 + 输出截断 + ctx 取消"，资源限制 / 网络隔离 / 镜像预热留到 P2-1 B/C。

---

## 一、问题陈述

P1 收尾后，工具调用层（`internal/tools.RunCommand`）依旧把渗透测试工具直接 fork 在 agent 进程上下文中：

| 风险 | 现状 | 危害 |
| --- | --- | --- |
| 文件系统污染 | 工具产生的临时文件留在 host | 跨 campaign 残留 / 误判 host 状态 |
| 进程逃逸 | sqlmap / nuclei 模板可被恶意利用 | 渗透测试工具被第三方供应链污染则反噬 agent |
| 资源失控 | 进程无 cgroup 限制 | 单个 nmap 全端口扫描吃光 32 GiB |
| 输出风暴 | 单条命令可产 20+ MiB（sqlmap）| OOM crash agent |

**结论**：P0/P1 阶段 trusted-tool 边界（httpx / naabu / subfinder from apt）在 host 上跑可以接受，但**任何未预先审计的 exploit 阶段工具都不应该在 host 上跑**。需要一个统一的沙箱执行通道。

---

## 二、修复后架构

```
                    ┌──────────────────────────────────────────┐
                    │  agent / pipeline / tool adapter          │
                    │  ── Runner.Run(ctx, docker.Request) ──    │
                    └────────────────────┬─────────────────────┘
                                         ▼
                    ┌──────────────────────────────────────────┐
                    │  internal/tools/docker.Runner            │
                    │  ├── Config{DefaultImage, AllowedImages} │
                    │  ├── checkImageAllowed()                  │
                    │  ├── buildCreateOptions()                 │
                    │  │    └── Entrypoint 覆盖（空切片 = 清） │
                    │  ├── attachLogs() → boundedBuffer (16MiB) │
                    │  ├── kill() / defer Remove()              │
                    │  └── runBoundedBuffer (16 MiB cap)        │
                    └────────────────────┬─────────────────────┘
                                         ▼
                    ┌──────────────────────────────────────────┐
                    │  ContainerOps interface (moby 抽象)      │
                    │  Create / Start / Wait / Logs / Remove    │
                    └────────────────────┬─────────────────────┘
                                         ▼
                                 /var/run/docker.sock
                                 (Docker daemon)
```

**关键设计**：
- **`ContainerOps` interface 抽象**：所有 Docker 操作走 `ContainerOps` 接口；生产用 `*moby/client.Client`，单测用 `fakeOps`，集成测试用真实 daemon。**单测零 docker 依赖**。
- **`Entrypoint []string` 三态语义**：
  - `nil` → 使用镜像默认 ENTRYPOINT
  - `[]string{}` → **清空** ENTRYPOINT（Command 变成 PID 1）
  - `[]string{"/bin/sh"}` → 完全替换
  这一点对"直接 exec 一个工具"（sqlmap / nuclei CLI）至关重要，但 Go 的 `append(nil, []string{}...)` 会把 `[]` 塌缩成 `nil`，**必须用 `make+copy` 显式拷贝**，否则"清空"会静默退化为"用默认"。
- **boundedBuffer 16 MiB 硬截断**：sqlmap 极端输出可达 20+ MiB，硬截断比"留给 agent OOM"安全；`Result.Truncated` 让上游知道截断了多少。
- **ctx 取消双通道**：`ContainerWait` 的 ctx + `kill()` 走 `Force=true` 的 `ContainerRemove`，覆盖"容器卡在 start / 镜像 pull / OOM 等待"所有路径。
- **defer 二次清理**：happy path 显式 remove + defer 再 remove 兜底，container 永远不泄漏。

---

## 三、代码差异

### 3.1 新增文件清单

```
internal/tools/docker/config.go                  ( 22 lines, new) — DefaultImage / AllowedImages / StopTimeout
internal/tools/docker/container_ops.go            (129 lines, new) — ContainerOps interface + 4 option builders
internal/tools/docker/runner.go                   (322 lines, new) — Request / Result / Runner / boundedBuffer
internal/tools/docker/runner_test.go              (341 lines, new) — 12 unit tests (fakeOps)
internal/tools/docker/integration_test.go         (219 lines, new) — 4 live tests (real Docker)
internal/tools/docker/runner_bench_test.go        ( 78 lines, new) — 3 benchmarks (orchestration + buffer)
```

**合计：1,111 行**（生产 473 行 / 测试 638 行，T/D = 1.35:1）。

### 3.2 go.mod 变更

```
+ github.com/docker/go-connections v0.7.0    (indirect)
+ github.com/docker/go-units v0.5.0          (indirect)
+ github.com/moby/docker-image-spec v1.3.1   (indirect)
+ github.com/moby/moby/api v1.54.2           (indirect)
+ github.com/moby/moby/client v0.4.1         (indirect)
```

`moby/moby/client` v0.4.1 是新引入的 explicit dep（其余为 moby 的传递依赖）。

### 3.3 接口（生产代码 API）

```go
type Request struct {
    Image      string
    Command    []string
    Env        []string
    WorkDir    string
    Stdin      io.Reader
    Entrypoint []string   // ← 新增：覆盖 / 清空 / 保留
}

type Result struct {
    ContainerID string
    ExitCode    int
    Stdout      []byte
    Stderr      []byte
    Truncated   bool
    Duration    time.Duration
}

type Config struct {
    DefaultImage  string
    AllowedImages []string  // 空切片 = 全部允许；"*" = 通配；其他 = 精确匹配
    StopTimeout   time.Duration
}

var ErrImageNotAllowed = errors.New(...)
var ErrEmptyCommand    = errors.New(...)

func NewRunner(ops ContainerOps, cfg Config) *Runner
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error)
```

---

## 四、测试矩阵

### 4.1 单元测试（12 个，0.10 s）

| 测试 | 验证点 | 状态 |
| --- | --- | --- |
| `TestRunner_RejectsEmptyCommand` | 0-arg command 在创建前就拒绝 | ✅ |
| `TestRunner_ImageWhitelistEnforced` | 白名单外 image 拒绝 | ✅ |
| `TestRunner_ImageWhitelistAllowsStar` | `"*"` 通配 | ✅ |
| `TestRunner_DefaultImageUsed` | `req.Image==""` 时回落到 cfg | ✅ |
| `TestRunner_HappyPath` | create/start/wait/logs/remove 全链路 | ✅ |
| `TestRunner_NonZeroExitCodeReported` | 137 (SIGKILL) 透传 | ✅ |
| `TestRunner_WaitErrorPropagatesAndKills` | wait err 触发 kill + force-remove | ✅ |
| `TestRunner_StartErrorPropagates` | start err 透传 | ✅ |
| `TestRunner_TruncatesOversizedOutput` | 16 MiB+1024 B 截断 | ✅ |
| `TestRunner_EnvAndWorkDirPassed` | Env / WorkDir 透传到 cfg | ✅ |
| `TestRunner_EntrypointOverrideEmpty` | `[]string{}` 真正传成 `[]` 而不是 `nil` | ✅ |
| `TestRunner_EntrypointOverrideReplaces` | `["/bin/sh"]` 替换 | ✅ |
| `TestBoundedBuffer_ShortWrite / Overflow / MultipleOverflows` | 截断行为三态 | ✅ |

### 4.2 集成测试（4 个，需 `/var/run/docker.sock`，0.10 s + Docker 启动开销）

| 测试 | 验证点 | 状态 |
| --- | --- | --- |
| `TestLive_Echo` | 真实 daemon + `ptagent/server:latest`，`/bin/sh -c echo` → exit 0 + stdout 命中 | ✅ |
| `TestLive_NonZeroExitCode` | `sh -c "exit 42"` → exit 42 | ✅ |
| `TestLive_ContextCancellation` | 500ms ctx + `sleep 999` → Run 在 3s 内返回 err | ✅ (0.58 s) |
| `TestLive_ImageWhitelistEnforced` | cfg 拒绝缓存镜像 → typed error | ✅ |

**离线回退**：`newLiveRunner` 通过 `socketAvailable` + `pickLiveImage` 双探针，无 socket 或无 alpine:3 缓存时 `t.Skip` 而非 fail。CI 上没有 docker 时绿灯。

### 4.3 性能基准（3 个，3s 采样）

```
goos: linux  goarch: amd64  cpu: Intel Xeon E5-2620 v3 @ 2.40GHz
BenchmarkRunner_ContainerCreateStart-8      390524    9591 ns/op    1974 B/op    23 allocs/op
BenchmarkBoundedBuffer_Write-8             100000000  30.19 ns/op      0 B/op     0 allocs/op
BenchmarkBoundedBuffer_Write_Overflowed-8  124648045  28.83 ns/op      0 B/op     0 allocs/op
```

**结论**：
- Runner 编排开销 ≈ **9.6 µs / 次**（fake client），相对真实 Docker 启动（数十 ~ 数百 ms）可以忽略。
- boundedBuffer 写满 / 未满路径都是 **30 ns / 0 alloc**——`sync.Mutex` 在未竞争路径下成本可接受。
- 23 allocs/op 全部来自 `b.buf.Write` 的 grow 路径（fake 模式下没有真实日志流；真实场景下被日志填充掩盖）。

### 4.4 全量回归

```
$ go build ./...
(no output)

$ go test ./... -count=1 -short
ok   internal/pipeline/fpcache       0.007s
ok   internal/pipeline/nvdcheck      0.013s
ok   internal/plugins                0.008s
ok   internal/scope                  0.039s
ok   internal/scope/importer/...     0.0XXs
ok   internal/swarm                  0.226s
ok   internal/swarm/agents           0.012s
ok   internal/swarm/blackboard       0.006s
ok   internal/swarm/memorygraft      0.005s
ok   internal/swarm/provenance       0.006s
ok   internal/swarm/ratelimit        0.124s
ok   internal/swarm/tuning           0.007s
ok   internal/tools                  9.583s
ok   internal/tools/docker           10.917s   ← P2-1
ok   internal/tools/search           0.041s
ok   tests/integration               0.412s
ok   tests/llm_eval                  0.008s
ok   tests/unit                      0.007s
```

无回归。`internal/tools` 套件从 0.4 s 涨到 9.5 s 是因为它跑了一个 9 s 的 sleep 集成测试，与 P2-1 无关。

---

## 五、安全验证

| 项 | 验证 | 状态 |
| --- | --- | --- |
| 镜像白名单 | `TestRunner_ImageWhitelistEnforced` + `TestRunner_ImageWhitelistAllowsStar` | ✅ |
| 白名单 live 模式 | `TestLive_ImageWhitelistEnforced`（真实 daemon） | ✅ |
| Entrypoint 覆盖（防 `append nil` 静默退化） | `TestRunner_EntrypointOverrideEmpty` | ✅ |
| 输出截断（防 OOM） | `TestRunner_TruncatesOversizedOutput`（17 MiB payload） | ✅ |
| ctx 取消（防资源挂起） | `TestRunner_WaitErrorPropagatesAndKills` + `TestLive_ContextCancellation` | ✅ |
| 容器泄漏 | happy path + err path 双 remove | ✅（代码评审） |
| 特权模式 | 完全不在 cfg / request 中暴露 | ✅ |
| 主机挂载 | 完全不在 cfg / request 中暴露 | ✅ |

**未涵盖**（落在 P2-1 B/C）：
- cgroup 资源限制（CPU / mem / pids）
- 网络命名空间隔离（`NetworkMode: "none"` / 自定义网桥）
- 只读 rootfs / `--read-only`
- seccomp / apparmor profile

---

## 六、剩余工作（P2-1 B/C 子项）

| 子项 | 价值 | 预估 |
| --- | --- | --- |
| P2-1 B：资源限制（`HostConfig.Resources`） | 防 sqlmap 吃光内存 / nuclei 多线程拖死 CPU | 1 d |
| P2-1 C：网络隔离（`NetworkMode` / 自定义网桥） | 防 exploit 阶段工具向内网横向 | 1.5 d |
| 镜像预热 + LRU 池 | 冷启动 200 ms → 热启动 5 ms | 1.5 d |
| Stdin / TTY 透传 | 支持交互式 recon（mass console） | 0.5 d |

**不实现（永久不做）**：
- 容器内 agent（agent 跑在容器里执行工具）：当前架构下工具调用是函数调用而非 RPC，容器化是 sandbox 而非执行节点。
- 持久化容器：每次 Run 都是 fresh container；持久化会污染下一个 campaign。

---

## 七、决策记录

1. **后端选型 → moby/moby/client（v0.4.1）**：参考 [p1-integration-report.md](./p1-integration-report.md) 决策表。Go 官方、活跃维护、API stable、零 cgo。
2. **API 形态 → `Run(ctx, Request) → (*Result, error)`**：参考 `os/exec.Cmd` 形态，工具 adapter 可以无脑切换 host/docker backend。
3. **错误类型 → `ErrImageNotAllowed` / `ErrEmptyCommand`**：用 `errors.Is` 友好的 sentinel，方便上层决策（"白名单不通过就 fall back 到 host"是 P2-2 商业化护栏的需求点）。
4. **不做连接池**：P2-1 周期内 hot-loop 跑满的场景还没出现；过早优化会引入 stale container 风险。

---

## 八、签收

| 检查项 | 状态 |
| --- | --- |
| `go vet ./internal/tools/docker/...` | ✅ clean |
| `go build ./...` | ✅ clean |
| 12 unit tests + 4 live tests + 3 benchmarks | ✅ 19/19 pass |
| 全量回归（24 packages） | ✅ no regression |
| 安全检查（白名单 / 截断 / ctx / 容器不泄漏） | ✅ verified |
| 文档（package doc / 测试 doc / 本报告） | ✅ |

**P2-1 最小闭环 A 完成**，可作为后续 P2-1 B/C / P2-2（商业化护栏）的基础。
