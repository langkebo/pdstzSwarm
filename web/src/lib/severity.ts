// Severity mapping for blackboard.FindingType → UI severity.
//
// Single source of truth for "how serious is this finding". Replaces
// the previous behavior of substring-matching the event detail
// (`event.detail?.includes("CRITICAL")`) which broke the moment an
// agent changed its logging vocabulary.
//
// Adding a new FindingType? You must add it here. The exhaustive
// switch in `severityFromType` will fail the build until you do.

import type { FindingType } from "./api";

export type Severity = "critical" | "high" | "medium" | "low" | "informational";

/** Visual + textual label per severity. */
export const SEVERITY_LABEL: Record<Severity, string> = {
  critical: "严重",
  high: "高危",
  medium: "中危",
  low: "低危",
  informational: "提示",
};

/**
 * Tailwind / CSS color tokens per severity. Centralized so the same
 * colors show on the donut, the finding list, and the badge in the
 * campaign header.
 */
export const SEVERITY_COLOR: Record<Severity, string> = {
  critical: "#dc2626", // red-600
  high: "#ea580c", // orange-600
  medium: "#ca8a04", // yellow-600
  low: "#2563eb", // blue-600
  informational: "#6b7280", // gray-500
};

/**
 * Compute the UI severity for a given blackboard.FindingType.
 *
 * Rationale per group:
 *
 *   CRITICAL — exploitable success, exposed credentials, matched CVE
 *   HIGH     — confirmed vulnerabilities, session established
 *   MEDIUM   — internal recon signals, likely attack surface
 *   LOW      — generic endpoints, minor reconnaissance
 *   INFO     — task/campaign bookkeeping
 */
export function severityFromType(type: FindingType | string): Severity {
  switch (type) {
    case "EXPLOIT_SUCCESS":
    case "CREDENTIAL":
    case "CVE_MATCH":
      return "critical";

    case "VULNERABILITY":
    case "SESSION":
    case "SECRET_FOUND":
      return "high";

    case "HTTP_ENDPOINT":
    case "JS_FILE":
    case "PIVOT":
      return "medium";

    case "PORT_OPEN":
    case "SUBDOMAIN":
      return "low";

    case "EXPLOIT_FAIL":
    case "REPORT":
    case "TASK_COMPLETE":
    case "CAMPAIGN_COMPLETE":
    case "AGENT_ERROR":
      return "informational";

    default:
      // Unknown type — be loud (orange = "high") so the operator
      // notices and adds the type to the switch above. Silent "info"
      // is the worst default for a security product.
      return "high";
  }
}

/**
 * Aggregate findings into a per-severity count map. Returns all five
 * keys (zero-filled) so the donut chart never has a missing slice.
 */
export function countBySeverity<S extends string>(
  severities: readonly S[]
): Record<S, number> {
  const out = {} as Record<S, number>;
  for (const s of severities) {
    out[s] = (out[s] ?? 0) + 1;
  }
  return out;
}

/**
 * Resolve severity from a Finding that may come from either the
 * blackboard (has `type`) or the ClassifiedFinding path (has `severity`).
 * ClassifiedFinding.severity takes precedence when available.
 */
export function severityFromFinding(f: { type?: string; severity?: string; attack_category?: string }): Severity {
  // ClassifiedFinding path: severity is explicit
  if (f.severity) {
    const s = f.severity.toLowerCase();
    if (s === "critical") return "critical";
    if (s === "high") return "high";
    if (s === "medium") return "medium";
    if (s === "low") return "low";
    if (s === "info" || s === "informational") return "informational";
  }
  // Blackboard path: derive from type
  if (f.type) {
    return severityFromType(f.type);
  }
  // Fallback from attack_category
  if (f.attack_category) {
    const cat = f.attack_category.toLowerCase();
    if (cat.includes("rce") || cat.includes("remote_code")) return "critical";
    if (cat.includes("critical") || cat.includes("injection") || cat.includes("sqli")) return "high";
    if (cat.includes("xss") || cat.includes("auth") || cat.includes("ssrf")) return "high";
    if (cat.includes("path_traversal") || cat.includes("xxe") || cat.includes("idor")) return "high";
    if (cat.includes("port") || cat.includes("info") || cat.includes("tech") || cat.includes("recon")) return "medium";
    if (cat.includes("subdomain") || cat.includes("redirect")) return "medium";
  }
  return "medium";
}
