"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import dynamic from "next/dynamic";
import {
  api,
  connectWebSocket,
  type CampaignEvent,
  type Finding,
  type UserMessage,
} from "@/lib/api";
import { useDashboardStore } from "@/lib/store";
import {
  SEVERITY_COLOR,
  SEVERITY_LABEL,
  severityFromFinding,
  type Severity,
} from "@/lib/severity";
import { GlassPanel } from "@/components/GlassPanel";
import { SeverityBadge } from "@/components/SeverityBadge";
import { TopBar } from "@/components/TopBar";
import { UserInputDock, formatUserInputAsAnsi } from "@/components/UserInputDock";
import { cn } from "@/lib/cn";

/**
 * P5+ 终端 — xterm.js wrapper is ~150 KB gzip. We only pay that
 * cost on /live (the only page that needs a real terminal), and
 * we delay it until after hydration to keep the dashboard static.
 */
const Terminal = dynamic(
  () => import("@/components/Terminal").then((m) => m.Terminal),
  {
    ssr: false,
    loading: () => (
      <div className="px-4 py-6 text-sm text-text-muted font-mono">
        <span className="text-accent">▸</span> 正在启动 xterm.js
        <span className="animate-blink-cursor">▌</span>
      </div>
    ),
  },
);
import { formatEventAsAnsi } from "@/components/Terminal";

const PHASES = [
  { key: "initializing", label: "初始化" },
  { key: "recon", label: "侦察" },
  { key: "classify", label: "分类" },
  { key: "plan", label: "规划" },
  { key: "execute", label: "执行" },
  { key: "report", label: "报告" },
  { key: "complete", label: "完成" },
] as const;
type Phase = (typeof PHASES)[number]["key"];

const PHASE_LABELS: Record<Phase, string> = {
  initializing: "初始化",
  recon: "侦察",
  classify: "分类",
  plan: "规划",
  execute: "执行",
  report: "报告",
  complete: "完成",
};

function phaseOrder(p: Phase): number {
  return PHASES.findIndex((x) => x.key === p);
}

const AGENTS: { key: string; name: string; role: string }[] = [
  { key: "orchestrator", name: "编排器", role: "规划与协调" },
  { key: "recon", name: "侦察代理", role: "子域名探测 · HTTP · 漏洞" },
  { key: "classifier", name: "分类器", role: "CVE · CVSS · 误报" },
  { key: "exploit", name: "利用代理", role: "攻击链" },
  { key: "report", name: "报告代理", role: "报告输出" },
];

/** Map a phase key to the agent key that owns it. */
function phaseAgent(phase: Phase): string | null {
  switch (phase) {
    case "initializing": return "orchestrator";
    case "recon": return "recon";
    case "classify": return "classifier";
    case "plan":
    case "execute": return "exploit";
    case "report": return "report";
    case "complete": return null;
  }
}

/** Infer the current phase from a state_change event detail string. */
function inferPhase(detail: string): Phase {
  const d = (detail || "").toLowerCase();
  return d.includes("recon")
    ? "recon"
    : d.includes("classif")
      ? "classify"
      : d.includes("plan")
        ? "plan"
        : d.includes("execut")
          ? "execute"
          : d.includes("report")
            ? "report"
            : d.includes("complete")
              ? "complete"
              : "initializing";
}

/**
 * LiveOps — the live operations dashboard. Reads the active campaign
 * id from the URL (?id=…) or falls back to whatever is currently
 * selected in the dashboard store. Rendered as a static page so it
 * can co-exist with the project's `output: export` config.
 */
export default function LiveOpsPage() {
  // useSearchParams() forces a CSR bailout in `output: export`. We
  // wrap the whole body in Suspense so the static export can finish
  // (the fallback is the skeleton we already show below).
  return (
    <Suspense fallback={<BootSkeleton />}>
      <LiveOpsInner />
    </Suspense>
  );
}

function BootSkeleton() {
  return (
    <div className="min-h-screen flex items-center justify-center">
      <div className="font-mono text-xs uppercase tracking-widest text-text-muted">
        <span className="text-accent">▸</span> 正在连接集群
        <span className="animate-blink-cursor">▌</span>
      </div>
    </div>
  );
}

function LiveOpsInner() {
  const search = useSearchParams();
  const router = useRouter();
  const idFromUrl = search.get("id") || search.get("campaign");
  const activeId = useDashboardStore((s) => s.activeCampaignId);
  const [autoId, setAutoId] = useState<string | null>(null);

  // When no ID is provided via URL or store, auto-select the latest campaign
  useEffect(() => {
    if (idFromUrl || activeId) return;
    api.campaigns.list().then((res) => {
      const campaigns = res.data ?? [];
      if (campaigns.length > 0) {
        // Pick the most recent campaign (sorted by created_at desc)
        const latest = campaigns.reduce((a, b) =>
          (b.created_at || "") > (a.created_at || "") ? b : a
        );
        setAutoId(latest.id);
      }
    }).catch(() => {});
  }, [idFromUrl, activeId]);

  const id = idFromUrl || activeId || autoId || "—";

  const events = useDashboardStore((s) => s.events);
  const findings = useDashboardStore((s) => s.findings);
  const userInputs = useDashboardStore((s) => s.userInputs);
  const addEvent = useDashboardStore((s) => s.addEvent);
  const setEvents = useDashboardStore((s) => s.setEvents);
  const addFinding = useDashboardStore((s) => s.addFinding);
  const setFindings = useDashboardStore((s) => s.setFindings);
  const addUserInput = useDashboardStore((s) => s.addUserInput);
  const clearEvents = useDashboardStore((s) => s.clearEvents);
  const setAgentStatus = useDashboardStore((s) => s.setAgentStatus);

  const [phase, setPhase] = useState<Phase>("initializing");
  const [elapsed, setElapsed] = useState(0);
  const [filter, setFilter] = useState<Severity | null>(null);
  const [generating, setGenerating] = useState(false);

  async function generateReport() {
    if (id === "—" || generating) return;
    setGenerating(true);
    try {
      const r = await api.reports.create(id);
      router.push(`/reports/detail?id=${r.id}`);
    } catch {
      // surface in console; user can retry from /reports
      setGenerating(false);
    }
  }

  useEffect(() => {
    clearEvents();
  }, [clearEvents]);

  useEffect(() => {
    const interval = setInterval(() => setElapsed((e) => e + 1), 1000);
    return () => clearInterval(interval);
  }, []);

  // Load historical findings when the campaign changes.
  useEffect(() => {
    if (id === "—") return;
    let cancelled = false;
    api.findings
      .list(id)
      .then((res) => {
        if (cancelled) return;
        setFindings(res.data ?? []);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [id, setFindings]);

  // Load historical events + subscribe to WebSocket for live updates.
  // Without the initial REST load, the terminal is empty when navigating
  // to a campaign that has already started.
  useEffect(() => {
    if (id === "—") return;
    let cancelled = false;

    /** Update phase and agent statuses based on a state_change event. */
    function handleStateChange(detail: string) {
      const next = inferPhase(detail);
      setPhase(next);

      // Mark the agent that owns the new phase as active, and mark
      // all agents from earlier phases as complete.
      const newAgent = phaseAgent(next);
      const newOrder = phaseOrder(next);
      for (const a of AGENTS) {
        const aOrder = PHASES.findIndex((p) => p.key === a.key || phaseAgent(p.key as Phase) === a.key);
        if (a.key === newAgent) {
          setAgentStatus(a.key, "active");
        } else if (aOrder !== -1 && aOrder < newOrder) {
          setAgentStatus(a.key, "complete");
        }
      }

      // When campaign completes, mark all agents complete
      if (next === "complete") {
        for (const a of AGENTS) {
          setAgentStatus(a.key, "complete");
        }
      }
    }

    // 1. Load historical events via REST so the terminal is populated
    //    immediately, even for campaigns that started before the page
    //    was opened.
    api.events
      .list(id)
      .then((res) => {
        if (cancelled) return;
        const events = res.data ?? [];
        for (const ev of events) {
          addEvent(ev);
          if (ev.event_type === "state_change") {
            handleStateChange(ev.detail || "");
          }
        }
      })
      .catch(() => {});

    // 2. Subscribe to WebSocket for real-time updates
    const ws = connectWebSocket(id, {
      onSnapshot: (snapshotEvents: CampaignEvent[]) => {
        // Batch-merge the initial snapshot into the store to avoid
        // the gap between REST fetch and WS subscribe.
        setEvents(snapshotEvents);
        // Infer current phase from the latest state_change event
        for (const ev of snapshotEvents) {
          if (ev.event_type === "state_change") {
            handleStateChange(ev.detail || "");
          }
        }
      },
      onEvent: (event: CampaignEvent) => {
        addEvent(event);
        if (event.event_type === "state_change") {
          handleStateChange(event.detail || "");
        }
        // Update agent status based on events
        if (event.agent_name && event.agent_name !== "engine" && event.agent_name !== "system") {
          const agentName = event.agent_name;
          if (event.event_type === "tool_call" || event.event_type === "thought") {
            setAgentStatus(agentName, "active");
          } else if (event.event_type === "tool_result" || event.event_type === "finding_discovered") {
            setAgentStatus(agentName, "active");
          } else if (event.event_type === "error") {
            setAgentStatus(agentName, "error");
          }
        }
      },
      onFinding: (finding: Finding) => {
        addFinding(finding);
      },
      onUserInput: (msg: UserMessage) => {
        addUserInput(msg);
      },
      onReconnect: () => {
        // Re-fetch missed events and findings after a WebSocket reconnect
        if (!id || id === "—") return;
        api.events.list(id).then((r) => {
          const events = r.data ?? [];
          for (const e of events) addEvent(e);
        }).catch(() => {});
        api.findings.list(id).then((r) => {
          const findings = r.data ?? [];
          setFindings(findings);
        }).catch(() => {});
      },
    });
    return () => {
      cancelled = true;
      ws.close();
    };
  }, [id, addEvent, setEvents, addFinding, addUserInput, setAgentStatus]);

  /**
   * Pre-render the most recent N events as ANSI-colored lines for
   * the xterm.js terminal. We cap the buffer at the last 2,000
   * events to keep the terminal responsive (5000-line scrollback
   * gives xterm a comfortable headroom for search).
   *
   * P5+ 用户输入闭环 — user-issued messages are interleaved
   * into the terminal feed so the operator sees their own voice
   * "in line" with the swarm's output. We use a stable key
   * (`evt:<id>` for events, `usr:<id>` for user messages) so the
   * React xterm driver can dedupe across re-renders.
   */
  const terminalLines = useMemo(() => {
    type Tagged =
      | { kind: "evt"; ts: number; id: string; line: string }
      | { kind: "usr"; ts: number; id: string; line: string };
    const tagged: Tagged[] = [];
    for (const e of events) {
      const ts = new Date(e.timestamp ?? 0).getTime();
      tagged.push({
        kind: "evt",
        ts,
        id: `${e.event_type ?? "evt"}-${ts}-${e.detail?.length ?? 0}`,
        line: formatEventAsAnsi(e),
      });
    }
    for (const m of userInputs) {
      tagged.push({
        kind: "usr",
        ts: new Date(m.timestamp).getTime(),
        id: m.id,
        line: formatUserInputAsAnsi(m),
      });
    }
    tagged.sort((a, b) => a.ts - b.ts);
    return tagged.slice(-2000).map((t) => t.line);
  }, [events, userInputs]);

  const visibleFindings = useMemo(() => {
    const mine = findings.filter((f) => f.campaign_id === id);
    const filtered = filter
      ? mine.filter((f) => severityFromFinding(f) === filter)
      : mine;
    return filtered.slice(-50).reverse();
  }, [findings, id, filter]);

  const counts = useMemo(() => {
    const c: Record<Severity, number> = {
      critical: 0, high: 0, medium: 0, low: 0, informational: 0,
    };
    for (const f of findings) {
      if (f.campaign_id !== id) continue;
      c[severityFromFinding(f)]++;
    }
    return c;
  }, [findings, id]);

  const formatTime = (s: number) =>
    `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;

  const progress = phase === "initializing"
    ? 0
    : Math.round((phaseOrder(phase) / (PHASES.length - 1)) * 100);

  return (
    <div className="min-h-screen flex flex-col">
      <TopBar
        title={`实时操作 ${id === "—" ? "" : `· ${String(id).slice(0, 8)}`}`}
        subtitle={`阶段=${PHASE_LABELS[phase]} · 耗时=${formatTime(elapsed)} · WebSocket=自动`}
        actions={
          <>
            <span className="hidden md:inline-flex items-center gap-2 px-3 py-1.5 rounded-md border border-border bg-background/60 font-mono text-[11px] text-text-secondary">
              <span className={phase === "complete" ? "status-dot-offline" : "status-dot-online"} /> WebSocket {phase === "complete" ? "已断开" : "已连接"}
            </span>
            <button
              type="button"
              onClick={generateReport}
              disabled={id === "—" || generating}
              className="btn-cyber"
              title="根据当前任务的发现生成 Markdown 报告"
            >
              {generating ? "⏳ 正在生成…" : "≣ 生成报告"}
            </button>
            <button
              type="button"
              onClick={() => api.campaigns.stop(id).catch(() => {})}
              className="btn-cyber border-rose-500/50 text-rose-400 hover:bg-rose-500/10 hover:border-rose-400 hover:text-rose-300"
            >
              ⏹ 停止
            </button>
          </>
        }
      />

      {/* Phase progress strip */}
      <div className="px-6 py-3 border-b border-border bg-background/40 flex items-center gap-4 flex-wrap">
        <div className="flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-widest text-text-muted">
          {PHASES.filter((p) => p.key !== "initializing").map((p) => (
            <PhaseStep
              key={p.key}
              label={p.label}
              active={phase === p.key}
              done={phaseOrder(phase) >= phaseOrder(p.key) && phase !== "initializing"}
            />
          ))}
        </div>
        <div className="flex-1 min-w-[180px]">
          <ProgressBar value={progress} />
        </div>
        <span className="font-mono text-[11px] text-text-secondary tabular-nums">
          [{progress}%]
        </span>
      </div>

      {/* 3-panel grid */}
      <div className="flex-1 grid grid-cols-1 lg:grid-cols-12 gap-0 overflow-hidden">
        {/* Left: agents */}
        <GlassPanel
          variant="soft"
          className="lg:col-span-3 p-4 m-3 mr-1.5 overflow-y-auto"
          frame
          frameTag="agents.swarm"
        >
          <h3 className="text-[10px] font-mono uppercase tracking-[0.2em] text-text-muted mb-3">
            ◤ 代理集群
          </h3>
          <AgentSwarm />
        </GlassPanel>

        {/* Center: terminal event stream */}
        <GlassPanel
          variant="soft"
          className="lg:col-span-6 p-0 m-3 mx-1.5 overflow-hidden crt-scanlines"
          frame
          frameTag="events.stream"
        >
          <div className="flex items-center justify-between px-4 py-2 border-b border-border bg-background/40">
            <h3 className="text-[10px] font-mono uppercase tracking-[0.2em] text-text-muted">
              ◤ 事件流{" "}
              <span className="text-text-secondary">[{events.length}]</span>
            </h3>
            <span className="text-[9px] font-mono text-text-muted uppercase tracking-widest">
              p5+ · xterm
            </span>
          </div>
          <div className="h-[calc(100%-36px)] min-h-[360px]">
            <Terminal
              lines={terminalLines}
              autoFit
              testId="live.terminal"
            />
          </div>
        </GlassPanel>

        {/* Right: findings */}
        <GlassPanel
          variant="soft"
          className="lg:col-span-3 p-4 m-3 ml-1.5 overflow-y-auto"
          frame
          frameTag="findings.live"
        >
          <h3 className="text-[10px] font-mono uppercase tracking-[0.2em] text-text-muted mb-3">
            ◤ 发现 <span className="text-text-secondary">[{visibleFindings.length}]</span>
          </h3>

          <div className="flex gap-2 mb-4 flex-wrap" role="group" aria-label="严重度筛选">
            {(Object.keys(counts) as Severity[]).map((sev) => {
              const active = filter === sev;
              return (
                <button
                  key={sev}
                  type="button"
                  onClick={() => setFilter(active ? null : sev)}
                  className={cn(
                    "flex flex-col items-center px-2 py-1 rounded border transition-colors",
                    active
                      ? "border-accent bg-accent/10"
                      : "border-transparent hover:border-border"
                  )}
                  aria-pressed={active}
                >
                  <div
                    className="w-7 h-7 rounded flex items-center justify-center font-mono text-[11px] font-bold"
                    style={{
                      backgroundColor: `${SEVERITY_COLOR[sev]}22`,
                      color: SEVERITY_COLOR[sev],
                    }}
                  >
                    {counts[sev]}
                  </div>
                  <span className="text-[9px] font-mono uppercase tracking-widest text-text-muted mt-1">
                    {SEVERITY_LABEL[sev][0]}
                  </span>
                </button>
              );
            })}
          </div>

          <div className="space-y-2">
            {visibleFindings.map((f) => {
              const sev = severityFromFinding(f);
              return <FindingRow key={f.id} finding={f} severity={sev} />;
            })}
            {visibleFindings.length === 0 && (
              <p className="text-xs font-mono text-text-muted">
                {filter
                  ? `暂无 ${SEVERITY_LABEL[filter]} 级发现`
                  : "暂无发现"}
              </p>
            )}
          </div>
        </GlassPanel>
      </div>

      {/* Footer metrics */}
      <div className="border-t border-border bg-background/60 px-6 py-2 flex items-center gap-6 flex-wrap text-[11px] font-mono text-text-muted">
        <span>
          发现数：{" "}
          <strong className="text-text-primary">{visibleFindings.length}</strong>
        </span>
        <span>
          事件数：{" "}
          <strong className="text-text-primary">{events.length}</strong>
        </span>
        <span>
          操作员输入：{" "}
          <strong className="text-cyan-400">{userInputs.length}</strong>
        </span>
        <span>
          耗时：{" "}
          <strong className="text-text-primary">{formatTime(elapsed)}</strong>
        </span>
        <span>
          阶段：{" "}
          <strong className="text-accent">{PHASE_LABELS[phase]}</strong>
        </span>
        <span className="ml-auto text-[10px] uppercase tracking-widest opacity-70">
          /api/v1/campaigns/{String(id).slice(0, 8)}/ws
        </span>
      </div>

      {/* P5+ 用户输入闭环 — chat-style operator input dock.
          Sits below the footer metrics so it stays in view on
          long pages and is the natural next step for any
          operator already watching the terminal. */}
      <UserInputDock campaignId={id === "—" ? null : id} />
    </div>
  );
}

function PhaseStep({
  label,
  active,
  done,
}: {
  label: string;
  active?: boolean;
  done?: boolean;
}) {
  if (done)
    return (
      <span className="px-2 py-0.5 rounded-sm bg-emerald-500/10 text-emerald-400 border border-emerald-500/30">
        {label} ✓
      </span>
    );
  if (active)
    return (
      <span className="px-2 py-0.5 rounded-sm bg-accent/10 text-accent border border-accent/40 animate-pulse-soft">
        {label}
      </span>
    );
  return <span className="px-2 py-0.5 text-text-muted">{label}</span>;
}

function ProgressBar({ value }: { value: number }) {
  const filled = Math.max(0, Math.min(20, Math.round((value / 100) * 20)));
  return (
    <div
      className="font-mono text-[11px] text-accent select-none"
      aria-label={`${value}% complete`}
    >
      [{Array(filled).fill("█").join("")}
      {Array(20 - filled).fill("░").join("")}]
    </div>
  );
}

function AgentSwarm() {
  const statuses = useDashboardStore((s) => s.agentStatuses);

  return (
    <div className="space-y-2">
      {AGENTS.map((a) => {
        const status = statuses[a.key] ?? "idle";
        const dot = {
          active: "status-dot-online",
          idle: "status-dot-idle",
          complete: "bg-emerald-500 shadow-[0_0_8px_rgba(16,185,129,0.6)]",
          error: "status-dot-error",
        }[status];
        return (
          <div
            key={a.key}
            className={cn(
              "block w-full text-left rounded-md border p-2.5 bg-background/60",
              status === "active"
                ? "border-accent/40 agent-active"
                : "border-border"
            )}
            aria-label={`${a.name} status: ${status}`}
          >
            <div className="flex items-center gap-2">
              <span className={dot} />
              <span className="text-xs font-medium text-text-primary">
                {a.name}
              </span>
            </div>
            <p className="text-[10px] font-mono text-text-muted mt-1 truncate">
              {a.role}
            </p>
          </div>
        );
      })}
    </div>
  );
}

function FindingRow({
  finding,
  severity,
}: {
  finding: Finding;
  severity: Severity;
}) {
  const [expanded, setExpanded] = useState(false);
  const title = describeFinding(finding);
  const sev = severityFromFinding(finding);

  return (
    <div
      className={cn(
        "rounded-md border transition-colors cursor-pointer",
        expanded ? "border-accent/40 bg-accent/5" : "border-transparent hover:border-border hover:bg-background/40"
      )}
      onClick={() => setExpanded(!expanded)}
      role="button"
      tabIndex={0}
      aria-expanded={expanded}
      onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setExpanded(!expanded); } }}
    >
      <div className="flex items-start gap-2 text-xs py-1.5 px-2">
        <SeverityBadge severity={severity} />
        <div className="flex-1 min-w-0">
          <p className="text-text-primary truncate">{title}</p>
          <p className="text-[10px] font-mono text-text-muted truncate">
            {finding.type} · {finding.target}
          </p>
        </div>
        <span className="text-[10px] text-text-muted mt-0.5 shrink-0">
          {expanded ? "▲" : "▼"}
        </span>
      </div>
      {expanded && (
        <div className="px-2 pb-2 space-y-1.5 border-t border-border/50 mt-0.5 pt-2">
          <DetailRow label="ID" value={finding.id} />
          <DetailRow label="类型" value={finding.type || finding.attack_category} />
          <DetailRow label="目标" value={finding.target} />
          <DetailRow label="严重度" value={finding.severity ? SEVERITY_LABEL[sev] : SEVERITY_LABEL[severity]} />
          {finding.cvss_score !== undefined && finding.cvss_score > 0 && (
            <DetailRow label="CVSS" value={finding.cvss_score.toFixed(1)} />
          )}
          {finding.description && (
            <DetailRow label="描述" value={finding.description} />
          )}
          {finding.confidence && (
            <DetailRow label="置信度" value={finding.confidence} />
          )}
          {finding.evidence && finding.evidence.length > 0 && (
            <div className="text-[10px] font-mono">
              <span className="text-text-muted">证据: </span>
              <span className="text-text-secondary break-all">
                {finding.evidence.map((e) => e.content).join("; ").slice(0, 300)}
              </span>
            </div>
          )}
          {finding.data && typeof finding.data === "object" && (
            Object.entries(finding.data as Record<string, unknown>)
              .filter(([k]) => !["id", "type", "target", "severity", "campaign_id"].includes(k))
              .slice(0, 12)
              .map(([k, v]) => (
                <DetailRow key={k} label={k} value={typeof v === "string" ? v : JSON.stringify(v)} />
              ))
          )}
        </div>
      )}
    </div>
  );
}

function DetailRow({ label, value }: { label: string; value?: string | null }) {
  if (!value) return null;
  const display = value.length > 200 ? value.slice(0, 200) + "…" : value;
  return (
    <div className="text-[10px] font-mono">
      <span className="text-text-muted">{label}: </span>
      <span className="text-text-secondary break-all">{display}</span>
    </div>
  );
}

function describeFinding(f: Finding): string {
  // ClassifiedFinding path: use title directly
  if (f.title && f.title.trim().length > 0) return f.title;
  // Blackboard path: extract from data
  if (f.data && typeof f.data === "object") {
    const d = f.data as Record<string, unknown>;
    const candidates: unknown[] = [d.title, d.name, d.cve_id, d.message, d.url, d.host, d.endpoint];
    for (const c of candidates) {
      if (typeof c === "string" && c.trim().length > 0) return c;
    }
  }
  return f.target || f.type || f.attack_category || "Unknown finding";
}
