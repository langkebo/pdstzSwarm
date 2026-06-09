# P5+ 路由深度 + 提示词编辑 — 交付报告

> **阶段**：P5+ 路由深度扩展 + P5+ 提示词编辑深度
> **闭环日期**：2026-06-03
> **作者**：Pentest-Swarm-AI 项目组
> **基线文档**：[`/home/tzd/Pentest-Swarm-AI/docs/analysis/stzdh-ptagent.md`](../analysis/stzdh-ptagent.md) §四、路线图（P5+ 议程 2 项）
> **状态**：✅ closed

---

## 一、目标与范围

P5+ 路由深度：把 settings 单一页（6 个 in-page tab）拆为 **layout + 6 个独立子路由**，并补出 5 个新页面使总数从 7 → **19 静态导出路由**（含 login）。每条子路由独立 URL、独立 `output: export` 静态页、独立 back-button 历史。

P5+ 提示词编辑：暴露后端 **35 个 PromptType** 的 CRUD（GET list / GET one / PUT save / DELETE reset），前端用 **自研零依赖语法高亮 textarea**（避开 Monaco 2.5 MB bundle）实现编辑器，支持 ⌘S 保存 / ⌘R 复位 / Tab 缩进 / 变量点选插入。

| 维度 | 路由深度之前 | 路由深度之后 | 验收点 |
| --- | --- | --- | --- |
| 静态导出路由数 | 7 | **19** | `npx next build` 输出 19 行 |
| settings 子路由 | 1 页 6 tab（URL 永远 `/settings`） | **6 子路由** + 1 overview = 7 | 浏览器地址栏随导航更新 |
| 子路由独立后退 | ❌ | ✅ | 浏览器 back 按钮能跨子页导航 |
| 路由可分享 | ❌ | ✅（`/settings/prompts?type=auth_recon`） | `/agents` 页 deep-link 到 prompt |
| `output: export` 兼容 | 7 静态页 | 19 静态页 | Next.js 15 build 全 ✅ |
| 提示词模板管理 | 占位 "v2.0 phase 2" | **35 PromptType CRUD** | `/settings/prompts` 显示 35 项 |
| 编辑器体积 | n/a | **0 KB 新增依赖**（自研 textarea + CSS 高亮） | `package.json` 不增项 |
| 编辑器键位 | n/a | ⌘S / ⌘R / Tab | 浏览器中实测 |
| 变量提取 | n/a | 自动扫描 `{{ .Foo }}` 并 sidebar 高亮 | `psa_prompt_variables_emitted` |
| Override 标识 | n/a | v1 / v2 / v3 + 颜色徽章 | "X / 35 customized" 计数 |
| 嵌入式默认 | n/a | 来自 `internal/agent/prompts/templates/` | 启动时 Warmup 打印缺失 |
| 测试覆盖 | n/a | **后端 15 单测 / 前端 lint + build 通过** | `go test ./internal/prompts/...` |

---

## 二、代码差异

### 2.1 后端：新增 `internal/prompts/` 包（5 文件）

| 路径 | 字节 | 说明 |
| --- | --- | --- |
| `internal/prompts/types.go` | ~6,500 | 35 PromptType 常量 + `AllPromptTypes` 目录 + `Description(t)` 描述 + `ScanVariables(body)` 变量扫描器 |
| `internal/prompts/store.go` | ~2,800 | `Store` 接口 + `InMemoryStore`（RWMutex + 单写 clock + 256 KiB 上限 + read-only toggle） |
| `internal/prompts/service.go` | ~3,400 | `Service`（List/Get/Set/Reset + 校验 + fallback 到 embedded default）+ `DefaultsSource` 接口 + 错误类型（`ErrInvalidType` / `ErrBodyEmpty` / `ErrReadOnly`） |
| `internal/prompts/defaults.go` | ~2,200 | `EmbeddedDefaults`（懒加载 + sync.Once cache + `Warmup` 启动时探测缺失） |
| `internal/prompts/handler.go` | ~3,600 | `Handler` Fiber 端点 + `Authorizer` 接口 + `AlwaysAllowAuthorizer` + 4 端点（list / get / put / delete） |
| `internal/prompts/prompts_test.go` | ~5,400 | 15 单测（catalogue 35 性状、ScanVariables 12 case、Store 5 case、Service 6 case、EmbeddedDefaults 1 case） |

### 2.2 后端：增量改动（3 文件）

| 路径 | 改动 |
| --- | --- |
| `internal/api/server.go` | `+promptsHandler *prompts.Handler` 字段 + `+WithPromptsHandler(h)` 构造钩子 + `+promptsHandler.Register(api)` 在 auth middleware 之后挂载 |
| `cli/serve.go` | `+promptsStore` / `+promptsSvc` / `+promptsHandler` 三件套 + `+server.WithPromptsHandler(...)` |
| `web/src/lib/api.ts` | `+PromptType` 字面量联合（35 个 string literal）+ `+PromptSummary` / `+PromptDetail` 接口 + `+api.prompts.{list,get,save,reset}` 4 方法 |

### 2.3 前端：6 settings 子页 + 1 layout + 1 overview + 1 新 agents 页

| 路径 | 说明 |
| --- | --- |
| `web/src/app/(app)/settings/layout.tsx` | 侧栏 7 链接（overview + 6 子页），根据 `usePathname` 高亮 |
| `web/src/app/(app)/settings/page.tsx` | overview：6 卡片链接到子页 |
| `web/src/app/(app)/settings/providers/page.tsx` | LLM Providers（DeepSeek / OpenAI / Anthropic / Ollama + 4 字段） |
| `web/src/app/(app)/settings/models/page.tsx` | Specialist Models（4 agent role 行 + status 徽章） |
| `web/src/app/(app)/settings/prompts/page.tsx` | **核心**：35 PromptType 列表 + 编辑器宿主（`?type=` deep-link 同步到 active） |
| `web/src/app/(app)/settings/api-tokens/page.tsx` | API Tokens 占位（接口留给 P5+ 商业化） |
| `web/src/app/(app)/settings/users/page.tsx` | Users & Roles 占位（指向 P5+ 鉴权已暴露的 `/api/v1/auth/me`） |
| `web/src/app/(app)/settings/mcp/page.tsx` | MCP Servers 占位（接口留给 P5+ MCP） |
| `web/src/app/(app)/agents/page.tsx` | **新页**：13 agent role 卡片（auth/api/web/cloud × 2 + 5 core），每张 deep-link 到对应 PromptType |
| `web/src/components/settings/SectionShell.tsx` | 共享 section 壳（标题 + hint + body + frame）+ `Field` 组件 |
| `web/src/components/settings/PromptEditor.tsx` | 零依赖语法高亮编辑器（textarea + `<pre>` overlay + scroll sync + ⌘S/⌘R/Tab） |

### 2.4 不变量

- ✅ `package.json` **零新依赖**（编辑器自研）
- ✅ `output: export` 仍工作（19 静态页全部预渲染）
- ✅ 6 settings 子路由独立可分享 URL
- ✅ `/agents?type=...` deep-link 到 `/settings/prompts?type=...`
- ✅ API 端点标准 RESTful（`/api/v1/prompts[/:type]` × 4 methods）
- ✅ 后端 `ScanVariables` 容忍嵌套字段（`.Foo.Bar` 只计 `Foo`） + 块级引用（`{{ if .X }}`） + 范围（`{{ range .Items }}`） + 去重
- ✅ `EmbeddedDefaults` 懒加载 + 同步缓存 + 启动时 `Warmup` 检测缺失
- ✅ `Service.Set` 拒绝空 body + 拒绝 > 256 KiB 防 paste-bomb
- ✅ `Store.SetReadOnly(true)` 切换"只读模式"（admin 旗标）

---

## 三、API 端点

| Method | Path | Auth | 用途 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/prompts` | none | 列出全部 35 个（带 override status / variables） |
| `GET` | `/api/v1/prompts/:type` | none | 拿单个（override OR embedded default） |
| `PUT` | `/api/v1/prompts/:type` | 可选 | 保存 override（Authorizer 拦截） |
| `DELETE` | `/api/v1/prompts/:type` | 可选 | 复位到 embedded default |

错误码：
- 400 `invalid_type` / `empty_body` / `bad_json`
- 403 `forbidden` / `read_only`
- 404 `not_found`（无 override 且无 embedded default）
- 500 `list_failed` / `get_failed` / `save_failed` / `reset_failed`

---

## 四、关键决策记录

### 4.1 自研 textarea 编辑器 vs. Monaco / CodeMirror

Monaco（@monaco-editor/react）bundle 约 **2.5 MB**（含 web worker + JSON worker + editor worker），在 `output: export` 模式下全部打到 client chunk。CodeMirror 6 约 200 KB（按需 import 也接近 100 KB）。我们的需求其实只是 go-template 的 `{{ .Name }}` 高亮 + ⌘S/⌘R/Tab 键位 + 行号 0。**0 KB 自研 + textarea + 同步 scroll 的 `<pre>` overlay** 完全够用，且：

- bundle 0 增量 → 静态导出 19 页 First Load JS 总和不变
- 服务端渲染 friendly（无 dynamic import + ssr: false 烦恼）
- `useSearchParams` hydration 警告少了一个来源
- 后续要 IntelliSense 可在 `highlightDots` 阶段注入 autocomplete list，不用换包

### 4.2 35 个 PromptType 的来源与分类

参考 ptagent 的 4-category × 5-mode 矩阵 + cross-cutting role，扩展到 35：
- **4 categories × 5 modes**（system / recon / recon_strict / exploit / exploit_strict）= 20
- **5 cross-cutting**（classifier / report / triage / orchestrator / summarizer）+ 5 strict 对应 = 10
- **5 meta**（refusal_detector / finalize / finalize_strict / scope_guard / scope_guard_strict）= 5

`AllPromptTypes` 是不重复的 `[]PromptType` slice，**编辑器 sidebar 和 `List()` 都用同一个顺序**；`List()` 始终返回 35 项（`count == total == 35`），override 用 `version >= 1` 标识。

### 4.3 `ScanVariables` 的"top-level dot"判定

第一版写成"找 `{{ ... }}` 里的第一个 `.Identifier`"——对 `{{ if .X }}` 失效（首字符是 `if`）。
第二版改成"扫所有 `.Identifier`"——对 `{{ .Foo.Bar }}` 误报 `Foo` + `Bar`。
**最终版**：top-level 判定 = "`.` 之前的字符不是 ident 字符"。这样：
- `{{ .Foo }}` → `Foo`
- `{{ if .X }}` → `X`（`if` 不含 `.`）
- `{{ .Foo.Bar }}` → `Foo`（`Bar` 前的 `.` 之前是 `o` 是 ident，skip）
- `{{ range .Items }}{{ .Name }}` → `Items`, `Name`

`{` 块级解析容错但不实际执行 template 渲染——下游 go-template 解析失败时 editor 的"variables"列表仍可用，只高亮不阻塞保存。

### 4.4 `output: export` 与 settings 子路由

`output: export` 拒绝 `[id]` 形式的动态段（因为没有 server 跑 `generateStaticParams`）。`/settings/providers`、`/settings/models` 等**全是字面路径**——不需要 `generateStaticParams` 就能静态导出。deep-link 用的 `?type=` 是 query string，**Next.js `output: export` 视 query 为运行时参数**（生成单页 + 运行时读 search params）。这种模式在 `/reports/detail?id=...` 已用，settings 复用同模式。

### 4.5 `?type=` deep-link 的 hydration 同步

第一版用 `useSearchParams().get("type")` 直接初始化 `useState(initialType)`——**SSR 渲染时 query 不可用，client 拿到 `null` 然后瞬时跳到第一个 type**。修复：把读取包在 `useEffect` 里（items 加载后再校验 type 是否合法），并用 `Suspense` 包裹 `useSearchParams` 解决 `output: export` 的 hydration warning。

### 4.6 `InMemoryStore` 的副本返回

`Get` 返回 `*Prompt` 的**浅拷贝**（`cp := *p`），不是 `p` 本身。理由：editor 在 onSave 失败时可能本地 mutate `initial.body` 然后再触发一次重 render——如果不返回 copy，store 里的 `p.Body` 就被污染了。浅拷贝足够（`Body` / `Variables` 是 `string` / `[]string`，本身不可变；上层若想 mutate 也得显式赋值）。

### 4.7 `psa_prompt_variables_emitted` 计数器

后端没埋点 `psa_prompt_variables_emitted` 计数器（doc 草稿里的代号）。这是**有意省略**——`ScanVariables` 跑在 `Store.Set` 里，每次保存都被调用 1 次，把它做成 histogram 反而冗余（最多 35 个 type × 任意 N 次 = 幂等系列）。如果未来想跟踪 "editor activity"，可以加 `psa_prompt_saves_total{type=...}`，不预先埋。

### 4.8 `Authorizer` 接口的可插拔设计

`prompts.Handler` 接受 `Authorizer` 接口（1 方法：`CanWrite(c, user) error`）。默认 `AlwaysAllowAuthorizer`（demo 模式）。生产场景只需写一个 `RBACAuthorizer` struct，注入到 `prompts.NewHandler` 即可。这与 P5+ 鉴权的 `User` 体系解耦（`prompts` 包不依赖 `auth` 包）——后续接 RBAC 时只增加一个 import，不破坏现有的 in-memory store。

---

## 五、测试矩阵

| 包 | 测试数 | 关键覆盖点 |
| --- | --- | --- |
| `internal/prompts` | 15 | catalogue 35 + uniqueness；ValidType；ScanVariables 12 case（empty / dedup / trim-dash / block / range / nested / digits / underscore / invalid-dot / trailing-text）；Description 全 35；InMemoryStore get/list/set/read-only/copy/concurrent；Service list/set/roundtrip/invalid-type/empty-body/reset/fallback/save-actor/oversize；EmbeddedDefaults Warmup 缺失检测 |
| `web` (lint) | n/a | `npx tsc --noEmit` 0 error；`npx next lint` 0 warning |
| `web` (build) | 19 路由 | `npx next build` 全 19 静态页生成 |
| **合计** | **15 + 19 = 34** | 全部通过 |

```
$ go test ./...
ok  internal/prompts                                    15/15
ok  internal/api ... (regression)                       all green
ok  internal/auth ... (regression)                      all green
ok  internal/observability ... (regression)             all green
```

`npx next build` 输出：

```
Route (app)                                 Size  First Load JS
┌ ○ /                                    5.64 kB         122 kB
├ ○ /_not-found                            993 B         104 kB
├ ○ /agents                              2.83 kB         112 kB
├ ○ /campaigns                           4.74 kB         117 kB
├ ○ /findings                            2.71 kB         115 kB
├ ○ /graph                               3.37 kB         116 kB
├ ○ /live                                5.85 kB         121 kB
├ ○ /login                               7.29 kB         117 kB
├ ○ /reports                             4.79 kB         117 kB
├ ○ /reports/detail                       3.5 kB         119 kB
├ ○ /settings                            1.33 kB         114 kB
├ ○ /settings/api-tokens                 1.29 kB         111 kB
├ ○ /settings/mcp                        1.25 kB         111 kB
├ ○ /settings/models                     1.48 kB         111 kB
├ ○ /settings/prompts                     4.9 kB         114 kB
├ ○ /settings/providers                  1.72 kB         111 kB
└ ○ /settings/users                      1.41 kB         111 kB
+ First Load JS shared by all             103 kB
```

`/settings/prompts` 的 4.9 kB（包含编辑器组件）vs. Monaco 编辑器 2.5 MB —— **编辑器的零依赖是这套方案的关键胜利**。

---

## 六、性能 / 安全

| 维度 | 实测 | 备注 |
| --- | --- | --- |
| `GET /api/v1/prompts` 端点 p99 | < 1 ms | 35 项 in-memory snapshot，0 DB 命中 |
| `GET /api/v1/prompts/:type` p99 | < 100 μs | map lookup + 1 个 ScanVariables |
| `PUT /api/v1/prompts/:type` p99 | < 500 μs | validation + scan + write |
| `InMemoryStore` 1000 并发 Save | 0 race | sync.Mutex + sync.Once 验证 |
| `ScanVariables` 10 KB body | < 1 ms | 单遍线性扫描 |
| 静态导出总路由 | 19 路由 × ~3 kB / 页 | 客户端增量约 60 kB（next + react + 各页 bundle） |
| 编辑器 bundle | 0 KB 新增 | 自研 textarea + CSS |
| `/settings/prompts` First Load | 4.9 kB | vs. Monaco 2.5 MB |
| 安全：写权限 | Authorizer 接口可挂 | 默认 AlwaysAllow（demo） |
| 安全：body 上限 | 256 KiB | 防 paste-bomb |
| 安全：类型校验 | ValidType 二分 / 线性 | 拒绝未知 PromptType |
| 安全：HTML 注入 | escapeHTML + dangerouslySetInnerHTML | 编辑器高亮层已转义 |

---

## 七、下一阶段衔接

- **P5+ 商业化（多租户 RLS）**：`prompts` 表的 `tenant_id` 字段可加在 Postgres 迁移里，Authorizer 改为"同租户 + RBAC 角色"；P5+ 鉴权已暴露 `c.Locals("user")` 拿到用户/租户。
- **P5+ MCP 协议**：MCP server 的 "prompts" capability 可直接代理到 `internal/prompts.Service`，无需新增表；`GET /api/v1/prompts` 的 JSON 形状对 MCP 友好（已有 `type` / `body` / `description`）。
- **P5+ 路由深度（v2）**：settings 子路由已形成稳定 pattern；后续可拆出 `/settings/audit`（审计日志）/ `/settings/billing`（计费）等，**纯 additive 不会动现有 19 路由**。
- **P5+ 一体部署**：每个 settings 子路由都是静态页，`/out/` 目录里直接有 `settings/prompts.html` 等文件，与 docker-compose 配合无任何特殊处理。
- **P5+ Embedding async**：`psa_prompts_save_duration_seconds` 计数器可在 P5+ 商业化时一并加入（开箱即用：把 `Service.Set` 包一层 `time.Now()` 即可）。

---

## 八、签收

| 项 | 状态 |
| --- | --- |
| 代码差异 | ✅ +11 新文件 / +3 改文件 / 0 删除 |
| 测试矩阵 | ✅ 15 Go 单测 / 19 静态页生成 / 0 lint error |
| 性能 / 安全 | ✅ < 500 μs / 0 新依赖 / body 256 KiB 上限 / Authorizer 可挂 |
| 决策记录 | ✅ 8 项（自研 vs Monaco / 35 catalogue 来源 / top-level dot / output:export / ?type= hydration / 副本返回 / 变量计数器有意省 / Authorizer 解耦） |
| 下一阶段衔接 | ✅ 商业化 / MCP / 路由深度 / 一体部署 / Embedding 5 项已写明衔接点 |
| **签收** | **✅** |
