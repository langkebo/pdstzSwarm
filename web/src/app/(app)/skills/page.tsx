"use client";

import { useEffect, useState, useMemo } from "react";
import { GlassPanel } from "@/components/GlassPanel";
import { TopBar } from "@/components/TopBar";
import { cn } from "@/lib/cn";
import { api, type SkillItem, type SkillCategoryStat } from "@/lib/api";
import { Search, ExternalLink, Github, X, ChevronRight } from "lucide-react";

/* ------------------------------------------------------------------ */
/* Category definitions (must mirror internal/skills/category.go)      */
/* ------------------------------------------------------------------ */

interface CategoryInfo {
  key: string;
  glyph: string;
  label: string;
}

const ALL_CATEGORIES: CategoryInfo[] = [
  { key: "code_audit",  glyph: "🔒", label: "代码审计" },
  { key: "pentest",     glyph: "⚔️", label: "渗透测试" },
  { key: "reverse_engineering", glyph: "🔍", label: "逆向工程" },
  { key: "ctf",         glyph: "🏆", label: "CTF竞赛" },
  { key: "threat_modeling", glyph: "🎯", label: "威胁建模" },
  { key: "mobile_security", glyph: "📱", label: "移动安全" },
  { key: "incident_response", glyph: "🚨", label: "应急响应" },
  { key: "security_tools", glyph: "🛡️", label: "安全工具" },
];

/* ------------------------------------------------------------------ */
/* Component                                                           */
/* ------------------------------------------------------------------ */

export default function SkillsPage() {
  const [skills, setSkills] = useState<SkillItem[]>([]);
  const [stats, setStats] = useState<SkillCategoryStat[]>([]);
  const [registrySize, setRegistrySize] = useState(0);
  const [loading, setLoading] = useState(true);
  const [category, setCategory] = useState<string>("all");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<SkillItem | null>(null);

  // Initial fetch — list all skills + stats.
  useEffect(() => {
    let cancelled = false;
    async function load() {
      setLoading(true);
      try {
        const [listRes, statsRes] = await Promise.all([
          api.skills.list(),
          api.skills.stats(),
        ]);
        if (cancelled) return;
        setSkills(listRes.data);
        setRegistrySize(listRes.meta.registry);
        setStats(statsRes.categories);
      } catch {
        // API not available — show empty state.
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    return () => { cancelled = true; };
  }, []);

  // Re-fetch when category or query changes.
  useEffect(() => {
    let cancelled = false;
    async function reload() {
      setLoading(true);
      try {
        const params: { category?: string; q?: string } = {};
        if (category !== "all") params.category = category;
        if (query.trim()) params.q = query.trim();
        const res = await api.skills.list(params);
        if (cancelled) return;
        setSkills(res.data);
      } catch {
        /* ignore */
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    reload();
    return () => { cancelled = true; };
  }, [category, query]);

  // Debounced search.
  const [draft, setDraft] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setQuery(draft), 300);
    return () => clearTimeout(timer);
  }, [draft]);

  // Compute category counts from stats.
  const catCounts = useMemo(() => {
    const m: Record<string, number> = {};
    for (const s of stats) m[s.category] = s.count;
    return m;
  }, [stats]);

  const totalCount = skills.length;

  return (
    <div className="min-h-screen flex flex-col">
      <TopBar
        title="Skill 市场"
        subtitle={`${registrySize} 个社区 Skill · ${ALL_CATEGORIES.length} 个领域 · 来自 openclaw-sec-skills 社区`}
      />

      <div className="flex flex-1 overflow-hidden">
        {/* Main panel */}
        <div
          className={cn(
            "flex-1 overflow-auto p-6 space-y-4 animate-fade-in",
            selected && "hidden md:block"
          )}
        >
          {/* Filters row */}
          <div className="flex flex-col sm:flex-row items-start sm:items-center gap-3">
            {/* Category tabs */}
            <div className="flex items-center gap-2 flex-wrap flex-1">
              <button
                onClick={() => setCategory("all")}
                className={cn(
                  "px-3 py-1 rounded-md font-mono text-xs uppercase tracking-widest border transition-colors",
                  category === "all"
                    ? "border-accent text-accent bg-accent/10"
                    : "border-border text-text-secondary hover:text-text-primary"
                )}
                type="button"
              >
                全部
                <span className="ml-1 text-text-muted">({registrySize})</span>
              </button>
              {ALL_CATEGORIES.map((c) => (
                <button
                  key={c.key}
                  onClick={() => setCategory(c.key)}
                  className={cn(
                    "px-3 py-1 rounded-md font-mono text-xs uppercase tracking-widest border transition-colors",
                    category === c.key
                      ? "border-accent text-accent bg-accent/10"
                      : "border-border text-text-secondary hover:text-text-primary"
                  )}
                  type="button"
                >
                  {c.glyph} {c.label}
                  {(catCounts[c.key] ?? 0) > 0 && (
                    <span className="ml-1 text-text-muted">
                      ({catCounts[c.key]})
                    </span>
                  )}
                </button>
              ))}
            </div>

            {/* Search */}
            <div className="relative w-full sm:w-64">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-text-muted" />
              <input
                type="text"
                placeholder="搜索 Skill..."
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                className={cn(
                  "w-full rounded-md border border-border bg-background/40",
                  "pl-8 pr-3 py-1.5 font-mono text-xs text-text-primary",
                  "placeholder:text-text-muted focus:outline-none focus:border-accent/50",
                  "transition-colors"
                )}
              />
              {draft && (
                <button
                  onClick={() => { setDraft(""); setQuery(""); }}
                  className="absolute right-2 top-1/2 -translate-y-1/2"
                  type="button"
                >
                  <X className="w-3 h-3 text-text-muted hover:text-text-primary" />
                </button>
              )}
            </div>
          </div>

          {/* Result count */}
          <p className="font-mono text-[11px] text-text-muted">
            {loading
              ? "加载中…"
              : query
              ? `搜索 "${query}"：找到 ${totalCount} 个 Skill`
              : `共 ${totalCount} 个 Skill`}
          </p>

          {/* Skill grid */}
          {loading && skills.length === 0 ? (
            <div className="grid grid-cols-1 gap-3">
              {[1, 2, 3, 4, 5].map((i) => (
                <GlassPanel key={i} variant="soft" className="p-4 h-24 animate-pulse">
                  <div className="h-3 bg-surface-hover rounded w-1/3 mb-2" />
                  <div className="h-3 bg-surface-hover rounded w-2/3" />
                </GlassPanel>
              ))}
            </div>
          ) : skills.length === 0 ? (
            <GlassPanel variant="soft" className="p-8 text-center">
              <p className="font-mono text-sm text-text-muted">没有找到匹配的 Skill</p>
              <p className="font-mono text-xs text-text-muted mt-1">
                尝试更换分类或搜索词
              </p>
            </GlassPanel>
          ) : (
            <div className="grid grid-cols-1 gap-3">
              {skills.map((sk) => {
                const catInfo = ALL_CATEGORIES.find((c) => c.key === sk.category);
                return (
                  <button
                    key={sk.name}
                    onClick={() => setSelected(sk)}
                    type="button"
                    className="text-left w-full"
                  >
                    <GlassPanel
                      variant="soft"
                      className="p-4 hover:border-accent/40 transition-colors group w-full"
                      frame
                      frameTag={`skill.${sk.name}`}
                    >
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center gap-2 mb-1">
                            <span className="text-sm">{catInfo?.glyph ?? "📦"}</span>
                            <h3 className="font-display text-sm font-semibold text-text-primary truncate">
                              {sk.name}
                            </h3>
                            <span className="text-[10px] font-mono text-text-muted border border-border rounded px-1.5 shrink-0">
                              {catInfo?.label ?? sk.category}
                            </span>
                          </div>
                          <p className="text-[11px] font-mono text-text-muted leading-snug line-clamp-2">
                            {sk.description}
                          </p>
                          {sk.tags && sk.tags.length > 0 && (
                            <div className="flex items-center gap-1.5 mt-2 flex-wrap">
                              {sk.tags.slice(0, 5).map((t) => (
                                <span
                                  key={t}
                                  className="px-1.5 py-0.5 text-[9px] font-mono rounded-sm bg-surface-hover text-text-secondary border border-border"
                                >
                                  {t}
                                </span>
                              ))}
                              {sk.tags.length > 5 && (
                                <span className="text-[9px] font-mono text-text-muted">
                                  +{sk.tags.length - 5}
                                </span>
                              )}
                            </div>
                          )}
                        </div>
                        <ChevronRight className="w-4 h-4 text-text-muted group-hover:text-accent shrink-0 mt-1 transition-colors" />
                      </div>
                    </GlassPanel>
                  </button>
                );
              })}
            </div>
          )}
        </div>

        {/* Detail panel */}
        {selected && (
          <div className="w-[420px] shrink-0 border-l border-border overflow-auto p-6 animate-fade-in">
            <div className="flex items-center justify-between mb-4">
              <h2 className="font-display text-sm font-semibold text-text-primary truncate flex-1 mr-2">
                {selected.name}
              </h2>
              <button
                onClick={() => setSelected(null)}
                className="text-text-muted hover:text-text-primary transition-colors"
                type="button"
              >
                <X className="w-4 h-4" />
              </button>
            </div>

            {/* Category badge */}
            {(() => {
              const ci = ALL_CATEGORIES.find((c) => c.key === selected.category);
              return (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-sm font-mono text-[10px] border border-border text-text-secondary mb-3">
                  {ci?.glyph} {ci?.label ?? selected.category}
                </span>
              );
            })()}

            {/* Description */}
            <p className="font-mono text-[12px] text-text-secondary leading-relaxed mb-4">
              {selected.description}
            </p>

            {/* Source */}
            <div className="mb-4">
              <p className="font-mono text-[10px] uppercase tracking-widest text-text-muted mb-1.5">
                来源
              </p>
              <a
                href={selected.source}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1.5 font-mono text-[11px] text-accent hover:underline break-all"
              >
                {selected.source_type === "github" ? (
                  <Github className="w-3.5 h-3.5" />
                ) : (
                  <ExternalLink className="w-3.5 h-3.5" />
                )}
                {selected.source_label || selected.source}
              </a>
            </div>

            {/* Tags */}
            {selected.tags && selected.tags.length > 0 && (
              <div className="mb-4">
                <p className="font-mono text-[10px] uppercase tracking-widest text-text-muted mb-1.5">
                  标签
                </p>
                <div className="flex flex-wrap gap-1">
                  {selected.tags.map((t) => (
                    <span
                      key={t}
                      onClick={() => {
                        setDraft(t);
                        setSelected(null);
                      }}
                      className="px-2 py-0.5 text-[10px] font-mono rounded-sm bg-accent/10 text-accent border border-accent/30 cursor-pointer hover:bg-accent/20 transition-colors"
                      title={`搜索标签: ${t}`}
                    >
                      {t}
                    </span>
                  ))}
                </div>
              </div>
            )}

            {/* Quick actions */}
            <div className="space-y-2">
              <p className="font-mono text-[10px] uppercase tracking-widest text-text-muted mb-1">
                操作
              </p>
              <a
                href={selected.source}
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center justify-center gap-2 w-full rounded-md border border-border bg-background/40 px-3 py-2 font-mono text-[11px] text-text-secondary hover:border-accent/40 hover:text-accent transition-colors"
              >
                <ExternalLink className="w-3.5 h-3.5" />
                查看源代码
              </a>
              <button
                onClick={() => {
                  navigator.clipboard.writeText(selected.name);
                }}
                type="button"
                className="flex items-center justify-center gap-2 w-full rounded-md border border-border bg-background/40 px-3 py-2 font-mono text-[11px] text-text-secondary hover:border-accent/40 hover:text-accent transition-colors"
              >
                复制 Skill 名称
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}