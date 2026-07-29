import React, { type ReactNode } from 'react';

import type { WorkArchiveState, WorkState } from '../../work/types';

export interface WorkWorkspaceProps {
  name: string;
  state: WorkState;
  archiveState: WorkArchiveState;
  titleStatus?: ReactNode;
  status?: ReactNode;
  actions?: ReactNode;
  cornerstoneEntry?: ReactNode;
  cornerstoneCount?: number;
  addonPanel?: ReactNode;
  children: ReactNode;
}

/** Fixed shell around WorkCard. Its header, Cornerstone and AddOn nodes never
 * participate in face switching and therefore keep their component identity. */
export const WorkWorkspace: React.FC<WorkWorkspaceProps> = ({
  name,
  state,
  archiveState,
  titleStatus,
  status,
  actions,
  cornerstoneEntry,
  cornerstoneCount = 0,
  addonPanel,
  children,
}) => (
  <section
    className="wg2-work-card"
    data-testid="work-workspace"
    data-state={state}
    data-archive={archiveState}
    aria-label={`${name} Work`}
  >
    <header className="wg2-work-outer-header" data-testid="work-outer-header">
      <div className="wg2-work-outer-header-left">
        <h1 className="wg2-work-outer-title">{name}</h1>
        {archiveState === 'archived' && (
          <span className="wg2-work-outer-state">已归档</span>
        )}
        {titleStatus && (
          <div className="wg2-work-title-status" data-testid="work-title-status">
            {titleStatus}
          </div>
        )}
      </div>
      <div className="wg2-work-outer-header-right">
        {status}
        {actions}
      </div>
      <div className="wg2-work-cornerstone-entry" data-testid="work-cornerstone-entry">
        {cornerstoneEntry ?? (cornerstoneCount > 0 && (
          <span className="wg2-work-cornerstone-count">{cornerstoneCount} Cornerstone</span>
        ))}
      </div>
      <div className="wg2-work-addon-area" data-testid="work-addon-area">
        {addonPanel}
      </div>
    </header>
    {children}
  </section>
);
