"use client";

/**
 * UserInputDock — P5+ 用户输入闭环
 *
 * A chat-style operator input bar that lives at the bottom of the
 * /live page. It does three things at once:
 *
 *  1. **Renders the in-memory log** of operator-issued messages
 *     (newest at the bottom), so the user can see what they
 *     (and any other operator on the same campaign) have said.
 *     The list is scroll-locked to the bottom on new messages.
 *
 *  2. **Captures new operator input** via a multi-line textarea
 *     with a Send button. The "command" feel is preserved:
 *     `⌘/Ctrl+Enter` (or `Enter` if "send on Enter" is on)
 *     dispatches; plain `Enter` inserts a newline.
 *
 *  3. **Sends the message** to POST
 *     `/api/v1/campaigns/:id/input` via `api.campaigns.input`.
 *     The server stamps the message with an id + timestamp +
 *     author and broadcasts it on the campaign's WebSocket; the
 *     live page's `addUserInput` then renders it via this same
 *     dock (so the optimistic path is a no-op — the server
 *     echo is the source of truth).
 *
 * Quick-action chips (above the textarea) are pre-canned messages
 * that match common operator intents: focus a sub-path, skip a
 * host, request a chain explanation, or trigger the global stop
 * (which is delegated to the existing stop button — we don't
 * duplicate the API). Chips are a power-user shortcut; the
 * textarea is the primary surface.
 *
 * The component is **disabled** (textarea + send button greyed
 * out) when there is no active campaign id, so an operator
 * landing on /live without a campaign gets a clear "no input
 * target" state instead of a confusing disabled POST.
 */

import { useEffect, useRef, useState } from "react";
import { api, type UserMessage } from "@/lib/api";
import { useDashboardStore } from "@/lib/store";
import { cn } from "@/lib/cn";

interface UserInputDockProps {
  campaignId: string | null;
}

interface QuickAction {
  /** Command key shown on the chip (e.g. "/focus"). */
  key: string;
  /** Pre-filled text to insert into the textarea. */
  text: string;
  /** Hint shown in the chip's title attribute. */
  hint: string;
}

const QUICK_ACTIONS: QuickAction[] = [
  { key: "/focus",  text: "/focus ",                          hint: "聚焦到指定路径或资产" },
  { key: "/skip",   text: "/skip ",                            hint: "跳过某个目标或阶段" },
  { key: "/explain",text: "/explain the last finding",        hint: "解释最近发现的成因" },
  { key: "/report", text: "/generate report",                  hint: "请求立即生成报告" },
  { key: "/stop",   text: "/stop",                              hint: "请求停止当前任务" },
  { key: "/help",   text: "/help",                              hint: "查看可用指令" },
];

/**
 * Format a UserMessage for the inline terminal feed. Mirrors
 * the colour palette in `components/Terminal.tsx` so user
 * input reads as "operator voice" against the agent stream.
 *
 *   12:34:56.789  ▶ user@alice: focus on /admin
 */
export function formatUserInputAsAnsi(m: UserMessage): string {
  const ts = new Date(m.timestamp);
  const hh = String(ts.getHours()).padStart(2, "0");
  const mm = String(ts.getMinutes()).padStart(2, "0");
  const ss = String(ts.getSeconds()).padStart(2, "0");
  const stamp = `${hh}:${mm}:${ss}`;
  // Reuse the xterm theme tokens: text-muted for the stamp, a
  // distinct cyan for the "user" tag, text-primary for the body.
  const dim = `\x1b[38;2;100;116;139m${stamp}\x1b[0m`;
  const tag = `\x1b[38;2;103;232;249m▶ user@${m.author || "anon"}\x1b[0m`;
  const body = `\x1b[38;2;226;232;240m${m.text}\x1b[0m`;
  return `${dim}  ${tag}: ${body}`;
}

export function UserInputDock({ campaignId }: UserInputDockProps) {
  const userInputs = useDashboardStore((s) => s.userInputs);
  const setUserInputs = useDashboardStore((s) => s.setUserInputs);
  const addUserInput = useDashboardStore((s) => s.addUserInput);

  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [replayLoaded, setReplayLoaded] = useState(false);

  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const scrollerRef = useRef<HTMLDivElement | null>(null);

  // Replay history on mount + when the campaign id changes.
  // The WebSocket only carries the live tail; on a fresh page
  // load (or when the operator switches campaigns) we need to
  // pull the full log so the dock isn't empty.
  useEffect(() => {
    setReplayLoaded(false);
    setError(null);
    if (!campaignId) {
      setUserInputs([]);
      return;
    }
    let cancelled = false;
    api.campaigns
      .listUserInputs(campaignId)
      .then((msgs) => {
        if (cancelled) return;
        setUserInputs(msgs);
        setReplayLoaded(true);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : String(err));
        setReplayLoaded(true);
      });
    return () => {
      cancelled = true;
    };
  }, [campaignId, setUserInputs]);

  // Scroll the message list to the bottom whenever a new
  // message arrives (so the operator always sees their own
  // last input).
  useEffect(() => {
    const el = scrollerRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
  }, [userInputs.length]);

  function focusTextarea() {
    textareaRef.current?.focus();
  }

  async function send(text: string) {
    const trimmed = text.trim();
    if (!trimmed || !campaignId || sending) return;
    setSending(true);
    setError(null);
    try {
      const saved = await api.campaigns.input(campaignId, trimmed);
      // Server is the source of truth — push the canonical
      // message (server-stamped id/timestamp/author) onto
      // the local store. The WebSocket echo will de-dupe by
      // id, so this is safe even if the broadcast arrives
      // a few ms before the POST returns.
      addUserInput(saved);
      setDraft("");
      // Return focus to the textarea for chained inputs.
      requestAnimationFrame(focusTextarea);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSending(false);
    }
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    // ⌘/Ctrl + Enter → send. Plain Enter inserts a newline.
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      void send(draft);
    }
  }

  function handleQuickAction(q: QuickAction) {
    if (!campaignId) {
      setError("没有活动任务，无法发送指令");
      return;
    }
    // /stop is a navigation-style action: delegate to the
    // top-bar stop button. We forward by sending a /stop
    // marker; the swarm can read it from the operator log.
    // We also fire the actual stop in parallel so the UI
    // doesn't have to wait for the swarm to react.
    if (q.text === "/stop") {
      void api.campaigns.stop(campaignId).catch(() => {});
    }
    setDraft(q.text);
    requestAnimationFrame(focusTextarea);
  }

  const disabled = !campaignId || campaignId === "—";

  return (
    <section
      className="border-t border-border bg-background/60 backdrop-blur"
      data-testid="live.user-input-dock"
      aria-label="操作员输入"
    >
      {/* Header strip: title + connection status + replay status */}
      <div className="px-4 py-2 flex items-center gap-3 border-b border-border/60">
        <h3 className="text-[10px] font-mono uppercase tracking-[0.2em] text-text-muted">
          ◤ 操作员输入
          <span className="text-text-secondary"> [{userInputs.length}]</span>
        </h3>
        <span className="text-[9px] font-mono text-text-muted uppercase tracking-widest">
          p5+ · chat
        </span>
        <span className="ml-auto text-[10px] font-mono text-text-muted flex items-center gap-1.5">
          {disabled ? (
            <>
              <span className="status-dot-idle" />
              未选择任务
            </>
          ) : replayLoaded ? (
            <>
              <span className="status-dot-online" />
              链路就绪 · ⌘↵ 发送
            </>
          ) : (
            <>
              <span className="status-dot-idle animate-pulse" />
              拉取历史中…
            </>
          )}
        </span>
      </div>

      {/* Message log — newest at the bottom, auto-scroll */}
      <div
        ref={scrollerRef}
        className="max-h-32 overflow-y-auto px-4 py-2 space-y-1 font-mono text-[11px]"
        role="log"
        aria-live="polite"
        aria-relevant="additions"
        data-testid="live.user-input-log"
      >
        {userInputs.length === 0 && (
          <p className="text-text-muted">
            {disabled
              ? "请先在 /campaigns 创建并启动一个任务。"
              : "暂无输入记录。试试点击下方的快捷指令，或直接在输入框中输入。"}
          </p>
        )}
        {userInputs.map((m) => (
          <UserInputLine key={m.id} message={m} />
        ))}
      </div>

      {/* Quick-action chips */}
      <div className="px-4 py-1.5 flex items-center gap-1.5 flex-wrap border-t border-border/40">
        <span className="text-[9px] font-mono uppercase tracking-widest text-text-muted mr-1">
          快捷
        </span>
        {QUICK_ACTIONS.map((q) => (
          <button
            key={q.key}
            type="button"
            onClick={() => handleQuickAction(q)}
            disabled={disabled}
            title={q.hint}
            className={cn(
              "px-2 py-0.5 rounded border font-mono text-[10px]",
              "border-border bg-background/60 text-text-secondary",
              "hover:border-accent/50 hover:text-accent",
              "disabled:opacity-40 disabled:cursor-not-allowed",
            )}
          >
            {q.key}
          </button>
        ))}
      </div>

      {/* Input row */}
      <form
        className="px-4 py-2 flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          void send(draft);
        }}
      >
        <textarea
          ref={textareaRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={handleKeyDown}
          disabled={disabled}
          rows={1}
          maxLength={4000}
          placeholder={
            disabled
              ? "等待活动任务…"
              : "给集群下达指令…（⌘/Ctrl + ↵ 发送，Enter 换行）"
          }
          aria-label="操作员指令"
          data-testid="live.user-input-textarea"
          className={cn(
            "input-cyber flex-1 min-h-[36px] max-h-32 resize-y py-1.5",
            "disabled:opacity-50 disabled:cursor-not-allowed",
          )}
        />
        <button
          type="submit"
          disabled={disabled || !draft.trim() || sending}
          className={cn("btn-cyber shrink-0", sending && "opacity-60")}
          title="发送指令"
        >
          {sending ? "⏳ 发送中…" : "▶ 发送"}
        </button>
      </form>

      {/* Error banner */}
      {error && (
        <div
          className="px-4 py-1.5 text-[11px] font-mono text-rose-300 border-t border-rose-500/30 bg-rose-500/5"
          role="alert"
        >
          ✕ {error}
        </div>
      )}
    </section>
  );
}

function UserInputLine({ message }: { message: UserMessage }) {
  const ts = new Date(message.timestamp);
  const hh = String(ts.getHours()).padStart(2, "0");
  const mm = String(ts.getMinutes()).padStart(2, "0");
  const ss = String(ts.getSeconds()).padStart(2, "0");
  return (
    <div className="flex items-start gap-2" data-testid="live.user-input-line">
      <span className="text-text-muted tabular-nums">
        {hh}:{mm}:{ss}
      </span>
      <span className="text-cyan-400 shrink-0">▶ user@{message.author || "anon"}</span>
      <span className="text-text-primary break-words whitespace-pre-wrap">
        {message.text}
      </span>
    </div>
  );
}

export default UserInputDock;
