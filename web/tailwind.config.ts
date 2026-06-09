import type { Config } from "tailwindcss";

const config: Config = {
  darkMode: "class",
  content: ["./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // Surface ladder — used by glass-panel blends
        background: "#0A0A0F",
        surface: "#12121A",
        "surface-2": "#161624",
        "surface-hover": "#1E1E30",
        border: "#1A1A2E",
        "border-strong": "#2A2A40",
        "border-active": "#60A5FA",

        // Accent ladder — hacker signal green + electric blue
        accent: "#00FF9C",
        "accent-dim": "#00B872",
        "accent-2": "#60A5FA",
        "accent-3": "#A78BFA",

        // Text ladder — slate on near-black
        "text-primary": "#E2E8F0",
        "text-secondary": "#94A3B8",
        "text-muted": "#64748B",
        "text-code": "#00FF9C",

        // Severity ramp — kept centralized so charts, badges, and
        // status pills never disagree.
        severity: {
          critical: "#FF3366",
          high: "#FF6B35",
          medium: "#FFC107",
          low: "#00FF9C",
          info: "#6B7280",
        },
      },
      fontFamily: {
        display: [
          "var(--font-space-grotesk)",
          "Space Grotesk",
          "ui-sans-serif",
          "system-ui",
          "sans-serif",
        ],
        body: [
          "var(--font-inter)",
          "Inter",
          "ui-sans-serif",
          "system-ui",
          "sans-serif",
        ],
        mono: [
          "var(--font-jetbrains-mono)",
          "JetBrains Mono",
          "Fira Code",
          "ui-monospace",
          "monospace",
        ],
        retro: [
          "var(--font-vt323)",
          "VT323",
          "ui-monospace",
          "monospace",
        ],
      },
      boxShadow: {
        // Soft cyan glow used on focus rings / hover halos
        "glow-cyan":
          "0 0 0 1px rgba(0, 255, 156, 0.4), 0 0 24px rgba(0, 255, 156, 0.18)",
        "glow-blue":
          "0 0 0 1px rgba(96, 165, 250, 0.4), 0 0 24px rgba(96, 165, 250, 0.18)",
        "inset-soft": "inset 0 1px 0 0 rgba(255, 255, 255, 0.04)",
      },
      backgroundImage: {
        // Diagonal hairline grid used as a subtle base layer
        "grid-hairline":
          "linear-gradient(rgba(255,255,255,0.03) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,0.03) 1px, transparent 1px)",
        "gradient-radial":
          "radial-gradient(ellipse at top, rgba(96,165,250,0.10), transparent 60%)",
        "gradient-cyan":
          "linear-gradient(135deg, #00FF9C 0%, #60A5FA 100%)",
      },
      backgroundSize: {
        "grid-32": "32px 32px",
      },
      animation: {
        "pulse-soft": "pulse-soft 2.4s ease-in-out infinite",
        "scan-line": "scan-line 8s linear infinite",
        "blink-cursor": "blink-cursor 1s steps(2) infinite",
        "matrix-fall": "matrix-fall 12s linear infinite",
        "status-pulse": "status-pulse 1.6s ease-in-out infinite",
        "fade-in": "fade-in 320ms cubic-bezier(0.16, 1, 0.3, 1) both",
        "slide-in-right": "slide-in-right 240ms cubic-bezier(0.16, 1, 0.3, 1) both",
        "slide-in-up": "slide-in-up 320ms cubic-bezier(0.16, 1, 0.3, 1) both",
      },
      keyframes: {
        "pulse-soft": {
          "0%, 100%": { opacity: "0.6" },
          "50%": { opacity: "1" },
        },
        "scan-line": {
          "0%": { transform: "translateY(-100%)" },
          "100%": { transform: "translateY(100%)" },
        },
        "blink-cursor": {
          "0%, 100%": { opacity: "1" },
          "50%": { opacity: "0" },
        },
        "matrix-fall": {
          "0%": { transform: "translateY(-100%)" },
          "100%": { transform: "translateY(100%)" },
        },
        "status-pulse": {
          "0%, 100%": {
            boxShadow: "0 0 0 0 rgba(0, 255, 156, 0.55)",
          },
          "50%": {
            boxShadow: "0 0 0 6px rgba(0, 255, 156, 0)",
          },
        },
        "fade-in": {
          "0%": { opacity: "0", transform: "translateY(4px)" },
          "100%": { opacity: "1", transform: "translateY(0)" },
        },
        "slide-in-right": {
          "0%": { opacity: "0", transform: "translateX(16px)" },
          "100%": { opacity: "1", transform: "translateX(0)" },
        },
        "slide-in-up": {
          "0%": { opacity: "0", transform: "translateY(12px)" },
          "100%": { opacity: "1", transform: "translateY(0)" },
        },
      },
    },
  },
  plugins: [],
};

export default config;
