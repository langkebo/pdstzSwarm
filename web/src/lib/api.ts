// Frontend API surface for Pentest-Swarm-AI.
//
// Aligned with the Go-side blackboard.Finding / ws.Message envelope
// introduced in P0. The previous version used a hard-coded Finding
// shape with severity = "critical" | "high" | ... that the page
// derived by substring-matching the event detail — a fragile
// approach that broke the moment agents stopped writing "CRITICAL"
// into the detail string.
//
// The new shape mirrors blackboard.Finding almost 1:1. Severity is
// derived from the structured Type field via a static map (severity.ts
// in the components/ folder), not from string-matching. This makes the
// wire format the single source of truth.

const API_BASE =
  process.env.NEXT_PUBLIC_API_URL || "/api/v1";

/* ------------------------------------------------------------------ */
/* Domain types — mirror internal/swarm/blackboard/types.go            */
/* ------------------------------------------------------------------ */

export type CampaignStatus =
  | "planned"
  | "initializing"
  | "executing"
  | "complete"
  | "aborted"
  | "failed";

// UserMessage is the operator-issued message envelope sent via
// POST /api/v1/campaigns/:id/input and broadcast on the campaign's
// WebSocket as a `kind: "user_input"` envelope.
//
// The dashboard renders these in two places at once:
//  1. The chat-style input dock (UserInputDock) at the bottom of
//     /live, with the author avatar, the message body, and a
//     relative timestamp. This is the primary operator surface.
//  2. The xterm event stream, prefixed with `▶ user: …`, so the
//     message is interleaved with the agent swarm's output. This
//     lets the operator see "I said X, the swarm then did Y" in
//     a single linear timeline.
export interface UserMessage {
  id: string;
  campaign_id: string;
  author: string;
  text: string;
  timestamp: string;
}

export type CampaignMode = "fast" | "balanced" | "deep" | "stealth";

export interface Campaign {
  id: string;
  name: string;
  target: string;
  objective: string;
  status: CampaignStatus | string;
  mode: CampaignMode | string;
  created_at: string;
  started_at?: string;
  completed_at?: string;
  username?: string;
  password?: string;
}

/**
 * The 16 blackboard.FindingType values, exposed as a string-literal
 * union. Adding a new type on the Go side must be reflected here
 * (and in severity.ts) — we surface a TS error on the build to keep
 * the union exhaustive.
 */
export type FindingType =
  | "PORT_OPEN"
  | "SUBDOMAIN"
  | "HTTP_ENDPOINT"
  | "JS_FILE"
  | "SECRET_FOUND"
  | "VULNERABILITY"
  | "CVE_MATCH"
  | "SESSION"
  | "EXPLOIT_SUCCESS"
  | "EXPLOIT_FAIL"
  | "CREDENTIAL"
  | "REPORT"
  | "PIVOT"
  | "TASK_COMPLETE"
  | "CAMPAIGN_COMPLETE"
  | "AGENT_ERROR";

/** Raw blackboard.Finding as the API returns it. */
export interface Finding {
  id: string;
  campaign_id: string;
  agent_name?: string;
  /** Finding type (from blackboard) or attack_category (from ClassifiedFinding) */
  type?: FindingType | string;
  /** Attack category from classifier (maps to type when blackboard is absent) */
  attack_category?: string;
  target: string;
  /** Title from ClassifiedFinding (e.g. "Port 443 open") */
  title?: string;
  /** Description from ClassifiedFinding */
  description?: string;
  /** Severity from ClassifiedFinding */
  severity?: string;
  /** CVSS score from ClassifiedFinding */
  cvss_score?: number;
  /** Confidence from ClassifiedFinding */
  confidence?: string;
  /** False positive probability from ClassifiedFinding */
  false_positive_probability?: number;
  /** Evidence from ClassifiedFinding */
  evidence?: Array<{ type: string; content: string }>;
  /** Opaque JSON blob with type-specific details (blackboard). */
  data?: Record<string, unknown> | string;
  /** Pheromone weight at the time of read (0.0–1.0). */
  pheromone?: number;
  /** Half-life in seconds for the pheromone decay. */
  half_life_sec?: number;
  created_at?: string;
  classified_at?: string;
}

/**
 * CampaignEvent mirrors internal/pipeline/context.go. Kept for the
 * legacy event path (5-phase runner) so older clients keep working.
 * New blackboard findings flow through the `finding` kind on the
 * WebSocket and should be displayed via the Finding type.
 */
export interface CampaignEvent {
  id: string;
  campaign_id: string;
  timestamp: string;
  event_type: string;
  agent_name: string;
  detail: string;
}

/* ------------------------------------------------------------------ */
/* WebSocket envelope — mirror internal/api/ws/message.go              */
/* ------------------------------------------------------------------ */

export type MessageKind = "event" | "finding" | "user_input" | "snapshot";

export type WSMessage =
  | { kind: "event"; event: CampaignEvent }
  | { kind: "finding"; finding: Finding }
  | { kind: "user_input"; user_input: UserMessage }
  | { kind: "snapshot"; events: CampaignEvent[] };

/** Type guard: is this an event message? */
export function isEventMessage(m: WSMessage): m is { kind: "event"; event: CampaignEvent } {
  return m.kind === "event";
}

/** Type guard: is this a finding message? */
export function isFindingMessage(m: WSMessage): m is { kind: "finding"; finding: Finding } {
  return m.kind === "finding";
}

/** Type guard: is this a user_input message? */
export function isUserInputMessage(
  m: WSMessage,
): m is { kind: "user_input"; user_input: UserMessage } {
  return m.kind === "user_input";
}

/* ------------------------------------------------------------------ */
/* Reports — mirror internal/reports/types.go                          */
/* ------------------------------------------------------------------ */

export type ReportStatus = "queued" | "generating" | "ready" | "failed";
export type ReportFormat = "markdown" | "json";

/** Mirrors reports.Summary on the Go side. */
export interface ReportSummary {
  total_findings: number;
  critical_count: number;
  high_count: number;
  medium_count: number;
  low_count: number;
  info_count: number;
  average_cvss: number;
  overall_risk: string;
  unique_targets: number;
  unique_agents: number;
  duration_seconds: number;
  has_remediation: boolean;
}

/** Mirrors reports.Section on the Go side. */
export interface ReportSection {
  key: string;
  title: string;
  order: number;
  body: string;
}

/** Compact row used in the Reports list page. */
export interface ReportListItem {
  id: string;
  campaign_id: string;
  title: string;
  status: ReportStatus;
  format: ReportFormat;
  byte_size: number;
  error_message?: string;
  created_at: string;
  completed_at?: string;
}

/** Full report record returned by /reports/:id. */
export interface Report extends ReportListItem {
  markdown?: string;
  summary: ReportSummary;
  sections: ReportSection[];
}

/** Lightweight JSON shape returned by /reports/:id/json (no markdown body). */
export interface ReportJSON {
  id: string;
  campaign_id: string;
  title: string;
  summary: ReportSummary;
  sections: ReportSection[];
  created_at: string;
  byte_size: number;
}

/* ------------------------------------------------------------------ */
/* Prompt types (P5+ 提示词编辑) — mirror internal/prompts/types.go     */
/* ------------------------------------------------------------------ */

/** The 35 PromptType slots, surfaced as a string-literal union.
 *  Adding a new type on the Go side MUST be reflected here
 *  (and in the catalogue display). */
export type PromptType =
  | "auth_system" | "auth_recon" | "auth_recon_strict" | "auth_exploit" | "auth_exploit_strict"
  | "api_system"  | "api_recon"  | "api_recon_strict"  | "api_exploit"  | "api_exploit_strict"
  | "web_system"  | "web_recon"  | "web_recon_strict"  | "web_exploit"  | "web_exploit_strict"
  | "cloud_system"| "cloud_recon"| "cloud_recon_strict"| "cloud_exploit"| "cloud_exploit_strict"
  | "classifier" | "classifier_strict"
  | "report"     | "report_strict"
  | "triage"     | "triage_strict"
  | "orchestrator" | "orchestrator_strict"
  | "summarizer" | "summarizer_strict"
  | "refusal_detector"
  | "finalize"   | "finalize_strict"
  | "scope_guard"| "scope_guard_strict"
  | "recon" | "exploit" | "report_alias";

/** List entry from GET /api/v1/prompts. */
export interface PromptSummary {
  type: PromptType;
  description?: string;
  variables?: string[];
  version: number; // 0 = no override, ≥ 1 = user has saved
  updated_at: number;
  updated_by?: string;
}

/** Full entry from GET /api/v1/prompts/:type (includes body). */
export interface PromptDetail extends PromptSummary {
  body: string;
}

/** Status of a managed MCP server. Mirrors mcp.ServerStatus. */
export type MCPServerStatus = "enabled" | "disabled";

/** Per-server status report returned by /api/v1/mcp/servers. */
export interface MCPServerInfo {
  name: string;
  description: string;
  status: MCPServerStatus;
  tool_count: number;
  tools: string[];
  allow?: string[];
  server_info: { name: string; version: string };
}

/** A single tool row from the fan-out catalogue. */
export interface MCPToolInfo {
  name: string;
  description: string;
  inputSchema?: unknown;
}

/* ------------------------------------------------------------------ */
/* Skills (P5+ Skill 市场) — mirror internal/skills/skill.go           */
/* ------------------------------------------------------------------ */

/** A single skill from the community skill index. */
export interface SkillItem {
  name: string;
  description: string;
  category: string;
  source: string;
  source_type: string;
  source_label: string;
  tags?: string[];
  owner?: string;
  repo?: string;
}

/** Category stat from GET /skills/stats. */
export interface SkillCategoryStat {
  category: string;
  display_name: string;
  emoji: string;
  count: number;
}

/** Request body for PUT /api/v1/mcp/servers/:name/allow. */
export interface MCPServerAllowRequest {
  allow: string[]; // empty list = clear (== all tools)
}

/** Request body for PUT /api/v1/mcp/servers/:name/status. */
export interface MCPServerStatusRequest {
  status: MCPServerStatus;
}

/* ------------------------------------------------------------------ */
/* REST client                                                          */
/* ------------------------------------------------------------------ */

async function fetchAPI<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) {
    // Attempt to surface a server-supplied error message without
    // masking the status code.
    const text = await res.text().catch(() => "");
    throw new Error(`API error ${res.status}: ${text || res.statusText}`);
  }
  return res.json() as Promise<T>;
}

/* ------------------------------------------------------------------ */
/* Auth types — P5+ 鉴权闭环                                          */
/* ------------------------------------------------------------------ */

export type AuthRole = "admin" | "operator" | "viewer";

export interface AuthUser {
  id: string;
  username: string;
  email?: string;
  display_name?: string;
  avatar_url?: string;
  role: AuthRole;
  provider?: string;
  created_at: string;
  last_seen_at: string;
}

export interface AuthSessionResponse {
  user: AuthUser;
  expires_at: string;
  issued_at: string;
  oauth_providers: string[];
}

export interface AuthMeResponse {
  user: AuthUser;
  oauth_providers: string[];
}

export interface OAuthProviderDescriptor {
  name: string;
  label: string;
}

export const api = {
  auth: {
    login: (username: string, password: string, remember = true) =>
      fetch(`${API_BASE}/auth/login`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password, remember }),
        credentials: "include",
      }).then(async (r) => {
        if (!r.ok) {
          const text = await r.text().catch(() => "");
          throw new Error(text || `HTTP ${r.status}`);
        }
        return r.json() as Promise<AuthSessionResponse>;
      }),
    logout: () =>
      fetch(`${API_BASE}/auth/logout`, {
        method: "POST",
        credentials: "include",
      }).then(async (r) => {
        if (!r.ok) throw new Error(`Logout failed: HTTP ${r.status}`);
        return r.json();
      }),
    me: () =>
      fetch(`${API_BASE}/auth/me`, { credentials: "include" }).then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json() as Promise<AuthMeResponse>;
      }),
    providers: () =>
      fetchAPI<{ providers: OAuthProviderDescriptor[] }>("/auth/providers"),
    oauthBeginURL: (provider: string, redirect = "/campaigns") =>
      `${API_BASE}/auth/oauth/${encodeURIComponent(provider)}?redirect=${encodeURIComponent(redirect)}`,
  },
  campaigns: {
    list: () => fetchAPI<{ data: Campaign[] }>("/campaigns"),
    get: (id: string) => fetchAPI<Campaign>(`/campaigns/${id}`),
    create: (data: Partial<Campaign>) =>
      fetch(`${API_BASE}/campaigns`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "include",
        body: JSON.stringify(data),
      }).then(async (r) => {
        if (!r.ok) {
          let detail = `${r.status} ${r.statusText}`;
          try {
            const body = await r.json();
            if (body?.error?.message) detail = body.error.message;
          } catch {
            /* non-JSON body, keep the status line */
          }
          throw new Error(detail);
        }
        return r.json();
      }),
    start: (id: string) =>
      fetch(`${API_BASE}/campaigns/${id}/start`, {
        method: "POST",
        credentials: "include",
      }).then(async (r) => {
        if (!r.ok) {
          let detail = `${r.status} ${r.statusText}`;
          try {
            const body = await r.json();
            if (body?.error?.message) detail = body.error.message;
          } catch {
            /* non-JSON body, keep the status line */
          }
          throw new Error(detail);
        }
        return r.json();
      }),
    stop: (id: string) =>
      fetch(`${API_BASE}/campaigns/${id}/stop`, {
        method: "POST",
        credentials: "include",
      }).then(async (r) => {
        if (!r.ok) {
          let detail = `${r.status} ${r.statusText}`;
          try {
            const body = await r.json();
            if (body?.error?.message) detail = body.error.message;
          } catch {
            /* non-JSON body, keep the status line */
          }
          throw new Error(detail);
        }
        return r.json();
      }),
    /**
     * PutUserInput injects an operator-issued message into a
     * running campaign. P5+ 用户输入闭环.
     *
     * Backend returns the canonical UserMessage (server-assigned
     * id, server-assigned timestamp, possibly rewritten author
     * from session). The caller should treat the returned object
     * as the source of truth — the locally-supplied `text` is
     * NOT re-echoed.
     *
     * Throws on 4xx/5xx — caller should surface the error in the
     * input dock (we don't toast in the dock itself; the inline
     * error state is more discoverable).
     */
    input: (id: string, text: string, author?: string) =>
      fetch(`${API_BASE}/campaigns/${id}/input`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "include",
        body: JSON.stringify({ text, author: author ?? "" }),
      }).then(async (r) => {
        if (!r.ok) {
          let detail = `${r.status} ${r.statusText}`;
          try {
            const body = await r.json();
            if (body?.error?.message) detail = body.error.message;
          } catch {
            /* non-JSON body, keep the status line */
          }
          throw new Error(detail);
        }
        return (await r.json()) as UserMessage;
      }),
    /**
     * ListUserInputs returns the operator-issued message log for
     * a campaign, ordered oldest → newest. Used by the input dock
     * to replay history on reconnect (the WebSocket only carries
     * the live tail). Returns an empty array when there are no
     * messages yet — the backend never returns null.
     */
    listUserInputs: (id: string) =>
      fetchAPI<{ data: UserMessage[]; meta: { total: number } }>(
        `/campaigns/${id}/user-inputs`,
      ).then((r) => r.data ?? []),
  },
  findings: {
    list: (campaignId: string) =>
      fetchAPI<{
        data: Finding[];
        meta: { total: number; source: "blackboard" | "memory" };
      }>(`/campaigns/${campaignId}/findings`),
  },
  events: {
    list: (campaignId: string) =>
      fetchAPI<{
        data: CampaignEvent[];
        meta: { total: number };
      }>(`/campaigns/${campaignId}/events`),
  },
  models: {
    list: () => fetchAPI<{ models: string[] }>("/models"),
  },
  stats: () => fetchAPI<Record<string, number>>("/stats"),

  /**
   * Reports API (P5+). Reports are point-in-time renderings of a
   * campaign's findings into Markdown, persisted by the Go reports
   * service. The shapes here mirror internal/reports/types.go 1:1.
   */
  reports: {
    listRecent: (limit = 50) =>
      fetchAPI<{
        data: ReportListItem[];
        meta: { total: number; warning?: string };
      }>(`/reports?limit=${limit}`),
    listByCampaign: (campaignId: string, limit = 50) =>
      fetchAPI<{
        data: ReportListItem[];
        meta: { total: number; warning?: string };
      }>(`/reports/by-campaign/${campaignId}?limit=${limit}`),
    get: (id: string) => fetchAPI<Report>(`/reports/${id}`),
    getJSON: (id: string) =>
      fetchAPI<ReportJSON>(`/reports/${id}/json`),
    /** Returns the raw markdown text. */
    getMarkdown: (id: string) =>
      fetch(`${API_BASE}/reports/${id}/markdown`, { credentials: "include" }).then((r) => {
        if (!r.ok) {
          throw new Error(`API error ${r.status}: ${r.statusText}`);
        }
        return r.text();
      }),
    /** Triggers a new report generation. Returns the new Report record. */
    create: (campaignId: string, title?: string) =>
      fetch(`${API_BASE}/reports`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "include",
        body: JSON.stringify({ campaign_id: campaignId, title: title ?? "" }),
      }).then(async (r) => {
        if (!r.ok) {
          const text = await r.text().catch(() => "");
          throw new Error(`Report creation failed: ${text || `HTTP ${r.status}`}`);
        }
        return r.json() as Promise<Report>;
      }),
    delete: (id: string) =>
      fetch(`${API_BASE}/reports/${id}`, {
        method: "DELETE",
        credentials: "include",
      }).then(async (r) => {
        if (!r.ok) {
          throw new Error(`Delete failed: HTTP ${r.status}`);
        }
        return r.json();
      }),
  },

  /**
   * Prompts API (P5+ 提示词编辑). 35 PromptType slots. The
   * editor uses list() to populate the left rail, get() to
   * load the active template body, put() to save, and delete()
   * to reset to the embedded default.
   */
  prompts: {
    list: () =>
      fetchAPI<{
        count: number;
        total: number;
        prompts: PromptSummary[];
      }>("/prompts"),
    get: (type: string) =>
      fetchAPI<PromptDetail>(`/prompts/${encodeURIComponent(type)}`),
    save: (type: string, body: string) =>
      fetch(`${API_BASE}/prompts/${encodeURIComponent(type)}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ body }),
        credentials: "include",
      }).then((r) => {
        if (!r.ok) {
          return r.text().then((text) => {
            throw new Error(text || `HTTP ${r.status}`);
          });
        }
        return r.json() as Promise<PromptDetail>;
      }),
    reset: (type: string) =>
      fetch(`${API_BASE}/prompts/${encodeURIComponent(type)}`, {
        method: "DELETE",
        credentials: "include",
      }).then(async (r) => {
        if (!r.ok) {
          const text = await r.text().catch(() => "");
          throw new Error(text || `Reset failed: HTTP ${r.status}`);
        }
        return r.json();
      }),
  },

  /**
   * Skills API (P5+ Skill 市场). The community skill index is
   * embedded in the Go binary and exposed through /api/v1/skills.
   */
  skills: {
    /** List all skills, optionally filtered by category or search query. */
    list: (params?: { category?: string; q?: string }) => {
      const qs = new URLSearchParams();
      if (params?.category) qs.set("category", params.category);
      if (params?.q) qs.set("q", params.q);
      const q = qs.toString();
      return fetchAPI<{
        data: SkillItem[];
        meta: { total: number; registry: number };
      }>(`/skills${q ? "?" + q : ""}`);
    },
    /** Get a single skill by name. */
    get: (name: string) =>
      fetchAPI<SkillItem>(`/skills/${encodeURIComponent(name)}`),
    /** Get per-category skill counts. */
    stats: () =>
      fetchAPI<{
        total: number;
        categories: SkillCategoryStat[];
      }>("/skills/stats"),
  },

  /**
   * MCP API (P5+ MCP 协议). The settings/mcp page uses
   * listServers() to render the managed-server
   * catalogue, setStatus() / setAllow() to flip the
   * toggles, and listTools() to show the fan-out
   * tool inventory. The same surface is mirrored by
   * the CLI's `pentestswarm mcp serve --transport=sse`
   * so curl / Postman can drive it.
   */
  mcp: {
    listServers: () =>
      fetchAPI<{ count: number; servers: MCPServerInfo[] }>("/mcp/servers"),
    getServer: (name: string) =>
      fetchAPI<MCPServerInfo>(`/mcp/servers/${encodeURIComponent(name)}`),
    setStatus: (name: string, status: MCPServerStatus) =>
      fetch(`${API_BASE}/mcp/servers/${encodeURIComponent(name)}/status`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        credentials: "include",
        body: JSON.stringify({ status }),
      }).then(async (r) => {
        if (!r.ok) {
          throw new Error(`Set status failed: HTTP ${r.status}`);
        }
        return r.json();
      }),
    setAllow: (name: string, allow: string[]) =>
      fetch(`${API_BASE}/mcp/servers/${encodeURIComponent(name)}/allow`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        credentials: "include",
        body: JSON.stringify({ allow }),
      }).then(async (r) => {
        if (!r.ok) {
          throw new Error(`Set allow list failed: HTTP ${r.status}`);
        }
        return r.json();
      }),
    listTools: () =>
      fetchAPI<{ count: number; tools: MCPToolInfo[] }>("/mcp/tools"),
    invoke: (name: string, args: Record<string, unknown>, server?: string) =>
      fetch(`${API_BASE}/mcp/invoke`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "include",
        body: JSON.stringify({ name, arguments: args, server }),
      }).then(async (r) => {
        if (!r.ok) {
          const text = await r.text().catch(() => "");
          throw new Error(`MCP invoke failed: ${text || `HTTP ${r.status}`}`);
        }
        return r.json();
      }),
  },
};

/* ------------------------------------------------------------------ */
/* WebSocket client                                                     */
/* ------------------------------------------------------------------ */

export interface WSCallbacks {
  onEvent?: (event: CampaignEvent) => void;
  onFinding?: (finding: Finding) => void;
  /** P5+ 用户输入闭环 — operator-issued message broadcast. */
  onUserInput?: (msg: UserMessage) => void;
  /** Called when the server sends an initial snapshot of recent events. */
  onSnapshot?: (events: CampaignEvent[]) => void;
  onError?: (err: Error) => void;
  onClose?: () => void;
  onOpen?: () => void;
  /** Called after a reconnect to allow the caller to re-fetch missed data. */
  onReconnect?: () => void;
}

/**
 * Open a WebSocket to the live campaign stream. The server pushes
 * tagged-union messages (see WSMessage); the callback dispatch
 * routes by `kind` so callers don't have to repeat the discriminant
 * check.
 *
 * Returns a handle with .close() for unmount-time cleanup. The
 * underlying socket is auto-reconnected on transient errors with a
 * 1s → 30s exponential backoff (capped). Reconnects preserve the
 * callbacks; only the underlying connection is rebuilt.
 */
export function connectWebSocket(
  campaignId: string,
  cb: WSCallbacks = {}
): WebSocketHandle {
  const baseUrl = API_BASE.startsWith("/")
    ? `${window.location.origin}${API_BASE}`
    : API_BASE;
  const wsUrl = baseUrl.replace(/^http/, "ws") + `/campaigns/${campaignId}/ws`;
  return new WebSocketHandle(wsUrl, cb);
}

export class WebSocketHandle {
  private socket: WebSocket | null = null;
  private closed = false;
  private retryMs = 1000;
  private readonly maxRetryMs = 30_000;
  private wasConnected = false;

  constructor(
    private readonly url: string,
    private readonly cb: WSCallbacks
  ) {
    this.connect();
  }

  private connect(): void {
    if (this.closed) return;
    try {
      this.socket = new WebSocket(this.url);
    } catch (err) {
      this.handleError(err instanceof Error ? err : new Error(String(err)));
      this.scheduleReconnect();
      return;
    }

    this.socket.onopen = () => {
      this.retryMs = 1000;
      // If we were previously connected, this is a reconnect —
      // notify the caller so they can re-fetch missed data.
      if (this.wasConnected) {
        this.cb.onReconnect?.();
      }
      this.wasConnected = true;
      this.cb.onOpen?.();
    };
    this.socket.onmessage = (msg) => {
      try {
        const parsed = JSON.parse(msg.data) as WSMessage;
        if (parsed.kind === "event" && parsed.event) {
          this.cb.onEvent?.(parsed.event);
        } else if (parsed.kind === "finding" && parsed.finding) {
          this.cb.onFinding?.(parsed.finding);
        } else if (parsed.kind === "user_input" && parsed.user_input) {
          // P5+ 用户输入闭环 — fan user input messages out to
          // a dedicated callback. Default no-op so callers
          // that don't need this surface stay simple.
          this.cb.onUserInput?.(parsed.user_input);
        } else if (parsed.kind === "snapshot" && parsed.events) {
          // Initial snapshot of recent events sent on connect.
          this.cb.onSnapshot?.(parsed.events);
        }
        // Unknown kinds: drop silently — server may add new kinds
        // before the client learns about them.
      } catch (err) {
        this.cb.onError?.(
          err instanceof Error ? err : new Error("parse failed")
        );
      }
    };
    this.socket.onerror = () => {
      this.cb.onError?.(new Error("websocket error"));
    };
    this.socket.onclose = () => {
      this.cb.onClose?.();
      this.scheduleReconnect();
    };
  }

  private scheduleReconnect(): void {
    if (this.closed) return;
    const wait = this.retryMs;
    this.retryMs = Math.min(this.retryMs * 2, this.maxRetryMs);
    setTimeout(() => this.connect(), wait);
  }

  private handleError(err: Error): void {
    this.cb.onError?.(err);
  }

  close(): void {
    this.closed = true;
    if (this.socket) {
      this.socket.onclose = null;
      this.socket.close();
    }
  }
}
