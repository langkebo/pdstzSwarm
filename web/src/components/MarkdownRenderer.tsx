"use client";

/**
 * MarkdownRenderer — P5+ Reports Markdown body renderer.
 *
 * Uses `react-markdown` + `remark-gfm` for the actual parsing (the
 * report Markdown includes GFM tables, code-fenced JSON payload
 * blocks, and `details` blocks for raw finding data). The component
 * only does the *styling* — it walks the rendered tree and applies the
 * project design tokens (signal-green accents, glass panel, mono font
 * for code blocks).
 *
 * The component is `dynamic(() => import(...), { ssr: false })`
 * loaded from the page so the `react-markdown` chunk does not bloat
 * the initial dashboard bundle.
 */

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { cn } from "@/lib/cn";

interface MarkdownRendererProps {
  source: string;
  className?: string;
}

export function MarkdownRenderer({ source, className }: MarkdownRendererProps) {
  return (
    <div
      className={cn(
        // Anchor the typography inside the panel. We re-declare the
        // h1/h2/h3 sizes so the report (which uses # / ## / ### for
        // its sections) lines up with the rest of the dashboard's
        // scale, not the default browser-rendered HTML sizes.
        "report-prose text-slate-100",
        "leading-relaxed",
        className,
      )}
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          h1: ({ children, ...props }) => (
            <h1
              {...props}
              className="text-2xl font-semibold text-accent mt-0 mb-4 tracking-tight"
            >
              {children}
            </h1>
          ),
          h2: ({ children, ...props }) => (
            <h2
              {...props}
              className="text-xl font-semibold text-slate-50 mt-8 mb-3 pb-2 border-b border-white/10"
            >
              {children}
            </h2>
          ),
          h3: ({ children, ...props }) => (
            <h3
              {...props}
              className="text-lg font-semibold text-slate-100 mt-6 mb-2"
            >
              {children}
            </h3>
          ),
          p: ({ children, ...props }) => (
            <p {...props} className="text-sm text-slate-300 my-3">
              {children}
            </p>
          ),
          ul: ({ children, ...props }) => (
            <ul
              {...props}
              className="list-disc list-inside text-sm text-slate-300 my-3 space-y-1"
            >
              {children}
            </ul>
          ),
          ol: ({ children, ...props }) => (
            <ol
              {...props}
              className="list-decimal list-inside text-sm text-slate-300 my-3 space-y-1"
            >
              {children}
            </ol>
          ),
          li: ({ children, ...props }) => (
            <li {...props} className="text-sm text-slate-300">
              {children}
            </li>
          ),
          strong: ({ children, ...props }) => (
            <strong {...props} className="font-semibold text-accent">
              {children}
            </strong>
          ),
          em: ({ children, ...props }) => (
            <em {...props} className="italic text-slate-200">
              {children}
            </em>
          ),
          code: ({ node, className: cls, children, ...props }) => {
            // react-markdown v9 dropped the `inline` prop; infer
            // inline-ness from the absence of a block-level parent
            // (`pre`). The parent-child wiring is handled by the
            // pre component below — here we just always emit a `<code>`
            // and let the parent decide its display.
            const isBlock = !!node?.position && (
              (node.position.end.line ?? 0) - (node.position.start.line ?? 0) > 0
            );
            if (isBlock) {
              return (
                <code
                  {...props}
                  className={cn(
                    "block p-3 rounded-md bg-black/40 text-slate-200 text-xs font-mono overflow-x-auto",
                    "border border-white/5",
                    cls,
                  )}
                >
                  {children}
                </code>
              );
            }
            return (
              <code
                {...props}
                className="px-1.5 py-0.5 rounded bg-white/10 text-accent text-xs font-mono"
              >
                {children}
              </code>
            );
          },
          pre: ({ children, ...props }) => (
            <pre
              {...props}
              className="my-4 rounded-md overflow-hidden border border-white/5"
            >
              {children}
            </pre>
          ),
          blockquote: ({ children, ...props }) => (
            <blockquote
              {...props}
              className="border-l-2 border-accent/60 pl-4 my-3 text-sm italic text-slate-400"
            >
              {children}
            </blockquote>
          ),
          a: ({ children, ...props }) => (
            <a
              {...props}
              className="text-accent hover:underline underline-offset-2"
              target="_blank"
              rel="noopener noreferrer"
            >
              {children}
            </a>
          ),
          table: ({ children, ...props }) => (
            <div className="my-4 overflow-x-auto">
              <table
                {...props}
                className="w-full text-sm border-collapse"
              >
                {children}
              </table>
            </div>
          ),
          thead: ({ children, ...props }) => (
            <thead
              {...props}
              className="bg-white/5 border-b border-white/10"
            >
              {children}
            </thead>
          ),
          th: ({ children, ...props }) => (
            <th
              {...props}
              className="text-left px-3 py-2 text-xs font-semibold uppercase tracking-wide text-slate-300"
            >
              {children}
            </th>
          ),
          td: ({ children, ...props }) => (
            <td
              {...props}
              className="px-3 py-2 text-sm text-slate-200 border-b border-white/5"
            >
              {children}
            </td>
          ),
          hr: () => (
            <hr className="my-6 border-t border-white/10" />
          ),
          details: ({ children, ...props }) => (
            <details
              {...props}
              className="my-3 rounded border border-white/5 bg-black/30"
            >
              {children}
            </details>
          ),
          summary: ({ children, ...props }) => (
            <summary
              {...props}
              className="cursor-pointer select-none px-3 py-2 text-xs font-mono text-slate-300 hover:text-accent"
            >
              {children}
            </summary>
          ),
        }}
      >
        {source}
      </ReactMarkdown>
    </div>
  );
}
