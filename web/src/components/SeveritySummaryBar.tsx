"use client";

/**
 * SeveritySummaryBar — compact horizontal summary of a report's
 * finding distribution. Used in the Reports list rows and the
 * report-detail header.
 *
 * The bar is sized so a single row of a long list can carry the
 * total findings count, the risk label, and a stacked colored bar —
 * 30 px tall, 100% width, glassy.
 */

import { cn } from "@/lib/cn";
import type { ReportSummary } from "@/lib/api";

interface Props {
  summary: ReportSummary;
  className?: string;
}

const RISK_STYLES: Record<string, string> = {
  critical: "text-rose-300 border-rose-500/50 bg-rose-500/10",
  high: "text-orange-300 border-orange-500/50 bg-orange-500/10",
  medium: "text-amber-300 border-amber-500/50 bg-amber-500/10",
  low: "text-sky-300 border-sky-500/50 bg-sky-500/10",
  informational: "text-slate-300 border-slate-500/50 bg-slate-500/10",
};

const RISK_LABEL: Record<string, string> = {
  critical: "严重",
  high: "高危",
  medium: "中危",
  low: "低危",
  informational: "提示",
};

function count(s: ReportSummary, key: keyof ReportSummary): number {
  return (s[key] as number) || 0;
}

export function SeveritySummaryBar({ summary, className }: Props) {
  const total = Math.max(summary.total_findings, 1);
  const crit = (count(summary, "critical_count") / total) * 100;
  const high = (count(summary, "high_count") / total) * 100;
  const med = (count(summary, "medium_count") / total) * 100;
  const low = (count(summary, "low_count") / total) * 100;
  const info = (count(summary, "info_count") / total) * 100;

  const risk = (summary.overall_risk || "informational").toLowerCase();
  const riskStyle =
    RISK_STYLES[risk] || RISK_STYLES.informational;
  const riskLabel = RISK_LABEL[risk] || RISK_LABEL.informational;

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <div className="flex items-center justify-between text-xs">
        <div className="flex items-center gap-2">
          <span
            className={cn(
              "inline-flex items-center gap-1 rounded border px-2 py-0.5 font-mono text-[10px] uppercase tracking-wider",
              riskStyle,
            )}
          >
            <span className="size-1.5 rounded-full bg-current" />
            {riskLabel}
          </span>
          <span className="text-slate-400">
            {summary.total_findings} 个发现
          </span>
        </div>
        {summary.average_cvss > 0 && (
          <span className="font-mono text-slate-400">
            平均 CVSS{" "}
            <span className="text-slate-200">
              {summary.average_cvss.toFixed(2)}
            </span>
          </span>
        )}
      </div>
      <div
        className="h-1.5 w-full overflow-hidden rounded-full bg-white/5 flex"
        title={`严重 ${count(summary, "critical_count")} · 高危 ${count(summary, "high_count")} · 中危 ${count(summary, "medium_count")} · 低危 ${count(summary, "low_count")} · 提示 ${count(summary, "info_count")}`}
      >
        {crit > 0 && (
          <div className="h-full bg-rose-500" style={{ width: `${crit}%` }} />
        )}
        {high > 0 && (
          <div className="h-full bg-orange-500" style={{ width: `${high}%` }} />
        )}
        {med > 0 && (
          <div className="h-full bg-amber-500" style={{ width: `${med}%` }} />
        )}
        {low > 0 && (
          <div className="h-full bg-sky-500" style={{ width: `${low}%` }} />
        )}
        {info > 0 && (
          <div
            className="h-full bg-slate-500/60"
            style={{ width: `${info}%` }}
          />
        )}
      </div>
    </div>
  );
}
