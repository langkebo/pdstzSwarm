import { create } from "zustand";
import type { Campaign, CampaignEvent, Finding, UserMessage } from "./api";
import { severityFromFinding } from "./severity";

const MAX_EVENTS = 500;
const MAX_FINDINGS = 2000;
const MAX_USER_INPUTS = 500;

export type AgentStatus = "idle" | "active" | "complete" | "error";

export type DashboardPanel = "surface" | "paths" | "mitre";

interface DashboardStore {
  // Campaigns
  campaigns: Campaign[];
  setCampaigns: (campaigns: Campaign[]) => void;
  activeCampaignId: string | null;
  setActiveCampaignId: (id: string | null) => void;

  // Events (from WebSocket, kind=event)
  events: CampaignEvent[];
  addEvent: (event: CampaignEvent) => void;
  setEvents: (events: CampaignEvent[]) => void;
  clearEvents: () => void;

  // Findings (from REST + WebSocket, kind=finding).
  // Deduplicated by finding ID — the same finding can come over both
  // the initial GET /campaigns/:id/findings and the live WS push.
  findings: Finding[];
  setFindings: (findings: Finding[]) => void;
  addFinding: (finding: Finding) => void;
  findingCount: (campaignId: string) => number;

  // User input log (P5+ 用户输入闭环). Same dedup-by-id story
  // as findings: a message can come over the initial GET
  // /campaigns/:id/user-inputs and over the live WS push. We
  // cap at MAX_USER_INPUTS so a 6-hour campaign with 1 msg/sec
  // doesn't grow without bound.
  userInputs: UserMessage[];
  setUserInputs: (msgs: UserMessage[]) => void;
  addUserInput: (msg: UserMessage) => void;
  clearUserInputs: () => void;

  // Per-severity tallies, derived. We store them on the store so
  // the dashboard donut can read them with a single selector
  // (avoiding a re-aggregate on every render).
  severityCounts: Record<string, number>;
  recomputeSeverityCounts: () => void;

  // Agent status
  agentStatuses: Record<string, AgentStatus>;
  setAgentStatus: (agent: string, status: AgentStatus) => void;

  // UI state
  selectedPanel: DashboardPanel;
  setSelectedPanel: (panel: DashboardPanel) => void;
}

function tally(findings: Finding[]): Record<string, number> {
  // Use severityFromFinding which respects the explicit severity field
  // from ClassifiedFindings, falling back to type-based inference.
  const counts: Record<string, number> = {
    critical: 0, high: 0, medium: 0, low: 0, informational: 0,
  };
  for (const f of findings) {
    const s = severityFromFinding(f) as string;
    counts[s] = (counts[s] ?? 0) + 1;
  }
  return counts;
}

export const useDashboardStore = create<DashboardStore>((set, get) => ({
  campaigns: [],
  setCampaigns: (campaigns) => set({ campaigns }),
  activeCampaignId: null,
  setActiveCampaignId: (id) => set({ activeCampaignId: id }),

  events: [],
  addEvent: (event) =>
    set((state) => {
      // Dedup by event id — the same event can arrive over both the
      // initial REST GET and the live WS push.
      // Skip dedup for zero-UUID events (legacy state machine events
      // that all shared the same zero ID) — always append them.
      if (event.id && event.id !== "00000000-0000-0000-0000-000000000000" && state.events.some((e) => e.id === event.id)) {
        return state;
      }
      // Cap to MAX_EVENTS to keep the panel responsive on long runs.
      const next = state.events.length >= MAX_EVENTS
        ? [...state.events.slice(-(MAX_EVENTS - 1)), event]
        : [...state.events, event];
      return { events: next };
    }),
  setEvents: (events) =>
    set((state) => {
      // Merge snapshot events with existing events, dedup by id.
      // Zero-UUID events are not deduplicated (legacy state machine
      // events all shared the same zero ID).
      const seen = new Set<string>();
      const merged: CampaignEvent[] = [];
      for (const e of [...state.events, ...events]) {
        if (e.id && e.id !== "00000000-0000-0000-0000-000000000000" && seen.has(e.id)) continue;
        if (e.id && e.id !== "00000000-0000-0000-0000-000000000000") seen.add(e.id);
        merged.push(e);
      }
      if (merged.length > MAX_EVENTS) {
        merged.splice(0, merged.length - MAX_EVENTS);
      }
      return { events: merged };
    }),
  clearEvents: () => set({ events: [] }),

  findings: [],
  setFindings: (findings) =>
    set((state) => {
      // Replace + dedup. We use the union of existing and incoming
      // IDs to handle the common case where the WS push races the
      // initial GET.
      const seen = new Set<string>();
      const merged: Finding[] = [];
      for (const f of [...state.findings, ...findings]) {
        if (seen.has(f.id)) continue;
        seen.add(f.id);
        merged.push(f);
      }
      if (merged.length > MAX_FINDINGS) {
        merged.splice(0, merged.length - MAX_FINDINGS);
      }
      return { findings: merged, severityCounts: tally(merged) };
    }),
  addFinding: (finding) =>
    set((state) => {
      if (state.findings.some((f) => f.id === finding.id)) {
        // Already present (initial GET raced the live push).
        return state;
      }
      const next = state.findings.length >= MAX_FINDINGS
        ? [...state.findings.slice(-(MAX_FINDINGS - 1)), finding]
        : [...state.findings, finding];
      return { findings: next, severityCounts: tally(next) };
    }),
  findingCount: (campaignId) =>
    get().findings.filter((f) => f.campaign_id === campaignId).length,

  userInputs: [],
  setUserInputs: (msgs) =>
    set(() => {
      // Replace path — used on initial GET. Dedup by id, keep
      // oldest→newest order, cap to MAX_USER_INPUTS.
      const seen = new Set<string>();
      const merged: UserMessage[] = [];
      for (const m of msgs) {
        if (!m.id || seen.has(m.id)) continue;
        seen.add(m.id);
        merged.push(m);
      }
      if (merged.length > MAX_USER_INPUTS) {
        merged.splice(0, merged.length - MAX_USER_INPUTS);
      }
      return { userInputs: merged };
    }),
  addUserInput: (msg) =>
    set((state) => {
      // WS push path — skip if we already have this id (initial
      // GET raced the live push).
      if (!msg.id) return state;
      if (state.userInputs.some((m) => m.id === msg.id)) return state;
      const next = state.userInputs.length >= MAX_USER_INPUTS
        ? [...state.userInputs.slice(-(MAX_USER_INPUTS - 1)), msg]
        : [...state.userInputs, msg];
      return { userInputs: next };
    }),
  clearUserInputs: () => set({ userInputs: [] }),

  severityCounts: { critical: 0, high: 0, medium: 0, low: 0, informational: 0 },
  recomputeSeverityCounts: () =>
    set((state) => ({ severityCounts: tally(state.findings) })),

  agentStatuses: {
    orchestrator: "idle",
    recon: "idle",
    classifier: "idle",
    exploit: "idle",
    report: "idle",
  },
  setAgentStatus: (agent, status) =>
    set((state) => ({
      agentStatuses: { ...state.agentStatuses, [agent]: status },
    })),

  selectedPanel: "surface",
  setSelectedPanel: (panel) => set({ selectedPanel: panel }),
}));
