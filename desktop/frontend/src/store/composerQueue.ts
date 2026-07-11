// composerQueue owns the Composer queue — an ordered list of pending messages
// that will be sent when the current run completes. Each item is keyed by a
// stable queueItemId. Duplicate IDs are treated as updates (add becomes upsert),
// so the common "re-send the same message" pattern doesn't create duplicates.
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
  /** The message content to send. */
  content: string;
  /** When this item was queued (epoch ms). */
  createdAt: number;
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
   * Reorder by moving the item at `fromIndex` to `toIndex` (in-place).
   * Clamped to valid range; no-op if indices are identical or out of bounds.
   */
  reorderItems: (fromIndex: number, toIndex: number) => void;

  /** Remove all queued items. */
  clearQueue: () => void;
};

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

  clearQueue: () => set({ items: [] }),
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
