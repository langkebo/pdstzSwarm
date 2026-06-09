"use client";

import { useEffect, useMemo, useState, useCallback } from "react";
import { useRouter } from "next/navigation";
import { api, type Campaign } from "@/lib/api";
import { useDashboardStore } from "@/lib/store";
import { GlassPanel } from "@/components/GlassPanel";
import { TopBar } from "@/components/TopBar";
import { EmptyState, NO_CAMPAIGNS_MOTIF } from "@/components/EmptyState";
import { CreateCampaignDialog } from "@/components/CreateCampaignDialog";
import { SeverityBadge } from "@/components/SeverityBadge";
import { cn } from "@/lib/cn";
import { severityFromFinding, type Severity } from "@/lib/severity";

type StatusFilter = "all" | "running" | "complete" | "failed";
type View = "grid" | "list";

const STATUS_STYLES: Record<string, string> = {
  planned: "status-paused",
  initializing: "status-pending",
  recon: "status-running",
  classifying: "status-pending",
  planning: "status-paused",
  executing: "status-running",
  reporting: "status-pending",
  complete: "status-complete",
  completed: "status-complete",
  failed: "status-failed",
  aborted: "status-failed",
  running: "status-running",
};

export default function CampaignsPage() {
  const router = useRouter();
  const [view, setView] = useState<View>("grid");
  const [filter, setFilter] = useState<StatusFilter>("all");
  const [query, setQuery] = useState("");
  const [generatingId, setGeneratingId] = useState<string | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const [showCreateDialog, setShowCreateDialog] = useState(false);

  const campaigns = useDashboardStore((s) => s.campaigns);
  const setCampaigns = useDashboardStore((s) => s.setCampaigns);
  const findings = useDashboardStore((s) => s.findings);

  const refreshCampaigns = useCallback(() => {
    api.campaigns
      .list()
      .then((r) => setCampaigns(r.data ?? []))
      .catch(() => {});
  }, [setCampaigns]);

  useEffect(() => {
    refreshCampaigns();
  }, [refreshCampaigns]);

  /**
   * generateReportForCampaign — P5+ 报告闭环入口。复用 web/src/lib/api.ts
   * 中的 `api.reports.create`，与 /live 页的「≣ Generate Report」按钮
   * 走同一条 API。生成成功后 router.push 到详情页。
   */
  async function generateReportForCampaign(id: string, name?: string) {
    if (!id || id === "—" || generatingId) return;
    setGeneratingId(id);
    setToast(null);
    try {
      const r = await api.reports.create(id, name);
      router.push(`/reports/detail?id=${r.id}`);
    } catch (err) {
      setToast(
        `为 ${name ?? id.slice(0, 8)} 生成报告失败：${
          err instanceof Error ? err.message : String(err)
        }`,
      );
    } finally {
      setGeneratingId(null);
    }
  }

  async function handleStartCampaign(id: string) {
    setToast(null);
    try {
      await api.campaigns.start(id);
      refreshCampaigns();
      router.push(`/live?id=${id}`);
    } catch (err) {
      setToast(
        `启动任务失败：${err instanceof Error ? err.message : String(err)}`
      );
    }
  }

  const findingsByCampaign = useMemo(() => {
    const map: Record<string, number> = {};
    for (const f of findings) {
      map[f.campaign_id] = (map[f.campaign_id] ?? 0) + 1;
    }
    return map;
  }, [findings]);

  const severityByCampaign = useMemo(() => {
    const map: Record<string, Record<Severity, number>> = {};
    for (const f of findings) {
      const sev = severityFromFinding(f);
      const m = (map[f.campaign_id] ??= {
        critical: 0, high: 0, medium: 0, low: 0, informational: 0,
      });
      m[sev]++;
    }
    return map;
  }, [findings]);

  const filtered = useMemo(() => {
    return campaigns.filter((c) => {
      if (filter !== "all") {
        if (filter === "running" && !["running", "executing", "recon", "planning", "reporting", "classifying"].includes(String(c.status))) {
          return false;
        }
        if (filter === "complete" && !["complete", "completed"].includes(String(c.status))) {
          return false;
        }
        if (filter === "failed" && !["failed", "aborted"].includes(String(c.status))) {
          return false;
        }
      }
      if (query) {
        const q = query.toLowerCase();
        if (!`${c.target} ${c.objective} ${c.name ?? ""}`.toLowerCase().includes(q)) {
          return false;
        }
      }
      return true;
    });
  }, [campaigns, filter, query]);

  return (
    <div className="min-h-screen flex flex-col">
      <TopBar
        title="任务列表"
        subtitle={`共 ${campaigns.length} 个 · 当前 ${filtered.length} 个`}
        actions={
          <>
            <div className="hidden md:flex items-center bg-background/60 border border-border rounded-md overflow-hidden">
              <button
                onClick={() => setView("grid")}
                className={cn(
                  "px-3 py-1.5 font-mono text-[10px] uppercase tracking-widest",
                  view === "grid" ? "bg-accent/10 text-accent" : "text-text-muted"
                )}
                type="button"
              >
                ▦ 网格
              </button>
              <button
                onClick={() => setView("list")}
                className={cn(
                  "px-3 py-1.5 font-mono text-[10px] uppercase tracking-widest",
                  view === "list" ? "bg-accent/10 text-accent" : "text-text-muted"
                )}
                type="button"
              >
                ☰ 列表
              </button>
            </div>
            <button
              className="btn-cyber-solid"
              type="button"
              onClick={() => setShowCreateDialog(true)}
            >
              <span>+</span> 新建任务
            </button>
          </>
        }
      />

      {/* Filters */}
      <div className="px-6 py-3 border-b border-border bg-background/40 flex flex-wrap items-center gap-2">
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索目标 / 任务目标 / 名称…"
          className="input-cyber max-w-xs"
          spellCheck={false}
        />
        <FilterPill
          label="全部"
          active={filter === "all"}
          onClick={() => setFilter("all")}
        />
        <FilterPill
          label="运行中"
          active={filter === "running"}
          onClick={() => setFilter("running")}
        />
        <FilterPill
          label="已完成"
          active={filter === "complete"}
          onClick={() => setFilter("complete")}
        />
        <FilterPill
          label="失败"
          active={filter === "failed"}
          onClick={() => setFilter("failed")}
        />
      </div>

      <div className="p-6 animate-fade-in">
        {toast && (
          <div
            role="alert"
            className="mb-3 rounded border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-xs text-rose-200 font-mono"
          >
            {toast}
          </div>
        )}
        {filtered.length === 0 ? (
          <EmptyState
            motif={NO_CAMPAIGNS_MOTIF}
            title={campaigns.length === 0 ? "NO_CAMPAIGNS_FOUND" : "NO_MATCHES"}
            description={
              campaigns.length === 0
                ? "请通过「新建任务」按钮或 CLI 启动集群。"
                : "请尝试更换筛选条件或搜索关键字。"
            }
            action={
              <button
                className="btn-cyber-solid"
                type="button"
                onClick={() => setShowCreateDialog(true)}
              >
                <span>+</span> 启动新任务
              </button>
            }
          />
        ) : view === "grid" ? (
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
            {filtered.map((c) => (
              <CampaignCard
                key={c.id}
                campaign={c}
                findingsCount={findingsByCampaign[c.id] ?? 0}
                severityBreakdown={severityByCampaign[c.id]}
                onGenerateReport={generateReportForCampaign}
                generatingId={generatingId}
                onStartCampaign={handleStartCampaign}
              />
            ))}
          </div>
        ) : (
          <GlassPanel variant="soft" className="p-4" frame frameTag="campaigns.list">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-[10px] font-mono uppercase tracking-[0.18em] text-text-muted border-b border-border">
                  <th className="text-left py-2 px-3">目标</th>
                  <th className="text-left py-2 px-3">状态</th>
                  <th className="text-left py-2 px-3">模式</th>
                  <th className="text-right py-2 px-3">发现数</th>
                  <th className="text-right py-2 px-3">创建时间</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((c) => (
                  <tr
                    key={c.id}
                    onClick={() => (window.location.href = `/live?id=${c.id}`)}
                    className="border-b border-border/40 hover:bg-surface-hover/50 cursor-pointer"
                  >
                    <td className="py-3 px-3 font-medium">{c.target}</td>
                    <td className="py-3 px-3">
                      <span
                        className={cn(
                          "inline-flex items-center gap-1.5 px-1.5 py-0.5 text-[10px] font-mono uppercase tracking-widest rounded-sm",
                          STATUS_STYLES[c.status] ?? "status-paused"
                        )}
                      >
                        <span className="block w-1.5 h-1.5 rounded-full bg-current animate-pulse-soft" />
                        {c.status}
                      </span>
                    </td>
                    <td className="py-3 px-3 font-mono text-[11px] text-text-secondary uppercase">
                      {String(c.mode ?? "balanced")}
                    </td>
                    <td className="py-3 px-3 text-right font-mono tabular-nums">
                      {findingsByCampaign[c.id] ?? 0}
                    </td>
                    <td className="py-3 px-3 text-right font-mono text-[11px] text-text-muted">
                      {new Date(c.created_at).toLocaleString()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </GlassPanel>
        )}
      </div>

      <CreateCampaignDialog
        open={showCreateDialog}
        onClose={() => setShowCreateDialog(false)}
        onCreated={refreshCampaigns}
      />
    </div>
  );
}

function FilterPill({
  label,
  active,
  onClick,
}: {
  label: string;
  active?: boolean;
  onClick?: () => void;
}) {
  return (
    <button
      onClick={onClick}
      type="button"
      className={cn(
        "px-2.5 py-1 font-mono text-[10px] uppercase tracking-widest rounded-sm border",
        active
          ? "bg-accent/10 text-accent border-accent/40"
          : "text-text-secondary border-border hover:border-border-strong"
      )}
    >
      {label}
    </button>
  );
}

function CampaignCard({
  campaign,
  findingsCount,
  severityBreakdown,
  onGenerateReport,
  generatingId,
  onStartCampaign,
}: {
  campaign: Campaign;
  findingsCount: number;
  severityBreakdown?: Record<Severity, number>;
  onGenerateReport: (id: string, name?: string) => void;
  generatingId: string | null;
  onStartCampaign?: (id: string) => void;
}) {
  const statusClass = STATUS_STYLES[campaign.status] ?? "status-paused";
  const isPlanned = campaign.status === "planned";
  const isFailed = campaign.status === "failed";
  return (
    <GlassPanel
      variant="soft"
      className="group relative p-4 cursor-pointer hover:border-accent-2/40"
      onClick={() => {
        if (isPlanned) {
          // Navigate to live page so user can start the campaign
          window.location.href = `/live?id=${campaign.id}`;
          return;
        }
        if (isFailed) {
          // Navigate to live page so user can restart the campaign
          window.location.href = `/live?id=${campaign.id}`;
          return;
        }
        window.location.href = `/live?id=${campaign.id}`;
      }}
    >
      <div className="flex items-start justify-between gap-2 mb-2">
        <div className="min-w-0">
          <p className="font-mono text-[10px] uppercase tracking-widest text-text-muted truncate">
            {String(campaign.id).slice(0, 8)}
          </p>
          <h3 className="font-display text-base font-semibold text-text-primary truncate">
            {campaign.target}
          </h3>
        </div>
        <span
          className={cn(
            "inline-flex items-center gap-1.5 px-1.5 py-0.5 text-[10px] font-mono uppercase tracking-widest rounded-sm",
            statusClass
          )}
        >
          <span className="block w-1.5 h-1.5 rounded-full bg-current animate-pulse-soft" />
          {campaign.status}
        </span>
      </div>
      <p className="text-xs text-text-secondary line-clamp-2 mb-3">
        {campaign.objective}
      </p>
      <div className="flex items-center gap-2 flex-wrap text-[10px] font-mono">
        <span className="px-1.5 py-0.5 rounded-sm bg-background/60 border border-border text-text-secondary uppercase tracking-widest">
          mode: {String(campaign.mode ?? "balanced")}
        </span>
        <span className="px-1.5 py-0.5 rounded-sm bg-background/60 border border-border text-text-secondary uppercase tracking-widest">
          findings: {findingsCount}
        </span>
      </div>
      {severityBreakdown && (
        <div className="mt-3 flex items-center gap-1.5 flex-wrap">
          {(["critical", "high", "medium", "low", "informational"] as Severity[]).map(
            (s) =>
              severityBreakdown[s] > 0 && (
                <SeverityBadge
                  key={s}
                  severity={s}
                  label={String(severityBreakdown[s])}
                />
              )
          )}
        </div>
      )}
      <p className="text-[10px] font-mono text-text-muted mt-3 pt-3 border-t border-border/60">
        {new Date(campaign.created_at).toLocaleString()}
      </p>
      {isPlanned && (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onStartCampaign?.(campaign.id);
          }}
          className="mt-3 w-full rounded-sm border border-emerald-500/40 bg-emerald-500/10 px-2 py-2 text-xs font-mono uppercase tracking-wide text-emerald-400 hover:bg-emerald-500/20 transition-colors"
          title="启动此任务"
        >
          ▶ 启动任务
        </button>
      )}
      {isFailed && (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onStartCampaign?.(campaign.id);
          }}
          className="mt-3 w-full rounded-sm border border-amber-500/40 bg-amber-500/10 px-2 py-2 text-xs font-mono uppercase tracking-wide text-amber-400 hover:bg-amber-500/20 transition-colors"
          title="重新启动此任务"
        >
          ↻ 重新启动
        </button>
      )}
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          onGenerateReport(campaign.id, campaign.name);
        }}
        disabled={generatingId === campaign.id || campaign.id === "—"}
        className="absolute top-2 right-2 rounded-sm border border-accent/40 bg-accent/10 px-2 py-1 text-[10px] font-mono uppercase tracking-wide text-accent hover:bg-accent/20 transition-colors disabled:opacity-50 disabled:cursor-wait opacity-0 group-hover:opacity-100"
        title="为该任务生成 Markdown 报告"
      >
        {generatingId === campaign.id ? "生成中…" : "≣ 报告"}
      </button>
    </GlassPanel>
  );
}
