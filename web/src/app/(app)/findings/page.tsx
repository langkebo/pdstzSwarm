"use client";

import { useMemo, useState } from "react";
import { useDashboardStore } from "@/lib/store";
import { GlassPanel } from "@/components/GlassPanel";
import { TopBar } from "@/components/TopBar";
import { SeverityBadge } from "@/components/SeverityBadge";
import { EmptyState } from "@/components/EmptyState";
import { cn } from "@/lib/cn";
import {
  SEVERITY_COLOR,
  SEVERITY_LABEL,
  severityFromFinding,
  type Severity,
} from "@/lib/severity";

const NO_FINDINGS_MOTIF = `┌────────────────────────┐
│ 0xDEAD_BEEF            │
│   no_signal.dat        │
│   awaiting_input…      │
└────────────────────────┘`;

export default function FindingsPage() {
  const findings = useDashboardStore((s) => s.findings);
  const [filter, setFilter] = useState<Severity | null>(null);
  const [query, setQuery] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const filtered = useMemo(() => {
    return findings
      .filter((f) => (filter ? severityFromFinding(f) === filter : true))
      .filter((f) => {
        if (!query) return true;
        const q = query.toLowerCase();
        return (
          f.target.toLowerCase().includes(q) ||
          (f.type || "").toLowerCase().includes(q) ||
          (f.agent_name || "").toLowerCase().includes(q) ||
          JSON.stringify(f.data ?? {})
            .toLowerCase()
            .includes(q)
        );
      })
      .sort(
        (a, b) =>
          new Date(b.created_at ?? 0).getTime() -
          new Date(a.created_at ?? 0).getTime()
      );
  }, [findings, filter, query]);

  const selected = useMemo(
    () => filtered.find((f) => f.id === selectedId) ?? null,
    [filtered, selectedId]
  );

  return (
    <div className="min-h-screen flex flex-col">
      <TopBar
        title="发现浏览器"
        subtitle={`共 ${findings.length} 项 · 当前 ${filtered.length} 项匹配`}
        actions={
          <button className="btn-cyber" type="button">
            ⇣ 导出 JSON
          </button>
        }
      />

      <div className="px-6 py-3 border-b border-border bg-background/40 flex flex-wrap items-center gap-2">
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索目标 / 类型 / 代理 / 数据…"
          className="input-cyber max-w-md"
          spellCheck={false}
        />
        <span className="text-[10px] font-mono text-text-muted">严重度：</span>
        {(["critical", "high", "medium", "low", "informational"] as Severity[]).map(
          (s) => (
            <button
              key={s}
              onClick={() => setFilter((cur) => (cur === s ? null : s))}
              className={cn(
                "px-2 py-1 font-mono text-[10px] uppercase tracking-widest rounded-sm border",
                filter === s
                  ? "text-text-primary"
                  : "text-text-secondary border-border hover:border-border-strong"
              )}
              style={
                filter === s
                  ? {
                      backgroundColor: `${SEVERITY_COLOR[s]}22`,
                      borderColor: `${SEVERITY_COLOR[s]}66`,
                      color: SEVERITY_COLOR[s],
                    }
                  : undefined
              }
              type="button"
            >
              {SEVERITY_LABEL[s]}
            </button>
          )
        )}
      </div>

      <div className="flex-1 grid grid-cols-1 lg:grid-cols-[1fr_400px] gap-0 overflow-hidden">
        {/* List */}
        <div className="overflow-y-auto p-3">
          {filtered.length === 0 ? (
            <EmptyState
              motif={NO_FINDINGS_MOTIF}
              title="未发现匹配项"
              description="当前筛选条件下没有发现项。尝试更换搜索关键字或等待集群上报。"
            />
          ) : (
            <GlassPanel variant="soft" className="p-2" frame frameTag="findings.list">
              <ul className="divide-y divide-border/40">
                {filtered.map((f) => {
                  const sev = severityFromFinding(f);
                  const isSelected = f.id === selectedId;
                  return (
                    <li key={f.id}>
                      <button
                        onClick={() => setSelectedId(f.id)}
                        className={cn(
                          "w-full text-left flex items-center gap-3 py-2 px-3 rounded-md transition-colors",
                          isSelected
                            ? "bg-accent/10 border border-accent/40"
                            : "border border-transparent hover:bg-surface-hover/50"
                        )}
                        type="button"
                      >
                        <SeverityBadge severity={sev} />
                        <div className="flex-1 min-w-0">
                          <p className="text-sm text-text-primary truncate">
                            <span className="font-mono text-[10px] text-text-muted mr-2">
                              {f.type}
                            </span>
                            {describeFinding(f)}
                          </p>
                          <p className="text-[10px] font-mono text-text-muted truncate mt-0.5">
                            {f.target} · {f.agent_name ?? "—"} ·{" "}
                            {f.created_at
                              ? new Date(f.created_at).toLocaleString()
                              : "—"}
                          </p>
                        </div>
                      </button>
                    </li>
                  );
                })}
              </ul>
            </GlassPanel>
          )}
        </div>

        {/* Detail drawer */}
        <aside
          className={cn(
            "border-l border-border bg-background/60 overflow-y-auto p-4",
            "transition-all duration-200",
            selected ? "translate-x-0" : "translate-x-0"
          )}
        >
          {selected ? (
            <FindingDetail finding={selected} />
          ) : (
            <div className="h-full flex flex-col items-center justify-center text-center font-mono">
              <p className="text-4xl text-accent/40 mb-2">◢◤</p>
              <p className="text-sm text-text-muted">
                选中一条发现以查看完整负载数据。
              </p>
            </div>
          )}
        </aside>
      </div>
    </div>
  );
}

function FindingDetail({ finding }: { finding: import("@/lib/api").Finding }) {
  const sev = severityFromFinding(finding);
  return (
    <div className="space-y-4">
      <GlassPanel
        variant="soft"
        className="p-4"
        frame
        frameTag="finding.detail"
        stripe={SEVERITY_COLOR[sev]}
      >
        <div className="flex items-center gap-2 mb-2">
          <SeverityBadge severity={sev} />
          <span className="text-[10px] font-mono uppercase tracking-widest text-text-muted">
            {finding.type}
          </span>
        </div>
        <h2 className="font-display text-lg font-semibold text-text-primary">
          {describeFinding(finding)}
        </h2>
        <p className="font-mono text-xs text-text-muted mt-1 break-all">
          编号：{finding.id}
        </p>
      </GlassPanel>

      <div className="space-y-2 text-sm font-mono">
        <KV label="目标" value={finding.target} />
        <KV label="代理" value={finding.agent_name ?? "—"} />
        <KV
          label="创建时间"
          value={
            finding.created_at
              ? new Date(finding.created_at).toLocaleString()
              : "—"
          }
        />
        {finding.pheromone !== undefined && (
          <KV
            label="信息素"
            value={`${(finding.pheromone * 100).toFixed(0)}%`}
          />
        )}
        {finding.half_life_sec !== undefined && (
          <KV label="半衰期" value={`${finding.half_life_sec} 秒`} />
        )}
      </div>

      {finding.data && (
        <GlassPanel variant="soft" className="p-3">
          <p className="text-[10px] font-mono uppercase tracking-widest text-text-muted mb-2">
            负载数据
          </p>
          <pre className="text-[11px] font-mono text-text-primary overflow-x-auto whitespace-pre-wrap break-all">
            {JSON.stringify(finding.data, null, 2)}
          </pre>
        </GlassPanel>
      )}
    </div>
  );
}

function KV({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center gap-2 text-[11px]">
      <span className="text-text-muted uppercase tracking-widest">{label}:</span>
      <span className="text-text-primary break-all">{value}</span>
    </div>
  );
}

function describeFinding(f: import("@/lib/api").Finding): string {
  if (f.data && typeof f.data === "object") {
    const d = f.data as Record<string, unknown>;
    const candidates: unknown[] = [d.title, d.name, d.cve_id, d.message, d.url, d.host, d.endpoint];
    for (const c of candidates) {
      if (typeof c === "string" && c.trim().length > 0) return c;
    }
  }
  return f.target || f.type || "未知发现";
}
