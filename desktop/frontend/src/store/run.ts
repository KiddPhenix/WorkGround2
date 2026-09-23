// run owns the Run state — records keyed by runId, each containing an ordered
// list of streamed events. Events are merged idempotently by eventId so
// repeated/late deliveries from the backend don't duplicate or regress terminal
// runs. Expand/collapse is an explicit UI preference stored alongside the record.
//
// This store holds only the state-model layer: no persistence, no backend calls,
// no component references. Components subscribe via selectors and dispatch typed
// actions.

import { create } from "zustand";
import { forgetDetailBodies, resetDetailBodies } from "../lib/runDetailData";
import { forgetDetailScroll, resetDetailScrollMemory } from "../lib/runDetailScroll";

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

export type RunStepStatus = "running" | "completed" | "failed" | "stopped";

/**
 * Which wire phase produced or last updated an event. Kept on the record so a
 * late/duplicate delivery can be ordered without guessing: a `progress` chunk
 * is a partial preview and may never overwrite a `result`.
 */
export type RunEventPhase = "dispatch" | "progress" | "result" | "note";

export type RunEventKind =
  | "search"
  | "read"
  | "edit"
  | "command"
  | "test"
  | "browser"
  | "generic";

export type RunEvent = {
  eventId: string;
  /** Stable visual semantics assigned by the wire-event projection layer. */
  kind: RunEventKind;
  /**
   * Compact summary used by the miniature activity scene. It is a preview, not
   * the full log — the detail panel reads `output`/`error` for that.
   */
  content: string;
  /** Source tool metadata used by the compact activity scene. */
  toolName?: string;
  args?: string;
  /** Optional short label shown in step tabs, e.g. "已读 1 个文件" */
  stepLabel?: string;
  /** Status of this individual step; independent from the enclosing run. */
  status?: RunStepStatus;
  /** Wire phase that last wrote this event. */
  phase?: RunEventPhase;
  /**
   * Stable backend identity of the tool call. The only safe key for the
   * on-demand full-args/output lookup — never derive it from list position.
   */
  callId?: string;
  parentId?: string;
  /** Full tool output kept next to `content`, so a failure never hides it. */
  output?: string;
  /** Terminal error text, rendered separately from `output`. */
  error?: string;
  /** Unified diff reported by the tool itself, when it produced one. */
  diff?: string;
  added?: number;
  removed?: number;
  /** The backend head+tailed this output for display; it is not the whole log. */
  truncated?: boolean;
  /** Body was elided for memory; full args/output only via the on-demand load. */
  archived?: boolean;
  /** Wall-clock duration of the call, in milliseconds. */
  durationMs?: number;
  /** When this record was first observed (streaming elapsed time). */
  startedAt?: number;
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
  /** Right-side detail panel is open for this run. */
  detailOpen?: boolean;
  /** Selected record identity (stable eventId); undefined = follow latest. */
  detailSelectedEventId?: string;
  /** The user explicitly asked the detail panel to keep following the newest record. */
  detailFollowLatest?: boolean;
  /** Detail panel is maximized over the whole window. */
  detailMaximized?: boolean;
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

  /** Open or close the right-side detail panel. Closing keeps the selection. */
  setRunDetailOpen: (runId: string, open: boolean) => void;

  /**
   * Select a record in the detail panel by its stable eventId. Passing
   * undefined restores follow-latest and clears the explicit selection.
   */
  selectRunDetailEvent: (runId: string, eventId?: string) => void;

  /** Explicitly follow the newest record instead of holding the current one. */
  setRunDetailFollowLatest: (runId: string, follow: boolean) => void;

  /** Maximize / restore the detail panel. */
  setRunDetailMaximized: (runId: string, maximized: boolean) => void;

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
    detailFollowLatest: true,
  };
}

/** Fields the detail panel reads; compared to keep repeated merges a no-op. */
const EVENT_FIELDS: readonly (keyof RunEvent)[] = [
  "kind", "content", "toolName", "args", "stepLabel", "status", "phase", "callId",
  "output", "error", "diff", "added", "removed", "truncated", "archived", "durationMs", "parentId", "startedAt",
];

function sameEvent(a: RunEvent, b: RunEvent): boolean {
  return EVENT_FIELDS.every((field) => a[field] === b[field]);
}

/** Whether any run other than `runId` currently owns the detail panel. */
function noOtherPanelOpen(runs: Record<string, RunRecord>, runId: string): boolean {
  return Object.entries(runs).every(([id, record]) => id === runId || !record.detailOpen);
}

/**
 * Folds a re-delivered event into the record it already owns.
 *
 * Late and duplicate deliveries are normal (reconnect, replay, a tool finishing
 * after its turn reported done), so this must be order-independent:
 *  - a `result` is authoritative and is never overwritten by a later `dispatch`
 *    or `progress` preview of the same call;
 *  - a missing field from a thinner delivery never erases a known one;
 *  - the per-step status only ever moves forward out of `running`.
 *
 * Returns null when the delivery carries nothing new.
 */
function mergeEvent(previous: RunEvent, next: RunEvent): RunEvent | null {
  if (previous.phase === "result" && next.phase !== "result") {
    const enriched = {
      ...previous,
      args: previous.args ?? next.args,
      toolName: previous.toolName ?? next.toolName,
      callId: previous.callId ?? next.callId,
      parentId: previous.parentId ?? next.parentId,
      startedAt: previous.startedAt ?? next.startedAt,
      diff: previous.diff ?? next.diff,
    };
    return sameEvent(previous, enriched) ? null : enriched;
  }
  const merged: RunEvent = {
    ...previous,
    ...next,
    kind: next.kind ?? previous.kind,
    toolName: next.toolName ?? previous.toolName,
    args: next.args ?? previous.args,
    output: next.output ?? previous.output,
    error: next.error ?? previous.error,
    diff: next.diff ?? previous.diff,
    added: next.added ?? previous.added,
    removed: next.removed ?? previous.removed,
    callId: next.callId ?? previous.callId,
    durationMs: next.durationMs ?? previous.durationMs,
    startedAt: previous.startedAt ?? next.startedAt,
    truncated: next.truncated ?? previous.truncated,
    archived: next.archived ?? previous.archived,
    status: next.status ?? previous.status,
  };
  return sameEvent(previous, merged) ? null : merged;
}

// ── Store ────────────────────────────────────────────────────────────────────

export const useRunStore = create<RunState & RunActions>((set) => ({
  runs: {},

  mergeRunEvent: (runId, sessionId, turnId, event) =>
    set((s) => {
      const existing = s.runs[runId];
      if (!existing) {
        // New run
        return {
          runs: {
            ...s.runs,
            [runId]: createRunRecord(runId, sessionId, turnId, event),
          },
        };
      }
      // A finished run never gains new records — a late or duplicate delivery
      // must not reopen work that already reported done. Records it already
      // owns can still be completed by a late result (see mergeEvent).
      const terminal = isTerminalStatus(existing.status);
      const eventIndex = existing.events.findIndex((e) => e.eventId === event.eventId);
      if (eventIndex < 0) {
        if (terminal) return s;
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
      // Repeated delivery is idempotent. A matching eventId with new detail is
      // an in-place stream update (dispatch → progress → result), not a tab.
      const previous = existing.events[eventIndex];
      const merged = mergeEvent(previous, event);
      if (!merged) return s;
      const events = [...existing.events];
      events[eventIndex] = merged;
      return {
        runs: {
          ...s.runs,
          [runId]: { ...existing, events },
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
            ...(isTerminalStatus(status) ? { completedAt: now, expanded: false } : {}),
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

  setRunDetailOpen: (runId, open) =>
    set((s) => {
      const existing = s.runs[runId];
      if (!existing) return s;
      if (existing.detailOpen === open && (!open || noOtherPanelOpen(s.runs, runId))) return s;
      // The panel is pinned to the same right edge for every run, so exactly one
      // run owns it at a time: opening another run's panel closes the previous.
      const runs: typeof s.runs = {};
      for (const [id, record] of Object.entries(s.runs)) {
        if (id === runId) continue;
        runs[id] = record.detailOpen ? { ...record, detailOpen: false } : record;
      }
      const first = open && existing.detailOpen === undefined && existing.detailSelectedEventId === undefined;
      const initialEvent = first && existing.selectedStepIndex !== undefined
        ? existing.events[existing.selectedStepIndex]
        : undefined;
      runs[runId] = {
        ...existing,
        detailOpen: open,
        ...(first ? {
          detailFollowLatest: !initialEvent,
          detailSelectedEventId: initialEvent?.eventId,
        } : {}),
      };
      return { runs };
    }),

  selectRunDetailEvent: (runId, eventId) =>
    set((s) => {
      const existing = s.runs[runId];
      if (!existing) return s;
      const follow = eventId === undefined;
      const next: RunRecord = {
        ...existing,
        detailSelectedEventId: eventId,
        detailFollowLatest: follow,
        // Both views answer "which record am I reading", so releasing the
        // panel's pin releases the compact card's pinned step too.
        ...(follow ? { selectedStepIndex: undefined } : {}),
      };
      if (
        existing.detailSelectedEventId === next.detailSelectedEventId &&
        existing.detailFollowLatest === next.detailFollowLatest &&
        existing.selectedStepIndex === next.selectedStepIndex
      ) return s;
      return { runs: { ...s.runs, [runId]: next } };
    }),

  setRunDetailFollowLatest: (runId, follow) =>
    set((s) => {
      const existing = s.runs[runId];
      if (!existing || existing.detailFollowLatest === follow) return s;
      return {
        runs: {
          ...s.runs,
          [runId]: {
            ...existing,
            detailFollowLatest: follow,
            // Following again drops the pinned selection, so the newest record wins.
            ...(follow ? { detailSelectedEventId: undefined } : {}),
          },
        },
      };
    }),

  setRunDetailMaximized: (runId, maximized) =>
    set((s) => {
      const existing = s.runs[runId];
      if (!existing || existing.detailMaximized === maximized) return s;
      return {
        runs: { ...s.runs, [runId]: { ...existing, detailMaximized: maximized } },
      };
    }),

  clearRun: (runId) => {
    // A removed run must not leave its fetched bodies behind for a later run
    // that happens to reuse the same ids.
    forgetDetailBodies(runId);
    forgetDetailScroll(runId);
    set((s) => {
      if (!(runId in s.runs)) return s;
      const { [runId]: _, ...rest } = s.runs;
      return { runs: rest };
    });
  },

  clearAllRuns: () => {
    resetDetailBodies();
    resetDetailScrollMemory();
    set({ runs: {} });
  },
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
