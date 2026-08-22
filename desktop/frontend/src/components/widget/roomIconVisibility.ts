import type { DesktopIconItem } from "../../lib/bridge";

export const ROOM_ICON_VISIBILITY_KEY = "wg2.icon-widget-room-icons-visible";

export interface RoomIconStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

// Legacy users and corrupt values keep the existing visible behavior.
export function parseRoomIconVisibility(raw: string | null): boolean {
  if (raw === null) return true;
  try {
    const value: unknown = JSON.parse(raw);
    return typeof value === "boolean" ? value : true;
  } catch {
    return true;
  }
}

export function readRoomIconVisibility(storage: RoomIconStorage): boolean {
  return parseRoomIconVisibility(storage.getItem(ROOM_ICON_VISIBILITY_KEY));
}

// Let storage failures reach the manager so it can keep the old value visible
// and expose the same action as a safe retry entry.
export function writeRoomIconVisibility(storage: RoomIconStorage, visible: boolean): void {
  storage.setItem(ROOM_ICON_VISIBILITY_KEY, JSON.stringify(visible));
}

export function visibleDesktopIcons(items: DesktopIconItem[], showRooms: boolean): DesktopIconItem[] {
  return showRooms ? items : items.filter((item) => item.kind !== "room" || item.unreadCount > 0);
}
