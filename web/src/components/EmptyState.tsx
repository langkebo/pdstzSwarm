"use client";

import { cn } from "@/lib/cn";

interface EmptyStateProps {
  title: string;
  description?: string;
  /** ASCII art / motif shown above the title. */
  motif?: string;
  action?: React.ReactNode;
  className?: string;
}

/**
 * EmptyState — used by Campaigns, Findings, Live Ops when there's
 * nothing to show. The motif is plain text (mono) so the design
 * language stays cohesive.
 */
export function EmptyState({
  title,
  description,
  motif,
  action,
  className,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center text-center",
        "py-16 px-6",
        className
      )}
    >
      {motif && (
        <pre
          aria-hidden
          className="font-mono text-[10px] leading-[1.2] text-accent/40 select-none mb-4"
        >
          {motif}
        </pre>
      )}
      <p className="font-display text-lg font-semibold text-text-primary tracking-wide">
        {title}
      </p>
      {description && (
        <p className="mt-2 text-sm text-text-secondary max-w-md font-mono">
          {description}
        </p>
      )}
      {action && <div className="mt-6">{action}</div>}
    </div>
  );
}

export const NO_CAMPAIGNS_MOTIF = `┌──────────────────────────┐
│  /\\\\________/\\\\    _    │
│  \\\\_  _  __ _/   /_\\\\   │
│    \\\\| || |/ _ \\\\|  _  │
│     \\\\| || |  __/| | | │
│      \\\\|_||_|\\\\__||_| |_│
└──────────────────────────┘`;
