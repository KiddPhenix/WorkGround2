import { projectIconKey, type ProjectIconKey } from "../../lib/projectIcons";
import type { ProjectNode } from "../../lib/types";

// The workspace management dialog reads its authoritative list from
// app.ListProjectTree(). It never builds or persists its own workspace state:
// every successful mutation reloads the backend tree.
export interface WorkspaceRow {
  root: string;
  label: string;
  pinned: boolean;
  icon: ProjectIconKey;
}

// projectWorkspaceRows projects only the backend's project nodes. Global and
// folder nodes are excluded; a project without a root is skipped. The backend
// tree already owns ordering (pinned first, then project file order), so rows
// keep the tree order verbatim.
export function projectWorkspaceRows(tree: ProjectNode[]): WorkspaceRow[] {
  const rows: WorkspaceRow[] = [];
  for (const node of tree) {
    if (node.kind !== "project" || !node.root) continue;
    rows.push({
      root: node.root,
      label: node.label || node.root,
      pinned: Boolean(node.pinned),
      // projectIcon is a stable key (star/bookmark/code/terminal/bolt); an
      // unknown or empty value normalizes to "" so the row always renders the
      // folder fallback through the shared projectIconKey contract.
      icon: projectIconKey(node.projectIcon),
    });
  }
  return rows;
}

// deleteConfirmNext drives the two-step delete confirmation: the first click on
// a row arms it, the second click on the same row confirms (returns true), and
// clicking another row re-arms that row instead. A confirmed delete clears the
// armed state so the caller refreshes from the authoritative list.
export function deleteConfirmNext(current: string | null, root: string): { armed: string | null; confirmed: boolean } {
  if (current === root) return { armed: null, confirmed: true };
  return { armed: root, confirmed: false };
}

// renameTitle passes the raw input through unchanged after trimming. An empty
// title is deliberately kept empty: the backend clears the display override and
// restores the folder-name semantics, so the frontend never fabricates a
// fallback label.
export function renameTitle(raw: string): string {
  return raw.trim();
}
