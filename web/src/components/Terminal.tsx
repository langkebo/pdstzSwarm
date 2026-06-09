"use client";

/**
 * Terminal — P5+ 终端
 *
 * Wraps xterm.js in a project-styled frame. Renders events as a
 * proper terminal (ANSI colors, line wrapping, copy/paste, search)
 * rather than a plain `<pre>` of divs.
 *
 * Design choices:
 *  1. **Dynamic import** — xterm + 3 addons is ~150 KB gzip. We don't
 *     pay that cost on the dashboard / settings pages; only the /live
 *     page (where it's actually used) loads it.
 *  2. **ANSI coloring** — the host code prefixes each line with a
 *     tiny ANSI sequence so xterm renders event types in their
 *     semantic color (thought / tool_call / finding / state_change).
 *  3. **Search bar** — the `@xterm/addon-search` addon gives users
 *     `/`-style search across the buffer with `n` / `N` for next/prev.
 *  4. **CRT scanlines** — the wrapper applies a `::before` repeating
 *     gradient so the terminal keeps the visual language of the rest
 *     of the app (see `styles/terminal.css`).
 *
 * Usage:
 *   <Terminal lines={events.map(toAnsi)} autoFit />
 *
 * `lines` is an array of already-ANSI-formatted strings. The component
 * appends `\r\n` and writes the new tail to the buffer (avoiding
 * re-writing the whole history on every event).
 */

import { useEffect, useRef, useState } from "react";
import { cn } from "@/lib/cn";

type TerminalStatus = "loading" | "ready" | "error";

export interface TerminalProps {
  /**
   * Already-ANSI-colored lines to write. The component diffs the
   * incoming array and only writes the new tail to the buffer.
   */
  lines: string[];
  /** Renders the toolbar (clear / search / copy). Default: true. */
  showToolbar?: boolean;
  /** Auto-fit on resize. Default: true. */
  autoFit?: boolean;
  /** Initial greeting written once on mount. */
  banner?: string;
  /** Optional font size in px. Default: 12. */
  fontSize?: number;
  /** Maximum scrollback lines. Default: 5000. */
  scrollback?: number;
  /** Optional className for the outer wrapper. */
  className?: string;
  /** Optional test id for E2E. */
  testId?: string;
}

export function Terminal({
  lines,
  showToolbar = true,
  autoFit = true,
  banner,
  fontSize = 12,
  scrollback = 5000,
  className,
  testId,
}: TerminalProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const termRef = useRef<import("@xterm/xterm").Terminal | null>(null);
  const fitRef = useRef<import("@xterm/addon-fit").FitAddon | null>(null);
  const searchRef = useRef<import("@xterm/addon-search").SearchAddon | null>(null);
  const lastWrittenRef = useRef<number>(0);
  const [status, setStatus] = useState<TerminalStatus>("loading");
  const [error, setError] = useState<string | null>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchTerm, setSearchTerm] = useState("");

  // ---- Boot xterm exactly once. ----
  useEffect(() => {
    let cancelled = false;
    let cleanup: (() => void) | null = null;

    (async () => {
      try {
        const [{ Terminal: XTerm }, { FitAddon }, { WebLinksAddon }, { SearchAddon }] =
          await Promise.all([
            import("@xterm/xterm"),
            import("@xterm/addon-fit"),
            import("@xterm/addon-web-links"),
            import("@xterm/addon-search"),
          ]);
        if (cancelled || !containerRef.current) return;

        const term = new XTerm({
          fontFamily:
            'var(--font-jetbrains-mono), "JetBrains Mono", monospace',
          fontSize,
          lineHeight: 1.25,
          cursorBlink: true,
          cursorStyle: "block",
          scrollback,
          convertEol: true,
          allowProposedApi: true,
          theme: {
            background: "#00000000", // transparent — let crt-scanlines show
            foreground: "#E2E8F0", // --text-primary
            cursor: "#00FF9C", // --accent
            cursorAccent: "#0A0A0F",
            selectionBackground: "rgba(0, 255, 156, 0.25)",
            black: "#0A0A0F",
            red: "#FF3366", // --severity-critical
            green: "#00FF9C", // --accent
            yellow: "#FFC107", // --severity-medium
            blue: "#60A5FA", // --accent-2
            magenta: "#A78BFA", // --accent-3
            cyan: "#00B872", // --accent-dim
            white: "#E2E8F0",
            brightBlack: "#64748B",
            brightRed: "#FF6B35", // --severity-high
            brightGreen: "#5EEAD4",
            brightYellow: "#FCD34D",
            brightBlue: "#7DD3FC",
            brightMagenta: "#C4B5FD",
            brightCyan: "#67E8F9",
            brightWhite: "#F8FAFC",
          },
        });

        const fit = new FitAddon();
        const links = new WebLinksAddon();
        const search = new SearchAddon();

        term.loadAddon(fit);
        term.loadAddon(links);
        term.loadAddon(search);

        term.open(containerRef.current);
        if (autoFit) {
          // run fit twice: once now, once after fonts settle
          try {
            fit.fit();
            requestAnimationFrame(() => fit.fit());
          } catch {
            /* viewport not measured yet */
          }
        }

        if (banner) {
          term.writeln(banner);
        } else {
          term.writeln(
            "\x1b[38;2;0;255;156m# 渗透测试集群实时终端 v1.0\x1b[0m",
          );
          term.writeln(
            "\x1b[38;2;100;116;139m# 已连接。等待事件推送…\x1b[0m",
          );
        }
        term.writeln("");

        termRef.current = term;
        fitRef.current = fit;
        searchRef.current = search;
        setStatus("ready");

        // Auto-fit on window resize
        const onResize = () => {
          try {
            fit.fit();
          } catch {
            /* ignore */
          }
        };
        window.addEventListener("resize", onResize);

        // ResizeObserver for parent size changes (sidebar toggle etc.)
        let observer: ResizeObserver | null = null;
        if (typeof ResizeObserver !== "undefined" && containerRef.current) {
          observer = new ResizeObserver(() => {
            try {
              fit.fit();
            } catch {
              /* ignore */
            }
          });
          observer.observe(containerRef.current);
        }

        cleanup = () => {
          window.removeEventListener("resize", onResize);
          observer?.disconnect();
          try {
            term.dispose();
          } catch {
            /* ignore */
          }
          termRef.current = null;
          fitRef.current = null;
          searchRef.current = null;
        };
      } catch (e) {
        if (cancelled) return;
        setStatus("error");
        setError(e instanceof Error ? e.message : String(e));
      }
    })();

    return () => {
      cancelled = true;
      if (cleanup) cleanup();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []); // intentional one-shot

  // ---- Diff incoming lines: write only the new tail. ----
  useEffect(() => {
    if (status !== "ready" || !termRef.current) return;
    if (lines.length < lastWrittenRef.current) {
      // buffer was reset upstream; resync from scratch
      termRef.current.clear();
      lastWrittenRef.current = 0;
    }
    for (let i = lastWrittenRef.current; i < lines.length; i++) {
      const line = lines[i];
      termRef.current.writeln(line);
    }
    lastWrittenRef.current = lines.length;
  }, [lines, status]);

  function handleClear() {
    if (!termRef.current) return;
    termRef.current.clear();
    termRef.current.writeln("\x1b[38;2;100;116;139m# 缓冲区已清空\x1b[0m");
    lastWrittenRef.current = 0;
  }

  function handleSearchSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!searchRef.current || !termRef.current || !searchTerm) return;
    searchRef.current.findNext(searchTerm, {
      caseSensitive: false,
      wholeWord: false,
      regex: false,
    });
  }

  function handleSearchKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (!searchRef.current) return;
    if (e.key === "Escape") {
      setSearchOpen(false);
      setSearchTerm("");
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (e.shiftKey) {
        searchRef.current.findPrevious(searchTerm, {
          caseSensitive: false,
        });
      } else {
        searchRef.current.findNext(searchTerm, {
          caseSensitive: false,
        });
      }
    }
  }

  async function handleCopy() {
    if (!termRef.current) return;
    const sel = termRef.current.getSelection();
    if (!sel) return;
    try {
      await navigator.clipboard.writeText(sel);
    } catch {
      /* clipboard might be unavailable in some embed contexts */
    }
  }

  return (
    <div
      className={cn("terminal-frame", className)}
      data-testid={testId}
      data-status={status}
    >
      {showToolbar && (
        <div className="terminal-toolbar" role="toolbar" aria-label="终端工具栏">
          <span className="label">≣ xterm.js</span>
          <span>·</span>
          <span>{lines.length} 行</span>
          <span className="spacer" />
          {searchOpen ? (
            <form onSubmit={handleSearchSubmit}>
              <input
                className="search"
                placeholder="搜索…（↵ 下一个，⇧↵ 上一个，esc 关闭）"
                value={searchTerm}
                onChange={(e) => setSearchTerm(e.target.value)}
                onKeyDown={handleSearchKeyDown}
                autoFocus
                aria-label="搜索终端"
              />
            </form>
          ) : (
            <button type="button" onClick={() => setSearchOpen(true)} title="搜索">
              ⌕ 搜索
            </button>
          )}
          <button type="button" onClick={handleCopy} title="复制选区">
            ⎘ 复制
          </button>
          <button type="button" onClick={handleClear} title="清空缓冲区">
            ⌫ 清空
          </button>
        </div>
      )}
      <div className="terminal-inner" ref={containerRef} aria-label="实时终端输出" />
      {status === "loading" && (
        <div
          className="absolute inset-0 flex items-center justify-center pointer-events-none text-[10px] font-mono uppercase tracking-widest text-text-muted"
          aria-live="polite"
        >
          <span className="text-accent">▸</span> 正在启动 xterm.js
          <span className="animate-blink-cursor">▌</span>
        </div>
      )}
      {status === "error" && error && (
        <div
          className="absolute inset-0 flex items-center justify-center p-4 text-[11px] font-mono text-rose-300 bg-background/80"
          role="alert"
        >
          <div>
            <p className="font-bold mb-1">终端启动失败</p>
            <p className="text-rose-200/80">{error}</p>
            <p className="text-text-muted mt-2 text-[10px]">
              提示：/live 页面应使用动态渲染（xterm chunk 不能静态导出）。
            </p>
          </div>
        </div>
      )}
      <span className="terminal-status" aria-hidden>
        ● 在线
      </span>
    </div>
  );
}

/**
 * ansiColor wraps a string with a 24-bit ANSI SGR color escape.
 * The xterm theme re-uses our design tokens but in raw RGB for fidelity.
 */
export function ansi(rgb: [number, number, number], text: string): string {
  return `\x1b[38;2;${rgb[0]};${rgb[1]};${rgb[2]}m${text}\x1b[0m`;
}

/** Semantic event colors. Kept in lock-step with /live/page.tsx typeColors. */
export const EVENT_COLORS = {
  thought: [148, 163, 184] as [number, number, number], // text-secondary
  tool_call: [255, 193, 7] as [number, number, number], // severity-medium
  tool_result: [0, 255, 156] as [number, number, number], // accent
  finding_discovered: [255, 51, 102] as [number, number, number], // critical
  state_change: [167, 139, 250] as [number, number, number], // accent-3
  step_executed: [255, 193, 7] as [number, number, number],
  error: [244, 63, 94] as [number, number, number], // rose
  milestone: [16, 185, 129] as [number, number, number], // emerald
} as const;

/** Format a campaign event into a single ANSI line. */
export interface AnsiEvent {
  timestamp: string | Date;
  agent_name?: string;
  event_type: keyof typeof EVENT_COLORS | string;
  detail: string;
}

export function formatEventAsAnsi(e: AnsiEvent): string {
  const ts =
    typeof e.timestamp === "string"
      ? new Date(e.timestamp)
      : e.timestamp;
  const hh = String(ts.getHours()).padStart(2, "0");
  const mm = String(ts.getMinutes()).padStart(2, "0");
  const ss = String(ts.getSeconds()).padStart(2, "0");
  const ms = String(ts.getMilliseconds()).padStart(3, "0");
  const stamp = `${hh}:${mm}:${ss}.${ms}`;
  const color =
    (EVENT_COLORS as Record<string, [number, number, number]>)[
      e.event_type
    ] ?? [100, 116, 139];
  const head = ansi([100, 116, 139], stamp);
  const tag = e.agent_name
    ? ansi([0, 255, 156], `[${e.agent_name}]`) + " "
    : "";
  const body = ansi(color, e.detail);
  return `${head}  ${tag}${body}`;
}

export default Terminal;
