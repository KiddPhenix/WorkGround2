import React, { useCallback, useEffect, useState, type ReactNode } from 'react';

import type { RetryIntent, RetryStatus, RunSelection, Work } from '../../work/types';
import { RunProgressIndicator } from './RunProgressIndicator';

export interface RunProgressPopoverProps {
  work: Work;
  selection?: RunSelection;
  onSelect: (selection: RunSelection) => void;
  onRetry?: (intent: RetryIntent) => void | Promise<void>;
  retryByTarget: Record<string, RetryStatus>;
  readonly: boolean;
  archived: boolean;
  trigger: (controls: {
    open: boolean;
    panelId: string;
    pin: () => void;
  }) => ReactNode;
}

/** Header-anchored progress preview. Hover is transient; activating the status
 * entry pins the panel until the explicit close action is used. */
export const RunProgressPopover: React.FC<RunProgressPopoverProps> = ({
  work,
  selection,
  onSelect,
  onRetry,
  retryByTarget,
  readonly,
  archived,
  trigger,
}) => {
  const [hovered, setHovered] = useState(false);
  const [pinned, setPinned] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  const panelId = `work-${work.id}-run-progress`;
  const open = pinned || (hovered && !dismissed);

  useEffect(() => {
    setHovered(false);
    setPinned(false);
    setDismissed(false);
  }, [work.id]);

  const pin = useCallback(() => {
    setDismissed(false);
    setPinned(true);
  }, []);

  const close = useCallback(() => {
    setPinned(false);
    setHovered(false);
    setDismissed(true);
  }, []);

  return (
    <div
      className={`wg2-run-progress-popover${pinned ? ' wg2-run-progress-popover--pinned' : ''}`}
      data-testid="run-progress-popover"
      data-open={open ? 'true' : 'false'}
      data-pinned={pinned ? 'true' : 'false'}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => {
        setHovered(false);
        setDismissed(false);
      }}
    >
      {trigger({ open, panelId, pin })}
      <div
        id={panelId}
        className="wg2-run-progress-popover__panel"
        data-testid="run-progress-popover-panel"
        role="region"
        aria-label="运行进度"
        hidden={!open}
      >
        {pinned && (
          <div className="wg2-run-progress-popover__toolbar">
            <button
              type="button"
              className="wg2-run-progress-popover__close"
              data-testid="run-progress-close"
              aria-label="关闭运行进度"
              onClick={close}
            >
              ×
            </button>
          </div>
        )}
        <RunProgressIndicator
          work={work}
          selection={selection}
          onSelect={onSelect}
          onRetry={onRetry}
          retryByTarget={retryByTarget}
          readonly={readonly}
          archived={archived}
        />
      </div>
    </div>
  );
};
