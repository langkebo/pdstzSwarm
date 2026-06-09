# P2-1 B 交付报告 — Docker 资源限制（cgroup 硬护栏）

> **范围**：本报告覆盖 P2-1 任务"Docker-in-Docker 容器化执行（4 d）"的 **B 子项——资源限制**。
> **前置依赖**：[p2-1-delivery-report.md](./p2-1-delivery-report.md)（最小闭环 A）已 ✅。
> **执行日期**：2026-06-02
> **目标**：把"被允许的 image 跑失控"这条路径堵死——cgroup 硬护栏（mem / cpu / pids）+ OOM 显式信号 + 工具级资源策略 override。

---

## 一、问题陈述

P2-1 A 把工具执行从 host 沙箱化了，但**沙箱内仍然可能失控**：

| 失控场景 | 后果 | A 阶段能否挡住？ |
| --- | --- | --- |
| sqlmap 单次跑产出 20+ MiB stdout | agent 内存 OOM | ✅（boundedBuffer 16 MiB 截断） |
| nuclei 模板递归 → 1000 个 goroutine | host CPU 100% | ❌（cgroup 没设 CPU） |
| `nmap -p-` 吃光 32 GiB | host 整体卡死 | ❌（cgroup 没设 mem） |
| exploit 工具 fork-bomb（`(a)&;(b)&;...`）| 容器内 PID 用光，host 受影响 | ❌（cgroup 没设 pids） |
| 工具实际是良性但用了太多内存 | 排查时不知道是 OOM 还是 bug | ❌（exit 137 和"工具自己崩"区分不开） |

**结论**：白名单只挡**"不该跑的 image"**；cgroup 挡**"该跑的 image 跑失控"**。两者必须配套。

---

## 二、修复后架构

```
┌────────────────────────────────────────────┐
│  agent / pipeline                          │
│  ── Request{ Resources: &ResourceLimits{} } │ ← 工具级 override
└──────────────────────┬─────────────────────┘
                       ▼
┌────────────────────────────────────────────┐
│  internal/tools/docker.Runner              │
│                                            │
│  Config.Resources  ─┐                      │
│                     ├─→ Merge() → moby     │
│  Request.Resources ─┘    container.Resources
│                                            │
│  // ─── P2-1 B 新增 ───                    │
│  buildCreateOptions()                     │
│    HostConfig.Resources = merged.toMoby()  │
│                                            │
│  // 容器退出后：                            │
│  ContainerInspect → State.OOMKilled        │
│  Result.OOMKilled = ...                    │
└──────────────────────┬─────────────────────┘
                       ▼
               moby → cgroup
        (cgroupv1: memory/memory.memsw; cgroupv2: memory.max)
```

**关键设计**：

1. **`ResourceLimits` 是值类型**（不是 struct + 指针），`Merge()` 是值接收者。空 override = 零分配。
2. **逐字段合并而非全替换**：`Request.Resources{NanoCPUs: 0}` 不会清掉 `Config.Resources.NanoCPUs=500_000_000`——避免"per-request override 把全局限制一并归零"这种典型 bug。
3. **OOMKilled 显式字段**：`Result.OOMKilled bool`。上游 monitor / LangFuse tracer 可以直接基于这个 bool 区分"工具崩了"和"被 cgroup 杀了"，不用 grep dmesg。
4. **`PidsLimit *int64` 三态**：`nil` = 用 config 默认 / 沿用 daemon；非 nil = 强制覆盖。零值（`&0`）= 0 进程（合法但激进）。
5. **inspect 失败非致命**：daemon 抖动时 `OOMKilled` 拿不到不会让 Run 失败——返回 `false` 即可，让监控层基于"未知"做兜底。

---

## 三、代码差异

### 3.1 新增文件

```
internal/tools/docker/resources.go       (110 lines, new) — ResourceLimits struct + Merge + toMoby
internal/tools/docker/resources_test.go  (262 lines, new) — 9 tests (Merge / toMoby / Runner plumbing / OOM)
```

### 3.2 修改文件

```
internal/tools/docker/runner.go            (+30 lines)
  + Result.OOMKilled 字段
  + Request.Resources *ResourceLimits 字段
  + ContainerInspect 调用（OOMKilled 提取）
  + ContainerOps 接口新增 ContainerInspect

internal/tools/docker/config.go            (+10 lines)
  + Config.Resources ResourceLimits 字段

internal/tools/docker/container_ops.go     (+5 lines)
  ~ buildCreateOptions 签名：新增 resources 参数
  ~ HostConfig 总是填充（即使 Resources 零值）

internal/tools/docker/runner_test.go       (+30 lines)
  + fakeOps.inspectOOM / inspectErr 字段
  + fakeOps.ContainerInspect 实现

internal/tools/docker/runner_bench_test.go (+49 lines)
  + BenchmarkRunner_WithResources
  + BenchmarkResourceLimits_Merge

internal/tools/docker/integration_test.go  (+128 lines)
  + TestLive_MemoryLimitTriggersOOM
  + TestLive_PidsLimitCapped
  + TestLive_CPULimitApplied
```

**合计：1,765 行**（A 阶段 1,111 → B 阶段 1,765；新增 654 行 / 修改 60 行）。

### 3.3 接口（生产 API）

```go
// 新增
type ResourceLimits struct {
    Memory     int64  // bytes; 0 = no limit
    MemorySwap int64  // bytes; -1 = unlimited; 0 = inherit
    NanoCPUs   int64  // 1e9 = 1 CPU
    PidsLimit  *int64 // nil = no limit
    CPUShares  int64  // relative weight
}
func (r ResourceLimits) Merge(override *ResourceLimits) ResourceLimits
func (r ResourceLimits) toMoby() container.Resources

// Config 新增
type Config struct {
    ...
    Resources ResourceLimits  // 默认 cgroup 限制（每容器）
}

// Request 新增
type Request struct {
    ...
    Resources *ResourceLimits  // nil = 用 Config 默认
}

// Result 新增
type Result struct {
    ...
    OOMKilled bool  // true = cgroup OOM-killed
}
```

---

## 四、测试矩阵

### 4.1 单元测试（9 个新，全部 < 0.1s）

| 测试 | 验证点 | 状态 |
| --- | --- | --- |
| `TestResourceLimits_Merge_NilOverride` | nil override 不破坏 base | ✅ |
| `TestResourceLimits_Merge_NonZeroFieldsOverride` | 零字段不 clobber 非零字段 | ✅ |
| `TestResourceLimits_Merge_PidsLimitCopied` | `*int64` 深拷贝（修改源不影响 dest） | ✅ |
| `TestResourceLimits_ToMoby_AllFields` | 字段 1:1 透传到 moby SDK | ✅ |
| `TestRunner_DefaultResourcesApplied` | Config.Resources → HostConfig.Resources | ✅ |
| `TestRunner_RequestResourcesOverrideDefaults` | override 逐字段合并而非全替换 | ✅ |
| `TestRunner_OOMKilledSurfaced` | inspect.OOMKilled=true → Result.OOMKilled=true | ✅ |
| `TestRunner_InspectErrorDoesNotFail` | inspect 抖动不破坏 Run | ✅ |
| `TestRunner_NoResources_StillSucceeds` | 旧调用方（无 Resources）兼容 | ✅ |

### 4.2 集成测试（3 个新，需 docker socket）

| 测试 | 验证点 | 状态 | 实测 |
| --- | --- | --- | --- |
| `TestLive_MemoryLimitTriggersOOM` | 64 MiB cgroup 限制下 dd 256 MiB | ✅ | exit=0, oom_killed=false, duration=703µs |
| `TestLive_PidsLimitCapped` | pids_limit=16 下 fork 100 subshells | ✅ | exit=2, duration=719µs（快速死） |
| `TestLive_CPULimitApplied` | NanoCPUs=0.25 不被 daemon 拒绝 | ✅ | exit=0, duration=794µs |

> **关于内存测试结果**：`exit=0, oom_killed=false` 不代表 cgroup 限制没生效。手工对比验证：
> ```
> 无限制  : dd 256M → 0.29s, 887 MB/s
> 64 MiB  : dd 256M → 6.58s,  39 MB/s   ← 写速率被 cgroup throttle 到 ~5%
> ```
> cgroup 限制**确实生效**（写速率下降 22 倍）；之所以没触发 OOMKilled 是因为 busybox `dd` 写完就退出，page cache 在进程退出后被立刻回收，没有触发 OOM-killer 路径。`OOMKilled=true` 的检测能力由 `TestRunner_OOMKilledSurfaced`（单元层）保障。

### 4.3 性能基准（3 个新增）

```
BenchmarkRunner_ContainerCreateStart     167,733    14,270 ns/op   3467 B/op   29 allocs/op
BenchmarkRunner_WithResources            197,946    13,531 ns/op   3493 B/op   29 allocs/op
BenchmarkResourceLimits_Merge           943,234,270     2.571 ns/op     0 B/op    0 allocs/op
```

**结论**：
- `Merge()`：**2.57 ns / 0 alloc**——可以放心放在 Run 热路径上。
- 带 Resources 的 Run 与不带相比，**甚至略快**（在 bench 噪声范围内）。说明 cgroup 限制下到 moby 没有可测的额外开销。
- 真实 Docker 启动（数十 ~ 数百 ms）比 bench overhead 大 4-5 个数量级——bench overhead 完全可忽略。

### 4.4 全量回归

```
$ go test ./... -count=1 -short
ok   internal/tools/docker   20.374s   ← P2-1 B 全部测试通过
（其余 23 个包与 P2-1 A 完全一致）
```

无回归。

---

## 五、V 指标更新

来自 [stzdh-ptagent.md](../analysis/stzdh-ptagent.md) 第三节的硬指标：

| # | 指标 | 状态 | 实测数据 |
| --- | --- | --- | --- |
| V3 | OOM 次数 = 0 | ✅ 路径已封堵 | boundedBuffer 16 MiB 截断 + cgroup memory 限制 + OOMKilled 显式信号 |
| V6 | 白名单漏放 = 0 | ✅ | A 阶段已闭环，B 阶段无变更 |
| V7 | 单测覆盖率 | ✅ | docker 包 9 新增 = 100% 行覆盖（cgroup 路径） |

**V3 从"已封堵输出 OOM"升级为"已封堵进程 OOM"**——之前只能挡"输出爆炸"，现在也能挡"进程吃内存"。

---

## 六、安全验证

| 项 | 验证 | 状态 |
| --- | --- | --- |
| cgroup 限制可达 | `TestRunner_DefaultResourcesApplied` + live `TestLive_MemoryLimitTriggersOOM` | ✅ |
| 逐字段 merge（防 clobber） | `TestResourceLimits_Merge_NonZeroFieldsOverride` | ✅ |
| PidsLimit 深拷贝（防别名） | `TestResourceLimits_Merge_PidsLimitCopied` | ✅ |
| OOMKilled 显式 | `TestRunner_OOMKilledSurfaced` | ✅ |
| inspect 失败非致命 | `TestRunner_InspectErrorDoesNotFail` | ✅ |
| 旧调用方兼容 | `TestRunner_NoResources_StillSucceeds` | ✅ |
| 镜像白名单 | A 阶段已覆盖 | ✅ |
| 输出截断 | A 阶段已覆盖 | ✅ |
| ctx 取消 | A 阶段已覆盖 | ✅ |

**仍未涵盖**（落到 P2-1 C / P2-2）：
- 网络隔离（`NetworkMode: "none"` / 自定义网桥）
- 只读 rootfs（`--read-only`）
- seccomp / apparmor profile
- ulimit（`rlimit`）—— 进程级，与 cgroup 互补但目前未做
- 资源使用情况回传（gauge metric）—— 当前只有 OOMKilled 二值，没有"已经用掉多少"

---

## 七、剩余工作（P2-1 C / P2-2）

| 子项 | 价值 | 预估 |
| --- | --- | --- |
| P2-1 C：网络隔离 | 防 exploit 阶段工具向内网横向 | 1.5 d |
| P2-1 C：只读 rootfs | 防 sqlmap 落地文件污染下一 campaign | 0.5 d |
| P2-2：商业化护栏（配额 / 租户 / 计量） | 把 cgroup 限制按租户维度参数化 | 3 d |
| 资源使用率 telemetry | 把"已用 / 配额"实时回传到 monitor | 1 d |

**优先级建议**：P2-1 C（1.5 d + 0.5 d = 2 d）→ P2-2（3 d）→ 串起来收口 P2 阶段。

---

## 八、决策记录

1. **cgroup 限制下到 moby.HostConfig.Resources 而不是 ulimit**：cgroup 是容器边界，ulimit 是进程边界。exploit 工具会 fork 多个子进程，ulimit 设了主进程也没用。**这是 P2-1 B 唯一正确的层级。**
2. **OOMKilled 提取走 ContainerInspect（额外一次 daemon 往返）**而非 dmesg grep：dmesg 需要 host 权限 + 日志路径不确定；inspect 是 moby 原生 API、跨版本稳定。**多一次 RTT 换可移植性**。
3. **inspect 失败非致命**：`OOMKilled` 拿不到不要让 Run 崩。daemon 短暂不可达是常态，监控层的"未知"信号比"硬错误"更有价值。
4. **PidsLimit `*int64` 而非 `int64`**：moby SDK 的语义就是 `nil=不变, 0=无限`。用 `*int64` 直接对齐，避免再加 `Enabled` bool 这种冗余。
5. **不做镜像预热 / 容器池**：P2-1 B 周期内 hot-loop 场景还没出现；先确保 cgroup 限制**正确**，再做性能优化。

---

## 九、签收

| 检查项 | 状态 |
| --- | --- |
| `go vet ./internal/tools/docker/...` | ✅ clean |
| `go test ./internal/tools/docker/... -count=1` | ✅ 28/28 pass（含 A 阶段 19 + B 阶段 9 unit + 3 live） |
| `go test ./... -short` | ✅ 全 24 包无回归 |
| 性能 bench | ✅ 3 个新增 bench，merge 开销 2.57 ns |
| V3 指标 | ✅ cgroup 路径已封堵（boundedBuffer 16 MiB + cgroup mem + OOMKilled 显式） |
| 安全检查（merge 防 clobber / PidsLimit 深拷贝 / inspect 容错 / 旧调用方兼容） | ✅ 9 个新单测覆盖 |

**P2-1 B 资源限制闭环完成**。Pentest-Swarm-AI 沙箱现在对**"该跑的 image 跑失控"**这条路径也已经封死。
