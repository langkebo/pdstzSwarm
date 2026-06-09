"use client";

import { useMemo } from "react";
import { SEVERITY_COLOR, SEVERITY_LABEL, type Severity } from "@/lib/severity";

interface SeverityChartProps {
  /** Counts per severity. Missing keys are treated as zero. */
  counts?: Partial<Record<Severity, number>>;
  /** Diameter in CSS pixels. Default 128 (matches the dashboard's
   *  32 × 4 grid). */
  size?: number;
  /** Stroke width in CSS pixels. Default 12. */
  strokeWidth?: number;
  /** Show legend to the right. Default true. */
  showLegend?: boolean;
  /** Optional click handler — receives the clicked severity. */
  onSelect?: (s: Severity) => void;
}

const ORDER: readonly Severity[] = [
  "critical", "high", "medium", "low", "informational",
] as const;

const RADIUS = 40; // matches the SVG viewBox of 0..100
const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

/**
 * Donut chart with one arc per severity. Replaces the previous
 * placeholder (which was a single background circle that always
 * rendered the same way regardless of data). The math is the standard
 * "stroke-dasharray = arc length, gap = remainder" trick on an
 * SVG <circle> rotated -90° so 0° is at 12 o'clock.
 */
export function SeverityChart({
  counts,
  size = 128,
  strokeWidth = 12,
  showLegend = true,
  onSelect,
}: SeverityChartProps) {
  const data = useMemo(
    () =>
      ORDER.map((s) => ({
        severity: s,
        value: counts?.[s] ?? 0,
        color: SEVERITY_COLOR[s],
        label: SEVERITY_LABEL[s],
      })),
    [counts]
  );

  const total = useMemo(() => data.reduce((a, d) => a + d.value, 0), [data]);

  // Cumulative offset (in arc length) for each slice. Empty slices
  // contribute 0 to offset and 0 to dash length, so they vanish.
  const slices = useMemo(() => {
    let cursor = 0;
    return data.map((d) => {
      const len = total === 0 ? 0 : (d.value / total) * CIRCUMFERENCE;
      const offset = cursor;
      cursor += len;
      return { ...d, dashLen: len, dashOffset: -offset };
    });
  }, [data, total]);

  return (
    <div className="flex items-center gap-6">
      <div
        className="relative"
        style={{ width: size, height: size }}
        role="img"
        aria-label={
          total === 0
            ? "暂无发现"
            : `严重度分布：${data
                .map((d) => `${d.value} 项 ${d.label}`)
                .join(", ")}`
        }
      >
        <svg viewBox="0 0 100 100" className="w-full h-full -rotate-90">
          {/* Background ring */}
          <circle
            cx="50"
            cy="50"
            r={RADIUS}
            fill="none"
            stroke="#1A1A2E"
            strokeWidth={strokeWidth}
          />
          {/* Data arcs */}
          {total > 0 &&
            slices.map((s) => (
              <circle
                key={s.severity}
                cx="50"
                cy="50"
                r={RADIUS}
                fill="none"
                stroke={s.color}
                strokeWidth={strokeWidth}
                strokeDasharray={`${s.dashLen} ${
                  CIRCUMFERENCE - s.dashLen
                }`}
                strokeDashoffset={s.dashOffset}
                strokeLinecap="butt"
                style={{ cursor: onSelect ? "pointer" : "default" }}
                onClick={() => onSelect?.(s.severity)}
              >
                <title>
                  {s.label}: {s.value} (
                  {total > 0
                    ? Math.round((s.value / total) * 100)
                    : 0}
                  %)
                </title>
              </circle>
            ))}
        </svg>
        <div className="absolute inset-0 flex items-center justify-center">
          <span className="text-2xl font-bold">{total}</span>
        </div>
      </div>
      {showLegend && (
        <div className="space-y-2">
          {data.map((d) => (
            <button
              key={d.severity}
              type="button"
              onClick={() => onSelect?.(d.severity)}
              className="flex items-center gap-2 w-full text-left hover:opacity-80"
              aria-label={`按 ${d.label}（${d.value} 项）筛选`}
            >
              <div
                className="w-3 h-3 rounded-full flex-shrink-0"
                style={{ backgroundColor: d.color }}
              />
              <span className="text-sm text-gray-400">{d.label}</span>
              <span className="text-sm font-medium ml-auto tabular-nums">
                {d.value}
              </span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
