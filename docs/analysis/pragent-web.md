# Pentest-Swarm-AI Web 前端完整分析报告

## 一、技术架构

Pentest-Swarm-AI 的 WEB 前端是一个基于 Next.js 的现代化单页应用 (SPA)，构建后通过 Go 的 `//go:embed` 内嵌到二进制中，由 Go 服务统一提供 API 和静态文件服务。

### 核心技术栈

| 层级 | 技术 | 用途 |
|------|------|------|
| 框架 | Next.js 15 (App Router) | 服务端渲染、文件路由、静态导出 |
| UI 库 | React 19 | 组件化 UI 构建 |
| 状态管理 | Zustand | 全局状态管理（Campaign / Findings / Agent 状态） |
| 样式方案 | Tailwind CSS 4 | 原子化 CSS + 暗色主题 |
| 终端模拟 | xterm.js | WebSocket 实时日志渲染 |
| 图表 | Recharts | 统计图表可视化 |
| 图标 | Lucide React | SVG 图标库 |
| 构建输出 | Static Export (`next build && next export`) | 生成 `web/out/` 静态文件 |

### 部署路径

```
web/out/                    ← Next.js 静态导出产物
  ├── index.html            ← 控制台首页
  ├── login.html            ← 登录页
  ├── campaigns.html        ← 任务列表
  ├── live.html             ← 实时监控
  ├── graph.html            ← 攻击图可视化
  ├── findings.html         ← 发现列表
  ├── reports.html          ← 报告列表
  ├── agents.html           ← 智能体管理
  ├── settings/             ← 系统设置（用户/模型/提示词/MCP/API Token）
  ├── 404.html              ← 自定义 404
  └── _next/static/         ← 静态资源 (JS/CSS/字体)
```

内嵌方式：Go 服务通过 `internal/webfs/` 包的 `//go:embed all:out` 指令将 `web/out/` 目录打包进二进制，启动时提供 Fiber 中间件统一服务。

## 二、路由体系 (15 条路由)

Next.js App Router 文件路由，通过静态导出生成对应的 HTML 文件：

| 路由 | 页面 | 功能 |
|------|------|------|
| `/` | 控制台首页 | 任务列表、新建任务、严重度分布 |
| `/login` | 登录页 | 密码登录 + OAuth 提供商登录 |
| `/campaigns` | 任务管理 | 创建/编辑/查看渗透测试任务 |
| `/live` | 实时监控 | WebSocket 实时事件流 + 发现推送 |
| `/graph` | 攻击图 | 攻击路径可视化关系图 |
| `/findings` | 发现列表 | 漏洞发现表格展示与筛选 |
| `/reports` | 报告列表 | 渗透测试报告列表 |
| `/reports/detail` | 报告详情 | 单个报告详细查看 |
| `/agents` | 智能体管理 | Agent 列表与状态 |
| `/settings` | 系统设置 | 设置入口导航 |
| `/settings/users` | 用户管理 | 用户列表、角色管理 |
| `/settings/models` | 模型配置 | LLM 模型分配 |
| `/settings/prompts` | 提示词管理 | 35 种提示词模板编辑 |
| `/settings/providers` | 提供商管理 | LLM API 配置 |
| `/settings/mcp` | MCP 服务器 | Model Context Protocol 服务器管理 |
| `/settings/api-tokens` | API Token | 外部调用凭证管理 |

## 三、功能模块详解

### 模块一：用户认证

- **路由**：`/login`
- **功能**：用户名/密码登录、OAuth 2.0 第三方登录
- **API 端点**：
  - `POST /api/v1/auth/login` — 密码登录
  - `POST /api/v1/auth/logout` — 登出
  - `GET /api/v1/auth/me` — 获取当前用户
  - `GET /api/v1/auth/providers` — 获取 OAuth 提供商列表
  - `GET /api/v1/auth/oauth/:provider` — 发起 OAuth 流程
  - `GET /api/v1/auth/oauth/callback` — OAuth 回调
- **认证方式**：HTTP-only Cookie（`psa_session`），Path=/，SameSite=Lax

### 模块二：任务管理（Campaigns）

- **路由**：`/campaigns`、`/`
- **功能**：创建、查看、管理渗透测试任务
- **API 端点**：
  - `GET /api/v1/campaigns` — 任务列表
  - `POST /api/v1/campaigns` — 创建任务
  - `DELETE /api/v1/campaigns/:id` — 删除任务

### 模块三：实时监控（Live）

- **路由**：`/live`
- **功能**：WebSocket 实时推送渗透测试事件和漏洞发现
- **技术实现**：
  - WebSocket 连接 `/ws` 获取实时事件流
  - 事件类型：Agent 日志、工具调用、发现推送
  - Agent 状态实时展示（在线/离线/忙碌）
  - 用户输入 Dock 组件，支持向运行中的任务发送指令
- **关键组件**：`UserInputDock`、`AgentStatus`

### 模块四：攻击图可视化（Graph）

- **路由**：`/graph`
- **功能**：展示渗透测试攻击路径和实体关系图
- **技术实现**：基于 HTML5 Canvas / SVG 的交互式关系图

### 模块五：发现管理（Findings）

- **路由**：`/findings`
- **功能**：漏洞发现列表，支持按严重度筛选和表格展示
- **API 端点**：
  - `GET /api/v1/campaigns/:id/findings` — 获取发现列表

### 模块六：报告管理（Reports）

- **路由**：`/reports`、`/reports/detail`
- **功能**：渗透测试报告列表查看和详情展示
- **API 端点**：
  - `GET /api/v1/campaigns/:id/report` — 获取报告

### 模块七：智能体管理（Agents）

- **路由**：`/agents`
- **功能**：查看和管理 AI 渗透测试智能体（Agent）
- **功能子模块**：
  - Agent 列表展示
  - Agent 状态监控（在线/离线/执行中）
  - Agent 类型配置

### 模块八：LLM 提供商管理

- **路由**：`/settings/providers`
- **功能**：
  - 配置 LLM 提供商（API Base URL + Key）
  - 为每个 Agent 类型配置对应的模型
  - 测试连接
- **支持的提供商类型**：openai、deepseek、qwen、local 等

### 模块九：提示词管理

- **路由**：`/settings/prompts`
- **功能**：
  - 查看/编辑/创建系统提示词模板
  - 支持 35 种 PromptType
  - Go template 语法验证
- **API 端点**：
  - `GET /api/v1/prompts` — 获取提示词列表
  - `POST /api/v1/prompts` — 创建提示词
  - `PUT /api/v1/prompts/:id` — 更新提示词
  - `DELETE /api/v1/prompts/:id` — 删除提示词
  - `POST /api/v1/prompts/validate` — 验证模板

### 模块十：API Token 管理

- **路由**：`/settings/api-tokens`
- **功能**：创建/删除/更新 API Token，用于外部 API 调用

### 模块十一：用户管理

- **路由**：`/settings/users`
- **功能**：用户列表表格展示（名称、邮箱、角色、状态、创建时间）、用户删除

### 模块十二：MCP 服务器管理

- **路由**：`/settings/mcp`
- **功能**：
  - 管理 MCP（Model Context Protocol）服务器
  - 支持 STDIO 和 SSE 两种传输协议
  - 配置服务器命令、参数、环境变量/请求头
  - 管理每个 MCP 服务器的工具启用/禁用

### 模块十三：模型配置

- **路由**：`/settings/models`
- **功能**：配置各 Agent 类型使用的 LLM 模型

## 四、数据层

### API 通信方式

- **REST API**：`/api/v1/*` 端点，JSON 请求/响应
- **WebSocket**：`/ws` 端点，实时事件推送
- **认证方式**：HTTP-only Cookie 自动附带

### 全部 API 端点

**认证（Auth）**：
| 端点 | 方法 | 用途 |
|------|------|------|
| `/api/v1/auth/login` | POST | 密码登录 |
| `/api/v1/auth/logout` | POST | 登出 |
| `/api/v1/auth/me` | GET | 获取当前用户 |
| `/api/v1/auth/providers` | GET | OAuth 提供商列表 |
| `/api/v1/auth/oauth/:provider` | GET | 发起 OAuth |
| `/api/v1/auth/oauth/callback` | GET | OAuth 回调 |

**任务（Campaigns）**：
| 端点 | 方法 | 用途 |
|------|------|------|
| `/api/v1/campaigns` | GET | 任务列表 |
| `/api/v1/campaigns` | POST | 创建任务 |
| `/api/v1/campaigns/:id` | DELETE | 删除任务 |

**发现（Findings）**：
| 端点 | 方法 | 用途 |
|------|------|------|
| `/api/v1/campaigns/:id/findings` | GET | 发现列表 |

**报告（Reports）**：
| 端点 | 方法 | 用途 |
|------|------|------|
| `/api/v1/campaigns/:id/report` | GET | 获取报告 |

**提示词（Prompts）**：
| 端点 | 方法 | 用途 |
|------|------|------|
| `/api/v1/prompts` | GET | 提示词列表 |
| `/api/v1/prompts` | POST | 创建提示词 |
| `/api/v1/prompts/:id` | PUT | 更新提示词 |
| `/api/v1/prompts/:id` | DELETE | 删除提示词 |
| `/api/v1/prompts/validate` | POST | 验证模板 |

**实时通信**：
| 端点 | 协议 | 用途 |
|------|------|------|
| `/ws` | WebSocket | 实时事件/日志/发现推送 |

## 五、状态管理架构

Pentest-Swarm-AI 前端采用 Zustand 进行全局状态管理：

### Store 结构

| Slice | 状态内容 | 用途 |
|-------|---------|------|
| campaigns | `Campaign[]` | 当前任务列表 |
| events | `WSMessage[]` | WebSocket 实时事件流 |
| findings | `Finding[]` | 漏洞发现列表 |
| userInputs | `UserInput[]` | 用户输入历史 |
| severityCounts | `Record<string, number>` | 严重度分布统计 |
| agentStatuses | `Record<string, AgentStatus>` | Agent 在线状态 |
| selectedPanel | string | 当前选中面板 |
| isLoading | boolean | 加载状态 |

### 数据流向

```
WebSocket (/ws) → Zustand Store → React 组件（Live/Findings/Graph）
REST API → Zustand Store → React 组件（Campaigns/Settings）
用户操作 → React 组件 → REST API / WebSocket → 后端
```

## 六、关键 UI 组件

| 组件 | 功能 |
|------|------|
| Sidebar | 侧边栏导航（Campaigns/Live/Graph/Findings/Reports/Agents/Settings） |
| UserInputDock | 用户输入面板，向运行中的任务发送指令 |
| AgentStatus | Agent 状态指示器 |
| Terminal (xterm.js) | 实时终端日志渲染 |
| CampaignCard | 任务卡片展示 |
| FindingTable | 发现数据表格 |
| SeverityChart | 严重度分布饼图 |
| Breadcrumb | 面包屑导航 |

## 七、功能模块总结

Pentest-Swarm-AI WEB 端实现了 **13 大核心功能模块**，覆盖了从认证、任务管理、AI 自动化执行、实时监控、攻击图可视化、报告生成到系统运维管理的完整功能链路：

| 序号 | 模块 | 核心能力 |
|------|------|---------|
| 1 | 用户认证 | 密码登录 + OAuth 2.0 |
| 2 | 任务管理 | 创建/查看/删除渗透测试任务 |
| 3 | 实时监控 | WebSocket 实时事件流 + 发现推送 |
| 4 | 攻击图可视化 | 攻击路径关系图 |
| 5 | 发现管理 | 漏洞发现表格展示与筛选 |
| 6 | 报告管理 | 报告列表与详情查看 |
| 7 | 智能体管理 | Agent 列表与状态监控 |
| 8 | 提供商管理 | LLM API 配置 + 模型分配 |
| 9 | 提示词管理 | 35 种提示词模板编辑 |
| 10 | 模型配置 | Agent 类型 → LLM 模型映射 |
| 11 | API Token | 外部调用凭证管理 |
| 12 | 用户管理 | 用户列表 + 角色管理 |
| 13 | MCP 服务器 | STDIO/SSE 协议工具管理 |