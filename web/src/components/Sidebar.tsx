"use client";

import { useEffect, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import { api, type AuthUser } from "@/lib/api";
import { getCachedUser, fetchUser, setCachedUser } from "@/lib/auth-cache";
import { cn } from "@/lib/cn";

interface NavItem {
  href: string;
  label: string;
  glyph: string;
  /** A short tag rendered next to the label, e.g. "LIVE". */
  tag?: string;
}

const NAV: NavItem[] = [
  { href: "/", label: "仪表盘", glyph: "◐" },
  { href: "/campaigns", label: "任务", glyph: "⌬" },
  { href: "/findings", label: "发现", glyph: "☰" },
  { href: "/graph", label: "图谱", glyph: "⬢" },
  { href: "/live", label: "实时操作", glyph: "▶", tag: "实时" },
  { href: "/skills", label: "Skill 市场", glyph: "✦", tag: "NEW" },
  { href: "/reports", label: "报告", glyph: "≣", tag: "P5+" },
  { href: "/settings", label: "设置", glyph: "⚙" },
];

export function Sidebar() {
  const pathname = usePathname();
  const router = useRouter();
  const [user, setUser] = useState<AuthUser | null>(null);
  const [loggingOut, setLoggingOut] = useState(false);

  // Fetch the current user once on mount. If the session is invalid
  // Use cached user from AuthGuard if available, otherwise fetch.
  // This avoids a duplicate API call on page load.
  useEffect(() => {
    const cached = getCachedUser();
    if (cached) {
      setUser(cached);
      return;
    }
    let cancelled = false;
    fetchUser()
      .then((user) => {
        if (!cancelled) {
          setCachedUser(user);
          setUser(user);
        }
      })
      .catch(() => {
        if (!cancelled) setUser(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function handleLogout() {
    setLoggingOut(true);
    try {
      await api.auth.logout();
    } finally {
      setUser(null);
      setLoggingOut(false);
      router.push("/login");
    }
  }
  return (
    <aside className="w-60 bg-background/60 border-r border-border flex flex-col backdrop-blur-md">
      {/* Brand */}
      <div className="px-4 py-4 border-b border-border">
        <div className="flex items-center gap-2.5">
          <div className="w-8 h-8 rounded border border-accent/50 bg-accent/10 flex items-center justify-center text-accent font-mono text-sm font-bold shadow-glow-cyan">
            ⌬
          </div>
          <div className="leading-tight">
            <p className="font-display font-bold text-text-primary text-sm tracking-wide">
              PentestSwarm
            </p>
            <p className="text-[10px] font-mono uppercase tracking-[0.18em] text-text-muted">
              AI 控制台 · v2.0
            </p>
          </div>
        </div>
      </div>

      {/* Nav */}
      <nav className="flex-1 p-2 space-y-0.5" aria-label="主导航">
        {NAV.map((item) => {
          const active =
            item.href === "/"
              ? pathname === "/"
              : pathname.startsWith(item.href);
          return (
            <a
              key={item.href}
              href={item.href}
              aria-current={active ? "page" : undefined}
              className={cn(
                "group flex items-center gap-3 px-3 py-2 rounded-md",
                "font-mono text-xs uppercase tracking-wider",
                "transition-colors duration-150",
                active
                  ? "bg-accent/10 text-accent border border-accent/30 shadow-[inset_0_0_0_1px_rgba(0,255,156,0.08)]"
                  : "text-text-secondary border border-transparent hover:text-text-primary hover:bg-surface-hover hover:border-border"
              )}
            >
              <span
                className={cn(
                  "w-5 text-center text-base",
                  active ? "text-accent" : "text-text-muted group-hover:text-accent-2"
                )}
              >
                {item.glyph}
              </span>
              <span className="flex-1">{item.label}</span>
              {item.tag && (
                <span className="px-1.5 py-0.5 text-[9px] tracking-widest rounded-sm bg-accent/15 text-accent border border-accent/40 animate-pulse-soft">
                  {item.tag}
                </span>
              )}
              {active && (
                <span className="w-1 h-1 rounded-full bg-accent shadow-[0_0_6px_rgba(0,255,156,0.7)]" />
              )}
            </a>
          );
        })}
      </nav>

      {/* System status footer */}
      <div className="p-3 border-t border-border space-y-2">
        <div className="flex items-center gap-2 text-[10px] font-mono uppercase tracking-widest text-text-muted">
          <span className="status-dot-online" />
          <span>系统运行中</span>
        </div>
        <div className="text-[10px] font-mono text-text-muted leading-relaxed">
          <div>
            <span className="text-text-secondary">构建版本</span>{" "}
            <span className="text-text-primary">v2.1-dev</span>
          </div>
        </div>
      </div>

      {/* User menu — P5+ 鉴权闭环 */}
      <UserMenu user={user} onLogout={handleLogout} loggingOut={loggingOut} />
    </aside>
  );
}

/**
 * UserMenu shows the currently authenticated user at the bottom of
 * the sidebar. When no user is logged in (e.g. the /auth/me call
 * failed because the cookie expired) it renders a "sign in" link.
 */
function UserMenu({
  user,
  onLogout,
  loggingOut,
}: {
  user: AuthUser | null;
  onLogout: () => void;
  loggingOut: boolean;
}) {
  if (!user) {
    return (
      <div className="p-3 border-t border-border">
        <a
          href="/login"
          className="flex items-center justify-center gap-2 rounded border border-border bg-background/40 px-3 py-2 text-[10px] font-mono uppercase tracking-widest text-text-secondary hover:border-accent hover:text-accent transition-colors"
        >
          ⟨ 登录
        </a>
      </div>
    );
  }
  const initials =
    (user.display_name || user.username || "?").slice(0, 2).toUpperCase();
  return (
    <div className="p-3 border-t border-border space-y-2">
      <div className="flex items-center gap-2.5">
        <div
          className="w-7 h-7 rounded-full bg-accent/15 border border-accent/40 text-accent font-mono text-[10px] font-bold flex items-center justify-center"
          aria-hidden
        >
          {initials}
        </div>
        <div className="min-w-0 leading-tight flex-1">
          <p className="text-[11px] font-mono text-text-primary truncate">
            {user.display_name || user.username}
          </p>
          <p className="text-[9px] font-mono uppercase tracking-widest text-text-muted">
            {user.role} · {user.provider ?? "password"}
          </p>
        </div>
      </div>
      <button
        type="button"
        onClick={onLogout}
        disabled={loggingOut}
        className="w-full rounded border border-border bg-background/40 px-3 py-1.5 text-[10px] font-mono uppercase tracking-widest text-text-secondary hover:border-rose-500/40 hover:text-rose-300 transition-colors disabled:opacity-50"
      >
        {loggingOut ? "正在退出…" : "⟩ 退出登录"}
      </button>
    </div>
  );
}
