"use client";

import { SectionShell } from "@/components/settings/SectionShell";

export default function UsersPage() {
  return (
    <SectionShell
      title="用户与角色"
      hint="RBAC：管理员 · 操作员 · 审计员 · 只读。"
      tag="settings.users"
    >
      <p className="text-sm text-text-secondary font-mono">
        用户管理界面将在 P5+ 商业化（多租户）阶段交付。
        此处为占位结构；鉴权后端（P5+ 鉴权）已开放
        <code className="mx-1 font-mono text-accent">/api/v1/auth/me</code>
        接口，
        <code className="mx-1 font-mono text-accent">SessionMiddleware</code>
        会在请求上下文填充 <code className="font-mono text-accent">c.Locals(&quot;user&quot;)</code>，
        用于报告作者的归属。
      </p>
    </SectionShell>
  );
}
