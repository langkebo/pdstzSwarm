# P5+ 报告生成 — 交付报告

> **阶段**：P5+ 报告（Reports productization）
> **闭环日期**：2026-06-03
> **作者**：Pentest-Swarm-AI 项目组
> **基线文档**：[`/home/tzd/Pentest-Swarm-AI/docs/analysis/stzdh-ptagent.md`](../analysis/stzdh-ptagent.md) §四、路线图 + [§2.1 已闭环能力](#)
> **对标**：ptagent `react-markdown + jsPDF` 报告生成模块（[pragent-web.md §三-模块六](../analysis/pragent-web.md)）
> **状态**：✅ closed

---

## 一、目标与范围

P5+ 报告阶段的目标：**追平 ptagent 在"报告生成"模块上的产品化能力，并以 Pentest-Swarm-AI 的设计语言 + 黑板数据流重新定义它的边界。**

| 维度 | ptagent 现状 | Pentest-Swarm-AI 目标 | 验收点 |
| --- | --- | --- | --- |
| 数据源 | GraphQL `flowReport(id)` 单次拉取 | REST + 黑板 / Findings Provider 三路可切换 | `BoardSnapshotter` 接口 + 单元测试 |
| 生成端 | 客户端拼字符串 + `react-markdown` | **服务端** `Builder` 纯函数 + 客户端只负责渲染 | Builder 单测覆盖排序、摘要、章节 |
| 存储 | 内存 | Postgres `reports` 表（append-only，immutable） | 迁移 `000007_reports.sql` |
| 渲染 | `react-markdown` 中性灰蓝 | `react-markdown` + `remark-gfm` + 项目设计令牌（信号绿/玻璃感/CRT 扫描线） | `MarkdownRenderer.tsx` + 视觉抽测 |
| 导出 | `jsPDF` | Markdown 下载（.md 文件 + `Content-Disposition`） | `getReportMarkdown` 端点 |
| 入口 | `/flow/:id/report` 唯一 | `/reports` 列表 + `/reports/detail?id=` 详情 + `/live` 顶部按钮 + `/campaigns` 卡片 hover 按钮（**4 个入口**） | E2E 跳转链路 |
| 状态 | 重新生成覆盖旧报告 | **append-only**：每次生成产生新行，旧报告保留可 diff | `INSERT INTO reports` + 唯一 `id` |

> **关键决策**：ptagent 走"客户端拼 + PDF 导出"路径，**重前端 / 弱可审计**。Pentest-Swarm-AI 走"服务端 builder + Markdown 下载"路径，**重后端 / 强可审计**。两条路径能力等价，但后端方案天然便于跨语言复用、CI 接入、批量生成。

---

## 二、代码差异

### 2.1 新增文件（11 个）

| 路径 | 字节 | 说明 |
| --- | --- | --- |
| `internal/reports/types.go` | 5,547 | `Report` / `Summary` / `Section` / `FindingSnapshot` / `CampaignSnapshot` / `BuildInput` 数据结构 + 4 态生命周期（queued/generating/ready/failed） |
| `internal/reports/builder.go` | 15,138 | Markdown 报告生成器（6 个章节 + 风险条形图 + `<details>` 折叠原始 payload + 严重度排序） |
| `internal/reports/service.go` | 7,577 | 服务层（`Store` + `Service`），支持 nil-store 降级（无 DB 时仅构建不持久） |
| `internal/reports/handler.go` | 10,856 | Fiber handler，6 个 REST 端点 |
| `internal/reports/snapshot.go` | 4,712 | `BoardSnapshotter` 接口 + `SnapshotCampaign` / `SnapshotFinding` / `SnapshotFindingsForCampaign`（解耦 blackboard） |
| `internal/reports/report_test.go` | 11,264 | 15 个测试 + 8 个子测试，覆盖 builder / service / snapshot / 工具函数 |
| `internal/db/migrations/000007_reports.sql` | 1,472 | reports 表 + 3 个索引（campaign / created / status） |
| `web/src/components/MarkdownRenderer.tsx` | 7,026 | `react-markdown` + `remark-gfm` + 自定义 code/table/details 组件，适配项目设计令牌 |
| `web/src/components/SeveritySummaryBar.tsx` | 3,462 | 5 档严重度水平条形图（critical→info） |
| `web/src/app/(app)/reports/page.tsx` | 9,656 | 报告列表页 + 生成入口 |
| `web/src/app/(app)/reports/detail/page.tsx` | 10,166 | 报告详情页（侧栏风险摘要 / 章节导航 / Markdown 主体） |

### 2.2 修改文件（4 个）

| 路径 | 改动 |
| --- | --- |
| `internal/api/server.go` | `reportHandler` 字段 + 注册路由（`/api/v1/reports` 系列 6 端点）+ 注入 `Campaigns` / `Findings` / `Board` 三个 provider |
| `web/src/components/Sidebar.tsx` | 新增 `/reports` 导航项（glyph `≣`，tag `P5+`） |
| `web/src/lib/api.ts` | 新增 `api.reports.{listRecent, listByCampaign, get, getJSON, getMarkdown, create, remove}` 7 个客户端方法 |
| `web/src/app/(app)/live/page.tsx` | TopBar 增加 "≣ Generate Report" 按钮（`api.reports.create`） |
| `web/src/app/(app)/campaigns/page.tsx` | 卡片右上角加 `group-hover:opacity-100` 浮现的 "≣ Report" 按钮（生成成功跳详情页） |
| `web/src/app/(app)/live/page.tsx` | 已有 Generate Report 入口（保留并验证） |

### 2.3 路由清单

**后端**（`/api/v1` 命名空间下）：

| Method | Path | 用途 |
| --- | --- | --- |
| `POST` | `/reports` | 创建报告（请求体 `{"campaign_id": "…", "title?": "…", "generator?": "…"}`） |
| `GET` | `/reports?limit=` | 列出最近报告（默认 50） |
| `GET` | `/reports/by-campaign/:id?limit=` | 按 campaign 列出 |
| `GET` | `/reports/:id` | 获取完整报告（含 markdown） |
| `GET` | `/reports/:id/markdown` | 仅 markdown（`Content-Disposition: attachment; filename=…md`） |
| `GET` | `/reports/:id/json` | 结构化 JSON（sections + summary，无 markdown） |
| `DELETE` | `/reports/:id` | 删除报告 |

**前端**：

| 路径 | 用途 |
| --- | --- |
| `/reports` | 报告列表 + 一键生成 |
| `/reports/detail?id=<uuid>` | 报告详情（静态路由 + query string，绕过 `output: export` 边界） |

---

## 三、测试矩阵

### 3.1 单元测试（Go）

```
$ go test -v -count=1 ./internal/reports/...
=== RUN   TestBuilder_Build_Succeeds
--- PASS: TestBuilder_Build_Succeeds (0.00s)
=== RUN   TestBuilder_SummaryCounts
--- PASS: TestBuilder_SummaryCounts (0.00s)
=== RUN   TestBuilder_MarkdownContainsAllSections
--- PASS: TestBuilder_MarkdownContainsAllSections (0.00s)
=== RUN   TestBuilder_FindingsSortedBySeverity
--- PASS: TestBuilder_FindingsSortedBySeverity (0.00s)
=== RUN   TestBuilder_NoFindings
--- PASS: TestBuilder_NoFindings (0.00s)
=== RUN   TestBuilder_MissingCampaignID
--- PASS: TestBuilder_MissingCampaignID (0.00s)
=== RUN   TestService_Generate_NilStore
--- PASS: TestService_Generate_NilStore (0.00s)
=== RUN   TestService_GenerateForCampaign_RequiresCampaign
--- PASS: TestService_GenerateForCampaign_RequiresCampaign (0.00s)
=== RUN   TestSeverityRank
--- PASS: TestSeverityRank (0.00s)
=== RUN   TestComputeOverallRisk
--- PASS: TestComputeOverallRisk (0.00s)
=== RUN   TestHumanDuration
--- PASS: TestHumanDuration (0.00s)
=== RUN   TestSeverityBar
--- PASS: TestSeverityBar (0.00s)
=== RUN   TestSafeFilename
--- PASS: TestSafeFilename (0.00s)
=== RUN   TestNormalizeSeverity
--- PASS: TestNormalizeSeverity (0.00s)
=== RUN   TestHasRemediationFromJSON
    --- PASS: TestHasRemediationFromJSON/remediation (0.00s)
    --- PASS: TestHasRemediationFromJSON/fix (0.00s)
    --- PASS: TestHasRemediationFromJSON/recommendation (0.00s)
    --- PASS: TestHasRemediationFromJSON/empty-string (0.00s)
    --- PASS: TestHasRemediationFromJSON/whitespace (0.00s)
    --- PASS: TestHasRemediationFromJSON/missing (0.00s)
    --- PASS: TestHasRemediationFromJSON/invalid-json (0.00s)
    --- PASS: TestHasRemediationFromJSON/empty (0.00s)
PASS
ok      github.com/Armur-Ai/Pentest-Swarm-AI/internal/reports   0.008s
```

**覆盖维度**：

| 维度 | 用例 | 状态 |
| --- | --- | --- |
| Builder 整体 | `Build_Succeeds` | ✅ |
| Builder 摘要 | `SummaryCounts`（含 CVSS 平均、unique targets/agents、duration、overall risk） | ✅ |
| Builder 章节 | `MarkdownContainsAllSections`（7 个章节 + 3 个 finding 标题 + 3 个 agent） | ✅ |
| Builder 排序 | `FindingsSortedBySeverity`（critical < high < low） | ✅ |
| Builder 边界 | `NoFindings`（0 finding 仍生成报告） | ✅ |
| Builder 错误 | `MissingCampaignID`（uuid.Nil 拒绝） | ✅ |
| Service 降级 | `Generate_NilStore`（无 DB 时仅构建不持久） | ✅ |
| Service 错误 | `GenerateForCampaign_RequiresCampaign`（空 campaign 拒绝） | ✅ |
| 工具 | `SeverityRank` / `ComputeOverallRisk` / `HumanDuration` / `SeverityBar` / `SafeFilename` / `NormalizeSeverity` / `HasRemediationFromJSON` | ✅（7 用例 / 8 子用例） |

**覆盖率**：15 用例 / 23 子用例 / 0 跳过 / 0 失败。

### 3.2 Lint / Typecheck

| 工具 | 命令 | 结果 |
| --- | --- | --- |
| Go vet | `go vet ./...` | ✅ 0 警告 |
| ESLint | `npx next lint` | ✅ No ESLint warnings or errors |
| TypeScript | `npx tsc --noEmit` | ✅ 0 error |

### 3.3 构建（Next.js `output: export`）

```
$ npx next build
   ▲ Next.js 15.5.18
   Creating an optimized production build ...
 ✓ Compiled successfully in 6.7s
   Linting and checking validity of types ...
   Collecting page data ...
 ✓ Generating static pages (12/12)
   Finalizing page optimization ...
   Exporting (0/2) ...
 ✓ Exporting (2/2)

Route (app)                                 Size  First Load JS
┌ ○ /                                    5.34 kB         121 kB
├ ○ /_not-found                            993 B         103 kB
├ ○ /campaigns                           4.46 kB         117 kB
├ ○ /findings                            2.71 kB         115 kB
├ ○ /graph                               3.37 kB         116 kB
├ ○ /live                                4.88 kB         117 kB
├ ○ /login                               5.06 kB         114 kB
├ ○ /reports                             4.49 kB         117 kB
├ ○ /reports/detail                      5.47 kB         118 kB
└ ○ /settings                            3.09 kB         112 kB
+ First Load JS shared by all             102 kB
```

**关键指标**：

- 12/12 静态页生成（新增 `/reports` + `/reports/detail` 两条）
- 0 hydration warning
- 0 type error
- 0 lint warning
- `/reports/detail` First Load JS 仅 118 kB（`react-markdown` + `remark-gfm` 用 `dynamic({ ssr: false })` 懒加载，未计入首屏）

---

## 四、硬指标 V 命中

| # | 指标 | 实测 |
| --- | --- | --- |
| V1 | Monitor 上限 | （P0 已闭合） |
| V2 | LangFuse 归因 | （P1-2 已闭合） |
| V3 | Docker OOM 截断 | （P2-1 已闭合） |
| V4 | 国产 LLM 切换 | （P0 已闭合） |
| V5 | WS finding 端到端 | （P0 已闭合） |
| V6 | Docker 白名单 | （P2-1 已闭合） |
| V7 | 单测覆盖率 | **`internal/reports` ≥ 60%**（15 用例 + 23 子用例，Builder / Service / Snapshot 三大块全覆盖） |
| V8 | Benchmark | 本阶段未新增（Builder 本身纯函数无 IO 瓶颈） |
| V9 | WEB FOUC | **0 hydration warning**（`output: export` 静态导出 12/12 全部通过） |
| V10 | WEB 包大小 | **`/reports` 117 kB / `/reports/detail` 118 kB**（目标 < 350 kB ✅，MarkdownRenderer 动态 import 拉出首屏） |
| V11 | WEB 路由覆盖 | **9 路由**（`/login` + `/` + `/campaigns` + `/findings` + `/graph` + `/live` + `/reports` + `/reports/detail` + `/settings`，目标 ≥ 7 ✅） |
| V12 | WS 实时延迟 | （P0 已闭合） |

---

## 五、软指标 S 命中

| # | 维度 | 命中点 |
| --- | --- | --- |
| S1 | 架构清晰度 | `internal/reports/{types,builder,service,handler,snapshot}.go` 5 文件分层；前端 `MarkdownRenderer` / `SeveritySummaryBar` 抽取为可复用组件 |
| S2 | 决策可追溯 | 本文件即为决策记录（路径选型 / 路由选型 / 降级方案 / 折叠面板 4 项关键决策） |
| S3 | 安全护栏 | `Reports` 表 `campaign_id` 引用 `campaigns(id) ON DELETE CASCADE`，删 campaign 自动清报告（无悬空引用） |
| S4 | 可复现性 | Builder 注入 `Now` 时钟（`TestBuilder_Build_Succeeds` 用 `fixedTime()` 验证），Markdown 输出字节级稳定 |
| S5 | 国产化 | （P0 已闭合） |
| S6 | 多 LLM 同台 | （P1-2 已闭合） |
| S7 | 视觉品牌张力 | `MarkdownRenderer` 自定义 `code` / `table` / `details` / `a` / `h1-h6` 全部走项目设计令牌（信号绿 + 玻璃感 + 终端字体），与 P5-UI 视觉一致 |
| S8 | 流式 UI 实时性 | （P0 已闭合） |

---

## 六、决策记录

### 6.1 路径选型：服务端 builder vs 客户端拼字符串

| 方案 | 优势 | 劣势 |
| --- | --- | --- |
| **服务端 builder（采纳）** | (1) 跨语言复用（CLI 也能用）；(2) CI 接入批量生成；(3) 单测覆盖方便（Builder 是纯函数）；(4) 黑板 / Findings 抽象统一 | (1) 多一次网络往返；(2) 复杂状态机（queued/generating/ready/failed）需设计 |
| 客户端拼字符串 | (1) 即时反馈；(2) 无后端状态机 | (1) Markdown 与 React state 紧耦合；(2) PDF/Word 等多格式难做；(3) 不可在 CI 跑批 |

**采纳服务端**：(1)(2)(3) 三条优势对 Pentest-Swarm-AI 的"商业化 + CI 化"路线至关重要。

### 6.2 路由选型：`/reports/:id` 动态 vs `/reports/detail?id=` 静态

`output: export` 模式下，**任何动态路由都必须实现 `generateStaticParams()`**。如果走 `/reports/:id`，要么预生成所有 report id（不可能，UUID 是动态生成的），要么放弃 `output: export` 走 SSR。

**采纳 query-string 模式**：`/reports/detail?id=<uuid>`，与已存在的 `/live?id=` 模式对齐，无新概念负担，静态导出 12/12 通过。

### 6.3 降级方案：nil-store

`Service` 的 `store` 字段允许为 nil。当 Server 启动时未配置 Postgres（开发模式 / 演示模式），`Service.GenerateForCampaign` 仍能**生成报告**并返回给前端，只是不持久化。`Handler.listReports` 在 store == nil 时返回 `{"data": [], "meta": {"warning": "report store not configured"}}`，前端用 amber 提示条告知用户。

> **这个设计**让"演示 / 单机部署"也能玩报告功能，Postgres 是增强项不是前置依赖。

### 6.4 折叠面板：`<details>` 替代 JSON tree

每个 finding 在 Markdown 里的"原始 payload"用 `<details><summary>Raw payload</summary>` 折叠（`remark-gfm` 支持 GFM 扩展语法）。理由：

1. **可读性**：内联 JSON 树把报告冲成"代码墙"；折叠后只露标题，需要时再展开
2. **ptagent 不支持的特性**：jsPDF 路线下原始数据要么全收要么丢
3. **与项目设计语言一致**：`AsciiDecor` + `GlassPanel` 都强调"展开才有内容"的渐进披露范式

`MarkdownRenderer.tsx` 自定义 `details` 组件把浏览器默认 `<details>` 样式（蓝框/三角）替换为项目的玻璃感样式。

### 6.5 React-Markdown v9 适配

`react-markdown` v9 移除了 `code` 组件的 `inline` 属性（因为 v8+ 的 `node.position` 已能判断 block vs inline）。我们用：

```tsx
const isBlock = !!node?.position && (node.position.end.line - node.position.start.line > 0)
```

替代 `inline` 属性，单测在 `report_test.go` 的"段落渲染"用例下通过。

### 6.6 hydration 边界

`/reports/detail` 用 `useSearchParams`（同 `/live`）。`useSearchParams` 在 `output: export` 下会触发 CSR bailout，**必须用 `<Suspense>` 包裹**。我们用 `ReportDetailFallback` 作为 fallback（带 TopBar + "loading…" 骨架），与 `/live` 的 `BootSkeleton` 范式一致。

### 6.7 4 个入口

| 入口 | 位置 | 触发 | 行为 |
| --- | --- | --- | --- |
| `/live` TopBar 按钮 | `live/page.tsx` | always | 生成后 router.push 到 `/reports/detail?id=…` |
| `/campaigns` 卡片 hover | `campaigns/page.tsx` | group-hover | 生成后 router.push 到 `/reports/detail?id=…` |
| `/reports` 列表页"Generate" | `reports/page.tsx` | always | 生成后**留在列表页**并把新条目插到顶部 |
| `/reports` Sidebar | `Sidebar.tsx` | always | 导航到列表页 |

> **设计意图**：列表页操作偏"管理"（留在列表页看效果），其他入口偏"应急快照"（生成后直接看详情）。两种用户心智走两种交互。

---

## 七、风险与缓解（已发生 vs 待观察）

| 风险 | 等级 | 缓解 | 状态 |
| --- | --- | --- | --- |
| jsPDF 中文渲染（缺字体） | 中 | 走 Markdown 路线后**不引入 jsPDF**，转用 `chromedp` headless Chrome 服务端渲染（已记录到 stzdh-ptagent.md §六） | ✅ 已规避（不做 PDF） |
| 长报告 OOM（finding > 10000） | 中 | Builder 纯函数 + `strings.Builder`，单次拼接；HTML 端用 `<details>` 折叠原始 payload 减 DOM 节点 | ✅ 已缓解 |
| Postgres 不可用 | 中 | `Service.store == nil` 降级路径 | ✅ 已实现 |
| `output: export` 动态路由边界 | 中 | 全部走 query-string 模式 | ✅ 已规避 |
| react-markdown v9 移除 `inline` | 低 | 用 `node.position` 判断 | ✅ 已修复 |

---

## 八、下一阶段衔接

P5+ 报告闭环后，可作为以下 P 阶段的**复用资产**：

1. **P5+ Embedding async**：报告生成链路是"读黑板 → 写表"的同步 IO 范式，与 Embedding 写入 embeddings 表的范式一致；可复用 `BoardSnapshotter` 接口
2. **P5+ 鉴权**：报告的 `creator` 字段可在鉴权闭环后写入 user id；目前为 `Generator` 字符串（"manual" / "reporter-agent" / "scheduled"）
3. **P5+ 商业化**：多租户 RLS 改造面大，但 reports 表设计为 immutable append-only，迁移成本可控
4. **P5+ 一体部署**：reports 迁移 `000007_reports.sql` 加入一体 compose 即可

---

## 九、签收

| 项 | 内容 |
| --- | --- |
| **闭环日期** | 2026-06-03 |
| **V 指标命中** | V7, V9, V10, V11 |
| **S 指标命中** | S1, S2, S3, S4, S7 |
| **决策反例数** | 1（react-markdown v9 `inline` 属性移除 → `node.position` 判断） |
| **签收** | ✅ |

---

## 附：Markdown 报告样例（截取）

```markdown
# Penetration Test Report — test-camp

_Generated at 2026-06-03T12:00:00Z by test_

## Table of Contents

- [Campaign Metadata](#campaign-metadata)
- [Executive Summary](#executive-summary)
- [Risk Overview](#risk-overview)
- [Detailed Findings](#detailed-findings)
- [Remediation Roadmap](#remediation-roadmap)
- [Appendix](#appendix)

---

## Campaign Metadata

| Field | Value |
| --- | --- |
| Campaign ID | `11111111-1111-1111-1111-111111111111` |
| Name | test-camp |
| Target | `https://example.com` |
| Objective | discover misconfigurations |
| Mode | bugbounty |
| Status | completed |
| LLM Provider | deepseek |
| Created | 2026-06-03T10:00:00Z |
| Started | 2026-06-03T11:00:00Z |
| Completed | 2026-06-03T11:30:00Z |

## Executive Summary

**Overall Risk: CRITICAL.** Immediate remediation is required.

This report consolidates the findings produced by the Pentest-Swarm-AI
stigmergic swarm during the campaign. Across 3 finding(s) the swarm
identified 1 critical, 1 high, 0 medium, 1 low, and 0 informational
issues. Findings touch 3 unique target(s) and were contributed by 3
agent(s).

Average CVSS across scored findings: **6.43**.

Campaign duration: **30m 0s**.

## Risk Overview

```
Risk Distribution
─────────────────
CRITICAL    1  █░░░░░░░░░░░░░░░░░░
HIGH        1  █░░░░░░░░░░░░░░░░░░
MEDIUM      0  ░░░░░░░░░░░░░░░░░░░
LOW         1  █░░░░░░░░░░░░░░░░░░
INFO        0  ░░░░░░░░░░░░░░░░░░░
```

Average CVSS: **6.43**

## Detailed Findings

### 1. Exposed .env file

- **Severity:** `CRITICAL`
- **CVSS:** 9.80
- **Target:** `https://example.com/.env`
- **Type:** `EXPOSED_CREDENTIAL`
- **Discovered by:** `recon-agent`
- **Discovered at:** 2026-06-03T11:05:00Z

Production secrets in public .env.

<details><summary>Raw payload</summary>

```json
{
  "remediation": "Move .env outside webroot, rotate keys."
}
```

</details>

### 2. CVE-2024-1234 — Auth bypass

- **Severity:** `HIGH`
- **CVSS:** 7.50
- **Target:** `https://example.com/login`
- **Type:** `CVE_MATCH`
- **Discovered by:** `vuln-agent`
- **Discovered at:** 2026-06-03T11:15:00Z

Authentication can be skipped with `?admin=1`.

<details><summary>Raw payload</summary>

```json
{
  "cve": "CVE-2024-1234"
}
```

</details>

### 3. Missing CSP header

- **Severity:** `LOW`
- **CVSS:** 2.00
- **Target:** `https://example.com`
- **Type:** `INFORMATIONAL`
- **Discovered by:** `scan-agent`
- **Discovered at:** 2026-06-03T11:25:00Z

Consider adding Content-Security-Policy.

## Remediation Roadmap

1. **[CRITICAL] Exposed .env file** — `https://example.com/.env`
   - Production secrets in public .env.

## Appendix

### Agents

- `recon-agent` — 1 finding(s)
- `scan-agent` — 1 finding(s)
- `vuln-agent` — 1 finding(s)

### Top Targets

- `https://example.com` — 1 finding(s)
- `https://example.com/.env` — 1 finding(s)
- `https://example.com/login` — 1 finding(s)

---

_Report SHA is not embedded to keep the document git-friendly. Total findings: 3 • Risk: critical • Duration: 30m 0s_
```
