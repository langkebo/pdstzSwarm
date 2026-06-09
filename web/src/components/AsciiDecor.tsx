import { cn } from "@/lib/cn";

interface AsciiDecorProps {
  /** Optional short tag rendered in the top-right, e.g. "SECURE_LOGIN_v2.0" */
  tag?: string;
  /** Container class. */
  className?: string;
}

/**
 * AsciiDecor — draws the four ASCII corner brackets + an optional
 * tag chip. Pointer-events: none. Intended as a hero/panel frame.
 */
export function AsciiDecor({ tag, className }: AsciiDecorProps) {
  return (
    <div className={cn("ascii-frame", className)} aria-hidden>
      <span className="tl">┌</span>
      <span className="tr">┐</span>
      <span className="bl">└</span>
      <span className="br">┘</span>
      {tag && (
        <span className="absolute -top-2.5 right-3 px-1.5 py-0.5 bg-background text-[9px] font-mono uppercase tracking-[0.2em] text-accent">
          ⟨ {tag} ⟩
        </span>
      )}
    </div>
  );
}
