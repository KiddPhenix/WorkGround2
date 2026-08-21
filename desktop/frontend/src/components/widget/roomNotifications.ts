import type { DesktopIconItem, DesktopIconNotice } from "../../lib/bridge";

export const ROOM_NOTIFICATION_MODE_KEY = "wg2.icon-widget-room-notification-mode";

export type RoomNotificationMode = "count" | "popup";
export type RoomAttention = NonNullable<DesktopIconNotice["attention"]>;

export interface RoomNotificationStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export interface RoomPopupCandidate {
  itemId: string;
  noticeId: string;
  sequence: number;
  createdAt: number;
  attention?: RoomAttention;
}

export interface RoomPopupState {
  initialized: boolean;
  watermarks: Record<string, number>;
  queue: RoomPopupCandidate[];
}

export function parseRoomNotificationMode(raw: string | null): RoomNotificationMode {
  if (raw === null) return "count";
  try {
    const value: unknown = JSON.parse(raw);
    return value === "popup" || value === "count" ? value : "count";
  } catch {
    return "count";
  }
}

export function readRoomNotificationMode(storage: RoomNotificationStorage): RoomNotificationMode {
  return parseRoomNotificationMode(storage.getItem(ROOM_NOTIFICATION_MODE_KEY));
}

export function writeRoomNotificationMode(storage: RoomNotificationStorage, mode: RoomNotificationMode): void {
  storage.setItem(ROOM_NOTIFICATION_MODE_KEY, JSON.stringify(mode));
}

export function newRoomPopupState(): RoomPopupState {
  return { initialized: false, watermarks: {}, queue: [] };
}

function roomSequence(item: DesktopIconItem): number {
  return Math.max(item.conversationSequence ?? 0, ...item.notifications.map((notice) => notice.readSequence ?? 0));
}

function attentionRank(attention?: RoomAttention): number {
  return attention ? 0 : 1;
}

function sortRoomPopupQueue(queue: RoomPopupCandidate[]): RoomPopupCandidate[] {
  return queue.sort((left, right) =>
    attentionRank(left.attention) - attentionRank(right.attention)
    || left.createdAt - right.createdAt
    || left.sequence - right.sequence
    || left.itemId.localeCompare(right.itemId));
}

// reconcileRoomPopups consumes snapshots monotonically. The first real
// snapshot only establishes per-Room sequence watermarks, so restart never
// replays historical unread messages. Count mode advances the same watermarks
// without queuing; switching to popup therefore starts with the next message.
export function reconcileRoomPopups(state: RoomPopupState, items: DesktopIconItem[], mode: RoomNotificationMode): RoomPopupState {
  const rooms = items.filter((item) => item.kind === "room");
  const watermarks = { ...state.watermarks };
  if (!state.initialized) {
    for (const item of rooms) watermarks[item.id] = roomSequence(item);
    return { initialized: true, watermarks, queue: [] };
  }

  const visible = new Set(rooms.map((item) => item.id));
  const queue = mode === "popup" ? state.queue.filter((candidate) => visible.has(candidate.itemId)) : [];
  for (const item of rooms) {
    const sequence = roomSequence(item);
    const previous = watermarks[item.id] ?? 0;
    watermarks[item.id] = Math.max(previous, sequence);
    if (mode !== "popup" || sequence <= previous) continue;
    for (const notice of item.notifications) {
      const noticeSequence = notice.readSequence ?? 0;
      if (notice.kind !== "message" || noticeSequence <= previous) continue;
      const candidate: RoomPopupCandidate = {
        itemId: item.id,
        noticeId: notice.id,
        sequence: noticeSequence,
        createdAt: notice.createdAt,
        attention: notice.attention,
      };
      const existing = queue.findIndex((queued) => queued.itemId === item.id && queued.noticeId === notice.id && queued.sequence === noticeSequence);
      if (existing < 0) queue.push(candidate);
    }
  }
  return { initialized: true, watermarks, queue: sortRoomPopupQueue(queue) };
}

export function consumeRoomPopup(state: RoomPopupState): { state: RoomPopupState; candidate?: RoomPopupCandidate } {
  const [candidate, ...queue] = state.queue;
  return { candidate, state: { ...state, queue } };
}

export function roomAttentionLabel(attention?: RoomAttention): string {
  if (attention === "mention_both") return "提到了你和你的 Agent";
  if (attention === "mention_agent") return "提到了你的 Agent";
  if (attention === "mention_member") return "提到了你";
  return "新消息";
}
