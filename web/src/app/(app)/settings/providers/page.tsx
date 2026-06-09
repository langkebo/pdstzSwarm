"use client";

import { useState } from "react";
import { SectionShell, Field } from "@/components/settings/SectionShell";
import { cn } from "@/lib/cn";

const PROVIDERS = [
  { name: "DeepSeek", desc: "DeepSeek-V3 / R1 · 性价比最佳 · 国内直连", selected: true },
  { name: "OpenAI", desc: "gpt-4o · o1 · 推理能力强" },
  { name: "Anthropic", desc: "claude-sonnet-4-6 · 综合质量优秀" },
  { name: "Ollama（本地）", desc: "本地模型 · 完全隐私" },
];

export default function ProvidersPage() {
  const [selected, setSelected] = useState<string>("DeepSeek");

  return (
    <SectionShell
      title="LLM 提供商"
      hint="选择编排器将要调用的上游 API。单个代理的模型在下一页配置。"
      tag="settings.providers"
    >
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        {PROVIDERS.map((p) => (
          <button
            key={p.name}
            onClick={() => setSelected(p.name)}
            className={cn(
              "text-left p-3 rounded-md border transition-colors",
              selected === p.name
                ? "bg-accent/10 border-accent/50"
                : "border-border hover:border-border-strong bg-background/60"
            )}
            type="button"
          >
            <p className="font-medium text-sm text-text-primary">{p.name}</p>
            <p className="text-[11px] font-mono text-text-muted mt-1">{p.desc}</p>
          </button>
        ))}
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-3 mt-4">
        <Field label="API 基础 URL" placeholder="https://api.deepseek.com/v1" />
        <Field label="API 密钥" type="password" placeholder="sk-…" />
        <Field
          label="默认模型"
          placeholder="deepseek-chat"
          hint="编排器默认使用此模型，除非下方显式覆盖。"
        />
        <Field label="超时时间（秒）" type="number" placeholder="120" />
      </div>
    </SectionShell>
  );
}
