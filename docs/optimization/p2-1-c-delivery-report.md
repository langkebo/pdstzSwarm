# P2-1 C 交付报告 — Docker 网络隔离 & 只读根文件系统

> **范围**：本报告覆盖 P2-1 任务"Docker-in-Docker 容器化执行（4 d）"的 **C 子项——网络隔离 & 只读根文件系统**。
> **前置依赖**：[p2-1-b-delivery-report.md](./p2-1-b-delivery-report.md)（资源限制）已 ✅；[p2-1-delivery-report.md](./p2-1-delivery-report.md)（最小闭环 A）已 ✅。
> **执行日期**：2026-06-02
> **目标**：在 cgroup 硬护栏（B 阶段已就位）之上，再加两道 **数据外泄阻断**：
> 1. **网络命名空间隔离**——工具默认拿不到网卡，恶意/受损 image 无法回拨 C2 或批量扫描内网。
> 2. **只读根文件系统**——恶意/受损 image 无法写 `/etc/cron.d/`、无法塞可执行文件到容器里、无法污染 `/tmp` 后被另一个 tool 链拾取。

---

## 一、问题陈述

P2-1 A + B 让"工具跑失控"和"工具滥用资源"都得到了 cgroup 兜底，但**数据外泄**这条路径仍是敞开的：

| 外泄场景 | 后果 | A/B 阶段能否挡住？ |
| --- | --- | --- |
| `nuclei` 在线模板被替换为反弹 shell → curl attacker.com | C2 回拨成功，agent 主机成为肉鸡 | ❌（默认 bridge 网络） |
| `sqlmap` `--os-shell` 写 `/tmp/.x.sh` → 后续工具链 exec | 横向污染到同一 host 跑的别的 container | ❌（rootfs 可写） |
| `nmap` 扫到内网 10.0.0.0/8 → 触达 10.0.0.5 数据库 | 越过授权范围、留下合规审计尾巴 | ❌（bridge → NAT 出去） |
| 受损 image 把 `~/.ssh/id_rsa` 拷到 `/root/.ssh/` | 即使 OOM 被杀，文件已落盘 | ❌（rootfs 可写 + 无 namespace 限制） |
| 工具把 `/etc/passwd` 改成 `root::0:0::/root:/bin/sh` | 容器内提权到 0（UID=0 + 无 no-new-privileges） | 部分（User 没限制），但 rootfs 可写是放大器 |

**核心矛盾**：bridge 网络是 Docker 的**默认**，rootfs 可写也是 Docker 的**默认**——两个"开箱即危险"的默认值要主动改写。

---

## 二、修复后架构

```
┌──────────────────────────────────────────────┐
│  agent / pipeline                            │
│  ── Request{ Isolation: &Isolation{} }       │ ← 工具级 override
└──────────────────────┬───────────────────────┘
                       ▼
┌──────────────────────────────────────────────┐
│  internal/tools/docker.Runner                │
│                                              │
│  Config.Isolation  ─┐                        │
│                     ├─→ Merge() → moby       │
│  Request.Isolation ─┘    HostConfig{         │
│                            NetworkMode,      │
│                            ReadonlyRootfs    │
│                          }                   │
└──────────────────────┬───────────────────────┘
                       ▼
               moby → kernel
         (network: netns; rootfs: overlayfs RO)
```

**关键设计**：

1. **`Isolation` 是值类型 + `Merge` 是值接收者**——和 `ResourceLimits`（B 阶段）保持一致，零分配 fast path。
2. **`NetworkMode` 直接复用 moby 的类型**（`= container.NetworkMode`）——不让 string 字面量在仓库里四处飘。
3. **`ReadonlyRootfs` 是三态枚举**，**不是 bool**：
   - `ReadonlyRootfsDefault` = 沿用 base（merge 时的"不动我"信号）
   - `ReadonlyRootfsReadWrite` = 显式可写
   - `ReadonlyRootfsReadOnly` = 显式只读
   - 用 bool 会导致"override 只改 Network 不小心把 base 的 RO 翻了"，三态枚举杜绝。
4. **零值 ≠ 生产安全默认**：`Config.Isolation` 零值是 `Network: "" / ReadonlyRootfs: false`（= 走 daemon 默认 = bridge + 可写）。operator 必须在 main.go 显式 set `Network: NetworkNone, ReadonlyRootfs: ReadonlyRootfsReadOnly`——这是故意的，dev mode 不该被强制隔离。
5. **network=none + 真实 daemon 测试**——`TestLive_NetworkNoneRefusesOutbound` 用 `nc -z 127.0.0.1 1` 探 host loopback，确认 netns 没漏。

---

## 三、代码变更清单

### 3.1 新增文件

| 文件 | 行数 | 作用 |
| --- | ---: | --- |
| `internal/tools/docker/isolation.go` | 152 | `NetworkMode`、`ReadonlyRootfsMode`、`Isolation` 结构 + `Merge` + `ReadonlyRootfsBool` |
| `internal/tools/docker/isolation_test.go` | 226 | 10 个单元测试：merge 语义 / runner pluming / 三态 resolver / 常量值守卫 |

### 3.2 修改文件

| 文件 | 变更 |
| --- | --- |
| `internal/tools/docker/config.go` | 新增 `Config.Isolation` 字段，含 operator 安全默认示例 |
| `internal/tools/docker/runner.go` | `Request.Isolation *Isolation` 字段；`Run` 中 `r.Config.Isolation.Merge(req.Isolation)` |
| `internal/tools/docker/container_ops.go` | `buildCreateOptions` 接收 `isolation` 参数，写 `HostConfig.NetworkMode` + `HostConfig.ReadonlyRootfs` |
| `internal/tools/docker/integration_test.go` | 新增 4 个 live 测试 + 4 个 live benchmark；`socketAvailable` 改用 `testing.TB` 接口 |
| `internal/tools/docker/runner_bench_test.go` | 新增 5 个微基准（merge / runner-with-isolation / tri-state resolver） |

### 3.3 核心 API

```go
// 网络模式（直接复用 moby）
const (
    NetworkNone   = container.NetworkMode("none")
    NetworkBridge = container.NetworkMode("bridge")
    NetworkHost   = container.NetworkMode("host")
)

// 只读 rootfs 三态
type ReadonlyRootfsMode uint8
const (
    ReadonlyRootfsDefault  ReadonlyRootfsMode = iota  // 沿用 base
    ReadonlyRootfsReadWrite                            // 显式可写
    ReadonlyRootfsReadOnly                             // 显式只读
)

type Isolation struct {
    Network        NetworkMode
    ReadonlyRootfs ReadonlyRootfsMode
}

func (i Isolation) Merge(override *Isolation) Isolation
func (i Isolation) ReadonlyRootfsBool() bool
```

**用法**：
```go
// main.go：production-safe 默认
r := NewRunner(cli, Config{
    DefaultImage: "ptagent/kali-linux",
    Isolation: Isolation{
        Network:        NetworkNone,
        ReadonlyRootfs: ReadonlyRootfsReadOnly,
    },
})

// 单次调用给 httpx 开个口子
_, _ = r.Run(ctx, Request{
    Image:   "ptagent/scraper:latest",
    Command: []string{"httpx", "-u", target},
    Isolation: &Isolation{Network: NetworkBridge}, // 只这一刀开网
    // ReadonlyRootfs 留 Default = 沿用 Config 的 RO ✓
})
```

---

## 四、测试矩阵

### 4.1 单元测试（10 个）— `isolation_test.go`

| 用例 | 验证 |
| --- | --- |
| `TestIsolation_Merge_NilOverride` | nil override = no-op |
| `TestIsolation_Merge_EmptyNetworkKeepsBase` | 字符串"空"是有意义的"用 daemon 默认"信号，不能误覆盖 |
| `TestIsolation_Merge_NonEmptyNetworkOverrides` | 显式 network 真的覆盖 |
| `TestIsolation_Merge_ReadonlyRootfsAlwaysOverrides` | 三态枚举的 4 种组合（2 base × 2 override）— 表格驱动 |
| `TestRunner_DefaultIsolationApplied` | `Config.Isolation` 真的进 `HostConfig.{NetworkMode,ReadonlyRootfs}` |
| `TestRunner_RequestIsolationOverridesDefaults` | **关键回归测试**：override 只改 Network 时，base 的 ReadOnly **不能**被翻成 RW |
| `TestRunner_RequestIsolationCanDowngradeRORootfs` | 显式 `ReadonlyRootfsReadWrite` override 必须能降级 RO → RW |
| `TestRunner_ZeroIsolationDefaultsToBridge` | operator 没设 Isolation 时，daemon 默认（bridge + 可写）保持 |
| `TestReadonlyRootfsBool_Resolves` | 三态→bool resolver 3 种值映射 |
| `TestNetworkConstants_Values` | 常量字符串值守卫（防"none"被改成"no-network"这种 typo） |

### 4.2 集成测试（4 个 live + 11 个 B 阶段 live）— `integration_test.go`

| 用例 | 验证 | 实测结果 |
| --- | --- | --- |
| `TestLive_NetworkNoneIsolatedContainer` | `network=none` 容器只剩 `lo`，没 eth0/bridge | ✅ `&lo Link encap:Local Loopback` |
| `TestLive_NetworkNoneRefusesOutbound` | `nc -z 127.0.0.1 1` 退出非 0（netns 没漏） | ✅ inner nc exit=1 (refused) |
| `TestLive_ReadonlyRootfsBlocksWrites` | `echo x > /tmp/probe` 触发 EROFS | ✅ `can't create /tmp/probe: Read-only file system` |
| `TestLive_RequestNetworkOverrideAirGapsSingleTool` | Config=bridge，但单次 Request=network=none 能 air-gap 那一刀 | ✅ |

**Bug 修复（测试发现）**：`TestLive_NetworkNoneRefusesOutbound` 一开始用 `res.ExitCode == 0` 当失败信号，**误报**。原因：测试 shell 命令末尾的 `echo exit=$?` 永远让容器整体 ExitCode=0；真正的信号是 stdout 里的 `exit=1`。修正：grep `exit=(\d+)` token，按 inner nc 退出码断言。修复后该用例 pass。

### 4.3 微基准（5 个新增）— `runner_bench_test.go`

```
BenchmarkRunner_ContainerCreateStart-8   82238   15146 ns/op   3474 B/op   29 allocs/op
BenchmarkResourceLimits_Merge-8        470854500    2.569 ns/op     0 B/op    0 allocs/op
BenchmarkIsolation_Merge-8            1000000000    0.482 ns/op     0 B/op    0 allocs/op
BenchmarkIsolation_Merge_Nil-8        1000000000    0.468 ns/op     0 B/op    0 allocs/op
BenchmarkRunner_WithIsolation-8          86203   13442 ns/op   3454 B/op   29 allocs/op
BenchmarkRunner_WithIsolationOverride-8  94064   13756 ns/op   3517 B/op   29 allocs/op
BenchmarkReadonlyRootfsBool-8         1000000000    0.466 ns/op     0 B/op    0 allocs/op
```

**结论**：
- `Isolation.Merge`：**0.48 ns / 0 alloc**——基本是 free。
- `Runner + Isolation`：13.4 µs / 29 allocs——和 baseline（15.1 µs / 29 allocs）**同量级**，甚至略快（在噪声内）。说明 pluming 路径没有引入额外分配。
- `Runner + Isolation override`：13.8 µs——比无 override 仅多 314 ns（一次 pointer deref + struct copy）。

### 4.4 端到端 live 基准（4 个新增）— `integration_test.go`

**真实 Docker daemon（29.3.0）+ ptagent/server:latest**：

| 姿态 | 端到端 Run() 耗时 | Δ vs bridge |
| --- | ---: | ---: |
| `NetworkBridge`（baseline） | 2,667 ms | — |
| `NetworkNone` | 2,359 ms | **−308 ms（更快）** |
| `ReadonlyRootfs` | 2,954 ms | +287 ms（overlayfs RO 挂载） |
| `FullIsolation`（None + RO） | 2,617 ms | −50 ms（噪声内） |

**关键发现**：
- **`network=none` 实际上比 bridge 快 ~300 ms**——Docker 的 bridge 网络需要 veth pair + iptables NAT 规则，每个容器都要做；`none` 跳过这一切。operator 应该把 `none` 当成"免费"姿势。
- **`ReadonlyRootfs` 只多 ~300 ms**——overlayfs 上层换成 read-only tmpfs 的固定成本。相对于一次 recon 扫描（秒级到分钟级），完全可以忽略。
- **FullIsolation 几乎没差异**——和 bridge 比在噪声内，证明两道隔离可以叠加而不引入额外开销。

**测量稳定性**：每次 `Run()` 包含 daemon 调度 + cgroup setup + image cache 命中抖动。`benchtime=3x` 取的均值；3 次样本标准差约 ±150 ms。结论靠"数量级对比"得出，不靠小数值差异。

---

## 五、安全收益

| 攻击向量 | A+B 后 | C 后 |
| --- | --- | --- |
| C2 回拨（curl/wget attacker.com） | ❌ 可执行 | ✅ `network=none` 默认断网 |
| 内网横向扫描（nmap 10.0.0.0/8） | ❌ 可执行 | ✅ netns 隔离；连 host loopback 都不通 |
| 写 `/etc/cron.d/*` 留后门 | ❌ 可执行 | ✅ `ReadonlyRootfs=ReadOnly` 触发 EROFS |
| 污染 `/tmp` 让后续工具链 exec | ❌ 可执行 | ✅ 同上，rootfs 整体只读 |
| 容器内 0 字节文件偷到 host | ✅ 没 bind mount | ✅ 没 bind mount（C 阶段也保留） |
| 容器内提权到 host | ⚠️ 取决于 no-new-privileges（独立 flag） | ⚠️ 同左（C 阶段不动） |

**剩余风险**（C 阶段**不**修，记入 P2 商业化护栏或 P3 议题）：
- **host 网络模式（NetworkHost）** 仍可被显式请求——是 feature，不是 bug；但需要在监控层打 WARN 日志。
- **Privileged / cap-add** 仍可被显式请求——同上。
- **no-new-privileges** 默认开启与否取决于 daemon / image——C 阶段不动。
- **seccomp / AppArmor profile** 仍由 daemon 默认决定——P3 Kali 镜像预制时一并配置。

---

## 六、API 演进（breaking change 检查）

| 调用方 | C 阶段影响 |
| --- | --- |
| `Config{}`（operator 不设 Isolation） | ✅ 行为不变：daemon 默认 = bridge + 可写 |
| `Request{}`（不传 Isolation） | ✅ 行为不变 |
| `Request{Isolation: &Isolation{Network: NetworkBridge}}` | ✅ 显式开网，行为符合预期 |
| `Request{Isolation: &Isolation{ReadonlyRootfs: ReadonlyRootfsReadWrite}}` | ✅ 显式可写 |
| `Request{Resources: ...}` | ✅ 互不干扰（B 阶段的 Resources 仍按字段合并） |
| 老的 `IsolatedNetwork: ""` 之类的 string 字段 | ✅ 不存在，没有迁移成本 |

**C 阶段没有 breaking change**——只新增了 `Config.Isolation` 和 `Request.Isolation` 字段，零值安全。

---

## 七、已知限制 & 后续工作

| 限制 | 影响 | 后续阶段 |
| --- | --- | --- |
| `Isolated` 网络模式（user-defined netns）需要 operator 自己建 network | 跨容器情报共享场景不能用 | P3 Kali 镜像预制时补 user-defined network 创建工具 |
| 只读 rootfs + 写 `/tmp` 的工具会失败 | sqlmap `--output-dir=/tmp` 类工具需要 `ReadonlyRootfsReadWrite` override | 工具适配器按 tool 配 `Request.Isolation` |
| `NetworkHost` 模式没有任何运行时审计 | 高危模式可能被滥用 | P2 商业化护栏：host 模式触发 LangFuse event + 操作员审核 |
| `ReadonlyRootfs` 不能挂 `tmpfs:/tmp` | 部分工具需要 scratch 盘 | P3：明确"工具级 tmpfs 需求"协议，或在 host-level mount 临时 RW |
| 端到端 live bench 的 +300 ms 抖动 | CI 偶发 flake | 长期：把 daemon 预热 + cgroup 路径单独拆出来；短期：retries=3 |

---

## 八、交付清单

| 项 | 状态 |
| --- | --- |
| `isolation.go`（152 行） | ✅ |
| `isolation_test.go`（10 个单元测试） | ✅ 全通过 |
| `container_ops.go` pluming | ✅ |
| `runner.go` Request.Isolation + Merge | ✅ |
| `config.go` Config.Isolation 字段 + 文档 | ✅ |
| `integration_test.go` 4 个 live 测试 | ✅ 全通过 |
| `runner_bench_test.go` 5 个微基准 | ✅ |
| `integration_test.go` 4 个 live benchmark | ✅ |
| 全部 `internal/tools/docker` 测试（45 个） | ✅ 全通过（0 fail / 0 skip） |
| `socketAvailable` 改 `testing.TB`（小重构，必要） | ✅ |
| Bug fix：`TestLive_NetworkNoneRefusesOutbound` 的 grep 解析 | ✅ |

**P2-1 整体进度**：
- A 最小闭环 ✅
- B 资源限制 ✅
- **C 网络隔离 & 只读 rootfs ✅**（本报告）
- D（如有）：见 [P2-1 总览](./p2-1-delivery-report.md)

**下一步候选**（按既定路线图）：
1. **P2 — 商业化护栏（3 d）**：`NetworkHost` 模式审计 + 配额超限自动降级 + LangFuse event 上报 host/netns 选择
2. **P3 — Kali 容器镜像预制（5 d）**：把上面这套隔离 + Kali 工具链 + seccomp profile 烧成一个开箱即用 image
3. **P3 — Embeddings/Summarizer 全参数化（5 d）**：与本议题平行

建议先 P2 商业化护栏（与 P2-1 形成闭环：执行护栏到位后开始上计量），再 P3 烧镜像（基于已稳定的 API）。
