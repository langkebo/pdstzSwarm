"use client";

import { SectionShell } from "@/components/settings/SectionShell";

export default function APITokensPage() {
  return (
    <SectionShell
      title="API 令牌"
      hint="外部 API 访问。令牌支持作用域控制、到期感知与即时吊销。"
      tag="settings.api_tokens"
    >
      <p className="text-sm text-text-secondary font-mono">
        API 令牌管理界面将在 P5+ 商业化（多租户）阶段交付。
        此处为占位结构。
      </p>
    </SectionShell>
  );
}
