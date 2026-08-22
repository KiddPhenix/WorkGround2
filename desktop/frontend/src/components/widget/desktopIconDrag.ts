import type { DesktopIconItem } from "../../lib/bridge";

const ICON_SLOT_WIDTH = 68;

export function desktopIconDragOrder(startOrder: number, startX: number, clientX: number, count: number): number {
  if (count <= 1) return 0;
  const target = startOrder + Math.round((clientX - startX) / ICON_SLOT_WIDTH);
  return Math.max(0, Math.min(count - 1, target));
}

// Reorder only the dragged item's architectural zone. The backend remains the
// durable source of truth; this projection gives immediate insertion feedback
// until ApplyDesktopIconAction confirms (or rejects) the same target order.
export function previewDesktopIconMove(items: DesktopIconItem[], movedID: string, targetOrder: number): DesktopIconItem[] {
  const moved = items.find((item) => item.id === movedID);
  if (!moved) return items;
  const indices = items
    .map((item, index) => ({ item, index }))
    .filter(({ item }) => item.position.row === moved.position.row && item.position.zone === moved.position.zone)
    .map(({ index }) => index);
  if (indices.length <= 1) return items;
  const zone = indices.map((index) => items[index]);
  const source = zone.findIndex((item) => item.id === movedID);
  if (source < 0) return items;
  const [entry] = zone.splice(source, 1);
  zone.splice(Math.max(0, Math.min(zone.length, targetOrder)), 0, entry);
  const next = [...items];
  indices.forEach((index, order) => {
    const item = zone[order];
    next[index] = item.position.order === order ? item : { ...item, position: { ...item.position, order } };
  });
  return next;
}
