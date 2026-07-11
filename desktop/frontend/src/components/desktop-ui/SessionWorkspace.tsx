// SessionWorkspace — the main content area (minmax(0, 1fr) column).
// Composes: SessionHeader (104px) → TaskMemoryBar (64px) →
//   ConversationViewport (flex: 1) → SessionFooterDock.
//
// Pure presentational — does NOT subscribe to stores.

import type { ReactNode } from "react";

export interface SessionWorkspaceProps {
  /** Session title shown in the 104px header. */
  title: string;
  /** Rendered TaskMemoryBar component. */
  memoryBar: ReactNode;
  /** Rendered Transcript / conversation component. */
  conversation: ReactNode;
  /** Rendered footer dock (DecisionSurface + ArtifactShelf + QueueTray + ComposerZone). */
  footer: ReactNode;
  /** Rendered AddOn launcher button / element. */
  addonLauncher?: ReactNode;
  /** Rendered more-menu button / element. */
  moreMenu?: ReactNode;
}

/**
 * SessionWorkspace — the main content column.
 * Stacks header, memory bar, conversation viewport, and footer dock vertically.
 */
export function SessionWorkspace({
  title,
  memoryBar,
  conversation,
  footer,
  addonLauncher,
  moreMenu,
}: SessionWorkspaceProps) {
  return (
    <section className="session-workspace" aria-label="Session workspace">
      {/* SessionHeader (104px) */}
      <header className="session-header">
        <h1 className="session-header__title" title={title}>
          {title}
        </h1>
        <div className="session-header__actions">
          {addonLauncher}
          {moreMenu}
        </div>
      </header>

      {/* TaskMemoryBar */}
      {memoryBar}

      {/* ConversationViewport */}
      <div className="conversation-viewport">
        {conversation}
      </div>

      {/* SessionFooterDock */}
      <div className="session-footer-dock">
        {footer}
      </div>
    </section>
  );
}
