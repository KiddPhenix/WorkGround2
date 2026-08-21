import type { ProjectNode } from "../../lib/types";
import { projectIconKey, type ProjectIconKey } from "../../lib/projectIcons";

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
export function applyRoomPins(rows: RoomRow[], pinnedTopicIds: string[]): RoomRow[] {
  const byTopic = new Map(rows.map((row) => [row.topicId, row]));
  const pinned = new Set<string>();
  const ordered: RoomRow[] = [];
  for (const topicId of pinnedTopicIds) {
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
export function applyRoomIcons(rows: RoomRow[], icons: Record<string, string>): RoomRow[] {
  return rows.map((row) => ({ ...row, icon: projectIconKey(icons[row.topicId]) }));
}

// roomRows projects collaboration Rooms from the backend tree, walking it in
// tree order (global folder first, then each project in file order, pinned
// first inside each section). Only topic / global_topic nodes bound to a
// collaboration session with a real topicId and sessionPath qualify; crew, IM,
// Work and plain sessions never match. Duplicate topicIds collapse to the
// first occurrence so row identity matches the backend mutation key
// (RenameTopic / SetTopicPinned / TrashTopic are all topicId-keyed).
export function roomRows(tree: ProjectNode[]): RoomRow[] {
  const rows: RoomRow[] = [];
  const seen = new Set<string>();
  const walk = (nodes: ProjectNode[]) => {
    for (const node of nodes) {
      if ((node.kind === "topic" || node.kind === "global_topic") && node.sessionKind === "collaboration") {
        const topicId = node.topicId?.trim() ?? "";
        const sessionPath = node.sessionPath?.trim() ?? "";
        if (topicId && sessionPath && !seen.has(topicId)) {
          seen.add(topicId);
          const global = node.kind === "global_topic";
          rows.push({
            topicId,
            label: node.label || "Room",
            pinned: Boolean(node.pinned),
            icon: "",
            scope: global ? "global" : "project",
            workspaceRoot: global ? "" : (node.root ?? ""),
            sessionPath,
          });
        }
      }
      if (node.children?.length) walk(node.children);
    }
  };
  walk(tree);
  return rows;
}
