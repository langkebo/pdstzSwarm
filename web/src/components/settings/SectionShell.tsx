"use client";

import { ReactNode } from "react";
import { GlassPanel } from "@/components/GlassPanel";

/**
 * SectionShell — the standard frame every settings sub-page
 * uses. Keeps the page body under one consistent visual rhythm
 * (title + optional hint + body) so the rail navigation is
 * the only thing that changes between routes.
 */
export function SectionShell({
  title,
  hint,
  children,
  tag,
}: {
  title: string;
  hint?: string;
  children: ReactNode;
  tag?: string;
}) {
  return (
    <GlassPanel variant="soft" className="p-5" frame frameTag={tag ?? `settings.${title.toLowerCase().replace(/\s+/g, "_")}`}>
      <header className="mb-4">
        <h3 className="font-display text-base font-semibold text-text-primary">
          {title}
        </h3>
        {hint && (
          <p className="text-[11px] font-mono text-text-muted mt-1">{hint}</p>
        )}
      </header>
      {children}
    </GlassPanel>
  );
}

export function Field({
  label,
  type = "text",
  placeholder,
  hint,
  value,
  onChange,
}: {
  label: string;
  type?: string;
  placeholder?: string;
  hint?: string;
  value?: string | number;
  onChange?: (e: React.ChangeEvent<HTMLInputElement>) => void;
}) {
  return (
    <label className="block">
      <span className="label-cyber">⟨ {label} ⟩</span>
      <input
        type={type}
        placeholder={placeholder}
        value={value}
        onChange={onChange}
        className="input-cyber"
        autoComplete="off"
        spellCheck={false}
      />
      {hint && (
        <span className="block text-[10px] font-mono text-text-muted mt-1">
          {hint}
        </span>
      )}
    </label>
  );
}
