import { projectIconKey, type ProjectIconKey } from "../../lib/projectIcons";
import { t } from "../../lib/i18n";

// The Rooms management dialog reads its authoritative list from
// app.ListProjectTree(). It never builds or persists its own room state: every
// successful mutation reloads the backend tree, and the backend alone owns pin
// order and titles.
export interface RoomRow {
  topicId: string;
  label: string;
  pinned: boolean;
  icon: ProjectIconKey;
  scope: "global" | "project";
  workspaceRoot: string;
  sessionPath: string;
}

export const ROOM_PIN_LIMIT = 7;

type BindingRecord = Record<string, unknown>;

function bindingRecord(value: unknown): BindingRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as BindingRecord
    : null;
}

function bindingArray(value: unknown, keys: string[], label: string): unknown[] {
  if (value == null) return [];
  if (Array.isArray(value)) return value;
  const record = bindingRecord(value);
  if (record) {
    for (const key of keys) {
      const nested = record[key];
      if (Array.isArray(nested)) return nested;
      if (key in record && nested == null) return [];
    }
  }
  throw new Error(t("desktopIcon.rooms.invalidBindingFormat", { name: label }));
}

// Wails serializes a nil Go slice as null. Older local builds also exposed the
// persisted state object instead of its topicIds array. Normalize both at the
// binding boundary so an empty pin preference never hides the Room tree.
export function normalizeRoomPins(value: unknown): string[] {
  const result: string[] = [];
  const seen = new Set<string>();
  for (const entry of bindingArray(value, ["topicIds", "TopicIDs", "pins", "Pins"], t("desktopIcon.rooms.pinSettings"))) {
    if (typeof entry !== "string") throw new Error(t("desktopIcon.rooms.invalidPinId"));
    const topicId = entry.trim();
    if (!topicId || seen.has(topicId)) continue;
    seen.add(topicId);
    result.push(topicId);
  }
  return result;
}

// Map payloads are normally plain objects. Accept the old {icons: {...}} state
// wrapper and nil maps, while rejecting malformed data explicitly; callers can
// then fall back to default glyphs without discarding the authoritative list.
export function normalizeRoomIcons(value: unknown): Record<string, string> {
  if (value == null) return {};
  let record = bindingRecord(value);
  if (!record) throw new Error(t("desktopIcon.rooms.invalidIconFormat"));
  const wrapped = bindingRecord(record.icons) ?? bindingRecord(record.Icons);
  if (wrapped) record = wrapped;
  const result: Record<string, string> = {};
  for (const [rawTopicId, rawIcon] of Object.entries(record)) {
    const topicId = rawTopicId.trim();
    if (!topicId) continue;
    if (typeof rawIcon !== "string") throw new Error(t("desktopIcon.rooms.invalidIconForTopic", { name: topicId }));
    result[topicId] = rawIcon;
  }
  return result;
}

export function pinnedRoomRows(rows: RoomRow[]): RoomRow[] {
  return rows.filter((row) => row.pinned).slice(0, ROOM_PIN_LIMIT);
}

export function roomPinsFull(rows: RoomRow[]): boolean {
  return rows.filter((row) => row.pinned).length >= ROOM_PIN_LIMIT;
}

// applyRoomPins joins the desktop-specific pin source onto the Room tree
// projection. It deliberately ignores ProjectNode.pinned, whose meaning is
// sidebar topic ordering. Pinned rows follow the persisted desktop pin order;
// all remaining Rooms retain their authoritative tree order.
export function applyRoomPins(rows: RoomRow[], pinnedTopicIds: unknown): RoomRow[] {
  const byTopic = new Map(rows.map((row) => [row.topicId, row]));
  const pinned = new Set<string>();
  const ordered: RoomRow[] = [];
  for (const topicId of normalizeRoomPins(pinnedTopicIds)) {
    if (pinned.size >= ROOM_PIN_LIMIT) break;
    const row = byTopic.get(topicId);
    if (!row || pinned.has(topicId)) continue;
    pinned.add(topicId);
    ordered.push({ ...row, pinned: true });
  }
  for (const row of rows) {
    if (!pinned.has(row.topicId)) ordered.push({ ...row, pinned: false });
  }
  return ordered;
}

// applyRoomIcons joins the independent desktop icon preferences without
// pruning preferences for topics that are temporarily absent from the tree.
export function applyRoomIcons(rows: RoomRow[], icons: unknown): RoomRow[] {
  const normalized = normalizeRoomIcons(icons);
  return rows.map((row) => ({ ...row, icon: projectIconKey(normalized[row.topicId]) }));
}

// roomRows projects collaboration Rooms from the backend tree, walking it in
// tree order (global folder first, then each project in file order, pinned
// first inside each section). Only topic / global_topic nodes bound to a
// collaboration session with a real topicId and sessionPath qualify; crew, IM,
// Work and plain sessions never match. Duplicate topicIds collapse to the
// first occurrence so row identity matches the backend mutation key
// (RenameTopic / SetTopicPinned / TrashTopic are all topicId-keyed).
export function roomRows(tree: unknown): RoomRow[] {
  const rows: RoomRow[] = [];
  const seen = new Set<string>();
  const walk = (value: unknown) => {
    const nodes = bindingArray(value, ["nodes", "Nodes", "items", "Items", "tree", "Tree", "projectTree", "ProjectTree"], t("desktopIcon.rooms.roomList"));
    for (const rawNode of nodes) {
      const node = bindingRecord(rawNode);
      if (!node) throw new Error(t("desktopIcon.rooms.invalidNode"));
      const kind = String(node.kind ?? node.Kind ?? "");
      const sessionKind = String(node.sessionKind ?? node.SessionKind ?? "");
      if ((kind === "topic" || kind === "global_topic") && sessionKind === "collaboration") {
        const topicId = String(node.topicId ?? node.TopicID ?? "").trim();
        const sessionPath = String(node.sessionPath ?? node.SessionPath ?? "").trim();
        if (topicId && sessionPath && !seen.has(topicId)) {
          seen.add(topicId);
          const global = kind === "global_topic";
          rows.push({
            topicId,
            label: String(node.label ?? node.Label ?? "") || "Room",
            pinned: Boolean(node.pinned ?? node.Pinned),
            icon: "",
            scope: global ? "global" : "project",
            workspaceRoot: global ? "" : String(node.root ?? node.Root ?? ""),
            sessionPath,
          });
        }
      }
      const children = node.children ?? node.Children;
      if (children != null) walk(children);
    }
  };
  walk(tree);
  return rows;
}
