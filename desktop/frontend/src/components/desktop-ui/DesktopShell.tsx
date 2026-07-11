// DesktopShell is the two-column Grid layout root for the Iris UI.
// Left: WorkspaceSidebar (264px fixed).  Right: SessionWorkspace (minmax(0, 1fr)).
//
// This is a pure presentational component — it does NOT subscribe to stores.
// It takes ready-to-render sidebar and workspace children so the parent (App)
// decides which data flows in.

import type { ReactNode } from "react";

export interface DesktopShellProps {
  sidebar: ReactNode;
  workspace: ReactNode;
}

/**
 * DesktopShell — the two-column Grid root.
 * - Sidebar: 264px fixed width with 1px border-right.
 * - Workspace: fills remaining space.
 */
export function DesktopShell({ sidebar, workspace }: DesktopShellProps) {
  return (
    <div className="desktop-shell layout--iris" role="main" aria-label="Desktop shell">
      {sidebar}
      {workspace}
    </div>
  );
}
