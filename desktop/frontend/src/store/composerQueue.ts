// composerQueue owns the Composer queue — an ordered list of pending messages
// that will be sent once the owning session becomes safe/idle. Each item is
// scoped to a session (sessionId) and keyed by a stable queueItemId; the
// requestId is a stable idempotency key so a retried send does not duplicate.
//
// This store is only the state-model layer: no persistence, no backend calls,
// no component references.

import { create } from "zustand";

// ── Types ───────────────────────────────────────────────────────────────────

export type QueueItem = {
  /** Stable unique ID for the queue entry (e.g. a UUID or a server-assigned id). */
  queueItemId: string;
  /** Stable request idempotency key for safe retry. */
  requestId: string;
  /** Owning session/tab. Items are only drained from their own session. */
  sessionId?: string;
  /** The visible message content (what the user typed and sees in the tray). */
  content: string;
  /** Backend submit text (may include attachments/session context). Defaults to content. */
  submitText?: string;
  /** Retryable send-failure message; undefined while the item is healthy. */
  error?: string;
  /** When this item was queued (epoch ms). */
  createdAt: number;
};

export type QueueDrainGate = {
  /** The controller is ready to accept a turn. */
  ready: boolean;
  /** A foreground turn is still running. */
  running: boolean;
  /** An approval/ask/decision gate (or equivalent blocking UI action) is pending. */
  decisionPending: boolean;
};

export type ComposerQueueState = {
  items: QueueItem[];
};

export type ComposerQueueActions = {
  /**
   * Add an item to the queue. If an item with the same queueItemId already
   * exists, it is updated (upsert semantics) — no duplicates.
   * New items are appended at the end.
   */
  addItem: (item: QueueItem) => void;

  /**
   * Partially update a queued item by id. No-op if the id is not found.
   */
  updateItem: (queueItemId: string, partial: Partial<QueueItem>) => void;

  /**
   * Remove a queued item by id. Safe if the id is not found.
   */
  removeItem: (queueItemId: string) => void;

  /**
   * Reorder by moving the item at `fromIndex` to `toIndex` across the whole
   * list. Clamped to valid range; no-op if indices are identical or out of bounds.
   */
  reorderItems: (fromIndex: number, toIndex: number) => void;

  /**
   * Reorder within one session's queue, moving the item at the session-local
   * `fromIndex` to `toIndex`. Other sessions keep their relative order.
   */
  reorderSessionItems: (sessionId: string, fromIndex: number, toIndex: number) => void;

  /** Remove all queued items. */
  clearQueue: () => void;

  /** Remove every queued item belonging to a session. */
  clearSessionQueue: (sessionId: string) => void;
};

// ── Helpers ──────────────────────────────────────────────────────────────────

let queueItemSeq = 0;

function nextQueueItemId(): string {
  queueItemSeq += 1;
  return `q-${Date.now().toString(36)}-${queueItemSeq}`;
}

/** Build a QueueItem with stable ids, scoped to a session. */
export function makeQueueItem(input: {
  sessionId?: string;
  content: string;
  submitText?: string;
  queueItemId?: string;
  requestId?: string;
}): QueueItem {
  const queueItemId = input.queueItemId ?? nextQueueItemId();
  return {
    queueItemId,
    requestId: input.requestId ?? `req-${queueItemId}`,
    sessionId: input.sessionId ?? "",
    content: input.content,
    submitText: input.submitText,
    createdAt: Date.now(),
  };
}

/** True when the queue may drain: ready, idle, and no decision gate. */
export function canDrainQueue(gate: QueueDrainGate): boolean {
  return gate.ready && !gate.running && !gate.decisionPending;
}

// ── Store ────────────────────────────────────────────────────────────────────

export const useComposerQueueStore = create<
  ComposerQueueState & ComposerQueueActions
>((set) => ({
  items: [],

  addItem: (item) =>
    set((s) => {
      const idx = s.items.findIndex(
        (i) => i.queueItemId === item.queueItemId,
      );
      if (idx >= 0) {
        // Upsert: replace existing
        const next = [...s.items];
        next[idx] = item;
        return { items: next };
      }
      return { items: [...s.items, item] };
    }),

  updateItem: (queueItemId, partial) =>
    set((s) => {
      const idx = s.items.findIndex((i) => i.queueItemId === queueItemId);
      if (idx < 0) return s;
      const next = [...s.items];
      next[idx] = { ...next[idx], ...partial };
      return { items: next };
    }),

  removeItem: (queueItemId) =>
    set((s) => ({
      items: s.items.filter((i) => i.queueItemId !== queueItemId),
    })),

  reorderItems: (fromIndex, toIndex) =>
    set((s) => {
      if (fromIndex === toIndex) return s;
      if (
        fromIndex < 0 ||
        fromIndex >= s.items.length ||
        toIndex < 0 ||
        toIndex >= s.items.length
      )
        return s;
      const next = [...s.items];
      const [moved] = next.splice(fromIndex, 1);
      next.splice(toIndex, 0, moved);
      return { items: next };
    }),

  reorderSessionItems: (sessionId, fromIndex, toIndex) =>
    set((s) => {
      if (fromIndex === toIndex) return s;
      const sessionIds = s.items
        .map((item, index) => ({ item, index }))
        .filter((entry) => (entry.item.sessionId ?? "") === sessionId);
      if (
        fromIndex < 0 ||
        fromIndex >= sessionIds.length ||
        toIndex < 0 ||
        toIndex >= sessionIds.length
      )
        return s;
      const fromGlobal = sessionIds[fromIndex].index;
      const toGlobal = sessionIds[toIndex].index;
      const next = [...s.items];
      const [moved] = next.splice(fromGlobal, 1);
      next.splice(toGlobal, 0, moved);
      return { items: next };
    }),

  clearQueue: () => set({ items: [] }),

  clearSessionQueue: (sessionId) =>
    set((s) => ({
      items: s.items.filter((i) => i.sessionId !== sessionId),
    })),
}));

// ── Selectors ───────────────────────────────────────────────────────────────

/** Select a queue item by id. */
export function selectQueueItem(
  items: QueueItem[],
  queueItemId: string,
): QueueItem | undefined {
  return items.find((i) => i.queueItemId === queueItemId);
}

/** True when the queue has at least one item. */
export function selectQueueHasItems(items: QueueItem[]): boolean {
  return items.length > 0;
}

/** The first item in the queue (the next to be sent), or undefined. */
export function selectQueueHead(items: QueueItem[]): QueueItem | undefined {
  return items[0];
}

/** The subset of items belonging to one session, preserving FIFO order. */
export function selectItemsBySession(
  items: QueueItem[],
  sessionId: string,
): QueueItem[] {
  return items.filter((i) => (i.sessionId ?? "") === sessionId);
}

/** The FIFO head for one session, or undefined. */
export function selectSessionQueueHead(
  items: QueueItem[],
  sessionId: string,
): QueueItem | undefined {
  return items.find((i) => (i.sessionId ?? "") === sessionId);
}
