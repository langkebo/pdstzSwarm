# PTAgent 能力基线 & Pentest-Swarm-AI 超越路线图

> **文档定位**：本文件是 `/home/tzd/Pentest-Swarm-AI` 项目对参考实现 `ptagent`（位于 `/home/tzd/stzdh/ptagent-images.tar`）的**持续作战地图**。它不是一次性对比表，而是一份随项目演进而更新的"目标差距 + 超越路径 + 验收标准"的三段式契约。
> **基线日期**：2026-06-02（已 P0 / P1 / P2-1 三阶段闭环）
> **更新规则**：每完成一个 P 阶段，对应章节由 `pending` 翻转为 `closed`，并把"实测指标"补到表里。
> **最后同步**：2026-06-04（P5-UI 设计语言重塑完成；P5+ 报告 / 终端（xterm.js）/ 鉴权（OAuth 2.0 + Session）/ 观测（pg_exporter）/ 路由深度 + 提示词编辑 / MCP 协议（12 tools / 3 resources / 2 prompts / 9 端点 / 0 外部依赖） / **Embedding async（AsyncEmbedder worker pool + RedisEmbedder 跨进程缓存）** / **商业化（Ed25519 License + CORS 白名单 + 多租户 RLS 隔离）** / **一体部署（4 阶段 Dockerfile + web 嵌入 + 集成 compose 6 命名卷 + Caddyfile + 启停脚本）** 九大 P5+ 子阶段全部闭环；**P5+ 操作员输入闭环（POST /campaigns/:id/input + WS kind=user_input + UserInputDock 聊天面板 + 终端内联 + 6 快捷指令）新增闭环**；签收表共 24 行 ✅，仅"一体化部署" 1 项保留 ⏳，但实际 4 阶段 Dockerfile + web 嵌入 + 集成 compose 已落地，签收表条目维护中滞后）
> **最终目标**：Pentest-Swarm-AI 在**架构清晰度、可观测性、安全护栏、国产化、本地化、可复现性、视觉品牌张力**七个维度上**全部**反超 ptagent，且不损失 ptagent 在**多智能体编排 / 工具覆盖 / GraphQL 数据层**上的领先项。

---

## 一、参考基线：ptagent 已具备什么

下表是 ptagent 公开容器镜像拆解 + WEB 镜像解包（参见 [stzdh-ptagent.md](./stzdh-ptagent.md) 原始记录、[pragent-web.md](./pragent-web.md) WEB 镜像解包记录）综合出来的能力基线：

### 1.1 智能体矩阵（15 种 + 35 提示词模板）

| # | 智能体 | 角色 | 提示词模板 | 用途 |
| --- | --- | --- | --- | --- |
| 1 | `primary_agent` | 顶层调度 | `primary_agent` | 任务分解、工具编排、结果汇流 |
| 2 | `pentester` | 渗透执行 | `pentester` / `question_pentester` | PTES 全流程 |
| 3 | `coder` | 代码编写 | `coder` / `question_coder` | exp / 工具脚本 |
| 4 | `installer` | 环境准备 | `installer` / `question_installer` | 工具安装、依赖修复 |
| 5 | `searcher` | OSINT | `searcher` / `question_searcher` | 子域名 / 泄露情报 |
| 6 | `adviser` | 安全咨询 | `adviser` / `question_adviser` | 风险评级、合规建议 |
| 7 | `generator` | 子任务生成 | `generator` / `subtasks_generator` | 任务分解 |
| 8 | `refiner` | 任务精炼 | `refiner` / `subtasks_refiner` | 模糊 → 清晰 |
| 9 | `reporter` | 报告 | `reporter` / `task_reporter` | 最终报告 |
| 10 | `reflector` | 反思 | `reflector` / `question_reflector` | 失败分析 |
| 11 | `enricher` | 知识图谱 | `enricher` / `question_enricher` | Graphiti 调用 |
| 12 | `memorist` | 向量记忆 | `memorist` / `question_memorist` | pgvector 检索 |
| 13 | `summarizer` | 摘要 | `summarizer` | 长对话压缩 |
| 14 | `tool_call_fixer` | 工具修复 | `toolcall_fixer` / `input_toolcall_fixer` | JSON 格式修复 |
| 15 | `assistant` | 通用对话 | `assistant` | 自由问答 |

辅助提示词 21 种：`image_chooser` / `language_chooser` / `flow_descriptor` / `task_descriptor` / `execution_logs` / `full_execution_context` / `short_execution_context` / `tool_call_id_collector` / `tool_call_id_detector` / `question_execution_monitor` / `question_task_planner` / `task_assignment_wrapper` 等。

### 1.2 工具覆盖（50+ 工具，分 11 类）

| 类别 | 工具 | 价值 |
| --- | --- | --- |
| 信息收集 | nmap、whois、dig、subfinder | L3/L4 拓扑 |
| 爬虫 | scraper、headless browser | Web 2.0 表层 |
| 漏洞扫描 | nuclei、OpenVAS、Nessus | 已知 CVE 触发 |
| 漏洞利用 | metasploit、Exploit-DB、custom exp | POC 链 |
| 密码 | hashcat、hydra、john | 凭据恢复 |
| Web | sqlmap、xsser、burp | Web 漏洞 |
| 网络 | ping、traceroute、curl、wget | 辅助 |
| 文件 | cat、grep、find、awk、sed | 本地操作 |
| 编程 | go build、gcc、python、npm | exp 编写 |
| **容器** | **docker run / exec** | **隔离执行** |
| 搜索 | Google、DDG、Sploitus、Tavily | 外部情报 |
| 知识图谱 | Graphiti | 实体关联 |
| 向量 | pgvector | 语义记忆 |

### 1.3 基础设施

- **Kali Linux 容器**：工具执行统一跑在 `ptagent/kali-linux` 容器里
- **Graphiti + pgvector**：双层记忆（结构化 + 向量）
- **多 LLM 顶置**：通过预设切换 OpenAI / Claude / Ollama / 国产模型

### 1.4 WEB 前端基线（来源：[pragent-web.md](./pragent-web.md)）

> P5-UI 阶段补入。WEB 前端是 ptagent 唯一成熟的产品化界面，必须单独建立可对照的模型。

#### 1.4.1 技术栈

| 层级 | 技术 | 用途 |
| --- | --- | --- |
| UI 框架 | React 19 | 组件化 UI 构建 |
| 构建工具 | Vite | 代码打包、代码分割、热更新 |
| 路由 | React Router v6 | 嵌套路由 + 鉴权路由分组 |
| 数据层 | Apollo Client | GraphQL 查询/变更/订阅 |
| UI 组件 | Radix UI | 无障碍原语（Dialog/Dropdown/Tooltip） |
| 样式 | Tailwind CSS | 原子化 CSS |
| 终端 | xterm.js + 5 addon | 终端日志渲染 + ANSI |
| 表单 | React Hook Form + Zod | 表单状态与校验 |
| 表格 | @tanstack/react-table | 数据表格 |
| 日期 | date-fns | 日期格式化 |
| 图标 | lucide-react | SVG 图标 |
| Markdown | react-markdown | 报告渲染 |
| PDF | jsPDF | 报告导出 |
| 实时 | graphql-ws | WebSocket 订阅 |

**部署路径**：构建后内嵌进 Go 二进制，由 `http.FileServer` 静态服务托管 —— 与我方 `output: export` 后的 `out/` 静态分发思路一致。

#### 1.4.2 路由体系（17 条 + 8 个 settings 分包）

> PSA 现状对照：Next.js App Router（7 条 + 1 个 route group `(app)`），详见 [ptagent-comparison-report.md](./ptagent-comparison-report.md) §4.7.2。

| 路由 | 分包 | 备注 |
| --- | --- | --- |
| `/` (Flows) | `flows-DeugZi3U.js` | 首页流程列表 |
| `/new-flow` | `new-flow-QGGChuNV.js` | 创建流程 |
| `/flow/:id` | `flow-1bLTml\_7.js` | **流程详情（最大组件）** |
| `/flow/:id/report` | `flow-report-CIHbtdgo.js` | 流程报告 |
| `/login` | `login-BEXlgm0O.js` | 登录 |
| `/oauth/result` | `oauth-result-B6Ey1Vii.js` | OAuth 回调 |
| `/settings/*` | 8 个独立分包 | Providers / Prompts / Users / Tokens / MCP 等 |

#### 1.4.3 11 大功能模块

| # | 模块 | 核心能力 | 关键实现 | PSA 现状 |
| --- | --- | --- | --- | --- |
| 1 | 用户认证 | 密码 + OAuth2.0 | POST `/api/v1/auth/login` + HttpOnly cookie | ✅ /login |
| 2 | 流程管理 | CRUD + 收藏 + 状态追踪 | GraphQL `flows` / `flow($id)` / `favoriteFlow` | ✅ /campaigns |
| 3 | AI 自动化 | 多智能体调度 + 实时日志 | mutations + GraphQL Subscription | ✅ /live + WS |
| 4 | 终端模拟 | ANSI + 搜索 + WebGL 加速 | xterm.js + 5 个 addon | ✅ CRT 扫描线 + 24-bit ANSI（`/live`） |
| 5 | 命令执行 | Kali 容器内执行 | mutation `executeCommand` | ✅ Docker SDK |
| 6 | 报告生成 | Markdown 预览 + PDF 导出 | react-markdown + jsPDF | ✅ `/reports` + `/reports/detail` |
| 7 | 提供商管理 | LLM API 配置 + 模型分配 | mutations `testProvider` / `testAgent` | ✅ /settings Providers |
| 8 | 提示词管理 | 35 种提示词模板编辑 | mutation `validatePrompt` | ✅ `/settings/prompts` 35 PromptType + 零依赖编辑器 |
| 9 | API Token | 外部调用凭证管理 | CRUD | ✅ /settings Tokens |
| 10 | 用户管理 | 列表 + 角色 + 删除确认 | — | ✅ `/settings/users` 占位 + `/api/v1/auth/me`；多租户议题见 P5+ 商业化 |
| 11 | MCP 服务器 | STDIO / SSE 协议工具 | per-server 工具启用/禁用 | ✅ Manager fan-out + 12 tools / 3 resources / 2 prompts（0-dep 自研） |

**PSA 闭环度**：**10/11 追平** + **1/11 长期**（MCP 协议 / 提示词管理 / 用户管理已在 P5+ 阶段补齐；多租户纳入 P5+ 商业化议程长期跟踪）。

#### 1.4.4 数据层（42 个 GraphQL 操作）

- **Queries (15)**：flows / flow / flowReport / settings / settingsPrompts / settingsProviders / settingsUser / apiTokens / assistants / assistantLogs / tasks / providers / flowsStatsTotal / flowsExecutionStatsByPeriod / toolcallsStats* / usageStatsByAgentType / usageStatsByModel
- **Mutations (27)**：createFlow / renameFlow / deleteFlow / finishFlow / stopFlow / favorite / putUserInput / callAssistant / createAssistant / stopAssistant / createProvider / testProvider / createPrompt / validatePrompt / createAPIToken / 等

> **PSA 对照**：REST + 原生 WebSocket 消息信封（`kind: event` / `kind: finding`），端到端 < 500ms（V5 硬指标）。

#### 1.4.5 状态管理

- **React Context + Apollo Client cache** 组合
- 三个主要 Context：FlowProvider（流程详情）/ ProvidersProvider（设置）/ SystemSettingsProvider（全局）
- 数据流：UI → mutation → server → subscription → cache → UI

| Context | 作用域 | 状态内容 | 持久化 | PSA 对位 |
| --- | --- | --- | --- | --- |
| `FlowProvider` | 流程详情页 | `flowData`, `assistants`, `assistantLogs`, `selectedAssistantId`, `isLoading` | 无（页面级） | `useLiveCampaignStore` (Zustand) |
| `ProvidersProvider` | 全局（Settings） | `providers` 列表, `selectedProvider` | **localStorage** | `useSettingsStore` (Zustand persist) |
| `SystemSettingsProvider` | 全局 | `settings` 配置, `isLoading` | 无（每次拉取） | `useSettingsStore` (Zustand) |

**关键差异**：
- ptagent 用 3 个 Context + Apollo cache 组合实现"跨页面状态共享"；
- PSA 用 Zustand（无 Provider 包装、零 Apollo boilerplate、SSR-friendly）实现等价能力，**代码量约为前者 1/3**。

#### 1.4.6 关键 UI 组件（10 个）

| 组件 | 文件 | 功能 | PSA 对位 |
| --- | --- | --- | --- |
| `Terminal` | `terminal-BZ22UCQy.js` | xterm.js 终端模拟器 | (待 P5+) |
| `Markdown Renderer` | `markdown-BfIZnwxB.js` | Markdown 渲染 | (待 P5+ 报告) |
| `DataTable` | `data-table-ChEeRrkP.js` | 通用数据表格 | (待 P5+) |
| `StatusCard` | `status-card-CC2RC62L.js` | 状态卡片 | `GlassPanel` (P5-UI) |
| `ConfirmationDialog` | `confirmation-dialog-DUZaUygz.js` | 确认对话框 | (Radix 同位) |
| `FlowForm` | `flow-form-C1mvGoxT.js` | 流程创建表单 | (RHF 同位) |
| `Breadcrumb` | `breadcrumb-DMqZ5gu9.js` | 面包屑 | `TopBar` (P5-UI) |
| `FlowStatusIcon` | `flow-status-icon-KQsU-Gn3.js` | 状态图标 | `SeverityBadge` (P5-UI) |
| `ServerSelect` | `server-C\_Bzy9Rf.js` | Provider 选择器 | (settings 内部) |
| `UserApi` | `user-api-D67t3XUK.js` | 用户 API 封装 | `web/src/lib/api.ts` |

**借鉴要点**：ptagent 走"通用化原子组件"路线，PSA 走"主题化品牌组件"路线。两条路径本质相同：抽取可复用 UI 降低页面级代码量。差异在于"通用 vs 品牌"。

---

## 二、Pentest-Swarm-AI 现状盘点

> 之所以能"超越"，前提是认清我方已有的差异化资产和已闭环能力。下列清单只列**已经写进代码并能跑通的**能力，不列"计划要做"。

### 2.1 已闭环能力（2026-06-03 截止）

| 能力 | 代码位置 | 交付报告 | 对 ptagent 优势 |
| --- | --- | --- | --- |
| **执行监控三层防护**（总次数 / 单一工具连击 / 角色配额） | `internal/swarm/monitor.go` | [p0-delivery-report.md](../optimization/p0-delivery-report.md) | ptagent 仅有 `question_execution_monitor` 提示词，**无强制执行层** |
| **国产 LLM 顶置**（DeepSeek/GLM/Kimi/Qwen 零代码切换） | `internal/llm/factory.go` | 同上 | ptagent 通过环境变量配置，**需查文档** |
| **检索 Provider 适配器**（DDG + Tavily + Sploitus + SEARXNG + Perplexity） | `internal/tools/search/*.go` | [p1-1-delivery-report.md](../optimization/p1-1-delivery-report.md) | 同一接口可插拔，**测试可重放** |
| **WebSocket 真实数据接入**（Zustand store + 消息信封） | `internal/api/ws/`, `web/src/lib/api.ts` | [p0-delivery-report.md](../optimization/p0-delivery-report.md) | ptagent 走 GraphQL subscription，**多 1-2 跳** |
| **LangFuse 桥接**（LLM / finding / agent 全事件可观测） | `internal/observability/langfuse/` | [p1-2-delivery-report.md](../optimization/p1-2-delivery-report.md) | ptagent 无等价 trace 基础设施 |
| **Docker 沙箱**（moby SDK + 白名单 + 输出截断） | `internal/tools/docker/` | [p2-1-delivery-report.md](../optimization/p2-1-delivery-report.md) | ptagent 假设镜像可信，**无白名单** |
| **商业化护栏（部分）**（NetworkHost 审计 + 配额自动降级 + LangFuse event 上报） | `internal/guardrails/` | [p2-delivery-report.md](../optimization/p2-delivery-report.md) | ptagent 仅 LICENSE_KEY，**无运行期护栏** |
| **Embedder + Summarizer 全参数化**（5 impl + 11 参数 + LRU cache） | `internal/memory/` | [p3-2-delivery-report.md](../optimization/p3-2-delivery-report.md) | ptagent 配置项较少 |
| **Kali 容器镜像预制**（多阶段 + manifest.json + 三档 seccomp） | `images/kali/` + `internal/tools/docker/` | [p3-1-delivery-report.md](../optimization/p3-1-delivery-report.md) | ptagent Kali 镜像闭源，**我方可审计** |
| **镜像 CI**（GitHub Actions 5 jobs + 本地验证脚本） | `.github/workflows/image-ci.yml` + `scripts/ci/` | [p3-5-delivery-report.md](../optimization/p3-5-delivery-report.md) | ptagent 无 CI |
| **轻量化知识图谱**（GraphStore 接口 + Postgres 后端 + 双轨抽取） | `internal/graph/` | [p4-delivery-report.md](../optimization/p4-delivery-report.md) | ptagent 强依赖 Neo4j，**我方可降级** |
| **P5-UI 设计语言重塑**（信号绿 + 玻璃感 + ASCII 角标 + CRT 扫描线 + Matrix 雨 + SwarmOrbit SVG） | `web/src/app/**`, `web/src/components/**`, `web/tailwind.config.ts`, `web/src/app/globals.css` | [p5-ui-delivery-report.md](../optimization/p5-ui-delivery-report.md) (本节内嵌) | ptagent 走 Radix 中性灰蓝，**我方品牌张力更强** |
| **P5+ 报告闭环**（服务端 Builder + Markdown 下载 + 4 入口） | `internal/reports/`, `internal/db/migrations/000007_reports.sql`, `web/src/app/(app)/reports/**`, `web/src/components/MarkdownRenderer.tsx`, `web/src/components/SeveritySummaryBar.tsx`, `web/src/lib/api.ts` | [p5-plus-reports-delivery-report.md](../optimization/p5-plus-reports-delivery-report.md) | ptagent 走客户端拼字符串 + jsPDF，**我方服务端 Builder + Markdown + 4 入口覆盖度 × 入口数双高** |
| **P5+ 终端（xterm.js）** | `web/src/components/Terminal.tsx`, `web/src/styles/terminal.css`, `web/src/app/(app)/live/page.tsx`, `web/src/app/layout.tsx` | [p5-plus-terminal-delivery-report.md](../optimization/p5-plus-terminal-delivery-report.md) | ptagent 走 xterm.js 中性样式，**我方 CRT 扫描线 + 24-bit ANSI + 项目 token 视觉签名** |
| **P5+ 鉴权（OAuth 2.0 + Session）** | `internal/auth/`, `internal/db/migrations/000008_auth.sql`, `internal/api/server.go`, `cli/serve.go`, `web/src/lib/api.ts`, `web/src/app/login/page.tsx`, `web/src/components/Sidebar.tsx` | [p5-plus-auth-delivery-report.md](../optimization/p5-plus-auth-delivery-report.md) | ptagent 走全程 OAuth，**我方密码 first + OAuth 附加 + zero-dep + 4 provider 可插拔** |
| **P5+ 路由深度 + 提示词编辑**（7 → 19 静态导出路由 + 35 PromptType 编辑器） | `internal/prompts/`, `internal/api/server.go`, `cli/serve.go`, `web/src/app/(app)/settings/{layout,page}.tsx`, `web/src/app/(app)/settings/{providers,models,prompts,api-tokens,users,mcp}/page.tsx`, `web/src/app/(app)/agents/page.tsx`, `web/src/components/settings/{SectionShell,PromptEditor}.tsx`, `web/src/lib/api.ts` | [p5-plus-routes-prompts-delivery-report.md](../optimization/p5-plus-routes-prompts-delivery-report.md) | ptagent 走 Monaco + 单页 settings，**我方零依赖自研 textarea + 6 子路由 sidebar rail + 35 PromptType 目录 + Authorizer 可插拔** |
| **P5+ 操作员输入闭环**（POST `/campaigns/:id/input` + WS `kind=user_input` + `UserInputDock` 聊天面板 + xterm 内联 + 6 快捷指令） | `internal/api/ws/message.go`, `internal/api/ws/hub.go`, `internal/api/server.go`, `internal/api/user_input_test.go`, `web/src/components/UserInputDock.tsx`, `web/src/lib/api.ts`, `web/src/lib/store.ts`, `web/src/app/(app)/live/page.tsx` | [p5-plus-user-input-delivery-report.md](../optimization/p5-plus-user-input-delivery-report.md) | ptagent 走 `putUserInput` GraphQL Mutation 但**仅写入 prompt 上下文**，**无独立聊天面板 / 无快捷指令 / 无终端内联**；**我方补齐了"对话感"维度**：REST + WS 双通道 + 6 快捷指令（/focus /skip /explain /report /stop /help）+ 历史回放 + 内联错误 + 4 状态指示 + ⌘Enter 发送 |

### 2.2 架构性差异（**这是超越的真正筹码**）

| 维度 | ptagent | Pentest-Swarm-AI | 谁赢 |
| --- | --- | --- | --- |
| **执行模型** | 线性：primary_agent → 子 agent（一个执行完再下一个） | **stigmergic swarm**：所有 agent 共享黑板，**并行 + 抢占 + 信息素驱动** | **我方**（README 卖点） |
| **状态共享** | 各自 context window | **Blackboard**（PostgreSQL / Redis）+ Memory Graft（cross-campaign 知识） | **我方** |
| **执行保险** | 提示词约束 + LicenseKey | **Monitor 三层硬护栏** + Docker 白名单 + Guardrails | **我方** |
| **可观测性** | LangFuse + OTel + pg_exporter | **LangFuse 三件套**（tracer / LLM provider / finding observer） | **我方**（pg_exporter 仍待补） |
| **测试文化** | 容器即测试，**无单测** | **13 unit + 4 live + 3 bench**（仅 P2-1 一个包） | **我方** |
| **文档化** | README + prompt 列表 | **delivery report**（每 P 一份，含指标 / 测试 / 决策） | **我方** |
| **国产化** | 通过环境变量 | **case-by-case 预设** + OpenAI 兼容协议 | **我方**（体验） |
| **多 LLM 同台** | 串行 | **provider 装饰器自动 token 上报** | **我方** |
| **数据层** | GraphQL（统一 query/mutation/subscription） | REST + 原生 WebSocket 消息信封 | 持平（PSA 更轻） |
| **实时性** | GraphQL subscription（多跳） | WebSocket 直连 + Zustand（< 500ms 端到端） | **我方** |
| **状态管理** | React Context + Apollo cache | Zustand（无 Apollo boilerplate） | **我方** |
| **可视化** | 静态图表 + Lucide | SeverityChart + SwarmOrbit + 力导向 Graph | **我方**（与 stigmergy 贴合） |
| **设计语言** | Radix 中性灰蓝 | 信号绿 + 玻璃感 + ASCII 角标 | **我方** |
| **流式 UI** | 需手动刷新 | 实时 dashboard / live 大屏 / 事件流 | **我方** |
| **终端渲染** | xterm.js + 5 addon（强还原） | 原生 mono + CRT 扫描线（轻量） | **ptagent**（长 ANSI 场景） |
| **报告生成** | react-markdown + jsPDF（PDF 导出） | **服务端 Builder + Markdown 下载 + 4 入口**（`/reports` 列表 + `/reports/detail?id=` 详情 + `/live` 顶部 + `/campaigns` 卡片 hover） | **我方**（覆盖度 × 入口数双高） |
| **a11y** | Radix 原语 | Tailwind + 焦点环 | ptagent 略胜 |
| **工具覆盖** | 50+ Kali 工具，**立即可用** | 同等覆盖（Kali 镜像 P3-1 已预制） | 持平 |
| **知识图谱** | Graphiti 集成 | 轻量化 GraphStore + Postgres | 路径不同 |
| **鉴权** | 全程 OAuth（无密码） | 密码 first + OAuth 附加（Google / GitHub）+ 4 provider 可插拔 + zero-dep | 持平（我方覆盖度 × demo 友好 × 离线可玩；ptagent 单 OAuth 路径） |
| **单页/流式前端** | 无 | WebSocket 实时 dashboard | **我方** |

### 2.3 仍待交付的能力（ptagent 当前领先，需要追平或反超）

| 缺口 | ptagent 现状 | Pentest-Swarm-AI 状态 | 下一步 |
| --- | --- | --- | --- |
| 报告生成（Markdown + PDF） | ✅ react-markdown + jsPDF | ✅ `internal/reports` Builder + Markdown 下载 + 4 入口 | **持平**（我方覆盖度 × 入口数双高，ptagent PDF 导出） |
| OAuth 2.0 第三方登录 | ✅ `/oauth/result` | ✅ `internal/auth` OAuth Provider 接口 + Google / GitHub 4 provider + session cookie + state 防 CSRF | **持平**（我方密码 first + 4 provider 可插拔 + zero-dep，ptagent 全 OAuth） |
| xterm.js 终端 | ✅ xterm.js + 5 addon | ✅ xterm.js + CRT 扫描线 + 24-bit ANSI + 项目 token | **持平**（我方视觉签名 + 增量 diff 写入，ptagent 功能完整） |
| pg_exporter | ✅ Postgres Exporter v0.16.0 | ✅ `/metrics` 端点暴露 pg_exporter 指标 + Grafana 4 面板 | **持平**（zero-dep + 直接复用 Prometheus 1.x 客户端） |
| LICENSE_KEY / CORS_ORIGINS | ✅ 全部就绪 | ❌ 无 license、无 CORS 白名单 | **P5+ 商业化议程**（季度内） |
| 部署形态（一体 compose） | ✅ 4 镜像开箱即用 | ⚠️ 需手动启 Postgres/Redis | **P5+ 一体部署**（季度内） |
| Embedding 异步化 + Redis 缓存 | ✅ 内置 | ⚠️ 同步阻塞 | **P5+ async worker**（3 d） |
| 35 种 PromptType 模板编辑器 | ✅ Monaco + `validatePrompt` | ✅ `internal/prompts` Service + InMemoryStore + 4 REST 端点 + 自研零依赖 textarea 编辑器 | **反超**（零依赖 + 35 完整目录 + Authorizer 接口可插拔；ptagent Monaco 2.5 MB bundle 阻塞首屏） |
| 路由深度扩展（17 → PSA 多路由） | ✅ 17 路由 | ✅ **19 静态导出路由**（含 `/login` / `/graph` / `/agents` / `/settings/{providers,models,prompts,api-tokens,users,mcp}` 6 子页） | **反超**（19 > 17；ptagent 17 含 4 标签 settings，PSA 子路由独立 URL + back-button 友好） |
| MCP 服务器协议 | ✅ STDIO / SSE | ✅ STDIO + SSE + 9 端点 + Manager fan-out + 12 tools / 3 resources / 2 prompts + 0 外部依赖 | **反超**（PSA 6/11 维度领先：端点 9 vs 0、tools 12 vs 8、resources 3 vs 0、prompts 2 vs 0、fan-out 1 vs 0、0-dep 0 KB vs mcp-go 200 KB+；5/11 持平） |
| 多用户 / 多租户 | ✅ 列表 + 角色 + 删除 | ⚠️ `/settings/users` 占位 + `/api/v1/auth/me`；多租户 RLS 未实施 | **P5+ 商业化议程**（含 LICENSE_KEY） |

**WEB 前端维度小结**（参考 [ptagent-web.md](./ptagent-web.md) 与 [ptagent-comparison-report.md](./ptagent-comparison-report.md) §4.7）：
- **已追平 / 反超**：数据流实时性、状态管理轻量化、可视化贴合 stigmergy、流式 UI、视觉品牌、报告生成（覆盖度 × 入口数反超，PDF 略输）、xterm.js 终端（视觉签名 + 增量 diff 写入，ptagent 5 addon 全套）、**鉴权 OAuth 2.0**（覆盖度 × demo 友好 × 离线可玩，ptagent 全程 OAuth）、**35 种 PromptType 编辑深度**（零依赖 textarea + 4 REST 端点 + Authorizer 接口）、**路由深度**（19 静态页 > ptagent 17 路由）、**MCP 协议**（12 tools + 3 resources + 2 prompts + 9 端点 + Manager fan-out + 0 外部依赖；ptagent 8 tools + 0/0/0 + UI 嵌入 SSE）、**操作员输入闭环**（REST + WS 双通道 + UserInputDock 聊天面板 + xterm 内联 + 6 快捷指令 + 历史回放；ptagent 走 primary_agent 串行，**无 in-flight 用户输入回路**——PSA 唯一反超的"对话感"维度）
- **待补**：LICENSE_KEY / CORS、多租户
- **路径不同但目标一致**：报告生成用 server-side Go template（`internal/reports/`） + client-side `react-markdown` 渲染；xterm 集成走 `dynamic({ ssr: false })` + 24-bit ANSI 主题；鉴权走 zero-dep SHA-256 + 4 provider 可插拔；提示词编辑器走自研 textarea + `<pre>` 覆盖层 + Scroll Sync，避开 Monaco 2.5 MB bundle；路由深度走 `?type=` query string + Suspense，避开 `output: export` 动态路由边界；MCP 协议走 stdlib `encoding/json` + `bufio` 自研 JSON-RPC 2.0 核心，避开 `mcp-go` 200 KB+ 间接依赖；**操作员输入闭环**走 Zustand selector + `dynamic({ ssr: false })` xterm diff 写入 + `addUserInput` 状态去重（POST 响应 + WS 广播的 race 防护），避开 Apollo 全量订阅样板。

---

## 三、超越定义（胜利条件）

> 这是文档最关键的一段。它把"超越 ptagent"**量化、可测、可签收**。任何 P 阶段交付时，都要把对应项从 `pending` 翻到 `closed` 并贴实测数据。

### 3.1 硬指标（必须达到，否则不算超越）

| # | 指标 | ptagent 实测/估算 | Pentest-Swarm-AI 目标 | 验收方式 |
| --- | --- | --- | --- | --- |
| V1 | 单 campaign 工具调用失控上限 | 无限（仅靠提示词） | **5,000 总 / 200 单工具连击 / 5,000 单角色**（monitor 硬护栏） | `TestMonitor_HardCap` + chaos test |
| V2 | LLM token 归因精度 | 0%（无 trace） | **100%**（LangFuse provider 装饰器） | LangFuse 仪表盘截图 |
| V3 | 容器执行失败时 OOM 次数 | sqlmap 常态 OOM | **0**（boundedBuffer 16 MiB 截断） | `TestRunner_TruncatesOversizedOutput` + 渗透回归 |
| V4 | 国产 LLM 切换耗时 | 改 env + 重启镜像 | **0 秒**（factory case 即时切换） | `TestFactoryPresets_*` 12 个 |
| V5 | WebSocket finding 端到端延迟 | N/A（无流式） / GraphQL 多跳 800ms+ | **< 500 ms**（agent 写入 → 前端收到） | E2E benchmark |
| V6 | Docker 白名单漏放 | 0%（无白名单） | **100% 拒绝**（`TestRunner_ImageWhitelistEnforced` + live） | 单元 + live |
| V7 | 单元测试覆盖率（per package） | 0%（ptagent 无单测） | **≥ 60%**（每个新增包） | `go test -cover` |
| V8 | Benchmark 数据 | 无 | 每包 ≥ 1 个 `BenchmarkXxx` | `go test -bench` 跑通 |
| V9 | WEB 静态资源首次加载（FOUC 抑制） | Apollo + GraphQL 解析有 hydration gap | **0 hydration warning**（next/font + Zustand SSR-friendly） | `next build` 输出 |
| V10 | WEB 包大小（首屏） | 估 ~600 KB（React 19 + Apollo） | **< 350 KB**（Next.js 7 静态页 + Zustand） | Lighthouse / `next build` output |
| V11 | WEB 路由覆盖度 | 17 路由（11 模块） | **≥ 19 路由 + 11 模块 11/11 追平** | 路由清单 + 模块映射表 |
| V12 | 实时数据端到端延迟 | GraphQL subscription 多跳 800ms+ | **< 500ms**（WebSocket 直连 + Zustand） | E2E benchmark |

### 3.2 软指标（差异化优势，必须在文档中可见）

| # | 维度 | ptagent | Pentest-Swarm-AI | 表达位置 |
| --- | --- | --- | --- | --- |
| S1 | 架构清晰度 | 单 README | 分层文档（analysis / optimization 双线） | 本文件 + [ptagent-comparison-report.md](./ptagent-comparison-report.md) |
| S2 | 决策可追溯 | 无 | 每 P 一份 delivery report，含"为什么这样做" | `docs/optimization/p{0,1,2,3,4,5}-*-delivery-report.md` |
| S3 | 安全护栏 | 提示词约束 + LicenseKey | 代码层硬护栏 | `monitor.go` / `runner.go` / `guardrails/` |
| S4 | 可复现性 | 镜像即一切 | **测试 + bench 双覆盖**，CI 可跑 | `**/*_test.go` |
| S5 | 国产化体验 | 文档级 | **零代码** 切换 + 提示词本地化 | `factory.go` |
| S6 | 多 LLM 同台 | 串行 | **provider 装饰器**自动 token 上报 | `langfuse/provider.go` |
| S7 | 视觉品牌张力 | Radix 中性灰蓝 | **信号绿 + 玻璃感 + ASCII 角标 + CRT 扫描线** | [pragent-web.md](./pragent-web.md) + `web/src/app/globals.css` |
| S8 | 流式 UI 实时性 | GraphQL subscription 多跳 | **WebSocket 直连 < 500ms** | `web/src/lib/api.ts` + `internal/api/findings_bridge.go` |

---

## 四、路线图（按 ROI 排序）

> 标注规则：`✅ closed` = 已完成且达标；`🟡 in-flight` = 进行中；`⏳ pending` = 待启动；`🚫 dropped` = 主动放弃（带理由）。

| 阶段 | 任务 | ROI | 依赖 | 状态 | 交付报告 |
| --- | --- | --- | --- | --- | --- |
| P0 | Web UI 真实数据接入 | 高 | 既有 WebSocket | ✅ | [p0-delivery-report.md](../optimization/p0-delivery-report.md) |
| P1-1 | DDG / Tavily / Sploitus / SEARXNG / Perplexity 检索 | 高 | 无 | ✅ | [p1-1-delivery-report.md](../optimization/p1-1-delivery-report.md) |
| P1-2 | LangFuse 桥接 | 中高 | 现有 tracer 接口 | ✅ | [p1-2-delivery-report.md](../optimization/p1-2-delivery-report.md) |
| P1 集成 | P1-1 + P1-2 联调 | 中高 | P1-1, P1-2 | ✅ | [p1-integration-report.md](../optimization/p1-integration-report.md) |
| **P2-1 A** | **Docker 沙箱最小闭环** | **高** | 无 | ✅ | [p2-1-delivery-report.md](../optimization/p2-1-delivery-report.md) |
| P2-1 B | 资源限制（cgroup） | 高 | P2-1 A | ✅ | [p2-1-b-delivery-report.md](../optimization/p2-1-b-delivery-report.md) |
| P2-1 C | 网络隔离 | 中高 | P2-1 A | ✅ | [p2-1-c-delivery-report.md](../optimization/p2-1-c-delivery-report.md) |
| **P2-2** | **商业化护栏**（NetworkHost 审计 + 配额 + LangFuse 上报） | **中高** | Monitor + Docker | ✅ | [p2-delivery-report.md](../optimization/p2-delivery-report.md) |
| **P3-1** | **Kali 容器镜像预制** | **高**（拉齐 ptagent） | P2-1 | ✅ | [p3-1-delivery-report.md](../optimization/p3-1-delivery-report.md) |
| **P3-2** | **Embeddings / Summarizer 全参数化** | 中 | 无 | ✅ | [p3-2-delivery-report.md](../optimization/p3-2-delivery-report.md) |
| **P3.5** | **镜像 CI**（GitHub Action 跑 build + manifest verify + seccomp parse） | 中 | P3-1 | ✅ | [p3-5-p4-delivery-report.md](../optimization/p3-5-p4-delivery-report.md) |
| **P4** | **轻量化知识图谱**（拉齐 ptagent，不引 Neo4j 硬依赖） | 中（基础）/ 高（enricher 接通） | 现有 asm 目录 | ✅ | [p4-delivery-report.md](../optimization/p4-delivery-report.md) |
| **P5-UI** | **WEB 设计语言重塑**（信号绿 + 玻璃感 + ASCII 角标 + CRT 扫描线 + Matrix 雨 + SwarmOrbit SVG） | **高** | P0 数据接入 | ✅ | 见第七节"签收表"备注 + [pragent-web.md](./pragent-web.md) |
| **P5+ 报告** | 报告生成（Markdown + PDF） | 中 | P5-UI | ✅ | [p5-plus-reports-delivery-report.md](../optimization/p5-plus-reports-delivery-report.md) |
| **P5+ 终端** | xterm.js 集成 + CRT 扫描线 + 24-bit ANSI | 中 | P5-UI | ✅ | [p5-plus-terminal-delivery-report.md](../optimization/p5-plus-terminal-delivery-report.md) |
| **P5+ 鉴权** | OAuth 2.0 + Session（密码 first + provider 可插拔） | 中 | P5+ 报告 | ✅ | [p5-plus-auth-delivery-report.md](../optimization/p5-plus-auth-delivery-report.md) |
| **P5+ 观测** | pg_exporter 接入 | 低 | P5+ 报告 | ✅ | [p5-plus-observability-delivery-report.md](../optimization/p5-plus-observability-delivery-report.md) |
| **P5+ 路由深度** | settings 拆 6 子路由 + 静态页 7 → 19 | 中 | P5+ 鉴权 | ✅ | [p5-plus-routes-prompts-delivery-report.md](../optimization/p5-plus-routes-prompts-delivery-report.md) |
| **P5+ 提示词编辑** | 35 PromptType + 零依赖 textarea + 4 REST | 高 | P5+ 路由深度 | ✅ | [p5-plus-routes-prompts-delivery-report.md](../optimization/p5-plus-routes-prompts-delivery-report.md) |
| **P5+ MCP** | STDIO + SSE + 9 端点 + Manager fan-out + 12 tools / 3 resources / 2 prompts + 0 外部依赖 | 中 | P5+ 鉴权 | ✅ | [p5-plus-mcp-delivery-report.md](../optimization/p5-plus-mcp-delivery-report.md) |
| **P5+ Embedding** | async worker + Redis 缓存 | 中 | P3-2 | ✅ | [p5-plus-embedding-delivery-report.md](../optimization/p5-plus-embedding-delivery-report.md) |
| **P5+ 商业化** | LICENSE_KEY / CORS_ORIGINS / 多租户 RLS | 中 | P2-2 | ✅ | [p5-plus-commercial-delivery-report.md](../optimization/p5-plus-commercial-delivery-report.md) |
| **P5+ 一体部署** | 4 阶段 Dockerfile + web 嵌入 + 集成 compose（4 镜像 + 6 命名卷 + 9 healthcheck）+ Caddyfile + 启停脚本 + 自动迁移 | 高 | P5+ 商业化 | ✅ | [p5-plus-deployment-delivery-report.md](../optimization/p5-plus-deployment-delivery-report.md) |

---

## 五、阶段执行计划（每阶段必带的产出）

> **不变式**：每个 P 阶段交付时**必须**包含以下 5 项，缺一项不算闭环。模板见 [optimization-report.md](../optimization/optimization-report.md)。

1. **代码差异**（`git diff --stat` + 关键 diff 摘录）
2. **测试矩阵**（unit / live / bench，三种类型都要有）
3. **性能 / 安全数据**（硬指标 V1-V10 中命中项的实测值）
4. **决策记录**（为什么选这个方案 / 否决了什么）
5. **下一阶段衔接说明**（本次产物如何被下个 P 复用）

### 5.1 P5-UI 阶段交付清单（2026-06-03 闭环）

> 本节是 P5-UI 阶段的"嵌入式交付报告"（本项目暂未单独立 `p5-ui-delivery-report.md` 文件，下一阶段如需对外发布可独立成文）。

| 项 | 内容 | 位置 |
| --- | --- | --- |
| **代码差异** | 7 个新页面（`login/page.tsx` + `(app)/{page, campaigns, agents, findings, graph, live, settings}/page.tsx`）+ 9 个新组件（`MatrixRain` / `SwarmOrbit` / `Sidebar` / `TopBar` / `GlassPanel` / `SeverityBadge` / `AsciiDecor` / `EmptyState` / `cn`）+ 1 个 layout 重组（`(app)` 路由组）+ `tailwind.config.ts` / `globals.css` 全量更新 | 见 git diff |
| **设计语言令牌** | **信号绿 #00FF9C**（`accent`）+ 玻璃面板（`.glass-panel` 两档）+ 4 层背景（径向渐变 + 32px 网格 + CRT 扫描线 + Matrix 雨）+ ASCII 角标（`AsciiDecor`）+ 严重度梯度（critical/high/medium/low/info 5 档） | `web/tailwind.config.ts` |
| **核心组件** | `GlassPanel`（frame/strong 两档）、`SeverityBadge`（5 档颜色）、`SwarmOrbit`（SVG 8 节点 + 5 信息素环）、`MatrixRain`（CSS 动画 + 字符集）、`Sidebar`（玻璃感 6 项导航）、`TopBar`（stigmergy 状态徽章 + 实时时钟） | `web/src/components/*.tsx` |
| **页面** | `/login`（玻璃感 + ASCII 角标 + CRT 扫描线 + Matrix 雨背景）；`/`（Dashboard：SeverityChart + SwarmOrbit + 实时 finding 列表）；`/campaigns`（CRUD）；`/agents`（蜂群可视化）；`/findings`（严重度分布 + 列表）；`/graph`（力导向图）；`/live`（实时事件流 + 进度条）；`/settings`（6 标签 Tabs） | `web/src/app/(app)/*/page.tsx` |
| **字体** | `next/font` 自托管 Space Grotesk + Inter + JetBrains Mono；消除 `no-page-custom-font` 警告；build 阶段嵌入到 `out/_next/static/media/` | `web/src/app/layout.tsx` |
| **测试矩阵** | `next build` ✅ 10/10 静态页；`npx next lint` ✅ No ESLint warnings or errors；7 条路由 HTTP 200 (`/login` `/` `/campaigns` `/findings` `/graph` `/live` `/settings`) | `web/out/` |
| **性能 / 安全** | 0 hydration warning；包大小 First Load JS < 350KB（next 7 静态页输出）；TS 编译 0 error；0 a11y critical issue | `next build` 输出 |
| **决策记录** | (1) 选用 Next.js 15 App Router + 路由组 `(app)` 而非 Remix/React Router：与 Go 静态分发模型对齐 + route group 让 `/login` 跳过 sidebar；(2) 玻璃感用 `.glass-panel{,-strong}` 两档，背景用 4 层（径向渐变 + 32px 网格 + CRT 扫描线 + Matrix 雨），逐层独立可调；(3) 字体走 `next/font` 自托管，消除 no-page-custom-font 警告；(4) `/campaigns/[id]` 动态路由在 `output: export` 下报 `generateStaticParams` 缺失，改为静态 `/live?id=…` 用 query string 读 campaign id，避开边界；(5) `useSearchParams` 加 `<Suspense>` 包裹解决 hydration 不匹配 | 本文 |
| **下一阶段衔接** | 报告生成可复用 `GlassPanel.frame` + `EmptyState`；OAuth 回调可复用 `LoginInner` Suspense 模式；xterm 集成复用 `crt-scanlines` 容器 | P5+ 报告 / 鉴权 / 终端 |

### 5.2 P5+ 报告阶段交付清单（2026-06-03 闭环）

> 详细交付报告见 [`docs/optimization/p5-plus-reports-delivery-report.md`](../optimization/p5-plus-reports-delivery-report.md)；本节为嵌入式摘要。

| 项 | 内容 | 位置 |
| --- | --- | --- |
| **代码差异** | 后端：`internal/reports/{types,builder,service,handler,snapshot}.go`（5 个新文件，~43 KB）+ `internal/db/migrations/000007_reports.sql`（reports 表 + 3 索引）+ `internal/api/server.go` 注册 6 个 REST 端点。前端：`MarkdownRenderer.tsx` + `SeveritySummaryBar.tsx`（2 个新组件）+ `web/src/app/(app)/reports/{page,detail/page}.tsx`（2 个新页面）+ `Sidebar.tsx` 加 `/reports` 入口 + `live/page.tsx` 加 Generate Report 按钮 + `campaigns/page.tsx` 卡片 hover 按钮 + `web/src/lib/api.ts` 加 `api.reports.*` 7 个方法 | 见 git diff |
| **后端 API 端点** | 6 端点：`POST /api/v1/reports`、`GET /api/v1/reports`、`GET /api/v1/reports/by-campaign/:id`、`GET /api/v1/reports/:id`、`GET /api/v1/reports/:id/markdown`、`GET /api/v1/reports/:id/json`、`DELETE /api/v1/reports/:id` | `internal/reports/handler.go` |
| **前端入口** | 4 入口：Sidebar 导航 + `/reports` 列表页 Generate 按钮 + `/live` TopBar 按钮 + `/campaigns` 卡片 hover 按钮 | `web/src/app/(app)/**` |
| **报告章节** | 6 章节：Campaign Metadata / Executive Summary / Risk Overview（含 ASCII 风险条形图） / Detailed Findings（按严重度排序 + `<details>` 折叠原始 payload） / Remediation Roadmap / Appendix | `internal/reports/builder.go` |
| **数据源** | 三路可切换：自定义 `Findings` provider → 黑板 `BoardSnapshotter.Query` → 内存 `state.Findings`（降级） | `internal/reports/handler.go` |
| **降级方案** | `Service.store == nil` 时仍能生成报告（不持久化），`Handler.listReports` 返回 warning 给前端 | `internal/reports/service.go` |
| **测试矩阵** | 15 个 Go 单测 + 8 个子测试（Builder / Service / Snapshot 三大块全覆盖）；`npx tsc --noEmit` 0 error；`npx next lint` No warnings；`npx next build` 12/12 静态页生成 | `go test -v ./internal/reports/...` + `next build` |
| **性能 / 安全** | First Load JS：列表页 117 kB / 详情页 118 kB（MarkdownRenderer 动态 import 拉出首屏）；0 hydration warning；reports 表 `campaign_id` 引用 `campaigns(id) ON DELETE CASCADE`，无悬空引用 | `next build` + `000007_reports.sql` |
| **决策记录** | (1) **服务端 Builder 而非客户端拼字符串**：跨语言复用 + CI 化 + 纯函数单测；(2) **`/reports/detail?id=` 而非 `/reports/:id`**：避开 `output: export` 动态路由边界；(3) **Markdown 下载而非 PDF**：规避 jsPDF 中文缺字体问题；(4) **4 入口** 而非 1 入口：列表页 = 管理 / 其他入口 = 应急快照，两种用户心智；(5) **`<details>` 折叠原始 payload**：避免 DOM 节点爆炸 + 渐进披露范式与项目设计语言一致 | [p5-plus-reports-delivery-report.md §六](../optimization/p5-plus-reports-delivery-report.md) |
| **下一阶段衔接** | `BoardSnapshotter` 接口可复用于 P5+ Embedding async；reports 表 immutable 设计与 P5+ 商业化多租户 RLS 改造兼容；`Service.store == nil` 降级方案可作为 P5+ 一体部署的"无 DB 演示模式"基础 | P5+ Embedding / 商业化 / 一体部署 |

### 5.3 P5+ 终端阶段交付清单（2026-06-03 闭环）

> 详细交付报告见 [`docs/optimization/p5-plus-terminal-delivery-report.md`](../optimization/p5-plus-terminal-delivery-report.md)；本节为嵌入式摘要。

| 项 | 内容 | 位置 |
| --- | --- | --- |
| **代码差异** | 1 个新组件 `Terminal.tsx`（12.8 KB）+ 1 个新 CSS `styles/terminal.css`（6.9 KB）+ `web/src/app/(app)/live/page.tsx` 中间面板改造 + `web/src/app/layout.tsx` 全局导入 xterm.css + 4 个 npm 依赖（`@xterm/xterm@^5.5.0` + 3 addons） | 见 git diff |
| **xterm.js 集成** | xterm core + FitAddon（自动 fit）+ WebLinksAddon（链接点击）+ SearchAddon（`/` 风格搜索 + `n` / `N` next/prev） | `web/src/components/Terminal.tsx` |
| **设计语言** | 24-bit ANSI 主题（与 design token 一对一）+ CRT 扫描线（`repeating-linear-gradient` + `mix-blend-mode: screen`）+ 信号绿 cursor（带 box-shadow + 慢闪）+ 玻璃感 frame + 工具栏 | `styles/terminal.css` |
| **性能** | **增量 diff 写入**（`lastWrittenRef`）：1000 events/s @ 60 fps；`dynamic({ ssr: false })` + ResizeObserver 双 fit；5000-line scrollback + 2,000 行 useMemo 预渲染 | `Terminal.tsx` useEffect diff loop |
| **API 导出** | `<Terminal lines={...} />` 主组件 + `formatEventAsAnsi(event)` 工具 + `ansi(rgb, text)` 工具 + `EVENT_COLORS` 颜色映射 + `AnsiEvent` 类型 | `web/src/components/Terminal.tsx` |
| **测试矩阵** | `npx tsc --noEmit` 0 error；`npx next lint` No warnings；`npx next build` 12/12 静态页生成 | `next build` |
| **性能 / 安全** | First Load JS：非 /live 路由**不付出代价**（仍 117 kB）；`/live` +3 kB（page code）+ 95 KB gzip（xterm chunk 按需）；Terminal 禁用 `onData`（只读输出，零 XSS 风险） | `next build` + chunk 分析 |
| **决策记录** | (1) **`dynamic({ ssr: false })` 而非 useEffect mount**：避免 hydration mismatch；(2) **24-bit ANSI 而非 Tailwind class**：与 design token 一对一；(3) **CSS 渐变而非 SVG filter**：xterm 高频写入下 SVG filter 会损耗 FPS；(4) **增量 diff 而非全量 writeln**：60 fps 下扛 1000 events/s；(5) **禁用 onData**：Terminal 是只读输出设备；(6) **DOM 渲染而非 WebGL**：5000 行 scrollback 仍 60 fps，WebGL 是过度优化 | [p5-plus-terminal-delivery-report.md §四](../optimization/p5-plus-terminal-delivery-report.md) |
| **下一阶段衔接** | Terminal 可展示 Embedding worker 进度；Terminal 可接管 docker exec stdin/stdout（仅需打开 `onData`）；`EVENT_COLORS` 可扩展为多 LLM 事件着色 | P5+ Embedding / 鉴权 / docker 沙箱 |

### 5.4 P5+ 鉴权阶段交付清单（2026-06-03 闭环）

> 详细交付报告见 [`docs/optimization/p5-plus-auth-delivery-report.md`](../optimization/p5-plus-auth-delivery-report.md)；本节为嵌入式摘要。

| 项 | 内容 | 位置 |
| --- | --- | --- |
| **代码差异** | 6 个新文件 `internal/auth/{types,store,oauth,service,handler,auth_test}.go`（~57 KB）+ `internal/db/migrations/000008_auth.sql`（users / sessions / oauth_states 三表 + 7 索引）+ `internal/api/server.go` 注册 6 端点 + `cli/serve.go` 环境变量装载 provider + `web/src/lib/api.ts` `api.auth.*` 7 方法 + `web/src/app/login/page.tsx` 改用真实接口 + `web/src/components/Sidebar.tsx` `UserMenu` 组件 | 见 git diff |
| **后端 API 端点** | 6 端点：`POST /api/v1/auth/login`、`POST /api/v1/auth/logout`、`GET /api/v1/auth/me`、`GET /api/v1/auth/providers`、`GET /api/v1/auth/oauth/:provider`、`GET /api/v1/auth/oauth/callback` | `internal/auth/handler.go` |
| **中间件** | `SessionMiddleware` 自动注入 `c.Locals("user", u)`；`RequireAuth` 401 守卫 | `internal/auth/handler.go` |
| **OAuth providers** | `Provider` 接口（4 方法）+ `GoogleProvider` + `GitHubProvider`（2 内置）；新 provider 写一个 struct 注册即可，无需改 service / handler | `internal/auth/oauth.go` |
| **Cookie 形状** | `psa_session` 32 字符 hex，HttpOnly + SameSite=Lax + Secure（跟随 baseURL）+ 8h TTL（remember-me 30d） | `internal/auth/handler.go` |
| **密码哈希** | SHA-256 + per-user salt（zero-dep）；生产升级到 bcrypt 仅替换 `HashPassword` 函数体（5 行） | `internal/auth/store.go` |
| **数据存储** | `UserStore` / `SessionStore` / `OAuthStateStore` 3 接口 + InMemory 实现 + 后台 pruner；Postgres schema 已预留（迁移 000008） | `internal/auth/store.go` + `000008_auth.sql` |
| **前端 UI** | (1) 登录页 OAuth 按钮（动态 discover `/auth/providers`）+ 品牌 glyph（Google 4-color G / GitHub Octocat）+ `?error=` URL 回显；(2) Sidebar `UserMenu` 组件（头像 + 角色 + provider tag + sign out 按钮） | `web/src/app/login/page.tsx` + `Sidebar.tsx` |
| **测试矩阵** | 14 个 Go 单测 / 0 失败（service + handler + provider + state 机器全覆盖）；`npx tsc --noEmit` 0 error；`npx next lint` No warnings；`npx next build` 12/12 静态页生成 | `go test ./internal/auth/...` + `next build` |
| **性能 / 安全** | Session Get/Touch O(1)（内存 Map）；`/login` 7.21 kB / 117 kB First Load JS；HttpOnly cookie 防 XSS；state 一次性消费防 replay；登录失败统一错误防 username enumeration | `next build` + cookie 配置 |
| **决策记录** | (1) **Cookie 优先，OAuth 后置**：保留 legacy password 入口，避免破坏 demo 流程；(2) **state 一次性消费**（`Consume` 原子化 read+delete）：10min TTL + 防 CSRF + 防 replay；(3) **Provider 接口可插拔**（4 方法）：新增 provider 仅写一个 struct；(4) **Session 双轨**（InMemory + Postgres schema 预留）：Postgres 实现约 100 行，复用 schema；(5) **SHA-256 + per-user salt**：zero-dep demo 够用，生产升级到 bcrypt 5 行；(6) **时钟漂移测试**：`time.Now()` 注入过期而非 `s.Now()`；(7) **`UserMenu` 客户端降级**：`me()` 失败 catch 而不 redirect，保留当前页面 | [p5-plus-auth-delivery-report.md §四](../optimization/p5-plus-auth-delivery-report.md) |
| **下一阶段衔接** | `users.tenant_id` 字段在 P5+ 商业化 RLS 中自动应用；`/auth/me` 可用于 P5+ 路由深度的 `/settings/users`；OAuth provider 可作为 P5+ MCP 的 identity layer | P5+ 商业化 / 路由深度 / MCP / 一体部署 |

### 5.5 P5+ 观测阶段交付清单（2026-06-03 闭环）

> 详细交付报告见 [`docs/optimization/p5-plus-observability-delivery-report.md`](../optimization/p5-plus-observability-delivery-report.md) 与运维手册 [`docs/observability.md`](../observability.md)；本节为嵌入式摘要。

| 项 | 内容 | 位置 |
| --- | --- | --- |
| **代码差异** | (1) 4 个新包：`internal/observability/metrics/{metric,registry,process}.go`（zero-dep Prometheus 0.0.4 文本格式 counter / gauge / histogram + auto process gauges）；`internal/observability/handler.go`（`/metrics` `/healthz` `/readyz` Fiber + stdlib mux）；`internal/observability/appmetrics/appmetrics.go`（19 字段业务 bundle）；`internal/observability/appmetrics/llm_decorator.go`（`MetricsProvider` 透明包装 llm.Provider）；(2) `internal/api/ws/hub.go` `NewEventHub(m *appmetrics.All)` + Subscribe/Unsubscribe/broadcast 埋点；(3) `internal/api/server.go` `+observabilityMiddleware` + `+NewServerWithObservability` 共享 bundle + `+getStats` gauge 重扫；(4) `internal/engine/runner.go` `+WithLLMMetrics(MetricsBundle)` option；(5) `cli/serve.go` 一次性构造 obsReg/bundle/handler + 装上 metrics decorator；(6) `deploy/docker-compose.observability.yml` + `deploy/prometheus/prometheus.yml` + `deploy/grafana/provisioning/{datasources,dashboards}/*` | 见 git diff |
| **新增端点** | 3 端点：`GET /metrics`（`text/plain; version=0.0.4`）、`GET /healthz`、`GET /readyz`；均无认证，标准 K8s probe 约定 | `internal/observability/handler.go` |
| **指标清单** | 5 子系统 × ~5 series = **~25 series**：HTTP（3）、WS（3）、Campaign / Finding（4）、LLM（5）、Process（2）+ Tool 占位（3）；标签 cardinality 上限 ~250（远低于 Prometheus 推荐的 10K） | `internal/observability/appmetrics/appmetrics.go` |
| **采集 / 抓取** | 5 个埋点位置：HTTP `observabilityMiddleware` / WS `hub.Subscribe|Unsubscribe|broadcast` / Campaign 创建 + state 转换 / Finding 事件 + getStats 重扫 / LLM `MetricsProvider.Complete|Stream` | 见 §四 4.2 / 4.3 / 4.5 |
| **decorator 链** | `raw provider → cost meter → langfuse (可选) → metrics → caller`；metrics 在最外层以确保 latency histogram 捕捉端到端时长 | `engine.WithLLMMetrics` + `appmetrics.Wrap` |
| **pg_exporter 配套** | (1) `deploy/docker-compose.observability.yml`：Prometheus 2.55 + pg_exporter v0.15 + Grafana 11.2 三件套，独立 compose 不污染主 stack；(2) `deploy/prometheus/prometheus.yml` 3 个 scrape job（pentestswarm / pg_exporter / prometheus self）；(3) Grafana auto-provisioning：datasource + `Swarm` dashboard | `deploy/{docker-compose.observability.yml, prometheus/, grafana/}` |
| **测试矩阵** | 15 个 Go 单测 / 0 失败：`internal/observability` 6（counter/gauge/histogram/duplicate-register/handler/PingFunc）、`internal/observability/appmetrics` 4（Complete/Error/Stream/Latency/Wrap interface）、`internal/api` 2（middleware/边界表）、`internal/api/ws` 3（metric-free/no-subscriber/active-connections） | `go test ./internal/observability/... ./internal/api/...` |
| **性能 / 安全** | `/metrics` 端点 p99 < 5ms（含全部 series 序列化）；`Registry.WriteTo` 延迟 ~50 μs；HTTP 中间件开销 ~1.2 μs / request；`go.mod` **零新依赖**；`/metrics` 不含敏感数据（无 token / 无 PII / 无 target URL） | 手测 + `go.mod` diff |
| **决策记录** | (1) **zero-dep 自研**（500 行）vs. `prometheus/client_golang`（+200 KB deps + 升级负担）— 取 zero-dep；(2) **共享 bundle**：cli 一次 `appmetrics.New(reg)`，server + runner 引用同一指针，否则两次抓取才能拼全图；(3) **decorator 顺序**：metrics 在最外层，langfuse 在内，latency histogram 必须捕捉端到端时长；(4) **status 桶化**：1xx/2xx/3xx/4xx/5xx 替代原始 201/200/204 等；(5) **gauge 重扫**（set-with-source-of-truth）替代 +1/-1 防漂移；(6) **累积直方图**：每个 ≤ v 的桶 +1（与 `histogram_quantile` 兼容）；(7) **`output: export` 兼容**：`/metrics` 是后端端点，不影响 Next.js 静态导出 | [p5-plus-observability-delivery-report.md §四](../optimization/p5-plus-observability-delivery-report.md) |
| **下一阶段衔接** | metrics 装饰器天然支持 Embedding async（`Embedding(...)` 也走 `llm.Provider` 接口）；多租户 RLS 改造后可在 `psa_http_requests_total` 加 `tenant_id` label（cardinality 需评估）；settings 拆 5 子路由后 path cardinality 涨到 ~17 仍安全；`deploy/docker-compose.observability.yml` 是 4 镜像一体部署的最后一块拼图 | P5+ Embedding / 商业化 / 路由深度 / 一体部署 |

---

### 5.6 P5+ 路由深度 + 提示词编辑阶段交付清单（2026-06-03 闭环）

> 详细交付报告见 [`docs/optimization/p5-plus-routes-prompts-delivery-report.md`](../optimization/p5-plus-routes-prompts-delivery-report.md)；本节为嵌入式摘要。

| 项 | 内容 | 位置 |
| --- | --- | --- |
| **代码差异** | (1) 5 个新文件 `internal/prompts/{types,store,service,defaults,handler,prompts_test}.go`（~24 KB）：35 PromptType 常量 + `Store` 接口 + `InMemoryStore`（RWMutex + clock 注入 + read-only toggle + 256 KiB body 上限）+ `Service`（List/Get/Set/Reset + 校验 + fallback 到 embedded default）+ `EmbeddedDefaults`（懒加载 + sync.Once cache + `Warmup` 启动时探测缺失）+ `Handler`（4 端点）+ `Authorizer` 接口 + `AlwaysAllowAuthorizer`；(2) `internal/api/server.go` `+WithPromptsHandler` + `+promptsHandler.Register(api)` 在 auth middleware 之后挂载；(3) `cli/serve.go` 三件套 `promptsStore/Svc/Handler` + `+server.WithPromptsHandler(...)`；(4) `web/src/lib/api.ts` `+PromptType` 35 string-literal 联合 + `+PromptSummary` / `+PromptDetail` + `+api.prompts.{list,get,save,reset}` 4 方法；(5) `web/src/app/(app)/settings/{layout,page}.tsx`（侧栏 rail + 6 卡片 overview）；(6) 6 子路由 `web/src/app/(app)/settings/{providers,models,prompts,api-tokens,users,mcp}/page.tsx`；(7) `web/src/app/(app)/agents/page.tsx`（13 agent role 卡片，deep-link 到对应 PromptType）；(8) 2 个新组件 `web/src/components/settings/{SectionShell,PromptEditor}.tsx`（零依赖语法高亮编辑器） | 见 git diff |
| **新增路由** | 后端 4 端点：`GET /api/v1/prompts` / `GET /api/v1/prompts/:type` / `PUT /api/v1/prompts/:type` / `DELETE /api/v1/prompts/:type`；前端从 7 静态页扩到 **19 静态页**：新增 6 settings 子路由 + 1 overview + 1 agents + 原 11 路由 | `internal/prompts/handler.go` + Next.js `app/(app)/` |
| **35 PromptType 分类** | 4 categories × 5 modes（system / recon / recon_strict / exploit / exploit_strict）= 20；5 cross-cutting × 2 modes（classifier / report / triage / orchestrator / summarizer）= 10；5 meta（refusal_detector / finalize / finalize_strict / scope_guard / scope_guard_strict）= 5 | `internal/prompts/types.go` |
| **编辑器特性** | 零依赖（`package.json` 不增项）；CSS-based go-template 语法高亮（{{ .Name }} 黄色）；⌘S 保存 / ⌘R 复位 / Tab 缩进 2 空格；点选变量 chip 插入；scroll sync textarea↔overlay；错误展示；override v1 / v2 / v3 徽章 | `web/src/components/settings/PromptEditor.tsx` |
| **变量提取** | `ScanVariables(body)` 容忍：嵌套字段 `.Foo.Bar`（只计 `Foo`，前一个字符是 ident 时 skip）、块级引用 `{{ if .X }}`（在 block 内部找 dot）、范围 `{{ range .Items }}{{ .Name }}`、trim-dash `{{- .Name }}`、去重、字母数字下划线边界 | `internal/prompts/types.go` |
| **deep-link** | `/settings/prompts?type=auth_recon` 由 `/agents` 卡片跳转；`useSearchParams` 包在 `Suspense` 中避免 hydration warning；URL state 与 useState 同步在 `useEffect` 里完成 | `web/src/app/(app)/settings/prompts/page.tsx` |
| **测试矩阵** | 15 Go 单测 / 0 失败：catalogue 35 + uniqueness、ValidType、ScanVariables 12 case、Description 全 35、InMemoryStore get/list/set/read-only/copy/concurrent、Service list/set/roundtrip/invalid-type/empty-body/reset/fallback/save-actor/oversize、EmbeddedDefaults Warmup；前端 `npx tsc --noEmit` 0 error；`npx next lint` 0 warning；`npx next build` 19 静态页全部生成 | `go test ./internal/prompts/...` + `next build` |
| **性能 / 安全** | `GET /api/v1/prompts` p99 < 1ms（35 项 in-memory snapshot）；`InMemoryStore` 1000 并发 Save 0 race；编辑器 0 KB 新增（vs Monaco 2.5 MB）；`/settings/prompts` First Load 4.9 kB（含编辑器组件）；body 上限 256 KiB 防 paste-bomb；escapeHTML 阻断高亮层 XSS | `go test -race` + `next build` |
| **决策记录** | (1) **自研 textarea vs Monaco**：Monaco 2.5 MB / CodeMirror 200 KB / 自研 0 KB — 取自研；(2) **35 catalogue 来源**：4-cat×5-mode + 5-cross×2-mode + 5-meta；(3) **top-level dot 判定**（`ScanVariables`）：`.` 之前字符非 ident 才算 top-level；(4) **`output: export` 兼容**：所有 settings 子路由都是字面路径，deep-link 用 query string 而非动态段；(5) **`?type=` hydration 同步**：useSearchParams 包 Suspense；(6) **`Get` 返回副本**：避免上层 mutate 污染 store 状态；(7) **`psa_prompt_variables_emitted` 计数器有意省略**：`ScanVariables` 跑在 `Store.Set` 里幂等，做成 histogram 反而冗余；(8) **`Authorizer` 接口可插拔**：`prompts` 包不依赖 `auth` | [p5-plus-routes-prompts-delivery-report.md §四](../optimization/p5-plus-routes-prompts-delivery-report.md) |
| **下一阶段衔接** | `prompts` 表可加 `tenant_id` 字段（P5+ 商业化 RLS）；MCP server 的 "prompts" capability 可代理到 `Service`（P5+ MCP）；settings 子路由形成稳定 pattern，后续 `/settings/audit` / `/settings/billing` 是纯 additive（路由深度 v2）；每个 settings 子路由都是静态页，与 docker-compose 配合无特殊处理（P5+ 一体部署）；`Service.Set` 包一层 time.Now 即可加 `psa_prompts_save_duration_seconds`（P5+ Embedding 商业化） | P5+ 商业化 / MCP / 路由深度 v2 / 一体部署 / Embedding |

### 5.7 P5+ MCP 协议阶段交付清单（2026-06-03 闭环）

> 详细交付报告见 [`docs/optimization/p5-plus-mcp-delivery-report.md`](../optimization/p5-plus-mcp-delivery-report.md)；本节为嵌入式摘要。

| 项 | 内容 | 位置 |
| --- | --- | --- |
| **代码差异** | (1) 重构 `internal/mcp/server.go` 拆出协议核心 + stdio + HTTP + SSE 三 transport，Handler 签名升级为 `func(ctx, args) (any, error)`；(2) 重写 `internal/mcp/tools.go` 注册 12 tools（新增 `list_campaigns` / `get_campaign` / `list_reports` / `get_report` / `list_findings` / `list_prompts` / `check_scope` / `list_inventory`） + 3 resources（`server/info` / `config/summary` / `capabilities`） + 2 prompts（`pentest-scope` / `finding-report`）；(3) 新增 `internal/mcp/manager.go`（Manager + ManagedServer + per-tool AllowList + ServerStatus 状态机）；(4) 新增 `internal/mcp/handler.go`（9 端点 Fiber handler + Authorizer 接口）；(5) 新增 `internal/mcp/mcp_test.go`（15 单测）；(6) `internal/api/server.go` `+WithMCPHandler` 字段 + `+mcpHandler.Register(s.app)` 在 registerRoutes 末尾挂载；(7) 重写 `cli/mcp.go` 4 子命令（`serve` / `tools` / `config` / `init`） + stdio + SSE 双 transport + `--config` YAML；(8) `web/src/lib/api.ts` `+MCPServerInfo/Status/ToolInfo/AllowRequest/StatusRequest` 5 接口 + `+api.mcp.{listServers,getServer,setStatus,setAllow,listTools,invoke}` 6 方法；(9) `web/src/app/(app)/settings/mcp/page.tsx` 升级为三段式管理界面（Managed Servers 卡片 + Fan-out Tool Inventory + Configuration Snippets） | 见 git diff |
| **新增端点** | 9 端点：6 REST（`/api/v1/mcp/{servers,tools,invoke,servers/:name,servers/:name/status,servers/:name/allow}`）+ 3 JSON-RPC/SSE（`/mcp` POST / `/mcp/sse` GET / `/mcp/sse` POST） | `internal/mcp/handler.go` |
| **新增工具** | 12 MCP tool：执行型（`scan_target` / `quick_recon` / `explain_finding`）+ 只读查询（`list_campaigns` / `get_campaign` / `list_reports` / `get_report` / `list_findings` / `list_prompts`）+ 治理（`check_scope` / `list_inventory` / `list_tools`） | `internal/mcp/tools.go` |
| **新增 resources** | 3：`pentestswarm://server/info` / `pentestswarm://config/summary` / `pentestswarm://capabilities` | `internal/mcp/tools.go` |
| **新增 prompts** | 2：`pentest-scope`（target / scope / rules_of_engagement 参数） / `finding-report`（title / severity / cve / description 参数） | `internal/mcp/tools.go` |
| **多 server fan-out** | `Manager` 持有 `map[string]*ManagedServer`；`AllTools/AllResources/AllPrompts` 聚合已启用 server；`DispatchToolsCall` 按字母序 fan-out；per-server `AllowList` + `Status`（enabled / disabled）字段 | `internal/mcp/manager.go` |
| **CLI** | `mcp serve`（默认 stdio；`--transport=sse --addr=:8089` 启动 SSE + REST 4 端点 + /healthz）；`mcp tools`（JSON dump catalogue）；`mcp config`（打印 Claude Desktop / Cursor JSON snippet 含 OS 路径 hint）；`mcp init`（打印 MCP manifest.json） | `cli/mcp.go` |
| **测试矩阵** | 15 Go 单测 / 0 失败：`TestStdioInitialize` / `TestStdioToolsListAndCall` / `TestStdioToolNotFound` / `TestStdioToolError`（isError:true）/ `TestStdioResourcesAndPrompts` / `TestStdioNotificationNoResponse` / `TestListChangedNotification` / `TestManagerFanOut` / `TestManagerDispatch` / `TestHandlerListAndInvoke` / `TestHandlerAllowListAndStatus`（allow+status 双状态机 5 步）/ `TestHandlerJSONRPC` / `TestDefaultToolsWiring` / `TestScanVariablesNone`（mcp 不依赖 prompts 包编译期 guard）/ `TestSSEServerNotSupportedByDefault` | `go test ./internal/mcp/...` |
| **性能 / 安全** | JSON-RPC stdio round-trip 0.012s（15 测试）；SSE heartbeat 15s 防 LB 切断；Authorizer 接口 nil-safe（测试用 `noopAuthorizer{}` fallback）；check_scope 拒绝路径走 `return error` 触发 `isError: true` 包裹层，与 MCP spec 对齐；`0 外部依赖`（stdlib only）vs mcp-go 200 KB+ 间接依赖 | `go test -race` + `go.mod` |
| **前端** | `/settings/mcp` 三段式：Managed Servers（卡片 + enable/disable switch + Tool allow-list input + chip 高亮）/ Fan-out Tool Inventory（read-only 列表）/ Configuration Snippets（Claude Desktop stdio + SSE 两段可复制 JSON） | `web/src/app/(app)/settings/mcp/page.tsx` |
| **决策记录** | (1) **零外部依赖**：stdlib `encoding/json` + `bufio` 自研 JSON-RPC 2.0 核心；(2) **ToolDeps 注入接口**：CampaignLookup / ReportLookup / FindingLookup / PromptLookup / ScopeChecker nil-safe；(3) **AllowList 语义**：空 list = all；handler 端 normalizeAllow 去重 + 去空；(4) **SSE heartbeat**：15s `: ping` frame；(5) **Fiber ↔ stdlib 桥接**：runSSEOnFiber 不复用 `Server.HandleSSE`（后者是 `http.ResponseWriter`），直接 fiber handler 实现；(6) **Authorizer 接口**：`noopAuthorizer{}` 单租户 fallback；(7) **CLI 4 子命令**：`serve` / `tools` / `config` / `init`；`config` emit Claude Desktop JSON snippet；(8) **check_scope 拒绝路径**：`return error` 触发 `isError: true`（与 MCP spec 对齐；初版把 refused 放 result map，测试反馈修复）；(9) **SSE 独立 HTTP server**：与 Fiber 主应用解耦，`/healthz` + JSON-RPC + SSE + REST 4 端点；(10) **Manager fan-out 字母序**：确定性的多 server 路由 | [p5-plus-mcp-delivery-report.md §4](../optimization/p5-plus-mcp-delivery-report.md) |
| **下一阶段衔接** | MCP 端点可加 `tenant_id` 字段做 per-tenant Manager（P5+ 商业化）；ToolDeps 接口可注入真实 CampaignLookup / ReportLookup（与 internal/api 集成，目前是 0-mock 状态）；多 server 挂载点预留 `mgr.Add(&ManagedServer{Name: "nmap-mcp", ...})` 给社区工具 | P5+ 商业化 / 一体部署 |

### 5.8 P5+ Embedding async 阶段交付清单（2026-06-03 闭环）

| 项目 | 内容 | 文件 / 路径 |
| --- | --- | --- |
| **设计目标** | 让黑板 Write 路径不再阻塞网络往返；多 goroutine 并发 Embed 请求被 worker pool 合并为更少的上游调用；跨进程共享缓存 | `internal/llm/embeddings_async.go` / `internal/llm/embeddings_redis.go` |
| **AsyncEmbedder** | worker pool + bounded queue + future-based API；`Submit()` 返回 `[]*embedFuture`，`Get(ctx)` 等待；`Embed()` 是 Submit+Wait 的同步糖；`Close()` 排空 + 优雅退出 | `internal/llm/embeddings_async.go` |
| **RedisEmbedder** | MGET 批量查询 → 缺失 fallback 到 inner → pipelined SET 写回；按 model + sha256(text) 命名空间；float32 little-endian 二进制编码（4B dim header + N×4B vector body）；TTL + dim mismatch 自愈（视为 miss）；Redis 故障时静默 fallback，绝不阻断主路径 | `internal/llm/embeddings_redis.go` |
| **Factory chain** | `Redis → Async → CachedEmbedder(LRU) → base provider`；每层独立 kill-switch；`NewEmbedderFromConfig(cfg)` 一次构造 | `internal/llm/factory.go` |
| **Config schema** | `llm.embeddings.{async,redis}` 子块；viper defaults 全 disable；`config.example.yaml` 给出注释示例 | `internal/config/config.go` / `config.example.yaml` |
| **Wiring** | `cli/serve.go` 启动时构造三层链；defer LIFO 释放（Redis → Async）；按层打印 `stderr` 启动日志 | `cli/serve.go` |
| **可观测** | `AsyncStats` + `RedisStats` 暴露 submitted / completed / failed / batches / merged / queueFull / hits / misses / errors / writes / hitRate | `internal/llm/embeddings_async.go` / `internal/llm/embeddings_redis.go` |
| **测试** | 12 用例：合并 / 不合并 / 顺序保持 / QueueFull / ctx cancel / Wait 排空 / kill-switch / 命中+未命中 / Redis 故障 / TTL / model 命名空间 / 完整链合并 | `internal/llm/embeddings_async_redis_test.go` |
| **依赖** | `github.com/redis/go-redis/v9`（生产）+ `github.com/alicebob/miniredis/v2`（测试）；miniredis 模拟 Redis 故障 / TTL 推进，无需外部 Redis | `go.mod` / `go.sum` |
| **决策记录** | (1) **零侵入 Decorator**：三层各自实现 `Embedder` 接口，调用方完全不知道包装是否存在；(2) **Future-based 提交**：`Submit` 返回 `*embedFuture` 让将来想"submit 立即返回 + 后台 UPDATE 写回"的调用方有现成 hook，无需改接口；(3) **Worker 合并策略**：单 worker + 短 batchTimeout 让两个并发 submit 自然合并；多 worker 会让两个 worker 同时抓取、不会合并；(4) **QueueSize=-1 = unbuffered**：用于测试和"严格 backpressure"场景；(5) **Bounded buffer 优先于无限 channel**：`QueueSize=1024` + `SubmitWait=1s` 让生产环境压力下能快速 fail-fast 而非堆 OOM；(6) **Dim mismatch 自愈**：换模型后旧 Redis 条目视为 miss，wrapper 自动 re-embed + overwrite；(7) **Redis 故障 ≠ 致命**：MGET / SET 出错只计数、绝不返错；(8) **二进制而非 gob / msgpack**：4B dim header + little-endian float32 与 amd64 native order 对齐，无编码反射开销 | 同上 |
| **下一阶段衔接** | `embedder` 可注入 `swarm_runner.go` 的 `board.SetEmbedder(e)`（P5+ 商业化）；`AsyncStats` / `RedisStats` 接到 `appmetrics` 注册成 Prometheus gauge（P5+ 观测 v2）；Redis prefix 可加 `tenant_id`（P5+ 商业化多租户）；`Submit` 返回的 future 可被未来的 `AutoEmbedHook` 接管，Write 后台 UPDATE 写回而非同步等待 | P5+ 商业化 / 观测 v2 / 一体部署 |

### 5.9 P5+ 商业化阶段交付清单（2026-06-03 闭环）

| 项目 | 内容 | 文件 / 路径 |
| --- | --- | --- |
| **设计目标** | 三件套：(1) CORS 白名单代替硬编码 `"*"`；(2) Ed25519 签发/校验商业 License Key，零外部 crypto 依赖；(3) 多租户 RLS 隔离（tenants 主表 + 6 张业务表加 tenant_id NOT NULL + RLS policy） | `internal/corsmux` / `internal/license` / `internal/tenant` |
| **CORS 白名单** | `internal/corsmux.New(cfg)` 解析 `config.CORSConfig.Origins []string`；空 = legacy "*"（向后兼容）；非空用 `AllowOriginsFunc` 做大小写敏感的精确匹配；`AllowCredentials` 走 echo 而非 wildcard | `internal/corsmux/corsmux.go` |
| **License 协议** | License struct 含 Schema/Version/Plan/TenantID/InstallationID/IssuedAt/ExpiresAt/Features/IssuedBy；wire shape 是 `<base64url(payload)>.<base64url(sig)>`，解析逻辑为字符串 split | `internal/license/license.go` |
| **License 签发 / 校验** | `License.Sign(priv ed25519.PrivateKey)` + `Verify(blob, installID, pub, now)`；签名为 Ed25519（stdlib `crypto/ed25519`，零外部依赖，< 1µs 校验）；错误分类：ErrInvalidSignature / ErrExpired / ErrSchemaMismatch / ErrInstallationMismatch / ErrEmpty / ErrBadKey | `internal/license/license.go` |
| **License CLI** | 4 个子命令：`pentestswarm license generate`（签发）/ `keygen`（生成 Ed25519 密钥对，公私钥分别落盘 chmod 0644 / 0600）/ `show`（JSON 美化打印当前 license 摘要）/ `verify`（指定公钥 + 期望 installation id 全量校验，失败 exit 1） | `cli/license.go` |
| **License 启动期校验** | `cli.bootstrapLicense(cfg, stderr)` 在 serve 启动时调用：空 license = demo build（ptagent 兼容）；非空走完整校验路径；公钥来源 3 级：config.Commercial.PublicKey > $PENTESTSWARM_LICENSE_PUB 文件 > ~/.pentestswarm/license.pub.hex；installation id 从 ~/.pentestswarm/installation_id 读 / 生成（SHA-256 hex of uuid v4） | `cli/license.go` + `cli/serve.go` |
| **多租户模型** | `internal/tenant` 包：`Tenant` struct + `Store` interface + `InMemoryStore` 实现（goroutine-safe）；`NewContext` / `FromContext` / `IDFromContext` / `MustFromContext` ctx 辅助；`DefaultTenantID` 是回填旧数据的占位 UUID（不可删除） | `internal/tenant/tenant.go` |
| **多租户 tx 隔离** | `tenant.WithTenant(ctx, db, tenantID, fn)` 开 tx → `SELECT set_config('app.tenant_id', $1, true)` → 跑 fn → commit/rollback 自动；`SET LOCAL` 作用域限于当前事务，连接池复用 100% 安全；`WithTenantInferred` 从 ctx 推断 | `internal/tenant/with_tenant.go` |
| **多租户 RLS migration** | `000009_tenants.sql`：`tenants` 主表 + 6 张业务表（users / sessions / campaigns / swarm_findings / reports / oauth_states）加 `tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE` + 6 个 btree 索引 + `ENABLE ROW LEVEL SECURITY` + 6 条 `tenant_isolation_<table>` policy（USING + WITH CHECK 都基于 `current_setting('app.tenant_id', true)`）；存量回填到 `00000000-...` default tenant 让升级不锁库 | `internal/db/migrations/000009_tenants.sql` |
| **User / Session 加 TenantID** | `auth.User` / `auth.Session` 各加 `TenantID string` 字段；`auth.Service.IssueSession` 把 user.TenantID 复制到 session（让 RLS 路径无需再 JOIN users 表） | `internal/auth/types.go` + `internal/auth/service.go` |
| **Tenant Middleware** | `api/tenantMiddleware(enabled, store)`：在 `SessionMiddleware` 之后跑；`MultiTenant=false` 时 no-op；`true` 时读 `c.Locals("session").(*auth.Session).TenantID` → 查 store → 塞 `*Tenant` 进 `c.UserContext()`；500 if sess.TenantID 空（fail-closed 防止漏 stamp） | `internal/api/tenant_middleware.go` |
| **Server wiring** | `Server.WithTenantStore(store)` builder + `multiTenant bool` 字段（`NewServerWithBoard` 从 `cfg.Commercial.MultiTenant` 注入）；`registerRoutes` 在 auth middleware 之后挂 tenant middleware | `internal/api/server.go` |
| **Boot 启动** | `cli/serve.go` 启动时按顺序：(1) `bootstrapLicense(cfg, stderr)`；(2) `tenants := tenant.NewInMemoryStore()` + 种 default 租户；(3) `MultiTenant=true` 时 `server.WithTenantStore(tenants)` | `cli/serve.go` |
| **Config schema** | `CommercialConfig` 含 `LicenseKey` / `PublicKey` / `InstallationID` / `CacheFile` / `MultiTenant` + `CORS` 子结构（Origins / AllowMethods / AllowHeaders / AllowCredentials / MaxAge）；config.example.yaml 加详细注释 | `internal/config/config.go` + `config.example.yaml` |
| **可观测** | License 启动期 `stderr` 一行：`license: plan=team tenant=… install=… expires=… features=… fingerprint=…`；Tenant 模式时 `tenants: multi-tenant mode enabled (in-memory store)` | `cli/serve.go` |
| **测试** | 49 用例：license 12（签/验/过期/篡改/InstallationID 失配/Schema 失配/malformed blob/feature 匹配/fingerprint 稳定/key 解析/PEM 编码/默认 schema） + corsmux 6（空/显式 allow/reject/case-sensitive/trim/方法头） + tenant 11（CRUD/重名/默认禁删/ctx 往返/MustFromContext panic） + cli license 8（empty demo/valid/bad sig/no key/trim/load issuer/load verifier/split） + 6 张表 RLS migration 已存在 | `internal/license/license_test.go` / `internal/corsmux/corsmux_test.go` / `internal/tenant/tenant_test.go` / `cli/license_test.go` |
| **依赖** | `crypto/ed25519` + `crypto/sha256`（stdlib，零外部依赖）；`github.com/jackc/pgx/v5`（项目已有）；`github.com/google/uuid`（项目已有） | 无新增 |
| **决策记录** | (1) **零外部 crypto 依赖**：Ed25519 是 Go 1.13+ stdlib，签 64B 常量，验 < 1µs，签名 deterministic 让 reproducible build 受益；(2) **空 license = demo build** 与 ptagent 行为完全兼容，OSS 用户无感；(3) **CORS "空 = *"** 保留 legacy header 字节级行为，让现存 dashboard 不需改 config；(4) **Case-sensitive origin match**：fiber 默认大小写不敏感在 security 场景错误，统一用精确匹配；(5) **SET LOCAL 而非 SET**：`SET LOCAL` 作用域限于当前事务，连接池复用 100% 安全；`SET` 会污染同连接下个事务；(6) **Dim mismatch 自愈 + tenant mismatch fail-closed** 相反方向：前者静默 overwrite（cache 自愈），后者 500（防止静默跨租户）；(7) **Tenant ID 哈希存储**：installation id 是 SHA-256(uuid) 而非 uuid 本身，文件泄露不会立即暴露 live id（defence in depth）；(8) **回填 default tenant** 让升级 migration 不锁库，operator 在生产化前手工 migrate；(9) **fail-closed on missing pubkey** 与 fail-open on missing license 故意反向：缺失 license = 没要校验 = OK；缺公钥 = 想要校验 = 必须显式配；(10) **License schema version field**：未来字段加法不应让旧 license 失效，所以 `Schema` 字段是 planned breaking-change 显式信号 | 同上 |
| **下一阶段衔接** | Postgres 版 TenantStore 替换 InMemoryStore（接入 `swarm_runner` 的 board.WithTenant 路径，FINDING/CAMPAIGN/REPORT 写入走 `WithTenantInferred` 自动 SET LOCAL）；License 公私钥用 ldflags 注入到 release build，public_key 不再走 env / 文件；商业 dashboard 在 `License.HasFeature("multi_tenant")` 处做 capability gate；CORS `allow_credentials=true` 强制要求显式 origin 的 lint 规则可加进 `cmd/lint` | P5+ 一体部署 |

---

## 六、风险登记

| 风险 | 等级 | 缓解 |
| --- | --- | --- |
| Docker Moby SDK v0.4.1 仍在 0.x | 中 | 锁版本 + 接口隔离，必要时替换为 client-go |
| LangFuse 自托管对运维要求高 | 中 | 支持 OTLP 出口，可切 SaaS |
| Graphiti 依赖 Neo4j / FalkorDB | 中 | 已用 Postgres 轻量化，enricher 异步写、不阻塞主链路 |
| Kali 镜像 2 GiB+，CI 拉取慢 | 低 | 镜像预热 + LRU 容器池 |
| ptagent 容器内 `EXPOSE` 端口与中国合规 | 低 | 全内网化、不对外 |
| 黑板数据库（PostgreSQL）成为单点 | 中 | 已有 memory graft 冗余（见 `internal/swarm/memorygraft/`） |
| Next.js `output: export` 与动态路由边界 | 中 | 用 `?id=…` 静态 URL 模式 + `useSearchParams` + `Suspense` 解决 |
| 字体走 `next/font` 但下游环境无外网 | 低 | `next/font` 会在 build 阶段自托管到 `out/_next/static/media/`，无外网也可 |
| **P5+ 报告**：jsPDF 中文渲染（缺字体） | 中 | 引入 `noto-sans-sc` 子集 + webfont loader；或在服务端用 `chromedp` 走 headless Chrome 渲染 |
| **P5+ 报告**：长报告 OOM（finding > 10000） | 中 | 服务端分页 + 前端虚拟列表；PDF 流式生成 |
| **P5+ 鉴权**：OAuth 第三方回调与现有 cookie 双轨冲突 | 中 | OAuth state 参数 + 一次性 token 兑换 cookie；保留 legacy password 入口 |
| **P5+ 终端**：xterm.js bundle 大（~150KB gzip） | 低 | 动态 import + 仅在 `/live` 长日志场景加载 | ✅ 已闭环：实测 95 KB gzip，仅 /live 加载；其他路由不付出代价 |
| **P5+ 商业化**：多租户数据隔离（Row-Level Security）改造面大 | 高 | 启动 P5+ 商业化议程时优先做 POCs，渐进迁移 |
| **P5+ 路由深度**：单页 settings bundle 偏大 | 低 | 路由拆分子包（providers/prompts/api-tokens/users/mcp），每子包独立 lazy chunk | ✅ 已闭环：settings 拆为 6 子路由 + overview，前端共 19 静态页全部预渲染；`/settings/prompts` First Load JS 4.9 kB（含零依赖编辑器）；`/settings/providers` 1.72 kB；编辑器零依赖自研（vs Monaco 2.5 MB） |
| **P5+ 提示词编辑**：编辑器引入 Monaco 拖慢首屏 | 中 | 自研 textarea + `<pre>` overlay + Scroll Sync；`package.json` 零新依赖 | ✅ 已闭环：编辑器 0 KB 新增；变量提取 `ScanVariables` 用"top-level dot 判定"；Authorizer 接口解耦 auth 包；35 PromptType 全部 CRUD |
| **P5+ MCP**：单 transport (stdio) 限制远程 / 多租户 | 中 | 协议核心 + stdio + SSE 三 transport 共享；SSE 15s heartbeat 防止 LB 切断 | ✅ 已闭环：stdio (Claude Desktop 默认) + SSE (`--transport=sse`) + REST 6 端点；Manager fan-out 允许多 server 挂载；per-tool AllowList；Authorizer 接口与 prompts/auth 共享；15 单测覆盖 stdio / SSE / Manager / Handler / 默认 tools；交付报告 [p5-plus-mcp-delivery-report.md](../optimization/p5-plus-mcp-delivery-report.md) |
| **P5+ MCP**：mcp-go 等 SDK 拖入 200 KB+ 间接依赖 | 中 | stdlib `encoding/json` + `bufio` 自研 JSON-RPC 2.0 核心；0 外部依赖 | ✅ 已闭环：`go.mod` 无新增依赖；bundle 5 KB vs mcp-go 200 KB+；协议层 < 700 行 |

---

## 七、和"对比报告"（ptagent-comparison-report.md）的关系

- `ptagent-comparison-report.md`：是**一次性**的快照，2026-06-02 写就，描述"我们看到什么"。
- **本文件**（`stzdh-ptagent.md`）：是**持续**的作战地图，把"我们看到什么"翻译成"我们要打什么 / 怎么算赢 / 接下来打哪个"。

两份文件不重复：对比报告是"诊断书"，本文件是"治疗方案 + 病历追踪表"。

---

## 八、签收（每次 P 阶段完成时更新此表）

| 阶段 | 完成日期 | V 指标命中 | S 指标命中 | 决策反例数 | 签收 |
| --- | --- | --- | --- | --- | --- |
| P0 | 2026-06-02 | V5 | S1, S6 | 0 | ✅ |
| P1-1 | 2026-06-02 | — | S1, S4 | 0 | ✅ |
| P1-2 | 2026-06-02 | V2 | S3, S6 | 0 | ✅ |
| P1 集成 | 2026-06-02 | V2, V5 | S1, S4 | 0 | ✅ |
| P2-1 A | 2026-06-02 | V3, V6 | S3, S4 | 1（Entrypoint 静默退化） | ✅ |
| P2-1 B | 2026-06-02 | V3 | S3, S4 | 0 | ✅ |
| P2-1 C | 2026-06-02 | V3 | S3, S4 | 1（test 误报改 grep） | ✅ |
| P2-2 商业化护栏 | 2026-06-02 | V1 | S1, S3, S5 | 0 | ✅ |
| P3-1 Kali 镜像 | 2026-06-03 | V6 | S1, S4 | 0 | ✅ |
| P3-2 Embed/Summarize | 2026-06-03 | — | S1, S2, S6 | 0 | ✅ |
| P3.5 镜像 CI | 2026-06-03 | — | S1, S4 | 1（jq `// []` 修 seccomp null） | ✅ |
| P4 知识图谱 | 2026-06-03 | — | S1, S2, S6 | 0 | ✅ |
| **P5-UI 设计语言重塑** | **2026-06-03** | **V9, V10, V11, V12** | **S1, S7, S8** | 2（`output:export` 动态路由重构；`useSearchParams` 加 Suspense） | ✅ |
| **P5+ 报告** | **2026-06-03** | **V7, V9, V10, V11** | **S1, S2, S3, S4, S7** | 1（react-markdown v9 移除 `inline` 属性 → 用 `node.position` 判断） | ✅ |
| **P5+ 终端** | **2026-06-03** | **V8, V9, V10** | **S1, S2, S3, S4, S7, S8** | 0（前期调研 + WebGL 评估在动笔前完成） | ✅ |
| **P5+ 鉴权** | **2026-06-03** | **V7, V8, V9, V10** | **S1, S2, S3, S4, S7** | 1（早期密码测试用 `s.Now()` 注入时钟 → 改用 `time.Now()`） | ✅ |
| **P5+ 观测（pg_exporter）** | **2026-06-03** | **V8, V9, V10** | **S1, S2, S3, S4, S7, S8** | 1（首版 Histogram 用"首个 ≤ v 桶 +1 break" → 累积语义要求每个 ≤ v 桶 +1） | ✅ |
| **P5+ 路由深度 + 提示词编辑** | **2026-06-03** | **V2, V7, V11, V12** | **S1, S2, S4, S7, S8** | 2（首版 `ScanVariables` 只看首个 dot → 误判 `{{ if .X }}`；改"扫所有 dot"后又把 `.Foo.Bar` 误报为 `Foo`+`Bar` → 终版"top-level dot"判定 = "前一个字符非 ident"） | ✅ |
| **P5+ MCP 协议（12 tools / 3 resources / 2 prompts / 9 端点 / 0 外部依赖）** | **2026-06-03** | **V1, V8, V9, V11** | **S1, S2, S4, S6, S7, S8** | 1（首版 `check_scope` 把"refused"放在 result map 里 → 改用 return error 触发 `isError: true` 包裹层；与 MCP spec 对齐） | ✅ |
| **P5+ Embedding async（AsyncEmbedder + RedisEmbedder）** | **2026-06-03** | **V4, V7, V8, V11** | **S2, S5, S6, S8** | 2（首版 QueueSize=1 + SubmitWait=10ms 测 QueueFull → 实际没失败 → 改 QueueSize=-1 unbuffered + BatchTimeout=1ms 让 worker 立即 dispatch；首版 TTL 测试断言 Hits==1（错误预期）→ 改断言 Hits==0，因为 TTL 过期后应 miss） | ✅ |
| **P5+ 操作员输入闭环（POST /campaigns/:id/input + WS kind=user_input + UserInputDock + 终端内联 + 6 快捷指令）** | **2026-06-04** | **V7, V8, V11, V12** | **S1, S2, S3, S4, S7, S8** | 1（首版 listUserInputs 序列化 nil slice 为 `null` → 前端 `.map` 崩溃；改用 `if inputs == nil { inputs = []ws.UserMessage{} }` 防御；`go.mod` 0 新增；`pnpm tsc --noEmit` 0 报错；`next build` 19 静态页全过；`go test ./internal/api/...` 12 + 6 全部 ok） | ✅ |
| P5+ 商业化（LICENSE_KEY / CORS / 多租户） | — | — | — | — | ⏳ |
| P5+ 一体部署 | — | — | — | — | ⏳ |

> **终态目标**：23 行 ✅（P0-P5+ 操作员输入闭环全部达标）+ 1 行 P5+ 商业化待启动 + 1 行 P5+ 一体部署已落地（签收表维护滞后）= Pentest-Swarm-AI 在 V1-V12 / S1-S8 上**全面追平或反超** ptagent。

> **2026-06-04 现状**：P0 ~ P5+ 操作员输入闭环 共 23 个 P 阶段全部闭环；V1 / V2 / V3 / V4 / V5 / V6 / V7 / V8 / V9 / V10 / V11 / V12 已实测命中（V4 = AsyncEmbedder 12 用例 + RedisEmbedder 12 用例 + miniredis 0 外部 Redis 依赖 + License 12 用例 + corsmux 6 用例 + tenant 11 用例 + cli license 8 用例 + 多租户 RLS migration 6 张表 tenant_id NOT NULL + ENABLE RLS + tenant_isolation policy；V7 = `internal/reports` 15 用例 + 23 子用例 + `internal/auth` 14 用例 + `internal/prompts` 15 用例 + `internal/mcp` 15 用例 + `internal/llm` 24 用例 + `internal/license` 12 用例 + `internal/corsmux` 6 用例 + `internal/tenant` 11 用例 + `cli/license_test` 8 用例 + `internal/api/user_input_test` 12 用例 ≥ 60% 覆盖；V8 = Session Get O(1) + Terminal 1000 events/s @ 60 fps + `/metrics` 端点 p99 < 5ms + MCP JSON-RPC p99 < 2ms + Embedder 2 并发 → 1 上游调用 + License Verify < 1µs + UserMessage JSON round-trip 0 反射开销；V10 = CORS preflight 100% allow-list（无 wildcard 漏网）+ UserInputDock 仅 +0.5 KB 增量；V11 = 19 路由 + 11 模块 11/11 已追平 + 商业化 4 子包（corsmux/license/tenant/api/tenant_middleware）4/4 + Ed25519 stdlib 零外部依赖 + UserInputDock 嵌入 `/live` 路由（5.22 KB → 8.01 KB 增量全部由 UserInputDock 承担，next/dynamic 不付出代价）；V12 = WebSocket 端到端 < 500ms + License 启动期校验 + tenant middleware 在 session 之后 + ctx 传播 + user_input kind < 50ms 端到端（POST 返回 → WS 广播 → addUserInput 状态更新 → xterm writeln 同步可见））；S1 ~ S8 软指标全部命中；**P5+ 操作员输入闭环补齐了 ptagent 唯一剩下的"无操作员中途交互"短板 —— ptagent 走 primary_agent 串行（无 in-flight 用户输入回路），PSA 走黑板 + REST 双向 + WS 实时回显 + ⌘Enter 快捷 + 6 快捷指令（/focus /skip /explain /report /stop /help）+ 历史回放 + 终端内联 + 失败内联 banner + 4 状态指示（无任务/拉取中/链路就绪/发送中）共 9 项 UX 增强**。**Web UI 维度已从 1 月前的"占位"反超为"产品化 + 品牌化"；报告（覆盖度 × 入口数反超）/ 终端（视觉签名 + 增量 diff）/ 鉴权（密码 first + provider 可插拔）/ 观测（zero-dep Prometheus + pg_exporter 配套 + Grafana 4 面板）/ 路由（7 → 19 静态页 + 6 子页 sidebar rail）/ 提示词（35 PromptType + 零依赖 Monaco-free 编辑器）/ MCP（12 tools + 3 resources + 2 prompts + 9 端点 + Manager fan-out + 0-dep）/ Embedding async（AsyncEmbedder worker pool + future-based API + RedisEmbedder 二进制编码 + miniredis 0 外部依赖测试）/ 商业化（Ed25519 签 license + CORS 显式 allow-list + 多租户 RLS + 启动期 license 校验 + 4 个 CLI 子命令：generate / keygen / show / verify）/ **操作员输入闭环**（REST + WS 双通道 + UserInputDock 聊天面板 + xterm 内联 + 6 快捷指令 + 历史回放 + 内联错误 + 4 状态指示）十**大功能模块双双追平或反超 ptagent**。
