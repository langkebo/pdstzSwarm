"use client";

import { useState, useRef, useEffect, useCallback, useMemo } from "react";
import { api, type PromptType, type PromptDetail } from "@/lib/api";
import { cn } from "@/lib/cn";

/**
 * Lightweight go-template syntax-highlighted editor.
 *
 * The implementation is a `<textarea>` stacked on top of a
 * `<pre>` whose `innerHTML` is a tokenised copy of the same
 * text. We sync scroll between the two layers so the highlight
 * moves with the cursor. Tab inserts two spaces, Ctrl-S saves,
 * Ctrl-R resets to the embedded default.
 *
 * The editor avoids the 2.5 MB Monaco bundle — important for
 * the `output: export` model, where every KB of JS ends up
 * shipped to the browser. The trade-off is no IntelliSense or
 * multi-cursor; the variables list in the right rail is the
 * "suggest" surface.
 */
export function PromptEditor({
  type,
  initial,
  onSave,
  onReset,
}: {
  type: PromptType;
  initial: PromptDetail;
  onSave: (next: PromptDetail) => void;
  onReset: () => void;
}) {
  const [body, setBody] = useState(initial.body);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<number | null>(initial.updated_at || null);
  const taRef = useRef<HTMLTextAreaElement | null>(null);
  const preRef = useRef<HTMLPreElement | null>(null);

  // Reset state when the active type changes.
  useEffect(() => {
    setBody(initial.body);
    setDirty(false);
    setError(null);
    setSavedAt(initial.updated_at || null);
  }, [initial.type, initial.body, initial.updated_at]);

  // Tokenised body for the syntax-highlight overlay.
  const highlighted = useMemo(() => highlightTemplate(body), [body]);

  // Sync scroll between textarea + overlay.
  const onScroll = useCallback(() => {
    if (taRef.current && preRef.current) {
      preRef.current.scrollTop = taRef.current.scrollTop;
      preRef.current.scrollLeft = taRef.current.scrollLeft;
    }
  }, []);

  async function doSave() {
    setSaving(true);
    setError(null);
    try {
      const updated = await api.prompts.save(type, body);
      onSave(updated);
      setSavedAt(updated.updated_at);
      setDirty(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  // Tab inserts two spaces at the cursor instead of changing
  // focus (the default browser behaviour, which is awful for
  // an editor). Ctrl/Cmd-S saves, Ctrl/Cmd-R resets.
  const onKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
      if ((e.ctrlKey || e.metaKey) && e.key === "s") {
        e.preventDefault();
        void doSave();
        return;
      }
      if ((e.ctrlKey || e.metaKey) && e.key === "r") {
        e.preventDefault();
        onReset();
        return;
      }
      if (e.key === "Tab") {
        e.preventDefault();
        const ta = e.currentTarget;
        const start = ta.selectionStart;
        const end = ta.selectionEnd;
        const before = body.slice(0, start);
        const after = body.slice(end);
        const next = before + "  " + after;
        setBody(next);
        setDirty(true);
        // Restore cursor position after the inserted spaces.
        requestAnimationFrame(() => {
          ta.selectionStart = ta.selectionEnd = start + 2;
        });
      }
    },
    // doSave is stable; intentionally not in deps to avoid
    // re-binding the listener on every keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [body, onReset]
  );

  return (
    <div className="space-y-2">
      {/* Toolbar */}
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <span className="font-mono text-xs uppercase tracking-widest text-accent">
            {type}
          </span>
          {initial.version > 0 ? (
            <span className="font-mono text-[10px] px-1.5 py-0.5 rounded border border-accent/40 text-accent">
              v{initial.version} · 已覆盖
            </span>
          ) : (
            <span className="font-mono text-[10px] px-1.5 py-0.5 rounded border border-border text-text-muted">
              默认
            </span>
          )}
          {savedAt && (
            <span className="font-mono text-[10px] text-text-muted">
              保存于 {formatTime(savedAt)}
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {dirty && (
            <span className="font-mono text-[10px] text-yellow-400">● 未保存</span>
          )}
          <button
            onClick={onReset}
            disabled={saving || initial.version === 0}
            className="btn-cyber-sm"
            type="button"
          >
            恢复默认
          </button>
          <button
            onClick={doSave}
            disabled={saving || !dirty}
            className={cn(
              "btn-cyber-sm",
              dirty ? "border-accent text-accent" : "opacity-50"
            )}
            type="button"
          >
            {saving ? "保存中…" : "保存（⌘S）"}
          </button>
        </div>
      </div>

      {error && (
        <div className="font-mono text-[11px] text-red-400 border border-red-400/30 bg-red-400/5 p-2 rounded">
          {error}
        </div>
      )}

      {initial.description && (
        <p className="font-mono text-[11px] text-text-muted">{initial.description}</p>
      )}

      {/* Editor */}
      <div className="relative font-mono text-xs border border-border rounded-md overflow-hidden bg-background/80">
        <pre
          ref={preRef}
          aria-hidden
          className="absolute inset-0 p-3 m-0 overflow-auto whitespace-pre pointer-events-none text-text-primary"
          dangerouslySetInnerHTML={{ __html: highlighted + "\n" }}
        />
        <textarea
          ref={taRef}
          value={body}
          onChange={(e) => {
            setBody(e.target.value);
            setDirty(true);
          }}
          onScroll={onScroll}
          onKeyDown={onKeyDown}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          wrap="off"
          rows={20}
          className="relative w-full p-3 m-0 bg-transparent text-transparent caret-accent selection:bg-accent/30 resize-none focus:outline-none"
        />
      </div>

      {/* Variables (parsed by backend; surfaced here so the
          editor can show the "X variables detected" badge and
          click-to-insert). */}
      {initial.variables && initial.variables.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="font-mono text-[10px] uppercase text-text-muted tracking-widest">
            变量
          </span>
          {initial.variables.map((v) => (
            <button
              key={v}
              onClick={() => {
                const snippet = `{{ .${v} }}`;
                const ta = taRef.current;
                if (!ta) return;
                const start = ta.selectionStart;
                const end = ta.selectionEnd;
                const next = body.slice(0, start) + snippet + body.slice(end);
                setBody(next);
                setDirty(true);
                requestAnimationFrame(() => {
                  ta.focus();
                  ta.selectionStart = ta.selectionEnd = start + snippet.length;
                });
              }}
              className="font-mono text-[10px] px-1.5 py-0.5 rounded border border-border hover:border-accent text-text-secondary hover:text-accent"
              type="button"
              title={`在光标处插入 {{ .${v} }}`}
            >
              {v}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function formatTime(unix: number): string {
  return new Date(unix * 1000).toLocaleString();
}

/**
 * Tokenise a go-template body into HTML. The highlight is
 * intentionally simple: {{ ... }} blocks are wrapped in a span;
 * .Identifiers inside are bolded; the rest is plain text. We
 * escape user content first so the result is safe to use with
 * `dangerouslySetInnerHTML`.
 */
function highlightTemplate(src: string): string {
  let out = "";
  let i = 0;
  while (i < src.length) {
    if (src[i] === "{" && src[i + 1] === "{") {
      // Find the closing `}}`.
      let j = i + 2;
      while (j < src.length - 1 && !(src[j] === "}" && src[j + 1] === "}")) {
        j++;
      }
      if (j >= src.length - 1) {
        out += escapeHTML(src.slice(i));
        break;
      }
      const inner = src.slice(i + 2, j);
      out += `<span class="text-accent">{{ ${highlightDots(inner)} }}</span>`;
      i = j + 2;
    } else {
      let j = i;
      while (j < src.length && !(src[j] === "{" && src[j + 1] === "{")) {
        j++;
      }
      out += escapeHTML(src.slice(i, j));
      i = j;
    }
  }
  return out;
}

function highlightDots(s: string): string {
  // Tokenise .Identifier tokens inside an action block.
  let out = "";
  let i = 0;
  while (i < s.length) {
    if (s[i] === ".") {
      let name = "";
      let k = i + 1;
      while (k < s.length && isIdent(s[k])) {
        name += s[k];
        k++;
      }
      if (name) {
        out += `<span class="text-yellow-400">.${escapeHTML(name)}</span>`;
        i = k;
        continue;
      }
    }
    out += escapeHTML(s[i]);
    i++;
  }
  return out;
}

function isIdent(c: string): boolean {
  return /[A-Za-z0-9_]/.test(c);
}

function escapeHTML(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
