"use client";

import { cn } from "@/lib/cn";
import { AsciiDecor } from "./AsciiDecor";

interface GlassPanelProps {
  children: React.ReactNode;
  /** Visual weight. `strong` = login surface, `soft` = nested cards. */
  variant?: "soft" | "strong";
  /** Add a 1px top status stripe. */
  stripe?: string;
  /** Show the ASCII corner brackets + optional tag. */
  frame?: boolean;
  frameTag?: string;
  /** Optional click handler — makes the panel behave like a button. */
  onClick?: () => void;
  className?: string;
}

/**
 * GlassPanel — the only "surface" primitive the app uses. Wraps the
 * .glass-panel base class plus optional accent stripes / frames.
 */
export function GlassPanel({
  children,
  variant = "soft",
  stripe,
  frame = false,
  frameTag,
  onClick,
  className,
}: GlassPanelProps) {
  const variantClass = variant === "strong" ? "glass-panel-strong" : "glass-panel";
  // When onClick is provided, render as a button so we get native
  // keyboard semantics (Enter/Space) and proper a11y.
  if (onClick) {
    return (
      <button
        type="button"
        onClick={onClick}
        className={cn(
          variantClass,
          "rounded-lg relative text-left w-full",
          className
        )}
        style={stripe ? ({ "--stripe": stripe } as React.CSSProperties) : undefined}
      >
        {stripe && (
          <div
            aria-hidden
            className="absolute inset-x-0 top-0 h-px"
            style={{ background: stripe, opacity: 0.6 }}
          />
        )}
        {frame && <AsciiDecor tag={frameTag} />}
        {children}
      </button>
    );
  }
  return (
    <div
      className={cn(variantClass, "rounded-lg relative", className)}
      style={stripe ? ({ "--stripe": stripe } as React.CSSProperties) : undefined}
    >
      {stripe && (
        <div
          aria-hidden
          className="absolute inset-x-0 top-0 h-px"
          style={{ background: stripe, opacity: 0.6 }}
        />
      )}
      {frame && <AsciiDecor tag={frameTag} />}
      {children}
    </div>
  );
}
