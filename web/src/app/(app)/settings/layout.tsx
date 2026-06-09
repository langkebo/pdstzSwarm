"use client";

import { ReactNode } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { GlassPanel } from "@/components/GlassPanel";
import { TopBar } from "@/components/TopBar";
import { cn } from "@/lib/cn";

/**
 * Settings layout — owns the rail of sub-routes (P5+ 路由深度
 * 扩展). Six real links instead of in-page tabs so each section
 * has its own URL, its own back-button history, and its own
 * `output: export` static page.
 *
 * The rail is sticky on lg+ and collapses above md (the link
 * list is still visible — the page is mostly admin-only so the
 * lost-screen-real-estate is acceptable).
 */
const TABS = [
  { href: "/settings", label: "总览", glyph: "◇" },
  { href: "/settings/providers", label: "LLM 提供商", glyph: "◈" },
  { href: "/settings/models", label: "专项模型", glyph: "▦" },
  { href: "/settings/prompts", label: "提示词模板", glyph: "▤" },
  { href: "/settings/api-tokens", label: "API 令牌", glyph: "▣" },
  { href: "/settings/users", label: "用户与角色", glyph: "◉" },
  { href: "/settings/mcp", label: "MCP 服务器", glyph: "⌬" },
];

export default function SettingsLayout({ children }: { children: ReactNode }) {
  const pathname = usePathname();

  return (
    <div className="min-h-screen flex flex-col">
      <TopBar
        title="设置"
        subtitle="LLM 提供商 · 提示词 · API 令牌 · 用户 · MCP"
      />

      <div className="p-6 grid grid-cols-1 lg:grid-cols-[220px_1fr] gap-6 animate-fade-in">
        <GlassPanel
          variant="soft"
          className="p-2 self-start sticky top-4"
          frame
          frameTag="settings.nav"
        >
          <nav className="space-y-1" aria-label="设置导航">
            {TABS.map((t) => {
              const isActive =
                t.href === "/settings"
                  ? pathname === "/settings"
                  : pathname?.startsWith(t.href) ?? false;
              return (
                <Link
                  key={t.href}
                  href={t.href}
                  className={cn(
                    "w-full flex items-center gap-2 px-3 py-2 rounded-md font-mono text-xs uppercase tracking-wider",
                    "transition-colors duration-150",
                    isActive
                      ? "bg-accent/10 text-accent border border-accent/40"
                      : "text-text-secondary border border-transparent hover:text-text-primary hover:bg-surface-hover"
                  )}
                  aria-current={isActive ? "page" : undefined}
                >
                  <span className="w-4 text-center">{t.glyph}</span>
                  {t.label}
                </Link>
              );
            })}
          </nav>
        </GlassPanel>

        <div className="space-y-6 min-w-0">{children}</div>
      </div>
    </div>
  );
}
