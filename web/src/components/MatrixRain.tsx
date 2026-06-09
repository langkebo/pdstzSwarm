"use client";

import { useEffect, useRef } from "react";

interface MatrixRainProps {
  /** Override opacity. Default 0.18 — kept subtle so the foreground
   *  text remains readable. */
  opacity?: number;
  /** Disable animation, draw a single static frame. Used for SSR /
   *  reduced-motion fallback. */
  staticFrame?: boolean;
  className?: string;
}

/**
 * MatrixRain — a lightweight canvas matrix-rain effect. We avoid
 * `requestAnimationFrame` callbacks when the user has prefers-reduced-motion
 * enabled (the page-level CSS rule kills animations, so we just render
 * one frame and stop). The character set is intentionally limited to
 * "01<>/{}" to keep the visual coherent with the hacker aesthetic.
 */
const CHARSET = "01<>{}[]|/\\=+*abcdefABCDEF0123456789";

export function MatrixRain({
  opacity = 0.18,
  staticFrame = false,
  className,
}: MatrixRainProps) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const rafRef = useRef<number | null>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    let width = 0;
    let height = 0;
    let cols = 0;
    let drops: number[] = [];

    const resize = () => {
      const rect = canvas.getBoundingClientRect();
      width = rect.width;
      height = rect.height;
      canvas.width = Math.floor(width * dpr);
      canvas.height = Math.floor(height * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      cols = Math.max(1, Math.floor(width / 16));
      drops = new Array(cols).fill(0).map(() => Math.random() * height);
    };
    resize();
    const ro = new ResizeObserver(resize);
    ro.observe(canvas);

    const draw = () => {
      // Translucent black overlay creates the trail. The alpha is what
      // controls how long the streak persists per frame.
      ctx.fillStyle = "rgba(10, 10, 15, 0.08)";
      ctx.fillRect(0, 0, width, height);

      ctx.font = "13px JetBrains Mono, monospace";
      ctx.textBaseline = "top";

      for (let i = 0; i < cols; i++) {
        const ch = CHARSET.charAt(Math.floor(Math.random() * CHARSET.length));
        const x = i * 16;
        const y = drops[i];
        // Leading char — bright cyan
        ctx.fillStyle = `rgba(0, 255, 156, ${opacity})`;
        ctx.fillText(ch, x, y);
        // Trail of dim green above the head
        ctx.fillStyle = `rgba(0, 184, 114, ${opacity * 0.55})`;
        ctx.fillText(ch, x, y - 16);

        if (y > height && Math.random() > 0.975) {
          drops[i] = -16;
        }
        drops[i] += 16;
      }
      rafRef.current = requestAnimationFrame(draw);
    };

    const drawStatic = () => {
      ctx.fillStyle = "rgba(10, 10, 15, 1)";
      ctx.fillRect(0, 0, width, height);
      ctx.font = "13px JetBrains Mono, monospace";
      ctx.textBaseline = "top";
      for (let i = 0; i < cols; i++) {
        const ch = CHARSET.charAt(Math.floor(Math.random() * CHARSET.length));
        const x = i * 16;
        const y = Math.random() * height;
        ctx.fillStyle = `rgba(0, 255, 156, ${opacity})`;
        ctx.fillText(ch, x, y);
      }
    };

    if (staticFrame) {
      drawStatic();
    } else {
      rafRef.current = requestAnimationFrame(draw);
    }

    return () => {
      if (rafRef.current != null) cancelAnimationFrame(rafRef.current);
      ro.disconnect();
    };
  }, [opacity, staticFrame]);

  return <canvas ref={canvasRef} className={className} aria-hidden />;
}
