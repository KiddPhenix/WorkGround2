import type { DesktopIconItem } from "../../lib/bridge";
import { ROOM_PIN_LIMIT } from "./roomsManager";

// The Rooms dialog now exposes a desktop display count (0..ROOM_PIN_LIMIT)
// instead of the old boolean visibility switch. The new value lives under its
// own stable key so a stale legacy boolean can never overwrite a real count.
export const ROOM_ICON_COUNT_KEY = "wg2.icon-widget-room-icon-count";
export const LEGACY_ROOM_ICON_VISIBILITY_KEY = "wg2.icon-widget-room-icons-visible";

export interface RoomIconCountStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

// clampRoomIconCount keeps every entry point (initial read, write, and the
// pure item filter) on the same bounded range. Non-finite values fall back to
// the full default so a NaN from a corrupt store can never crash rendering.
export function clampRoomIconCount(value: number): number {
  if (!Number.isFinite(value)) return ROOM_PIN_LIMIT;
  return Math.min(ROOM_PIN_LIMIT, Math.max(0, Math.trunc(value)));
}

export function parseRoomIconCount(raw: string | null): number {
  if (raw === null) return ROOM_PIN_LIMIT;
  try {
    const value: unknown = JSON.parse(raw);
    return typeof value === "number" ? clampRoomIconCount(value) : ROOM_PIN_LIMIT;
  } catch {
    return ROOM_PIN_LIMIT;
  }
}

// migrateLegacyRoomIconCount maps the old boolean visibility value to the new
// count: hidden -> 0, visible/missing/corrupt -> the full default. It never
// mutates storage; the caller owns when and whether to persist the new key.
export function migrateLegacyRoomIconCount(raw: string | null): number {
  if (raw === null) return ROOM_PIN_LIMIT;
  try {
    const value: unknown = JSON.parse(raw);
    if (value === false) return 0;
    return ROOM_PIN_LIMIT;
  } catch {
    return ROOM_PIN_LIMIT;
  }
}

export function readRoomIconCount(storage: RoomIconCountStorage): number {
  const current = storage.getItem(ROOM_ICON_COUNT_KEY);
  if (current !== null) return parseRoomIconCount(current);
  return migrateLegacyRoomIconCount(storage.getItem(LEGACY_ROOM_ICON_VISIBILITY_KEY));
}

// Let storage failures reach the manager so it can keep the old value visible
// and expose the same action as a safe retry entry.
export function writeRoomIconCount(storage: RoomIconCountStorage, count: number): void {
  storage.setItem(ROOM_ICON_COUNT_KEY, JSON.stringify(clampRoomIconCount(count)));
}

// The authoritative snapshot already orders Rooms pinned-first, then fills the
// remaining durable slots in tree order, and finally appends every unread Room
// as overflow. The frontend only trims read Rooms beyond the chosen count; a
// live Room (unreadCount > 0) keeps showing until it is read and drops back
// under the same cap.
export function visibleDesktopIcons(items: DesktopIconItem[], roomCount: number): DesktopIconItem[] {
  const limit = clampRoomIconCount(roomCount);
  const visible: DesktopIconItem[] = [];
  let shownRooms = 0;
  for (const item of items) {
    if (item.kind !== "room") { visible.push(item); continue; }
    if (item.unreadCount > 0 || shownRooms < limit) {
      visible.push(item);
      shownRooms++;
    }
  }
  return visible;
}
