// run owns the Run state — records keyed by runId, each containing an ordered
// list of streamed events. Events are merged idempotently by eventId so
// repeated/late deliveries from the backend don't duplicate or regress terminal
// runs. Expand/collapse is an explicit UI preference stored alongside the record.
//
// This store holds only the state-model layer: no persistence, no backend calls,
// no component references. Components subscribe via selectors and dispatch typed
// actions.

import { create } from "zustand";

// ── Types ───────────────────────────────────────────────────────────────────

export type RunStatus =
  | "queued"
  | "running"
  | "waiting_user"
  | "reconnecting"
  | "completed"
  | "failed"
  | "cancelled";

export const TERMINAL_STATUSES: ReadonlySet<RunStatus> = new Set<RunStatus>([
  "completed",
  "failed",
  "cancelled",
]);

export function isTerminalStatus(status: RunStatus): boolean {
  return TERMINAL_STATUSES.has(status);
}

export type RunStepStatus = "running" | "completed" | "failed";

export type RunEvent = {
  eventId: string;
  content: string;
  /** Optional short label shown in step tabs, e.g. "已读 1 个文件" */
  stepLabel?: string;
  /** Status of this individual step; independent from the enclosing run. */
  status?: RunStepStatus;
};

export type RunRecord = {
  runId: string;
  sessionId: string;
  turnId: string;
  status: RunStatus;
  events: RunEvent[];
  /** Whether the run's detail view is expanded */
  expanded: boolean;
  /** 0-based selected step tab index; undefined = auto-follow latest */
  selectedStepIndex?: number;
  startedAt: number;
  completedAt?: number;
  errorMessage?: string;
};

export type RunState = {
  runs: Record<string, RunRecord>;
};

export type RunActions = {
  /**
   * Merge a streamed event into the run identified by `runId`.
   * Idempotent by eventId: repeating the exact same event is a no-op.
   * If the run is in a terminal status (completed/failed/cancelled) the event
   * is silently dropped — terminal runs never regress.
   */
  mergeRunEvent: (
    runId: string,
    sessionId: string,
    turnId: string,
    event: RunEvent,
  ) => void;

  /**
   * Set the run's status directly. Terminal guard applies: once a run is
   * completed/failed/cancelled, further status transitions are ignored.
   */
  setRunStatus: (
    runId: string,
    status: RunStatus,
    meta?: { errorMessage?: string },
  ) => void;

  /** Toggle the run's expanded/collapsed state. Independent of status. */
  setRunExpanded: (runId: string, expanded: boolean) => void;

  /** Collapse every run that belongs to a session before a newer run opens. */
  collapseSessionRuns: (sessionId: string) => void;

  /**
   * Set the selected step tab index (0-based). Undefined = auto-follow latest.
   * Also resets selection when the user clicks the last tab while it was already
   * selected, restoring auto-follow.
   */
  setRunSelectedStep: (runId: string, stepIndex?: number) => void;

  /** Remove a single run record. Safe to call on missing runId. */
  clearRun: (runId: string) => void;

  /** Remove all run records. */
  clearAllRuns: () => void;
};

// ── Helpers ─────────────────────────────────────────────────────────────────

function createRunRecord(
  runId: string,
  sessionId: string,
  turnId: string,
  event: RunEvent,
): RunRecord {
  return {
    runId,
    sessionId,
    turnId,
    status: "running",
    events: [event],
    expanded: true,
    startedAt: Date.now(),
  };
}

// ── Store ────────────────────────────────────────────────────────────────────

export const useRunStore = create<RunState & RunActions>((set) => ({
  runs: {},

  mergeRunEvent: (runId, sessionId, turnId, event) =>
    set((s) => {
      const existing = s.runs[runId];
      if (existing) {
        // Terminal guard: don't regress
        if (isTerminalStatus(existing.status)) return s;
        // Repeated delivery is idempotent. A matching eventId with new content
        // is an in-place stream update (dispatch → progress → result), not a tab.
        const eventIndex = existing.events.findIndex((e) => e.eventId === event.eventId);
        if (eventIndex >= 0) {
          const previous = existing.events[eventIndex];
          if (previous.content === event.content && previous.stepLabel === event.stepLabel && previous.status === event.status) return s;
          const events = [...existing.events];
          events[eventIndex] = event;
          return {
            runs: {
              ...s.runs,
              [runId]: { ...existing, events },
            },
          };
        }
        // Auto-advance selectedStepIndex when following latest
        const autoFollow = existing.selectedStepIndex === undefined || existing.selectedStepIndex === existing.events.length - 1;
        return {
          runs: {
            ...s.runs,
            [runId]: {
              ...existing,
              events: [...existing.events, event],
              ...(autoFollow ? { selectedStepIndex: existing.events.length } : {}),
            },
          },
        };
      }
      // New run
      return {
        runs: {
          ...s.runs,
          [runId]: createRunRecord(runId, sessionId, turnId, event),
        },
      };
    }),

  setRunStatus: (runId, status, meta) =>
    set((s) => {
      const existing = s.runs[runId];
      if (!existing) return s;
      // Terminal guard: don't regress
      if (isTerminalStatus(existing.status)) return s;
      // If transitioning into terminal, record completion time
      const now = Date.now();
      return {
        runs: {
          ...s.runs,
          [runId]: {
            ...existing,
            status,
            ...(isTerminalStatus(status) ? { completedAt: now } : {}),
            ...(meta?.errorMessage ? { errorMessage: meta.errorMessage } : {}),
          },
        },
      };
    }),

  setRunExpanded: (runId, expanded) =>
    set((s) => {
      const existing = s.runs[runId];
      if (!existing) return s;
      return {
        runs: { ...s.runs, [runId]: { ...existing, expanded } },
      };
    }),

  collapseSessionRuns: (sessionId) =>
    set((s) => {
      let changed = false;
      const runs = { ...s.runs };
      for (const [runId, run] of Object.entries(runs)) {
        if (run.sessionId !== sessionId || !run.expanded) continue;
        runs[runId] = { ...run, expanded: false };
        changed = true;
      }
      return changed ? { runs } : s;
    }),

  setRunSelectedStep: (runId, stepIndex) =>
    set((s) => {
      const existing = s.runs[runId];
      if (!existing) return s;
      return {
        runs: { ...s.runs, [runId]: { ...existing, selectedStepIndex: stepIndex } },
      };
    }),

  clearRun: (runId) =>
    set((s) => {
      if (!(runId in s.runs)) return s;
      const { [runId]: _, ...rest } = s.runs;
      return { runs: rest };
    }),

  clearAllRuns: () => set({ runs: {} }),
}));

// ── Selectors ───────────────────────────────────────────────────────────────

/** Select a single run record by runId. */
export function selectRun(
  runs: Record<string, RunRecord>,
  runId: string,
): RunRecord | undefined {
  return runs[runId];
}

/** Select all events for a run, or empty array if the run doesn't exist. */
export function selectRunEvents(
  runs: Record<string, RunRecord>,
  runId: string,
): RunEvent[] {
  return runs[runId]?.events ?? [];
}

/** Select all runIds whose status matches one of the given set. */
export function selectRunsByStatus(
  runs: Record<string, RunRecord>,
  ...statuses: RunStatus[]
): string[] {
  const set = new Set(statuses);
  return Object.keys(runs).filter((id) => set.has(runs[id].status));
}

/** Count runs by status. */
export function selectRunCountByStatus(
  runs: Record<string, RunRecord>,
  ...statuses: RunStatus[]
): number {
  return selectRunsByStatus(runs, ...statuses).length;
}
