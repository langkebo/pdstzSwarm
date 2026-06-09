"use client";

import { useState, useRef, useEffect } from "react";
import { useRouter } from "next/navigation";
import { api, type CampaignMode } from "@/lib/api";
import { cn } from "@/lib/cn";

interface CreateCampaignDialogProps {
  open: boolean;
  onClose: () => void;
  /** Called after a successful creation so the parent can refresh. */
  onCreated?: () => void;
}

const MODES: { value: CampaignMode; label: string; desc: string }[] = [
  { value: "fast", label: "快速", desc: "快速扫描，适合初步侦察" },
  { value: "balanced", label: "均衡", desc: "在速度与深度之间取得平衡" },
  { value: "deep", label: "深度", desc: "全面深入扫描，覆盖更多攻击面" },
  { value: "stealth", label: "隐匿", desc: "低速隐蔽扫描，降低被检测风险" },
];

export function CreateCampaignDialog({
  open,
  onClose,
  onCreated,
}: CreateCampaignDialogProps) {
  const router = useRouter();
  const [target, setTarget] = useState("");
  const [objective, setObjective] = useState("");
  const [mode, setMode] = useState<CampaignMode>("balanced");
  const [scope, setScope] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  // Reset form when dialog opens
  useEffect(() => {
    if (open) {
      setTarget("");
      setObjective("");
      setMode("balanced");
      setScope("");
      setUsername("");
      setPassword("");
      setError(null);
      // Focus the target input after a short delay for the animation
      setTimeout(() => inputRef.current?.focus(), 100);
    }
  }, [open]);

  // Close on Escape
  useEffect(() => {
    if (!open) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmedTarget = target.trim();
    if (!trimmedTarget) {
      setError("请输入目标地址");
      return;
    }

    setSubmitting(true);
    setError(null);

    try {
      const scopeList = scope
        .split(/[,;\s]+/)
        .map((s) => s.trim())
        .filter(Boolean);

      const result = await api.campaigns.create({
        target: trimmedTarget,
        objective: objective.trim() || "find all vulnerabilities",
        mode,
        scope: scopeList,
        username: username.trim() || undefined,
        password: password.trim() || undefined,
      } as never);

      // Auto-start the campaign after creation
      if (result?.id) {
        try {
          await api.campaigns.start(result.id);
        } catch (startErr) {
          // Show a non-blocking error — the campaign was created and
          // can be started manually from the dashboard.
          setError(
            `任务已创建，但自动启动失败：${startErr instanceof Error ? startErr.message : "未知错误"}。请前往任务列表手动启动。`
          );
          // Still close the dialog and refresh the list so the user
          // can see the new campaign.
          onCreated?.();
          onClose();
          return;
        }
      }

      onCreated?.();
      onClose();
      // Navigate to the live page to watch the campaign in real time
      if (result?.id) {
        router.push(`/live?id=${result.id}`);
      }
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "创建任务失败，请重试"
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/70 backdrop-blur-sm"
        onClick={onClose}
      />

      {/* Dialog */}
      <div
        className={cn(
          "relative z-10 w-full max-w-lg mx-4",
          "border border-border bg-background/95 backdrop-blur-xl",
          "rounded-lg shadow-2xl shadow-black/50",
          "animate-fade-in"
        )}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-border">
          <div>
            <h2 className="font-display text-lg font-bold text-text-primary">
              新建渗透任务
            </h2>
            <p className="text-[10px] font-mono uppercase tracking-widest text-text-muted mt-0.5">
              配置目标 · 模式 · 范围
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="text-text-muted hover:text-text-primary transition-colors font-mono text-lg leading-none px-1"
            aria-label="关闭"
          >
            ✕
          </button>
        </div>

        {/* Body */}
        <form onSubmit={handleSubmit} className="px-5 py-4 space-y-4">
          {/* Target */}
          <div>
            <label
              htmlFor="campaign-target"
              className="block text-[10px] font-mono uppercase tracking-widest text-text-secondary mb-1.5"
            >
              目标地址 <span className="text-rose-400">*</span>
            </label>
            <input
              ref={inputRef}
              id="campaign-target"
              type="text"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              placeholder="example.com 或 192.168.1.0/24"
              className="input-cyber w-full"
              disabled={submitting}
              autoComplete="off"
              spellCheck={false}
            />
            <p className="text-[10px] font-mono text-text-muted mt-1">
              域名、IP 地址或 CIDR 范围
            </p>
          </div>

          {/* Objective */}
          <div>
            <label
              htmlFor="campaign-objective"
              className="block text-[10px] font-mono uppercase tracking-widest text-text-secondary mb-1.5"
            >
              任务目标
            </label>
            <input
              id="campaign-objective"
              type="text"
              value={objective}
              onChange={(e) => setObjective(e.target.value)}
              placeholder="find all vulnerabilities"
              className="input-cyber w-full"
              disabled={submitting}
              autoComplete="off"
              spellCheck={false}
            />
          </div>

          {/* Mode */}
          <div>
            <label className="block text-[10px] font-mono uppercase tracking-widest text-text-secondary mb-1.5">
              扫描模式
            </label>
            <div className="grid grid-cols-2 gap-2">
              {MODES.map((m) => (
                <button
                  key={m.value}
                  type="button"
                  onClick={() => setMode(m.value)}
                  disabled={submitting}
                  className={cn(
                    "text-left px-3 py-2 rounded-md border font-mono text-xs transition-colors",
                    mode === m.value
                      ? "border-accent/50 bg-accent/10 text-accent"
                      : "border-border text-text-secondary hover:border-border-strong hover:text-text-primary"
                  )}
                >
                  <span className="uppercase tracking-wider font-semibold">
                    {m.label}
                  </span>
                  <span className="block text-[10px] text-text-muted mt-0.5 normal-case tracking-normal">
                    {m.desc}
                  </span>
                </button>
              ))}
            </div>
          </div>

          {/* Scope */}
          <div>
            <label
              htmlFor="campaign-scope"
              className="block text-[10px] font-mono uppercase tracking-widest text-text-secondary mb-1.5"
            >
              范围限制（可选）
            </label>
            <input
              id="campaign-scope"
              type="text"
              value={scope}
              onChange={(e) => setScope(e.target.value)}
              placeholder="example.com, api.example.com"
              className="input-cyber w-full"
              disabled={submitting}
              autoComplete="off"
              spellCheck={false}
            />
            <p className="text-[10px] font-mono text-text-muted mt-1">
              逗号分隔的域名列表，留空则自动从目标推导
            </p>
          </div>

          {/* Credentials */}
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label
                htmlFor="campaign-username"
                className="block text-[10px] font-mono uppercase tracking-widest text-text-secondary mb-1.5"
              >
                用户名（可选）
              </label>
              <input
                id="campaign-username"
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="admin"
                className="input-cyber w-full"
                disabled={submitting}
                autoComplete="off"
                spellCheck={false}
              />
            </div>
            <div>
              <label
                htmlFor="campaign-password"
                className="block text-[10px] font-mono uppercase tracking-widest text-text-secondary mb-1.5"
              >
                密码（可选）
              </label>
              <input
                id="campaign-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="••••••••"
                className="input-cyber w-full"
                disabled={submitting}
                autoComplete="off"
              />
            </div>
          </div>
          <p className="text-[10px] font-mono text-text-muted -mt-2">
            提供凭证以启用认证扫描（也可在目标地址后输入&ldquo;用户名xxx密码yyy&rdquo;）
          </p>

          {/* Error */}
          {error && (
            <div
              role="alert"
              className="rounded border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-xs text-rose-200 font-mono"
            >
              {error}
            </div>
          )}

          {/* Actions */}
          <div className="flex items-center justify-end gap-3 pt-2 border-t border-border">
            <button
              type="button"
              onClick={onClose}
              disabled={submitting}
              className="px-4 py-2 rounded-md border border-border text-xs font-mono uppercase tracking-wider text-text-secondary hover:text-text-primary hover:border-border-strong transition-colors disabled:opacity-50"
            >
              取消
            </button>
            <button
              type="submit"
              disabled={submitting || !target.trim()}
              className="btn-cyber-solid disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {submitting ? (
                <>
                  <span className="inline-block w-3 h-3 rounded-full border border-emerald-400 border-t-transparent animate-spin mr-1.5" />
                  创建中…
                </>
              ) : (
                <>
                  <span>+</span> 启动任务
                </>
              )}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}