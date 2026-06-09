# PentestSwarm AI — Web 控制台 PRD

> **项目代号**：`PSA-Console`
> **产品名**：PentestSwarm AI Console
> **目标用户**：渗透测试工程师 / 安全研究员 / 红队 / 合规审计
> **版本**：v2.0 重构
> **日期**：2026-06-03
> **关联文档**：[technical.md](./technical.md)（技术架构）

---

## 一、产品定位

PentestSwarm AI Console 是 Pentest-Swarm-AI 蜂群渗透测试平台的**统一 Web 控制台**，承担 4 个核心职责：

1. **任务编排**（Orchestration）：创建、追踪、停止渗透测试 campaign
2. **实时监控**（Live Operations）：通过 WebSocket 实时回放蜂群调度过程、agent 推理、工具执行
3. **知识沉淀**（Knowledge）：浏览 finding、关系图谱、报告
4. **平台治理**（Governance）：LLM provider、prompt 模板、API token、用户/MCP 服务器管理

参考项目 PTAgent 的 11 大模块（详见 `pragent-web.md`）将被分阶段实施。本 PRD 覆盖 **第一阶段：登录 + 主仪表盘 + 任务编排 + 实时监控**。

---

## 二、设计语言

### 2.1 核心定位

**"B 端玻璃感 × 黑客控制台"** —— 借鉴 `pragent-web.md` 第 1-3 节中星云/玻璃质感的设计灵感，但用**渗透测试/黑客控制台**的元素重新诠释：

| 维度 | 视觉参考（PTAgent 风格） | PSA-Console 风格（我们的方向） |
| --- | --- | --- |
| 背景 | 星空 + 玻璃质感 | **终端矩阵雨** + 暗色玻璃面板 |
| 主题色 | 蓝紫渐变 | **青绿/电光蓝（cyan + electric-blue）** + 严重度警戒色 |
| 字体 | 系统字体 + 标题字 | **JetBrains Mono + Space Grotesk**（黑客控制台气质） |
| 图标 | Lucide | Lucide + 少量自定义 ASCII art / 等宽字符 |
| 元素 | 3D 渲染机器人 | **实时终端日志** + ASCII 拓扑图 |
| 质感 | 毛玻璃 + 3D 投影 | **CRT 扫描线 + 等宽 ASCII 边框** |

### 2.2 色彩系统

```
background:       #0A0A0F  /* 主背景：纯黑偏蓝 */
surface:          #12121A  /* 卡片背景：深石板 */
surface-hover:    #1E1E30  /* 卡片 hover */
border:           #1A1A2E  /* 边框：深紫 */
border-active:    #60A5FA  /* 聚焦边框：电光蓝 */

accent:           #00FF9C  /* 主强调色：终端绿（hacker 信号） */
accent-dim:       #00B872  /* 主强调色 dim */
accent-2:         #60A5FA  /* 次强调色：电光蓝 */
accent-3:         #A78BFA  /* 三级：紫光（仅 hero） */

text-primary:     #E2E8F0  /* 主文字 */
text-secondary:   #94A3B8  /* 次文字 */
text-muted:       #64748B  /* 三级文字 */
text-code:        #00FF9C  /* 代码 / 终端文字 */

severity:
  critical:       #FF3366  /* 严重 */
  high:           #FF6B35  /* 高 */
  medium:         #FFC107  /* 中 */
  low:            #00FF9C  /* 低 */
  info:           #6B7280  /* 信息 */
```

### 2.3 字体系统

- **Display / 标题**：`Space Grotesk`（700/600）—— 现代科技感，区别于 PTAgent 的 Inter
- **Body**：`Inter`（400/500）—— 可读性优先
- **Mono / 终端**：`JetBrains Mono`（400/700）—— 终端、代码、finding ID、IP、哈希
- **装饰**：`VT323`（仅 ASCII 装饰位）—— 8-bit CRT 风格

### 2.4 图标系统

- 主体：`lucide-react`（与 pragent 一致）
- 装饰元素：纯 ASCII / Unicode（`▣ ▤ ▥ ▦ ▧ ▨ ▩ ▪ ▫` 矩阵框，`⟨⟩ ⟨/⟩ ⟨⟩` 角标）
- 严禁：emoji 作为 UI 元素（破坏科技感）

### 2.5 动效原则

- **首次加载**：分层交错动画（hero → form → footer，stagger 80ms）
- **状态切换**：150ms cubic-bezier(0.16, 1, 0.3, 1)
- **背景**：`requestAnimationFrame` 驱动 CRT 扫描线（极低频，subtle）
- **不滥用**滚动动效；动效服务于"系统在工作"的暗示

---

## 三、信息架构

### 3.1 路由（v2.0 第一阶段）

```
/login                       登录页（玻璃面板 + AI 机器人 + 星云背景）
/                            重定向到 /campaigns（已登录）
/campaigns                   任务列表（默认首页）
/campaigns/new               创建任务
/campaigns/:id               任务详情（含 Live / Findings / Report 三个 tab）
/live/:campaignId            实时监控大屏
/findings                    全局 finding 浏览器（跨 campaign）
/findings/:id                单个 finding 详情（含知识图谱邻接）
/graph                       知识图谱大屏（基于 react-force-graph-2d）
/agents                      Agent 编排状态
/settings                    设置总览
  ├─ /settings/providers     LLM provider 管理
  ├─ /settings/prompts       Prompt 模板
  ├─ /settings/api-tokens    API Token
  ├─ /settings/users         用户与角色
  └─ /settings/mcp           MCP 服务器
```

### 3.2 左侧导航（v2.0 之后阶段）

```
[PSA Logo]                            [online ●] [user ▾]
─────────────────────────────────────────────────────────
  ⌂  Campaigns
  ◐  Live
  ☰  Findings
  ⬢  Graph
  ⌬  Agents
  ⚙  Settings
─────────────────────────────────────────────────────────
  (footer: build hash + uptime)
```

---

## 四、第一阶段功能详述

### 4.1 登录页 `/login`

**核心需求**：
- 用户名 + 密码登录（HttpOnly Cookie）
- 错误信息内联显示
- 登录成功 → 跳转到 `/campaigns` 或 `?redirect=`

**视觉重点**（融合用户提供的视觉参考）：
- 背景：**深空 + 矩阵雨（crt-scanline + 极慢速 matrix rain canvas，subtle）**
- 左侧（占 60%）：**玻璃质感面板**，内含 3D / ASCII 渲染的"蜂群"（替代 PTAgent 的 AI 机器人，呼应"swarm"主题）—— 中心节点 + 6 个 agent 子节点 + 连线（SVG 动画）
- 右侧（占 40%）：**玻璃表单卡** —— 浮于星空之上
  - 顶部：`PSA-Console v2.0` 标题（Space Grotesk 700，electric blue）
  - 中部：用户名 / 密码输入框（mono font 字体，暗背景 + cyan focus ring）
  - 底部：登录按钮（filled cyan）+ 错误消息（critical color）
  - 装饰：右上角角标 `⟨ SECURE_LOGIN_v2.0 ⟩`（ASCII 边框）
- 表单下方：`Build #abc1234 · uptime 47d 12h`（mono，muted text）

**可访问性**：
- 键盘可达：Tab 顺序、Enter 提交
- 错误信息：`aria-live="polite"`
- 颜色对比度 ≥ 4.5:1（文本）/ 3:1（大型文本）
- 减弱动效：`prefers-reduced-motion` 媒体查询尊重

### 4.2 主仪表盘 `/campaigns`

**核心需求**：
- 列出所有 campaign（卡片网格 / 列表切换）
- 卡片显示：名称、状态（favicon 风格彩色 dot）、目标、agent 数、finding 数、最后活跃时间
- 顶部工具栏：搜索 + 状态过滤 + 排序 + "新建"按钮
- 空状态：ASCII art + "NO_CAMPAIGNS_FOUND" 提示

**视觉重点**：
- 卡片：暗玻璃 + hover 时 cyan 边框 + 顶部 1px 状态条
- 严重度：finding 数旁带 mini-severity-bars
- 状态点：脉冲动画（pulse ring）

### 4.3 实时监控大屏 `/live/:campaignId`

**核心需求**（PTAgent 模块三 + 模块四 + 模块六的融合）：
- 4 个面板：
  - 左：Agent 状态树（stigmergy 黑板可视化）
  - 中：终端日志（ANSI 渲染，xterm.js 或简化版）
  - 右：实时 finding 流（WebSocket 推送）
  - 顶：campaign 进度条 + token / 时间消耗

**视觉重点**：
- 4 个面板用 1px 暗色边框 + 标题栏（mono font + 状态点）
- 终端面板：纯黑底 + green text + 闪烁光标
- finding 流：右侧滑入，severity 色条
- 顶栏：`/command` 风格 ASCII 进度条 `[███████░░░] 73%`

### 4.4 全局 Finding 浏览器 `/findings`

**核心需求**：
- 跨 campaign 搜索
- 过滤：severity / type / campaign / target / 时间
- 排序：severity / 时间 / pheromone 强度
- 列表 + 详情抽屉
- 详情抽屉：完整 finding data + 邻接实体（来自 P4 知识图谱）

**视觉重点**：
- 列表：等宽字体行（finding ID + severity bar + target + 时间）
- 详情抽屉：从右滑入，宽度 50%
- 邻接实体：紧凑的 entity-chip 列表

### 4.5 知识图谱大屏 `/graph`

**核心需求**：
- 全屏 `react-force-graph-2d` 渲染
- 节点：实体（按类型着色：Host/CVE/Service/Tool...）
- 边：关系（带方向箭头 + 关系类型 label）
- 工具栏：campaign 过滤 / 类型过滤 / 搜索 / 缩放 / 重置
- 节点点击 → 右侧详情面板滑入

**视觉重点**：
- 暗背景，节点 8px 圆 + 类型色填充
- 边 1px 暗灰 + 选中时 cyan
- 详情面板：与 findings 抽屉共用样式

---

## 五、与 PTAgent 的对位（v2.0 第一阶段）

| PTAgent 模块 | 我们的实现 | 状态 |
| --- | --- | --- |
| 模块一：用户认证 | `/login` | ✅ 第一阶段 |
| 模块二：任务流程管理 | `/campaigns` | ✅ 第一阶段 |
| 模块三：AI 自动化执行 | `/live/:id` | ✅ 第一阶段 |
| 模块四：终端模拟器 | `/live/:id` 内嵌 | ✅ 第一阶段 |
| 模块五：报告生成 | `/campaigns/:id` Report tab | ⏳ 第二阶段 |
| 模块六：终端命令执行 | `/live/:id` 内嵌 | ✅ 第一阶段 |
| 模块七：LLM provider 管理 | `/settings/providers` | ⏳ 第二阶段 |
| 模块八：Prompt 管理 | `/settings/prompts` | ⏳ 第二阶段 |
| 模块九：API Token | `/settings/api-tokens` | ⏳ 第二阶段 |
| 模块十：用户管理 | `/settings/users` | ⏳ 第二阶段 |
| 模块十一：MCP 服务器 | `/settings/mcp` | ⏳ 第三阶段 |

---

## 六、非功能性需求

| 项 | 要求 |
| --- | --- |
| **性能** | 首屏 LCP < 2s（4G），路由切换 < 200ms |
| **可访问性** | WCAG 2.1 AA（键盘可达、对比度、aria-live） |
| **响应式** | 桌面优先（1280-1920），最小支持 1024×768 |
| **暗色** | 唯一主题（产品定位） |
| **国际化** | 第一阶段仅 zh-CN；预留 i18n 钩子 |
| **浏览器** | Evergreen Chrome / Edge / Firefox / Safari |
| **SEO** | 登录后应用，无需 SSR 优化（API 鉴权） |

---

## 七、风险与边界

### 7.1 风险

- **API 现状**：`/api/v1/auth/login` 等后端接口可能未全部就位 → 登录页需要 mock 兜底
- **WebSocket 协议**：`/api/ws` 推送格式需要确认 → 与后端约定 envelope 格式
- **图数据规模**：超过 5k 节点时 `react-force-graph-2d` 性能下降 → 引入 LOD（细节层次）

### 7.2 边界（不做）

- **不做** 多租户切换 UI（单租户假设）
- **不做** 移动端响应式（桌面优先，< 1024 不支持）
- **不做** 浅色主题（产品定位 = 暗）
- **不做** OAuth 第三方登录（第一阶段，README 留 TODO）
- **不做** 国际化（预留 hooks）

---

## 八、交付物清单

### 第一阶段（本 PRD 范围）
- `/login` 登录页（含视觉参考的玻璃感 + 黑客蜂群元素）
- `/campaigns` 任务列表
- `/live/:id` 实时监控大屏（4 面板）
- `/findings` 全局 finding 浏览器
- `/graph` 知识图谱大屏
- 共享组件：`<Sidebar />` `<TopBar />` `<SeverityBadge />` `<TerminalLog />` `<FindingRow />` `<EmptyState />` `<AsciiDecor />`
- 设计 token：扩展 `tailwind.config.ts` 颜色 + 字体 + 动画
- 玻璃面板基础类 `glass-panel`

### 第二阶段（不含）
- `/settings/*` 全部子页
- 报告生成（PDF 导出）
- 暗色 ↔ 浅色主题切换

### 第三阶段（不含）
- MCP 服务器管理
- 多租户

---

## 九、验收标准

1. **登录页**：能完成 mock 登录（含失败路径），键鼠可访问，`prefers-reduced-motion` 尊重
2. **任务列表**：能在 mock 数据下渲染、空状态、loading 状态
3. **实时大屏**：4 面板布局正确，终端日志能渲染 ANSI / 闪烁光标，finding 流能滚动
4. **Finding 浏览器**：能按 severity 过滤、详情抽屉滑入滑出
5. **图谱**：mock 数据下能渲染节点 / 边 / 缩放
6. **设计语言**：所有页面用同一玻璃 / 暗背景 / cyan accent / mono font
7. **0 回归**：`pnpm build` 通过
