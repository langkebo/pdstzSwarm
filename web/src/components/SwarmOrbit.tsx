"use client";

import { useEffect, useState } from "react";

interface SwarmOrbitProps {
  size?: number;
  className?: string;
}

/**
 * SwarmOrbit — a stylized SVG visualization of the Pentest-Swarm
 * topology: a central orchestrator with N satellite agents connected
 * by animated "data packets". Replaces PTAgent's 3D robot on the
 * login hero, and reinforces the "swarm" brand.
 *
 * The animation is CSS-only (SMIL fallback is omitted for SSR
 * friendliness). The orbit positions are deterministic so the
 * server-rendered SVG matches the client one.
 */
export function SwarmOrbit({ size = 360, className }: SwarmOrbitProps) {
  const [tick, setTick] = useState(0);
  // Light, single-shot re-render trigger for the packet dots.
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1500);
    return () => clearInterval(id);
  }, []);

  const cx = size / 2;
  const cy = size / 2;
  const orbitR = size * 0.34;
  const agentCount = 6;
  const agents = Array.from({ length: agentCount }, (_, i) => {
    const angle = (i / agentCount) * Math.PI * 2 - Math.PI / 2;
    return {
      x: cx + Math.cos(angle) * orbitR,
      y: cy + Math.sin(angle) * orbitR,
    };
  });

  return (
    <svg
      viewBox={`0 0 ${size} ${size}`}
      className={className}
      role="img"
      aria-label="集群拓扑"
    >
      <defs>
        <radialGradient id="swarm-core-glow" cx="50%" cy="50%" r="50%">
          <stop offset="0%" stopColor="#00FF9C" stopOpacity="0.6" />
          <stop offset="60%" stopColor="#00FF9C" stopOpacity="0.1" />
          <stop offset="100%" stopColor="#00FF9C" stopOpacity="0" />
        </radialGradient>
        <linearGradient id="swarm-link" x1="0%" y1="0%" x2="100%" y2="0%">
          <stop offset="0%" stopColor="#00FF9C" stopOpacity="0.1" />
          <stop offset="50%" stopColor="#60A5FA" stopOpacity="0.6" />
          <stop offset="100%" stopColor="#00FF9C" stopOpacity="0.1" />
        </linearGradient>
        <filter id="swarm-glow" x="-50%" y="-50%" width="200%" height="200%">
          <feGaussianBlur stdDeviation="2" result="blur" />
          <feMerge>
            <feMergeNode in="blur" />
            <feMergeNode in="SourceGraphic" />
          </feMerge>
        </filter>
      </defs>

      {/* Outer concentric guides */}
      <circle
        cx={cx}
        cy={cy}
        r={orbitR}
        fill="none"
        stroke="#1A1A2E"
        strokeDasharray="2 4"
      />
      <circle
        cx={cx}
        cy={cy}
        r={orbitR * 0.7}
        fill="none"
        stroke="#1A1A2E"
        strokeDasharray="1 6"
      />

      {/* Core glow halo */}
      <circle cx={cx} cy={cy} r={size * 0.18} fill="url(#swarm-core-glow)" />

      {/* Agent links + traveling packets */}
      {agents.map((a, i) => {
        const dur = 1.4 + (i % 3) * 0.3;
        return (
          <g key={`link-${i}`}>
            <line
              x1={cx}
              y1={cy}
              x2={a.x}
              y2={a.y}
              stroke="url(#swarm-link)"
              strokeWidth="1"
            />
            <circle r="2.5" fill="#00FF9C" filter="url(#swarm-glow)">
              <animate
                attributeName="cx"
                values={`${cx};${a.x};${cx}`}
                dur={`${dur}s`}
                repeatCount="indefinite"
                begin={`${(i * 0.3).toFixed(2)}s`}
              />
              <animate
                attributeName="cy"
                values={`${cy};${a.y};${cy}`}
                dur={`${dur}s`}
                repeatCount="indefinite"
                begin={`${(i * 0.3).toFixed(2)}s`}
              />
              <animate
                attributeName="opacity"
                values="0;1;1;0"
                dur={`${dur}s`}
                repeatCount="indefinite"
                begin={`${(i * 0.3).toFixed(2)}s`}
              />
            </circle>
            <circle
              r="1.2"
              fill="#60A5FA"
              filter="url(#swarm-glow)"
              key={`pkt-${i}-${tick}`}
            >
              <animate
                attributeName="cx"
                values={`${a.x};${cx}`}
                dur={`${dur * 0.9}s`}
                repeatCount="indefinite"
                begin={`${((i + 0.5) * 0.3).toFixed(2)}s`}
              />
              <animate
                attributeName="cy"
                values={`${a.y};${cy}`}
                dur={`${dur * 0.9}s`}
                repeatCount="indefinite"
                begin={`${((i + 0.5) * 0.3).toFixed(2)}s`}
              />
              <animate
                attributeName="opacity"
                values="0;0.8;0.8;0"
                dur={`${dur * 0.9}s`}
                repeatCount="indefinite"
                begin={`${((i + 0.5) * 0.3).toFixed(2)}s`}
              />
            </circle>
          </g>
        );
      })}

      {/* Core node */}
      <g>
        <circle cx={cx} cy={cy} r={20} fill="#0A0A0F" stroke="#00FF9C" strokeWidth="1.5" />
        <circle cx={cx} cy={cy} r={6} fill="#00FF9C" filter="url(#swarm-glow)">
          <animate
            attributeName="r"
            values="5;7;5"
            dur="2.4s"
            repeatCount="indefinite"
          />
        </circle>
        <text
          x={cx}
          y={cy + 38}
          textAnchor="middle"
          fill="#94A3B8"
          fontFamily="JetBrains Mono, monospace"
          fontSize="9"
          letterSpacing="2"
        >
          编排器
        </text>
      </g>

      {/* Agent nodes */}
      {agents.map((a, i) => (
        <g key={`agent-${i}`}>
          <circle
            cx={a.x}
            cy={a.y}
            r={11}
            fill="#12121A"
            stroke="#60A5FA"
            strokeWidth="1"
          />
          <circle cx={a.x} cy={a.y} r={2.5} fill="#60A5FA" filter="url(#swarm-glow)" />
          <text
            x={a.x}
            y={a.y + 24}
            textAnchor="middle"
            fill="#64748B"
            fontFamily="JetBrains Mono, monospace"
            fontSize="8"
            letterSpacing="1"
          >
            A{(i + 1).toString().padStart(2, "0")}
          </text>
        </g>
      ))}
    </svg>
  );
}
