"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, type MCPServerInfo, type MCPServerStatus, type MCPToolInfo } from "@/lib/api";
import { SectionShell } from "@/components/settings/SectionShell";

/**
 * MCP page — P5+ MCP 协议交付界面.
 *
 * Renders three panels stacked vertically inside a single
 * SectionShell so the settings rail keeps its single-page
 * rhythm:
 *
 *   1. Server catalogue — one card per managed server with
 *      an enable / disable switch and an inline "tools
 *      allowed" textarea. PUT calls go through
 *      api.mcp.setStatus / setAllow.
 *
 *   2. Fan-out tool inventory — a flat list of all tools
 *      the host transport currently exposes. Read-only;
 *      driven by api.mcp.listTools.
 *
 *   3. Configuration snippets — copy-pasteable Claude
 *      Desktop / Cursor JSON. The snippets include the
 *      SSE transport settings so the user can either
 *      install the stdio binary (recommended) or point
 *      their client at a remote SSE endpoint.
 *
 * The page is fully client-side. There's no
 * `useSearchParams` so we don't need a Suspense
 * boundary.
 */
export default function McpPage() {
  const [servers, setServers] = useState<MCPServerInfo[] | null>(null);
  const [tools, setTools] = useState<MCPToolInfo[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null); // "name:status" or "name:allow"

  const refresh = useCallback(async () => {
    setErr(null);
    try {
      const [s, t] = await Promise.all([
        api.mcp.listServers().catch((e) => {
          setErr(String(e?.message ?? e));
          return { count: 0, servers: [] as MCPServerInfo[] };
        }),
        api.mcp.listTools().catch(() => ({ count: 0, tools: [] as MCPToolInfo[] })),
      ]);
      setServers(s.servers);
      setTools(t.tools);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Toggle a server's enabled/disabled state. Optimistic
  // update; the server response is the source of truth.
  const onToggleStatus = useCallback(
    async (name: string, current: MCPServerStatus) => {
      const next: MCPServerStatus = current === "enabled" ? "disabled" : "enabled";
      setBusy(`${name}:status`);
      try {
        await api.mcp.setStatus(name, next);
        await refresh();
      } catch (e) {
        setErr(String((e as Error)?.message ?? e));
      } finally {
        setBusy(null);
      }
    },
    [refresh],
  );

  // Replace a server's tool allow-list. Empty input
  // means "all tools" — the API distinguishes nil
  // (clear) vs [] (also clear, per the handler
  // contract) so we always send [].
  const onSaveAllow = useCallback(
    async (name: string, raw: string) => {
      setBusy(`${name}:allow`);
      try {
        const list = raw
          .split(/[\s,]+/)
          .map((s) => s.trim())
          .filter(Boolean);
        await api.mcp.setAllow(name, list);
        await refresh();
      } catch (e) {
        setErr(String((e as Error)?.message ?? e));
      } finally {
        setBusy(null);
      }
    },
    [refresh],
  );

  const totalTools = useMemo(() => {
    if (!tools) return 0;
    return tools.length;
  }, [tools]);

  return (
    <SectionShell
      title="MCP 服务器"
      hint="STDIO + SSE 传输。支持逐服务器启用 / 禁用与逐工具白名单。"
      tag="settings.mcp"
    >
      {err && (
        <p className="mb-3 text-[11px] font-mono text-rose-400">
          ⚠ {err}
        </p>
      )}

      {/* Panel 1: server catalogue */}
      <div className="mb-5">
        <h4 className="font-display text-sm font-semibold text-text-primary mb-2">
          已纳管服务器（{servers?.length ?? 0}）
        </h4>
        {servers === null && (
          <p className="font-mono text-xs text-text-muted">加载中…</p>
        )}
        {servers && servers.length === 0 && (
          <p className="font-mono text-xs text-text-muted">
            尚未注册任何 MCP 服务器。执行
            <code className="mx-1 px-1.5 py-0.5 bg-white/5 rounded">
              pentestswarm mcp serve
            </code>
            以注册默认的 &quot;pentestswarm&quot; 核心。
          </p>
        )}
        <div className="space-y-3">
          {servers?.map((s) => (
            <ServerCard
              key={s.name}
              server={s}
              busy={busy === `${s.name}:status` || busy === `${s.name}:allow`}
              onToggleStatus={() => onToggleStatus(s.name, s.status)}
              onSaveAllow={(raw) => onSaveAllow(s.name, raw)}
            />
          ))}
        </div>
      </div>

      {/* Panel 2: fan-out tool inventory */}
      <div className="mb-5">
        <h4 className="font-display text-sm font-semibold text-text-primary mb-2">
          工具清单（{totalTools}）
        </h4>
        {tools === null && (
          <p className="font-mono text-xs text-text-muted">加载中…</p>
        )}
        {tools && tools.length === 0 && (
          <p className="font-mono text-xs text-text-muted">
            主机传输当前未暴露任何工具。
          </p>
        )}
        <ul className="divide-y divide-white/5 border border-white/5 rounded">
          {tools?.map((t) => (
            <li
              key={t.name}
              className="flex items-start gap-3 px-3 py-2 hover:bg-white/5 transition-colors"
            >
              <code className="font-mono text-xs text-cyber-cyan shrink-0 min-w-[180px]">
                {t.name}
              </code>
              <span className="font-mono text-[11px] text-text-secondary leading-relaxed">
                {t.description}
              </span>
            </li>
          ))}
        </ul>
      </div>

      {/* Panel 3: configuration snippets */}
      <ConfigSnippets />
    </SectionShell>
  );
}

function ServerCard({
  server,
  busy,
  onToggleStatus,
  onSaveAllow,
}: {
  server: MCPServerInfo;
  busy: boolean;
  onToggleStatus: () => void;
  onSaveAllow: (raw: string) => void;
}) {
  const [allowText, setAllowText] = useState((server.allow ?? []).join(", "));
  const isEnabled = server.status === "enabled";

  // Keep the textarea in sync when the server's
  // allow-list changes upstream (e.g. after a
  // successful PUT refresh). We guard with `name`
  // + `allow?.length` so a user typing isn't
  // clobbered by their own unsaved input.
  useEffect(() => {
    setAllowText((server.allow ?? []).join(", "));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [server.name, server.allow?.length]);

  return (
    <div className="border border-white/5 rounded p-3">
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <code className="font-mono text-sm text-text-primary">
              {server.name}
            </code>
            <span className="text-[10px] font-mono text-text-muted">
              v{server.server_info.version}
            </span>
            <span
              className={[
                "text-[10px] font-mono px-1.5 py-0.5 rounded",
                isEnabled
                  ? "bg-emerald-500/10 text-emerald-300"
                  : "bg-white/5 text-text-muted",
              ].join(" ")}
            >
              {server.status}
            </span>
          </div>
          {server.description && (
            <p className="text-[11px] font-mono text-text-muted mt-1">
              {server.description}
            </p>
          )}
          <p className="text-[10px] font-mono text-text-muted mt-1">
            {server.tool_count} 个工具已注册
          </p>
        </div>
        <button
          type="button"
          disabled={busy}
          onClick={onToggleStatus}
          className={[
            "px-3 py-1 text-[11px] font-mono rounded border",
            isEnabled
              ? "border-rose-400/30 text-rose-300 hover:bg-rose-500/10"
              : "border-emerald-400/30 text-emerald-300 hover:bg-emerald-500/10",
            busy ? "opacity-50 cursor-wait" : "",
          ].join(" ")}
        >
          {busy ? "处理中…" : isEnabled ? "禁用" : "启用"}
        </button>
      </div>

      {/* Per-tool allow-list */}
      <div className="mt-3">
        <label className="block">
          <span className="block text-[10px] font-mono text-text-muted mb-1">
            工具白名单（逗号分隔）。留空 = 全部工具。
          </span>
          <div className="flex gap-2">
            <input
              type="text"
              value={allowText}
              onChange={(e) => setAllowText(e.target.value)}
              placeholder="scan_target, quick_recon, list_campaigns"
              className="flex-1 px-2 py-1 bg-black/30 border border-white/5 rounded font-mono text-xs"
              spellCheck={false}
              autoComplete="off"
            />
            <button
              type="button"
              disabled={busy}
              onClick={() => onSaveAllow(allowText)}
              className="px-3 py-1 text-[11px] font-mono rounded border border-cyber-cyan/30 text-cyber-cyan hover:bg-cyber-cyan/10 disabled:opacity-50"
            >
              保存
            </button>
          </div>
        </label>

        {/* Tool chips */}
        {server.tools.length > 0 && (
          <div className="flex flex-wrap gap-1 mt-2">
            {server.tools.map((name) => {
              const allowed =
                !server.allow || server.allow.length === 0
                  ? true
                  : server.allow.includes(name);
              return (
                <span
                  key={name}
                  className={[
                    "text-[10px] font-mono px-1.5 py-0.5 rounded",
                    allowed
                      ? "bg-cyber-cyan/10 text-cyber-cyan"
                      : "bg-white/5 text-text-muted line-through",
                  ].join(" ")}
                  title={allowed ? "对主机可见" : "对主机隐藏"}
                >
                  {name}
                </span>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

function ConfigSnippets() {
  // Static snippets — the binary path is left as a
  // placeholder so the user can copy / paste without
  // having to substitute shell expansion.
  const claudeSnippet = JSON.stringify(
    {
      mcpServers: {
        pentestswarm: {
          command: "pentestswarm",
          args: ["mcp", "serve", "--config", "/etc/pentestswarm/orchestrator.yaml"],
        },
      },
    },
    null,
    2,
  );

  const sseSnippet = JSON.stringify(
    {
      mcpServers: {
        pentestswarm: {
          url: "http://localhost:8089/mcp/sse",
          transport: "sse",
        },
      },
    },
    null,
    2,
  );

  return (
    <div>
      <h4 className="font-display text-sm font-semibold text-text-primary mb-2">
        配置片段
      </h4>

      <SnippetBlock
        title="Claude Desktop — stdio（推荐）"
        body={claudeSnippet}
        hint="保存到：~/Library/Application Support/Claude/claude_desktop_config.json (macOS) 或 ~/.config/Claude/claude_desktop_config.json (Linux)。"
      />

      <SnippetBlock
        title="Claude Desktop / Cursor — SSE"
        body={sseSnippet}
        hint="在主机执行 `pentestswarm mcp serve --transport=sse --addr=:8089`，然后让客户端连接上述 URL。"
      />
    </div>
  );
}

function SnippetBlock({ title, body, hint }: { title: string; body: string; hint: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="mb-3">
      <div className="flex items-center justify-between mb-1">
        <span className="text-[11px] font-mono text-text-muted">{title}</span>
        <button
          type="button"
          onClick={() => {
            void navigator.clipboard.writeText(body).then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 1200);
            });
          }}
          className="text-[10px] font-mono px-2 py-0.5 border border-white/10 rounded hover:bg-white/5"
        >
          {copied ? "已复制" : "复制"}
        </button>
      </div>
      <pre className="bg-black/40 border border-white/5 rounded p-3 overflow-x-auto text-[11px] font-mono text-text-secondary">
{body}
      </pre>
      <p className="text-[10px] font-mono text-text-muted mt-1">{hint}</p>
    </div>
  );
}
