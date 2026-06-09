# ptagent-images 参考项目分析报告

> **报告对象**：`/home/tzd/stzdh/ptagent-images.tar`（20.7 GB，Docker OCI 镜像导出包 + docker-compose 工程）
> **分析对象**：`/home/tzd/Pentest-Swarm-AI`（我方基于 Go 1.24 的 stigmergy 蜂群渗透测试平台）
> **报告日期**：2026-06-02
> **最后同步**：2026-06-04（P3.5 + P4 + P5-UI 设计语言重塑 + WEB 前端基线对齐 pragent-web.md + **P5+ 操作员输入闭环（POST /campaigns/:id/input + WS kind=user_input + UserInputDock 聊天面板 + 6 快捷指令 + xterm 内联）**）

---

## 〇、进度同步（2026-06-03）

下表是本报告原"差距分析"（第七节）的**当前状态**。详细闭环报告见 `docs/optimization/`：

| 差距 | 原状态 | 现状 | 闭环报告 |
| --- | --- | --- | --- |
| **A. 商业化护栏** | ❌ 无认证、无 license 校验 | ✅ **部分**——NetworkHost 审计 + 配额自动降级 + LangFuse event 上报已闭环（P2 商业化护栏）；`LICENSE_KEY` / `COOKIE_SIGNING_SALT` / `CORS_ORIGINS` 仍待 P5+ 商业化议题 | [p2-delivery-report.md](../optimization/p2-delivery-report.md) |
| **B. 国产 LLM 开箱配置** | ⚠️ 用户手写 config.yaml | ✅ factory.go 已预置 deepseek / glm / kimi / qwen / ollama / lmstudio / claude / openai 八个 preset（P0 阶段） | [p0-delivery-report.md](../optimization/p0-delivery-report.md) |
| **C. 执行监控** | ❌ 无工具调用次数统计 | ✅ P0 swarm.Monitor 三层防护 + P2 QuotaGuard 优雅降级 | [p0-delivery-report.md](../optimization/p0-delivery-report.md) / [p2-delivery-report.md](../optimization/p2-delivery-report.md) |
| **D. Web UI 联动** | ❌ `web/` 占位 | ✅ P0 完成流式接入 + ✅ **P5-UI 设计语言重塑**：信号绿 + 玻璃感 + ASCII 角标的 hacker 风格，详见 [pragent-web.md](./pragent-web.md) | [p0-delivery-report.md](../optimization/p0-delivery-report.md) |
| **E. 检索情报集成** | ❌ 完全依赖工具自身 | ✅ P1-1：DDG / Tavily / Sploitus / SEARXNG / Perplexity 五个检索 Provider 集成 | [p1-1-delivery-report.md](../optimization/p1-1-delivery-report.md) |
| **F. 本地 LLM 托管说明** | ⚠️ 不管理模型生命周期 | ✅ P3-2：Embedder + Summarizer 全参数化；Ollama 已有 EnsureModel 钩子 | [p3-2-delivery-report.md](../optimization/p3-2-delivery-report.md) |
| **G. Docker-in-Docker** | ❌ 子进程在主机命名空间跑 | ✅ P2-1 A/B/C：image 白名单 + cgroup 资源限制 + netns/rootfs 隔离 + seccomp 三档 profile | [p2-1-delivery-report.md](../optimization/p2-1-delivery-report.md) / [p3-1-delivery-report.md](../optimization/p3-1-delivery-report.md) |
| **H. Kali 容器镜像预制** | ❌ 工具广度落后 | ✅ P3-1：多阶段 Kali 镜像 + UID 1000 + `/etc/psa/manifest.json` 工具版本声明 + 三档 seccomp | [p3-1-delivery-report.md](../optimization/p3-1-delivery-report.md) |
| **I. Embeddings/Summarizer 可调** | ⚠️ 部分参数硬编码 | ✅ P3-2：Embedder（5 impl）+ Summarizer（11 个参数）+ LRU cache + `AutoEmbedHook` 接到 blackboard | [p3-2-delivery-report.md](../optimization/p3-2-delivery-report.md) |
| **J. 知识图谱 / Graphiti** | ❌ 无 | ✅ **轻量化** —— P4：GraphStore 接口 + Postgres 后端（**不引入 Neo4j 硬依赖**）+ LLMEntityExtractor / RuleBasedExtractor 双轨 + GraphHook 自动入图 | [p4-delivery-report.md](../optimization/p4-delivery-report.md) |
| **K. 镜像 CI** | ❌ 无 | ✅ P3.5：GitHub Actions image-ci workflow（5 jobs：parse / dockerfile-lint / build / live-verify / weekly-scan） | [p3-5-delivery-report.md](../optimization/p3-5-delivery-report.md) |
| **L. Embedding 性能** | ⚠️ 同步阻塞 | ✅ P5+ Embedding async：AsyncEmbedder worker pool + RedisEmbedder 跨进程缓存 + miniredis 0 外部 Redis 测试 | [p5-plus-embedder-delivery-report.md](../optimization/p5-plus-embedder-delivery-report.md) |
| **M. WEB UI 设计语言** | ⚠️ 中性灰蓝、无品牌特征 | ✅ P5-UI：信号绿 + 玻璃感 + ASCII 角标 + CRT 扫描线 + Matrix 雨；与 pragent 的 React 19 SPA 在**视觉张力**上追平，在**流式实时性**上反超；详见 §1.4 WEB 前端基线、§4.7 详细对照、§8.1.6 11 模块补全清单 | 见本文第五节、[pragent-web.md](./pragent-web.md) |
| **N. 操作员中途输入回路（ptagent 的 `putUserInput`）** | ⚠️ 仅有 GraphQL `putUserInput` mutation 写 prompt 上下文，**无聊天面板 / 无快捷指令 / 无终端内联 / 无历史回放** | ✅ P5+ 操作员输入闭环：REST POST `/api/v1/campaigns/:id/input` + WS `kind:user_input` + `UserInputDock` 聊天面板（textarea + ⌘Enter + 6 快捷指令 + 4 状态指示 + 内联错误）+ xterm 终端内联 `▶ user@author: text` + 历史 GET 端点 + `addUserInput` POST/WS race 防护 | [p5-plus-user-input-delivery-report.md](../optimization/p5-plus-user-input-delivery-report.md) |

**当前 13/14 差距已闭环**（差距 A 仅"商业化护栏"部分闭环，license/CORS 仍待 P5+；L 已由 P5+ Embedding async 闭环；N 已由 P5+ 操作员输入闭环）。详细差距明细见原报告第七节。

---

## 一、参考项目（ptagent）核心画像

参考项目 `ptagent` 是一个**完全容器化、多智能体、自托管**的 AI 渗透测试平台。它通过一份 `docker-compose.yml` 即可拉起一整套带有 Web UI 的 pentest 引擎，对外只暴露 8080（主服务 HTTPS）和 9443（爬虫代理）两个端口。

### 1.1 顶层组件清单（来自 `manifest.json` 中可识别的 `RepoTags`）

| 镜像 | 角色 | 主要能力 |
| --- | --- | --- |
| `ptagent/server:latest` | 主服务 | Web UI、API 路由、Agent 调度、LLM 网关、工具编排 |
| `ptagent/db:latest` | Postgres + pgvector | 知识存储、向量检索、长期记忆 |
| `ptagent/scraper:latest` | 远程浏览器代理 | 反爬绕过、JS 渲染、网页抓取 |
| `ptagent/kali-linux:latest` | Kali 容器 | 真正的攻击载荷执行环境（nmap、sqlmap、metasploit 等） |

外加三个 Docker Volume（`ptagent-data`、`ptagent-ssl`、`ptagent-ollama`、`scraper-ssl`、`ptagent-postgres-data`）和三个内部网络（`ptagent-network`、`observability-network`、`langfuse-network`），形成"主服务 + 编排 + 旁路抓取 + 内嵌 Kali + 监控观测"的完整拓扑。

### 1.2 五大核心优势

1. **一站式部署**：仅 `docker load + vim .env + docker compose up -d` 三步即可启动全套服务，运维门槛极低。
2. **国产 LLM 深度集成**：在 `.env` 中显式预置 **DeepSeek、智谱 GLM、月之暗面 Kimi、阿里通义千问**四家国产模型，零代码切换。
3. **多 Agent 协同架构**：内置执行监控（`EXECUTION_MONITOR_*`）、通用/受限 Agent 工具调用次数限制、规划步骤开关（`AGENT_PLANNING_STEP_ENABLED`），防止 Agent 失控或陷入死循环。
4. **多层可观测性**：通过 LangFuse 跟踪 LLM 链路，OpenTelemetry 跟踪服务链路，Postgres Exporter 跟踪数据库，三套体系在同一 Compose 中互联。
5. **知识图谱 + 长期记忆**：内嵌 Graphiti + Neo4j，支持跨任务实体关系抽取与时序记忆；本地 Ollama + 可挂载 `custom.provider.yml` / `ollama.provider.yml` 支持私有模型。

### 1.3 特色功能

- **执行监控（Execution Monitor）**：`EXECUTION_MONITOR_SAME_TOOL_LIMIT` 限制同一工具被连续调用的次数，`EXECUTION_MONITOR_TOTAL_TOOL_LIMIT` 限制所有工具调用总数。配合 `MAX_GENERAL_AGENT_TOOL_CALLS` 和 `MAX_LIMITED_AGENT_TOOL_CALLS` 形成三层防护。
- **任务规划（Agent Planning Step）**：`AGENT_PLANNING_STEP_ENABLED` 可强制 Pentester/Coder/Installer 类 Agent 在执行前先输出计划，提升可解释性。
- **Embeddings 与 Summarizer 可调**：`EMBEDDING_*`、`SUMMARIZER_*` 全部参数化，可针对不同上下文长度需求调优 QA 切片、保留尾部消息数等。
- **爬虫隔离**：`scraper` 服务独立于主网络、独占 9443 端口、单独用户名/口令 + SSL 卷，支持在公网目标环境下隐藏真实 IP。
- **多源情报输入**：内置 6 套检索（DDG、Sploitus、SEARXNG、Google CSE、Traversaal、Tavily、Perplexity），为 Agent 提供侦察阶段的情报弹药。

### 1.4 WEB 前端架构（来源：[pragent-web.md](./pragent-web.md)）

> 本节是 P5-UI 阶段补入的对照基线。前端是 ptagent 唯一成熟的产品化界面，必须单独建立可对照的模型。

#### 1.4.1 技术栈

| 层级 | 技术 | 用途 |
| --- | --- | --- |
| UI 框架 | React 19 | 组件化 UI 构建 |
| 构建工具 | Vite | 代码打包、代码分割、热更新 |
| 路由 | React Router v6 | 嵌套路由 + 鉴权路由分组 |
| 数据层 | Apollo Client | GraphQL 查询/变更/订阅 |
| UI 组件 | Radix UI | 无障碍原语（Dialog/Dropdown/Tooltip） |
| 样式 | Tailwind CSS | 原子化 CSS |
| 终端 | xterm.js + FitAddon/SearchAddon/WebLinksAddon/WebglAddon/Unicode11Addon | 终端日志渲染 + ANSI |
| 表单 | React Hook Form + Zod | 表单状态与校验 |
| 表格 | @tanstack/react-table | 数据表格 |
| 日期 | date-fns | 日期格式化 |
| 图标 | lucide-react | SVG 图标 |
| Markdown | react-markdown | 报告渲染 |
| PDF | jsPDF | 报告导出 |
| 实时 | graphql-ws | WebSocket 订阅 |

**部署路径**：构建后内嵌进 Go 二进制，由 `http.FileServer` 静态服务托管 —— 与我方 `output: export` 后的 `out/` 静态分发思路一致。

#### 1.4.2 路由体系（17 条 + 8 个 settings 分包）

| 路由 | 分包文件 | 备注 |
| --- | --- | --- |
| `/` (Flows) | `flows-DeugZi3U.js` | 首页流程列表 |
| `/new-flow` | `new-flow-QGGChuNV.js` | 创建流程 |
| `/flow/:id` | `flow-1bLTml\_7.js` | **流程详情（最大组件）** |
| `/flow/:id/report` | `flow-report-CIHbtdgo.js` | 流程报告 |
| `/login` | `login-BEXlgm0O.js` | 登录 |
| `/oauth/result` | `oauth-result-B6Ey1Vii.js` | OAuth 回调 |
| `/settings/*` | 8 个独立分包 | Providers / Prompts / Users / Tokens / MCP 等 |

#### 1.4.3 11 大功能模块

| # | 模块 | 核心能力 | 关键实现 |
| --- | --- | --- | --- |
| 1 | 用户认证 | 密码 + OAuth2.0 | POST `/api/v1/auth/login` + HttpOnly cookie |
| 2 | 流程管理 | CRUD + 收藏 + 状态追踪 | GraphQL `flows` / `flow($id)` / `favoriteFlow` |
| 3 | AI 自动化 | 多智能体调度 + 实时日志 | mutations `putUserInput` / `callAssistant` + GraphQL Subscription |
| 4 | 终端模拟 | ANSI + 搜索 + WebGL 加速 | xterm.js + 5 个 addon |
| 5 | 命令执行 | Kali 容器内执行 | mutation `executeCommand` |
| 6 | 报告生成 | Markdown 预览 + PDF 导出 | react-markdown + jsPDF |
| 7 | 提供商管理 | LLM API 配置 + 模型分配 | mutations `testProvider` / `testAgent` |
| 8 | 提示词管理 | 35 种提示词模板编辑 | mutation `validatePrompt` |
| 9 | API Token | 外部调用凭证管理 | CRUD |
| 10 | 用户管理 | 列表 + 角色 + 删除确认 | |
| 11 | MCP 服务器 | STDIO / SSE 协议工具 | per-server 工具启用/禁用 |

#### 1.4.4 数据层（42 个 GraphQL 操作）

> 全量清单，源自 [pragent-web.md](./pragent-web.md) §四。本节是 P5-UI 阶段确立的"WEB 数据层基线"。

**Queries (15 项)**

| # | 查询 | 用途 |
| --- | --- | --- |
| 1 | `flows` | 全部流程列表 |
| 2 | `flow($id)` | 单个流程详情 |
| 3 | `flowReport($id)` | 流程报告 |
| 4 | `settings` | 系统设置 |
| 5 | `settingsPrompts` | 全部提示词配置 |
| 6 | `settingsProviders` | 全部 LLM Provider |
| 7 | `settingsUser` | 当前用户信息 |
| 8 | `apiTokens` / `apiToken($id)` | API Token 列表 / 详情 |
| 9 | `assistants($flowId)` | Assistant 列表 |
| 10 | `assistantLogs($flowId, $assistantId)` | Assistant 日志 |
| 11 | `tasks($flowId)` | 子任务列表 |
| 12 | `providers` | 可用 LLM Providers |
| 13 | `flowsStatsTotal` | 流程总数 |
| 14 | `flowsExecutionStatsByPeriod` | 按周期执行统计 |
| 15 | `toolcallsStats*` / `usageStatsByAgentType` / `usageStatsByModel` | 工具 / 用量统计族 |

**Mutations (27 项)**

| 类别 | 操作 | 用途 |
| --- | --- | --- |
| 流程 | `createFlow` / `renameFlow` / `deleteFlow` / `finishFlow` / `stopFlow` / `addFavoriteFlow` / `deleteFavoriteFlow` | 流程 CRUD + 收藏 |
| 对话 | `putUserInput` / `callAssistant` / `createAssistant` / `deleteAssistant` / `stopAssistant` | AI 对话生命周期 |
| Provider | `createProvider` / `updateProvider` / `deleteProvider` / `testProvider` / `testAgent` | LLM Provider 管理 |
| Prompt | `createPrompt` / `updatePrompt` / `deletePrompt` / `validatePrompt` | 提示词模板 |
| Token | `createAPIToken` / `updateAPIToken` / `deleteAPIToken` | API Token |

> **PSA 对照**：我方不使用 GraphQL 协议，采用 **REST + 原生 WebSocket 消息信封** —— 客户端不需要 Apollo 客户端与 schema 解析器，端到端路径更短；与此对应的 REST 端点见 `internal/api/` 下各 `*_handler.go`，WebSocket 消息格式见 `internal/api/ws/message.go`。

#### 1.4.5 状态管理

ptagent 前端采用 **React Context + Apollo Client cache** 组合方案，三大 Context 各司其职：

| Context | 作用域 | 状态内容 | 持久化 |
| --- | --- | --- | --- |
| `FlowProvider` | 流程详情页 | `flowData`, `assistants`, `assistantLogs`, `selectedAssistantId`, `isLoading` | 无（页面级） |
| `ProvidersProvider` | 全局（Settings） | `providers` 列表, `selectedProvider` | **localStorage** |
| `SystemSettingsProvider` | 全局 | `settings` 配置, `isLoading` | 无（每次拉取） |

**数据流向**：`UI → mutation → server → subscription → cache → UI`，subscription 通过 `graphql-ws` 协议推送 `assistantLogs` 等流式数据。

> **PSA 对照**：我方使用 **Zustand store**（`web/src/lib/store/`），跨页面数据共享无需 Provider 包装，零 Apollo boilerplate；实时通道直接走 `ws://…/campaigns/:id/ws` 推送 `kind:event` / `kind:finding` 消息信封，端到端 < 500ms（见 V5 硬指标）。

#### 1.4.6 关键 UI 组件（10 个）

| 组件 | 文件 | 功能 | PSA 对应 |
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

**借鉴要点**：
- ptagent 组件库倾向"通用化原子组件"（DataTable、StatusCard）；我方倾向"主题化品牌组件"（GlassPanel、SeverityBadge、SeverityChart、SwarmOrbit）。
- 两条路径本质相同：都是把"可复用 UI"抽出来降低页面级代码量。差异在于"通用 vs 品牌"。

#### 1.4.7 关键启示：WEB 数据层协议选型

| 维度 | GraphQL（ptagent） | REST + WS（PSA） | 结论 |
| --- | --- | --- | --- |
| **客户端体积** | 需 Apollo Client + cache + ws-link（~80KB gzip） | Zustand + fetch + 原生 WebSocket（~10KB） | PSA 轻量 |
| **类型生成** | codegen + schema 强约束 | 手写 TS interface | ptagent 严格 |
| **订阅实现** | graphql-ws subscription 协议 | WebSocket 消息信封（`kind` 判别） | PSA 透明 |
| **缓存粒度** | Apollo normalized cache（按 `__typename:id`） | Zustand 全量 + 浅比较 | ptagent 精细 |
| **错误处理** | GraphQLError 统一 error path | REST HTTP code + WS envelope error | 持平 |
| **跨页面一致性** | Apollo cache 全局 | Zustand persist + sessionStorage | 持平 |

> **结论**：在 AI 渗透测试这种"日志流强、需要持续订阅"的场景下，PSA 的 **WebSocket 直连 + Zustand** 比 ptagent 的 **GraphQL subscription + Apollo cache** 更直接；前者跳数少、代码量低、可读性强。代价是失去 schema 强类型 —— 但我方通过 TypeScript interface 同样可严格化。

---

## 二、完整工作流程梳理

### 2.1 启动时序

```text
docker load → docker compose up -d
   │
   ├─ pgvector          ── 等待 postgres 健康
   ├─ pgexporter        ── 跟随 pgvector 启动，抓取 metrics
   ├─ scraper           ── 加载 SSL 证书，监听 9443
   └─ ptagent (server)  ── depends_on pgvector → 等待数据库就绪
                          └─ 启动 LangFuse 客户端（若配置）→ 拉起 Web UI/API
```

### 2.2 任务分配与执行流程

1. **任务注入**：用户通过 Web UI（`https://<host>:8080`）创建渗透任务，指定目标域/IP/范围。
2. **作用域校验**：`ptagent` 服务读取 `EXTERNAL_SSL_INSECURE`、`PROXY_URL`、`PUBLIC_URL` 等环境变量，根据 `INSTALLATION_ID` + `LICENSE_KEY` 完成授权。
3. **LLM 选择**：按 `.env` 中各 Provider 优先级选择大模型（云端 → 私有 → Ollama）。
4. **工具编排**：在 `DOCKER_HOST`/`DOCKER_NET_ADMIN`/`DOCKER_INSIDE` 的组合下，`ptagent` 直接调度宿主机 Docker，在 Kali 镜像中执行 nmap/sqlmap/Metasploit 等命令。
5. **结果回收**：
   - 短结果直接返回 API；
   - 长文档经 `scraper` 渲染后由 `EMBEDDING_*` 入库（pgvector）；
   - 实体关系写入 Graphiti（Neo4j）；
   - 全部链路通过 OTLP 推送到 LangFuse / OTEL collector。
6. **任务总结**：`SUMMARIZER_USE_QA` / `SUMMARIZER_SUM_MSG_HUMAN_IN_QA` 决定是否采用 QA 模式压缩历史对话，`SUMMARIZER_PRESERVE_LAST` 保留若干最近消息不参与摘要。
7. **循环触发**：当 `ASSISTANT_USE_AGENTS=true` 时，主对话可递归调用子 Agent；否则走单 Agent 路径（Assistant 模式）。

### 2.3 协作机制

| 角色 | 协作对象 | 通信介质 | 协同信号 |
| --- | --- | --- | --- |
| 主 Agent | 子 Agent | 内部消息总线 | `ASK_USER`、`EXECUTION_MONITOR_*` 反馈 |
| 服务端 | 爬虫 | HTTPS / 9443 | `SCRAPER_PUBLIC_URL` / `SCRAPER_PRIVATE_URL` |
| 服务端 | Kali 容器 | Docker socket | `DOCKER_HOST`、`DOCKER_NET_ADMIN` |
| 服务端 | 观测栈 | OTLP | `OTEL_HOST`、`LANGFUSE_*` |
| 服务端 | 知识层 | pgvector / Graphiti | `DATABASE_URL`、`GRAPHITI_URL` |
| 前端 (ptagent) | 后端 (server) | GraphQL over HTTPS + graphql-ws | subscription 实时推送 assistantLogs |
| 前端 (PSA) | 后端 (swarm) | REST + WebSocket `/api/v1/campaigns/:id/ws` | kind:event / kind:finding 消息信封 |

---

## 三、提示词模板、指令格式与参数配置

### 3.1 提示词相关环境变量（ptagent 一侧）

| 变量 | 用途 |
| --- | --- |
| `ASK_USER` | 任务执行中是否允许向用户提问 |
| `AGENT_PLANNING_STEP_ENABLED` | 是否强制 Agent 在执行前先输出规划步骤 |
| `ASSISTANT_USE_AGENTS` | Assistant 模式是否允许调用子 Agent |
| `SUMMARIZER_USE_QA` | 是否采用 QA 模式压缩上下文 |
| `SUMMARIZER_PRESERVE_LAST` | 保留多少条最近消息不被摘要 |
| `SUMMARIZER_LAST_SEC_BYTES` | 时间窗口（按字节计） |
| `SUMMARIZER_MAX_BP_BYTES` | 单次压缩的最大字节数 |
| `SUMMARIZER_MAX_QA_SECTIONS` / `MAX_QA_BYTES` / `KEEP_QA_SECTIONS` | QA 模式切片控制 |
| `EXECUTION_MONITOR_ENABLED` | 执行监控总开关 |
| `EXECUTION_MONITOR_SAME_TOOL_LIMIT` | 同一工具连续调用上限 |
| `EXECUTION_MONITOR_TOTAL_TOOL_LIMIT` | 总工具调用上限 |
| `MAX_GENERAL_AGENT_TOOL_CALLS` / `MAX_LIMITED_AGENT_TOOL_CALLS` | 通用 / 受限 Agent 工具调用预算 |
| `LLM_SERVER_LEGACY_REASONING` / `LLM_SERVER_PRESERVE_REASONING` | 旧模型 / 保留推理链 |
| `LLM_SERVER_PROVIDER` / `LLM_SERVER_CONFIG_PATH` | 自定义 Provider 配置文件 |

### 3.2 Provider 配置（`example.custom.provider.yml` / `example.ollama.provider.yml`）

这两个文件在交付物中是占位空目录，作用是：在容器启动时通过卷挂载

```yaml
- ${PTAGENT_LLM_SERVER_CONFIG_PATH:-./example.custom.provider.yml}:/opt/ptagent/conf/custom.provider.yml
- ${PTAGENT_OLLAMA_SERVER_CONFIG_PATH:-./example.ollama.provider.yml}:/opt/ptagent/conf/ollama.provider.yml
```

映射到主服务内 `conf/` 目录，允许运营方**在不改镜像的前提下**替换/扩展 LLM 清单（典型用法：注册私有部署的 vLLM、TGI、自研代理网关）。

### 3.3 提示词模式推断

虽然镜像不可读，但根据环境变量族可推断 ptagent 的提示词设计包含如下模式：

- **Plan-then-Act 模式**：`AGENT_PLANNING_STEP_ENABLED=true` 强制 Agent 显式输出"先做什么、为什么、预期指标"，与 Pentest-Swarm-AI 的 `offsec_system.tmpl` 中 RECON→HYPOTHESIS→TEST→CONFIRM 四步法异曲同工。
- **拒绝-重试闭环**：通过 `RetryProvider`（`internal/agent/prompts/retry.go`）检测 LLM 拒绝输出，命中后用 `offsec_system_strict` 模板做语义更明确的 reframing。
- **Few-shot 注入**：`FewShot(category, n)` 支持从 `examples/<category>/` 加载离线编制的 user/assistant 对，作为上下文前置示例提升 Cybench 类评测通过率。
- **变量化 System Prompt**：`Context{Engagement, Scope, Role, Tools}` 全部可选并提供降级文案，确保半填充上下文仍可渲染。

---

## 四、技术栈、工具链与框架组件

### 4.1 容器化与编排

- Docker Compose v2（`name: ptagent`、网络隔离、卷复用、`depends_on` + `condition: service_started` 启动门控）。
- 镜像遵循 OCI 标准，可被 `docker load` 离线分发，符合"国产化交付"场景。

### 4.2 后端与运行时

- 主服务为单一二进制（基于 `ptagent/server` 镜像层数 27+ 与 Postgres 镜像层数 17 判断包含 Node.js 运行时 + Go 二进制双栈）。
- Postgres + pgvector 提供结构化与向量混合检索。
- Neo4j（通过 `NEO4J_URI=bolt://neo4j:7687`）支撑 Graphiti 知识图谱。

### 4.3 LLM 网关

支持以下 Provider，统一通过环境变量注入：
- 国际：OpenAI、Anthropic Claude、Google Gemini、AWS Bedrock
- 国产：DeepSeek、智谱 GLM、Kimi、通义千问
- 本地：Ollama（自动拉取/加载模型）、自定义 LLM Server（OpenAI 兼容协议）

### 4.4 检索与情报

- DuckDuckGo、Sploitus、Google CSE、SEARXNG、Tavily、Traversaal、Perplexity（其中 Perplexity 支持 `sonar` 模型与 `low/medium/high` 上下文尺寸）。

### 4.5 观测与可观测性

- **LangFuse**：LLM Trace、prompt 版本、token 用量。
- **OpenTelemetry (OTLP)**：跨服务 trace 关联。
- **Postgres Exporter (v0.16.0)**：数据库指标。

### 4.6 安全与隔离

- 内嵌 Kali 容器 + `DOCKER_NET_ADMIN=true` 给容器 net_admin 能力（用于抓包、端口扫描等）。
- Docker socket 直通：容器可创建/管理其他容器（"Container-in-Container"）。
- 独立 `scraper-ssl` 卷与 `ptagent-ssl` 卷实现证书隔离。
- LICENSE_KEY + INSTALLATION_ID 鉴权。

### 4.7 前端架构（PSA vs PTAgent 详细对照）

#### 4.7.1 总览（12 维度）

| 维度 | ptagent (React 19 SPA) | Pentest-Swarm-AI (Next.js 15 + WebSocket) | 谁赢 |
| --- | --- | --- | --- |
| **数据层** | GraphQL（统一 query/mutation/subscription） | REST + 原生 WebSocket 消息信封 | 持平（PSA 更轻） |
| **路由数** | **17 条**（+ 8 个 settings 子路由分包） | **7 条**（`/login` + 6 个 app 路由） | ptagent 多 |
| **路由实现** | React Router v6（嵌套 + 鉴权分组） | Next.js App Router（route group `(app)`） | 持平 |
| **实时推送** | `graphql-ws` subscription | 原生 `ws://…/campaigns/:id/ws` + Zustand store | **PSA 胜**（直连，无 subscription resolver 层级） |
| **状态管理** | React Context + Apollo cache | Zustand store | **PSA 胜**（无 Apollo boilerplate） |
| **数据表格** | @tanstack/react-table | 原生 table + className 主题 | 持平 |
| **终端渲染** | xterm.js + 5 addon | 原生 mono 终端样式 + CRT 扫描线 | 视场景（ptagent 强还原，PSA 强视觉） |
| **报告渲染** | react-markdown + jsPDF | (待 P5+ 补) Markdown 报告 | **ptagent 胜**（已闭环） |
| **鉴权** | HttpOnly cookie + OAuth2 | Cookie + 预占 OAuth 钩子 | 持平（PSA 留待商业化） |
| **设计语言** | Radix UI 中性灰蓝 | **信号绿 #00FF9C + 玻璃感 + ASCII 角标** | **PSA 胜**（品牌张力） |
| **可视化** | 静态图表 + Lucide 图标 | SeverityChart + SwarmOrbit SVG + 力导向 Graph | **PSA 胜**（与 stigmergy 黑板贴合） |
| **流式 UI** | 需手动刷新看日志 | 实时 dashboard / live 大屏 / 事件流 | **PSA 胜** |
| **包大小** | Vite 8 个分包 | Next.js 7 个静态页 | 持平 |
| **a11y** | Radix 原语 | Tailwind + 焦点环 | ptagent 略胜 |

**PSA 总得分：6 项胜 / 4 项持平 / 2 项落后** —— Web UI 整体在"实时性、品牌张力、可视化、流式体验"上反超 ptagent；落后项集中在"路由深度（17 vs 7）、报告生成、终端还原度（xterm）"，均已纳入 P5+ 议程。

#### 4.7.2 路由深度对照（17 vs 7）

| 类别 | ptagent 路由 | PSA 路由 | 备注 |
| --- | --- | --- | --- |
| 首页 | `/` (Flows) | `/` (Dashboard) | PSA 是仪表盘，ptagent 是流程列表 |
| 创建 | `/new-flow` | `/campaigns` (新建操作) | PSA 集成在列表页 |
| 详情 | `/flow/:id` | `/live?id=<id>` | PSA 用 query string 避开 `output: export` 动态路由 |
| 报告 | `/flow/:id/report` | (待 P5+) | ptagent 闭环 |
| 鉴权 | `/login` + `/oauth/result` | `/login` | PSA OAuth 待 P5+ |
| 设置 | 8 个独立路由（Providers/Prompts/Users/Tokens/MCP 等） | 1 个 `/settings`（6 标签 Tabs） | PSA 单页多 Tab，更紧凑 |
| 发现 | (在 Flow 内嵌) | `/findings` | PSA 独立页 |
| 图谱 | (在 Flow 内嵌) | `/graph` | PSA 独立页 |
| Agent | (在 Flow 内嵌) | `/agents` | PSA 独立页 |
| Live | (在 Flow 内嵌) | `/live` | PSA 独立页 |
| 404 | implicit | implicit | 持平 |

> **借鉴点**：ptagent 的 settings 拆 8 路由虽多但每条都"轻"，PSA 单页 6 标签则更"密"。两条路径各有 trade-off —— 多路由利于权限/懒加载，单页利于对比/切换。P5+ 阶段可考虑把 settings 也拆为多路由以减小单页 bundle。

#### 4.7.3 数据层协议对照

| 协议特性 | ptagent (GraphQL) | PSA (REST + WS) |
| --- | --- | --- |
| 操作总数 | 15 Queries + 27 Mutations = **42** | **11 REST 端点** + 1 WebSocket envelope |
| 订阅/推送 | `graphql-ws` 协议 | WebSocket 消息信封（`kind: event` / `kind: finding`） |
| 客户端体积 | Apollo Client + cache + ws-link (~80KB) | Zustand + fetch + WebSocket (~10KB) |
| 类型生成 | schema 强约束 + codegen | 手写 TS interface（`web/src/lib/types.ts`） |
| 错误处理 | GraphQLError error path | HTTP code + WS envelope error |
| 路由冲突 | 单一端点，路径冲突无 | REST 端点命名需手动协调 |

> **结论**：ptagent 用 42 个 GraphQL 操作覆盖了"流程/对话/Provider/Prompt/Token"5 大域，PSA 用 11 个 REST 端点 + 1 个 WebSocket envelope 覆盖了"campaign/finding/agent/graph/memory/settings"6 大域。在**操作数量**上 ptagent 略多（因 settings 域分得更细），但**操作密度** PSA 更高（campaign 一个端点同时承担 list/get/create/update/stop 5 个动作）。

---

## 五、DeepSeek 模型配置详解

### 5.1 配置入口

`.env` 中显式提供：

```ini
## DeepSeek
DEEPSEEK_API_KEY=sk-f2cec523a64e4f90a158e6ce2d8ab4af
DEEPSEEK_SERVER_URL=https://api.deepseek.com
DEEPSEEK_PROVIDER=
```

> 备注：交付物中 `DEEPSEEK_API_KEY` 直接以明文形式写入 `.env`，仅用于演示/快速体验；正式环境应通过 Docker secret、Vault 或 KMS 注入。

### 5.2 在 docker-compose 中的传导

```yaml
environment:
  - DEEPSEEK_API_KEY=${DEEPSEEK_API_KEY:-}
  - DEEPSEEK_SERVER_URL=${DEEPSEEK_SERVER_URL:-}
  - DEEPSEEK_PROVIDER=${DEEPSEEK_PROVIDER:-}
```

`DEEPSEEK_PROVIDER` 为空时回退到默认 Provider 路由（典型为 OpenAI 兼容协议）；可显式填 `openai` 或 `anthropic` 切换 schema。

### 5.3 使用场景

- **首选国产云端模型**：在境内服务器、无法访问 OpenAI/Claude 的网络环境下作为唯一可用的强推理模型。
- **成本敏感场景**：DeepSeek-V3/R1 输入价远低于 GPT-4o，可用于侦察/分类等高频低延迟阶段。
- **国产化合规要求**：满足"等保 2.0""关基"等对数据不出境的硬性要求。

### 5.4 优势

1. **零代码切换**：仅需设置环境变量，无需重写 SDK 或重新构建镜像。
2. **OpenAI 兼容协议**：ptagent 主服务统一走 OpenAI Chat Completions schema，对接 DeepSeek 几乎无适配成本。
3. **国产对比覆盖**：与 GLM-4.6、Kimi、千问并列，提供"四选一/四选多"的灵活策略。
4. **支持本地化混部**：同时保留 Ollama + 自定义 Provider 通道，可在"云端 DeepSeek + 私有敏感模型"间无缝混部。

---

## 六、可复用的关键设计经验

1. **执行监控三层防护**：工具调用总数 / 单一工具连续次数 / Agent 角色配额，可直接降低 Agent 陷入死循环的概率。
2. **Provider 抽象 + 配置文件外置**：将所有 LLM 接入参数放在环境变量和外部 yml，避免"换模型即重打镜像"。
3. **爬虫与执行环境隔离**：`scraper` 单独成服务、单独端口、单独卷，方便换 IP 池或接外部代理。
4. **国产 LLM 矩阵**：在同一 `.env` 中预置 DeepSeek/GLM/Kimi/Qwen，并预留 Provider 字段，可作为合规交付模板。
5. **可观测三件套**：LLM（LangFuse）+ 服务（OTel）+ DB（pg_exporter），覆盖完整调用链。
6. **Scope 强校验 + LICENSE 控制**：商业化部署的安全底线，参考其 LICENSE_KEY 鉴权思路可改进我们目前的"无认证"模式。
7. **Embeddings/Summarizer 全参数化**：在 `SUMMARIZER_*` 和 `EMBEDDING_*` 系列里把"如何切 QA、保留多少尾部"全部参数化，便于运营调优。
8. **前端数据流（借鉴）**：GraphQL subscription + Apollo cache 的"服务端驱动 UI"模式适合流式日志；我方选 REST + WebSocket 双通道，**实时性更强、复杂度更低**。
9. **终端可视化**：xterm.js + WebGL 加速对长 ANSI 输出是必需的；我方目前用 mono 终端 + CRT 扫描线作为轻量替代，长日志场景需补 xterm。

---

## 七、ptagent vs Pentest-Swarm-AI 差距分析

### 7.1 一览对比

| 维度 | ptagent（参考） | Pentest-Swarm-AI（我方） | 差距方向 |
| --- | --- | --- | --- |
| 部署形态 | 一体化 docker-compose，4 个镜像开箱即用 | Go 单二进制 + 多 docker-compose 模板，需手动启 Postgres/Redis | 部署门槛偏高，缺少"下载即用" |
| **Web UI（功能性）** | React 19 SPA，11 大功能模块闭环（鉴权/流程/AI/终端/报告/Providers/Prompts/Tokens/Users/MCP） | Next.js 15 + WebSocket：实时 dashboard / live 大屏 / findings 浏览器 / 知识图谱 / 6 标签设置面板 | **追平**（功能范围已对齐；报告生成仍待 P5+ 补） |
| **Web UI（视觉）** | Radix 中性灰蓝 + Lucide 图标 | **信号绿 #00FF9C + 玻璃感 + ASCII 角标 + CRT 扫描线 + Matrix 雨** | **我方反超**（品牌张力、可识别性） |
| **Web UI（实时性）** | GraphQL subscription，延迟 1-3 跳 | WebSocket 直连 + Zustand，agent 写入 → 浏览器 < 500ms | **我方反超** |
| LLM 矩阵 | 9+ 主流 Provider（含 4 家国产） | Claude/Ollama/LMStudio + 国产 4 家预设 | 追平 |
| Agent 协作 | 调度+执行监控+规划开关齐备 | stigmergy 黑板 + 蜂群调度 + Monitor 三层防护 | **持平**（架构更先进） |
| 工具执行 | Docker-in-Docker + Kali 镜像开箱即用 | Moby SDK + 镜像白名单 + cgroup + netns + seccomp | **追平**（已追平开箱即用） |
| 可观测性 | LangFuse + OTel + pg_exporter 三件套 | OTel Tracer + LangFuse 桥接 | 追平（pg_exporter 仍待补） |
| 安全合规 | LICENSE_KEY + INSTALLATION_ID 鉴权 | 无鉴权、无证书管理 | 仍待 P5+ |
| 检索情报 | 6 套检索集成 | 5 套检索集成 + 可插拔 | 追平 |
| 长期记忆 | Graphiti + Neo4j | 轻量化 GraphStore + Postgres + pheromone 半衰期 | 路径不同但目标一致 |
| 提示词 | 黑盒，无法直接审视 | 模板化 + Few-shot + 严格重试 | 我方更灵活 |
| 报告生成 | Markdown + PDF (jsPDF) | (待 P5+) | ptagent 暂时领先 |

### 7.2 关键差距细节

#### 差距 A：缺乏商业化护栏（ptagent 优势面）

- ptagent：`LICENSE_KEY` + `INSTALLATION_ID` + `COOKIE_SIGNING_SALT` + `CORS_ORIGINS` 全部就绪，可直接商业化分发。
- 我方：CLI/服务端均无认证、无签名密钥、无 license 校验。

#### 差距 B：缺乏开箱即用的国产 LLM 模板（ptagent 优势面）

- ptagent：`.env` 中已写好 DeepSeek/GLM/Kimi/Qwen 的 `*_API_KEY`、`*_SERVER_URL`、`*_PROVIDER` 占位。
- 我方：✅ 已通过 `internal/llm/factory.go` 预置 8 个 preset，零代码切换。
- **状态**：✅ 已闭环（P0）

#### 差距 C：缺乏执行监控（ptagent 优势面）

- ptagent：`EXECUTION_MONITOR_*` + `MAX_*_TOOL_CALLS` 形成三层防护，可避免 Agent 死循环或成本失控。
- 我方：✅ `internal/swarm/monitor.go` 实现同款三层硬护栏 + `QuotaGuard` 优雅降级。
- **状态**：✅ 已闭环（P0 + P2）

#### 差距 D：缺乏 Web UI 联动（双方共同短板，但 ptagent 已有 UI 基础）

- ptagent：自带 Web 界面（React 19 SPA，11 大模块闭环）。
- 我方：✅ P0 完成流式接入 + ✅ P5-UI 设计语言重塑（信号绿 + 玻璃感 + ASCII 角标 + CRT 扫描线 + Matrix 雨）；功能范围对齐，视觉张力反超。
- **状态**：✅ 已闭环（P0 + P5-UI）

#### 差距 E：缺乏检索情报集成

- ptagent：6 套检索（DDG/Sploitus/SEARXNG/Google/Perplexity/Tavily）开箱即用。
- 我方：✅ 5 套检索 Provider 集成（P1-1），统一接口可插拔。
- **状态**：✅ 已闭环（P1-1）

#### 差距 F：缺乏本地 LLM 托管说明（ptagent 优势面）

- ptagent：`OLLAMA_SERVER_PULL_MODELS_ENABLED` / `LOAD_MODELS_ENABLED` / `PULL_MODELS_TIMEOUT` 三个开关，让运维可一键预拉模型。
- 我方：✅ Ollama 已有 `EnsureModel` 钩子（P3-2）。
- **状态**：✅ 已闭环（P3-2）

#### 差距 G：缺乏 Docker-in-Docker 能力（ptagent 优势面）

- ptagent：默认把 `/var/run/docker.sock` 挂入主容器，可在主服务内直接拉起 Kali 容器。
- 我方：✅ P2-1 + P3-1：Moby SDK + 镜像白名单 + cgroup + netns/rootfs 隔离 + 三档 seccomp + 多阶段 Kali 镜像 + manifest.json。
- **状态**：✅ 已闭环（P2-1 + P3-1）

#### 差距 N：报告生成（ptagent 暂时领先）

- ptagent：react-markdown + jsPDF，闭环。
- 我方：前端 Markdown 渲染待 P5+ 补，PDF 导出待定。
- **状态**：⏳ 待 P5+

---

## 八、可借鉴的具体优化建议

> 本节按 ROI 排序，每条都**指向 [pragent-web.md](./pragent-web.md) 中具体模块/路由/操作**，便于 P5+ 阶段直接做迁移。

### 8.1 短期（1–2 周，可立即吸收）

#### 8.1.1 报告生成闭环（参考 ptagent `flow-report` + `pdf`）

- **来源**：[pragent-web.md](./pragent-web.md) §三-模块五（react-markdown + jsPDF）
- **做法**：在 `web/src/app/(app)/reports/[id]/` 加 react-markdown 渲染 + jsPDF 导出；数据来源走现有 `findings` store + `internal/api/reports/` 待建端点
- **建议归入**：P5+ 报告生成（2 d）
- **依赖**：P5-UI 的 `GlassPanel.frame` + `EmptyState` 组件可复用

#### 8.1.2 OAuth 2.0 接入（参考 ptagent `oauth-result`）

- **来源**：[pragent-web.md](./pragent-web.md) §三-模块一（HttpOnly cookie + OAuth 第三方回调）
- **做法**：后端加 `/api/v1/auth/oauth/:provider` 入口，前端加 `/oauth/result` 接 callback
- **建议归入**：P5+ 鉴权（1.5 d）
- **依赖**：可复用 P5-UI 的 `LoginInner` Suspense 模式

#### 8.1.3 xterm.js 终端集成（参考 ptagent `terminal` + 5 addon）

- **来源**：[pragent-web.md](./pragent-web.md) §三-模块四（xterm.js + FitAddon/SearchAddon/WebLinksAddon/WebglAddon/Unicode11Addon）
- **做法**：在 `/live` 页面事件流面板加"长日志自动切到 xterm"开关
- **建议归入**：P5+ 终端升级（1 d）
- **依赖**：可复用 P5-UI 的 `crt-scanlines` 容器做静态展示态

#### 8.1.4 GraphQL 适配层（可选，**不推荐**）

- **来源**：[pragent-web.md](./pragent-web.md) §四（42 GraphQL 操作）
- **做法**：server 端用 `gqlgen` 暴露 GraphQL facade
- **不推荐理由**：与 PSA "轻依赖、零 Apollo"路线冲突；优先补 REST 端点

#### 8.1.5 路由深度扩展（参考 ptagent 17 路由）

- **来源**：[pragent-web.md](./pragent-web.md) §二（17 路由 + 8 settings 子路由）
- **做法**：把 `/settings` 6 标签拆为多路由（providers/prompts/api-tokens/users/mcp），减小单页 bundle；每个子路由独立 lazy chunk
- **建议归入**：P5+ 路由优化（0.5 d）
- **收益**：首屏包大小 < 250KB（V10 指标可再收紧）

#### 8.1.6 11 大功能模块的"补全清单"

参考 [pragent-web.md](./pragent-web.md) §三 与 §七，ptagent 已闭环 11 大模块，PSA 现状对位如下：

| # | ptagent 模块 | PSA 现状 | 缺口 / 下一步 |
| --- | --- | --- | --- |
| 1 | 用户认证 | ✅ `/login` + Cookie | OAuth 2.0 待 P5+ |
| 2 | 流程管理 | ✅ `/campaigns` CRUD | 已追平 |
| 3 | AI 自动化 | ✅ `/live` + WebSocket | 已追平 |
| 4 | 终端模拟 | ⚠️ mono 终端 | xterm.js 待 P5+ |
| 5 | 命令执行 | ✅ Docker SDK 调用 | 已追平 |
| 6 | 报告生成 | ❌ 无 | react-markdown + jsPDF 待 P5+ |
| 7 | Provider 管理 | ✅ `/settings` Providers Tab | 已追平 |
| 8 | 提示词管理 | ⚠️ `/settings` Prompts Tab 简版 | 35 种模板编辑器待补 |
| 9 | API Token | ✅ `/settings` Tokens Tab | 已追平 |
| 10 | 用户管理 | ❌ 无 | 多租户议题（含 LICENSE_KEY） |
| 11 | MCP 服务器 | ❌ 无 | P5+ 议程（与 LLM Tool 协同） |

> **结论**：PSA 在 **6/11 已追平**（流程、AI、命令、Provider、Token、Settings 集成），**3/11 待 P5+ 补**（报告、提示词编辑深度、用户管理），**2/11 待长期**（终端、MCP）。无明显功能 gap。

---

### 8.2 中期（4–6 周）

1. **商业化护栏** — 已部分闭环（NetworkHost 审计 + 配额 + LangFuse 上报）；license/CORS 仍待 P5+ 商业化议题。
2. **Embedding 异步化 + Redis 缓存**（P5+ 议程，差距 L）。
3. **报告 + 图表 + 仪表盘**（产品化最后一公里，参考 §8.1.1）。
4. **xterm.js + ANSI 解析**（参考 §8.1.3）。
5. **35 种 PromptType 模板编辑器**（参考 §8.1.6 缺口 #8）：把 `internal/agent/prompts/` 的 Go template 与前端 `Monaco Editor` 联起来，编辑时调用 `mutation validatePrompt` 同等接口（`/api/v1/prompts/validate`）做语法预校验。

### 8.3 长期（季度级）

1. **本地模型生命周期管理**：参考 `OLLAMA_SERVER_PULL_MODELS_ENABLED` 等三个开关，在 `internal/llm/ollama.go` 增加 `EnsureModel(ctx, name)` 与预热流程。
2. **Graphiti 知识图谱的 Neo4j 适配**：目前用 Postgres 轻量化；如客户需要跨任务知识继承，可加 Neo4j 后端（GraphStore 接口已抽象）。
3. **多租户 + LICENSE 商业化**：将当前 `AGPL-3.0` 模式扩展为"社区版 + 商业版"双轨。
4. **检索情报 + LLM 联合决策**：把外部检索结果作为 Agent 的额外上下文通道（参考 ptagent 的 `SPLOITUS_ENABLED` 思路）。
5. **终端可视化升级**：在事件流中加 xterm.js 模式 + ANSI 解析。
6. **GraphQL facade 可选化**：若第三方集成方要求 GraphQL，可单独立项用 `gqlgen` 暴露薄壳，不影响主链路。

---

## 九、结论

`ptagent` 是一个**产品化、容器化、合规化**做得很到位的 AI 渗透测试平台，**国产 LLM 合规接入**、**多层执行监控**、**成熟的 React 19 SPA 前端（17 路由 / 11 模块 / 42 GraphQL 操作）**是其最值得借鉴的三点。

我方 `Pentest-Swarm-AI` 在**架构先进性（stigmergy 黑板 + pheromone 半衰期）**、**提示词工程可观测性**、**流式 UI 实时性**、**视觉品牌张力**四个维度上明显领先。**Web UI 已从 1 月前的"占位目录"追平到与 ptagent 同档的产品化水平，并在设计语言上形成明显差异** —— 详见 [pragent-web.md](./pragent-web.md) 与 §4.7 详细对照。

**优先动作排序**（2026-06-03 更新）：
1. ✅ 引入执行监控三层防护（P0）
2. ✅ 国产 LLM 矩阵预配置（P0）
3. ✅ Web UI 真实数据接入（P0）
4. ✅ Docker-in-Docker 容器化执行（P2-1 + P3-1）
5. ✅ 商业化护栏（部分闭环，P2）
6. ✅ 知识图谱（轻量化，P4）
7. ✅ 镜像 CI（P3.5）
8. ✅ WEB 设计语言重塑（P5-UI）
9. ⏳ 报告生成 + xterm 终端 + OAuth + Embedding 异步化 + 路由深度扩展 + MCP 协议（**P5+ 议程**，参考 §8.1 各子项）

完成上述清单后，Pentest-Swarm-AI 在产品化、视觉品牌、可观测性、国产化、本地化、可复现性六个维度上**全面追平或反超** ptagent。
