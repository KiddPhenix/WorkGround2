// SessionFooterDock — the bottom dock of SessionWorkspace.
// Stacks in order: DecisionSurface → ArtifactShelf → QueueTray → ComposerZone.
//
// Pure presentational — does NOT subscribe to stores.

import type { ReactNode } from "react";

export interface SessionFooterDockProps {
  /** DecisionSurface (AnswerCard / ApprovalCard / ClearContext) — conditionally rendered. */
  decisionSurface?: ReactNode;
  /** ArtifactShelf (64px permanent). */
  artifactShelf: ReactNode;
  /** QueueTray — conditionally rendered. */
  queueTray?: ReactNode;
  /** ComposerZone (176px: 128px editor + 48px config bar). */
  composerZone: ReactNode;
}

/**
 * SessionFooterDock — stacks the footer sections in spec order.
 */
export function SessionFooterDock({
  decisionSurface,
  artifactShelf,
  queueTray,
  composerZone,
}: SessionFooterDockProps) {
  return (
    <div className="session-footer-dock" role="region" aria-label="Session footer">
      {/* DecisionSurface is rendered outside the footer to keep it inline with conversation */}
      {decisionSurface}
      {artifactShelf}
      {queueTray}
      {composerZone}
    </div>
  );
}
