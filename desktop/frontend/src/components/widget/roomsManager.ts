import type { ProjectNode } from "../../lib/types";

// The Rooms management dialog reads its authoritative list from
// app.ListProjectTree(). It never builds or persists its own room state: every
// successful mutation reloads the backend tree, and the backend alone owns pin
// order and titles.
export interface RoomRow {
  topicId: string;
  label: string;
  pinned: boolean;
  scope: "global" | "project";
  workspaceRoot: string;
  sessionPath: string;
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
