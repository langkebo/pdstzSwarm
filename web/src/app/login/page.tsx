"use client";

import { Suspense, useEffect, useState, type FormEvent } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { GlassPanel } from "@/components/GlassPanel";
import { MatrixRain } from "@/components/MatrixRain";
import { SwarmOrbit } from "@/components/SwarmOrbit";
import { api, type OAuthProviderDescriptor } from "@/lib/api";
import { cn } from "@/lib/cn";

/**
 * Login screen — first impression of the product. Composed of:
 *   1. A persistent dark backdrop with subtle matrix rain and a
 *      hairline grid.
 *   2. A 60/40 split: left half hosts the brand + animated swarm
 *      topology (replaces the 3D robot from PTAgent); right half
 *      hosts the glass-strong form card.
 *   3. ASCII corner brackets frame the form. A small "build hash +
 *      uptime" mono caption sits below the form for that
 *      "production console" feel.
 *
 * The actual auth call is wired through /api/v1/auth/login, with
 * mock fallback when the endpoint is unreachable so dev still
 * lets the user in (see handleSubmit).
 */
export default function LoginPage() {
  // useSearchParams() forces a CSR bailout under `output: export`. We
  // wrap the form in Suspense so the static build can finish; the
  // fallback is a tiny glass panel placeholder.
  return (
    <Suspense
      fallback={
        <div className="min-h-screen flex items-center justify-center">
          <div className="font-mono text-xs uppercase tracking-widest text-text-muted">
            <span className="text-accent">▸</span> 正在加载认证
            <span className="animate-blink-cursor">▌</span>
          </div>
        </div>
      }
    >
      <LoginInner />
    </Suspense>
  );
}

function LoginInner() {
  const router = useRouter();
  const search = useSearchParams();

  const [username, setUsername] = useState("operator");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [remember, setRemember] = useState(true);
  const [providers, setProviders] = useState<OAuthProviderDescriptor[]>([]);

  // Auto-clear error when the user starts editing again.
  useEffect(() => {
    if (!error) return;
    const t = setTimeout(() => setError(null), 6000);
    return () => clearTimeout(t);
  }, [error]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    if (!username.trim() || !password) {
      setError("请输入用户名和密码");
      return;
    }

    setSubmitting(true);
    try {
      await api.auth.login(username, password, remember);
      // Cookie is set by the server. Redirect to the requested target
      // or the default campaigns list.
      const redirect = search.get("redirect") || "/campaigns";
      router.push(redirect);
    } catch (err) {
      // Surface the auth error to the user. We deliberately do NOT
      // fall through in dev — the user should see "wrong password"
      // even on the local box. (Demo creds are documented in
      // /docs/optimization/p5-plus-auth-delivery-report.md.)
      const message = err instanceof Error ? err.message : String(err);
      setError(`AUTH_FAILED :: ${message.slice(0, 80)}`);
    } finally {
      setSubmitting(false);
    }
  };

  const handleOAuth = (provider: OAuthProviderDescriptor) => {
    const redirect = search.get("redirect") || "/campaigns";
    window.location.assign(api.auth.oauthBeginURL(provider.name, redirect));
  };

  // Read ?error=… set by the OAuth callback when something went
  // wrong (e.g. ?error=oauth_failed, ?error=missing_state_or_code).
  useEffect(() => {
    const errCode = search.get("error");
    if (errCode) {
      const map: Record<string, string> = {
        oauth_failed: "OAuth 流程失败，请重试或使用密码登录。",
        missing_state_or_code: "OAuth 回调缺少必需参数。",
      };
      setError(`SSO 错误 :: ${map[errCode] ?? errCode}`);
    }
  }, [search]);

  // Discover the OAuth providers configured on the server. If the
  // endpoint is unreachable, we simply render no buttons.
  useEffect(() => {
    let cancelled = false;
    api.auth
      .providers()
      .then((r) => {
        if (!cancelled) setProviders(r.providers ?? []);
      })
      .catch(() => {
        if (!cancelled) setProviders([]);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <div className="relative min-h-screen overflow-hidden bg-background text-text-primary">
      {/* Layered backdrop: deep navy → radial gradient → hairline grid
          → CRT scanlines → matrix rain. Each layer is independent so
          we can tune their blend modes individually. */}
      <div
        aria-hidden
        className="absolute inset-0 -z-30"
        style={{
          background:
            "radial-gradient(ellipse at 20% 0%, rgba(0,255,156,0.08) 0%, transparent 55%), radial-gradient(ellipse at 80% 100%, rgba(96,165,250,0.10) 0%, transparent 60%), #0A0A0F",
        }}
      />
      <div aria-hidden className="absolute inset-0 -z-20 bg-tech-grid opacity-50" />
      <div aria-hidden className="absolute inset-0 -z-10 crt-scanlines" />
      <MatrixRain
        className="absolute inset-0 -z-10 w-full h-full opacity-50"
        opacity={0.18}
      />

      {/* Top status bar — subtle, but adds to the "console" feeling. */}
      <header className="absolute top-0 inset-x-0 z-10 flex items-center justify-between px-6 py-3 text-[10px] font-mono uppercase tracking-[0.2em] text-text-muted">
        <div className="flex items-center gap-3">
          <span className="status-dot-online" />
          <span>psa-console :: 安全会话</span>
        </div>
        <div className="hidden sm:flex items-center gap-4">
          <span>tls v1.3</span>
          <span>区域：cn-shanghai-1</span>
          <span className="text-accent">build #a1b2c3d</span>
        </div>
      </header>

      {/* 60 / 40 split */}
      <main className="relative z-0 grid min-h-screen lg:grid-cols-[3fr_2fr]">
        {/* LEFT — brand & hero */}
        <section className="hidden lg:flex flex-col justify-between p-10 pr-16">
          <div />
          <div className="space-y-8 animate-fade-in">
            <div>
              <p className="text-xs font-mono uppercase tracking-[0.3em] text-accent">
                ◤ PENTESTSWARM_AI · CONSOLE v2.0 ◢
              </p>
              <h1 className="mt-4 font-display text-5xl xl:text-6xl font-bold leading-[1.05]">
                <span className="text-gradient-cyan">自主化</span>
                <br />
                <span className="text-text-primary">渗透测试</span>
                <br />
                <span className="text-text-primary">集群规模执行</span>
              </h1>
              <p className="mt-6 max-w-md text-text-secondary font-mono text-sm leading-relaxed">
                5 个专项代理 · 1 个编排器 · 端到端侦察、利用与报告——实时呈现。
              </p>
            </div>

            <div className="relative">
              <SwarmOrbit size={360} className="w-full max-w-[360px] h-auto" />
            </div>

            <div className="grid grid-cols-3 gap-4 max-w-md">
              <Stat label="任务数" value="1,284" />
              <Stat label="发现数" value="34.6k" />
              <Stat label="代理数" value="5/5" />
            </div>
          </div>
          <div className="text-[10px] font-mono uppercase tracking-[0.2em] text-text-muted flex items-center gap-3">
            <span>© 2026 PentestSwarm Labs</span>
            <span className="opacity-50">|</span>
            <span>security@pentestswarm.ai</span>
          </div>
        </section>

        {/* RIGHT — login form */}
        <section className="flex items-center justify-center p-6 sm:p-10">
          <GlassPanel
            variant="strong"
            className="w-full max-w-md p-8 sm:p-10 animate-slide-in-up"
          >
            <FormHeader />

            <form className="mt-6 space-y-4" onSubmit={handleSubmit} noValidate>
              <div>
                <label htmlFor="username" className="label-cyber">
                  ⟨ 用户名 / 操作员编号 ⟩
                </label>
                <input
                  id="username"
                  name="username"
                  type="text"
                  autoComplete="username"
                  spellCheck={false}
                  className="input-cyber"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  aria-invalid={!!error}
                />
              </div>

              <div>
                <label htmlFor="password" className="label-cyber">
                  ⟨ passphrase ⟩
                </label>
                <div className="relative">
                  <input
                    id="password"
                    name="password"
                    type={showPassword ? "text" : "password"}
                    autoComplete="current-password"
                    spellCheck={false}
                    className="input-cyber pr-16"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    aria-invalid={!!error}
                  />
                  <button
                    type="button"
                    onClick={() => setShowPassword((s) => !s)}
                    className="absolute inset-y-0 right-0 px-3 text-[10px] font-mono uppercase tracking-widest text-text-muted hover:text-accent"
                    aria-label={showPassword ? "隐藏密码" : "显示密码"}
                  >
                    {showPassword ? "隐藏" : "显示"}
                  </button>
                </div>
              </div>

              <div className="flex items-center justify-between pt-1">
                <label className="flex items-center gap-2 cursor-pointer select-none">
                  <span
                    className={cn(
                      "w-4 h-4 rounded-sm border flex items-center justify-center transition-colors",
                      remember
                        ? "bg-accent/20 border-accent text-accent"
                        : "bg-background border-border-strong"
                    )}
                  >
                    {remember && (
                      <svg
                        viewBox="0 0 12 12"
                        className="w-3 h-3"
                        aria-hidden
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="2"
                      >
                        <path d="M2 6.5 5 9.5 10 3" />
                      </svg>
                    )}
                  </span>
                  <input
                    type="checkbox"
                    className="sr-only"
                    checked={remember}
                    onChange={(e) => setRemember(e.target.checked)}
                  />
                  <span className="text-[11px] font-mono uppercase tracking-widest text-text-secondary">
                    记住会话
                  </span>
                </label>
                <a
                  href="/forgot"
                  className="text-[11px] font-mono uppercase tracking-widest text-text-muted hover:text-accent"
                >
                  忘记密码？
                </a>
              </div>

              {/* Inline error slot. Always rendered so the layout
                  doesn't jump when the error appears. */}
              <div
                role="alert"
                aria-live="polite"
                className={cn(
                  "min-h-[36px] text-[11px] font-mono uppercase tracking-widest",
                  error
                    ? "text-severity-critical opacity-100"
                    : "opacity-0"
                )}
              >
                {error ?? "—"}
              </div>

              <button
                type="submit"
                disabled={submitting}
                className="btn-cyber-solid w-full py-3 text-sm"
              >
                {submitting ? (
                  <>
                    <Spinner />
                    正在认证…
                  </>
                ) : (
                  <>
                    <span>⟨ 启动会话</span>
                    <span>⟩</span>
                  </>
                )}
              </button>
            </form>

            <FormFooter providers={providers} onOAuth={handleOAuth} />
          </GlassPanel>
        </section>
      </main>
    </div>
  );
}

function FormHeader() {
  return (
    <header className="space-y-3">
      <div className="flex items-center justify-between">
        <p className="text-[10px] font-mono uppercase tracking-[0.3em] text-accent">
          ◢ 安全登录 v2.0
        </p>
        <p className="text-[10px] font-mono uppercase tracking-[0.3em] text-text-muted">
          [A]
        </p>
      </div>
      <h2 className="font-display text-2xl font-bold text-text-primary">
        <span className="text-gradient-cyan">PSA</span>
        <span className="text-text-primary"> 控制台</span>
      </h2>
      <p className="text-xs font-mono text-text-secondary">
        身份验证后即可调度集群。
      </p>
    </header>
  );
}

function FormFooter({
  providers = [],
  onOAuth,
}: {
  providers?: OAuthProviderDescriptor[];
  onOAuth?: (p: OAuthProviderDescriptor) => void;
}) {
  return (
    <footer className="mt-6 pt-5 border-t border-border/60 space-y-3">
      <div className="flex items-center justify-between text-[10px] font-mono uppercase tracking-widest text-text-muted">
        <span>构建版本 #a1b2c3d</span>
        <span>运行时长 47 天 12 时 03 分</span>
      </div>
      {providers.length > 0 ? (
        <div className="space-y-2">
          <p className="text-[10px] font-mono uppercase tracking-[0.2em] text-text-muted">
            <span className="text-accent">▸</span> sso / oauth 2.0
          </p>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
            {providers.map((p) => (
              <button
                key={p.name}
                type="button"
                onClick={() => onOAuth?.(p)}
                className="flex items-center justify-center gap-2 rounded border border-border bg-background/40 px-3 py-2 text-[11px] font-mono uppercase tracking-widest text-text-secondary hover:border-accent hover:text-accent transition-colors"
                aria-label={`使用 ${p.label} 登录`}
              >
                <ProviderGlyph name={p.name} />
                <span>使用 {p.label} 登录</span>
              </button>
            ))}
          </div>
        </div>
      ) : (
        <p className="text-[10px] font-mono text-text-muted">
          <span className="text-accent">▸</span> sso / oauth 2.0 —{" "}
          <span className="text-text-secondary">未配置</span>
        </p>
      )}
    </footer>
  );
}

function ProviderGlyph({ name }: { name: string }) {
  // Tiny inline glyphs for the two providers we ship. Fallback: a
  // generic square — extensible when we add more providers.
  if (name === "google") {
    return (
      <svg viewBox="0 0 24 24" className="w-3.5 h-3.5" aria-hidden>
        <path fill="#4285F4" d="M21.6 12.227c0-.79-.07-1.55-.2-2.28H12v4.32h5.39a4.61 4.61 0 0 1-2 3.03v2.51h3.23c1.89-1.74 2.98-4.31 2.98-7.58z" />
        <path fill="#34A853" d="M12 22c2.7 0 4.96-.9 6.62-2.43l-3.23-2.51c-.9.6-2.04.96-3.39.96-2.6 0-4.81-1.76-5.6-4.12H3.06v2.59A10 10 0 0 0 12 22z" />
        <path fill="#FBBC05" d="M6.4 13.9a6.01 6.01 0 0 1 0-3.8V7.5H3.06a10 10 0 0 0 0 9l3.34-2.6z" />
        <path fill="#EA4335" d="M12 6.4c1.47 0 2.79.5 3.83 1.5l2.86-2.86A10 10 0 0 0 3.06 7.5L6.4 10.1C7.19 7.74 9.4 6 12 6.4z" />
      </svg>
    );
  }
  if (name === "github") {
    return (
      <svg viewBox="0 0 24 24" className="w-3.5 h-3.5" aria-hidden fill="currentColor">
        <path d="M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.11.79-.25.79-.55v-2.04c-3.2.69-3.88-1.37-3.88-1.37-.52-1.33-1.27-1.69-1.27-1.69-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.18 1.76 1.18 1.02 1.75 2.69 1.24 3.34.95.1-.74.4-1.24.72-1.52-2.55-.29-5.24-1.27-5.24-5.66 0-1.25.45-2.27 1.18-3.07-.12-.29-.51-1.46.11-3.04 0 0 .96-.31 3.15 1.18a10.9 10.9 0 0 1 5.74 0c2.19-1.49 3.15-1.18 3.15-1.18.62 1.58.23 2.75.11 3.04.74.8 1.18 1.82 1.18 3.07 0 4.4-2.69 5.36-5.26 5.65.41.36.78 1.06.78 2.14v3.17c0 .31.21.67.8.55A11.5 11.5 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5z" />
      </svg>
    );
  }
  return <span className="w-3.5 h-3.5 rounded-sm border border-current opacity-50" />;
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="glass-panel rounded-md p-3">
      <p className="text-[10px] font-mono uppercase tracking-[0.18em] text-text-muted">
        {label}
      </p>
      <p className="mt-1 font-mono text-lg font-bold text-accent tabular-nums">
        {value}
      </p>
    </div>
  );
}

function Spinner() {
  return (
    <svg
      className="w-3.5 h-3.5 animate-spin"
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden
    >
      <circle
        cx="12"
        cy="12"
        r="9"
        stroke="currentColor"
        strokeOpacity="0.25"
        strokeWidth="3"
      />
      <path
        d="M21 12a9 9 0 0 1-9 9"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
      />
    </svg>
  );
}
