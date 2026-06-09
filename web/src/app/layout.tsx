import type { Metadata } from "next";
import { Inter, JetBrains_Mono, Space_Grotesk, VT323 } from "next/font/google";
import "./globals.css";
// P5+ 终端 — xterm.js 设计令牌覆盖（与项目信号绿/玻璃感对齐）
import "@xterm/xterm/css/xterm.css";
import "@/styles/terminal.css";

// Self-hosted via next/font so we don't pay the runtime cost of an
// extra <link> request and we sidestep the no-page-custom-font
// lint rule. Each font only requests the weights we actually use.
const inter = Inter({
  subsets: ["latin"],
  weight: ["400", "500", "600", "700"],
  variable: "--font-inter",
  display: "swap",
});
const jbMono = JetBrains_Mono({
  subsets: ["latin"],
  weight: ["400", "500", "700"],
  variable: "--font-jetbrains-mono",
  display: "swap",
});
const grotesk = Space_Grotesk({
  subsets: ["latin"],
  weight: ["500", "600", "700"],
  variable: "--font-space-grotesk",
  display: "swap",
});
const vt323 = VT323({
  subsets: ["latin"],
  weight: ["400"],
  variable: "--font-vt323",
  display: "swap",
});

export const metadata: Metadata = {
  title: "渗透测试集群 AI — 控制台",
  description:
    "自主 AI 驱动的渗透测试平台 — 编排、实时操作与知识库一体化。",
};

/**
 * Root layout. Provides global fonts and CSS only — the sidebar
 * shell lives in app/(app)/layout.tsx so that /login can opt out
 * of the rail.
 */
export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html
      lang="zh-CN"
      className={`${inter.variable} ${jbMono.variable} ${grotesk.variable} ${vt323.variable} dark`}
    >
      <body className="min-h-screen bg-background text-text-primary antialiased">
        {children}
      </body>
    </html>
  );
}
