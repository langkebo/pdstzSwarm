"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { GlassPanel } from "@/components/GlassPanel";
import { TopBar } from "@/components/TopBar";
import { useDashboardStore } from "@/lib/store";
import { severityFromFinding, SEVERITY_COLOR, SEVERITY_LABEL, type Severity } from "@/lib/severity";
import { cn } from "@/lib/cn";

interface GraphNode {
  id: string;
  label: string;
  type: "host" | "service" | "cve" | "tool" | "agent";
  severity?: Severity;
}
interface GraphEdge {
  from: string;
  to: string;
  label: string;
}

const TYPE_COLORS: Record<GraphNode["type"], string> = {
  host: "#60A5FA",
  service: "#00FF9C",
  cve: "#FF3366",
  tool: "#A78BFA",
  agent: "#FFC107",
};

const TYPE_LABELS: Record<GraphNode["type"], string> = {
  host: "主机",
  service: "服务",
  cve: "漏洞",
  tool: "工具",
  agent: "代理",
};

export default function GraphPage() {
  const findings = useDashboardStore((s) => s.findings);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const animRef = useRef<number | null>(null);

  // Derive a small mock graph from findings. In v2.0 phase 2 this is
  // replaced by the real graph store; for now we project the
  // dashboard data through a few hand-written heuristics so the
  // panel always renders something useful.
  const { nodes, edges } = useMemo(() => buildGraph(findings), [findings]);

  const [hover, setHover] = useState<GraphNode | null>(null);
  const [selected, setSelected] = useState<GraphNode | null>(null);
  const [filter, setFilter] = useState<Set<GraphNode["type"]>>(
    new Set(["host", "service", "cve", "tool", "agent"])
  );

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);

    const state: {
      positions: Record<string, { x: number; y: number; vx: number; vy: number }>;
      dragging: GraphNode | null;
      dragOffset: { x: number; y: number } | null;
      transform: { zoom: number; tx: number; ty: number };
      hover: GraphNode | null;
      selected: GraphNode | null;
    } = {
      positions: {},
      dragging: null,
      dragOffset: null,
      transform: { zoom: 1, tx: 0, ty: 0 },
      hover: null,
      selected: null,
    };

    // Initialize positions in a circle.
    const r = 220;
    nodes.forEach((n, i) => {
      const angle = (i / Math.max(1, nodes.length)) * Math.PI * 2;
      state.positions[n.id] = {
        x: Math.cos(angle) * r,
        y: Math.sin(angle) * r,
        vx: 0,
        vy: 0,
      };
    });

    const resize = () => {
      const rect = canvas.getBoundingClientRect();
      canvas.width = Math.floor(rect.width * dpr);
      canvas.height = Math.floor(rect.height * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    };
    resize();
    const ro = new ResizeObserver(resize);
    ro.observe(canvas);

    const toWorld = (cx: number, cy: number) => {
      const rect = canvas.getBoundingClientRect();
      return {
        x: (cx - rect.width / 2 - state.transform.tx) / state.transform.zoom,
        y: (cy - rect.height / 2 - state.transform.ty) / state.transform.zoom,
      };
    };

    const nodeAt = (cx: number, cy: number) => {
      const w = toWorld(cx, cy);
      let best: GraphNode | null = null;
      let bestD = 20;
      for (const n of nodes) {
        if (!filter.has(n.type)) continue;
        const p = state.positions[n.id];
        const d = Math.hypot(p.x - w.x, p.y - w.y);
        if (d < bestD) {
          bestD = d;
          best = n;
        }
      }
      return best;
    };

    const tick = () => {
      const rect = canvas.getBoundingClientRect();
      ctx.save();
      ctx.clearRect(0, 0, rect.width, rect.height);
      ctx.translate(rect.width / 2 + state.transform.tx, rect.height / 2 + state.transform.ty);
      ctx.scale(state.transform.zoom, state.transform.zoom);

      // Light physics: edges pull nodes together, all nodes repel.
      const force = 0.02;
      const linkLen = 120;
      for (const n of nodes) {
        if (!filter.has(n.type)) continue;
        const p = state.positions[n.id];
        for (const m of nodes) {
          if (m.id === n.id) continue;
          if (!filter.has(m.type)) continue;
          const q = state.positions[m.id];
          const dx = q.x - p.x;
          const dy = q.y - p.y;
          const d2 = dx * dx + dy * dy + 0.01;
          if (d2 < 80 * 80) {
            const f = -50 / d2;
            p.vx += dx * f;
            p.vy += dy * f;
          }
        }
      }
      for (const e of edges) {
        const a = state.positions[e.from];
        const b = state.positions[e.to];
        if (!a || !b) continue;
        const dx = b.x - a.x;
        const dy = b.y - a.y;
        const d = Math.hypot(dx, dy) + 0.01;
        const f = ((d - linkLen) * force) / d;
        a.vx += dx * f;
        a.vy += dy * f;
        b.vx -= dx * f;
        b.vy -= dy * f;
      }

      // Edges
      ctx.lineWidth = 1;
      for (const e of edges) {
        if (!filter.has(nodes.find((n) => n.id === e.from)?.type ?? "host")) continue;
        if (!filter.has(nodes.find((n) => n.id === e.to)?.type ?? "host")) continue;
        const a = state.positions[e.from];
        const b = state.positions[e.to];
        if (!a || !b) continue;
        const active = state.hover && (state.hover.id === e.from || state.hover.id === e.to);
        ctx.strokeStyle = active ? "rgba(0, 255, 156, 0.7)" : "rgba(96, 165, 250, 0.2)";
        ctx.beginPath();
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
        ctx.stroke();
      }

      // Nodes
      for (const n of nodes) {
        if (!filter.has(n.type)) continue;
        const p = state.positions[n.id];
        const isActive = state.hover?.id === n.id || state.selected?.id === n.id;
        const color = n.severity ? SEVERITY_COLOR[n.severity] : TYPE_COLORS[n.type];
        ctx.beginPath();
        ctx.arc(p.x, p.y, isActive ? 8 : 6, 0, Math.PI * 2);
        ctx.fillStyle = color;
        ctx.shadowColor = color;
        ctx.shadowBlur = isActive ? 16 : 6;
        ctx.fill();
        ctx.shadowBlur = 0;
        if (isActive) {
          ctx.font = "10px JetBrains Mono, monospace";
          ctx.fillStyle = "#E2E8F0";
          ctx.fillText(n.label, p.x + 10, p.y - 8);
        }
        // Step position
        p.vx *= 0.85;
        p.vy *= 0.85;
        if (state.dragging?.id !== n.id) {
          p.x += p.vx;
          p.y += p.vy;
        }
      }

      ctx.restore();
      animRef.current = requestAnimationFrame(tick);
    };

    const onPointer = (e: PointerEvent) => {
      const rect = canvas.getBoundingClientRect();
      const cx = e.clientX - rect.left;
      const cy = e.clientY - rect.top;
      const n = nodeAt(cx, cy);
      state.hover = n;
      setHover(n);
      if (e.type === "pointerdown") {
        if (n) {
          state.dragging = n;
          setSelected(n);
          state.selected = n;
          const w = toWorld(cx, cy);
          state.dragOffset = { x: w.x - state.positions[n.id].x, y: w.y - state.positions[n.id].y };
          canvas.setPointerCapture(e.pointerId);
        }
      } else if (e.type === "pointermove" && state.dragging) {
        const w = toWorld(cx, cy);
        state.positions[state.dragging.id].x = w.x - (state.dragOffset?.x ?? 0);
        state.positions[state.dragging.id].y = w.y - (state.dragOffset?.y ?? 0);
      } else if (e.type === "pointerup") {
        state.dragging = null;
        state.dragOffset = null;
      }
    };
    canvas.addEventListener("pointerdown", onPointer);
    canvas.addEventListener("pointermove", onPointer);
    canvas.addEventListener("pointerup", onPointer);
    canvas.addEventListener("pointerleave", () => {
      state.hover = null;
      setHover(null);
    });

    animRef.current = requestAnimationFrame(tick);

    return () => {
      if (animRef.current != null) cancelAnimationFrame(animRef.current);
      ro.disconnect();
      canvas.removeEventListener("pointerdown", onPointer);
      canvas.removeEventListener("pointermove", onPointer);
      canvas.removeEventListener("pointerup", onPointer);
    };
  }, [nodes, edges, filter]);

  const toggle = (t: GraphNode["type"]) => {
    setFilter((cur) => {
      const next = new Set(cur);
      if (next.has(t)) next.delete(t);
      else next.add(t);
      return next;
    });
  };

  return (
    <div className="min-h-screen flex flex-col">
      <TopBar
        title="知识图谱"
        subtitle={`${nodes.length} 节点 · ${edges.length} 边 · 拖拽可探索`}
        actions={
          <button
            className="btn-ghost"
            type="button"
            onClick={() => setFilter(new Set(["host", "service", "cve", "tool", "agent"]))}
          >
            ⟲ 重置筛选
          </button>
        }
      />

      <div className="flex-1 grid grid-cols-1 lg:grid-cols-[1fr_320px] gap-0 overflow-hidden">
        <div className="relative bg-background">
          <canvas ref={canvasRef} className="absolute inset-0 w-full h-full" />
          {/* Filter chips */}
          <div className="absolute top-3 left-3 flex gap-2 flex-wrap">
            {(["host", "service", "cve", "tool", "agent"] as GraphNode["type"][]).map(
              (t) => (
                <button
                  key={t}
                  onClick={() => toggle(t)}
                  className={cn(
                    "px-2 py-1 rounded-sm border font-mono text-[10px] uppercase tracking-widest",
                    filter.has(t)
                      ? "border-accent/40 bg-background/80 text-text-primary"
                      : "border-border bg-background/40 text-text-muted"
                  )}
                  style={filter.has(t) ? { color: TYPE_COLORS[t] } : undefined}
                  type="button"
                >
                  <span
                    className="inline-block w-1.5 h-1.5 rounded-full mr-1.5 align-middle"
                    style={{ backgroundColor: TYPE_COLORS[t] }}
                  />
                  {TYPE_LABELS[t]}
                </button>
              )
            )}
          </div>
          {/* Legend */}
          <div className="absolute bottom-3 left-3 font-mono text-[10px] text-text-muted">
            <span className="text-accent">▸</span> 悬停高亮 ·
            <span className="ml-2">拖拽移动 · 单击固定</span>
          </div>
          {hover && (
            <div className="absolute top-3 right-3 px-2.5 py-1.5 rounded-md bg-background/80 border border-border backdrop-blur font-mono text-[11px]">
              <span className="text-text-muted">悬停：</span>{" "}
              <span className="text-accent">{hover.label}</span>
              <span className="text-text-muted">
                （{TYPE_LABELS[hover.type]}）
              </span>
            </div>
          )}
        </div>

        <aside className="border-l border-border bg-background/60 p-4 overflow-y-auto">
          <GlassPanel variant="soft" className="p-3" frame frameTag="graph.detail">
            <h3 className="font-display text-sm font-semibold text-text-primary mb-2">
              节点详情
            </h3>
            {selected ? (
              <dl className="text-[11px] font-mono space-y-1.5">
                <Row k="编号" v={selected.id} />
                <Row k="名称" v={selected.label} />
                <Row k="类型" v={TYPE_LABELS[selected.type]} />
                {selected.severity && (
                  <Row k="严重度" v={SEVERITY_LABEL[selected.severity]} />
                )}
                <div className="pt-2 mt-2 border-t border-border/60">
                  <p className="text-text-muted text-[10px] uppercase tracking-widest mb-1">
                    关联节点
                  </p>
                  <ul className="space-y-1">
                    {edges
                      .filter((e) => e.from === selected.id || e.to === selected.id)
                      .map((e, i) => (
                        <li key={i} className="text-text-secondary text-[10px]">
                          <span className="text-accent-2">
                            {e.from === selected.id ? "→" : "←"}
                          </span>{" "}
                          {e.label}{" "}
                          <span className="text-text-muted">
                            （{e.from === selected.id ? e.to : e.from}）
                          </span>
                        </li>
                      ))}
                  </ul>
                </div>
              </dl>
            ) : (
              <p className="text-xs text-text-muted font-mono">
                单击节点可查看其关联信息。
              </p>
            )}
          </GlassPanel>
        </aside>
      </div>
    </div>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex items-baseline gap-2">
      <dt className="text-text-muted uppercase tracking-widest text-[10px] w-20 flex-shrink-0">
        {k}
      </dt>
      <dd className="text-text-primary break-all">{v}</dd>
    </div>
  );
}

/**
 * buildGraph — derive a tiny graph from the finding stream. The
 * projection is intentionally simple: a finding's target becomes a
 * `host` node (or `service` when type=HTTP_ENDPOINT), the type
 * becomes a `tool` node, and CVE/SESSION/EXPLOIT_SUCCESS become
 * `cve` nodes attached via labeled edges. This gives the panel
 * something to render even with no upstream graph store.
 */
function buildGraph(findings: import("@/lib/api").Finding[]): {
  nodes: GraphNode[];
  edges: GraphEdge[];
} {
  const nodes = new Map<string, GraphNode>();
  const edges: GraphEdge[] = [];
  const seenEdge = new Set<string>();

  const addNode = (n: GraphNode) => {
    if (!nodes.has(n.id)) nodes.set(n.id, n);
  };
  const addEdge = (e: GraphEdge) => {
    const k = `${e.from}|${e.to}|${e.label}`;
    if (seenEdge.has(k)) return;
    seenEdge.add(k);
    edges.push(e);
  };

  // Always-present swarm agent nodes.
  ["orchestrator", "recon", "classifier", "exploit", "report"].forEach((a) => {
    addNode({ id: `agent:${a}`, label: a, type: "agent" });
  });

  for (const f of findings) {
    const sev = severityFromFinding(f);
    const targetId = `host:${f.target}`;
    if (f.type === "HTTP_ENDPOINT") {
      addNode({ id: `service:${f.target}`, label: f.target, type: "service", severity: sev });
      addEdge({ from: `host:${f.target}`, to: `service:${f.target}`, label: "暴露" });
    } else if (f.type === "CVE_MATCH") {
      const cve = (f.data as Record<string, unknown>)?.cve_id ?? f.target;
      addNode({ id: `cve:${cve}`, label: String(cve), type: "cve", severity: "critical" });
      addNode({ id: targetId, label: f.target, type: "host", severity: sev });
      addEdge({ from: `cve:${cve}`, to: targetId, label: "影响" });
    } else {
      addNode({ id: targetId, label: f.target, type: "host", severity: sev });
    }
    addNode({ id: `tool:${f.type || "unknown"}`, label: f.type || "unknown", type: "tool" });
    addEdge({ from: `tool:${f.type || "unknown"}`, to: targetId, label: "已发现" });
    if (f.agent_name) {
      addEdge({ from: `agent:${f.agent_name.toLowerCase()}`, to: targetId, label: "已扫描" });
    }
  }

  return { nodes: [...nodes.values()], edges };
}
