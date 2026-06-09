"use client";

import { useEffect, useState } from "react";
import { GlassPanel } from "@/components/GlassPanel";
import { TopBar } from "@/components/TopBar";
import { cn } from "@/lib/cn";

/**
 * Agents — list of agent roles in the swarm. The backend
 * doesn't currently expose a per-agent CRUD endpoint; the
 * canonical agent catalogue is the LLM preset in
 * `internal/llm/presets.go` + the role prompt under
 * `internal/agent/prompts/`. This page is read-only for now;
 * P5+ 提示词编辑 redirects here for the per-agent role
 * template.
 */
interface AgentRole {
  id: string;
  label: string;
  category: "auth" | "api" | "web" | "cloud" | "core";
  model: string;
  status: "ready" | "loading" | "missing";
  promptType: string;
  description: string;
}

const ROLES: AgentRole[] = [
  { id: "auth_recon",     label: "认证侦察",     category: "auth",  model: "deepseek-chat",     status: "ready", promptType: "auth_recon",     description: "枚举登录表单、OAuth 端点、解析 JWT、密码重置流程。" },
  { id: "auth_exploit",   label: "认证利用",     category: "auth",  model: "deepseek-chat",     status: "ready", promptType: "auth_exploit",   description: "IDOR、JWT 混淆、OAuth 状态绕过、双因素绕过、密码重置投毒。" },
  { id: "api_recon",      label: "接口侦察",     category: "api",   model: "deepseek-chat",     status: "ready", promptType: "api_recon",      description: "内省 GraphQL、枚举 REST 动词、跟踪 .well-known/openapi.json。" },
  { id: "api_exploit",    label: "接口利用",     category: "api",   model: "deepseek-chat",     status: "ready", promptType: "api_exploit",    description: "BOLA、BOPLA、认证缺陷、批量赋值、限速绕过。" },
  { id: "web_recon",      label: "Web 侦察",     category: "web",   model: "deepseek-chat",     status: "ready", promptType: "web_recon",      description: "指纹识别技术栈、跟踪站点地图、从 JS 文件枚举端点。" },
  { id: "web_exploit",    label: "Web 利用",     category: "web",   model: "deepseek-chat",     status: "ready", promptType: "web_exploit",    description: "XSS、SSRF、反序列化、请求走私、原型链污染。" },
  { id: "cloud_recon",    label: "云侦察",       category: "cloud", model: "deepseek-chat",     status: "ready", promptType: "cloud_recon",    description: "枚举 S3 / GCS / Azure Blob 存储、扫描元数据端点。" },
  { id: "cloud_exploit",  label: "云利用",       category: "cloud", model: "deepseek-chat",     status: "ready", promptType: "cloud_exploit",  description: "SSRF 命中 169.254.169.254、KMS、IAM 枚举、Lambda 调用。" },
  { id: "classifier",     label: "分类器",       category: "core",  model: "deepseek-reasoner", status: "ready", promptType: "classifier",     description: "为原始发现分配 OWASP 类别、严重度和 CVSS 评分。" },
  { id: "triage",         label: "分诊器",       category: "core",  model: "deepseek-reasoner", status: "ready", promptType: "triage",         description: "决定哪个代理角色应接手下一条黑板记录。" },
  { id: "orchestrator",   label: "编排器",       category: "core",  model: "deepseek-reasoner", status: "ready", promptType: "orchestrator",   description: "按步骤选择代理、范围守护、信息素衰减。" },
  { id: "summarizer",     label: "摘要器",       category: "core",  model: "deepseek-chat",     status: "ready", promptType: "summarizer",     description: "压缩代理上下文以适配模型的上下文窗口。" },
  { id: "report",         label: "报告器",       category: "core",  model: "deepseek-chat",     status: "ready", promptType: "report",         description: "根据分类后的发现与指标数据，渲染精美的 Markdown 报告。" },
];

const CATEGORY_GLYPHS: Record<AgentRole["category"], string> = {
  auth: "🔐",
  api: "⊕",
  web: "▣",
  cloud: "☁",
  core: "◇",
};

const CATEGORY_LABEL: Record<AgentRole["category"] | "all", string> = {
  all: "全部",
  auth: "认证",
  api: "接口",
  web: "Web",
  cloud: "云",
  core: "核心",
};

const STATUS_LABEL: Record<AgentRole["status"], string> = {
  ready: "就绪",
  loading: "加载中",
  missing: "缺失",
};

export default function AgentsPage() {
  const [category, setCategory] = useState<AgentRole["category"] | "all">("all");
  const [ready, setReady] = useState<number>(0);

  useEffect(() => {
    setReady(ROLES.filter((r) => r.status === "ready").length);
  }, []);

  const visible = category === "all" ? ROLES : ROLES.filter((r) => r.category === category);

  return (
    <div className="min-h-screen flex flex-col">
      <TopBar
        title="代理"
        subtitle={`${ready} / ${ROLES.length} 个代理角色就绪 · 单击可检视提示词模板`}
      />

      <div className="p-6 space-y-4 animate-fade-in">
        {/* Category filter */}
        <div className="flex items-center gap-2 flex-wrap">
          {(["all", "auth", "api", "web", "cloud", "core"] as const).map((c) => (
            <button
              key={c}
              onClick={() => setCategory(c)}
              className={cn(
                "px-3 py-1 rounded-md font-mono text-xs uppercase tracking-widest border transition-colors",
                category === c
                  ? "border-accent text-accent bg-accent/10"
                  : "border-border text-text-secondary hover:text-text-primary"
              )}
              type="button"
            >
              {CATEGORY_LABEL[c]}
              {c !== "all" && (
                <span className="ml-1 text-text-muted">
                  ({ROLES.filter((r) => r.category === c).length})
                </span>
              )}
            </button>
          ))}
        </div>

        {/* Grid */}
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
          {visible.map((r) => (
            <a
              key={r.id}
              href={`/settings/prompts?type=${encodeURIComponent(r.promptType)}`}
            >
              <GlassPanel
                variant="soft"
                className="p-4 h-full hover:border-accent/40 transition-colors"
                frame
                frameTag={`agent.${r.id}`}
              >
                <div className="flex items-start justify-between gap-2 mb-2">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="text-accent">{CATEGORY_GLYPHS[r.category]}</span>
                    <h3 className="font-display text-sm font-semibold text-text-primary truncate">
                      {r.label}
                    </h3>
                  </div>
                  <span
                    className={cn(
                      "px-1.5 py-0.5 rounded-sm font-mono text-[10px] uppercase tracking-widest border shrink-0",
                      r.status === "ready"
                        ? "status-running"
                        : r.status === "loading"
                        ? "status-pending"
                        : "status-paused"
                    )}
                  >
                    {STATUS_LABEL[r.status]}
                  </span>
                </div>
                <p className="text-[11px] font-mono text-text-muted leading-relaxed">
                  {r.description}
                </p>
                <div className="mt-3 flex items-center justify-between text-[10px] font-mono text-text-muted">
                  <span>模型：{r.model}</span>
                  <span>提示词：{r.promptType}</span>
                </div>
              </GlassPanel>
            </a>
          ))}
        </div>
      </div>
    </div>
  );
}
