"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, type Campaign } from "@/lib/api";
import { useDashboardStore } from "@/lib/store";
import { SeverityChart } from "@/components/SeverityChart";
import { SeverityBadge } from "@/components/SeverityBadge";
import { GlassPanel } from "@/components/GlassPanel";
import { TopBar } from "@/components/TopBar";
import { EmptyState, NO_CAMPAIGNS_MOTIF } from "@/components/EmptyState";
import { CreateCampaignDialog } from "@/components/CreateCampaignDialog";
import { cn } from "@/lib/cn";
import { severityFromFinding, type Severity } from "@/lib/severity";

interface Stats {
  campaigns: number;
  active_campaigns: number;
  total_findings: number;
  by_agent?: Record<string, number>;
}

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

const AGENTS = [
  { key: "orchestrator", name: "编排代理", role: "规划与协调" },
  { key: "recon", name: "侦察代理", role: "子域名探测 · HTTP 探测 · 漏洞扫描 · 端口扫描" },
  { key: "classifier", name: "分类代理", role: "CVE 匹配 · CVSS 评分 · 误报过滤" },
  { key: "exploit", name: "利用代理", role: "攻击链构建" },
  { key: "report", name: "报告代理", role: "PDF · HTML · Markdown · JSON" },
];

export default function DashboardPage() {
  const [stats, setStats] = useState<Stats>({
    campaigns: 0,
    active_campaigns: 0,
    total_findings: 0,
  });
  const [loading, setLoading] = useState(true);
  const [showCreateDialog, setShowCreateDialog] = useState(false);

  const findings = useDashboardStore((s) => s.findings);
  const severityCounts = useDashboardStore((s) => s.severityCounts);
  const campaigns = useDashboardStore((s) => s.campaigns);
  const setCampaigns = useDashboardStore((s) => s.setCampaigns);
  const setActiveCampaignId = useDashboardStore((s) => s.setActiveCampaignId);
  const agentStatuses = useDashboardStore((s) => s.agentStatuses);

  const refreshCampaigns = useCallback(() => {
    api.campaigns
      .list()
      .then((r) => {
        const list = r.data ?? [];
        setCampaigns(list);
        setActiveCampaignId(list[0]?.id ?? null);
      })
      .catch(() => {});
  }, [setCampaigns, setActiveCampaignId]);

  const donutCounts = useMemo(
    () => ({
      critical: severityCounts.critical ?? 0,
      high: severityCounts.high ?? 0,
      medium: severityCounts.medium ?? 0,
      low: severityCounts.low ?? 0,
      informational: severityCounts.informational ?? 0,
    }),
    [severityCounts]
  );

  useEffect(() => {
    const refresh = () => {
      api.stats()
        .then((s) => {
          const next: Stats = {
            campaigns: Number(s.campaigns ?? 0),
            active_campaigns: Number(s.active_campaigns ?? 0),
            total_findings: Number(s.total_findings ?? 0),
          };
          setStats(next);
          setLoading(false);
        })
        .catch(() => { setLoading(false); });
      refreshCampaigns();
    };
    refresh();
    const interval = setInterval(refresh, 5000);
    return () => clearInterval(interval);
  }, [refreshCampaigns]);

  const findingsByCampaign = useMemo(() => {
    const map: Record<string, number> = {};
    for (const f of findings) {
      map[f.campaign_id] = (map[f.campaign_id] ?? 0) + 1;
    }
    return map;
  }, [findings]);

  const recentFindings = useMemo(() => {
    return [...findings]
      .sort(
        (a, b) =>
          new Date(b.created_at ?? 0).getTime() -
          new Date(a.created_at ?? 0).getTime()
      )
      .slice(0, 6);
  }, [findings]);

  return (
    <div className="min-h-screen flex flex-col">
      <TopBar
        title="任务指挥中心"
        subtitle="集群活动概览 · 实时数据流已启用"
        actions={
          <>
            <button className="btn-ghost hidden md:inline-flex" type="button" onClick={() => setShowCreateDialog(true)}>
              ⟨/⟩ 导入目标
            </button>
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

      <div className="p-6 space-y-6 animate-fade-in">
        {/* KPI Cards */}
        <section
          aria-label="关键指标"
          className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4"
        >
          {loading ? (
            <>
              {[1,2,3,4].map(i => (
                <div key={i} className="rounded-lg border border-border bg-background/40 p-4 space-y-2 animate-pulse">
                  <div className="h-3 w-16 bg-border/50 rounded" />
                  <div className="h-7 w-12 bg-border/50 rounded" />
                </div>
              ))}
            </>
          ) : (
          <>
          <KPICard
            label="任务总数"
            value={String(stats.campaigns)}
            hint="累计"
            accent="accent"
          />
          <KPICard
            label="活跃扫描"
            value={String(stats.active_campaigns)}
            hint={stats.active_campaigns > 0 ? "运行中" : "空闲"}
            accent={stats.active_campaigns > 0 ? "accent" : "muted"}
            pulse={stats.active_campaigns > 0}
          />
          <KPICard
            label="发现总数"
            value={String(findings.length || stats.total_findings)}
            hint="实时流入"
            accent="severity-critical"
          />
          <KPICard
            label="在线代理"
            value={`${
              Object.values(agentStatuses).filter((s) => s !== "idle").length
            }/5`}
            hint="编排 + 4 个专项代理"
            accent="accent-2"
          />
          </>)}
        </section>

        {/* Campaigns + Quick chart row */}
        <section className="grid grid-cols-1 xl:grid-cols-3 gap-4">
          <GlassPanel
            variant="soft"
            className="xl:col-span-2 p-4"
            frame
            frameTag="campaigns.list"
          >
            <div className="flex items-center justify-between mb-4">
              <h3 className="font-display text-base font-semibold text-text-primary">
                活跃任务
              </h3>
              <Link
                href="/campaigns"
                className="text-[10px] font-mono uppercase tracking-[0.2em] text-accent hover:text-accent-2"
              >
                查看全部 ▸
              </Link>
            </div>

            {campaigns.length === 0 ? (
              <EmptyState
                motif={NO_CAMPAIGNS_MOTIF}
                title="NO_CAMPAIGNS_FOUND"
                description="暂无已注册任务。请点击「新建任务」按钮或通过 CLI 启动集群。"
                action={
                  <button
                    className="btn-cyber-solid"
                    type="button"
                    onClick={() => setShowCreateDialog(true)}
                  >
                    <span>+</span> 启动首个任务
                  </button>
                }
              />
            ) : (
              <div className="overflow-x-auto">
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
                    {campaigns.map((c) => (
                      <CampaignRow
                        key={c.id}
                        campaign={c}
                        findingsCount={findingsByCampaign[c.id] ?? 0}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </GlassPanel>

          <GlassPanel variant="soft" className="p-4" frame frameTag="severity">
            <h3 className="font-display text-base font-semibold text-text-primary mb-4">
              严重度分布
            </h3>
            <SeverityChart counts={donutCounts} />
          </GlassPanel>
        </section>

        {/* Agents + Live findings */}
        <section className="grid grid-cols-1 xl:grid-cols-3 gap-4">
          <GlassPanel variant="soft" className="p-4 xl:col-span-1" frame frameTag="agents.swarm">
            <h3 className="font-display text-base font-semibold text-text-primary mb-4">
              代理集群
            </h3>
            <div className="space-y-2">
              {AGENTS.map((a) => (
                <AgentRow
                  key={a.key}
                  name={a.name}
                  role={a.role}
                  status={agentStatuses[a.key] ?? "idle"}
                />
              ))}
            </div>
          </GlassPanel>

          <GlassPanel variant="soft" className="p-4 xl:col-span-2" frame frameTag="findings.live">
            <div className="flex items-center justify-between mb-4">
              <h3 className="font-display text-base font-semibold text-text-primary">
                实时发现流
              </h3>
              <Link
                href="/findings"
                className="text-[10px] font-mono uppercase tracking-[0.2em] text-accent hover:text-accent-2"
              >
                完整浏览器 ▸
              </Link>
            </div>
            {recentFindings.length === 0 ? (
              <p className="font-mono text-xs text-text-muted py-6 text-center">
                正在等待集群的第一条发现…
              </p>
            ) : (
              <ul className="space-y-2">
                {recentFindings.map((f) => (
                  <FindingListItem
                    key={f.id}
                    type={f.type || "unknown"}
                    target={f.target}
                    agent={f.agent_name}
                    severity={severityFromFinding(f)}
                    when={f.created_at}
                  />
                ))}
              </ul>
            )}
          </GlassPanel>
        </section>
      </div>

      <CreateCampaignDialog
        open={showCreateDialog}
        onClose={() => setShowCreateDialog(false)}
        onCreated={refreshCampaigns}
      />
    </div>
  );
}

/* ------------------------------------------------------------------ */
/* Local components                                                    */
/* ------------------------------------------------------------------ */

function KPICard({
  label,
  value,
  hint,
  accent,
  pulse,
}: {
  label: string;
  value: string;
  hint?: string;
  accent: "accent" | "accent-2" | "severity-critical" | "muted";
  pulse?: boolean;
}) {
  const colorClass = {
    accent: "text-accent",
    "accent-2": "text-accent-2",
    "severity-critical": "text-severity-critical",
    muted: "text-text-secondary",
  }[accent];

  return (
    <GlassPanel
      variant="soft"
      className="p-4"
      stripe={
        accent === "severity-critical"
          ? "var(--severity-critical)"
          : accent === "accent-2"
            ? "var(--accent-2)"
            : "var(--accent)"
      }
    >
      <p className="text-[10px] font-mono uppercase tracking-[0.18em] text-text-muted">
        {label}
      </p>
      <div className="flex items-baseline gap-2 mt-2">
        <p
          className={cn(
            "font-mono text-3xl font-bold tabular-nums",
            colorClass
          )}
        >
          {value}
        </p>
        {pulse && (
          <span className="status-dot-online" aria-label="运行中" />
        )}
      </div>
      {hint && (
        <p className="text-[10px] font-mono uppercase tracking-[0.18em] text-text-muted mt-1">
          {hint}
        </p>
      )}
    </GlassPanel>
  );
}

function CampaignRow({
  campaign,
  findingsCount,
}: {
  campaign: Campaign;
  findingsCount: number;
}) {
  const statusClass = STATUS_STYLES[campaign.status] ?? "status-paused";
  return (
    <tr
      onClick={() => (window.location.href = `/live?id=${campaign.id}`)}
      className="border-b border-border/40 hover:bg-surface-hover/50 transition-colors cursor-pointer"
    >
      <td className="py-3 px-3 font-medium text-text-primary">
        {campaign.target}
      </td>
      <td className="py-3 px-3">
        <span
          className={cn(
            "inline-flex items-center gap-1.5 px-1.5 py-0.5 text-[10px] font-mono uppercase tracking-widest rounded-sm",
            statusClass
          )}
        >
          <span className="block w-1.5 h-1.5 rounded-full bg-current animate-pulse-soft" />
          {campaign.status}
        </span>
      </td>
      <td className="py-3 px-3 font-mono text-[11px] text-text-secondary uppercase tracking-wider">
        {String(campaign.mode ?? "balanced")}
      </td>
      <td className="py-3 px-3 text-right font-mono tabular-nums text-text-primary">
        {findingsCount}
      </td>
      <td className="py-3 px-3 text-right font-mono text-[11px] text-text-muted">
        {new Date(campaign.created_at).toLocaleString()}
      </td>
    </tr>
  );
}

function AgentRow({
  name,
  role,
  status,
}: {
  name: string;
  role: string;
  status: "idle" | "active" | "complete" | "error";
}) {
  const dotClass = {
    active: "status-dot-online",
    idle: "status-dot-idle",
    complete: "bg-emerald-500 shadow-[0_0_8px_rgba(16,185,129,0.6)]",
    error: "status-dot-error",
  }[status];

  return (
    <div
      className={cn(
        "flex items-center gap-3 py-2 px-3 rounded-md border bg-background/60",
        status === "active"
          ? "border-accent/40 agent-active"
          : "border-border"
      )}
    >
      <span className={dotClass} />
      <div className="flex-1 min-w-0">
        <p className="text-sm font-medium text-text-primary">{name}</p>
        <p className="text-[10px] font-mono text-text-muted truncate">
          {role}
        </p>
      </div>
      <span className="text-[10px] font-mono uppercase tracking-widest text-text-muted">
        {status}
      </span>
    </div>
  );
}

function FindingListItem({
  type,
  target,
  agent,
  severity,
  when,
}: {
  type: string;
  target: string;
  agent?: string;
  severity: Severity;
  when?: string;
}) {
  return (
    <li className="flex items-start gap-3 py-2 px-3 rounded-md hover:bg-surface-hover/40 transition-colors">
      <SeverityBadge severity={severity} />
      <div className="flex-1 min-w-0">
        <p className="text-sm text-text-primary truncate">
          <span className="font-mono text-text-muted text-xs mr-2">
            {type}
          </span>
          {target}
        </p>
        <p className="text-[10px] font-mono uppercase tracking-widest text-text-muted mt-0.5">
          {agent ? `agent=${agent}` : "agent=—"}
          {when ? ` · ${new Date(when).toLocaleTimeString()}` : ""}
        </p>
      </div>
    </li>
  );
}
