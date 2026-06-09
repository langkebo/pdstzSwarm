"use client";

import { useState, useEffect, useCallback, Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { api, type PromptType, type PromptSummary, type PromptDetail } from "@/lib/api";
import { SectionShell } from "@/components/settings/SectionShell";
import { PromptEditor } from "@/components/settings/PromptEditor";
import { cn } from "@/lib/cn";

/**
 * /settings/prompts — the 35-PromptType editor (P5+ 提示词
 * 编辑闭环). The page composes:
 *
 *   1. A left rail of all 35 types, with a "X / 35 customized"
 *      counter and a vN override badge per type.
 *   2. A right pane with the PromptEditor (Monaco-free,
 *      zero-bundle syntax-highlighted textarea).
 *
 * URL state: `?type=auth_recon` deep-links to a specific
 * template — used by the /agents page's "click to inspect"
 * tiles.
 */
export default function PromptsPage() {
  return (
    <Suspense
      fallback={
        <SectionShell title="提示词模板" tag="settings.prompts.loading">
          <p className="font-mono text-sm text-text-muted">正在加载 35 个模板…</p>
        </SectionShell>
      }
    >
      <PromptsPageInner />
    </Suspense>
  );
}

function PromptsPageInner() {
  const searchParams = useSearchParams();
  const initialType = searchParams?.get("type") as PromptType | null;
  const [items, setItems] = useState<PromptSummary[] | null>(null);
  const [active, setActive] = useState<PromptType | null>(initialType);
  const [detail, setDetail] = useState<PromptDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Initial load: list
  useEffect(() => {
    let cancel = false;
    api.prompts
      .list()
      .then((r) => {
        if (cancel) return;
        setItems(r.prompts);
        if (!active && r.prompts.length > 0) {
          setActive(r.prompts[0].type);
        }
      })
      .catch((e) => {
        if (cancel) return;
        setError(String(e));
      });
    return () => {
      cancel = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Sync the `?type=` URL param to the active selection so
  // deep-links from /agents land on the right template.
  useEffect(() => {
    if (initialType && items && items.some((p) => p.type === initialType)) {
      setActive(initialType);
    }
  }, [initialType, items]);

  // When the active type changes, fetch the detail (which
  // includes the body).
  useEffect(() => {
    if (!active) return;
    let cancel = false;
    setDetail(null);
    api.prompts
      .get(active)
      .then((d) => {
        if (cancel) return;
        setDetail(d);
      })
      .catch((e) => {
        if (cancel) return;
        setError(String(e));
      });
    return () => {
      cancel = true;
    };
  }, [active]);

  const handleSave = useCallback((next: PromptDetail) => {
    setDetail(next);
    // Refresh the list so the version badge updates.
    api.prompts
      .list()
      .then((r) => setItems(r.prompts))
      .catch(() => {});
  }, []);

  const handleReset = useCallback(async () => {
    if (!active) return;
    if (!confirm(`将 ${active} 恢复为内嵌默认模板？`)) return;
    try {
      await api.prompts.reset(active);
      const d = await api.prompts.get(active);
      setDetail(d);
      const r = await api.prompts.list();
      setItems(r.prompts);
    } catch (e) {
      setError(String(e));
    }
  }, [active]);

  if (error) {
    return (
      <SectionShell title="提示词模板" tag="settings.prompts.error">
        <p className="font-mono text-sm text-red-400">
          加载失败：{error}
        </p>
        <p className="text-xs font-mono text-text-muted mt-2">
          请确认 API 服务已运行且 prompts 处理器已正确挂载（参见 <code>cli/serve.go</code>）。
        </p>
      </SectionShell>
    );
  }

  if (!items) {
    return (
      <SectionShell title="提示词模板" tag="settings.prompts.loading">
        <p className="font-mono text-sm text-text-muted">正在加载 35 个模板…</p>
      </SectionShell>
    );
  }

  const overriddenCount = items.filter((p) => p.version > 0).length;

  return (
    <SectionShell
      title="提示词模板"
      hint={`${overriddenCount} / 35 已自定义 · Go 模板语法 · ⌘S 保存 · ⌘R 恢复默认`}
      tag="settings.prompts"
    >
      <div className="grid grid-cols-1 lg:grid-cols-[200px_1fr] gap-4">
        <nav
          aria-label="提示词类型"
          className="space-y-0.5 max-h-[520px] overflow-y-auto pr-1"
        >
          {items.map((p) => (
            <button
              key={p.type}
              onClick={() => setActive(p.type)}
              className={cn(
                "w-full text-left px-2 py-1.5 rounded font-mono text-[11px]",
                "transition-colors duration-150",
                active === p.type
                  ? "bg-accent/10 text-accent border border-accent/40"
                  : "text-text-secondary border border-transparent hover:text-text-primary hover:bg-surface-hover"
              )}
              type="button"
            >
              <div className="flex items-center justify-between gap-1">
                <span className="truncate">{p.type}</span>
                {p.version > 0 ? (
                  <span className="text-[9px] px-1 rounded border border-accent/40 text-accent shrink-0">
                    v{p.version}
                  </span>
                ) : null}
              </div>
            </button>
          ))}
        </nav>

        <div className="min-w-0">
          {detail && active ? (
            <PromptEditor
              type={active}
              initial={detail}
              onSave={handleSave}
              onReset={handleReset}
            />
          ) : (
            <p className="font-mono text-xs text-text-muted">加载中…</p>
          )}
        </div>
      </div>
    </SectionShell>
  );
}
