// blockPopups is the once-per-revision reminder queue for blocked sessions.
// A `needs_input` / `needs_confirm` notice (Controller.PendingInteraction is
// the single source of truth; this module only mirrors it for popup timing)
// is queued the first time its revision appears. Consuming the queue opens the
// popup once; "稍后处理" or any close leaves the watermark at that revision,
// so the same notice does not re-pop. A changed pending interaction bumps the
// revision and re-queues the notice, keeping the reminder fresh without ever
// stealing focus repeatedly for the same block.
import type { DesktopIconItem, DesktopIconNotice } from "../../lib/bridge";

export interface BlockPopupCandidate {
  itemId: string;
  noticeId: string;
  revision: string;
  createdAt: number;
  priority: number;
}

export interface BlockPopupState {
  initialized: boolean;
  /** key: `${itemId}\u0000${noticeId}` → the revision already reminded. */
  watermarks: Record<string, string>;
  queue: BlockPopupCandidate[];
}

export function newBlockPopupState(): BlockPopupState {
  return { initialized: false, watermarks: {}, queue: [] };
}

export function isBlockingNotice(notice: DesktopIconNotice): boolean {
  return notice.kind === "needs_input" || notice.kind === "needs_confirm";
}

function blockKey(itemId: string, noticeId: string): string {
  return `${itemId}\u0000${noticeId}`;
}

function sortBlockPopupQueue(queue: BlockPopupCandidate[]): BlockPopupCandidate[] {
  return queue.sort(
    (left, right) =>
      left.priority - right.priority
      || left.createdAt - right.createdAt
      || left.itemId.localeCompare(right.itemId)
      || left.noticeId.localeCompare(right.noticeId),
  );
}

// reconcileBlockPopups consumes snapshots monotonically. The first real
// snapshot only establishes watermarks for sessions that are already blocked,
// so a widget restart never replays an old reminder (the block stays
// discoverable through the icon status/badge instead). Later snapshots queue a
// notice exactly once per revision; when the pending interaction changes the
// revision changes, so the reminder is re-raised for the new block.
export function reconcileBlockPopups(state: BlockPopupState, items: DesktopIconItem[]): BlockPopupState {
  const visible = new Set(items.map((item) => item.id));
  const watermarks = { ...state.watermarks };
  const queue = state.queue.filter((candidate) => visible.has(candidate.itemId));
  if (!state.initialized) {
    for (const item of items) {
      for (const notice of item.notifications) {
        if (!isBlockingNotice(notice)) continue;
        watermarks[blockKey(item.id, notice.id)] = notice.revision;
      }
    }
    return { initialized: true, watermarks, queue: [] };
  }

  for (const item of items) {
    for (const notice of item.notifications) {
      if (!isBlockingNotice(notice)) continue;
      const key = blockKey(item.id, notice.id);
      if (watermarks[key] === notice.revision) continue;
      watermarks[key] = notice.revision;
      const existing = queue.findIndex(
        (candidate) => candidate.itemId === item.id && candidate.noticeId === notice.id && candidate.revision === notice.revision,
      );
      if (existing < 0) {
        queue.push({
          itemId: item.id,
          noticeId: notice.id,
          revision: notice.revision,
          createdAt: notice.createdAt,
          priority: notice.priority,
        });
      }
    }
  }
  return { initialized: true, watermarks, queue: sortBlockPopupQueue(queue) };
}

export function consumeBlockPopup(state: BlockPopupState): { state: BlockPopupState; candidate?: BlockPopupCandidate } {
  const [candidate, ...queue] = state.queue;
  return { candidate, state: { ...state, queue } };
}
