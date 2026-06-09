"use client";

import Link from "next/link";
import { GlassPanel } from "@/components/GlassPanel";

/**
 * Settings overview — landing page for the /settings tree.
 * Each tile links to a sub-route (P5+ 路由深度扩展).
 */
const TILES = [
  { href: "/settings/providers", glyph: "◈", title: "LLM 提供商", hint: "DeepSeek · OpenAI · Anthropic · Ollama" },
  { href: "/settings/models", glyph: "▦", title: "专项模型", hint: "为每个代理角色单独配置模型" },
  { href: "/settings/prompts", glyph: "▤", title: "提示词模板", hint: "35 种 PromptType 槽位 · Go 模板语法" },
  { href: "/settings/api-tokens", glyph: "▣", title: "API 令牌", hint: "作用域可控、可吊销、感知到期" },
  { href: "/settings/users", glyph: "◉", title: "用户与角色", hint: "RBAC：管理员 · 操作员 · 审计员 · 只读" },
  { href: "/settings/mcp", glyph: "⌬", title: "MCP 服务器", hint: "STDIO + SSE 传输" },
];

export default function SettingsIndex() {
  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 animate-fade-in">
      {TILES.map((t) => (
        <Link key={t.href} href={t.href}>
          <GlassPanel
            variant="soft"
            className="p-4 h-full hover:border-accent/40 transition-colors"
            frame
            frameTag={t.href}
          >
            <div className="flex items-center gap-2 mb-2">
              <span className="text-accent text-lg">{t.glyph}</span>
              <h3 className="font-display text-sm font-semibold text-text-primary">
                {t.title}
              </h3>
            </div>
            <p className="text-[11px] font-mono text-text-muted">{t.hint}</p>
          </GlassPanel>
        </Link>
      ))}
    </div>
  );
}
