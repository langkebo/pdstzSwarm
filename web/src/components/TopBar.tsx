"use client";

import { useEffect, useState } from "react";
import { cn } from "@/lib/cn";
import { api } from "@/lib/api";

interface TopBarProps {
  title: string;
  subtitle?: string;
  /** Optional right-side actions (buttons, search). */
  actions?: React.ReactNode;
  className?: string;
}

function buildTickerMessages(stats: Record<string, number>): string[] {
  const campaigns = Number(stats.campaigns ?? 0);
  const active = Number(stats.active_campaigns ?? 0);
  const findings = Number(stats.total_findings ?? 0);

  return [
    active > 0
      ? `活跃任务: ${active} 个运行中`
      : "集群状态: 空闲",
    `累计任务: ${campaigns} · 发现: ${findings}`,
    "黑板·发现: 实时同步",
    "Docker·执行器: 就绪",
  ];
}

/**
 * TopBar — the page header that sits above the routed content. Hosts
 * the title, an environment tag, the live ticker, and a render-prop
 * slot for page-level actions.
 */
export function TopBar({ title, subtitle, actions, className }: TopBarProps) {
  const [tickerIdx, setTickerIdx] = useState(0);
  const [messages, setMessages] = useState<string[]>(buildTickerMessages({}));

  useEffect(() => {
    api.stats()
      .then((s) => setMessages(buildTickerMessages(s)))
      .catch(() => {});
  }, []);

  useEffect(() => {
    const id = setInterval(
      () => setTickerIdx((i) => (i + 1) % messages.length),
      4000
    );
    return () => clearInterval(id);
  }, [messages.length]);

  return (
    <div
      className={cn(
        "border-b border-border bg-background/60 backdrop-blur-md",
        "px-6 py-3 flex items-center gap-4",
        className
      )}
    >
      <div className="min-w-0">
        <h1 className="font-display text-xl font-bold text-text-primary leading-tight">
          {title}
        </h1>
        {subtitle && (
          <p className="text-xs font-mono text-text-muted mt-0.5 truncate">
            {subtitle}
          </p>
        )}
      </div>
      <div className="flex-1 min-w-0 hidden md:flex">
        <div className="font-mono text-[11px] text-text-muted truncate flex items-center gap-2">
          <span className="text-accent">▸</span>
          <span className="overflow-hidden text-ellipsis whitespace-nowrap">
            {messages[tickerIdx]}
          </span>
        </div>
      </div>
      <div className="flex items-center gap-2 flex-shrink-0">{actions}</div>
    </div>
  );
}
