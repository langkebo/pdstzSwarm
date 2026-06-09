"use client";

import { SectionShell } from "@/components/settings/SectionShell";
import { cn } from "@/lib/cn";

const SPECIALIST_MODELS = [
  { name: "侦察代理", model: "deepseek-chat", status: "ready" },
  { name: "分类器", model: "deepseek-reasoner", status: "ready" },
  { name: "利用代理", model: "deepseek-chat", status: "ready" },
  { name: "报告代理", model: "deepseek-chat", status: "ready" },
];

export default function ModelsPage() {
  return (
    <SectionShell
      title="专项模型"
      hint="为每个代理角色单独配置模型。留空则继承全局默认。"
      tag="settings.models"
    >
      <div className="space-y-2">
        {SPECIALIST_MODELS.map((m) => (
          <div
            key={m.name}
            className="flex items-center justify-between gap-3 py-2 px-3 bg-background/60 border border-border rounded-md"
          >
            <div className="min-w-0">
              <p className="text-sm font-medium text-text-primary">{m.name}</p>
              <p className="text-[11px] font-mono text-text-muted truncate">{m.model}</p>
            </div>
            <span
              className={cn(
                "px-1.5 py-0.5 rounded-sm font-mono text-[10px] uppercase tracking-widest border",
                m.status === "ready" ? "status-running" : "status-paused"
              )}
            >
              就绪
            </span>
          </div>
        ))}
      </div>
    </SectionShell>
  );
}
