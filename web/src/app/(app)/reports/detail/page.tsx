"use client";

/**
 * Report detail — P5+ Reports productization.
 *
 * Renders a single generated report. The view has two panes:
 *
 *   - left  : a sticky metadata panel with risk distribution,
 *             section table-of-contents, and a download-Markdown
 *             button
 *   - right : the Markdown body, rendered through the project design
 *             tokens via MarkdownRenderer
 *
 * The route is `/reports/detail?id=<uuid>` — a static page with the
 * id in the query string so it works under `output: export` (which
 * would otherwise require `generateStaticParams` for a dynamic route).
 */

import { Suspense, useEffect, useState } from "react";
import Link from "next/link";
import dynamic from "next/dynamic";
import { useSearchParams } from "next/navigation";
import { api, type Report } from "@/lib/api";
import { GlassPanel } from "@/components/GlassPanel";
import { TopBar } from "@/components/TopBar";
import { SeveritySummaryBar } from "@/components/SeveritySummaryBar";
import { cn } from "@/lib/cn";

// Lazy-load the Markdown renderer; it pulls in react-markdown +
// remark-gfm, which together are ~80KB gzip we don't want in the
// initial dashboard bundle.
const MarkdownRenderer = dynamic(
  () => import("@/components/MarkdownRenderer").then((m) => m.MarkdownRenderer),
  {
    ssr: false,
    loading: () => (
      <div className="px-4 py-6 text-sm text-text-muted font-mono">
        正在加载 Markdown 渲染器…
      </div>
    ),
  },
);

export default function ReportDetailPage() {
  return (
    <Suspense fallback={<ReportDetailFallback />}>
      <ReportDetail />
    </Suspense>
  );
}

function ReportDetailFallback() {
  return (
    <div className="min-h-screen flex flex-col">
      <TopBar title="报告" subtitle="加载中…" />
      <div className="flex-1 grid grid-cols-1 lg:grid-cols-[280px_1fr] gap-0 overflow-hidden">
        <aside className="border-r border-border bg-background/60 p-4">
          <p className="text-sm text-text-muted font-mono">加载中…</p>
        </aside>
        <article className="overflow-y-auto px-6 py-6">
          <p className="text-sm text-text-muted font-mono">加载中…</p>
        </article>
      </div>
    </div>
  );
}

function ReportDetail() {
  const search = useSearchParams();
  const id = search.get("id") ?? "";

  const [report, setReport] = useState<Report | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    if (!id) {
      setError("未提供报告 ID —— 请使用 ?id=<uuid> 访问。");
      setLoading(false);
      return;
    }
    (async () => {
      try {
        const r = await api.reports.get(id);
        if (cancelled) return;
        setReport(r);
        setLoading(false);
      } catch (err) {
        if (cancelled) return;
        setError(
          err instanceof Error ? err.message : "加载报告失败",
        );
        setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [id]);

  function downloadMarkdown() {
    if (!report) return;
    const blob = new Blob([report.markdown ?? ""], {
      type: "text/markdown;charset=utf-8",
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${report.title || report.id}.md`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  }

  return (
    <div className="min-h-screen flex flex-col">
      <TopBar
        title={report?.title ?? "报告"}
        subtitle={
          report
            ? `id ${report.id.slice(0, 8)} · ${formatBytes(report.byte_size)}`
            : "加载中…"
        }
        actions={
          <div className="flex items-center gap-2">
            <Link
              href="/reports"
              className="font-mono text-[10px] uppercase tracking-widest text-text-muted hover:text-accent"
            >
              ← 返回报告列表
            </Link>
            {report && (
              <button
                type="button"
                onClick={downloadMarkdown}
                className="btn-cyber"
              >
                ⇣ 下载 .md
              </button>
            )}
          </div>
        }
      />

      <div className="flex-1 grid grid-cols-1 lg:grid-cols-[280px_1fr] gap-0 overflow-hidden">
        {/* Sidebar */}
        <aside className="border-r border-border bg-background/60 overflow-y-auto p-4 space-y-4">
          {loading ? (
            <div className="text-sm text-text-muted font-mono">加载中…</div>
          ) : error ? (
            <div className="rounded border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-xs text-rose-200 font-mono">
              {error}
            </div>
          ) : report ? (
            <>
              <GlassPanel
                variant="soft"
                className="p-3"
                frame
                frameTag="rpt.meta"
              >
                <p className="text-[10px] font-mono uppercase tracking-widest text-text-muted mb-2">
                  风险概览
                </p>
                <SeveritySummaryBar summary={report.summary} />
              </GlassPanel>

              <GlassPanel
                variant="soft"
                className="p-3"
                frame
                frameTag="rpt.table"
              >
                <p className="text-[10px] font-mono uppercase tracking-widest text-text-muted mb-2">
                  报告章节
                </p>
                <ul className="space-y-1">
                  {report.sections
                    .slice()
                    .sort((a, b) => a.order - b.order)
                    .map((s) => (
                      <li
                        key={s.key}
                        className="font-mono text-xs text-text-secondary hover:text-accent"
                      >
                        <a
                          href={`#${slugify(s.title)}`}
                          className="block truncate"
                        >
                          <span className="text-text-muted">
                            {String(s.order).padStart(2, "0")}
                          </span>{" "}
                          {s.title}
                        </a>
                      </li>
                    ))}
                </ul>
              </GlassPanel>

              <GlassPanel
                variant="soft"
                className="p-3"
                frame
                frameTag="rpt.info"
              >
                <p className="text-[10px] font-mono uppercase tracking-widest text-text-muted mb-2">
                  生成信息
                </p>
                <dl className="space-y-1 font-mono text-[11px]">
                  <KV label="创建时间" value={fmtDate(report.created_at)} />
                  {report.completed_at && (
                    <KV
                      label="完成时间"
                      value={fmtDate(report.completed_at)}
                    />
                  )}
                  <KV label="格式" value={report.format} />
                  <KV
                    label="状态"
                    value={
                      report.status === "ready"
                        ? "完成"
                        : report.status === "failed"
                        ? "失败"
                        : report.status === "queued"
                        ? "等待中"
                        : "生成中"
                    }
                    className="uppercase"
                  />
                  <KV label="大小" value={formatBytes(report.byte_size)} />
                  {report.summary.unique_agents > 0 && (
                    <KV
                      label="代理数"
                      value={String(report.summary.unique_agents)}
                    />
                  )}
                  {report.summary.unique_targets > 0 && (
                    <KV
                      label="目标数"
                      value={String(report.summary.unique_targets)}
                    />
                  )}
                  {report.summary.duration_seconds > 0 && (
                    <KV
                      label="耗时"
                      value={humanDuration(
                        report.summary.duration_seconds,
                      )}
                    />
                  )}
                </dl>
              </GlassPanel>
            </>
          ) : null}
        </aside>

        {/* Body */}
        <article className="overflow-y-auto px-6 py-6">
          {loading ? (
            <div className="text-sm text-text-muted font-mono">加载中…</div>
          ) : report ? (
            <GlassPanel
              variant="soft"
              className="p-6"
              frame
              frameTag={`rpt.${report.id.slice(0, 6)}`}
            >
              <MarkdownRenderer source={report.markdown ?? ""} />
            </GlassPanel>
          ) : (
            <p className="text-sm text-text-muted font-mono">
              {error ?? "未找到报告。"}
            </p>
          )}
        </article>
      </div>
    </div>
  );
}

function KV({
  label,
  value,
  className,
}: {
  label: string;
  value: string;
  className?: string;
}) {
  return (
    <div className="flex items-baseline gap-2">
      <span className="text-text-muted uppercase tracking-widest w-20">
        {label}
      </span>
      <span className={cn("text-text-primary break-all", className)}>
        {value}
      </span>
    </div>
  );
}

function slugify(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^\w一-龥]+/g, "-")
    .replace(/(^-|-$)/g, "");
}

function fmtDate(iso: string): string {
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

function formatBytes(n: number): string {
  if (n <= 0) return "0 B";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / 1024 / 1024).toFixed(2)} MiB`;
}

function humanDuration(seconds: number): string {
  if (seconds <= 0) return "—";
  const d = new Date(seconds * 1000);
  const h = d.getUTCHours();
  const m = d.getUTCMinutes();
  const s = d.getUTCSeconds();
  if (h > 0) return `${h}h ${m}m ${s}s`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}
