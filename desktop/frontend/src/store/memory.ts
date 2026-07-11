// memory owns per-session memory lines — records keyed by stable sessionId,
// each holding a single goal/current/next-step value. All public actions and
// selectors require a session id so the store can hold independent values for
// multiple sessions simultaneously.

import { create } from "zustand";

// ── Types ───────────────────────────────────────────────────────────────────

export type MemoryLine = {
  /** What the session aims to achieve. */
  goal: string;
  /** What is currently being worked on. */
  current: string;
  /** The next planned step. */
  nextStep: string;
  goalSource?: string;
  currentSource?: string;
  nextStepSource?: string;
  revision?: number;
  updatedAt?: number;
  sessionKey?: string;
};

export type MemoryState = {
  memoryBySession: Record<string, MemoryLine>;
  revisionBySession: Record<string, number>;
  sessionKeyBySession: Record<string, string>;
};

export type MemoryActions = {
  /**
   * Set the memory line for a session. Pass null to clear (removes the entry).
   */
  setMemory: (sessionId: string, memory: MemoryLine | null) => void;

  /** Apply a backend revision, ignoring stale or duplicate delivery. */
  applyMemory: (sessionId: string, memory: MemoryLine) => void;

  /**
   * Clear the memory entry for one session without affecting others.
   */
  clearSessionMemory: (sessionId: string) => void;
};

function normalizeMemory(memory: MemoryLine): MemoryLine {
  return {
    ...memory,
    goal: typeof memory.goal === "string" ? memory.goal : "",
    current: typeof memory.current === "string" ? memory.current : "",
    nextStep: typeof memory.nextStep === "string" ? memory.nextStep : "",
  };
}

// ── Store ────────────────────────────────────────────────────────────────────

export const useMemoryStore = create<MemoryState & MemoryActions>((set) => ({
  memoryBySession: {},
  revisionBySession: {},
  sessionKeyBySession: {},

  setMemory: (sessionId, memory) =>
    set((s) => {
      if (memory === null) {
        if (!(sessionId in s.memoryBySession)) return s;
        const { [sessionId]: _, ...rest } = s.memoryBySession;
        return { memoryBySession: rest };
      }
      const normalized = normalizeMemory(memory);
      return {
        memoryBySession: { ...s.memoryBySession, [sessionId]: normalized },
      };
    }),

  applyMemory: (sessionId, memory) =>
    set((s) => {
      const normalized = normalizeMemory(memory);
      const existing = s.memoryBySession[sessionId];
      const incomingRevision = normalized.revision ?? 0;
      const existingRevision = s.revisionBySession?.[sessionId] ?? existing?.revision ?? 0;
      const existingSessionKey = s.sessionKeyBySession?.[sessionId] ?? existing?.sessionKey ?? "";
      const sameSession = !existingSessionKey || !normalized.sessionKey || existingSessionKey === normalized.sessionKey;
      if (sameSession && incomingRevision <= existingRevision) return s;
      const empty = !normalized.goal.trim() && !normalized.current.trim() && !normalized.nextStep.trim();
      if (empty) {
        const { [sessionId]: _, ...rest } = s.memoryBySession;
        return {
          memoryBySession: rest,
          revisionBySession: { ...(s.revisionBySession ?? {}), [sessionId]: incomingRevision },
          sessionKeyBySession: { ...(s.sessionKeyBySession ?? {}), [sessionId]: normalized.sessionKey ?? existingSessionKey },
        };
      }
      return {
        memoryBySession: { ...s.memoryBySession, [sessionId]: normalized },
        revisionBySession: { ...(s.revisionBySession ?? {}), [sessionId]: incomingRevision },
        sessionKeyBySession: { ...(s.sessionKeyBySession ?? {}), [sessionId]: normalized.sessionKey ?? existingSessionKey },
      };
    }),

  clearSessionMemory: (sessionId) =>
    set((s) => {
      if (!(sessionId in s.memoryBySession)) return s;
      const { [sessionId]: _, ...rest } = s.memoryBySession;
      return { memoryBySession: rest };
    }),
}));

// ── Selectors / Helpers ─────────────────────────────────────────────────────

/**
 * Read the memory line for a given session, or undefined if unset.
 */
export function selectMemory(
  memoryBySession: Record<string, MemoryLine>,
  sessionId: string,
): MemoryLine | undefined {
  return memoryBySession[sessionId];
}

/** True when the given session has a memory line set. */
export function selectMemoryExists(
  memoryBySession: Record<string, MemoryLine>,
  sessionId: string,
): boolean {
  return sessionId in memoryBySession;
}

/**
 * Format a MemoryLine into the standard display string:
 *   "目标：<goal> · 当前：<current> · 下一步：<nextStep>"
 */
export function formatMemoryLine(memory: MemoryLine): string {
  return `目标：${memory.goal} · 当前：${memory.current} · 下一步：${memory.nextStep}`;
}
