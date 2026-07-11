// WorkspaceSidebar — fixed 264px left panel.
// ProductBrand (logo + name), NewSessionAction, a slot for the project/session tree,
// and a SettingsEntry fixed at the bottom.
//
// Pure presentational — does NOT subscribe to stores.

import type { ReactNode } from "react";
import { SquarePen, Settings } from "lucide-react";
import logoWordmark from "../../assets/logo-wordmark.png";

export interface WorkspaceSidebarProps {
  /** Rendered tree content (ProjectTree or equivalent). */
  tree: ReactNode;
  onNewSession?: () => void;
  onOpenSettings?: () => void;
}

/**
 * WorkspaceSidebar — fixed 264px left panel with brand, new-session button,
 * tree slot, and settings entry.
 */
export function WorkspaceSidebar({
  tree,
  onNewSession,
  onOpenSettings,
}: WorkspaceSidebarProps) {
  return (
    <aside className="workspace-sidebar" aria-label="Workspace sidebar">
      {/* ProductBrand */}
      <div className="workspace-sidebar__brand">
        <img
          src={logoWordmark}
          alt="WorkGround2"
          className="workspace-sidebar__brand-logo"
          draggable={false}
        />
      </div>

      {/* NewSessionAction */}
      {onNewSession && (
        <button
          type="button"
          className="workspace-sidebar__new-session"
          aria-label="新建会话"
          onClick={onNewSession}
        >
          <SquarePen size={18} aria-hidden="true" />
          <span>新建会话</span>
        </button>
      )}

      {/* WorkspaceTree slot */}
      <div className="workspace-sidebar__tree">
        {tree}
      </div>

      {/* SettingsEntry */}
      {onOpenSettings && (
        <button
          type="button"
          className="workspace-sidebar__settings"
          aria-label="设置"
          onClick={onOpenSettings}
        >
          <Settings size={18} aria-hidden="true" />
          <span>设置</span>
        </button>
      )}
    </aside>
  );
}
