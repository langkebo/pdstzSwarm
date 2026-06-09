"use client";

import { cn } from "@/lib/cn";
import { SEVERITY_LABEL, type Severity } from "@/lib/severity";

interface SeverityBadgeProps {
  severity: Severity;
  /** Override the visible label. Defaults to SEVERITY_LABEL[severity]. */
  label?: string;
  /** When true the badge renders as a small monospaced pill (default). */
  compact?: boolean;
  className?: string;
}

const STYLE_BY_SEVERITY: Record<Severity, string> = {
  critical: "badge-critical",
  high: "badge-high",
  medium: "badge-medium",
  low: "badge-low",
  informational: "badge-info",
};

/**
 * SeverityBadge — single source of truth for inline severity chips.
 * Used by the finding list, the live filter rail, and the campaign
 * summary. Renders as a mono pill with a colored left-bar.
 */
export function SeverityBadge({
  severity,
  label,
  compact = true,
  className,
}: SeverityBadgeProps) {
  const text = label ?? SEVERITY_LABEL[severity];
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 font-mono uppercase tracking-wider",
        compact
          ? "px-1.5 py-0.5 text-[10px] rounded-sm"
          : "px-2 py-1 text-xs rounded-md",
        STYLE_BY_SEVERITY[severity],
        className
      )}
    >
      <span className="block w-1.5 h-1.5 rounded-full bg-current" />
      {text}
    </span>
  );
}
