"use client";

/**
 * Reports list — P5+ Reports productization.
 *
 * Lists every report known to the server (most-recent first), with
 * a one-click "Generate" path per campaign and a search box.
 *
 * The full report view lives at /reports/[id] and is rendered with
 * the MarkdownRenderer component. This page intentionally stays
 * simple: header, generator card, list.
 */

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, type Campaign, type ReportListItem } from "@/lib/api";
import { GlassPanel } from "@/components/GlassPanel";
import { TopBar } from "@/components/TopBar";
import { EmptyState } from "@/components/EmptyState";
import { AsciiDecor } from "@/components/AsciiDecor";
import { cn } from "@/lib/cn";

const NO_REPORTS_MOTIF = `┌─────────────────────────┐
│ 0xR3P0                  │
│   no_signal.md          │
│   awaiting_generation   │
└─────────────────────────┘`;

type CampaignLite = { id: string; name?: string; target?: string };

export default function ReportsPage() {
  const [reports, setReports] = useState<ReportListItem[]>([]);
  const [campaigns, setCampaigns] = useState<CampaignLite[]>([]);
  const [query, setQuery] = useState("");
  const [generating, setGenerating] = useState<string | null>(null);
  const [warning, setWarning] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [{ data, meta }, c] = await Promise.all([
          api.reports.listRecent(100),
          api.campaigns.list().catch(() => ({ data: [] as Campaign[] })),
        ]);
        if (cancelled) return;
        setReports(data ?? []);
        const cData = (c as { data?: CampaignLite[] }).data ?? [];
        setCampaigns(cData);
        setWarning(meta?.warning ?? null);
        setLoading(false);
      } catch {
        if (cancelled) return;
        setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return reports;
    return reports.filter(
      (r) =>
        r.title.toLowerCase().includes(q) ||
        r.campaign_id.toLowerCase().includes(q),
    );
  }, [reports, query]);

  async function generateFor(c: CampaignLite) {
    setGenerating(c.id);
    try {
      const r = await api.reports.create(c.id, c.name);
      // Prepend a list-item-shaped projection so the new row shows up
      // immediately, even if the server response includes extra fields.
      const item: ReportListItem = {
        id: r.id,
        campaign_id: r.campaign_id,
        title: r.title,
        status: r.status,
        format: r.format,
        byte_size: r.byte_size,
        error_message: r.error_message,
        created_at: r.created_at,
        completed_at: r.completed_at,
      };
      setReports((prev) => [item, ...prev]);
    } catch (err) {
      setWarning(
        `为 ${c.name ?? c.id} 生成报告失败：${
          err instanceof Error ? err.message : String(err)
        }`,
      );
    } finally {
      setGenerating(null);
    }
  }

  return (
    <div className="min-h-screen flex flex-col">
      <TopBar
        title="报告中心"
        subtitle="基于任务发现自动渲染的渗透测试报告"
        actions={
          <span className="font-mono text-[10px] uppercase tracking-widest text-text-muted">
            {reports.length} 份报告
          </span>
        }
      />

      <div className="px-6 py-3 border-b border-border bg-background/40 flex flex-wrap items-center gap-2">
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索标题 / 任务编号…"
          className="input-cyber max-w-md"
          spellCheck={false}
        />
        <span className="ml-auto text-[10px] font-mono text-text-muted">
          P5+ · Markdown
        </span>
      </div>

      <div className="flex-1 px-6 py-4 space-y-4">
        {warning && (
          <div className="rounded border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200 font-mono">
            {warning}。本会话内生成的报告仍会返回给调用方但不会持久化存储 —— 需将
            Postgres 连接池接入服务端以启用持久化。
          </div>
        )}

        <GlassPanel
          variant="strong"
          frame
          frameTag="reports.generate"
          className="p-4"
        >
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-sm text-slate-300">
              选择一个任务生成报告快照。报告不可变——每次生成都会创建一条新记录，便于历史版本对比与审计。
            </p>
            {campaigns.length > 0 && (
              <span className="text-xs font-mono text-text-muted">
                已知 {campaigns.length} 个任务
              </span>
            )}
          </div>
          {campaigns.length > 0 ? (
            <ul className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
              {campaigns.slice(0, 12).map((c) => (
                <li
                  key={c.id}
                  className="flex items-center justify-between rounded border border-border bg-background/40 px-3 py-2"
                >
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium text-text-primary">
                      {c.name || c.target || c.id.slice(0, 8)}
                    </div>
                    <div className="truncate font-mono text-[10px] text-text-muted">
                      {c.target}
                    </div>
                  </div>
                  <button
                    type="button"
                    onClick={() => generateFor(c)}
                    disabled={generating === c.id}
                    className={cn(
                      "ml-2 rounded-sm border border-accent/40 bg-accent/10 px-2 py-1 text-xs font-mono uppercase tracking-wide text-accent hover:bg-accent/20 transition-colors",
                      generating === c.id && "cursor-wait opacity-50",
                    )}
                  >
                    {generating === c.id ? "生成中…" : "生成"}
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p className="mt-3 text-xs text-text-muted font-mono">
              暂无任务。请先在「任务」页面创建一个。
            </p>
          )}
        </GlassPanel>

        {loading ? (
          <div className="flex items-center gap-2 text-sm text-text-muted font-mono">
            <span className="size-1.5 animate-pulse rounded-full bg-accent" />
            正在加载报告…
          </div>
        ) : filtered.length === 0 ? (
          <EmptyState
            motif={NO_REPORTS_MOTIF}
            title="未找到匹配报告"
            description="当前筛选条件下没有报告。可在上方选择一个任务生成，或等待任务执行完成。"
          />
        ) : (
          <ul className="grid gap-3">
            {filtered.map((r) => (
              <li key={r.id}>
                <Link
                  href={`/reports/detail?id=${r.id}`}
                  className="block transition hover:-translate-y-px"
                >
                  <GlassPanel
                    variant="soft"
                    className="p-4"
                    frame
                    frameTag={`rpt.${r.id.slice(0, 6)}`}
                  >
                    <div className="grid gap-3 sm:grid-cols-[1fr_220px]">
                      <div>
                        <h2 className="text-sm font-semibold text-text-primary truncate">
                          {r.title || `报告 ${r.id.slice(0, 8)}`}
                        </h2>
                        <p className="mt-1 font-mono text-[10px] text-text-muted">
                          编号 {r.id.slice(0, 8)} · 任务{" "}
                          {r.campaign_id.slice(0, 8)} ·{" "}
                          {formatBytes(r.byte_size)} · {r.format}
                        </p>
                      </div>
                      <div className="self-center font-mono text-[10px] text-text-muted">
                        {r.status === "failed" ? (
                          <span className="text-rose-400">生成失败</span>
                        ) : r.completed_at ? (
                          <>
                            生成于{" "}
                            <span className="text-text-primary">
                              {new Date(r.completed_at).toLocaleString()}
                            </span>
                          </>
                        ) : (
                          <span className="text-amber-300">生成中</span>
                        )}
                      </div>
                    </div>
                  </GlassPanel>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </div>

      <AsciiDecor
        tag="P5+"
        className="pointer-events-none fixed bottom-4 right-4 opacity-30"
      />
    </div>
  );
}

function formatBytes(n: number): string {
  if (n <= 0) return "0 B";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / 1024 / 1024).toFixed(2)} MiB`;
}
